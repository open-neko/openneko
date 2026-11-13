package webfront

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// chatTurnOptions is RunChatTurnOptions in packages/llm/src/work/run-chat-turn.ts.
// Emit must be safe to call from two goroutines: serve's event stream and
// the run's tool calls emit at the same time.
type chatTurnOptions struct {
	OrgID, ThreadID, RunID, Message string
	Channel                         string
	IncludeUxMetadata               bool
	NativeDelegationOff             bool
	GraphjinToolPolicy              string
	PackActions                     []packActionDescriptor
	Emit                            func(ctx context.Context, event any) error
	Summarize                       func(ctx context.Context, orgID, prior, transcript string) (string, error)

	resolve func(ctx context.Context, orgID string) (*llm.AxBackendConfig, map[string]string, error)
	runCore func(ctx context.Context, in agentCoreInput) (axRunResult, error)
	keepBot func(ctx context.Context, in botKeepInput) error
}

type chatTurnResult struct {
	Status, FinalText, Error, ErrorCode string
	Degraded                            bool
}

// spendCapExceeded is SpendCapExceeded in packages/llm/src/spend/run-guard.ts.
// The run's spend guard cancels the turn's context with it as the cause.
type spendCapExceeded struct{ capUSD, spentUSD float64 }

func (e *spendCapExceeded) Error() string {
	return "The run exceeded its $" + jsToFixed2(e.capUSD) + " spend cap ($" + jsToFixed2(e.spentUSD) + " spent)."
}

func spendStop(ctx context.Context) *spendCapExceeded {
	var stop *spendCapExceeded
	if ctx.Err() != nil && errors.As(context.Cause(ctx), &stop) {
		return stop
	}
	return nil
}

var errNoAnswer = errors.New("agent runtime contract violation: completed without assistant output or surface")

// chatTurnState is what the wrapped emit of runChatTurn() records.
type chatTurnState struct {
	emit func(ctx context.Context, event any) error

	mu            sync.Mutex
	assistantText strings.Builder
	surfaces      []any
	usageReported bool
	lastSnapshot  any
	needsInput    any
	sawAnswer     bool
	denials       map[string]bool
}

var afterQuestion = map[string]bool{"message": true, "surface": true, "vitals": true, "followups": true, "needs_input": true}

func (s *chatTurnState) wrapped(ctx context.Context, event any) error {
	typ, _ := jsGet(event, "type").(string)
	s.mu.Lock()
	switch typ {
	case "message":
		content, _ := jsGet(event, "content").(string)
		s.sawAnswer = s.sawAnswer || (jsGet(event, "role") == "assistant" && jsTrim(content) != "")
	case "surface":
		s.sawAnswer = s.sawAnswer || len(jsArray(jsGet(event, "messages"))) > 0
	case "action_request_emit", "needs_input", "output_emit":
		s.sawAnswer = true
	}
	if s.needsInput != nil && afterQuestion[typ] {
		s.mu.Unlock()
		return nil
	}
	switch typ {
	case "message":
		if content, ok := jsGet(event, "content").(string); ok && jsGet(event, "role") == "assistant" {
			s.assistantText.WriteString(content)
		}
	case "surface":
		s.surfaces = append(s.surfaces, jsArray(jsGet(event, "messages"))...)
	case "usage":
		if jsGet(event, "source") == "outer" {
			s.usageReported = true
		}
	case "tool_start":
		if snap := jsGet(event, "usageSnapshot"); jsTruthy(snap) {
			s.lastSnapshot = snap
		}
	case "needs_input":
		s.needsInput = event
	}
	s.mu.Unlock()
	if err := s.emit(ctx, event); err != nil {
		return err
	}
	denial := networkPolicyDenial(event)
	if denial == nil {
		return nil
	}
	port := "443"
	if p, ok := denial.vals["port"].(float64); ok {
		port = jsNumberString(p)
	}
	key := denial.vals["host"].(string) + ":" + port
	s.mu.Lock()
	seen := s.denials[key]
	s.denials[key] = true
	s.mu.Unlock()
	if seen {
		return nil
	}
	return s.emit(ctx, denial)
}

