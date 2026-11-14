package webfront

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/rivertype"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

const wfAPIAdmissionID = "e3500000-0000-4000-8000-000000000001"

func apiRunFront(t *testing.T, mode string) (*API, *pgxpool.Pool, jobs.WorkflowRunFireArgs) {
	t.Helper()
	a, db, wf := workflowTurnFront(t, `[]`)
	seed(t, db, `insert into workflow_api_access (workflow_id, org_id) values ($1, 'org')`, wf.ID)
	seed(t, db, `update workflow_run set trigger_kind = 'api', execution_mode = $2, admitted_at = now(),
		trigger_payload = '{"source":"external_api","requestBytes":17}' where id = $1`, wfTurnRun, mode)
	if mode == "single" {
		seed(t, db, `insert into workflow_api_admission (id, org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash,
			execution_mode, request_payload, request_bytes, status, attempts, expires_at)
			values ($1, 'org', $2, $3, repeat('a', 64), repeat('b', 64), 'single', '{"orderId":"1042"}', 17, 'enqueued', 1, now() + interval '1 hour')`,
			wfAPIAdmissionID, wf.ID, wfTurnRun)
	}
	return a, db, jobs.WorkflowRunFireArgs{OrgID: "org", WorkflowID: wf.ID, TriggerKind: "api", APIAdmissionID: wfAPIAdmissionID,
		WorkflowRunID: wfTurnRun, WorkRunID: wfTurnWorkRun, ThreadID: wfTurnThread, ExecutionMode: mode}
}

func apiRunRow(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	return rowText(t, db, `select concat_ws(' | ', a.status, coalesce(a.last_error_code, '-'), r.status, w.status,
		coalesce(r.error, '-'), coalesce(r.terminal_result::text, '-'), r.progress::text)
		from workflow_api_admission a join workflow_run r on r.id = a.workflow_run_id join work_run w on w.id = r.work_run_id
		where a.id = $1`, wfAPIAdmissionID)
}

func TestWorkflowAPIRunFireSingle(t *testing.T) {
	a, db, job := apiRunFront(t, "single")
	ctx := context.Background()
	a.turnResolve = fixedBackend("http://unused")
	card := jsObjOf("createSurface", jsObjOf("components", []any{
		jsObjOf("component", "Answer", "title", "Orders"),
		jsObjOf("component", "Table", "columns", []any{jsObjOf("key", "c0", "label", "Order")}, "rows", []any{jsObjOf("c0", "1,042")}),
	}))
	var got agentCoreInput
	calls := 0
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		calls++
		got = in
		_ = in.Emit(ctx, jsObjOf("type", "usage", "source", "outer", "usage", jsObjOf("totalTokens", 1200.0, "estimatedCostUsd", 0.0021)))
		_ = in.Emit(ctx, surfaceEvent([]any{card}))
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Order 1042 shipped."))
		return axRunResult{Status: "completed", FinalText: "Order 1042 shipped."}, nil
	}
	if err := a.WorkflowRunFire(ctx, job, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.UserMessage, "Execute the workflow using this externally admitted API input.") ||
		!strings.Contains(got.UserMessage, `{"orderId":"1042"}`) || got.Workflow.MaxToolIterations != 32 || got.Workflow.TimeoutMs != 600_000 {
		t.Errorf("core input: %q %+v", got.UserMessage, got.Workflow)
	}
	ws, err := turnWorkspace("org", wfTurnThread, wfTurnWorkRun)
	if err != nil {
		t.Fatal(err)
	}
	if input, err := os.ReadFile(filepath.Join(ws.RunRoot, workflowAPIInputFile)); err != nil || string(input) != "{\n  \"orderId\": \"1042\"\n}\n" {
		t.Errorf("trigger input %q %v", input, err)
	}
	want := `completed | - | completed | completed | - | {"text": "Order 1042 shipped.", "answer": {"table": {"rows": [{"order": 1042}], ` +
		`"columns": [{"key": "order", "label": "Order"}]}, "title": "Orders"}, "truncated": false} | {"stage": "completed"}`
	if g := apiRunRow(t, db); g != want {
		t.Errorf("run\n got %s\nwant %s", g, want)
	}
	if g := rowText(t, db, `select actual_tokens || ':' || actual_cost_micros || ':' || (lease_until is null) || ':' || (started_at is not null)
		from workflow_api_admission where id = $1`, wfAPIAdmissionID); g != "1200:2100:true:true" {
		t.Errorf("actuals %s", g)
	}
	if err := a.WorkflowRunFire(ctx, job, 2); err != nil || calls != 1 {
		t.Errorf("a second delivery: %v, %d core calls", err, calls)
	}
}

