package webfront

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const workflowAPIInputFile = "trigger_input.json"

// workflowTurnOptions holds the prepared run. TriggerPayload and
// TriggerInput are values as jsParse gives them.
type workflowTurnOptions struct {
	Workflow                   workflowRecord
	WorkflowRunID, TriggerKind string
	ThreadID, RunID            string
	ObservationID              string
	TriggerPayload             any
	TriggerInput               any
	HasTriggerInput            bool
	UserMessage                string
	Mode                       string
	IncludeUxMetadata          bool
	CardsOff                   bool
	TimeoutMs                  int
	MaxToolIterations          int
	Emit                       func(ctx context.Context, event any) error

	wrapEmit func(func(context.Context, any) error) func(context.Context, any) error
	now      func() time.Time
	resolve  func(ctx context.Context, orgID string) (*llm.AxBackendConfig, map[string]string, error)
	runCore  func(ctx context.Context, in agentCoreInput) (axRunResult, error)
}

type workflowTurnResult struct {
	Status, FinalText, Error, ErrorCode string
	Degraded                            bool
}

type workflowTurnState struct {
	emit func(ctx context.Context, event any) error

	mu            sync.Mutex
	assistantText strings.Builder
	needsInput    bool
	toolCalls     []skillToolCall
}

func (s *workflowTurnState) wrapped(ctx context.Context, event any) error {
	s.mu.Lock()
	switch jsGet(event, "type") {
	case "message":
		if content, ok := jsGet(event, "content").(string); ok && jsGet(event, "role") == "assistant" {
			s.assistantText.WriteString(content)
		}
	case "needs_input":
		s.needsInput = true
	case "tool_start":
		name, _ := jsGet(event, "name").(string)
		s.toolCalls = append(s.toolCalls, skillToolCall{Name: name, Input: jsGet(event, "input")})
	}
	s.mu.Unlock()
	return s.emit(ctx, event)
}

func (s *workflowTurnState) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assistantText.String()
}

func (s *workflowTurnState) asked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsInput
}

// workflowCoreServers is the logical servers runWorkflowAgentBackend()
// mounts, in its order.
func workflowCoreServers(files bool) []string {
	servers := []string{"neko_graphjin", "neko_workflow_output", "neko_action", "neko_memory"}
	if files {
		servers = append(servers, "neko_files")
	}
	return servers
}

var errWorkflowNeedsInput = errors.New("Workflow paused awaiting operator input")

// runWorkflowTurn is runWorkflowTurn() with the sandbox launcher's stage and
// the runner's runWorkflowAgentBackend(): the turn runs in this process and
// serve runs the model loop. An error return is a failure the caller
// reports; the run is already finished then.
func (a *API) runWorkflowTurn(ctx context.Context, o workflowTurnOptions) (workflowTurnResult, error) {
	wf := o.Workflow
	resolve := o.resolve
	if resolve == nil {
		resolve = a.agentBackendWithKeys
	}
	backend, keys, err := resolve(ctx, wf.OrgID)
	if err != nil {
		return workflowTurnResult{}, err
	}
	if _, err := a.DB.Exec(ctx, `update work_run set status = 'running', updated_at = now() where id = $1`, o.RunID); err != nil {
		return workflowTurnResult{}, err
	}
	st := &workflowTurnState{emit: o.Emit}
	ws, err := turnWorkspace(wf.OrgID, o.ThreadID, o.RunID)
	if err != nil {
		return workflowTurnResult{}, err
	}
	if o.HasTriggerInput {
		if err := os.WriteFile(filepath.Join(ws.RunRoot, workflowAPIInputFile), []byte(jsStringifyIndent(o.TriggerInput, "  ")+"\n"), 0o644); err != nil {
			return workflowTurnResult{}, err
		}
	}
	res, err := a.workflowTurnBody(ctx, o, st, backend, keys, ws)
	if err != nil {
		return a.failWorkflowTurn(ctx, o, st, err)
	}
	return res, nil
}

