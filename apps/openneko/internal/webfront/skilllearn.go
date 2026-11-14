package webfront

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

const (
	skillLearnMinRepeats = 5
	skillLearnSettlement = 2 * time.Hour
	learnedBodyMax       = 8000
	skillExcerptMax      = 4000
)

type skillUsageSnapshot struct {
	RunID, ContentHash string
	CreatedAt          time.Time
}

type skillLearnProposal struct {
	Lesson, Rationale, LearnedMarkdown string
	EvidenceRunIDs                     []string
}

type skillLearnTrace struct {
	Lesson         string   `json:"lesson,omitempty"`
	Rationale      string   `json:"rationale,omitempty"`
	EvidenceRunIDs []string `json:"evidenceRunIds,omitempty"`
	ContentHash    string   `json:"contentHash,omitempty"`
	BaseHash       string   `json:"baseHash,omitempty"`
	SkipReason     string   `json:"skipReason,omitempty"`
}

type learnedOverlay struct {
	SkillName, BaseHash, Status, LearnEventID, Body string
}

type skillLearnResult struct {
	Decision, Reason string
	Overlay          *learnedOverlay
	Trace            skillLearnTrace
	Diff             string
}

type skillLearnInput struct {
	OrgRoot, SkillName  string
	OrgEnabled, SkillOn bool
	Usages              []skillUsageSnapshot
	Now                 time.Time
	Propose             func(baseHash string, cohort []skillUsageSnapshot) (*skillLearnProposal, error)
}

var skillLessonProposer = llm.Program{
	Signature:   `learningPrompt:string "instructions plus skill excerpt and run ids" -> skillLearningProposal:string "a JSON object with lesson, rationale, learnedMarkdown, evidenceRunIds"`,
	Instruction: "You propose one additive skill lesson. Reply with JSON only. No tools.",
}

const skillProposalShape = `{
  "lesson": "one repeated procedure, short",
  "rationale": "why this lesson, citing run ids",
  "learnedMarkdown": "additive guidance only. no frontmatter, no secrets, no Boundary: line",
  "evidenceRunIds": ["run-id", "run-id"]
}`

// SkillLearn is runSkillLearnForOrgSkill() of the Node worker: it reads the
// settled uses of one org skill and, when one version repeats often enough,
// writes one additive LEARNED.md overlay from a model proposal.
func (a *API) SkillLearn(ctx context.Context, orgID, skillName string) error {
	res, err := a.skillLearn(ctx, orgID, skillName, func(root, baseHash string, cohort []skillUsageSnapshot) (*skillLearnProposal, error) {
		return a.proposeSkillLesson(ctx, orgID, root, skillName, baseHash, cohort)
	})
	if err != nil {
		return err
	}
	log.Printf("[skill_learn] org=%s skill=%s decision=%s reason=%s", orgID, skillName, res.Decision, res.Reason)
	return nil
}