func TestWorkflowAPIRunFireCompatSeed(t *testing.T) {
	a, db, job := apiRunFront(t, "single")
	seed(t, db, `update workflow_api_admission set request_payload = $2 where id = $1`, wfAPIAdmissionID,
		`{"`+hookSeedKey+`":{"mode":"sync","message":"Start the import."},"params":{"file":"a.csv"}}`)
	a.turnResolve = fixedBackend("http://unused")
	var got agentCoreInput
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		got = in
		return axRunResult{Status: "completed", FinalText: `{"ok":true}`}, nil
	}
	if err := a.WorkflowRunFire(context.Background(), job, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.UserMessage, "Start the import.") || strings.Contains(got.UserMessage, "externally admitted") {
		t.Errorf("seed %q", got.UserMessage)
	}
	ws, _ := turnWorkspace("org", wfTurnThread, wfTurnWorkRun)
	if input, _ := os.ReadFile(filepath.Join(ws.RunRoot, workflowAPIInputFile)); string(input) != "{\n  \"file\": \"a.csv\"\n}\n" {
		t.Errorf("trigger input %q", input)
	}
	if g := rowText(t, db, `select terminal_result::text from workflow_run where id = $1`, wfTurnRun); g != `{"ok": true}` {
		t.Errorf("result %s", g)
	}
}

func TestWorkflowAPIRunFireCeilings(t *testing.T) {
	for _, c := range []struct {
		name, limits string
		events       []any
		code, error  string
	}{
		{"tools", "max_tool_calls = 1", []any{jsObjOf("type", "tool_start", "id", "t1", "name", "lookup"), jsObjOf("type", "tool_start", "id", "t2", "name", "lookup")},
			"tool_call_limit", "The API run exceeded its tool-call ceiling."},
		{"model calls", "max_model_calls = 2", []any{jsObjOf("type", "tool_start", "id", "t1", "name", "lookup"), jsObjOf("type", "tool_start", "id", "t2", "name", "lookup")},
			"model_call_limit", "The API run exceeded its model-call ceiling."},
		{"tokens", "max_tokens_per_run = 1000", []any{jsObjOf("type", "usage", "usage", jsObjOf("totalTokens", 1001.0))},
			"token_limit", "The API run exceeded its token ceiling."},
		{"spend", "max_cost_micros_per_run = 1000", []any{jsObjOf("type", "usage", "usage", jsObjOf("totalTokens", 10.0, "billedCostUsd", 0.002))},
			"spend_limit", "The API run exceeded its provider-spend ceiling."},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, db, job := apiRunFront(t, "single")
			seed(t, db, `update workflow_api_access set `+c.limits+` where workflow_id = $1`, job.WorkflowID)
			a.turnResolve = fixedBackend("http://unused")
			after := 0
			a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
				for _, e := range c.events {
					if err := in.Emit(ctx, e); err != nil {
						return axRunResult{}, err
					}
					after++
				}
				return axRunResult{Status: "completed", FinalText: "Done."}, nil
			}
			if err := a.WorkflowRunFire(context.Background(), job, 1); err != nil {
				t.Fatal(err)
			}
			want := "failed | " + c.code + " | failed | failed | " + c.error + ` | - | {"stage": "failed"}`
			if g := apiRunRow(t, db); g != want {
				t.Errorf("run\n got %s\nwant %s", g, want)
			}
			if after != len(c.events)-1 {
				t.Errorf("%d events passed the ceiling", after)
			}
		})
	}
}

