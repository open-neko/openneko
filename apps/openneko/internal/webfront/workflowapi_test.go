package webfront

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

const wfToken = "onk_wf_0123456789ab_secret-value"

func wfAPIFront(t *testing.T) (*Front, *pgxpool.Pool, *setupWorker) {
	f, db := workflowsFront(t)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("workflow-api-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(wfToken))
	seed(t, db, `update workflow_api_access set enabled = true, token_verifier = $2, token_prefix = 'onk_wf_0123456789ab', token_created_at = now() where workflow_id = $1`,
		wfHeld, hex.EncodeToString(digest[:]))
	seed(t, db, `update workflow_definition set daily_run_budget = null where id = $1`, wfHeld)
	seed(t, db, `delete from workflow_api_rate_bucket`)
	worker := &setupWorker{status: `{"provider":{"pluginName":"scalekit"}}`, reply: map[string]string{"/admin/events/external": `{"matched":0}`}}
	srv := httptest.NewServer(worker)
	t.Cleanup(srv.Close)
	f.Gate.WorkerURL = srv.URL
	return f, db, worker
}

// awayFromMinuteEdge keeps a rate-limit sequence inside one minute bucket.
func awayFromMinuteEdge() {
	if now := time.Now(); now.Second() >= 45 {
		time.Sleep(now.Truncate(time.Minute).Add(time.Minute + time.Second).Sub(now))
	}
}

func wfCall(f *Front, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{"Authorization": "Bearer " + wfToken, "Idempotency-Key": "request-0001", "Content-Type": "application/json"}
	for k, v := range header {
		if v == "-" {
			delete(h, k)
			continue
		}
		h[k] = v
	}
	return call(f, method, target, body, h)
}

func wfErrBody(code, message string) string {
	return `{"error":{"code":"` + code + `","message":"` + message + `"}}`
}

func TestWorkflowAPIRejections(t *testing.T) {
	f, db, _ := wfAPIFront(t)
	runs := "/api/v1/workflows/" + wfHeld + "/runs"
	nested := `{"a":` + strings.Repeat(`[`, 11) + strings.Repeat(`]`, 11) + `}`
	for _, c := range []struct {
		name, target, body string
		header             map[string]string
		code               int
		want               string
	}{
		{"mode", runs + "?mode=bulk", `{}`, nil, 400, wfErrBody("invalid_mode", "mode must be 'single' or 'batch'.")},
		{"empty mode", runs + "?mode=", `{}`, nil, 400, wfErrBody("invalid_mode", "mode must be 'single' or 'batch'.")},
		{"media", runs, `{}`, map[string]string{"Content-Type": "text/plain"}, 415, wfErrBody("unsupported_media_type", "Content-Type must be application/json.")},
		{"media case", runs, `{}`, map[string]string{"Content-Type": "Application/JSON"}, 415, wfErrBody("unsupported_media_type", "Content-Type must be application/json.")},
		{"declared", runs, `{}`, map[string]string{"Content-Length": "1048577"}, 413, wfErrBody("request_too_large", "The request exceeds the deployment safety limit.")},
		{"json", runs, `{oops`, nil, 400, wfErrBody("invalid_json", "The request body is not valid JSON.")},
		{"bom", runs, "\ufeff{}", nil, 400, wfErrBody("invalid_json", "The request body is not valid JSON.")},
		{"key first", runs, `{}`, map[string]string{"Idempotency-Key": "short", "Authorization": "-"}, 400,
			wfErrBody("invalid_idempotency_key", "Idempotency-Key must contain 8 to 200 visible characters.")},
		{"no token", runs, `{}`, map[string]string{"Authorization": "-"}, 401, wfErrBody("invalid_credentials", "The workflow credential is invalid.")},
		{"wrong token", runs, `{}`, map[string]string{"Authorization": "Bearer wrong"}, 401, wfErrBody("invalid_credentials", "The workflow credential is invalid.")},
		{"two tokens", runs, `{}`, map[string]string{"Authorization": "Bearer " + wfToken + " x"}, 401, wfErrBody("invalid_credentials", "The workflow credential is invalid.")},
		{"bad id", "/api/v1/workflows/nope/runs", `{}`, nil, 401, wfErrBody("invalid_credentials", "The workflow credential is invalid.")},
		{"array", runs, `[1]`, nil, 400, wfErrBody("invalid_payload", "The request body must be a JSON object.")},
		{"depth", runs, nested, nil, 413, wfErrBody("payload_too_complex", "The JSON payload exceeds the maximum nesting depth.")},
		{"key size", runs, `{"` + strings.Repeat("k", 257) + `":1}`, nil, 413, wfErrBody("payload_key_too_large", "A JSON field name exceeds the 256-byte limit.")},
		{"batch", runs + "?mode=batch", `{"records":[{}]}`, nil, 422, wfErrBody("batch_not_ready", "This workflow does not have a compiled batch contract.")},
	} {
		w := wfCall(f, "POST", c.target, c.body, c.header)
		if w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "no-store, private" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s headers: %v", c.name, w.Header())
		}
		if (c.code == 401) != (w.Header().Get("WWW-Authenticate") == `Bearer realm="workflow-api"`) {
			t.Errorf("%s challenge: %v", c.name, w.Header())
		}
	}
	seed(t, db, `update workflow_api_access set max_request_bytes = 1024 where workflow_id = $1`, wfHeld)
	if w := wfCall(f, "POST", runs, `{"a":"`+strings.Repeat("x", 1100)+`"}`, nil); w.Code != 413 ||
		w.Body.String() != wfErrBody("request_too_large", "The request exceeds this workflow's 1024-byte limit.") {
		t.Errorf("workflow limit: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_definition set enabled = false where id = $1`, wfHeld)
	if w := wfCall(f, "POST", runs, `{}`, nil); w.Code != 409 || w.Body.String() != wfErrBody("workflow_disabled", "The workflow is disabled.") {
		t.Errorf("workflow off: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_definition set enabled = true where id = $1`, wfHeld)
	seed(t, db, `update workflow_api_access set enabled = false where workflow_id = $1`, wfHeld)
	if w := wfCall(f, "POST", runs, `{}`, nil); w.Code != 401 {
		t.Errorf("access off: %d %s", w.Code, w.Body)
	}
	if n := queryText(t, db, `select count(*)::text from workflow_api_admission`); n != "0" {
		t.Errorf("admissions: %s", n)
	}
}

func TestWorkflowAPIAdmission(t *testing.T) {
	f, db, _ := wfAPIFront(t)
	runs := "/api/v1/workflows/" + wfHeld + "/runs"
	body := `{"orderId":"1042","2":true,"apiKey":"planted-key","note":"` + strings.Repeat("é", 250) + `","n":1.50,"list":[1,2,3,4,5,6]}`
	w := wfCall(f, "POST", runs, body, nil)
	if w.Code != 202 {
		t.Fatalf("admit: %d %s", w.Code, w.Body)
	}
	runID := uuidRe.FindString(w.Body.String())
	expires := queryText(t, db, `select to_char(expires_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') from workflow_api_admission where workflow_run_id = $1`, runID)
	want := `{"runId":"` + runID + `","status":"queued","mode":"single","replay":false,"statusUrl":"/api/v1/workflows/` + wfHeld +
		`/runs/` + runID + `","expiresAt":"` + expires + `"}`
	if w.Body.String() != want || w.Header().Get("Location") != "/api/v1/workflows/"+wfHeld+"/runs/"+runID || w.Header().Get("Retry-After") != "3" {
		t.Errorf("admit: %s %v", w.Body, w.Header())
	}

	canonical := `{"2":true,"apiKey":"planted-key","list":[1,2,3,4,5,6],"n":1.5,"note":"` + strings.Repeat("é", 250) + `","orderId":"1042"}`
	key, err := config.SigningSecret(f.API.SecretsDir, "workflow-api:idempotency:v1")
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(wfHeld + "\x00request-0001"))
	row := queryText(t, db, `select concat_ws(' | ', idempotency_hash, payload_hash, execution_mode, request_payload::text, request_bytes,
		status, reserved_tokens, reserved_cost_micros, coalesce(input_file_path, '-'), coalesce(batch_contract::text, '-'))
		from workflow_api_admission where workflow_run_id = $1`, runID)
	wantRow := hex.EncodeToString(mac.Sum(nil)) + " | " + sha256Hex("single\x00"+canonical) +
		` | single | {"2": true, "n": 1.5, "list": [1, 2, 3, 4, 5, 6], "note": "` + strings.Repeat("é", 250) +
		`", "apiKey": "planted-key", "orderId": "1042"} | ` + strconv.Itoa(len(canonical)) + ` | pending | 100000 | 5000000 | - | -`
	if row != wantRow {
		t.Errorf("admission:\n%s\n%s", row, wantRow)
	}
	run := queryText(t, db, `select concat_ws(' | ', run.status, run.trigger_kind, run.trigger_payload::text, run.execution_mode,
		run.trigger_input_preview::text, run.progress::text, thread.title, thread.channel, work.status, work.actor_role, work.backend,
		run.admitted_at = run.created_at)
		from workflow_run run join work_thread thread on thread.id = run.thread_id join work_run work on work.id = run.work_run_id
		where run.id = $1`, runID)
	preview := `{"2": true, "n": 1.5, "list": [1, 2, 3, 4, 5], "note": "` + strings.Repeat("é", 237) + `…", "apiKey": "[redacted]", "orderId": "1042"}`
	if run != `queued | api | {"source": "external_api", "requestBytes": `+strconv.Itoa(len(canonical))+`} | single | `+preview+
		` | {"stage": "queued"} | Held flow | workflow | queued | service | ax | t` {
		t.Errorf("run: %s", run)
	}
	if n := queryText(t, db, `select count(*)::text from spend_reservation where work_run_id = (select work_run_id from workflow_run where id = $1)`, runID); n != "1" {
		t.Errorf("reservation: %s", n)
	}
	audit := queryText(t, db, `select concat_ws(' | ', entity_kind, event, payload::text) from audit_chain where entity_id = $1`, runID)
	if audit != `workflow_run | api_admitted | {"mode": "single", "workflowId": "`+wfHeld+`", "requestBytes": `+strconv.Itoa(len(canonical))+`, "acceptedRecords": null}` {
		t.Errorf("audit: %s", audit)
	}
	if strings.Contains(queryText(t, db, `select trigger_input_preview::text from workflow_run where id = $1`, runID), "planted-key") {
		t.Error("preview holds the key")
	}

	replay := wfCall(f, "POST", runs, `{"n":1.5,"2":true,"orderId":"1042","apiKey":"planted-key","note":"`+strings.Repeat("é", 250)+`","list":[1,2,3,4,5,6]}`, nil)
	if replay.Code != 202 || !strings.Contains(replay.Body.String(), `"runId":"`+runID+`","status":"queued","mode":"single","replay":true`) ||
		replay.Header().Get("Retry-After") != "3" {
		t.Errorf("replay: %d %s", replay.Code, replay.Body)
	}
	seed(t, db, `update workflow_run set status = 'running' where id = $1`, runID)
	if w := wfCall(f, "POST", runs, body, nil); !strings.Contains(w.Body.String(), `"status":"running"`) || w.Header().Get("Retry-After") != "1" {
		t.Errorf("running replay: %s %v", w.Body, w.Header())
	}
	if w := wfCall(f, "POST", runs, `{"orderId":"1043"}`, nil); w.Code != 409 ||
		w.Body.String() != wfErrBody("idempotency_conflict", "The Idempotency-Key was already used with different input.") {
		t.Errorf("conflict: %d %s", w.Code, w.Body)
	}
	if n := queryText(t, db, `select count(*)::text from workflow_api_admission`); n != "1" {
		t.Errorf("admissions: %s", n)
	}

	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, config) values ('org', 'public-url', 'public-url', '{"url":"https://proxy.example/neko"}')`)
	w = wfCall(f, "POST", runs, `{}`, map[string]string{"Idempotency-Key": "request-0002"})
	if !strings.HasPrefix(w.Header().Get("Location"), "https://proxy.example/neko/api/v1/workflows/"+wfHeld+"/runs/") ||
		!strings.Contains(w.Body.String(), `"statusUrl":"https://proxy.example/neko/api/v1/`) {
		t.Errorf("public url: %s %v", w.Body, w.Header())
	}
}

func TestWorkflowAPIBatch(t *testing.T) {
	f, db, _ := wfAPIFront(t)
	runs := "/api/v1/workflows/" + wfHeld + "/runs?mode=batch"
	seed(t, db, `update workflow_definition set output_contract = '{"apiBatch":{"version":1,"compiled":true,"compiler":"workflow",
		"recordsField":"rows","columns":[{"name":"Order","path":"order.id","default":1.50},{"name":"Note","path":"note"}]}}' where id = $1`, wfHeld)
	seed(t, db, `update workflow_api_access set batch_max_records = 3, batch_chunk_size = 2 where workflow_id = $1`, wfHeld)
	for _, c := range []struct {
		body, want string
		code       int
	}{
		{`{"records":[]}`, wfErrBody("invalid_batch", "Batch input must include a 'rows' array."), 400},
		{`{"rows":[]}`, wfErrBody("empty_batch", "Batch input must contain at least one record."), 400},
		{`{"rows":[{},{},{},{}]}`, wfErrBody("batch_record_limit", "The batch exceeds this workflow's 3-record limit."), 413},
		{`{"rows":[{},[]]}`, wfErrBody("invalid_batch_record", "Every batch record must be a JSON object."), 400},
	} {
		if w := wfCall(f, "POST", runs, c.body, nil); w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	w := wfCall(f, "POST", runs, `{"rows":[{"order":{"id":7},"b":1.0},{"note":"x"},{"z":1e2}]}`, nil)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"mode":"batch"`) {
		t.Fatalf("batch: %d %s", w.Code, w.Body)
	}
	runID := uuidRe.FindString(w.Body.String())
	row := queryText(t, db, `select concat_ws(' | ', admission.input_file_path, admission.batch_contract::text, admission.accepted_records,
		admission.reserved_tokens, coalesce(admission.request_payload::text, '-'), run.progress::text, run.work_run_id)
		from workflow_api_admission admission join workflow_run run on run.id = admission.workflow_run_id where run.id = $1`, runID)
	parts := strings.Split(row, " | ")
	work := parts[len(parts)-1]
	want := "runs/" + work + `/api-batch-input.ndjson | {"columns": [{"name": "Order", "path": "order.id", "default": 1.5}, {"name": "Note", "path": "note"}], ` +
		`"version": 1, "compiled": true, "compiler": "workflow", "recordsField": "rows"} | 3 | 0 | - | ` +
		`{"stage": "queued", "finalRows": 0, "chunkCount": 0, "acceptedRows": 3, "plannedChunks": 2, "processedRows": 0} | ` + work
	if row != want {
		t.Errorf("batch row:\n%s\n%s", row, want)
	}
	staged, err := os.ReadFile(filepath.Join(orgAgentRoot("org"), "runs", work, "api-batch-input.ndjson"))
	if err != nil || string(staged) != "{\"order\":{\"id\":7},\"b\":1}\n{\"note\":\"x\"}\n{\"z\":100}\n" {
		t.Errorf("staged: %q %v", staged, err)
	}
	if w := wfCall(f, "POST", runs, `{"rows":[{"a":2}]}`, nil); w.Code != 409 {
		t.Errorf("conflict: %d %s", w.Code, w.Body)
	}
	staged2, _ := filepath.Glob(filepath.Join(orgAgentRoot("org"), "runs", "*", "api-batch-input.ndjson"))
	if len(staged2) != 1 {
		t.Errorf("a refused batch left its input: %v", staged2)
	}
}

func TestWorkflowAPILimits(t *testing.T) {
	f, db, worker := wfAPIFront(t)
	runs := "/api/v1/workflows/" + wfHeld + "/runs"
	seed(t, db, `update workflow_api_access set request_limit_per_minute = 2 where workflow_id = $1`, wfHeld)
	awayFromMinuteEdge()
	for i, want := range []int{202, 202, 429} {
		w := wfCall(f, "POST", runs, `{}`, map[string]string{"Idempotency-Key": "rate-key-" + strconv.Itoa(i)})
		if w.Code != want {
			t.Errorf("rate %d: %d %s", i, w.Code, w.Body)
		}
		if want == 429 && (w.Body.String() != wfErrBody("invoke_rate_limited", "The request rate limit has been reached.") || w.Header().Get("Retry-After") == "") {
			t.Errorf("rate: %s %v", w.Body, w.Header())
		}
	}
	seed(t, db, `delete from workflow_api_rate_bucket`)
	seed(t, db, `update workflow_api_access set request_limit_per_minute = 30, queue_cap = 2 where workflow_id = $1`, wfHeld)
	if w := wfCall(f, "POST", runs, `{}`, map[string]string{"Idempotency-Key": "queue-key"}); w.Code != 429 ||
		w.Body.String() != wfErrBody("queue_full", "The workflow API queue is at capacity.") || w.Header().Get("Retry-After") != "15" {
		t.Errorf("queue: %d %s %v", w.Code, w.Body, w.Header())
	}
	if n := queryText(t, db, `select coalesce(sum(count), 0)::text from workflow_api_rate_bucket where operation = 'invoke' and scope_kind = 'workflow'`); n != "0" {
		t.Errorf("a refused admission kept its rate count: %s", n)
	}
	seed(t, db, `update workflow_api_admission set status = 'completed'`)
	seed(t, db, `update workflow_definition set daily_run_budget = 1 where id = $1`, wfHeld)
	if w := wfCall(f, "POST", runs, `{}`, map[string]string{"Idempotency-Key": "daily-key"}); w.Code != 429 ||
		w.Body.String() != wfErrBody("daily_run_budget_exhausted", "The workflow's daily run budget has been exhausted.") || w.Header().Get("Retry-After") != "3600" {
		t.Errorf("daily: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_definition set daily_run_budget = null where id = $1`, wfHeld)
	seed(t, db, `update workflow_api_admission set actual_cost_micros = 6000 where execution_mode = 'single'`)
	seed(t, db, `update workflow_api_access set rolling_cost_micros_budget = 10000 where workflow_id = $1`, wfHeld)
	if w := wfCall(f, "POST", runs, `{}`, map[string]string{"Idempotency-Key": "rolling-key"}); w.Code != 429 ||
		w.Body.String() != wfErrBody("rolling_budget_exhausted", "The rolling workflow API usage budget has been exhausted.") || w.Header().Get("Retry-After") != "3600" {
		t.Errorf("rolling: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_api_access set rolling_cost_micros_budget = 1000000000 where workflow_id = $1`, wfHeld)
	seed(t, db, `update spend_limit set workflow_daily_micros = 4000000 where org_id = 'org' and workflow_id is null`)
	w := wfCall(f, "POST", runs, `{}`, map[string]string{"Idempotency-Key": "spend-key"})
	if w.Code != 429 || !strings.HasPrefix(w.Body.String(), `{"error":{"code":"spend_budget_exhausted","message":"The daily spending limit of $4.00 for this workflow is reached. Try again after 00:00 UTC."}}`) {
		t.Errorf("spend: %d %s", w.Code, w.Body)
	}
	if n := queryText(t, db, `select count(*)::text from behavior_alert where org_id = 'org' and kind = 'spend.budget_blocked' and details->>'source' = 'api'`); n != "1" {
		t.Errorf("alert: %s", n)
	}
	worker.mu.Lock()
	calls := strings.Join(worker.calls, "\n")
	worker.mu.Unlock()
	if !strings.Contains(calls, `POST /admin/events/external`) {
		t.Errorf("dispatch: %s", calls)
	}
	if n := queryText(t, db, `select count(*)::text from work_run where org_id = 'org' and actor_role = 'service' and status = 'queued'`); n != "2" {
		t.Errorf("work runs: %s", n)
	}
}

func TestWorkflowAPIEdgeThrottle(t *testing.T) {
	f, _, _ := wfAPIFront(t)
	target := "/api/v1/workflows/" + wfHeld + "/runs/" + runAPI
	awayFromMinuteEdge()
	for i := 0; i < 60; i++ {
		if w := wfCall(f, "GET", target, "", map[string]string{"X-Forwarded-For": "203.0.113.9"}); w.Code == 429 {
			t.Fatalf("throttled at %d: %s", i, w.Body)
		}
	}
	w := wfCall(f, "GET", target, "", map[string]string{"X-Forwarded-For": " 203.0.113.9 , 10.0.0.1"})
	if w.Code != 429 || w.Body.String() != wfErrBody("edge_rate_limited", "The request rate limit has been reached.") {
		t.Errorf("edge: %d %s", w.Code, w.Body)
	}
	if w := wfCall(f, "GET", target, "", map[string]string{"X-Forwarded-For": "198.51.100.4"}); w.Code == 429 {
		t.Errorf("another client: %s", w.Body)
	}
}

func TestWorkflowAPIStatus(t *testing.T) {
	f, db, _ := wfAPIFront(t)
	w := wfCall(f, "POST", "/api/v1/workflows/"+wfHeld+"/runs", `{}`, nil)
	runID := uuidRe.FindString(w.Body.String())
	target := "/api/v1/workflows/" + wfHeld + "/runs/" + runID
	seed(t, db, `update workflow_run set created_at = '2026-11-01T00:00:00Z', admitted_at = '2026-11-01T00:00:01Z',
		result_expires_at = '2999-01-01T00:00:00Z', progress = '{"stage":"queued","2":1,"a":1.50}' where id = $1`, runID)
	w = wfCall(f, "GET", target, "", nil)
	want := `{"runId":"` + runID + `","workflowId":"` + wfHeld + `","mode":"single","status":"queued","createdAt":"2026-11-01T00:00:00.000Z",` +
		`"admittedAt":"2026-11-01T00:00:01.000Z","startedAt":null,"finishedAt":null,"expiresAt":"2999-01-01T00:00:00.000Z",` +
		`"progress":{"2":1,"a":1.5,"stage":"queued"},"result":null,"artifact":null,"error":null,"retryAfterSeconds":3}`
	if w.Code != 200 || w.Body.String() != want || w.Header().Get("Retry-After") != "3" {
		t.Errorf("queued:\n%d %s\n%s", w.Code, w.Body, want)
	}
	seed(t, db, `update workflow_run set status = 'failed', started_at = '2026-11-01T00:00:02Z', finished_at = '2026-11-01T00:00:03Z',
		terminal_result = '{"text":"done","truncated":false}', result_artifact_path = 'runs/x/artifacts/api-result.csv', error = 'boom',
		telemetry_summary = '{"usage":{"coverage":"unavailable"}}' where id = $1`, runID)
	seed(t, db, `update workflow_api_access set include_telemetry = true where workflow_id = $1`, wfHeld)
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, config) values ('org', 'public-url', 'public-url', '{"url":"https://proxy.example"}')`)
	w = wfCall(f, "GET", target, "", nil)
	want = `{"runId":"` + runID + `","workflowId":"` + wfHeld + `","mode":"single","status":"failed","createdAt":"2026-11-01T00:00:00.000Z",` +
		`"admittedAt":"2026-11-01T00:00:01.000Z","startedAt":"2026-11-01T00:00:02.000Z","finishedAt":"2026-11-01T00:00:03.000Z",` +
		`"expiresAt":"2999-01-01T00:00:00.000Z","progress":{"2":1,"a":1.5,"stage":"queued"},"telemetry":{"usage":{"coverage":"unavailable"}},` +
		`"result":{"text":"done","truncated":false},"artifact":{"url":"https://proxy.example` + target + `/artifact"},` +
		`"error":{"code":"workflow_failed","message":"boom"},"retryAfterSeconds":null}`
	if w.Code != 200 || w.Body.String() != want || w.Header().Get("Retry-After") != "" {
		t.Errorf("failed:\n%d %s\n%s", w.Code, w.Body, want)
	}
	seed(t, db, `update workflow_api_admission set last_error_code = 'worker_interrupted'`)
	seed(t, db, `update workflow_run set error = null where id = $1`, runID)
	if w := wfCall(f, "GET", target, "", nil); !strings.Contains(w.Body.String(), `"error":{"code":"worker_interrupted","message":"Workflow execution failed."}`) {
		t.Errorf("code only: %s", w.Body)
	}
	for _, c := range []struct {
		name, target, want string
		header             map[string]string
		code               int
	}{
		{"unknown", "/api/v1/workflows/" + wfHeld + "/runs/" + wfOrg, wfErrBody("run_not_found", "Workflow run not found."), nil, 404},
		{"not a uuid", "/api/v1/workflows/" + wfHeld + "/runs/nope", wfErrBody("run_not_found", "Workflow run not found."), nil, 404},
		{"other workflow's run", "/api/v1/workflows/" + wfHeld + "/runs/" + runHeld, wfErrBody("run_not_found", "Workflow run not found."), nil, 404},
		{"token", target, wfErrBody("invalid_credentials", "The workflow credential is invalid."), map[string]string{"Authorization": "Bearer nope"}, 401},
	} {
		if w := wfCall(f, "GET", c.target, "", c.header); w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	seed(t, db, `update workflow_run set result_expires_at = now() - interval '1 second' where id = $1`, runID)
	if w := wfCall(f, "GET", target, "", nil); w.Code != 410 || w.Body.String() != wfErrBody("result_expired", "The retained API result has expired.") {
		t.Errorf("expired: %d %s", w.Code, w.Body)
	}
	seed(t, db, `delete from workflow_api_rate_bucket`)
	seed(t, db, `update workflow_api_access set poll_limit_per_minute = 2 where workflow_id = $1`, wfHeld)
	awayFromMinuteEdge()
	for i, want := range []int{410, 410, 429} {
		if w := wfCall(f, "GET", target, "", nil); w.Code != want {
			t.Errorf("poll %d: %d %s", i, w.Code, w.Body)
		} else if want == 429 && w.Body.String() != wfErrBody("poll_rate_limited", "The request rate limit has been reached.") {
			t.Errorf("poll: %s", w.Body)
		}
	}
}

func TestWorkflowAPIArtifact(t *testing.T) {
	f, db, _ := wfAPIFront(t)
	w := wfCall(f, "POST", "/api/v1/workflows/"+wfHeld+"/runs", `{}`, nil)
	runID := uuidRe.FindString(w.Body.String())
	target := "/api/v1/workflows/" + wfHeld + "/runs/" + runID + "/artifact"
	if w := wfCall(f, "GET", target, "", nil); w.Code != 404 || w.Body.String() != wfErrBody("artifact_not_ready", "The workflow artifact is not available.") {
		t.Errorf("not ready: %d %s", w.Code, w.Body)
	}
	work := queryText(t, db, `select work_run_id::text from workflow_run where id = $1`, runID)
	dir := filepath.Join(orgAgentRoot("org"), "runs", work, "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api-result.csv"), []byte("a,b\n1,2\n"+strings.Repeat("3,4\n", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `update workflow_run set status = 'completed', result_artifact_path = $2 where id = $1`, runID, "runs/"+work+"/artifacts/api-result.csv")
	w = wfCall(f, "GET", target, "", nil)
	h := w.Header()
	if w.Code != 200 || w.Body.String() != "a,b\n1,2\n"+strings.Repeat("3,4\n", 300) || h.Get("Content-Type") != "text/csv; charset=utf-8" || h.Get("Content-Length") != "1208" ||
		h.Get("Content-Disposition") != `attachment; filename="workflow-`+runID+`.csv"` || h.Get("Cache-Control") != "no-store, private" ||
		h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("artifact: %d %q %v", w.Code, w.Body, h)
	}
	seed(t, db, `update workflow_api_access set max_artifact_bytes = 1024 where workflow_id = $1`, wfHeld)
	unavailable := wfErrBody("artifact_unavailable", "The workflow artifact is unavailable.")
	if w := wfCall(f, "GET", target, "", nil); w.Code != 410 || w.Body.String() != unavailable {
		t.Errorf("too large: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_api_access set max_artifact_bytes = 1048576 where workflow_id = $1`, wfHeld)
	outside := filepath.Join(t.TempDir(), "secret.csv")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.csv")); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `update workflow_run set result_artifact_path = $2 where id = $1`, runID, "runs/"+work+"/artifacts/link.csv")
	if w := wfCall(f, "GET", target, "", nil); w.Code != 410 || w.Body.String() != unavailable {
		t.Errorf("link: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_run set result_artifact_path = 'runs/missing.csv' where id = $1`, runID)
	if w := wfCall(f, "GET", target, "", nil); w.Code != 410 || w.Body.String() != unavailable {
		t.Errorf("missing: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_run set result_artifact_path = '../../escape.csv' where id = $1`, runID)
	if w := wfCall(f, "GET", target, "", nil); w.Code != 500 ||
		w.Body.String() != wfErrBody("internal_error", "The workflow API request could not be completed.") {
		t.Errorf("escape: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_run set result_expires_at = now() - interval '1 second' where id = $1`, runID)
	if w := wfCall(f, "GET", target, "", nil); w.Code != 410 || w.Body.String() != wfErrBody("result_expired", "The retained API result has expired.") {
		t.Errorf("expired: %d %s", w.Code, w.Body)
	}
}

func TestWorkflowAPIHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"Bearer abc": "abc", "bearer \t abc": "abc", "BEARER abc": "abc", "Bearer": "", "Bearer ": "", "Bearerabc": "",
		"Bearer a b": "", "Bearer abc ": "", "Basic abc": "", "Bearer a\xa0b": "",
	} {
		if got := wfBearer(in); got != want {
			t.Errorf("bearer %q: %q", in, got)
		}
	}
	for in, want := range map[string]float64{" 12abc": 12, "-3": -3, "+7": 7, "0x10": 0} {
		if got, ok := jsParseInt(in); !ok || got != want {
			t.Errorf("parseInt %q: %v %v", in, got, ok)
		}
	}
	if _, ok := jsParseInt("abc"); ok {
		t.Error("parseInt abc")
	}
	for in, ok := range map[string]bool{"12345678": true, " 1234567 ": false, "1234\x01678": false, strings.Repeat("k", 200): true, strings.Repeat("k", 201): false} {
		if _, err := wfIdempotencyKey(in); (err == nil) != ok {
			t.Errorf("key %q: %v", in, err)
		}
	}
	digest := sha256.Sum256([]byte("t"))
	lower := hex.EncodeToString(digest[:])
	upper := strings.ToUpper(lower)
	short := "abc"
	for name, c := range map[string]struct {
		verifier *string
		want     bool
	}{"lower": {&lower, true}, "upper": {&upper, true}, "short": {&short, false}, "none": {nil, false}} {
		if got := wfTokenMatches("t", c.verifier); got != c.want {
			t.Errorf("token %s: %v", name, got)
		}
	}
	big := &jsObj{}
	for i := 0; i < 40; i++ {
		big.set("field"+strconv.Itoa(i), strings.Repeat("x", 200))
	}
	preview := jsStringify(wfPreview(big), false)
	if !strings.HasPrefix(preview, `{"fields":["field0","field1",`) || !strings.HasSuffix(preview, `"field29"],"note":"Input preview omitted because it exceeded the 4 KiB display limit."}`) {
		t.Errorf("preview: %s", preview)
	}
	deep, _ := jsParse([]byte(`{"a":{"b":{"c":{"d":{"e":1}}}},"Private_Key":{"x":1},"s":"` + strings.Repeat("😀", 130) + `"}`))
	if got := jsStringify(wfPreview(deep.(*jsObj)), false); got != `{"a":{"b":{"c":{"d":{"e":"[truncated]"}}}},"Private_Key":"[redacted]","s":"`+strings.Repeat("😀", 118)+"�…"+`"}` {
		t.Errorf("deep: %s", got)
	}
	named, _ := jsParse([]byte(`{"customer":"Acme","password":"never-store-this","nested":{"api_key":"also-secret","note":"safe"},"tokenizedLabel":"x"}`))
	if got := jsStringify(wfPreview(named.(*jsObj)), false); got != `{"customer":"Acme","password":"[redacted]","nested":{"api_key":"[redacted]","note":"safe"},"tokenizedLabel":"[redacted]"}` {
		t.Errorf("named: %s", got)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.7:4000"
	if got := wfClientAddress(r); got != "192.0.2.7" {
		t.Errorf("peer: %s", got)
	}
	r.Header.Set("X-Forwarded-For", "")
	r.Header.Set("X-Real-Ip", "198.51.100.1")
	if got := wfClientAddress(r); got != "198.51.100.1" {
		t.Errorf("empty forwarded: %s", got)
	}
}