func (a *API) skillLearn(ctx context.Context, orgID, skillName string,
	propose func(root, baseHash string, cohort []skillUsageSnapshot) (*skillLearnProposal, error)) (skillLearnResult, error) {
	root := orgAgentRoot(orgID)
	var orgOn, skillOn *bool
	if err := noRows(a.DB.QueryRow(ctx, `select enabled from skill_learn_org where org_id = $1`, orgID).Scan(&orgOn)); err != nil {
		return skillLearnResult{}, err
	}
	if err := noRows(a.DB.QueryRow(ctx, `select enabled from skill_learn_state where org_id = $1 and skill_name = $2`, orgID, skillName).Scan(&skillOn)); err != nil {
		return skillLearnResult{}, err
	}
	rows, err := a.DB.Query(ctx, `select run_id::text, content_hash, created_at from skill_usage
		where org_id = $1 and skill_name = $2 order by created_at, id`, orgID, skillName)
	if err != nil {
		return skillLearnResult{}, err
	}
	var usages []skillUsageSnapshot
	for rows.Next() {
		var u skillUsageSnapshot
		if err := rows.Scan(&u.RunID, &u.ContentHash, &u.CreatedAt); err != nil {
			rows.Close()
			return skillLearnResult{}, err
		}
		usages = append(usages, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return skillLearnResult{}, err
	}

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return skillLearnResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1), hashtext($2))`, orgID, skillName); err != nil {
		return skillLearnResult{}, err
	}
	res, err := runSkillLearn(skillLearnInput{
		OrgRoot: root, SkillName: skillName,
		OrgEnabled: orgOn != nil && *orgOn, SkillOn: skillOn == nil || *skillOn,
		Usages: usages, Now: time.Now(),
		Propose: func(baseHash string, cohort []skillUsageSnapshot) (*skillLearnProposal, error) {
			return propose(root, baseHash, cohort)
		},
	})
	if err != nil {
		return skillLearnResult{}, err
	}
	trace, _ := json.Marshal(res.Trace)
	runIDs, _ := json.Marshal(append([]string{}, res.Trace.EvidenceRunIDs...))
	if _, err := tx.Exec(ctx, `insert into skill_learn_event (org_id, skill_name, base_hash, content_hash, run_ids, lesson,
		rationale, diff, model_trace, decision, reason) values ($1, $2, nullif($3, ''), nullif($4, ''), $5::jsonb, nullif($6, ''),
		nullif($7, ''), nullif($8, ''), $9::jsonb, $10, $11)`,
		orgID, skillName, res.Trace.BaseHash, res.Trace.ContentHash, string(runIDs), res.Trace.Lesson,
		res.Trace.Rationale, res.Diff, string(trace), res.Decision, res.Reason); err != nil {
		return skillLearnResult{}, err
	}
	if res.Decision == "applied" && res.Overlay != nil {
		if _, err := tx.Exec(ctx, `insert into skill_learn_state (org_id, skill_name, enabled, current_base_hash, current_learned_hash)
			values ($1, $2, true, $3, $4) on conflict (org_id, skill_name) do update
			set current_base_hash = excluded.current_base_hash, current_learned_hash = excluded.current_learned_hash, updated_at = now()`,
			orgID, skillName, res.Overlay.BaseHash, sha256Hex(res.Overlay.Body)); err != nil {
			return skillLearnResult{}, err
		}
	}
	return res, tx.Commit(ctx)
}

func runSkillLearn(in skillLearnInput) (skillLearnResult, error) {
	skip := func(reason string, trace skillLearnTrace) (skillLearnResult, error) {
		return skillLearnResult{Decision: "skipped", Reason: reason, Trace: trace}, nil
	}
	if !in.OrgEnabled || !in.SkillOn {
		return skip("flags_off", skillLearnTrace{SkipReason: "org or skill learning is off"})
	}
	var settled []skillUsageSnapshot
	for _, u := range in.Usages {
		if in.Now.Sub(u.CreatedAt) >= skillLearnSettlement {
			settled = append(settled, u)
		}
	}
	if len(settled) < skillLearnMinRepeats {
		return skip("below_repeat_threshold", skillLearnTrace{
			SkipReason:     "settled " + strconv.Itoa(len(settled)) + " < " + strconv.Itoa(skillLearnMinRepeats),
			EvidenceRunIDs: usageRunIDs(settled),
		})
	}
	var order []string
	byHash := map[string][]skillUsageSnapshot{}
	for _, u := range settled {
		if _, ok := byHash[u.ContentHash]; !ok {
			order = append(order, u.ContentHash)
		}
		byHash[u.ContentHash] = append(byHash[u.ContentHash], u)
	}
	slices.SortStableFunc(order, func(x, y string) int { return len(byHash[y]) - len(byHash[x]) })
	cohort := byHash[order[0]]
	if len(cohort) < skillLearnMinRepeats {
		return skip("below_repeat_threshold", skillLearnTrace{
			SkipReason:     "no content_hash cohort reached the repeat threshold",
			EvidenceRunIDs: usageRunIDs(settled),
		})
	}
	contentHash := cohort[0].ContentHash
	baseHash, err := fingerprintSkillTree(filepath.Join(in.OrgRoot, "skills", in.SkillName))
	if err != nil {
		return skip("missing_skill", skillLearnTrace{SkipReason: "skill directory is missing"})
	}
	proposal, err := in.Propose(baseHash, cohort)
	if err != nil {
		return skillLearnResult{}, err
	}
	if proposal == nil {
		return skip("no_proposal", skillLearnTrace{BaseHash: baseHash, ContentHash: contentHash,
			EvidenceRunIDs: usageRunIDs(cohort), SkipReason: "proposer returned no lesson"})
	}
	if problem := additiveLearnedBodyProblem(proposal.LearnedMarkdown); problem != "" {
		return skillLearnResult{Decision: "rejected", Reason: problem, Trace: skillLearnTrace{
			Lesson: proposal.Lesson, Rationale: proposal.Rationale, EvidenceRunIDs: proposal.EvidenceRunIDs,
			BaseHash: baseHash, ContentHash: contentHash, SkipReason: problem,
		}}, nil
	}
	existing := readLearnedOverlay(in.OrgRoot, in.SkillName)
	if existing != nil && existing.BaseHash != baseHash {
		return skillLearnResult{Decision: "stale", Reason: "existing overlay base_hash does not match current base",
			Trace: skillLearnTrace{BaseHash: baseHash, ContentHash: contentHash}}, nil
	}
	overlay := &learnedOverlay{SkillName: in.SkillName, BaseHash: baseHash, Status: "applied", Body: jsTrim(proposal.LearnedMarkdown)}
	if err := writeLearnedOverlay(in.OrgRoot, overlay); err != nil {
		return skillLearnResult{}, err
	}
	before := ""
	if existing != nil {
		before = existing.Body
	}
	return skillLearnResult{
		Decision: "applied", Reason: "applied additive learned guidance", Overlay: overlay,
		Diff: "--- before\n" + before + "\n+++ after\n" + overlay.Body + "\n",
		Trace: skillLearnTrace{Lesson: proposal.Lesson, Rationale: proposal.Rationale,
			EvidenceRunIDs: proposal.EvidenceRunIDs, BaseHash: baseHash, ContentHash: contentHash},
	}, nil
}

func usageRunIDs(list []skillUsageSnapshot) []string {
	out := make([]string, len(list))
	for i, u := range list {
		out[i] = u.RunID
	}
	return out
}

func (a *API) proposeSkillLesson(ctx context.Context, orgID, root, skillName, baseHash string, cohort []skillUsageSnapshot) (*skillLearnProposal, error) {
	allowed := usageRunIDs(cohort)
	excerpt, _ := os.ReadFile(filepath.Join(root, "skills", skillName, "SKILL.md"))
	ids, _ := json.Marshal(allowed)
	prompt := strings.Join([]string{
		"You extract one repeated skill lesson from settled agent runs.",
		"Return only JSON matching this shape:",
		skillProposalShape,
		"Do not call tools. Do not rewrite SKILL.md frontmatter.",
		"skillName: " + skillName,
		"baseHash: " + baseHash,
		"runIds: " + string(ids),
		"skill excerpt:",
		jsSlice(string(excerpt), skillExcerptMax),
	}, "\n")
	res, err := a.hostCalls().Generate(ctx, orgID, "skill_lessons", skillLessonProposer, map[string]any{"learningPrompt": prompt})
	if err != nil {
		return nil, err
	}
	return parseSkillLearnProposal(jsValueString(res.Outputs["skillLearningProposal"]), allowed), nil
}

func parseSkillLearnProposal(raw string, allowed []string) *skillLearnProposal {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw[start:end+1]), &obj); err != nil || obj == nil {
		return nil
	}
	field := func(key string) string {
		s, _ := obj[key].(string)
		return jsTrim(s)
	}
	p := &skillLearnProposal{Lesson: field("lesson"), Rationale: field("rationale"), LearnedMarkdown: field("learnedMarkdown")}
	evidence, _ := obj["evidenceRunIds"].([]any)
	if p.Lesson == "" || p.Rationale == "" || p.LearnedMarkdown == "" || len(evidence) == 0 {
		return nil
	}
	for _, v := range evidence {
		if id, ok := v.(string); ok && slices.Contains(allowed, id) {
			p.EvidenceRunIDs = append(p.EvidenceRunIDs, id)
		}
	}
	if len(p.EvidenceRunIDs) == 0 {
		return nil
	}
	return p
}

var (
	jsLineEnds       = strings.NewReplacer("\r", "\n", " ", "\n", " ", "\n")
	learnedSecret    = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|bearer[` + jsSpaceClass + `]+[a-z0-9._-]+|sk-[a-z0-9]{16,})`)
	learnedEmail     = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	learnedFrontLine = regexp.MustCompile(`(?m)^---[` + jsSpaceClass + `]*$`)
	learnedNameLine  = regexp.MustCompile(`(?m)^[` + jsSpaceClass + `]*name[` + jsSpaceClass + `]*:`)
	learnedToolPerm  = regexp.MustCompile(`(?i)\ballowed-tools[` + jsSpaceClass + `]*:`)
	learnedBoundary  = regexp.MustCompile(`(?m)^[` + jsSpaceClass + `]*Boundary[` + jsSpaceClass + `]*:`)
	learnedSkill     = regexp.MustCompile(`(?m)^skill_name:[ \t\n\f\r\v]*(.+)$`)
	learnedEventID   = regexp.MustCompile(`(?m)^learn_event_id:[ \t\n\f\r\v]*(.+)$`)
)

