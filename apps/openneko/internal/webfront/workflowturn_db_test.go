package webfront

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	wfTurnWorkflow = "e3000000-0000-4000-8000-000000000001"
	wfTurnRun      = "e3100000-0000-4000-8000-000000000001"
	wfTurnThread   = "e3200000-0000-4000-8000-000000000001"
	wfTurnWorkRun  = "e3300000-0000-4000-8000-000000000001"
)

// workflowTurnFront seeds a bot whose chat made a workflow, and one queued
// run of that workflow on its own thread.
func workflowTurnFront(t *testing.T, steps string) (*API, *pgxpool.Pool, workflowRecord) {
	t.Helper()
	f, db, wf := workflowTurnFrontHTTP(t, steps)
	return f.API, db, wf
}

func workflowTurnFrontHTTP(t *testing.T, steps string) (*Front, *pgxpool.Pool, workflowRecord) {
	t.Helper()
	f, db, _ := chatTurnFront(t)
	seed(t, db, `insert into bot (org_id, owner_user_id, name, coat, pattern, thread_id, instructions)
		values ('org', 'member', 'Stock watcher', 'ink', 'solid', $1, '')`, threadMine)
	seed(t, db, `insert into workflow_definition (id, org_id, name, description, goal, steps, network_hosts, created_by_thread_id)
		values ($1, 'org', 'Low stock', 'Flags low SKUs', 'Keep stock up', $2::jsonb, '{api.example.com}', $3)`, wfTurnWorkflow, steps, threadMine)
	seed(t, db, `insert into work_thread (id, org_id, title, channel) values ($1, 'org', 'Low stock', 'workflow')`, wfTurnThread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_role) values ($1, 'org', $2, 'ax', 'service')`, wfTurnWorkRun, wfTurnThread)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload)
		values ($1, 'org', $2, $3, $4, 'cron', '{"store":"north"}')`, wfTurnRun, wfTurnWorkflow, wfTurnThread, wfTurnWorkRun)
	wf, err := scanWorkflowRecord(db.QueryRow(context.Background(), `select `+workflowRecordColumns+` from workflow_definition where id = $1`, wfTurnWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	return f, db, wf
}

func workflowTurnOpts(wf workflowRecord, log *eventLog) workflowTurnOptions {
	payload, _ := jsParse([]byte(`{"store":"north"}`))
	return workflowTurnOptions{Workflow: wf, WorkflowRunID: wfTurnRun, TriggerKind: "cron", ThreadID: wfTurnThread, RunID: wfTurnWorkRun,
		TriggerPayload: payload, Mode: "headless", IncludeUxMetadata: true, Emit: log.emit, resolve: fixedBackend("http://model.invalid")}
}

func runRow(t *testing.T, db *pgxpool.Pool) (workStatus, workErr, wfStatus, wfSummary, wfErr string) {
	t.Helper()
	ctx := context.Background()
	var we, ws, wfe *string
	if err := db.QueryRow(ctx, `select status, error from work_run where id = $1`, wfTurnWorkRun).Scan(&workStatus, &we); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select status, summary, error from workflow_run where id = $1`, wfTurnRun).Scan(&wfStatus, &ws, &wfe); err != nil {
		t.Fatal(err)
	}
	return workStatus, deref(we), wfStatus, deref(ws), deref(wfe)
}

func TestWorkflowTurnThroughServe(t *testing.T) {
	a, db, wf := workflowTurnFront(t, `[{"id":"s1","description":"Read stock"},{"id":"s2","description":"Flag SKUs"}]`)
	answer := "Two SKUs are below the floor.\n```neko_value\n" + `{"minutes_saved":20,"basis":"stock check"}` + "\n```"
	model := fakeModel(t, []string{
		`{"javascriptCode":"final('Check stock', {})"}`,
		`{"javascriptCode":"final('Report', {status:'low'});"}`,
		answer,
	})
	t.Setenv("OPENNEKO_SERVE_SOCKET", startServe(t))
	log := &eventLog{}
	o := workflowTurnOpts(wf, log)
	o.resolve = fixedBackend(model.URL)
	o.TriggerInput, o.HasTriggerInput = jsObjOf("rows", []any{1.0, 2.0}), true
	res, err := a.runWorkflowTurn(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	events := log.all()
	if res.Status != "completed" || res.FinalText != "Two SKUs are below the floor." {
		t.Fatalf("result %+v\nevents %s", res, strings.Join(events, "\n"))
	}
	for i, want := range []string{`{"type":"status","message":"Starting workflow \"Low stock\" (cron)…"}`,
		`{"type":"status","message":"Preparing secure agent workspace…"}`, `{"type":"status","message":"Agent is working…"}`} {
		if events[i] != want {
			t.Fatalf("event %d: %s", i, events[i])
		}
	}
	if last := events[len(events)-1]; last != `{"type":"done","result":{"status":"completed","minutesSaved":20}}` {
		t.Fatalf("last event %s", last)
	}
	for _, e := range events {
		if strings.Contains(e, "org-stored-key") {
			t.Fatalf("an event holds the model key: %s", e)
		}
	}
	workStatus, workErr, wfStatus, summary, wfErr := runRow(t, db)
	if workStatus != "completed" || workErr != "" || wfStatus != "completed" || summary != "Two SKUs are below the floor." || wfErr != "" {
		t.Fatalf("rows: %s %q %s %q %q", workStatus, workErr, wfStatus, summary, wfErr)
	}
	if got := rowText(t, db, `select analysis_minutes_saved::text from work_run where id = $1`, wfTurnWorkRun); got != "20" {
		t.Fatalf("minutes saved %s", got)
	}
	if got := rowText(t, db, `select content from work_message where thread_id = $1 and run_id = $2 and role = 'assistant'`, wfTurnThread, wfTurnWorkRun); got != res.FinalText {
		t.Fatalf("assistant message %q", got)
	}
	if got := rowText(t, db, `select content from work_message where thread_id = $1 and run_id is null`, threadMine); got != "**Low stock**\n\nTwo SKUs are below the floor." {
		t.Fatalf("bot chat post %q", got)
	}
	ws, err := turnWorkspace("org", wfTurnThread, wfTurnWorkRun)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(ws.RunRoot, workflowAPIInputFile)); err != nil || string(raw) != "{\n  \"rows\": [\n    1,\n    2\n  ]\n}\n" {
		t.Fatalf("trigger input file %q %v", raw, err)
	}
	if left, _ := os.ReadDir(agentRunsRoot()); len(left) != 0 {
		t.Fatalf("stage left behind: %v", left)
	}
}

func TestWorkflowTurnOutcomes(t *testing.T) {
	t.Run("artifacts, prompt and seed", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[{"id":"s1","description":"Read stock"}]`)
		log := &eventLog{}
		o := workflowTurnOpts(wf, log)
		var got agentCoreInput
		o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			got = in
			if err := os.WriteFile(filepath.Join(in.Workspace.ArtifactRoot, "low.csv"), []byte("sku\n"), 0o644); err != nil {
				return axRunResult{}, err
			}
			_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Streamed text."))
			return axRunResult{Status: "completed"}, nil
		}
		res, err := a.runWorkflowTurn(context.Background(), o)
		if err != nil || res.Status != "completed" || res.FinalText != "Streamed text." {
			t.Fatalf("%+v %v", res, err)
		}
		w := got.Workflow
		if w == nil || w.WorkflowRunID != wfTurnRun || strings.Join(w.Servers, ",") != "neko_graphjin,neko_workflow_output,neko_action,neko_memory" ||
			strings.Join(w.NetworkHosts, ",") != "api.example.com" || w.TimeoutMs != 15*60_000 || w.MaxToolIterations != 150 || w.ReasoningEffort != "medium" {
			t.Fatalf("core input %+v", w)
		}
		for _, want := range []string{"<name>Low stock</name>", "<goal>Keep stock up</goal>", "  1. Read stock", "<mode>headless</mode>",
			"- [" + memTeam + "] business_rule (global): Revenue is net of refunds", got.Workspace.ArtifactRoot} {
			if !strings.Contains(got.Prompt, want) {
				t.Errorf("prompt lacks %q", want)
			}
		}
		if !strings.HasPrefix(got.UserMessage, "[scheduled run started at ") || !strings.Contains(got.UserMessage, "<trigger_input>\n{\n  \"store\": \"north\"\n}") {
			t.Fatalf("seed %q", got.UserMessage)
		}
		if !strings.Contains(strings.Join(log.all(), "\n"), `"type":"artifact","artifact":{"path":"runs/`+wfTurnWorkRun+`/artifacts/low.csv","label":"low.csv"}`) {
			t.Fatalf("no artifact event: %s", strings.Join(log.all(), "\n"))
		}
		if _, _, wfStatus, summary, _ := runRow(t, db); wfStatus != "completed" || summary != "Streamed text." {
			t.Fatalf("workflow run %s %q", wfStatus, summary)
		}
	})

	t.Run("an empty answer", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[]`)
		seed(t, db, `insert into workflow_output (org_id, workflow_run_id, work_run_id, kind, title, body) values ('org', $1, $2, 'report', 'Stock report', ' Three SKUs low. ')`,
			wfTurnRun, wfTurnWorkRun)
		o := workflowTurnOpts(wf, &eventLog{})
		o.runCore = func(context.Context, agentCoreInput) (axRunResult, error) {
			return axRunResult{Status: "completed"}, nil
		}
		if res, err := a.runWorkflowTurn(context.Background(), o); err != nil || res.FinalText != "" {
			t.Fatalf("%+v %v", res, err)
		}
		if _, _, _, summary, _ := runRow(t, db); summary != "Looked at the data; nothing to flag." {
			t.Fatalf("summary %q", summary)
		}
		if n := rowText(t, db, `select count(*)::text from work_message where run_id = $1`, wfTurnWorkRun); n != "0" {
			t.Fatalf("%s assistant rows", n)
		}
		if got := rowText(t, db, `select content from work_message where thread_id = $1 and run_id is null`, threadMine); got != "**Stock report**\n\nThree SKUs low." {
			t.Fatalf("bot chat post %q", got)
		}
	})

	t.Run("a question pauses the run", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[]`)
		log := &eventLog{}
		o := workflowTurnOpts(wf, log)
		o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Which store?"))
			_ = in.Emit(ctx, jsObjOf("type", "needs_input", "question", "Which store?"))
			return axRunResult{Status: "completed", FinalText: "Which store?"}, nil
		}
		res, err := a.runWorkflowTurn(context.Background(), o)
		if err != nil || res.Status != "needs_input" || res.FinalText != "Which store?" {
			t.Fatalf("%+v %v", res, err)
		}
		workStatus, workErr, wfStatus, summary, wfErr := runRow(t, db)
		if workStatus != "failed" || workErr != "" || wfStatus != "needs_input" || summary != "Which store?" || wfErr != "" {
			t.Fatalf("rows: %s %q %s %q %q", workStatus, workErr, wfStatus, summary, wfErr)
		}
		if events := log.all(); events[len(events)-1] != `{"type":"done","result":{"status":"needs_input"}}` {
			t.Fatalf("events %s", strings.Join(events, "\n"))
		}
		if got := rowText(t, db, `select content from work_message where thread_id = $1 and run_id is null`, threadMine); got != "**Low stock is waiting for input.**\n\nWhich store?" {
			t.Fatalf("bot chat post %q", got)
		}
	})

	t.Run("a thrown error fails the run", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[]`)
		log := &eventLog{}
		o := workflowTurnOpts(wf, log)
		boom := errors.New("tool server would not start")
		o.runCore = func(context.Context, agentCoreInput) (axRunResult, error) { return axRunResult{}, boom }
		if _, err := a.runWorkflowTurn(context.Background(), o); !errors.Is(err, boom) {
			t.Fatalf("err %v", err)
		}
		workStatus, workErr, wfStatus, _, wfErr := runRow(t, db)
		if workStatus != "failed" || workErr != boom.Error() || wfStatus != "failed" || wfErr != boom.Error() {
			t.Fatalf("rows: %s %q %s %q", workStatus, workErr, wfStatus, wfErr)
		}
		events := log.all()
		if events[len(events)-2] != `{"type":"error","message":"tool server would not start"}` || events[len(events)-1] != `{"type":"done","result":{"status":"failed"}}` {
			t.Fatalf("events %s", strings.Join(events, "\n"))
		}
		if got := rowText(t, db, `select content from work_message where thread_id = $1 and run_id is null`, threadMine); got != "**Low stock failed.**\n\ntool server would not start" {
			t.Fatalf("bot chat post %q", got)
		}
	})

	t.Run("a cancel", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[]`)
		log := &eventLog{}
		o := workflowTurnOpts(wf, log)
		ctx, cancel := context.WithCancelCause(context.Background())
		o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Half"))
			cancel(nil)
			return axRunResult{}, ctx.Err()
		}
		res, err := a.runWorkflowTurn(ctx, o)
		if err != nil || res.Status != "cancelled" || res.FinalText != "Half" || res.Error != "" {
			t.Fatalf("%+v %v", res, err)
		}
		workStatus, workErr, wfStatus, summary, wfErr := runRow(t, db)
		if workStatus != "cancelled" || workErr != "" || wfStatus != "cancelled" || summary != "Half" || wfErr != "" {
			t.Fatalf("rows: %s %q %s %q %q", workStatus, workErr, wfStatus, summary, wfErr)
		}
		if !strings.Contains(strings.Join(log.all(), "\n"), `{"type":"error","message":"Cancelled by user."}`) {
			t.Fatalf("events %s", strings.Join(log.all(), "\n"))
		}
		if got := rowText(t, db, `select content from work_message where thread_id = $1 and run_id is null`, threadMine); got != "**Low stock stopped.**" {
			t.Fatalf("bot chat post %q", got)
		}
	})

	t.Run("the spend stop ends a thrown turn", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[]`)
		log := &eventLog{}
		o := workflowTurnOpts(wf, log)
		ctx, cancel := context.WithCancelCause(context.Background())
		o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			cancel(&spendCapExceeded{capUSD: 2, spentUSD: 2.5})
			return axRunResult{}, ctx.Err()
		}
		res, err := a.runWorkflowTurn(ctx, o)
		msg := "The run exceeded its $2.00 spend cap ($2.50 spent)."
		if err != nil || res.Status != "failed" || res.Error != msg {
			t.Fatalf("%+v %v", res, err)
		}
		if workStatus, workErr, wfStatus, _, wfErr := runRow(t, db); workStatus != "failed" || workErr != msg || wfStatus != "failed" || wfErr != msg {
			t.Fatalf("rows: %s %q %s %q", workStatus, workErr, wfStatus, wfErr)
		}
		if !strings.Contains(strings.Join(log.all(), "\n"), `{"type":"error","message":"`+msg+`"}`) {
			t.Fatalf("events %s", strings.Join(log.all(), "\n"))
		}
	})

	t.Run("the spend stop", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[]`)
		log := &eventLog{}
		o := workflowTurnOpts(wf, log)
		ctx, cancel := context.WithCancelCause(context.Background())
		o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			cancel(&spendCapExceeded{capUSD: 5, spentUSD: 5.25})
			return axRunResult{Status: "cancelled"}, nil
		}
		res, err := a.runWorkflowTurn(ctx, o)
		msg := "The run exceeded its $5.00 spend cap ($5.25 spent)."
		if err != nil || res.Status != "failed" || res.Error != msg || res.ErrorCode != "spend_limit" {
			t.Fatalf("%+v %v", res, err)
		}
		workStatus, workErr, wfStatus, _, wfErr := runRow(t, db)
		if workStatus != "failed" || workErr != msg || wfStatus != "failed" || wfErr != msg {
			t.Fatalf("rows: %s %q %s %q", workStatus, workErr, wfStatus, wfErr)
		}
		if events := log.all(); events[len(events)-1] != `{"type":"done","result":{"status":"failed","minutesSaved":0}}` {
			t.Fatalf("events %s", strings.Join(events, "\n"))
		}
	})

	t.Run("a turn that times out continues", func(t *testing.T) {
		a, db, wf := workflowTurnFront(t, `[]`)
		log := &eventLog{}
		o := workflowTurnOpts(wf, log)
		var prompts []string
		o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			prompts = append(prompts, in.Prompt)
			if len(prompts) == 1 {
				_ = in.Emit(ctx, jsObjOf("type", "tool_start", "id", "t1", "name", "mcp_neko_graphjin_execute_graphql", "input", jsObjOf("query", "{ stock { sku } }")))
				_ = in.Emit(ctx, jsObjOf("type", "error", "message", "ax turn exceeded its 900s budget and was terminated"))
				return axRunResult{Status: "failed", Error: "ax turn exceeded its 900s budget", TimedOut: true}, nil
			}
			return axRunResult{Status: "completed", FinalText: "Done."}, nil
		}
		if res, err := a.runWorkflowTurn(context.Background(), o); err != nil || res.Status != "completed" {
			t.Fatalf("%+v %v", res, err)
		}
		if len(prompts) != 2 || !strings.HasSuffix(prompts[1], "Last actions of the earlier turn:\n- mcp_neko_graphjin_execute_graphql {\"query\":\"{ stock { sku } }\"}\n</continuation>") {
			t.Fatalf("prompts %q", prompts)
		}
		events := strings.Join(log.all(), "\n")
		if !strings.Contains(events, `"Turn budget reached; continuing the run (1/2)"`) || strings.Contains(events, "exceeded its") {
			t.Fatalf("events %s", events)
		}
		if workStatus, _, _, _, _ := runRow(t, db); workStatus != "completed" {
			t.Fatalf("work run %s", workStatus)
		}
	})

	t.Run("an API run gets no continuation", func(t *testing.T) {
		a, _, wf := workflowTurnFront(t, `[]`)
		o := workflowTurnOpts(wf, &eventLog{})
		o.TriggerKind, o.TimeoutMs, o.MaxToolIterations, o.CardsOff = "api", 90_000, 12, true
		calls := 0
		var got agentCoreInput
		o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			calls++
			got = in
			return axRunResult{Status: "failed", Error: "ax turn exceeded its 90s budget", TimedOut: true}, nil
		}
		if res, err := a.runWorkflowTurn(context.Background(), o); err != nil || res.Status != "failed" || calls != 1 {
			t.Fatalf("%+v %v after %d calls", res, err, calls)
		}
		if got.Workflow.TimeoutMs != 90_000 || got.Workflow.MaxToolIterations != 12 || !got.CardsOff || !got.CoreCards {
			t.Fatalf("core input %+v %+v", got.Workflow, got)
		}
	})
}

