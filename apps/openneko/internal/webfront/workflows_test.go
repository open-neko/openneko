package webfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	subHeld   = "f1000000-0000-4000-8000-000000000001"
	runAPI    = "f1000000-0000-4000-8000-000000000002"
	outOther  = "f1000000-0000-4000-8000-000000000003"
	workRunAP = "f1000000-0000-4000-8000-000000000004"
)

func workflowsFront(t *testing.T) (*Front, *pgxpool.Pool) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	t.Setenv("HOME", t.TempDir())
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seed(t, db, `update workflow_definition set updated_at = '2026-11-01T00:00:00Z'::timestamptz + (substr(id::text, 36)::int * interval '1 hour'),
		steps = '[{"id":"s1","description":"Check","n":1.50}]'`)
	seed(t, db, `update workflow_definition set cron = '0 7 * * *', cron_timezone = 'Asia/Kolkata', daily_run_budget = 5,
		system_prompt_overlay = 'Be brief', goal = 'Find late orders', description = 'Daily' where id = $1`, wfHeld)
	seed(t, db, `insert into subscription (id, org_id, workflow_id, source_kind, filter, debounce_ms) values
		($1, 'org', $2, 'workflow_output', '{"z":1,"a":{"b":2.50}}', 250)`, subHeld, wfHeld)
	seed(t, db, `update workflow_run set created_at = now(), finished_at = started_at + interval '2.5 seconds', status = 'completed',
		progress = '{"done":3}', telemetry_summary = '{"tokens":5}', summary = 'Two late', triggered_by_output_id = $2,
		triggered_by_subscription_id = $3, trigger_payload = '{"b":1.50,"a":[1e21]}', chain_depth = 1, trigger_kind = 'subscription'
		where id = $1`, runHeld, outputID, subHeld)
	seed(t, db, `update workflow_run set created_at = '2026-11-01T07:00:00Z'::timestamptz + interval '1 minute' * case id when $1 then 3 when $2 then 2 else 1 end
		where id in ($1, $2, $3)`, runOrg, runOwned, runOther)
	seed(t, db, `update work_run set status = 'completed', analysis_minutes_saved = 7 where id = (select work_run_id from workflow_run where id = $1)`, runHeld)
	seed(t, db, `update action_request set status = 'executed', minutes_saved = 3, created_at = now(), target = 'crm', summary = 'Note it' where id = $1`, arHeldRun)
	for _, p := range []string{`{"type":"text","n":1.50}`, `{"type":"tool_start"}`, `{"type":"tool_end"}`, `{"type":5}`, `{"type":null}`, `[1]`} {
		seed(t, db, `insert into work_run_event (org_id, thread_id, run_id, kind, payload)
			select 'org', thread_id, work_run_id, 'x', $2 from workflow_run where id = $1`, runHeld, p)
	}
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', '60000000-0000-4000-8000-000000000001', 'test')`, workRunAP)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, execution_mode, status,
		result_expires_at, result_artifact_path, created_at)
		values ($1, 'org', $2, '60000000-0000-4000-8000-000000000001', $3, 'api', 'batch', 'completed', now() + interval '1 day',
		'workflow-api/results/a.csv', '2026-10-01T00:00:00Z')`, runAPI, wfHeld, workRunAP)
	seed(t, db, `insert into workflow_api_access (workflow_id, org_id) values ($1, 'org')`, wfHeld)
	return f, db
}

func TestWorkflowList(t *testing.T) {
	f, _ := workflowsFront(t)
	if got := titles(t, call(f, "GET", "/api/v1/workflows", "", sessionCookie("member")).Body.Bytes(), "workflows", "name"); got != "Mine,Held flow" {
		t.Errorf("member: %s", got)
	}
	w := call(f, "GET", "/api/v1/workflows", "", sessionCookie("admin"))
	if got := titles(t, w.Body.Bytes(), "workflows", "name"); got != "Theirs,Mine,Held flow,Org flow" {
		t.Errorf("admin: %s", got)
	}
	want := `{"id":"` + wfHeld + `","name":"Held flow","description":"Daily","goal":"Find late orders","enabled":true,"status":"active",` +
		`"cron":"0 7 * * *","cronTimezone":"Asia/Kolkata","cronEnabled":true,"steps":[{"n":1.5,"id":"s1","description":"Check"}],` +
		`"createdAt":"`
	if !strings.Contains(w.Body.String(), want) || !strings.Contains(w.Body.String(), `"updatedAt":"2026-11-01T02:00:00.000Z"}`) {
		t.Errorf("admin body: %s", w.Body)
	}
}