// learnedTextProblems is scanLearnedText(): the reasons a learned body may
// not reach a skill.
func learnedTextProblems(text string) []string {
	lines := jsLineEnds.Replace(text)
	var out []string
	if learnedSecret.MatchString(text) {
		out = append(out, "candidate looks like a secret")
	}
	if learnedEmail.MatchString(text) {
		out = append(out, "candidate contains an email address")
	}
	if learnedFrontLine.MatchString(jsTrimStart(lines)) || learnedNameLine.MatchString(lines) {
		out = append(out, "candidate tries to set skill frontmatter")
	}
	if learnedToolPerm.MatchString(text) {
		out = append(out, "candidate edits tool permissions")
	}
	if learnedBoundary.MatchString(lines) {
		out = append(out, "candidate edits a safety boundary")
	}
	return out
}

func additiveLearnedBodyProblem(text string) string {
	trimmed := jsTrim(text)
	if trimmed == "" {
		return "learned body is empty"
	}
	if len(utf16.Encode([]rune(trimmed))) > learnedBodyMax {
		return "learned body is too long"
	}
	return strings.Join(learnedTextProblems(trimmed), "; ")
}

func learnedOverlayPath(root, skillName string) string {
	return filepath.Join(root, "skill-overlays", skillName, "LEARNED.md")
}