func TestWorkflowTurnScriptSteps(t *testing.T) {
	steps := `[{"id":"pull","description":"Pull stock","script":{"skill":"stockcheck","command":["python3","${SKILL_DIR}/run.py","${input.store}"]}},
		{"id":"note","description":"Read the sheet"}]`
	a, db, wf := workflowTurnFront(t, steps)
	ws, err := turnWorkspace("org", wfTurnThread, wfTurnWorkRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.SkillsRoot, "stockcheck"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"SKILL.md": "---\nname: stockcheck\ndescription: Checks stock\n---\n", "run.py": "print('ok')\n"} {
		if err := os.WriteFile(filepath.Join(ws.SkillsRoot, "stockcheck", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeStepSandbox{answers: []sandboxAnswerFixture{{ExitCode: 0, Output: "rows: 3\n"}}}
	fake.answers[0].Changed = append(fake.answers[0].Changed, struct {
		Path string `json:"path"`
		Mode int    `json:"mode"`
		Data string `json:"data"`
	}{Path: "skills/stockcheck/NOTES.md", Mode: 0o644, Data: base64.StdEncoding.EncodeToString([]byte("Stores: north, south\n"))})
	box := httptest.NewServer(fake)
	defer box.Close()
	t.Setenv("OPENNEKO_SANDBOX_URL", box.URL)

	log := &eventLog{}
	o := workflowTurnOpts(wf, log)
	var prompt string
	o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		prompt = in.Prompt
		return axRunResult{Status: "completed", FinalText: "Three SKUs are low."}, nil
	}
	if res, err := a.runWorkflowTurn(context.Background(), o); err != nil || res.Status != "completed" {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(prompt, "  1. Pull stock (script step: already ran; see <step_results>)") ||
		!strings.Contains(prompt, `<step id="pull" status="ok" exit_code="0" duration_s="0">`) || !strings.Contains(prompt, "output_tail:\nrows: 3\n</step>") {
		t.Fatalf("prompt lacks the step results:\n%s", prompt[max(0, len(prompt)-1500):])
	}
	fake.mu.Lock()
	seen := fake.seen
	fake.mu.Unlock()
	if len(seen) != 1 || !strings.Contains(seen[0].Command, "exec 'python3' '"+agentRunsRoot()+"/") || !strings.HasSuffix(seen[0].Command, "/skills/stockcheck/run.py' 'north'") {
		t.Fatalf("requests %+v", seen)
	}
	events := strings.Join(log.all(), "\n")
	for _, want := range []string{`"Agent is working…"}`, `{"type":"status","message":"Running script step: Pull stock"}`, `{"type":"status","message":"Script step pull ok in 0s"}`} {
		if !strings.Contains(events, want) {
			t.Fatalf("events lack %s:\n%s", want, events)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(ws.SkillsRoot, "stockcheck", "NOTES.md")); err != nil || string(raw) != "Stores: north, south\n" {
		t.Fatalf("the script step's skill edit was not saved: %q %v", raw, err)
	}
	if workStatus, workErr, _, _, _ := runRow(t, db); workStatus != "completed" || workErr != "" {
		t.Fatalf("work run %s %q", workStatus, workErr)
	}
}