func (a *API) workflowTurnBody(ctx context.Context, o workflowTurnOptions, st *workflowTurnState,
	backend *llm.AxBackendConfig, keys map[string]string, ws agentWorkspace) (workflowTurnResult, error) {
	wf := o.Workflow
	if err := st.wrapped(ctx, statusEvent("Starting workflow \""+wf.Name+"\" ("+o.TriggerKind+")…")); err != nil {
		return workflowTurnResult{}, err
	}
	act, err := a.runActor(ctx, o.RunID)
	if err != nil {
		return workflowTurnResult{}, err
	}
	ent, err := a.workflowEntitlement(ctx, wf.OrgID, wf.ID, act)
	if err != nil {
		return workflowTurnResult{}, err
	}
	team, err := a.heldItems(ctx, ent, "team_memory")
	if err != nil {
		return workflowTurnResult{}, err
	}
	memory, err := a.globalMemoryContext(ctx, wf.OrgID, 5, team)
	if err != nil {
		return workflowTurnResult{}, err
	}
	steps, _ := jsParse(wf.Steps)
	files := filesEnabled()
	terminal, err := a.orgTerminal(ctx, wf.OrgID)
	if err != nil {
		return workflowTurnResult{}, err
	}
	prompt := buildWorkflowPrompt(workflowPromptInput{Backend: "ax", Name: wf.Name, Description: wf.Description, Goal: wf.Goal,
		SystemPromptOverlay: wf.SystemPromptOverlay, Steps: jsArray(steps), Mode: o.Mode, IncludeUxMetadata: o.IncludeUxMetadata,
		MemoryContext: memory, Workspace: ws, Knowledge: readKnowledgePack(ws.KnowledgeRoot), FilesEnabled: files, ScriptData: terminal})
	now := time.Now
	if o.now != nil {
		now = o.now
	}
	seed := workflowSeedMessage(wf.Name, o.TriggerKind, o.UserMessage, o.TriggerPayload, now())
	heldSkills, err := a.heldItems(ctx, ent, "skill")
	if err != nil {
		return workflowTurnResult{}, err
	}
	var installed []string
	for _, s := range listInstalledSkills(ws.SkillsRoot) {
		installed = append(installed, s.Name)
	}
	allowed := runSkillScope(heldList(heldSkills), installed, false)
	_, allowed, own := runSkillsWithOwn(nil, allowed, ownRunSkills(ws.OrgRoot, act.UserID), false, terminal)
	limits := agentLimitsFor("workflow")
	timeout, maxCalls, continuations := limits.TimeoutMs, limits.MaxToolCalls, limits.MaxContinuations
	if o.TimeoutMs > 0 {
		timeout, continuations = o.TimeoutMs, 0
	}
	if o.MaxToolIterations > 0 {
		maxCalls = o.MaxToolIterations
	}
	in := agentCoreInput{Backend: backend, Prompt: prompt, UserMessage: seed, OrgID: wf.OrgID, ThreadID: o.ThreadID, RunID: o.RunID,
		CoreCards: o.CardsOff, CardsOff: o.CardsOff, Shell: terminal, Keys: keys, Socket: a.serveSocket(), Emit: st.wrapped,
		Workflow: &workflowCoreInput{WorkflowRunID: o.WorkflowRunID, ObservationID: o.ObservationID, Servers: workflowCoreServers(files),
			NetworkHosts: wf.NetworkHosts, TimeoutMs: timeout, MaxToolIterations: maxCalls, ReasoningEffort: limits.ReasoningEffort}}
	scripted := slices.ContainsFunc(jsArray(steps), func(s any) bool { return jsTruthy(jsGet(s, "script")) })

	release, err := a.slots.acquire(ctx,
		func() error { return st.wrapped(ctx, statusEvent("Waiting for another question to finish…")) },
		func() { a.touchAgentRun(context.WithoutCancel(ctx), o.RunID) })
	if err != nil {
		return workflowTurnResult{}, err
	}
	res, err := func() (axRunResult, error) {
		defer release()
		if err := st.wrapped(ctx, statusEvent("Preparing secure agent workspace…")); err != nil {
			return axRunResult{}, err
		}
		stage, err := stageTurn(ws, stageOptions{Allowed: allowed, Own: own, Terminal: terminal, Actor: act})
		if err != nil {
			return axRunResult{}, err
		}
		if scripted {
			stage.captureReason = "script_step"
		}
		if err := st.wrapped(ctx, statusEvent("Agent is working…")); err != nil {
			os.RemoveAll(stage.Dir)
			return axRunResult{}, err
		}
		res, err := a.workflowStagedCore(ctx, o, st, in, stage, steps, allowed, terminal, continuations)
		var evidence *skillEvidence
		if err == nil && res.Status == "completed" {
			st.mu.Lock()
			evidence = &skillEvidence{ToolCalls: slices.Clone(st.toolCalls)}
			st.mu.Unlock()
		}
		stage.finish(context.WithoutCancel(ctx), a, wf.OrgID, o.RunID, ctx.Err() != nil, evidence)
		return res, err
	}()
	if err != nil {
		return workflowTurnResult{}, err
	}
	if stop := spendStop(ctx); stop != nil {
		res.Status, res.Error, res.ErrorCode = "failed", stop.Error(), "spend_limit"
	}
	if st.asked() {
		return workflowTurnResult{}, errWorkflowNeedsInput
	}
	return a.finishWorkflowTurn(context.WithoutCancel(ctx), o, st, ws, res)
}