func readLearnedOverlay(root, skillName string) *learnedOverlay {
	raw, err := os.ReadFile(learnedOverlayPath(root, skillName))
	if err != nil {
		return nil
	}
	m := learnedFile.FindStringSubmatch(string(raw))
	if m == nil {
		return nil
	}
	o := &learnedOverlay{SkillName: learnedScalar(m[1], learnedSkill), BaseHash: learnedScalar(m[1], learnedBase),
		Status: learnedScalar(m[1], learnedStatus), LearnEventID: learnedScalar(m[1], learnedEventID), Body: jsTrim(m[2])}
	if o.BaseHash == "" {
		return nil
	}
	if o.Status == "" {
		o.Status = "applied"
	}
	if o.Status != "applied" && o.Status != "stale" && o.Status != "disabled" {
		return nil
	}
	if o.SkillName == "" {
		o.SkillName = skillName
	}
	return o
}

func writeLearnedOverlay(root string, o *learnedOverlay) error {
	path := learnedOverlayPath(root, o.SkillName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lines := []string{"---", "skill_name: " + o.SkillName, "base_hash: " + o.BaseHash, "status: " + o.Status}
	if o.LearnEventID != "" {
		lines = append(lines, "learn_event_id: "+o.LearnEventID)
	}
	lines = append(lines, "---", "", jsTrim(o.Body), "")
	return atomicWrite(path, []byte(strings.Join(lines, "\n")), 0o644)
}