func TestWorkflowAPIRunFireThrown(t *testing.T) {
	a, db, job := apiRunFront(t, "single")
	a.turnResolve = fixedBackend("http://unused")
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		return axRunResult{}, errors.New("serve went away")
	}
	if err := a.WorkflowRunFire(context.Background(), job, 1); err != nil {
		t.Fatalf("a claimed run asked for a retry: %v", err)
	}
	if g := apiRunRow(t, db); !strings.HasPrefix(g, "failed | workflow_failed | failed | failed | ") || !strings.Contains(g, "serve went away") {
		t.Errorf("run %s", g)
	}
}

func TestWorkflowAPIRunFireDeferAndExpire(t *testing.T) {
	a, db, job := apiRunFront(t, "single")
	seed(t, db, `update workflow_api_access set concurrency_cap = 1 where workflow_id = $1`, job.WorkflowID)
	seed(t, db, `insert into work_thread (id, org_id, title, channel) values ('e3200000-0000-4000-8000-000000000002', 'org', 'Other', 'workflow')`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_role) values ('e3300000-0000-4000-8000-000000000002', 'org',
		'e3200000-0000-4000-8000-000000000002', 'ax', 'service')`)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload, status)
		values ('e3100000-0000-4000-8000-000000000002', 'org', $1, 'e3200000-0000-4000-8000-000000000002',
		'e3300000-0000-4000-8000-000000000002', 'api', '{}', 'running')`, job.WorkflowID)
	seed(t, db, `insert into workflow_api_admission (org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash,
		execution_mode, request_payload, request_bytes, status, lease_until, expires_at)
		values ('org', $1, 'e3100000-0000-4000-8000-000000000002', repeat('c', 64), repeat('b', 64), 'single', '{}', 2, 'running', now() + interval '1 hour',
		now() + interval '1 hour')`, job.WorkflowID)
	a.turnCore = func(context.Context, agentCoreInput) (axRunResult, error) {
		t.Fatal("a deferred run started")
		return axRunResult{}, nil
	}
	var snooze *rivertype.JobSnoozeError
	if err := a.WorkflowRunFire(context.Background(), job, 1); !errors.As(err, &snooze) {
		t.Fatalf("deferred: %v", err)
	}
	if g := apiRunRow(t, db); g != `enqueued | concurrency_full | queued | running | - | - | {}` {
		t.Errorf("deferred run %s", g)
	}

	seed(t, db, `update workflow_api_admission set created_at = now() - interval '2 hours', expires_at = now() - interval '1 second' where id = $1`, wfAPIAdmissionID)
	if err := a.WorkflowRunFire(context.Background(), job, 2); err != nil {
		t.Fatal(err)
	}
	if g := apiRunRow(t, db); g != `expired | concurrency_full | cancelled | cancelled | Result retention elapsed before execution. | - | {}` {
		t.Errorf("expired run %s", g)
	}
}

