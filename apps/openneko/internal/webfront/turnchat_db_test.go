package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) emit(_ context.Context, e any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, jsStringify(e, false))
	return nil
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.events...)
}

func (l *eventLog) count(fragment string) int {
	n := 0
	for _, e := range l.all() {
		if strings.Contains(e, fragment) {
			n++
		}
	}
	return n
}

func fakeModel(t *testing.T, responses []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		content := responses[min(calls, len(responses)-1)]
		calls++
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("content-type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0,
				"delta": map[string]any{"role": "assistant", "content": content}}}})
			end, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: " + string(end) + "\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(model.Close)
	return model
}

func startServe(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "chat")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "serve.sock")
	ln, err := serve.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: 2}
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { stop(); <-done; os.RemoveAll(dir) })
	return socket
}

func chatTurnFront(t *testing.T) (*Front, *pgxpool.Pool, *fakeActionWorker) {
	t.Helper()
	f, db := memoryFront(t)
	clearAxEnv(t, nil)
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	seedRiver(t, db, f)
	worker := &fakeActionWorker{db: db}
	srv := httptest.NewServer(worker)
	t.Cleanup(srv.Close)
	f.API.Gate.WorkerURL = srv.URL
	seed(t, db, `delete from action_policy where org_id = 'org'`)
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority, enabled) values
		('org', 'external_default', '{}', '{external}', 'approval_required', 950, true),
		('org', 'slack_auto', '{slack_post}', '{}', 'auto_approve', 100, true)`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'ax', 'admin', 'admin')`,
		runAdmin, threadMine)
	return f, db, worker
}

func fixedBackend(url string) func(context.Context, string) (*llm.AxBackendConfig, map[string]string, error) {
	return func(context.Context, string) (*llm.AxBackendConfig, map[string]string, error) {
		return &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai-compatible", Model: "fixture", URL: url, KeyEnv: "OPENAI_API_KEY"}},
			map[string]string{"OPENAI_API_KEY": "org-stored-key"}, nil
	}
}

func rowText(t *testing.T, db *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var out *string
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return deref(out)
}

