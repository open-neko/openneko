package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

var errThreadGone = errors.New("Thread deleted before run start.")

type turnOptions struct {
	OrgID, ThreadID, RunID, Message string
	Channel                         string
	IncludeUxMetadata               bool
	NativeDelegationOff             bool
	Now                             func() time.Time
	Summarize                       func(ctx context.Context, orgID, prior, transcript string) (string, error)
}

type threadCompaction struct {
	Summary          string `json:"summary"`
	ThroughMessageID string `json:"throughMessageId"`
	UpdatedAt        string `json:"updatedAt"`
	Version          int    `json:"version"`
}

type runActorRow struct {
	UserID, Role string
}

// turnContext is what runChatTurn() in packages/llm/src/work/run-chat-turn.ts
// holds before it starts the agent. AllowedSkills is nil when the run may
// load every skill. Compaction is set only when this turn folded the
// transcript.
type turnContext struct {
	Prompt        string
	Backend       string
	Workspace     agentWorkspace
	Actor         runActorRow
	AllowedSkills []string
	HeldActions   heldSet
	Compaction    *threadCompaction
}

type transcriptMessage struct {
	ID, Role, Content string
}

func (a *API) loadTurnContext(ctx context.Context, o turnOptions) (*turnContext, error) {
	channel := orDefault(o.Channel, "web")
	messages, state, ok, err := a.turnTranscript(ctx, o.OrgID, o.ThreadID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errThreadGone
	}
	var bot *threadBot
	if channel == "web" {
		if bot, err = a.threadBotFor(ctx, o.OrgID, o.ThreadID); err != nil {
			return nil, err
		}
	}
	ws, err := turnWorkspace(o.OrgID, o.ThreadID, o.RunID)
	if err != nil {
		return nil, err
	}
	knowledge := readKnowledgePack(ws.KnowledgeRoot)
	act, err := a.runActor(ctx, o.RunID)
	if err != nil {
		return nil, err
	}
	sourceConfig := false
	if act.Role == "admin" && channel == "web" {
		if sourceConfig, err = a.sourceConfigEnabled(ctx, o.OrgID); err != nil {
			return nil, err
		}
	}

	var prior *threadCompaction
	var stored struct {
		Compaction *threadCompaction `json:"compaction"`
	}
	if json.Unmarshal(state, &stored) == nil {
		prior = stored.Compaction
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	summarize := o.Summarize
	if summarize == nil {
		summarize = a.compactSummary
	}
	summary, kept, compaction, err := compactTranscript(ctx, o.OrgID, messages, prior, jsTime(now()), summarize)
	if err != nil {
		return nil, err
	}

	ent, err := a.runEntitlement(ctx, o.OrgID, act)
	if err != nil {
		return nil, err
	}
	heldSkills, err := a.heldItems(ctx, ent, "skill")
	if err != nil {
		return nil, err
	}
	team, err := a.heldItems(ctx, ent, "team_memory")
	if err != nil {
		return nil, err
	}
	actions, err := a.heldItems(ctx, ent, "action")
	if err != nil {
		return nil, err
	}
	scope := memoryScope{OrgID: o.OrgID, ThreadID: o.ThreadID, RunID: o.RunID, Team: heldList(team)}
	if act.Role == "member" {
		scope.Layer = act.UserID
	}
	memory, err := a.memoryPromptContext(ctx, scope, o.Message)
	if err != nil {
		return nil, err
	}
	profile, err := a.operatorProfile(ctx, o.OrgID, act.UserID)
	if err != nil {
		return nil, err
	}
	terminal, err := a.orgTerminal(ctx, o.OrgID)
	if err != nil {
		return nil, err
	}

	available := listInstalledSkills(ws.SkillsRoot)
	names := make([]string, len(available))
	for i, s := range available {
		names[i] = s.Name
	}
	allowed := runSkillScope(heldList(heldSkills), names, sourceConfig)
	var skills []installedSkill
	for _, s := range available {
		if (allowed == nil || slices.Contains(allowed, s.Name)) && (!sandboxSkills[s.Name] || terminal) {
			skills = append(skills, s)
		}
	}

	prompt := make([]promptMessage, len(kept))
	for i, m := range kept {
		prompt[i] = promptMessage{Role: m.Role, Content: m.Content}
	}
	wantsCards := channel == "web" || channel == "telegram"
	return &turnContext{
		Prompt: buildWorkPrompt(workPromptInput{
			Backend:                   "ax",
			Workspace:                 ws,
			Knowledge:                 knowledge,
			Messages:                  prompt,
			CurrentUserMessage:        o.Message,
			PriorSummary:              summary,
			MemoryContext:             memory,
			OperatorProfile:           operatorProfileSection(profile),
			InstalledSkills:           skills,
			WantsCards:                wantsCards,
			IncludeUxMetadata:         o.IncludeUxMetadata,
			SupportsCardTool:          true,
			RichCards:                 wantsCards && channel == "web",
			SupportsSkillTool:         true,
			SupportsMemoryTool:        true,
			SupportsWorkflowTool:      true,
			SupportsPolicyTool:        true,
			SupportsSourceConfigTool:  sourceConfig,
			SupportsClarificationTool: true,
			SupportsNativeDelegation:  !o.NativeDelegationOff,
			InlineTranscript:          true,
			Bot:                       bot,
			FilesEnabled:              strings.TrimSpace(os.Getenv("OPENNEKO_SERVE_URL")) != "",
		}),
		Backend:       "ax",
		Workspace:     ws,
		Actor:         act,
		AllowedSkills: allowed,
		HeldActions:   actions,
		Compaction:    compaction,
	}, nil
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func (a *API) turnTranscript(ctx context.Context, orgID, threadID string) ([]transcriptMessage, []byte, bool, error) {
	if !isUUID(threadID) {
		return nil, nil, false, nil
	}
	var state []byte
	err := a.DB.QueryRow(ctx, `select backend_state from work_thread where org_id = $1 and id = $2`, orgID, threadID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	rows, err := a.DB.Query(ctx, `select id::text, role, content from work_message
		where org_id = $1 and thread_id = $2 order by created_at`, orgID, threadID)
	if err != nil {
		return nil, nil, false, err
	}
	messages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (transcriptMessage, error) {
		var m transcriptMessage
		return m, row.Scan(&m.ID, &m.Role, &m.Content)
	})
	return messages, state, true, err
}

func (a *API) threadBotFor(ctx context.Context, orgID, threadID string) (*threadBot, error) {
	var b threadBot
	err := a.DB.QueryRow(ctx, `select name, instructions from bot where org_id = $1 and thread_id = $2 limit 1`,
		orgID, threadID).Scan(&b.Name, &b.Instructions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &b, err
}

func (a *API) runActor(ctx context.Context, runID string) (runActorRow, error) {
	var user, role *string
	err := a.DB.QueryRow(ctx, `select actor_user_id, actor_role from work_run where id::text = $1 limit 1`, runID).Scan(&user, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return runActorRow{}, nil
	}
	return runActorRow{UserID: deref(user), Role: deref(role)}, err
}

// runEntitlement is runEntitlementActor() in
// packages/llm/src/work/entitlement-scope.ts for a chat run. A run with a
// user holds the user's items; a run without one is a service run that
// holds everything, unless a member role marks an unlinked channel sender,
// who holds nothing.
func (a *API) runEntitlement(ctx context.Context, orgID string, act runActorRow) (actor, error) {
	if act.UserID != "" {
		role, err := a.role(ctx, orgID, act.UserID)
		return actor{OrgID: orgID, UserID: act.UserID, Admin: role == "admin"}, err
	}
	if act.Role != "" && act.Role != "service" && act.Role != "admin" {
		return actor{OrgID: orgID}, nil
	}
	return actor{OrgID: orgID, Admin: true}, nil
}

func heldList(h heldSet) []string {
	if h.All {
		return nil
	}
	out := make([]string, 0, len(h.IDs))
	for id := range h.IDs {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// runSkillScope is runSkillScope() in packages/llm/src/work/admin-skills.ts.
func runSkillScope(held, installed []string, admin bool) []string {
	if admin {
		return held
	}
	list := held
	if list == nil {
		list = installed
	}
	out := []string{}
	for _, name := range list {
		if name != "graphjin-config" {
			out = append(out, name)
		}
	}
	return out
}

func (a *API) orgTerminal(ctx context.Context, orgID string) (bool, error) {
	if strings.TrimSpace(os.Getenv("OPENNEKO_SANDBOX_URL")) == "" {
		return false, nil
	}
	var raw []byte
	err := a.DB.QueryRow(ctx, `select config from llm_provider_config where org_id = $1 and scope = 'agent' limit 1`, orgID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	return cfg["terminal"] != false, nil
}

var unsafeSegmentPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// turnWorkspace is ensureWorkWorkspace() in packages/llm/src/work/workspace.ts.
func turnWorkspace(orgID, threadID, runID string) (agentWorkspace, error) {
	r, err := orgWorkspace(orgID)
	if err != nil {
		return agentWorkspace{}, err
	}
	run := filepath.Join(r.Runs, unsafeSegmentPattern.ReplaceAllString(runID, "_"))
	ws := agentWorkspace{
		OrgRoot: r.Org, SkillsRoot: r.Skills, MemoryRoot: r.Memory, KnowledgeRoot: r.Knowledge,
		UploadsRoot: r.Uploads, RunsRoot: r.Runs,
		ThreadUploadsRoot: filepath.Join(r.Uploads, unsafeSegmentPattern.ReplaceAllString(threadID, "_")),
		RunRoot:           run, ArtifactRoot: filepath.Join(run, "artifacts"), BinRoot: filepath.Join(run, "bin"),
	}
	for _, dir := range []string{ws.ThreadUploadsRoot, ws.RunRoot, ws.ArtifactRoot, ws.BinRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ws, err
		}
	}
	return ws, nil
}

var knowledgeSnapshotFiles = []string{"tables.json", "namespaces.json", "insights.json", "syntax.json", "INDEX.md", "mode.json"}

// readKnowledgePack is readKnowledgePack() in packages/llm/src/knowledge-pack.ts:
// the snapshot when it is whole, else the loose files.
func readKnowledgePack(root string) knowledgePack {
	files := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(root, ".knowledge-snapshot.json")); err == nil {
		var snap struct {
			Format   int                        `json:"format"`
			Source   *string                    `json:"source"`
			Revision *string                    `json:"revision"`
			Files    map[string]json.RawMessage `json:"files"`
		}
		if json.Unmarshal(raw, &snap) == nil && snap.Format == 1 && snap.Source != nil && snap.Revision != nil {
			for _, name := range knowledgeSnapshotFiles {
				var text string
				if json.Unmarshal(snap.Files[name], &text) != nil ||
					strings.HasSuffix(name, ".json") && !json.Valid([]byte(text)) {
					files = map[string]string{}
					break
				}
				files[name] = text
			}
		}
	}
	read := func(name string) string {
		if text, ok := files[name]; ok {
			return text
		}
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return "{}\n"
		}
		return string(raw)
	}
	k := knowledgePack{Mode: "legacy", Tables: read("tables.json"), Namespaces: read("namespaces.json"),
		Insights: read("insights.json"), Syntax: read("syntax.json")}
	var mode struct {
		Mode any `json:"mode"`
	}
	if json.Unmarshal([]byte(read("mode.json")), &mode) == nil && mode.Mode == "agentic" {
		k.Mode = "agentic"
	}
	return k
}

var (
	installedFrontmatter = regexp.MustCompile(`^---\s*\n((?s:.*?))\n---`)
	installedDescription = regexp.MustCompile(`(?m)^description:\s*([^\r\n\x{2028}\x{2029}]+)`)
	compactAfterValue    = envCount("OPENNEKO_COMPACT_AFTER_MESSAGES", 40)
	keepLastValue        = envCount("OPENNEKO_COMPACT_KEEP_LAST", 12)
)

// listInstalledSkills is listInstalledSkills() in packages/llm/src/work/workspace.ts.
func listInstalledSkills(root string) []installedSkill {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	var out []installedSkill
	for _, name := range names {
		md, err := os.ReadFile(filepath.Join(root, name, "SKILL.md"))
		if err != nil {
			continue
		}
		fm := installedFrontmatter.FindStringSubmatch(string(md))
		if fm == nil {
			continue
		}
		desc := ""
		if d := installedDescription.FindStringSubmatch(fm[1]); d != nil {
			desc = jsTrim(d[1])
		}
		out = append(out, installedSkill{Name: name, Description: desc})
	}
	return out
}

func envCount(name string, fallback int) int {
	n, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64)
	if err != nil || n == 0 || n != n {
		return fallback
	}
	return int(n)
}

// compactTranscript is compactIfNeeded() in
// packages/llm/src/work/compact-transcript.ts.
func compactTranscript(ctx context.Context, orgID string, messages []transcriptMessage, prior *threadCompaction, now string,
	summarize func(ctx context.Context, orgID, prior, transcript string) (string, error)) (string, []transcriptMessage, *threadCompaction, error) {
	found := -1
	if prior != nil {
		found = slices.IndexFunc(messages, func(m transcriptMessage) bool { return m.ID == prior.ThroughMessageID })
	}
	if found < 0 {
		prior = nil
	}
	pending := messages[found+1:]
	if len(pending) <= compactAfterValue {
		if prior == nil {
			return "", pending, nil, nil
		}
		return prior.Summary, pending, nil, nil
	}
	fold, kept := pending[:len(pending)-keepLastValue], pending[len(pending)-keepLastValue:]
	turns := make([]string, len(fold))
	for i, m := range fold {
		turns[i] = pick(m.Role == "user", "Operator", "Agent") + ": " + m.Content
	}
	previous := ""
	if prior != nil {
		previous = prior.Summary
	}
	summary, err := summarize(ctx, orgID, previous, strings.Join(turns, "\n\n"))
	if err != nil {
		return "", nil, nil, err
	}
	return summary, kept, &threadCompaction{Summary: summary, ThroughMessageID: fold[len(fold)-1].ID, UpdatedAt: now, Version: 1}, nil
}

var compactorProgram = llm.Program{
	Signature:   `priorSummary?:string "summary so far, absent on the first fold", transcript:string "older conversation turns to fold in" -> summary:string "dense factual summary, at most 1600 characters"`,
	Instruction: "You compact an OpenNeko analyst conversation so it can continue without the full transcript. Fold the prior summary and the new turns into one dense, factual summary (at most 1,600 characters) capturing only what later turns need: the operator's goals and standing asks; key findings, metrics, and figures surfaced; decisions and actions taken (including approved/rejected actions); data sources, entities, and time ranges examined; open/pending items; and the current focus. No greetings, no meta-commentary, no markdown headers — just the substance.",
}

func (a *API) compactSummary(ctx context.Context, orgID, prior, transcript string) (string, error) {
	values := map[string]any{"transcript": transcript}
	if prior != "" {
		values["priorSummary"] = prior
	}
	res, err := a.hostCalls().Generate(ctx, orgID, "compact", compactorProgram, values)
	if err != nil {
		return "", err
	}
	return jsTrim(jsValueString(res.Outputs["summary"])), nil
}