func TestWorkflowDetail(t *testing.T) {
	f, _ := workflowsFront(t)
	member := sessionCookie("member")
	w := call(f, "GET", "/api/v1/workflows/"+wfHeld, "", member)
	body := w.Body.String()
	for _, part := range []string{
		`{"workflow":{"id":"` + wfHeld + `","name":"Held flow","description":"Daily","goal":"Find late orders","systemPromptOverlay":"Be brief",` +
			`"enabled":true,"status":"active","steps":[{"n":1.5,"id":"s1","description":"Check"}],"cron":"0 7 * * *","cronTimezone":"Asia/Kolkata",` +
			`"cronEnabled":true,"dailyRunBudget":5,"runsToday":1,"minutesSaved30d":10,"createdByThreadId":null,"createdByRunId":null,`,
		`"subscriptions":[{"id":"` + subHeld + `","sourceKind":"workflow_output","filter":{"a":{"b":2.5},"z":1},"enabled":true,"debounceMs":250}],`,
		`"recentRuns":[{"id":"` + runHeld + `","status":"completed","triggerKind":"subscription","executionMode":null,"queueAttempts":0,` +
			`"progress":{"done":3},"telemetry":{"tokens":5},"summary":"Two late","error":null,"startedAt":"2026-11-01T08:00:00.123Z",` +
			`"finishedAt":"2026-11-01T08:00:02.623Z","durationMs":2500,"createdAt":"`,
		`{"id":"` + runAPI + `","status":"completed","triggerKind":"api","executionMode":"batch",`,
		`"recentActions":[{"id":"` + arHeldRun + `","workflowRunId":"` + runHeld + `","kind":"note","target":"crm","status":"executed",` +
			`"riskLevel":"high","summary":"Note it","approvedAt":null,"createdAt":"`,
		`"activitySparkline":[0,0,0,0,0,0,0,0,0,0,0,0,0,1]}`,
	} {
		if w.Code != 200 || !strings.Contains(body, part) {
			t.Errorf("detail lacks %s\n%s", part, body)
		}
	}
	for _, c := range []struct{ id, who, want string }{
		{wfOrg, "member", `{"error":"Workflow not found"}`},
		{wfOther, "member", `{"error":"Workflow not found"}`},
		{"nope", "member", `{"error":"workflow not found"}`},
		{"10000000-0000-4000-8000-0000000000ff", "admin", `{"error":"workflow not found"}`},
	} {
		if w := call(f, "GET", "/api/v1/workflows/"+c.id, "", sessionCookie(c.who)); w.Code != 404 || w.Body.String() != c.want {
			t.Errorf("%s as %s: %d %s", c.id, c.who, w.Code, w.Body)
		}
	}
	if w := call(f, "GET", "/api/v1/workflows/"+wfOther, "", sessionCookie("admin")); w.Code != 200 {
		t.Errorf("admin on another member's workflow: %d", w.Code)
	}
	node := f.Node.(*recorder)
	for _, p := range []string{"/manual-runs", "/subscriptions"} {
		call(f, "POST", "/api/v1/workflows/"+wfHeld+p, `{}`, member)
	}
	if strings.Join(node.hits, ",") != "POST /api/v1/workflows/"+wfHeld+"/manual-runs,POST /api/v1/workflows/"+wfHeld+"/subscriptions" {
		t.Errorf("Node got %v", node.hits)
	}
}