func TestWorkflowAPIRunFireBatch(t *testing.T) {
	a, db, job := apiRunFront(t, "batch")
	rel := filepath.Join("runs", wfTurnWorkRun, "api-batch-input.ndjson")
	path := filepath.Join(orgAgentRoot("org"), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"order\":{\"id\":7},\"note\":\"=SUM(A1)\"}\n\n{\"note\":\"a, \\\"b\\\"\"}\n{\"order\":{\"id\":[1]}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `update workflow_api_access set batch_chunk_size = 2 where workflow_id = $1`, job.WorkflowID)
	seed(t, db, `insert into workflow_api_admission (id, org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash,
		execution_mode, input_file_path, batch_contract, accepted_records, request_bytes, status, expires_at)
		values ($1, 'org', $2, $3, repeat('a', 64), repeat('b', 64), 'batch', $4, '{"columns":[{"name":"Order","path":"order.id","default":1.5},{"name":"Note","path":"note"}]}',
		3, 90, 'enqueued', now() + interval '1 hour')`, wfAPIAdmissionID, job.WorkflowID, wfTurnRun, rel)
	a.turnCore = func(context.Context, agentCoreInput) (axRunResult, error) {
		t.Fatal("a batch called the model")
		return axRunResult{}, nil
	}
	if err := a.WorkflowRunFire(context.Background(), job, 1); err != nil {
		t.Fatal(err)
	}
	csv, err := os.ReadFile(filepath.Join(orgAgentRoot("org"), "runs", wfTurnWorkRun, "artifacts", "api-result.csv"))
	wantCSV := "Order,Note\r\n7,'=SUM(A1)\r\n1.5,\"a, \"\"b\"\"\"\r\n[1],\r\n"
	if err != nil || string(csv) != wantCSV {
		t.Errorf("csv %q %v", csv, err)
	}
	want := `completed | - | completed | completed | - | {"kind": "csv", "rows": 3, "columns": ["Order", "Note"]} | ` +
		`{"stage": "completed", "finalRows": 3, "chunkCount": 2, "acceptedRows": 3, "artifactBytes": 47, "processedRows": 3}`
	if g := apiRunRow(t, db); g != want {
		t.Errorf("run\n got %s\nwant %s", g, want)
	}
	if g := rowText(t, db, `select result_artifact_path || ' | ' || summary from workflow_run where id = $1`, wfTurnRun); g !=
		"runs/"+wfTurnWorkRun+"/artifacts/api-result.csv | Processed 3 batch records into one CSV artifact." {
		t.Errorf("artifact %s", g)
	}
	if g := rowText(t, db, `select telemetry_summary->'batch' from workflow_run where id = $1`, wfTurnRun); g !=
		`{"finalRows": 3, "chunkCount": 2, "acceptedRows": 3, "artifactBytes": 47, "processedRows": 3}` {
		t.Errorf("telemetry batch %s", g)
	}
	if g := rowText(t, db, `select string_agg(payload->>'type', ',' order by id) from work_run_event where run_id = $1`, wfTurnWorkRun); g !=
		"status,artifact,message,done,telemetry" {
		t.Errorf("events %s", g)
	}
}

func TestWorkflowAPIRunFireBatchLimit(t *testing.T) {
	a, db, job := apiRunFront(t, "batch")
	rel := filepath.Join("runs", wfTurnWorkRun, "api-batch-input.ndjson")
	path := filepath.Join(orgAgentRoot("org"), rel)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(`{"note":"`+strings.Repeat("x", 2000)+"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `update workflow_api_access set max_artifact_bytes = 1024 where workflow_id = $1`, job.WorkflowID)
	seed(t, db, `insert into workflow_api_admission (id, org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash,
		execution_mode, input_file_path, batch_contract, accepted_records, request_bytes, status, expires_at)
		values ($1, 'org', $2, $3, repeat('a', 64), repeat('b', 64), 'batch', $4, '{"columns":[{"name":"Note","path":"note"}]}', 1, 2010, 'enqueued', now() + interval '1 hour')`,
		wfAPIAdmissionID, job.WorkflowID, wfTurnRun, rel)
	if err := a.WorkflowRunFire(context.Background(), job, 1); err != nil {
		t.Fatal(err)
	}
	want := `failed | workflow_failed | failed | failed | The batch result exceeds this workflow's artifact limit. | - | {"stage": "failed"}`
	if g := apiRunRow(t, db); g != want {
		t.Errorf("run\n got %s\nwant %s", g, want)
	}
	if g := rowText(t, db, `select telemetry_summary->>'batch' from workflow_run where id = $1`, wfTurnRun); g != `{"acceptedRows": 1}` {
		t.Errorf("telemetry batch %s", g)
	}
	if _, err := os.Stat(filepath.Join(orgAgentRoot("org"), "runs", wfTurnWorkRun, "artifacts", "api-result.csv")); !os.IsNotExist(err) {
		t.Errorf("a partial artifact is left: %v", err)
	}
}

func TestWorkflowAPISweep(t *testing.T) {
	a, db, job := apiRunFront(t, "single")
	ctx := context.Background()
	other := func(n, status, extra string) {
		thread, work, run := "e3200000-0000-4000-8000-00000000000"+n, "e3300000-0000-4000-8000-00000000000"+n, "e3100000-0000-4000-8000-00000000000"+n
		seed(t, db, `insert into work_thread (id, org_id, title, channel) values ($1, 'org', 'Other', 'workflow')`, thread)
		seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_role) values ($1, 'org', $2, 'ax', 'service')`, work, thread)
		seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload, status, terminal_result,
			result_artifact_path, admitted_at) values ($1, 'org', $2, $3, $4, 'api', '{}', 'running', '{"a":1}', 'runs/`+work+`/artifacts/api-result.csv', now())`,
			run, job.WorkflowID, thread, work)
		seed(t, db, `insert into workflow_api_admission (id, org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash,
			execution_mode, request_payload, request_bytes, status, `+extra+`)
			values ('e3500000-0000-4000-8000-00000000000`+n+`', 'org', $1, $2, repeat('`+n+`', 64), repeat('b', 64), 'single', '{}', 2, '`+status+`', `+
			map[string]string{"lease_until, expires_at": "now() - interval '1 second', now() + interval '1 hour'",
				"created_at, expires_at": "now() - interval '2 hours', now() - interval '1 second'"}[extra]+`)`, job.WorkflowID, run)
	}
	other("2", "running", "lease_until, expires_at")
	other("3", "completed", "created_at, expires_at")
	artifact := filepath.Join(orgAgentRoot("org"), "runs", "e3300000-0000-4000-8000-000000000003", "artifacts", "api-result.csv")
	_ = os.MkdirAll(filepath.Dir(artifact), 0o700)
	if err := os.WriteFile(artifact, []byte("a\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `update workflow_api_admission set status = 'pending', attempts = 0 where id = $1`, wfAPIAdmissionID)

	if err := a.WorkflowAPISweep(ctx); err != nil {
		t.Fatal(err)
	}
	if g := rowText(t, db, `select a.status || ':' || a.last_error_code || ':' || r.status || ':' || w.status || ':' || r.error
		from workflow_api_admission a join workflow_run r on r.id = a.workflow_run_id join work_run w on w.id = r.work_run_id
		where a.id = 'e3500000-0000-4000-8000-000000000002'`); g != "failed:worker_interrupted:failed:failed:Worker interrupted before the run completed." {
		t.Errorf("stale run %s", g)
	}
	if g := rowText(t, db, `select a.status || ':' || (r.terminal_result is null) || ':' || (r.result_artifact_path is null)
		from workflow_api_admission a join workflow_run r on r.id = a.workflow_run_id where a.id = 'e3500000-0000-4000-8000-000000000003'`); g != "expired:true:true" {
		t.Errorf("expired run %s", g)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Errorf("the expired artifact is left: %v", err)
	}
	if g := rowText(t, db, `select status || ':' || attempts || ':' || (queue_job_id is not null) || ':' || (dispatched_at is not null)
		from workflow_api_admission where id = $1`, wfAPIAdmissionID); g != "enqueued:1:true:true" {
		t.Errorf("queued admission %s", g)
	}
	args := riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")
	if len(args) != 1 || !strings.Contains(args[0].Args, `"triggerKind": "api"`) || !strings.Contains(args[0].Args, `"apiAdmissionId": "`+wfAPIAdmissionID+`"`) ||
		!strings.Contains(args[0].Args, `"executionMode": "single"`) || !strings.Contains(args[0].Args, `"queueAttempt": 1`) || args[0].MaxAttempts != 3 {
		t.Errorf("river jobs %+v", args)
	}
	if err := a.WorkflowAPISweep(ctx); err != nil || len(riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")) != 1 {
		t.Errorf("a second sweep queued again: %v", err)
	}
}

func TestWorkflowAPIAdmissionQueuesRiverJob(t *testing.T) {
	f, db, _ := wfAPIFront(t)
	seedRiver(t, db, f)
	w := wfCall(f, "POST", "/api/v1/workflows/"+wfHeld+"/runs", `{"orderId":"1042"}`, nil)
	if w.Code != 202 {
		t.Fatalf("admit: %d %s", w.Code, w.Body)
	}
	runID := uuidRe.FindString(w.Body.String())
	if g := queryText(t, db, `select status || ':' || attempts || ':' || queue_job_id from workflow_api_admission where workflow_run_id = $1`, runID); !strings.HasPrefix(g, "enqueued:1:") {
		t.Errorf("admission %s", g)
	}
	jobs := riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")
	if len(jobs) != 1 || !strings.Contains(jobs[0].Args, `"workflowRunId": "`+runID+`"`) {
		t.Errorf("river jobs %+v", jobs)
	}
	if w := wfCall(f, "POST", "/api/v1/workflows/"+wfHeld+"/runs", `{"orderId":"1042"}`, nil); w.Code != 202 || len(riverQueueJobs(t, db, "workflow_run_fire", "workflow_run")) != 1 {
		t.Errorf("a replay queued another job: %d", w.Code)
	}
}

func TestWorkflowAPIResultShapes(t *testing.T) {
	answer := wfAPIAnswer([]any{jsObjOf("version", "v1.0", "createSurface", jsObjOf("surfaceId", "answer-1", "components", []any{
		jsObjOf("id", "root", "component", "Answer", "title", "Orders by region"),
		jsObjOf("id", "table", "component", "Table",
			"columns", []any{jsObjOf("key", "c0", "label", "Region"), jsObjOf("key", "c1", "label", "Orders"), jsObjOf("key", "c2", "label", "Due by (UTC)"),
				jsObjOf("key", "c3", "label", "Orders"), jsObjOf("key", "c4", "label", "")},
			"rows", []any{jsObjOf("c0", "North", "c1", "12,699.5", "c2", "21 Aug", "c3", "3", "c4", "x")},
			"details", jsObjOf("North", jsObjOf("columns", []any{jsObjOf("key", "c0", "label", "Store"), jsObjOf("key", "c1", "label", "Orders")},
				"rows", []any{jsObjOf("c0", "Store 1", "c1", "7")}))),
		jsObjOf("id", "callout", "component", "Callout", "text", "North is behind plan."),
	}))})
	want := `{"title":"Orders by region","table":{"columns":[{"key":"region","label":"Region"},{"key":"orders","label":"Orders"},` +
		`{"key":"due_by_utc","label":"Due by (UTC)"},{"key":"orders_2","label":"Orders"},{"key":"column_5","label":""}],` +
		`"rows":[{"region":"North","orders":12699.5,"due_by_utc":"21 Aug","orders_2":3,"column_5":"x"}]},` +
		`"details":{"North":{"columns":[{"key":"store","label":"Store"},{"key":"orders","label":"Orders"}],"rows":[{"store":"Store 1","orders":7}]}},` +
		`"callout":"North is behind plan."}`
	if g := jsStringify(answer, false); g != want {
		t.Errorf("answer\n got %s\nwant %s", g, want)
	}
	if wfAPIAnswer(nil) != nil {
		t.Error("an answer with no card")
	}
	for _, c := range []struct {
		text   string
		max    int
		answer *jsObj
		want   string
	}{
		{`{"ok":true}`, 1024, nil, `{"ok":true}`},
		{"Done.", 4096, answer, `{"text":"Done.","answer":` + want + `,"truncated":false}`},
		{"Done.", 64, answer, `{"text":"Done.","truncated":false}`},
		{"[1,2]", 64, nil, `{"text":"[1,2]","truncated":false}`},
		{strings.Repeat("🙂", 200), 96, nil, ""},
		{"Done.", 10, nil, `{}`},
	} {
		got := jsStringify(wfAPIBoundedResult(c.text, c.max, c.answer), false)
		if c.want == "" {
			if len(got) > c.max || !strings.HasSuffix(got, `…","truncated":true}`) {
				t.Errorf("truncated %s (%d bytes)", got, len(got))
			}
			continue
		}
		if got != c.want {
			t.Errorf("%q in %d: %s", c.text, c.max, got)
		}
	}
	for in, want := range map[string]string{"plain": "plain", "=1+1": "'=1+1", "-2": "'-2", "@x": "'@x", "a,b": `"a,b"`, `say "hi"`: `"say ""hi"""`, "+1,2": `"'+1,2"`} {
		if g := wfAPICSVCell(in); g != want {
			t.Errorf("cell %q = %q, want %q", in, g, want)
		}
	}
}