// workflowStagedCore is runWorkflowAgentBackend() on the stage: the script
// steps, then the agent turn, continued in the same run folder when a turn
// runs out of time.
func (a *API) workflowStagedCore(ctx context.Context, o workflowTurnOptions, st *workflowTurnState, in agentCoreInput,
	stage *turnStage, steps any, allowed []string, terminal bool, maxContinuations int) (axRunResult, error) {
	in.Workspace = stage.Workspace
	in.Prompt = strings.ReplaceAll(in.Prompt, stage.host.OrgRoot, stage.Workspace.OrgRoot)
	first := in.Prompt
	if slices.ContainsFunc(jsArray(steps), func(s any) bool { return jsTruthy(jsGet(s, "script")) }) {
		var box *sandbox.Config
		if terminal {
			cfg := sandbox.ConfigFromEnv(os.Getenv)
			box = &cfg
		}
		var data sandbox.DataBroker
		if box != nil && !in.withoutScriptData {
			b := in.binding()
			var be toolBackend
			if in.toolBackend != nil {
				be = in.toolBackend(b)
			} else {
				rt := a.runTools(b)
				rt.events = st.wrapped
				be = rt
			}
			var err error
			if data, err = scriptDataBroker(b, be); err != nil {
				return axRunResult{}, err
			}
		}
		results, err := runScriptSteps(ctx, scriptStepsInput{Steps: jsArray(steps), Workspace: stage.Workspace,
			Input: o.TriggerPayload, Allowed: allowed, Sandbox: box, Data: data, Emit: st.wrapped})
		if err != nil {
			return axRunResult{}, err
		}
		if block := formatStepResults(results); block != "" {
			first += "\n\n" + block
		}
	}
	runCore := o.runCore
	if runCore == nil {
		runCore = a.runAgentCore
	}
	trail := &actionTrail{}
	var heldMu sync.Mutex
	var held any
	in.Emit = func(ctx context.Context, event any) error {
		trail.record(event)
		if jsGet(event, "type") == "error" {
			if msg, _ := jsGet(event, "message").(string); strings.HasPrefix(msg, "ax turn exceeded its ") {
				heldMu.Lock()
				held = event
				heldMu.Unlock()
				return nil
			}
		}
		return st.wrapped(ctx, event)
	}
	in.Prompt = first
	for continuation := 0; ; continuation++ {
		heldMu.Lock()
		held = nil
		heldMu.Unlock()
		res, err := runCore(ctx, in)
		if err != nil {
			return res, err
		}
		if !res.TimedOut || continuation >= maxContinuations || ctx.Err() != nil {
			heldMu.Lock()
			event := held
			heldMu.Unlock()
			if event != nil {
				if err := st.wrapped(ctx, event); err != nil {
					return res, err
				}
			}
			if res.TimedOut && continuation > 0 && res.Error != "" {
				res.Error += "; " + strconv.Itoa(continuation) + " continuation(s) also ran out of time"
			}
			return res, nil
		}
		if err := st.wrapped(ctx, statusEvent("Turn budget reached; continuing the run ("+strconv.Itoa(continuation+1)+"/"+
			strconv.Itoa(maxContinuations)+")")); err != nil {
			return res, err
		}
		in.Prompt = first + "\n\n" + section("workflow_continuation", "number", strconv.Itoa(continuation+1), "trail",
			orDefault(trail.summary(), "(none recorded)"))
	}
}