func TestChatTurnThroughServe(t *testing.T) {
	f, db, worker := chatTurnFront(t)
	a := f.API
	seed(t, db, `insert into bot (org_id, owner_user_id, name, coat, pattern, thread_id, instructions)
		values ('org', 'member', 'Stock watcher', 'ink', 'solid', $1, '')`, threadMine)
	seed(t, db, `insert into work_message (org_id, thread_id, run_id, role, content, created_at) values
		('org', $1, $2, 'user', 'Watch the stock for me', '2026-11-01T08:00:00Z'),
		('org', $1, $3, 'user', 'Summarise stock', '2026-11-01T09:00:00Z')`, threadMine, runMine, runAdmin)

	answer := strings.Join([]string{
		"Stock is fine.",
		"```neko_action_request\n" + `{"scope":"external","kind":"slack_post","target":"C1","payload":{"text":"Stock is fine"},"summary":"Post the stock summary","status":"approved","decision":"allow"}` + "\n```",
		"```neko_workflow_save\n" + `{"name":"Daily stock","steps":[{"id":"s1","description":"Check stock levels"}]}` + "\n```",
		"```neko_rule_save\n" + `{"name":"Ask before email","applies_to_kinds":["email"],"mode":"approval_required"}` + "\n```",
		"```neko_value\n" + `{"minutes_saved":12,"basis":"two reports"}` + "\n```",
		"```neko_followups\n" + `{"followups":["By region?"]}` + "\n```",
		"```neko_vitals\n" + `{"vitals":[{"label":"SKUs low","value":"3"}]}` + "\n```",
		"```neko_memory\n" + `[{"save":{"text":"Stock counts come from the ERP"}}]` + "\n```",
		"call:default_api:read_file{limit:100}",
	}, "\n")
	model := fakeModel(t, []string{
		`{"javascriptCode":"final('Summarise stock', {})"}`,
		`{"javascriptCode":"final('Report', {status:'fine'});"}`,
		answer,
	})
	t.Setenv("OPENNEKO_SERVE_SOCKET", startServe(t))

	ws, err := turnWorkspace("org", threadMine, runAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.ArtifactRoot, "stock.csv"), []byte("sku,qty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &eventLog{}
	kept := make(chan botKeepInput, 1)
	res, err := a.runChatTurn(context.Background(), chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runAdmin,
		Message: "Summarise stock", Channel: "web", IncludeUxMetadata: true, NativeDelegationOff: true, Emit: log.emit,
		Summarize: noSummary(t), resolve: fixedBackend(model.URL),
		keepBot: func(_ context.Context, in botKeepInput) error { kept <- in; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	events := log.all()
	if res.Status != "completed" || res.FinalText != "Stock is fine." {
		t.Fatalf("result %+v\nevents %s", res, strings.Join(events, "\n"))
	}
	for i, want := range []string{`{"type":"status","message":"Starting Ax…"}`, `{"type":"status","message":"Retrieving relevant context…"}`,
		`{"type":"status","message":"Preparing secure agent workspace…"}`, `{"type":"status","message":"Agent is working…"}`} {
		if events[i] != want {
			t.Fatalf("event %d: %s", i, events[i])
		}
	}
	if last := events[len(events)-1]; last != `{"type":"done","result":{"status":"completed","minutesSaved":12}}` {
		t.Fatalf("last event %s", last)
	}
	for _, want := range []string{
		`{"type":"artifact","artifact":{"path":"runs/` + runAdmin + `/artifacts/stock.csv","label":"stock.csv"}}`,
		`"type":"action_request_emit"`, `"kind":"slack_post","scope":"external","decision":"auto_approved","intent":"Post the stock summary","summary":"Post the stock summary"}`,
		`"surfaceId":"workflow-save-`, `"surfaceId":"policy-save-`,
		`{"type":"followups","items":["By region?"]}`, `{"type":"vitals","items":[{"label":"SKUs low","value":"3"}]}`,
	} {
		if log.count(want) != 1 {
			t.Errorf("events lack %s\n%s", want, strings.Join(events, "\n"))
		}
	}
	for _, e := range events {
		if strings.Contains(e, "org-stored-key") {
			t.Fatalf("an event holds the model key: %s", e)
		}
	}
	if len(worker.creates) != 1 || worker.creates[0]["status"] != "approved" || worker.creates[0]["intent"] != "Post the stock summary" {
		t.Fatalf("worker creates %v", worker.creates)
	}
	for sql, want := range map[string]string{
		`select status || ':' || coalesce(error, '-') || ':' || analysis_minutes_saved || ':' || analysis_minutes_basis from work_run where id = '` + runAdmin + `'`: "completed:-:12:two reports",
		`select string_agg(content, '|') from work_message where run_id = '` + runAdmin + `' and role = 'assistant'`:                                                 "Stock is fine.",
		`select kind || ':' || status from action_request where work_run_id = '` + runAdmin + `'`:                                                                    "slack_post:approved",
		`select name from workflow_definition where org_id = 'org' and name = 'Daily stock'`:                                                                         "Daily stock",
		`select mode from action_policy where org_id = 'org' and name = 'Ask before email'`:                                                                          "approval_required",
		`select kind || ':' || pinned || ':' || scope from work_memory where text = 'Stock counts come from the ERP'`:                                                "business_rule:true:global",
		`select string_agg(event, ',') from audit_chain where entity_id = '` + runAdmin + `'`:                                                                        "run:completed",
	} {
		if got := rowText(t, db, sql); got != want {
			t.Errorf("%s\n got %q, want %q", sql, got, want)
		}
	}
	select {
	case in := <-kept:
		if in.Reply != "Stock is fine." || len(in.Earlier) != 1 || in.Earlier[0] != "Watch the stock for me" || in.Bot.Name != "Stock watcher" {
			t.Fatalf("bot keeper input %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bot keeper did not run")
	}
}

type fakeCore func(ctx context.Context, in agentCoreInput) (axRunResult, error)

func TestChatTurnOutcomes(t *testing.T) {
	f, db, worker := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	newRun := func(actor string) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', $2, $2) returning id::text`,
			threadMine, actor).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	run := func(ctx context.Context, runID string, core fakeCore) (chatTurnResult, error, *eventLog) {
		t.Helper()
		log := &eventLog{}
		res, err := a.runChatTurn(ctx, chatTurnOptions{OrgID: "org", ThreadID: threadMine, RunID: runID, Message: "Question",
			Channel: "web", IncludeUxMetadata: true, Emit: log.emit, Summarize: noSummary(t), resolve: fixedBackend("http://unused"),
			runCore: core, keepBot: func(context.Context, botKeepInput) error { return nil }})
		return res, err, log
	}
	row := func(runID string) string {
		return rowText(t, db, `select status || ':' || coalesce(error, '-') from work_run where id = $1`, runID)
	}
	assistant := func(runID string) string {
		return rowText(t, db, `select string_agg(content, '|') from work_message where run_id = $1 and role = 'assistant'`, runID)
	}
	last := func(l *eventLog) string { e := l.all(); return e[len(e)-1] }

	t.Run("a question ends the run", func(t *testing.T) {
		id := newRun("admin")
		res, err, log := run(ctx, id, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			_ = in.Emit(ctx, jsObjOf("type", "needs_input", "question", "Which store?", "options", []any{"North", "South"}))
			_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Answer behind the form"))
			_ = in.Emit(ctx, jsObjOf("type", "surface", "messages", []any{jsObjOf("createSurface", jsObjOf("surfaceId", "x"))}))
			return axRunResult{Status: "completed", FinalText: "Answer behind the form"}, nil
		})
		want := "Which store?\n- North\n- South"
		if err != nil || res.Status != "needs_input" || res.FinalText != want || row(id) != "needs_input:-" || assistant(id) != want {
			t.Fatalf("%+v %v %s %q", res, err, row(id), assistant(id))
		}
		if log.count("Answer behind the form") != 0 || log.count(`"type":"surface"`) != 0 || last(log) != `{"type":"done","result":{"status":"needs_input"}}` {
			t.Fatalf("events %v", log.all())
		}
	})

	t.Run("cancel", func(t *testing.T) {
		id := newRun("admin")
		cctx, cancel := context.WithCancel(ctx)
		res, err, log := run(cctx, id, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Partial"))
			cancel()
			return axRunResult{Status: "cancelled"}, nil
		})
		if err != nil || res.Status != "cancelled" || res.FinalText != "Partial" || res.Error != "" || row(id) != "cancelled:-" {
			t.Fatalf("%+v %v %s", res, err, row(id))
		}
		if log.count(`{"type":"error","message":"Cancelled by user."}`) != 1 || last(log) != `{"type":"done","result":{"status":"cancelled"}}` {
			t.Fatalf("events %v", log.all())
		}
	})

	t.Run("spend stop", func(t *testing.T) {
		id := newRun("admin")
		cctx, cancel := context.WithCancelCause(ctx)
		res, err, log := run(cctx, id, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			_ = in.Emit(ctx, jsObjOf("type", "tool_start", "id", "t1", "name", "neko_graphjin__query",
				"usageSnapshot", jsObjOf("coverage", "complete", "estimatedCostUsd", 1.5, "missingReasons", []any{"a"})))
			cancel(&spendCapExceeded{capUSD: 1, spentUSD: 1.5})
			return axRunResult{Status: "cancelled"}, nil
		})
		msg := "The run exceeded its $1.00 spend cap ($1.50 spent)."
		if err != nil || res.Status != "failed" || res.Error != msg || row(id) != "failed:"+msg {
			t.Fatalf("%+v %v %s", res, err, row(id))
		}
		if log.count(`{"type":"usage","source":"outer","usage":{"coverage":"partial","estimatedCostUsd":1.5,"missingReasons":["a","The turn ended early; usage covers the model calls made before its last tool call."]}}`) != 1 ||
			log.count(`{"type":"error","message":"`+msg+`"}`) != 1 || last(log) != `{"type":"done","result":{"status":"failed"}}` {
			t.Fatalf("events %v", log.all())
		}
	})

	t.Run("a turn with no answer fails", func(t *testing.T) {
		id := newRun("admin")
		_, err, log := run(ctx, id, func(context.Context, agentCoreInput) (axRunResult, error) {
			return axRunResult{Status: "completed", FinalText: "  "}, nil
		})
		if !errors.Is(err, errNoAnswer) || row(id) != "failed:"+errNoAnswer.Error() || last(log) != `{"type":"done","result":{"status":"failed"}}` {
			t.Fatalf("%v %s %v", err, row(id), log.all())
		}
	})

	t.Run("a core error fails the run", func(t *testing.T) {
		id := newRun("admin")
		_, err, _ := run(ctx, id, func(context.Context, agentCoreInput) (axRunResult, error) { return axRunResult{}, errors.New("boom") })
		if err == nil || err.Error() != "boom" || row(id) != "failed:boom" {
			t.Fatalf("%v %s", err, row(id))
		}
	})

	t.Run("a structured answer leaves a summary", func(t *testing.T) {
		id := newRun("admin")
		res, err, _ := run(ctx, id, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			_ = in.Emit(ctx, jsObjOf("type", "surface", "messages", []any{jsObjOf("createSurface", jsObjOf("surfaceId", "s",
				"components", []any{jsObjOf("id", "k", "component", "KeyFigures", "title", "Stock", "items", []any{jsObjOf("label", "Low", "value", 3.0)})}))}))
			return axRunResult{Status: "completed", HasRaw: true, RawText: "```neko_vitals\n{\"vitals\":[{\"label\":\"SKUs\",\"value\":\"40\"}]}\n```"}, nil
		})
		want := "Structured result from my previous turn:\n- Stock\n- Low: 3\nHeadline figures:\n- SKUs: 40"
		if err != nil || res.FinalText != want || assistant(id) != want {
			t.Fatalf("%+v %v %q", res, err, assistant(id))
		}
	})

	t.Run("a network denial is reported once per host", func(t *testing.T) {
		id := newRun("admin")
		_, err, log := run(ctx, id, func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			for i := 0; i < 2; i++ {
				_ = in.Emit(ctx, jsObjOf("type", "tool_end", "id", fmt.Sprint(i), "name", "web_fetch",
					"result", jsObjOf("text", "host api.example.com is not on the web allowlist")))
			}
			return axRunResult{Status: "completed", FinalText: "Done."}, nil
		})
		if err != nil || log.count(`{"type":"capability_denied","capability":"network_egress","reason":"policy_denied","host":"api.example.com","method":"GET"}`) != 1 {
			t.Fatalf("%v %v", err, log.all())
		}
	})

	t.Run("a fence meets the run's grants", func(t *testing.T) {
		seed(t, db, `delete from item_grant where org_id = 'org' and item_type in ('action', 'pack')`)
		fence := func(context.Context, agentCoreInput) (axRunResult, error) {
			return axRunResult{Status: "completed", FinalText: "Posted.\n```neko_action_request\n" +
				`{"scope":"external","kind":"slack_post","summary":"Post it"}` + "\n```\n```neko_workflow_save\n{\"name\":\"\"}\n```"}, nil
		}
		before := len(worker.creates)
		res, err, log := run(ctx, newRun("member"), fence)
		if err != nil || res.Status != "completed" || res.FinalText != "Posted." {
			t.Fatalf("%+v %v", res, err)
		}
		if len(worker.creates) != before || log.count("action_request_emit") != 0 {
			t.Fatalf("a member with no grant for slack_post created a request: %v", worker.creates)
		}
		if log.count(`{"type":"error","message":"workflow save fence invalid: [`) != 1 {
			t.Fatalf("events %v", log.all())
		}
		seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id) select 'org', id, 'action', 'slack_post' from user_group where org_id = 'org' and slug = 'everyone'`)
		if _, _, log = run(ctx, newRun("member"), fence); len(worker.creates) != before+1 || log.count(`"decision":"auto_approved"`) != 1 {
			t.Fatalf("with the grant the request is made: %v", log.all())
		}
	})

	t.Run("compaction is stored", func(t *testing.T) {
		thread := "c1000000-0000-4000-8000-000000000001"
		seed(t, db, `insert into work_thread (id, org_id, created_by_user_id, backend_state) values ($1, 'org', 'admin', '{"keep":1}')`, thread)
		for i := 0; i < compactAfterValue+3; i++ {
			seed(t, db, `insert into work_message (org_id, thread_id, role, content, created_at) values ('org', $1, $2, $3, $4)`,
				thread, pick(i%2 == 0, "user", "assistant"), fmt.Sprint("m", i), time.Date(2026, 11, 1, 8, 0, i, 0, time.UTC))
		}
		var id string
		if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, actor_user_id, actor_role) values ('org', $1, 'ax', 'admin', 'admin') returning id::text`,
			thread).Scan(&id); err != nil {
			t.Fatal(err)
		}
		log := &eventLog{}
		res, err := a.runChatTurn(ctx, chatTurnOptions{OrgID: "org", ThreadID: thread, RunID: id, Message: "Next", Channel: "web",
			Emit: log.emit, resolve: fixedBackend("http://unused"),
			Summarize: func(context.Context, string, string, string) (string, error) { return "Folded summary", nil },
			runCore: func(context.Context, agentCoreInput) (axRunResult, error) {
				return axRunResult{Status: "completed", FinalText: "Done."}, nil
			}})
		if err != nil || res.Status != "completed" {
			t.Fatalf("%+v %v", res, err)
		}
		state := rowText(t, db, `select backend_state::text from work_thread where id = $1`, thread)
		if !strings.Contains(state, `"keep": 1`) || !strings.Contains(state, `"summary": "Folded summary"`) {
			t.Fatalf("backend state %s", state)
		}
	})

	host := orgAgentRoot("org")
	report := filepath.Join(host, "skills", "report", "SKILL.md")
	readText := func(p string) string {
		data, err := os.ReadFile(p)
		if err != nil {
			return "<none>"
		}
		return string(data)
	}
	skillChange := func() string {
		return rowText(t, db, `select scope || ':' || user_id || ':' || summary || ':' || coalesce(commit_sha, '-') from config_change
			where org_id = 'org' and artifact_kind = 'skill' order by created_at desc, id desc limit 1`)
	}
	skillEdit := func(text, tool string, seen *agentWorkspace, prompt *string) fakeCore {
		return func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			if seen != nil {
				*seen = in.Workspace
			}
			if prompt != nil {
				*prompt = in.Prompt
			}
			writeTestFile(t, filepath.Join(in.Workspace.SkillsRoot, "report", "SKILL.md"), text)
			writeTestFile(t, filepath.Join(in.Workspace.ArtifactRoot, "out.csv"), "x")
			_ = in.Emit(ctx, jsObjOf("type", "tool_start", "id", "t1", "name", tool, "input", jsObjOf("path", "notes/a.md")))
			_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Done."))
			return axRunResult{Status: "completed", FinalText: "Done."}, nil
		}
	}

	t.Run("the turn works in a stage and saves an admin's skill edit for the team", func(t *testing.T) {
		writeTestFile(t, report, "# Team report\n")
		writeTestFile(t, filepath.Join(host, "memory", "secret.md"), "memory")
		id := newRun("admin")
		var ws agentWorkspace
		var prompt string
		res, err, log := run(ctx, id, skillEdit("# Better report\n", "terminal", &ws, &prompt))
		if err != nil || res.Status != "completed" {
			t.Fatalf("%+v %v", res, err)
		}
		if !strings.HasPrefix(ws.OrgRoot, os.Getenv("OPENNEKO_AGENT_RUNS_DIR")) || !strings.Contains(prompt, ws.SkillsRoot) || strings.Contains(prompt, host) {
			t.Fatalf("the turn did not run in the stage: %s", ws.OrgRoot)
		}
		if _, err := os.Stat(ws.OrgRoot); err == nil {
			t.Error("the stage is still there")
		}
		if got := readText(report); got != "# Better report\n" {
			t.Errorf("host skill %q", got)
		}
		if got, want := skillChange(), "team::Updated skill in run "+id[:8]+": report:"; !strings.HasPrefix(got, want) || strings.HasSuffix(got, ":-") {
			t.Errorf("config change %q", got)
		}
		if log.count(`{"type":"artifact","artifact":{"path":"runs/`+id+`/artifacts/out.csv","label":"out.csv"}}`) != 1 {
			t.Errorf("events %v", log.all())
		}
	})

	t.Run("a member's skill edit goes to their own version", func(t *testing.T) {
		writeTestFile(t, report, "# Team report\n")
		id := newRun("member")
		res, err, _ := run(ctx, id, skillEdit("# My report\n", "mcp__neko__create_skill", nil, nil))
		if err != nil || res.Status != "completed" {
			t.Fatalf("%+v %v", res, err)
		}
		if got := readText(report); got != "# Team report\n" {
			t.Errorf("host skill %q", got)
		}
		if got, err := git(host, "show", userConfigRef("member")+":skills/report/SKILL.md"); err != nil || got != "# My report\n" {
			t.Errorf("member ref %q %v", got, err)
		}
		if got, want := skillChange(), "user:member:Updated skill in run "+id[:8]+": report:"; !strings.HasPrefix(got, want) {
			t.Errorf("config change %q", got)
		}
		var staged string
		res, err, _ = run(ctx, newRun("member"), func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			staged = readText(filepath.Join(in.Workspace.SkillsRoot, "report", "SKILL.md"))
			return axRunResult{Status: "completed", FinalText: "Done."}, nil
		})
		if err != nil || staged != "# My report\n" {
			t.Errorf("the member's next run saw %q (%v)", staged, err)
		}
	})

	t.Run("a turn with no skill writes saves nothing", func(t *testing.T) {
		writeTestFile(t, report, "# Team report\n")
		before := skillChange()
		res, err, _ := run(ctx, newRun("admin"), skillEdit("# Lost\n", "read_file", nil, nil))
		if err != nil || res.Status != "completed" || readText(report) != "# Team report\n" || skillChange() != before {
			t.Errorf("%+v %v %q", res, err, readText(report))
		}
	})

	t.Run("a removed file goes and a file changed on the host meanwhile stays", func(t *testing.T) {
		writeTestFile(t, report, "# Team report\n")
		old := filepath.Join(host, "skills", "report", "old.py")
		other := filepath.Join(host, "skills", "report", "other.py")
		writeTestFile(t, old, "old\n")
		writeTestFile(t, other, "other\n")
		res, err, _ := run(ctx, newRun("admin"), func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			if err := os.Remove(filepath.Join(in.Workspace.SkillsRoot, "report", "old.py")); err != nil {
				t.Error(err)
			}
			writeTestFile(t, filepath.Join(in.Workspace.SkillsRoot, "report", "other.py"), "from the run\n")
			writeTestFile(t, other, "from an admin\n")
			_ = in.Emit(ctx, jsObjOf("type", "tool_start", "id", "t1", "name", "process"))
			return axRunResult{Status: "completed", FinalText: "Done."}, nil
		})
		if err != nil || res.Status != "completed" || readText(old) != "<none>" || readText(other) != "from an admin\n" {
			t.Errorf("%+v %v %q %q", res, err, readText(old), readText(other))
		}
		os.Remove(other)
	})

	t.Run("a failed turn still saves its skill edits", func(t *testing.T) {
		writeTestFile(t, report, "# Team report\n")
		_, err, _ := run(ctx, newRun("admin"), func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			writeTestFile(t, filepath.Join(in.Workspace.SkillsRoot, "report", "SKILL.md"), "# Kept\n")
			return axRunResult{}, errors.New("model down")
		})
		if err == nil || readText(report) != "# Kept\n" {
			t.Errorf("%v %q", err, readText(report))
		}
	})

	t.Run("a deleted thread fails the run", func(t *testing.T) {
		id := newRun("admin")
		log := &eventLog{}
		res, err := a.runChatTurn(ctx, chatTurnOptions{OrgID: "org", ThreadID: unknownUUID, RunID: id, Message: "x", Emit: log.emit})
		if err != nil || res.Status != "failed" || res.Error != "Thread deleted before run start." || row(id) != "failed:Thread deleted before run start." ||
			len(log.all()) != 0 {
			t.Fatalf("%+v %v %s", res, err, row(id))
		}
	})
}