func (s *chatTurnState) question() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsInput
}

func statusEvent(message string) *jsObj { return jsObjOf("type", "status", "message", message) }

func doneEvent(status string) *jsObj {
	return jsObjOf("type", "done", "result", jsObjOf("status", status))
}

// runChatTurn is runChatTurn() in packages/llm/src/work/run-chat-turn.ts with
// the runner's part of the sandbox launcher: the turn runs in this process
// and serve runs the model loop. An error return is a failure the caller
// reports; the run is already finished then.
func (a *API) runChatTurn(ctx context.Context, o chatTurnOptions) (chatTurnResult, error) {
	if _, err := a.DB.Exec(ctx, `update work_run set status = 'running', updated_at = now() where id = $1`, o.RunID); err != nil {
		return chatTurnResult{}, err
	}
	var exists bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from work_thread where org_id = $1 and id::text = $2)`, o.OrgID, o.ThreadID).Scan(&exists); err != nil {
		return chatTurnResult{}, err
	}
	if !exists {
		msg := errThreadGone.Error()
		if err := a.finishWorkRun(ctx, o.RunID, "failed", &msg); err != nil {
			return chatTurnResult{}, err
		}
		log.Printf("[work-run] thread %s not found for run %s; marking failed and skipping", o.ThreadID, o.RunID)
		return chatTurnResult{Status: "failed", Error: msg}, nil
	}

	st := &chatTurnState{emit: o.Emit, denials: map[string]bool{}}
	t := &chatTurn{api: a, o: o, st: st}
	if err := st.wrapped(ctx, statusEvent("Starting Ax…")); err != nil {
		return t.fail(ctx, err)
	}
	if err := st.wrapped(ctx, statusEvent("Retrieving relevant context…")); err != nil {
		return t.fail(ctx, err)
	}
	tc, err := a.loadTurnContext(ctx, turnOptions{OrgID: o.OrgID, ThreadID: o.ThreadID, RunID: o.RunID, Message: o.Message,
		Channel: o.Channel, IncludeUxMetadata: o.IncludeUxMetadata, NativeDelegationOff: o.NativeDelegationOff, Summarize: o.Summarize})
	if err != nil {
		return t.fail(ctx, err)
	}
	t.tc = tc
	resolve := o.resolve
	if resolve == nil {
		resolve = a.agentBackendWithKeys
	}
	backend, keys, err := resolve(ctx, o.OrgID)
	if err != nil {
		return t.fail(ctx, err)
	}
	var held []packActionDescriptor
	for _, d := range o.PackActions {
		if tc.HeldActions.has(d.Kind) {
			held = append(held, d)
		}
	}
	if err := st.wrapped(ctx, statusEvent("Agent is working…")); err != nil {
		return t.fail(ctx, err)
	}
	runCore := o.runCore
	if runCore == nil {
		runCore = a.runAgentCore
	}
	res, err := runCore(ctx, agentCoreInput{Backend: backend, Prompt: tc.Prompt, UserMessage: o.Message,
		OrgID: o.OrgID, ThreadID: o.ThreadID, RunID: o.RunID, Workspace: tc.Workspace, BackendState: tc.BackendState,
		PackActions: held, SourceConfigEnabled: tc.SourceConfig, GraphjinToolPolicy: o.GraphjinToolPolicy,
		NativeDelegationOff: o.NativeDelegationOff, CoreCards: !tc.RichCards, CardsOff: !tc.WantsCards, Shell: tc.Terminal,
		Keys: keys, Socket: a.serveSocket(), Emit: st.wrapped})
	if err != nil {
		return t.fail(ctx, err)
	}
	bg := context.WithoutCancel(ctx)
	st.mu.Lock()
	noAnswer := res.Status == "completed" && jsTrim(res.FinalText) == "" && jsTrim(res.RawText) == "" && !st.sawAnswer
	partial := !st.usageReported && st.lastSnapshot != nil
	snapshot := st.lastSnapshot
	st.mu.Unlock()
	if noAnswer {
		return t.fail(ctx, errNoAnswer)
	}
	if partial {
		usage := jsSpread(snapshot)
		usage.set("coverage", "partial")
		usage.set("missingReasons", append(append([]any{}, jsArray(usage.vals["missingReasons"])...),
			"The turn ended early; usage covers the model calls made before its last tool call."))
		if err := st.wrapped(bg, jsObjOf("type", "usage", "source", "outer", "usage", usage)); err != nil {
			return t.fail(ctx, err)
		}
	}
	if ctx.Err() != nil {
		return t.fail(ctx, context.Cause(ctx))
	}
	return t.finish(bg, res)
}

type chatTurn struct {
	api *API
	o   chatTurnOptions
	st  *chatTurnState
	tc  *turnContext

	asked bool
}

func (t *chatTurn) finishNeedsInput(ctx context.Context) (chatTurnResult, error) {
	text := needsInputText(t.st.question())
	if !t.asked {
		t.asked = true
		if err := t.api.saveAssistantMessage(ctx, t.o.OrgID, t.o.ThreadID, t.o.RunID, text); err != nil {
			return chatTurnResult{}, err
		}
		if err := t.api.finishWorkRun(ctx, t.o.RunID, "needs_input", nil); err != nil {
			return chatTurnResult{}, err
		}
		if err := t.o.Emit(ctx, doneEvent("needs_input")); err != nil {
			return chatTurnResult{}, err
		}
	}
	return chatTurnResult{Status: "needs_input", FinalText: text}, nil
}

// fail is the catch block of runChatTurn(): a question asked ends the run
// as needs_input, a cancel or a spend stop is a result, and any other error
// goes back to the caller after the run is finished as failed.
func (t *chatTurn) fail(ctx context.Context, cause error) (chatTurnResult, error) {
	bg := context.WithoutCancel(ctx)
	if t.st.question() != nil {
		return t.finishNeedsInput(bg)
	}
	stop := spendStop(ctx)
	shutdown := ctx.Err() != nil && errors.Is(context.Cause(ctx), errTurnShutdown)
	aborted := stop == nil && !shutdown && (ctx.Err() != nil || errors.Is(cause, context.Canceled) || strings.Contains(cause.Error(), "aborted"))
	status, msg := "failed", cause.Error()
	switch {
	case stop != nil:
		msg = stop.Error()
	case aborted:
		status, msg = "cancelled", "Cancelled by user."
	}
	if err := t.st.wrapped(bg, jsObjOf("type", "error", "message", msg)); err != nil {
		log.Printf("[work-run] error event for %s not stored: %v", t.o.RunID, err)
	}
	var stored *string
	if !aborted {
		stored = &msg
	}
	if err := t.api.finishWorkRun(bg, t.o.RunID, status, stored); err != nil {
		return chatTurnResult{}, err
	}
	if err := t.st.wrapped(bg, doneEvent(status)); err != nil {
		log.Printf("[work-run] done event for %s not stored: %v", t.o.RunID, err)
	}
	if !aborted && stop == nil && !shutdown {
		return chatTurnResult{}, cause
	}
	t.st.mu.Lock()
	out := chatTurnResult{Status: status, FinalText: t.st.assistantText.String()}
	t.st.mu.Unlock()
	if stop != nil || shutdown {
		out.Error = msg
	}
	return out, nil
}

// finish is the result handling of runChatTurn() after the agent returns.
func (t *chatTurn) finish(ctx context.Context, res axRunResult) (chatTurnResult, error) {
	a, o, tc, st := t.api, t.o, t.tc, t.st
	if res.BackendState != nil || tc.Compaction != nil {
		base := res.BackendState
		if base == nil {
			base = tc.BackendState
		}
		state := jsSpread(base)
		if c := tc.Compaction; c != nil {
			state.set("compaction", jsObjOf("summary", c.Summary, "throughMessageId", c.ThroughMessageID,
				"updatedAt", c.UpdatedAt, "version", float64(c.Version)))
		}
		if _, err := a.DB.Exec(ctx, `update work_thread set backend_state = $2::jsonb, updated_at = now() where id = $1`,
			o.ThreadID, jsStringify(state, false)); err != nil {
			return t.fail(ctx, err)
		}
	}
	if st.question() != nil {
		return t.finishNeedsInput(ctx)
	}
	if res.Status == "completed" {
		for _, art := range discoverRunArtifacts(tc.Workspace) {
			if err := st.wrapped(ctx, jsObjOf("type", "artifact", "artifact", jsObjOf("path", art.Path, "label", art.Label))); err != nil {
				return t.fail(ctx, err)
			}
		}
	}

	st.mu.Lock()
	streamed := st.assistantText.String()
	st.mu.Unlock()
	source := res.FinalText
	if res.HasRaw {
		source = res.RawText
	}
	source = jsTrim(source)
	if source == "" {
		source = jsTrim(streamed)
	}
	rt := a.runTools(turnBinding{OrgID: o.OrgID, ThreadID: o.ThreadID, RunID: o.RunID, Kind: "work",
		WantsCards: tc.WantsCards, CardSchema: pick(tc.RichCards, "rich", "core"), SkillsRoot: tc.Workspace.SkillsRoot})
	rt.events = st.wrapped
	if err := t.applyFences(ctx, rt, source); err != nil {
		return t.fail(ctx, err)
	}

	value := extractFence(source, valueFence, valueEstimateShape, false).last()
	var minutes any
	if value != nil {
		minutes = clampAnalysisMinutes(value.vals["minutes_saved"])
		if _, err := a.DB.Exec(ctx, `update work_run set analysis_minutes_saved = $2, analysis_minutes_basis = $3, updated_at = now() where id = $1`,
			o.RunID, minutes, value.vals["basis"]); err != nil {
			log.Printf("[work-run] setWorkRunValue failed: %v", err)
		}
	}
	if followups := extractFence(source, followupsFence, followupsShape, false).last(); followups != nil {
		if err := st.wrapped(ctx, jsObjOf("type", "followups", "items", followups.vals["followups"])); err != nil {
			return t.fail(ctx, err)
		}
	}
	vitals := extractFence(source, vitalsFence, vitalsShape, false).last()
	if vitals != nil {
		if err := st.wrapped(ctx, jsObjOf("type", "vitals", "items", vitals.vals["vitals"])); err != nil {
			return t.fail(ctx, err)
		}
	}
	_, memories := extractMemoryFences(source)
	for _, op := range memories {
		threadID, runID := o.ThreadID, o.RunID
		pinned := op.Pinned == nil || *op.Pinned
		if _, err := rt.rememberMemory(ctx, memoryRememberIn{OrgID: o.OrgID, ThreadID: &threadID, RunID: &runID, Text: op.Text,
			Kind: "business_rule", Scope: orDefault(op.Scope, "global"), Pinned: pinned}); err != nil {
			log.Printf("[work-memory] fence-driven save failed: %v", err)
		}
	}

	text := displayText(res.FinalText)
	if text == "" && res.Status == "completed" {
		stored, _ := a.storedRunEvents(ctx, o.OrgID, o.RunID)
		var answer strings.Builder
		var notes []string
		st.mu.Lock()
		surfaces := append([]any{}, st.surfaces...)
		st.mu.Unlock()
		for _, e := range stored {
			switch jsGet(e, "type") {
			case "message":
				if s, ok := jsGet(e, "content").(string); ok && jsGet(e, "role") == "assistant" {
					answer.WriteString(s)
				}
			case "interim":
				s, _ := jsGet(e, "content").(string)
				notes = append(notes, stripDanglingToolCalls(s))
			case "surface":
				surfaces = append(surfaces, jsArray(jsGet(e, "messages"))...)
			}
		}
		text = structuredTurnSummary(vitals, surfaces, append(notes, stripDanglingToolCalls(answer.String())))
	}
	if text != "" {
		if err := a.saveAssistantMessage(ctx, o.OrgID, o.ThreadID, o.RunID, text); err != nil {
			return t.fail(ctx, err)
		}
	}
	var runErr *string
	if res.Error != "" {
		runErr = &res.Error
	}
	if err := a.finishWorkRun(ctx, o.RunID, res.Status, runErr); err != nil {
		return t.fail(ctx, err)
	}
	if tc.Bot != nil && res.Status == "completed" {
		var earlier []string
		for _, m := range tc.Messages {
			if m.Role == "user" && deref(m.RunID) != o.RunID {
				earlier = append(earlier, m.Content)
			}
		}
		keep := o.keepBot
		if keep == nil {
			keep = a.keepBotInstructions
		}
		in := botKeepInput{OrgID: o.OrgID, ThreadID: o.ThreadID, Bot: *tc.Bot, Message: o.Message, Reply: text, Earlier: earlier}
		go func() {
			if err := keep(ctx, in); err != nil {
				log.Printf("[bot] instructions update skipped: %v", err)
			}
		}()
	}
	if minutes == nil {
		minutes = 0.0
	}
	if err := st.wrapped(ctx, jsObjOf("type", "done", "result", jsObjOf("status", res.Status, "minutesSaved", minutes))); err != nil {
		return t.fail(ctx, err)
	}
	return chatTurnResult{Status: res.Status, FinalText: text, Error: res.Error, ErrorCode: res.ErrorCode, Degraded: res.Degraded}, nil
}

// applyFences runs the action, workflow and rule fences through the same
// run tools as the MCP tools, so a fence meets the same grants and checks.
func (t *chatTurn) applyFences(ctx context.Context, rt *runTools, source string) error {
	o, emit := t.o, t.st.wrapped
	for _, payload := range extractFence(source, actionRequestFence, actionRequestShape, false).Payloads {
		if err := t.fenceAction(ctx, rt, payload); err != nil {
			var emitErr emitError
			if errors.As(err, &emitErr) {
				return emitErr.err
			}
			log.Printf("[work-run] handleWorkActionRequest failed: %v", err)
		}
	}

	workflow := extractFence(source, workflowSaveFence, workflowSaveShape, true)
	if p := workflow.first(); p != nil {
		in := jsObjOf("orgId", o.OrgID)
		copyPresent(in, p, "name", "description", "goal", "systemPromptOverlay", "steps", "batch", "triggers")
		in.set("createdByThreadId", o.ThreadID)
		in.set("createdByRunId", o.RunID)
		saved, err := rt.saveWorkflow(ctx, in)
		if err != nil {
			if err := emit(ctx, jsObjOf("type", "error", "message", "workflow save failed: "+err.Error())); err != nil {
				return err
			}
		} else {
			wf, sub := jsGet(saved, "workflow"), jsGet(saved, "subscription")
			if err := emit(ctx, surfaceEvent(workflowSavedCard(wf, jsGet(saved, "action")))); err != nil {
				return err
			}
			if jsTruthy(sub) {
				if err := emit(ctx, surfaceEvent(subscriptionSavedCard(sub, jsLookup(wf, "name")))); err != nil {
					return err
				}
			} else if te := jsGet(saved, "triggerError"); jsTruthy(te) {
				msg := "workflow saved, but its data trigger was not wired (" + jsTemplate(jsLookup(te, "code")) + "): " + jsTemplate(jsLookup(te, "message"))
				if err := emit(ctx, jsObjOf("type", "error", "message", msg)); err != nil {
					return err
				}
			}
		}
	} else if len(workflow.Errors) > 0 {
		if err := emit(ctx, jsObjOf("type", "error", "message", "workflow save fence invalid: "+strings.Join(workflow.Errors, "; "))); err != nil {
			return err
		}
	}

	rule := extractFence(source, ruleSaveFence, policySaveShape, true)
	if p := rule.first(); p != nil {
		description := p.vals["description"]
		if description == nil {
			description = ""
		}
		var role any
		if p.vals["approver_role"] == "admin" {
			role = "admin"
		}
		in := jsObjOf("orgId", o.OrgID, "name", p.vals["name"], "description", description,
			"appliesToKinds", p.vals["applies_to_kinds"], "appliesToScopes", p.vals["applies_to_scopes"], "mode", p.vals["mode"],
			"riskThresholdAutoApprove", p.vals["risk_threshold_auto_approve"], "allowedTargets", p.vals["allowed_targets"],
			"deniedTargets", p.vals["denied_targets"], "limits", p.vals["limits"], "approverRole", role,
			"priority", p.vals["priority"], "enabled", p.vals["enabled"], "createdByThreadId", o.ThreadID, "createdByRunId", o.RunID)
		saved, err := rt.upsertPolicy(ctx, in)
		if err != nil {
			return emit(ctx, jsObjOf("type", "error", "message", "policy save failed: "+err.Error()))
		}
		return emit(ctx, surfaceEvent(policySavedCard(jsGet(saved, "policy"), jsGet(saved, "action"))))
	}
	if len(rule.Errors) > 0 {
		return emit(ctx, jsObjOf("type", "error", "message", "policy save fence invalid: "+strings.Join(rule.Errors, "; ")))
	}
	return nil
}

// fenceAction is handleWorkActionRequest() for one action fence. It asks the
// worker to store the request, as the action tool does, so the run's action
// grants apply.
func (t *chatTurn) fenceAction(ctx context.Context, rt *runTools, p *jsObj) error {
	o := t.o
	scope, kind, summary := p.vals["scope"].(string), p.vals["kind"].(string), p.vals["summary"].(string)
	decision, err := rt.evaluatePolicy(ctx, policyIn{OrgID: o.OrgID, Scope: scope, Kind: kind,
		Target: optionalString(p, "target"), Payload: p.vals["payload"], RiskLevel: optionalString(p, "risk_level")})
	if err != nil {
		return err
	}
	switch d, _ := jsField(decision, "decision"); d {
	case "deny", "no_policy":
		return nil
	}
	status := "pending_approval"
	if d, _ := jsField(decision, "decision"); d == "allow" {
		status = "approved"
	}
	policyID, _ := jsField(jsGet(decision, "policy"), "id")
	minutes, hasMinutes := p.vals["minutes_saved"]
	payload := p.vals["payload"]
	if payload == nil {
		payload = &jsObj{}
	}
	in := jsObjOf("orgId", o.OrgID, "workRunId", o.RunID, "policyId", policyID, "scope", scope, "kind", kind,
		"target", p.vals["target"], "payload", payload, "riskLevel", p.vals["risk_level"], "summary", summary,
		"intent", summary, "minutesSaved", clampActionMinutes(minutes, hasMinutes), "minutesSavedBasis", p.vals["basis"],
		"status", status)
	request, err := rt.createActionRequest(ctx, in)
	if err != nil {
		return err
	}
	id, _ := jsField(request, "id")
	stored, _ := jsField(request, "status")
	event := jsObjOf("type", "action_request_emit", "action_request_id", id, "kind", kind, "scope", scope,
		"decision", pick(stored == "approved", "auto_approved", "pending_approval"), "intent", summary, "summary", summary)
	if risk, ok := p.vals["risk_level"]; ok {
		event.set("risk_level", risk)
	}
	if err := t.st.wrapped(ctx, event); err != nil {
		return emitError{err}
	}
	if stored == "approved" {
		return rt.enqueueAction(ctx, actionRefIn{OrgID: o.OrgID, ActionRequestID: jsStringOf(id)})
	}
	return nil
}

func (a *API) agentBackendWithKeys(ctx context.Context, orgID string) (*llm.AxBackendConfig, map[string]string, error) {
	backend, err := llm.ResolveAgentBackend(ctx, a.DB, orgID)
	if err != nil {
		return nil, nil, err
	}
	token := a.GoogleToken
	if token == nil {
		token = llm.GoogleToken
	}
	keys, err := llm.AgentModelKeys(ctx, a.DB, a.SecretsDir, orgID, token)
	return backend, keys, err
}

// finishWorkRun is finishWorkRun() in packages/llm/src/work/store.ts: a
// terminal state is final, and each change leaves an audit row.
func (a *API) finishWorkRun(ctx context.Context, runID, status string, runErr *string) error {
	var orgID string
	err := a.DB.QueryRow(ctx, `update work_run set status = $2, error = $3, updated_at = now(), finished_at = now()
		where id::text = $1 and status in ('queued', 'running') returning org_id`, runID, status, runErr).Scan(&orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	var payloadErr any
	if runErr != nil {
		payloadErr = *runErr
	}
	a.recordAudit(ctx, auditEvent{OrgID: orgID, EntityKind: "work_run", EntityID: runID, Event: "run:" + status,
		Payload: map[string]any{"status": status, "error": payloadErr}})
	return nil
}

// saveAssistantMessage is saveAssistantWorkMessage(): one assistant row per
// run, replaced when the run writes it again.
func (a *API) saveAssistantMessage(ctx context.Context, orgID, threadID, runID, content string) error {
	tag, err := a.DB.Exec(ctx, `update work_message set content = $4 where id = (select id from work_message
		where org_id = $1 and thread_id = $2 and run_id = $3 and role = 'assistant' limit 1)`, orgID, threadID, runID, content)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := a.DB.Exec(ctx, `insert into work_message (org_id, thread_id, run_id, role, content) values ($1, $2, $3, 'assistant', $4)`,
			orgID, threadID, runID, content); err != nil {
			return err
		}
	}
	_, err = a.DB.Exec(ctx, `update work_thread set updated_at = now(), last_message_at = now() where id = $1`, threadID)
	return err
}

func (a *API) storedRunEvents(ctx context.Context, orgID, runID string) ([]any, error) {
	rows, err := a.DB.Query(ctx, `select payload::text from work_run_event where org_id = $1 and run_id = $2 order by id`, orgID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if v, err := jsParse([]byte(raw)); err == nil {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

type botKeepInput struct {
	OrgID, ThreadID string
	Bot             threadBot
	Message, Reply  string
	Earlier         []string
}

var instructionsKeeper = llm.Program{
	Signature:   `currentInstructions?:string "the bot's job description now, absent when it has none", earlierOperatorMessages?:string "the operator's earlier messages, absent when the bot already has a description", operatorMessage:string "what the operator said this turn", botReply:string "the end of the bot's reply" -> changed:boolean "true only when the job changed", instructions:string "the whole job description after this turn"`,
	Instruction: `You keep a bot's job description current. Read the operator's message, the bot's reply, the current job description and, for a bot with no description yet, the operator's earlier messages in this chat. Set changed to true only when the operator says what they want the bot to do for them, changes or drops part of that, or asks for the same kind of work again. A greeting, a thank-you or a one-off question is not a change. Earlier messages that ask for the same kind of work count as a job. When changed is true, write the whole new job description: 1 to 4 plain sentences that start with a verb and call the operator "you", such as "Watches stock levels and tells you which products to reorder." Keep every part of the current description that still holds, including wording the operator wrote. When changed is false, return the current description unchanged.`,
}

// keepBotInstructions is keepBotInstructionsCurrent() in
// packages/llm/src/work/bot-instructions.ts.
func (a *API) keepBotInstructions(ctx context.Context, in botKeepInput) error {
	message := jsTrim(in.Message)
	if message == "" {
		return nil
	}
	current := jsTrim(in.Bot.Instructions)
	values := map[string]any{"operatorMessage": message, "botReply": utf16Tail(jsTrim(in.Reply), 1500)}
	if current != "" {
		values["currentInstructions"] = current
	} else {
		var earlier []string
		for _, m := range in.Earlier {
			if m = jsSlice(jsTrim(m), 400); m != "" {
				earlier = append(earlier, m)
			}
		}
		earlier = earlier[max(0, len(earlier)-6):]
		for i, m := range earlier {
			earlier[i] = "- " + m
		}
		if len(earlier) > 0 {
			values["earlierOperatorMessages"] = strings.Join(earlier, "\n")
		}
	}
	res, err := a.hostCalls().Generate(ctx, in.OrgID, "bot_instructions", instructionsKeeper, values)
	if err != nil {
		return err
	}
	next := jsSlice(jsTrim(jsValueString(res.Outputs["instructions"])), botInstructionsMax)
	if res.Outputs["changed"] != true || next == "" || next == current {
		return nil
	}
	_, err = a.DB.Exec(ctx, `update bot set instructions = $3, updated_at = now() where org_id = $1 and thread_id = $2`,
		in.OrgID, in.ThreadID, jsSlice(jsTrim(next), botInstructionsMax))
	return err
}
