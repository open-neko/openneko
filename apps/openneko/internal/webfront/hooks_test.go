package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	hookID     = "01JHOOK000000000000000000A"
	hookOpen   = "01JHOOK000000000000000000B"
	hookOff    = "01JHOOK000000000000000000C"
	hookSecret = "hook-token-0123456789abcdef"
)

var runIDRe = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)

func hooksFront(t *testing.T) (*Front, *pgxpool.Pool, *setupWorker) {
	f, db := workflowsFront(t)
	digest := sha256.Sum256([]byte(hookSecret))
	seed(t, db, `update workflow_api_access set enabled = true, token_verifier = repeat('a', 64), token_prefix = 'onk_wf_' || repeat('b', 12),
		token_created_at = now() where workflow_id = $1`, wfHeld)
	seed(t, db, `insert into compat_webhook (reckon_workflow_id, org_id, workflow_id, token_sha256, params, batch_chunk_size, enabled) values
		($1, 'org', $4, $5, '{account,accounts}', 1000, true),
		($2, 'org', $4, $5, null, 3, true),
		($3, 'org', $4, $5, null, 1000, false)`, hookID, hookOpen, hookOff, wfHeld, hex.EncodeToString(digest[:]))
	seed(t, db, `delete from workflow_api_rate_bucket`)
	worker := &setupWorker{status: `{"provider":{"pluginName":"scalekit"}}`, reply: map[string]string{"/admin/events/external": `{"matched":0}`}}
	srv := httptest.NewServer(worker)
	t.Cleanup(srv.Close)
	f.Gate.WorkerURL = srv.URL
	return f, db, worker
}

func hookCall(f *Front, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{"X-Webhook-Token": hookSecret, "Idempotency-Key": "job-1", "Content-Type": "application/json"}
	for k, v := range header {
		if v == "-" {
			delete(h, k)
			continue
		}
		h[k] = v
	}
	return call(f, method, target, body, h)
}

func pending(runID, status string, replay bool) string {
	url := "https://neko.example/api/hooks/" + hookID + "/result/" + runID
	r := "false"
	if replay {
		r = "true"
	}
	return `{"ok":true,"runId":"` + runID + `","status":"` + status + `","mode":"single","idempotentReplay":` + r +
		`,"note":"Poll resultRequest.url with the same webhook token.","resultUrl":"/api/hooks/` + hookID + `/result/` + runID +
		`","resultRequest":{"method":"GET","url":"` + url + `","headers":{"X-Webhook-Token":"<same webhook token used to start the run>"},` +
		`"curl":"curl -H 'X-Webhook-Token: <same webhook token used to start the run>' '` + url + `'"}}`
}