// actionTrail is ActionTrail in packages/llm/src/workflows/agent-core.ts:
// the last 15 tool calls and the last 1,000 characters of answer text.
type actionTrail struct {
	mu      sync.Mutex
	actions []string
	text    string
}

func (t *actionTrail) record(event any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch jsGet(event, "type") {
	case "tool_start":
		input := ""
		if v, ok := event.(*jsObj).vals["input"]; ok {
			if _, undef := v.(jsUndefined); !undef {
				input = " " + jsStringify(v, false)
			}
		}
		t.actions = append(t.actions, "- "+jsTemplate(jsProp(event, "name"))+jsSlice(input, 300))
		if len(t.actions) > 15 {
			t.actions = t.actions[1:]
		}
	case "message":
		if content, ok := jsGet(event, "content").(string); ok && jsGet(event, "role") == "assistant" {
			t.text = utf16Tail(t.text+content, 1_000)
		}
	}
}

func (t *actionTrail) summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := slices.Clone(t.actions)
	if text := jsTrim(t.text); text != "" {
		lines = append(lines, "Last assistant text: "+text)
	}
	return strings.Join(lines, "\n")
}

func (a *API) finishWorkflowTurn(ctx context.Context, o workflowTurnOptions, st *workflowTurnState, ws agentWorkspace, res axRunResult) (workflowTurnResult, error) {
	if res.Status == "completed" {
		for _, art := range discoverRunArtifacts(ws) {
			if err := st.wrapped(ctx, jsObjOf("type", "artifact", "artifact", jsObjOf("path", art.Path, "label", art.Label))); err != nil {
				return workflowTurnResult{}, err
			}
		}
	}
	text := jsTrim(res.FinalText)
	if text == "" {
		text = jsTrim(st.text())
	}
	value := extractFence(text, valueFence, valueEstimateShape, false)
	text = value.Text
	var runErr *string
	if res.Error != "" {
		runErr = &res.Error
	}
	if err := a.finishWorkRun(ctx, o.RunID, res.Status, runErr); err != nil {
		return workflowTurnResult{}, err
	}
	var minutes any
	if p := value.last(); p != nil {
		minutes = clampAnalysisMinutes(p.vals["minutes_saved"])
		if _, err := a.DB.Exec(ctx, `update work_run set analysis_minutes_saved = $2, analysis_minutes_basis = $3, updated_at = now() where id = $1`,
			o.RunID, minutes, p.vals["basis"]); err != nil {
			log.Printf("[workflow-run] setWorkRunValue failed: %v", err)
		}
	}
	var summary *string
	if s := jsSlice(text, 4000); s != "" {
		summary = &s
	} else if res.Status == "completed" {
		s := "Looked at the data; nothing to flag."
		summary = &s
	}
	if err := a.finishWorkflowRun(ctx, o.WorkflowRunID, res.Status, summary, runErr); err != nil {
		return workflowTurnResult{}, err
	}
	if text != "" {
		if err := a.saveAssistantMessage(ctx, o.Workflow.OrgID, o.ThreadID, o.RunID, text); err != nil {
			return workflowTurnResult{}, err
		}
	}
	if minutes == nil {
		minutes = 0.0
	}
	if err := st.wrapped(ctx, jsObjOf("type", "done", "result", jsObjOf("status", res.Status, "minutesSaved", minutes))); err != nil {
		return workflowTurnResult{}, err
	}
	return workflowTurnResult{Status: res.Status, FinalText: text, Error: res.Error, ErrorCode: res.ErrorCode, Degraded: res.Degraded}, nil
}