func TestRunListLimit(t *testing.T) {
	for in, want := range map[string]int{
		"": 100, "abc": 100, "12abc": 12, " \t7": 7, "+3x": 3, "-3": 100, "0": 100, "0x10": 100, "1e3": 1,
		"500": 200, "200": 200, "00012": 12, "99999999999999999999": 200, "\uFEFF5": 5,
	} {
		if got := runListLimit(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func TestWorkflowRunList(t *testing.T) {
	f, db := workflowsFront(t)
	ids := func(path, who string) string {
		return titles(t, call(f, "GET", path, "", sessionCookie(who)).Body.Bytes(), "runs", "id")
	}
	if got := ids("/api/v1/workflow-runs", "member"); got != runHeld+","+runOwned+","+runAPI {
		t.Errorf("member: %s", got)
	}
	if got := ids("/api/v1/workflow-runs", "admin"); got != runHeld+","+runOrg+","+runOwned+","+runOther+","+runAPI {
		t.Errorf("admin: %s", got)
	}
	if got := ids("/api/v1/workflow-runs?status=active&limit=2", "admin"); got != runOrg+","+runOwned {
		t.Errorf("active: %s", got)
	}
	if got := ids("/api/v1/workflow-runs?status=completed", "admin"); got != runHeld+","+runAPI {
		t.Errorf("completed: %s", got)
	}
	seed(t, db, `update workflow_run set status = 'cancelled' where id = $1`, runOther)
	if got := ids("/api/v1/workflow-runs?status=failed", "admin"); got != runOther {
		t.Errorf("failed: %s", got)
	}
	w := call(f, "GET", "/api/v1/workflow-runs?status=bogus&limit=1x", "", sessionCookie("member"))
	want := `{"runs":[{"id":"` + runHeld + `","workflow":{"id":"` + wfHeld + `","name":"Held flow"},"triggerKind":"subscription",` +
		`"executionMode":null,"chainDepth":1,"status":"completed","queueAttempts":0,"progress":{"done":3},"telemetry":{"tokens":5},` +
		`"summary":"Two late","error":null,"startedAt":"2026-11-01T08:00:00.123Z","finishedAt":"2026-11-01T08:00:02.623Z","durationMs":2500,"createdAt":"`
	if !strings.HasPrefix(w.Body.String(), want) || !strings.HasSuffix(w.Body.String(), `"outputCount":0,"actionCount":1,"pendingActionCount":0}],"status":"all"}`) {
		t.Errorf("body: %s", w.Body)
	}
}

func TestWorkflowRunDetail(t *testing.T) {
	f, _ := workflowsFront(t)
	member := sessionCookie("member")
	w := call(f, "GET", "/api/v1/workflow-runs/"+runHeld, "", member)
	body := w.Body.String()
	for _, part := range []string{
		`{"workflow":{"id":"` + wfHeld + `","name":"Held flow","description":"Daily","goal":"Find late orders"},"run":{"id":"` + runHeld + `",` +
			`"workflowId":"` + wfHeld + `","threadId":"60000000-0000-4000-8000-000000000001","workRunId":"70000000-0000-4000-8000-000000000002",` +
			`"triggerKind":"subscription","triggerPayload":{"a":[1e+21],"b":1.5},"executionMode":null,"triggerInputPreview":null,"chainDepth":1,` +
			`"status":"completed","telemetry":{"tokens":5},"terminalResult":null,"progress":{"done":3},"queueAttempts":0,"admittedAt":null,` +
			`"resultExpiresAt":null,"artifactUrl":null,"summary":"Two late","error":null,"startedAt":"2026-11-01T08:00:00.123Z",` +
			`"finishedAt":"2026-11-01T08:00:02.623Z","createdAt":"`,
		`"outputs":[],"actions":[{"id":"` + arHeldRun + `","kind":"note","target":"crm","payload":{"a":[2],"b":1},"scope":"internal",` +
			`"riskLevel":"high","status":"executed","summary":"Note it","approvedAt":null,"rejectionReason":null,"createdAt":"`,
		`"type":"text","event":{"n":1.5,"type":"text"},"createdAt":"`,
		`"type":5,"event":{"type":5},`,
		`"type":"unknown","event":{"type":null},`,
		`"type":"unknown","event":[1],`,
		`"lineage":{"triggeredBySubscriptionId":"` + subHeld + `","triggeredByOutputId":"` + outputID + `","triggeredByObservationId":null,` +
			`"upstream":{"output":{"id":"` + outputID + `","title":"Late order","scope":null,"mood":null,"createdAt":"`,
		`"workflow":{"id":"` + wfOrg + `","name":"Org flow"},"workflowRunId":"` + runOrg + `"}}}`,
	} {
		if w.Code != 200 || !strings.Contains(body, part) {
			t.Errorf("run lacks %s\n%s", part, body)
		}
	}
	if strings.Contains(body, "tool_") {
		t.Errorf("tool events without detail=tools: %s", body)
	}
	tools := call(f, "GET", "/api/v1/workflow-runs/"+runHeld+"?detail=tools", "", member).Body.String()
	if strings.Count(tools, `"type":"tool_start","event"`)+strings.Count(tools, `"type":"tool_end","event"`) != 2 || strings.Count(tools, `"seq":`) != 6 {
		t.Errorf("detail=tools: %s", tools)
	}
	run := call(f, "GET", "/api/v1/workflow-runs/"+runOrg, "", sessionCookie("admin")).Body.String()
	if !strings.Contains(run, `"outputs":[{"id":"`+outputID+`","kind":"finding","title":"Late order","body":"","payload":{},"artifactPath":null,`+
		`"scope":null,"topic":null,"mood":null,"timeWindowStart":null,"timeWindowEnd":null,"freshnessTtlSeconds":null,"createdAt":"`) ||
		!strings.Contains(run, `"lineage":{"triggeredBySubscriptionId":null,"triggeredByOutputId":null,"triggeredByObservationId":null,"upstream":null}}`) {
		t.Errorf("org run: %s", run)
	}
	api := call(f, "GET", "/api/v1/workflow-runs/"+runAPI, "", member).Body.String()
	if !strings.Contains(api, `"artifactUrl":"/api/v1/workflow-runs/`+runAPI+`/artifact"`) || !strings.Contains(api, `"executionMode":"batch"`) {
		t.Errorf("api run: %s", api)
	}
	for _, c := range []struct{ id, who, want string }{
		{runOrg, "member", `{"error":"Run not found"}`},
		{runOther, "member", `{"error":"Run not found"}`},
		{"nope", "member", `{"error":"Workflow run not found"}`},
		{"20000000-0000-4000-8000-0000000000ff", "admin", `{"error":"Workflow run not found"}`},
	} {
		if w := call(f, "GET", "/api/v1/workflow-runs/"+c.id, "", sessionCookie(c.who)); w.Code != 404 || w.Body.String() != c.want {
			t.Errorf("%s as %s: %d %s", c.id, c.who, w.Code, w.Body)
		}
	}
}

func TestWorkflowRunArtifact(t *testing.T) {
	f, db := workflowsFront(t)
	member := sessionCookie("member")
	path := "/api/v1/workflow-runs/" + runAPI + "/artifact"
	results := filepath.Join(orgAgentRoot("org"), "workflow-api", "results")
	if err := os.MkdirAll(results, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(results, "a.csv"), []byte("sku,qty\nA-1,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := call(f, "GET", path, "", member)
	h := w.Header()
	if w.Code != 200 || w.Body.String() != "sku,qty\nA-1,3\n" || h.Get("Content-Type") != "text/csv; charset=utf-8" ||
		h.Get("Content-Length") != "14" || h.Get("Content-Disposition") != `attachment; filename="workflow-`+runAPI+`.csv"` ||
		h.Get("Cache-Control") != "no-store, private" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("artifact: %d %q %v", w.Code, w.Body, h)
	}

	if err := os.WriteFile(filepath.Join(results, "big.csv"), make([]byte, 1025), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "b.csv"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "b.csv"), filepath.Join(results, "link.csv")); err != nil {
		t.Fatal(err)
	}
	apiErr := func(code, message string) string {
		return `{"error":{"code":"` + code + `","message":"` + message + `"}}`
	}
	unavailable := apiErr("artifact_unavailable", "The workflow artifact is unavailable.")
	for _, c := range []struct {
		name, sql string
		status    int
		want      string
	}{
		{"too large", `with x as (update workflow_api_access set max_artifact_bytes = 1024 where workflow_id = '` + wfHeld + `')
			update workflow_run set result_artifact_path = 'workflow-api/results/big.csv' where id = $1`, 410, unavailable},
		{"missing", `update workflow_run set result_artifact_path = 'workflow-api/results/none.csv' where id = $1`, 410, unavailable},
		{"symlink out", `update workflow_run set result_artifact_path = 'workflow-api/results/link.csv' where id = $1`, 410, unavailable},
		{"directory", `update workflow_run set result_artifact_path = 'workflow-api/results' where id = $1`, 410, unavailable},
		{"escape", `update workflow_run set result_artifact_path = '../../b.csv' where id = $1`, 500, apiErr("internal_error", "Artifact download failed.")},
		{"not ready", `update workflow_run set status = 'running' where id = $1`, 404, apiErr("artifact_not_ready", "The workflow artifact is not available.")},
		{"expired", `update workflow_run set result_expires_at = now() - interval '1 second' where id = $1`, 410, apiErr("result_expired", "The retained API result has expired.")},
		{"not api", `update workflow_run set trigger_kind = 'manual', execution_mode = null where id = $1`, 404, apiErr("run_not_found", "Workflow run not found.")},
	} {
		if strings.Contains(c.sql, "$1") {
			seed(t, db, c.sql, runAPI)
		} else {
			seed(t, db, c.sql)
		}
		w := call(f, "GET", path, "", member)
		cache := w.Header().Get("Cache-Control")
		if w.Code != c.status || w.Body.String() != c.want || (c.status != 500) != (cache == "no-store, private") {
			t.Errorf("%s: %d %s %q", c.name, w.Code, w.Body, cache)
		}
	}
	for _, c := range []struct{ run, who, want string }{
		{runOrg, "member", `{"error":"Run not found"}`},
		{"nope", "member", apiErr("run_not_found", "Workflow run not found.")},
	} {
		if w := call(f, "GET", "/api/v1/workflow-runs/"+c.run+"/artifact", "", sessionCookie(c.who)); w.Code != 404 || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.run, w.Code, w.Body)
		}
	}
}