func TestHookRejections(t *testing.T) {
	f, db, _ := hooksFront(t)
	t.Setenv("HEADLESS_WEBHOOK_RATE_PER_MINUTE", "100")
	big := `{"accounts":[` + strings.Repeat(`"a",`, 1000) + `"a"]}`
	for _, c := range []struct {
		name, target, body string
		header             map[string]string
		code               int
		want               string
	}{
		{"unknown", "/api/hooks/01JNOPE0000000000000000000", `{}`, nil, 404, `{"error":"not found"}`},
		{"disabled", "/api/v1/hooks/" + hookOff, `{}`, nil, 404, `{"error":"not found"}`},
		{"bad token", "/api/hooks/" + hookID, `{}`, map[string]string{"X-Webhook-Token": "wrong"}, 401, `{"error":"unauthorized"}`},
		{"no token", "/api/hooks/" + hookID, `{}`, map[string]string{"X-Webhook-Token": "-"}, 401, `{"error":"unauthorized"}`},
		{"no key", "/api/hooks/" + hookID, `{}`, map[string]string{"Idempotency-Key": "-"}, 400,
			`{"error":"invalid_idempotency_key","message":"Send an Idempotency-Key header containing 1-200 letters, numbers, dots, underscores, colons, or dashes."}`},
		{"bad key", "/api/hooks/" + hookID, `{}`, map[string]string{"Idempotency-Key": "a b"}, 400,
			`{"error":"invalid_idempotency_key","message":"Send an Idempotency-Key header containing 1-200 letters, numbers, dots, underscores, colons, or dashes."}`},
		{"long key", "/api/hooks/" + hookID, `{}`, map[string]string{"Idempotency-Key": strings.Repeat("k", 201)}, 400,
			`{"error":"invalid_idempotency_key","message":"Send an Idempotency-Key header containing 1-200 letters, numbers, dots, underscores, colons, or dashes."}`},
		{"mode", "/api/hooks/" + hookID + "?mode=bulk", `{}`, nil, 400, `{"error":"invalid_mode","message":"mode must be \"single\" or \"batch\""}`},
		{"declared size", "/api/hooks/" + hookID, `{}`, map[string]string{"Content-Length": "307200"}, 413, `{"error":"payload_too_large","maxBytes":262144}`},
		{"size", "/api/hooks/" + hookID, `{"a":"` + strings.Repeat("x", 262144) + `"}`, nil, 413, `{"error":"payload_too_large","maxBytes":262144}`},
		{"json", "/api/hooks/" + hookID, `{oops`, nil, 400, `{"error":"invalid_json"}`},
		{"blank", "/api/hooks/" + hookID, `  `, nil, 400, `{"error":"invalid_json"}`},
		{"array", "/api/hooks/" + hookID, `[1,2]`, nil, 400, `{"error":"invalid_payload","message":"Request JSON must be an object."}`},
		{"null", "/api/hooks/" + hookID, `null`, nil, 400, `{"error":"invalid_payload","message":"Request JSON must be an object."}`},
		{"batch", "/api/hooks/" + hookID + "?mode=batch", big, nil, 413,
			`{"error":"too_many_records","message":"Batch arrays may contain at most 1000 records.","maxRecords":1000}`},
		{"chunk", "/api/hooks/" + hookOpen + "?mode=batch", `{"x":{"y":[[1,2,3,4]]}}`, nil, 413,
			`{"error":"too_many_records","message":"Batch arrays may contain at most 3 records.","maxRecords":3}`},
	} {
		w := hookCall(f, "POST", c.target, c.body, c.header)
		if w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	if n := queryText(t, db, `select count(*)::text from compat_webhook_run`); n != "0" {
		t.Errorf("runs: %s", n)
	}
	seed(t, db, `update workflow_api_access set enabled = false where workflow_id = $1`, wfHeld)
	if w := hookCall(f, "POST", "/api/hooks/"+hookID, `{}`, nil); w.Code != 422 ||
		w.Body.String() != `{"error":"batch_not_ready","status":"api_access_disabled","message":"The target workflow does not accept API runs."}` {
		t.Errorf("access off: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_api_access set enabled = true where workflow_id = $1`, wfHeld)
	seed(t, db, `update workflow_definition set enabled = false where id = $1`, wfHeld)
	if w := hookCall(f, "POST", "/api/hooks/"+hookID, `{}`, nil); w.Code != 409 || w.Body.String() != `{"error":"workflow_disabled"}` {
		t.Errorf("workflow off: %d %s", w.Code, w.Body)
	}
}

func TestHookAdmission(t *testing.T) {
	f, db, _ := hooksFront(t)
	body := `{"account":"AC1","secret":"drop","accounts":[1.50,1e21]}`
	w := hookCall(f, "POST", "/api/hooks/"+hookID, body, nil)
	runID := runIDRe.FindString(w.Body.String())
	if w.Code != 202 || w.Body.String() != pending(runID, "queued", false) || w.Header().Get("Retry-After") != "10" ||
		w.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("admit: %d %s %v", w.Code, w.Body, w.Header())
	}
	if ms := ulidTime(runID); time.Since(ms) > time.Minute || time.Since(ms) < 0 {
		t.Errorf("ulid time: %v", ms)
	}
	print := sha256Hex(`{"executionMode":"single","params":{"account":"AC1","accounts":[1.5,1e+21]}}`)
	got := queryText(t, db, `select concat_ws(' | ', compat.execution_mode, compat.idempotency_key, compat.request_fingerprint = $2,
		run.trigger_kind, run.status, run.execution_mode, run.trigger_input_preview::text, run.trigger_payload::text, run.progress::text,
		run.result_expires_at - run.admitted_at, adm.status, adm.request_bytes, adm.idempotency_hash = $3, adm.payload_hash = $2,
		adm.request_payload->'params', thread.title, thread.channel, work.status, work.actor_role, work.backend,
		res.source, res.reserved_micros)
		from compat_webhook_run compat join workflow_run run on run.id = compat.workflow_run_id
		join workflow_api_admission adm on adm.workflow_run_id = run.id
		join work_thread thread on thread.id = run.thread_id join work_run work on work.id = run.work_run_id
		join spend_reservation res on res.work_run_id = work.id where compat.id = $1`,
		runID, print, sha256Hex("reckon:"+hookID+":job-1"))
	want := `single | job-1 | t | api | queued | single | {"mode": "single", "params": {"account": "AC1", "accounts": [1.5, 1000000000000000000000]}} | ` +
		`{"mode": "single", "source": "reckon_webhook", "reckonWorkflowId": "` + hookID + `"} | {"stage": "queued"} | 7 days | pending | ` +
		`56 | t | t | {"account": "AC1", "accounts": [1.5, 1000000000000000000000]} | Webhook ` + hookID + ` | workflow | queued | service | ax | webhook | 5000000`
	if got != want {
		t.Errorf("rows:\n%s\n%s", got, want)
	}
	message := queryText(t, db, `select adm.request_payload->'openneko_reckon_webhook'->>'message' from workflow_api_admission adm
		join compat_webhook_run compat on compat.workflow_run_id = adm.workflow_run_id where compat.id = $1`, runID)
	started := queryText(t, db, `select to_char(created_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') from compat_webhook_run where id = $1`, runID)
	wantMessage := "[webhook run started at " + started + "] Begin executing the workflow.\n\nTrigger parameters (JSON):\n```json\n" +
		"{\n  \"account\": \"AC1\",\n  \"accounts\": [\n    1.5,\n    1e+21\n  ]\n}\n```\n" +
		"The same parameters are saved as JSON in $RUN_DIR/trigger_input.json. Read large inputs from that file.\n\n" +
		"[output] Write your single deliverable to $ARTIFACT_DIR/result.json. It is the only file the caller is served.\n" +
		"Keep every intermediate and row-level file under $RUN_DIR. Never print row data into the reply.\n\n" +
		"Reply with a short receipt only: counts and the artifact name."
	if message != wantMessage {
		t.Errorf("message:\n%s", message)
	}

	w = hookCall(f, "POST", "/api/hooks/"+hookID, `{"accounts":[1.5,1e21],"account":"AC1"}`, nil)
	if w.Code != 202 || w.Body.String() != pending(runID, "queued", true) || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay: %d %s", w.Code, w.Body)
	}
	if w := hookCall(f, "POST", "/api/hooks/"+hookID, `{"account":"AC2"}`, nil); w.Code != 409 || w.Body.String() != `{"error":"idempotency_conflict"}` {
		t.Errorf("conflict: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_run set status = 'running' where id = (select workflow_run_id from compat_webhook_run where id = $1)`, runID)
	if w := hookCall(f, "POST", "/api/hooks/"+hookID, body, nil); w.Code != 202 || w.Body.String() != pending(runID, "running", true) {
		t.Errorf("running replay: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update workflow_run set status = 'failed', error = 'agent failed' where id = (select workflow_run_id from compat_webhook_run where id = $1)`, runID)
	if w := hookCall(f, "POST", "/api/hooks/"+hookID, body, nil); w.Code != 502 || w.Header().Get("Idempotency-Replayed") != "true" ||
		w.Body.String() != `{"ok":false,"runId":"`+runID+`","status":"error","error":"agent failed"}` {
		t.Errorf("failed replay: %d %s", w.Code, w.Body)
	}

	w = hookCall(f, "POST", "/api/hooks/"+hookOpen+"?mode=batch", "\xef\xbb\xbf"+`{"z":1,"2":0,"a":"\u2028","1":{"b":[],"a":{}}}`, map[string]string{"Idempotency-Key": " k:2 "})
	if w.Code != 202 {
		t.Fatalf("batch: %d %s", w.Code, w.Body)
	}
	batchID := runIDRe.FindString(w.Body.String())
	message = queryText(t, db, `select adm.request_payload->'openneko_reckon_webhook'->>'message' from workflow_api_admission adm
		join compat_webhook_run compat on compat.workflow_run_id = adm.workflow_run_id where compat.id = $1`, batchID)
	if !strings.Contains(message, "```json\n{\n  \"1\": {\n    \"b\": [],\n    \"a\": {}\n  },\n  \"2\": 0,\n  \"z\": 1,\n  \"a\": \"\u2028\"\n}\n```") ||
		!strings.Contains(message, "$ARTIFACT_DIR/result.csv") ||
		!strings.HasSuffix(message, "[batch] Execute the complete workflow in chunks of at most 3 records.\n"+
			"Pass intermediate datasets between steps by file path, and publish exactly one CSV artifact.\n\n"+
			"Reply with a short receipt only: counts and the artifact name.") {
		t.Errorf("batch message:\n%s", message)
	}
	if got := queryText(t, db, `select idempotency_key || ' ' || (request_fingerprint = $2) from compat_webhook_run where id = $1`, batchID,
		sha256Hex(`{"executionMode":"batch","params":{"1":{"a":{},"b":[]},"2":0,"a":"`+"\u2028"+`","z":1}}`)); got != "k:2 true" {
		t.Errorf("batch row: %s", got)
	}
}

func ulidTime(id string) time.Time {
	var ms int64
	for _, c := range id[:10] {
		ms = ms*32 + int64(strings.IndexRune(ulidAlphabet, c))
	}
	return time.UnixMilli(ms)
}

func TestHookLimits(t *testing.T) {
	f, db, worker := hooksFront(t)
	t.Setenv("HEADLESS_WEBHOOK_RATE_PER_MINUTE", " 2 ")
	for i, want := range []int{202, 202, 429} {
		w := hookCall(f, "POST", "/api/hooks/"+hookID, `{}`, map[string]string{"Idempotency-Key": "rate-" + string(rune('a'+i))})
		if w.Code != want {
			t.Errorf("start %d: %d %s", i, w.Code, w.Body)
		}
		if want == 429 && (w.Body.String() != `{"error":"workflow_rate"}` || w.Header().Get("Retry-After") == "") {
			t.Errorf("rate: %s %v", w.Body, w.Header())
		}
	}
	t.Setenv("HEADLESS_WEBHOOK_RATE_PER_MINUTE", "")
	t.Setenv("HEADLESS_QUEUE_MAX_PER_WORKFLOW", "3")
	if w := hookCall(f, "POST", "/api/hooks/"+hookID, `{}`, map[string]string{"Idempotency-Key": "q"}); w.Code != 429 ||
		w.Body.String() != `{"error":"workflow_queue_full"}` || w.Header().Get("Retry-After") != "10" {
		t.Errorf("queue: %d %s", w.Code, w.Body)
	}
	t.Setenv("HEADLESS_QUEUE_MAX_PER_WORKFLOW", "")

	seed(t, db, `update spend_limit set org_hourly_micros = 4000000 where org_id = 'org' and workflow_id is null`)
	w := hookCall(f, "POST", "/api/hooks/"+hookID, `{}`, map[string]string{"Idempotency-Key": "budget"})
	if w.Code != 429 || !regexp.MustCompile(`^\{"error":"global_hourly_budget","limitUsd":4,"committedUsd":\d+(\.\d+)?,"reservationUsd":5\}$`).MatchString(w.Body.String()) {
		t.Errorf("budget: %d %s", w.Code, w.Body)
	}
	if ra, _ := strconv.Atoi(w.Header().Get("Retry-After")); ra < 1 || ra > 3600 {
		t.Errorf("budget retry: %d", ra)
	}
	alert := queryText(t, db, `select concat_ws(' | ', kind, subject, threshold, window_seconds, details->>'budget', details->>'source', details->>'message')
		from behavior_alert where org_id = 'org'`)
	reset := time.Now().UTC().Truncate(time.Hour).Add(time.Hour).Format("15:04")
	if alert != "spend.budget_blocked | org | 400 | 3600 | org_hourly | webhook | A run was blocked. The hourly spending limit of $4.00 for this organization is reached. Try again after "+reset+" UTC." {
		t.Errorf("alert: %s", alert)
	}
	worker.mu.Lock()
	calls := strings.Join(worker.calls, "\n")
	worker.mu.Unlock()
	if !strings.Contains(calls, `POST /admin/events/external {"dedupeKey":"spend.budget_blocked:org:`) ||
		!strings.Contains(calls, `"name":"spend.budget_blocked","orgId":"org","payload":{"budget":"org_hourly"`) ||
		!strings.Contains(calls, `"source":"spend-governance"`) {
		t.Errorf("dispatch: %s", calls)
	}
	hookCall(f, "POST", "/api/hooks/"+hookID, `{}`, map[string]string{"Idempotency-Key": "budget2"})
	if n := queryText(t, db, `select count(*)::text from behavior_alert where org_id = 'org'`); n != "1" {
		t.Errorf("alerts: %s", n)
	}
	if n := queryText(t, db, `select count(*)::text from compat_webhook_run where idempotency_key like 'budget%'`); n != "0" {
		t.Errorf("budget runs: %s", n)
	}
}

func writeArtifact(t *testing.T, db *pgxpool.Pool, runID, name, body string) {
	t.Helper()
	work := queryText(t, db, `select work_run_id::text from compat_webhook_run where id = $1`, runID)
	dir := filepath.Join(orgAgentRoot("org"), "runs", work, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHookResult(t *testing.T) {
	f, db, _ := hooksFront(t)
	runID := runIDRe.FindString(hookCall(f, "POST", "/api/hooks/"+hookID, `{"account":"AC1"}`, nil).Body.String())
	get := func(id string, header map[string]string) *httptest.ResponseRecorder {
		return hookCall(f, "GET", "/api/hooks/"+hookID+"/result/"+id, "", header)
	}
	if w := get(runID, nil); w.Code != 202 || w.Header().Get("Retry-After") != "10" ||
		w.Body.String() != `{"ok":true,"status":"queued","runId":"`+runID+`","progress":{"stage":"queued"}}` {
		t.Errorf("queued: %d %s", w.Code, w.Body)
	}
	if w := get("01JNOPE0000000000000000000", nil); w.Code != 404 || w.Body.String() != `{"error":"not found"}` {
		t.Errorf("unknown: %d %s", w.Code, w.Body)
	}
	if w := get(runID, map[string]string{"X-Webhook-Token": "wrong"}); w.Code != 401 {
		t.Errorf("token: %d", w.Code)
	}
	set := func(sql string) {
		seed(t, db, `update workflow_run set `+sql+` where id = (select workflow_run_id from compat_webhook_run where id = $1)`, runID)
	}
	set(`status = 'completed'`)
	if w := get(runID, nil); w.Code != 404 || w.Body.String() != `{"error":"no_artifacts","status":"completed","runId":"`+runID+`"}` {
		t.Errorf("none: %d %s", w.Code, w.Body)
	}
	writeArtifact(t, db, runID, "one.csv", "a\n")
	writeArtifact(t, db, runID, "Two.csv", "b\n")
	if w := get(runID, nil); w.Code != 409 || w.Body.String() != `{"error":"ambiguous_artifacts","status":"completed","runId":"`+runID+`","files":["Two.csv","one.csv"]}` {
		t.Errorf("ambiguous: %d %s", w.Code, w.Body)
	}
	writeArtifact(t, db, runID, "RESULT.JSON", `{"ok":true,"band":"Dormant"}`)
	w := get(runID, nil)
	if w.Code != 200 || w.Body.String() != `{"ok":true,"band":"Dormant"}` || w.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		w.Header().Get("Content-Disposition") != `inline; filename="RESULT.JSON"` || w.Header().Get("Cache-Control") != "no-store" ||
		w.Header().Get("Content-Length") != "28" {
		t.Errorf("served: %d %s %v", w.Code, w.Body, w.Header())
	}
	set(`status = 'failed', error = 'agent failed', progress = '{"stage":"x","phase":"y","2":1}'`)
	if w := get(runID, nil); w.Code != 502 || w.Body.String() != `{"error":"run_failed","status":"error","runId":"`+runID+
		`","message":"agent failed","progress":{"2":1,"phase":"failed","stage":"x","message":"agent failed"}}` {
		t.Errorf("failed: %d %s", w.Code, w.Body)
	}
	set(`status = 'cancelled', error = null, progress = 'null'`)
	if w := get(runID, nil); w.Code != 409 || w.Body.String() != `{"error":"run_failed","status":"aborted","runId":"`+runID+
		`","message":"Run did not produce an artifact.","progress":{"phase":"aborted","message":"Run did not produce an artifact."}}` {
		t.Errorf("cancelled: %d %s", w.Code, w.Body)
	}
	set(`status = 'completed', result_expires_at = now() - interval '1 hour'`)
	if w := get(runID, nil); w.Code != 410 || w.Body.String() != `{"error":"artifact_expired","status":"completed","runId":"`+runID+`"}` {
		t.Errorf("expired: %d %s", w.Code, w.Body)
	}

	batch := runIDRe.FindString(hookCall(f, "POST", "/api/hooks/"+hookID+"?mode=batch", `{}`, map[string]string{"Idempotency-Key": "b"}).Body.String())
	seed(t, db, `update workflow_run set status = 'completed', progress = '[7]' where id = (select workflow_run_id from compat_webhook_run where id = $1)`, batch)
	writeArtifact(t, db, batch, "result.json", "{}")
	if w := get(batch, nil); w.Code != 502 || w.Body.String() != `{"error":"run_failed","status":"error","runId":"`+batch+
		`","message":"Batch run ended without publishing the required result.csv artifact.","progress":{"0":7,"phase":"failed",`+
		`"message":"Batch run ended without publishing the required result.csv artifact."}}` {
		t.Errorf("batch missing: %d %s", w.Code, w.Body)
	}
	writeArtifact(t, db, batch, "result.csv", "a,b\n1,2\n")
	if w := get(batch, nil); w.Code != 200 || w.Body.String() != "a,b\n1,2\n" || w.Header().Get("Content-Disposition") != `attachment; filename="result.csv"` ||
		w.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Errorf("batch: %d %s %v", w.Code, w.Body, w.Header())
	}

	t.Setenv("HEADLESS_RESULT_RATE_PER_MINUTE", "1")
	seed(t, db, `delete from workflow_api_rate_bucket`)
	get(batch, nil)
	if w := get(batch, nil); w.Code != 429 || w.Body.String() != `{"error":"rate_limited"}` || w.Header().Get("Retry-After") == "" {
		t.Errorf("poll rate: %d %s", w.Code, w.Body)
	}
}

func TestHookWait(t *testing.T) {
	f, db, _ := hooksFront(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := context.Background()
		for i := 0; i < 100; i++ {
			var id string
			if db.QueryRow(ctx, `select id from compat_webhook_run where idempotency_key = 'wait'`).Scan(&id) == nil {
				writeArtifact(t, db, id, "result.json", `{"done":true}`)
				_, _ = db.Exec(ctx, `update workflow_run set status = 'completed' where id = (select workflow_run_id from compat_webhook_run where id = $1)`, id)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	w := hookCall(f, "POST", "/api/hooks/"+hookID+"?wait=true&wait_timeout=5", `{}`, map[string]string{"Idempotency-Key": "wait"})
	<-done
	if w.Code != 200 || w.Body.String() != `{"done":true}` {
		t.Errorf("wait: %d %s", w.Code, w.Body)
	}
	start := time.Now()
	w = hookCall(f, "POST", "/api/hooks/"+hookID+"?wait=1", `{}`, map[string]string{"Idempotency-Key": "short"})
	if took := time.Since(start); w.Code != 202 || !strings.Contains(w.Body.String(), `"status":"queued"`) || took < time.Second || took > 3*time.Second {
		t.Errorf("default wait: %d %s %v", w.Code, w.Body, took)
	}
	f.API.hookWaiters.Store(hookSyncWaiters)
	start = time.Now()
	if w := hookCall(f, "POST", "/api/hooks/"+hookID+"?wait=true", `{}`, map[string]string{"Idempotency-Key": "busy"}); w.Code != 202 || time.Since(start) > time.Second {
		t.Errorf("busy: %d", w.Code)
	}
}

func TestHookHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"ok":                       "ok",
		"\xef\xbb\xbf{}":           "{}",
		"a\xe2\x82b":               "a\ufffdb",
		"a\xff\xfeb":               "a\ufffd\ufffdb",
		"\xf0\x9f\x98":             "\ufffd",
		"\xed\xa0\x80":             "\ufffd\ufffd\ufffd",
		"\xef\xbb\xbf\xef\xbb\xbf": "\ufeff",
	} {
		if got := decodeText([]byte(in)); got != want {
			t.Errorf("decode %q: %q", in, got)
		}
	}
	for in, want := range map[float64]string{4: "4.00", 0.125: "0.13", 1.005: "1.00", 2.675: "2.67", 0.5: "0.50", 1234.5678: "1234.57"} {
		if got := jsToFixed2(in); got != want {
			t.Errorf("toFixed %v: %s", in, got)
		}
	}
	v, err := jsParse([]byte(`{"b":1,"10":2,"a":{"y":-0,"x":1e400},"2":[0.1,1e-7],"b":3,"01":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := jsStringify(v, false); got != `{"2":[0.1,1e-7],"10":2,"b":3,"a":{"y":0,"x":null},"01":4}` {
		t.Errorf("stringify: %s", got)
	}
	if got := jsCanonical(v); got != `{"01":4,"10":2,"2":[0.1,1e-7],"a":{"x":null,"y":0},"b":3}` {
		t.Errorf("canonical: %s", got)
	}
	if got := largestArray(v); got != 2 {
		t.Errorf("largest: %d", got)
	}
	if id := ulid(1_700_000_000_000); !runIDRe.MatchString(id) || id[:10] != "01HF7YAT00" || !ulidTime(id).Equal(time.UnixMilli(1_700_000_000_000)) {
		t.Errorf("ulid: %s", id)
	}
	if got := groupThousands(1000); got != "1,000" {
		t.Errorf("group: %s", got)
	}
}