// failWorkflowTurn is the catch block of runWorkflowTurn(): a question asked
// ends the run as needs_input, a cancel or a spend stop is a result, and any
// other error goes back to the caller after the run is finished as failed.
func (a *API) failWorkflowTurn(ctx context.Context, o workflowTurnOptions, st *workflowTurnState, cause error) (workflowTurnResult, error) {
	bg := context.WithoutCancel(ctx)
	asked := st.text()
	summary := func() *string {
		if s := jsSlice(asked, 4000); s != "" {
			return &s
		}
		return nil
	}
	if errors.Is(cause, errWorkflowNeedsInput) || st.asked() {
		if err := a.finishWorkRun(bg, o.RunID, "failed", nil); err != nil {
			return workflowTurnResult{}, err
		}
		if err := a.finishWorkflowRun(bg, o.WorkflowRunID, "needs_input", summary(), nil); err != nil {
			return workflowTurnResult{}, err
		}
		if err := st.wrapped(bg, doneEvent("needs_input")); err != nil {
			return workflowTurnResult{}, err
		}
		return workflowTurnResult{Status: "needs_input", FinalText: asked}, nil
	}
	stop := spendStop(ctx)
	shutdown := ctx.Err() != nil && errors.Is(context.Cause(ctx), errTurnShutdown)
	aborted := stop == nil && !shutdown && (ctx.Err() != nil || errors.Is(cause, context.Canceled) || strings.Contains(cause.Error(), "aborted"))
	status, msg := "failed", cause.Error()
	switch {
	case stop != nil:
		msg = stop.Error()
	case shutdown:
		msg = context.Cause(ctx).Error()
	case aborted:
		status, msg = "cancelled", "Cancelled by user."
	}
	if err := st.wrapped(bg, jsObjOf("type", "error", "message", msg)); err != nil {
		return workflowTurnResult{}, err
	}
	var stored *string
	if !aborted {
		stored = &msg
	}
	if err := a.finishWorkRun(bg, o.RunID, status, stored); err != nil {
		return workflowTurnResult{}, err
	}
	if err := a.finishWorkflowRun(bg, o.WorkflowRunID, status, summary(), stored); err != nil {
		return workflowTurnResult{}, err
	}
	if err := st.wrapped(bg, doneEvent(status)); err != nil {
		return workflowTurnResult{}, err
	}
	if !aborted && stop == nil && !shutdown {
		return workflowTurnResult{}, cause
	}
	out := workflowTurnResult{Status: status, FinalText: asked}
	if stop != nil || shutdown {
		out.Error = msg
	}
	return out, nil
}

// workflowEntitlement is runEntitlementActor() with the workflow: a service
// run of a pack's workflow holds only that pack's items.
func (a *API) workflowEntitlement(ctx context.Context, orgID, workflowID string, act runActorRow) (actor, error) {
	ent, err := a.runEntitlement(ctx, orgID, act)
	if err != nil || act.UserID != "" || !ent.Admin {
		return ent, err
	}
	packs, err := a.packsContaining(ctx, orgID, "workflow", workflowID)
	if err != nil || len(packs) == 0 {
		return ent, err
	}
	return actor{OrgID: orgID, PackID: packs[0]}, nil
}

// globalMemoryContext is formatGlobalMemoryPromptContext() in
// packages/llm/src/work/memory.ts.
func (a *API) globalMemoryContext(ctx context.Context, orgID string, limit int, team heldSet) (string, error) {
	const none = "No global memories are currently saved for this workspace."
	if !team.has("global") {
		return none, nil
	}
	all, err := a.collectMemories(ctx, a.DB, `select `+memoryColumns+` from work_memory
		where org_id = $1 and scope = 'global' and user_id is null and archived_at is null
		order by pinned desc, updated_at desc limit $2`, orgID, limit)
	if err != nil {
		return "", err
	}
	kept := keptIntact(all)
	if len(kept) == 0 {
		return none, nil
	}
	lines := []string{"Memories the operator has saved — treat as durable context:"}
	for _, m := range kept {
		scope := m.Scope
		if m.ScopeID != nil && *m.ScopeID != "" {
			scope += ":" + *m.ScopeID
		}
		lines = append(lines, "- ["+m.ID+"] "+m.Kind+" ("+scope+"): "+m.Text)
	}
	return strings.Join(lines, "\n"), nil
}

// finishWorkflowRun is finishWorkflowRun() in
// packages/llm/src/workflows/store.ts, with postWorkflowRunToBotChat().
func (a *API) finishWorkflowRun(ctx context.Context, workflowRunID, status string, summary, runErr *string) error {
	if _, err := a.DB.Exec(ctx, `update workflow_run set status = $2, summary = $3, error = $4, finished_at = now(), updated_at = now()
		where id = $1`, workflowRunID, status, summary, runErr); err != nil {
		return err
	}
	if err := a.postRunToBotChat(ctx, workflowRunID, status, deref(summary), deref(runErr)); err != nil {
		log.Printf("[workflow-run] bot chat post failed: %v", err)
	}
	return nil
}

// postRunToBotChat is postWorkflowRunToBotChat() in
// packages/llm/src/work/bot.ts: a bot's chat hears how each of its routine
// runs ended.
func (a *API) postRunToBotChat(ctx context.Context, workflowRunID, status, summary, runErr string) error {
	var orgID, threadID, routine string
	err := a.DB.QueryRow(ctx, `select r.org_id, b.thread_id::text, d.name from workflow_run r
		join workflow_definition d on d.id = r.workflow_id
		join bot b on b.org_id = r.org_id and b.thread_id = d.created_by_thread_id
		where r.id = $1 and r.thread_id <> b.thread_id limit 1`, workflowRunID).Scan(&orgID, &threadID, &routine)
	if err != nil {
		return noRows(err)
	}
	heading := func(text string) string { return "**" + jsTrim(text) + "**" }
	var parts []string
	switch status {
	case "completed":
		var title, body *string
		if err := noRows(a.DB.QueryRow(ctx, `select title, body from workflow_output where workflow_run_id = $1
			order by created_at desc limit 1`, workflowRunID).Scan(&title, &body)); err != nil {
			return err
		}
		switch {
		case jsTrim(deref(body)) != "":
			parts = []string{heading(orDefault(deref(title), routine)), jsTrim(deref(body))}
		case jsTrim(summary) != "":
			parts = []string{heading(routine), jsTrim(summary)}
		}
	case "needs_input":
		parts = []string{heading(routine + " is waiting for input."), jsTrim(summary)}
	case "failed":
		parts = []string{heading(routine + " failed."), jsTrim(runErr)}
	case "cancelled":
		parts = []string{heading(routine + " stopped."), jsTrim(runErr)}
	}
	parts = slices.DeleteFunc(parts, func(p string) bool { return p == "" })
	if len(parts) == 0 {
		return nil
	}
	if _, err := a.DB.Exec(ctx, `insert into work_message (org_id, thread_id, run_id, role, content) values ($1, $2, null, 'assistant', $3)`,
		orgID, threadID, strings.Join(parts, "\n\n")); err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, `update work_thread set updated_at = now(), last_message_at = now() where id = $1`, threadID)
	return err
}
