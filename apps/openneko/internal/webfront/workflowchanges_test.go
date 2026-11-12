package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func queryText(t *testing.T, db *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func TestWorkflowPatch(t *testing.T) {
	f, db := workflowsFront(t)
	member := sessionCookie("member")
	state := func(id string) string {
		return queryText(t, db, `select enabled || ',' || cron_enabled || ',' || coalesce(to_char(paused_until at time zone 'UTC', 'YYYY-MM-DD HH24:MI'), '-')
			from workflow_definition where id = $1`, id)
	}
	for _, c := range []struct{ body, want, state string }{
		{`{"cronEnabled":false}`, `{"ok":true}`, "true,false,-"},
		{`{"pauseForToday":true,"enabled":true}`, `{"ok":true}`, "false,false," + time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02") + " 00:00"},
		{`{"enabled":true,"cronEnabled":true}`, `{"ok":true}`, "true,true,-"},
		{`{"enabled":"no","pauseForToday":false}`, `{"error":"no recognized fields"}`, "true,true,-"},
		{`not json`, `{"error":"no recognized fields"}`, "true,true,-"},
		{`null`, `{"error":"internal server error"}`, "true,true,-"},
	} {
		w := call(f, "PATCH", "/api/v1/workflows/"+wfHeld, c.body, member)
		if w.Body.String() != c.want || state(wfHeld) != c.state {
			t.Errorf("%s: %d %s, state %s", c.body, w.Code, w.Body, state(wfHeld))
		}
	}
	for _, c := range []struct{ id, body, want string }{
		{wfOther, `{"enabled":false}`, `{"error":"Workflow not found"}`},
		{"nope", `{"enabled":false}`, `{"error":"workflow not found"}`},
		{"nope", `{}`, `{"error":"no recognized fields"}`},
		{"10000000-0000-4000-8000-0000000000ff", `{"enabled":false}`, `{"error":"workflow not found"}`},
	} {
		if w := call(f, "PATCH", "/api/v1/workflows/"+c.id, c.body, member); w.Body.String() != c.want {
			t.Errorf("%s %s: %d %s", c.id, c.body, w.Code, w.Body)
		}
	}
	if state(wfOther) != "true,true,-" {
		t.Errorf("hidden workflow changed: %s", state(wfOther))
	}
}

func TestWorkflowDelete(t *testing.T) {
	f, db := workflowsFront(t)
	member := sessionCookie("member")
	if w := call(f, "DELETE", "/api/v1/workflows/"+wfOther, "", member); w.Code != 404 || w.Body.String() != `{"error":"Workflow not found"}` {
		t.Errorf("hidden: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/workflows/"+wfOwned, "", member); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("own: %d %s", w.Code, w.Body)
	}
	if n := queryText(t, db, `select count(*)::text from workflow_run where workflow_id = $1`, wfOwned); n != "0" {
		t.Errorf("runs left: %s", n)
	}
	for _, id := range []string{wfOwned, "nope"} {
		if w := call(f, "DELETE", "/api/v1/workflows/"+id, "", member); w.Code != 404 || w.Body.String() != `{"error":"workflow not found"}` {
			t.Errorf("%s: %d %s", id, w.Code, w.Body)
		}
	}
	if n := queryText(t, db, `select count(*)::text from workflow_definition where id = $1`, wfOther); n != "1" {
		t.Errorf("hidden workflow deleted")
	}
}

func TestWorkflowAdopt(t *testing.T) {
	f, db := workflowsFront(t)
	seed(t, db, `update workflow_definition set description = 'Watch <churn>', goal = 'Alert "fast"', cron = '0 9 * * 1',
		system_prompt_overlay = 'Short', network_hosts = '{api.example.com}', daily_run_budget = 4,
		steps = '[{"id":"s1","description":"Read"},"Plain",7,{"id":"s3"},[1,null,"x"]]', output_contract = '{"b":1.50}'
		where id = $1`, wfOwned)
	if w := call(f, "POST", "/api/v1/workflows/"+wfOwned+"/adopt", "", sessionCookie("member")); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
		t.Errorf("member: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", "/api/v1/workflows/"+wfOther+"/adopt", "", sessionCookie("member")); w.Code != 404 || w.Body.String() != `{"error":"Workflow not found"}` {
		t.Errorf("member, hidden: %d %s", w.Code, w.Body)
	}
	admin := sessionCookie("admin")
	for _, id := range []string{wfOrg, "nope", "10000000-0000-4000-8000-0000000000ff"} {
		if w := call(f, "POST", "/api/v1/workflows/"+id+"/adopt", "", admin); w.Code != 404 || w.Body.String() != `{"error":"Personal workflow not found: `+id+`"}` {
			t.Errorf("%s: %d %s", id, w.Code, w.Body)
		}
	}
	w := call(f, "POST", "/api/v1/workflows/"+wfOwned+"/adopt", "", admin)
	body := w.Body.String()
	copyID := queryText(t, db, `select id::text from workflow_definition where org_id = 'org' and owner_user_id = '' and name = 'Mine'`)
	want := `{"workflow":{"id":"` + copyID + `","orgId":"org","name":"Mine","description":"Watch <churn>","enabled":true,"status":"active",` +
		`"goal":"Alert \"fast\"","systemPromptOverlay":"Short","steps":[{"id":"s1","description":"Read"},"Plain",7,{"id":"s3"},[1,null,"x"]],` +
		`"cron":"0 9 * * 1","cronTimezone":"UTC","cronEnabled":true,"networkHosts":[],"dailyRunBudget":4,"outputContract":{"b":1.5},` +
		`"createdByThreadId":null,"createdByRunId":null,"ownerUserId":"","originId":"` + copyID + `","parentId":"` + wfOwned + `","createdAt":"`
	if w.Code != 200 || !strings.HasPrefix(body, want) {
		t.Fatalf("adopt: %d %s", w.Code, body)
	}
	file, err := os.ReadFile(filepath.Join(orgAgentRoot("org"), "workflows", "mine.md"))
	wantFile := "---\ntype: Workflow\nname: \"Mine\"\ngoal: \"Alert \\\"fast\\\"\"\ncron: \"0 9 * * 1\"\ncron_timezone: \"UTC\"\n---\n\n" +
		"Watch <churn>\n\n## Steps\n1. Read\n2. Plain\n3. 7\n4. [object Object]\n5. 1,,x\n\n## System prompt overlay\n\nShort\n"
	if err != nil || string(file) != wantFile {
		t.Errorf("file: %v\n%q", err, file)
	}
	changes := queryText(t, db, `select string_agg(summary || '|' || scope || '|' || user_id || '|' || coalesce(actor_user_id, '-') || '|' ||
		status || '|' || (commit_sha is not null)::text, ';' order by created_at, summary) from config_change where org_id = 'org' and artifact_kind = 'workflow'`)
	if changes != "Added workflow: Mine|team||-|recorded|true;Adopted workflow for the team: Mine|team|member|admin|adopted|false" {
		t.Errorf("config changes: %s", changes)
	}
	seed(t, db, `update workflow_definition set network_hosts = '{keep.example.com}' where id = $1`, copyID)
	seed(t, db, `update workflow_definition set goal = 'Second' where id = $1`, wfOwned)
	if w := call(f, "POST", "/api/v1/workflows/"+wfOwned+"/adopt", "", admin); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+copyID+`"`) ||
		!strings.Contains(w.Body.String(), `"goal":"Second","systemPromptOverlay"`) || !strings.Contains(w.Body.String(), `"networkHosts":["keep.example.com"]`) {
		t.Errorf("second adopt: %d %s", w.Code, w.Body)
	}
	if n := queryText(t, db, `select count(*)::text from config_change where org_id = 'org' and summary = 'Updated workflow: Mine'`); n != "1" {
		t.Errorf("update versions: %s", n)
	}
}

func TestWorkflowSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Mine": "mine", "  Late Orders!! (EU) ": "late-orders-eu", "---": "", "Café Daily": "caf-daily",
		strings.Repeat("ab ", 40): strings.Repeat("ab-", 26) + "ab",
	} {
		if got := workflowSlug(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestBatchColumns(t *testing.T) {
	ok := `{"apiBatch":{"version":1,"compiled":true,"compiler":"workflow","recordsField":"rows","columns":[{"name":"SKU","path":"item.sku","default":null},{"name":"Qty","path":"q"}]}}`
	if field, cols, valid := batchColumns([]byte(ok)); !valid || field != "rows" || strings.Join(cols, ",") != "SKU,Qty" {
		t.Errorf("valid: %v %s %v", valid, field, cols)
	}
	if field, _, valid := batchColumns([]byte(strings.Replace(ok, `"rows"`, `"9rows"`, 1))); !valid || field != "records" {
		t.Errorf("bad records field: %v %s", valid, field)
	}
	for _, bad := range []string{
		`null`, `{}`, `{"apiBatch":[]}`,
		strings.Replace(ok, `"version":1`, `"version":2`, 1),
		strings.Replace(ok, `"compiled":true`, `"compiled":1`, 1),
		strings.Replace(ok, `"path":"q"`, `"path":"a.__proto__"`, 1),
		strings.Replace(ok, `"path":"q"`, `"path":"a..b"`, 1),
		strings.Replace(ok, `"name":"Qty"`, `"name":"SKU"`, 1),
		strings.Replace(ok, `"name":"Qty"`, `"name":"Q\nty"`, 1),
		strings.Replace(ok, `"default":null`, `"default":[1]`, 1),
		strings.Replace(ok, `"name":"Qty"`, `"name":"`+strings.Repeat("é", 81)+`"`, 1),
		`{"apiBatch":{"version":1,"compiled":true,"compiler":"workflow","columns":[]}}`,
	} {
		if _, _, valid := batchColumns([]byte(bad)); valid {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestDefaultAPILimits(t *testing.T) {
	l := defaultAPILimits()
	if l[limRuntime] != 900 || l[limToolCalls] != 150 || l[limModelCalls] != 301 {
		t.Errorf("defaults: %v", l)
	}
	t.Setenv("OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", "61500.9")
	t.Setenv("OPENNEKO_WORKFLOW_MAX_TOOL_CALLS", "600")
	if l := defaultAPILimits(); l[limRuntime] != 62 || l[limToolCalls] != 600 || l[limModelCalls] != 1024 {
		t.Errorf("env: %v", l)
	}
	t.Setenv("OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", "5")
	t.Setenv("OPENNEKO_WORKFLOW_MAX_TOOL_CALLS", "abc")
	if l := defaultAPILimits(); l[limRuntime] != 30 || l[limToolCalls] != 150 {
		t.Errorf("clamped: %v", l)
	}
}

var tokenPattern = regexp.MustCompile(`"token":"(onk_wf_[0-9a-f]{12})_[A-Za-z0-9_-]{43}"`)

func TestWorkflowAPIAccess(t *testing.T) {
	f, db := workflowsFront(t)
	admin := sessionCookie("admin")
	path := "/api/v1/workflows/" + wfOrg + "/api-access"
	for _, m := range []string{"GET", "POST", "PATCH", "DELETE"} {
		if w := call(f, m, path, `{"action":"enable"}`, sessionCookie("member")); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
			t.Errorf("member %s: %d %s", m, w.Code, w.Body)
		}
	}
	defaults := `"limits":{"requestLimitPerMinute":30,"pollLimitPerMinute":120,"queueCap":25,"concurrencyCap":2,"batchMaxRecords":1000,` +
		`"batchChunkSize":100,"maxRequestBytes":262144,"maxResultBytes":262144,"maxArtifactBytes":10485760,"maxRuntimeSeconds":900,` +
		`"maxModelCalls":301,"maxToolCalls":150,"maxTokensPerRun":2000000,"maxCostMicrosPerRun":5000000,"rollingWindowSeconds":86400,` +
		`"rollingTokenBudget":10000000,"rollingCostMicrosBudget":10000000,"retentionHours":168}`
	w := call(f, "GET", path, "", admin)
	want := `{"access":{"workflowId":"` + wfOrg + `","enabled":false,"tokenPrefix":null,"tokenCreatedAt":null,"tokenRotatedAt":null,` +
		`"lastUsedAt":null,` + defaults + `,"includeTelemetry":false,"batch":{"available":false,"recordsField":null,"columns":[]},` +
		`"createdAt":null,"updatedAt":null},"lastRequest":null,"publicUrl":"https://neko.example"}`
	if w.Code != 200 || w.Body.String() != want || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Errorf("GET: %d %s %v", w.Code, w.Body, w.Header())
	}
	for _, id := range []string{"nope", "10000000-0000-4000-8000-0000000000ff"} {
		for _, m := range []string{"GET", "POST", "PATCH", "DELETE"} {
			want := `{"error":{"code":"workflow_not_found","message":"Workflow not found."}}`
			if w := call(f, m, "/api/v1/workflows/"+id+"/api-access", `{"action":"enable","limits":{"queueCap":3}}`, admin); w.Code != 404 || w.Body.String() != want {
				t.Errorf("%s %s: %d %s", m, id, w.Code, w.Body)
			}
		}
	}
	steps := []struct{ method, body string }{{"POST", `{"action":"rotate"}`}, {"POST", `{"action":"x"}`}, {"POST", `null`}}
	for i, want := range []string{
		`{"error":{"code":"api_access_disabled","message":"Enable API access before rotating its token."}}`,
		`{"error":{"code":"invalid_action","message":"Action must be 'enable' or 'rotate'."}}`,
		`{"error":{"code":"internal_error","message":"Request failed."}}`,
	} {
		if w := call(f, steps[i].method, path, steps[i].body, admin); w.Body.String() != want {
			t.Errorf("%s: %d %s", steps[i].body, w.Code, w.Body)
		}
	}
	w = call(f, "POST", path, `{"action":"enable"}`, admin)
	m := tokenPattern.FindStringSubmatch(w.Body.String())
	if w.Code != 201 || m == nil || !strings.Contains(w.Body.String(), `"enabled":true,"tokenPrefix":"`+m[1]+`","tokenCreatedAt":"`) ||
		!strings.Contains(w.Body.String(), `"tokenRotatedAt":null`) {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	token := m[0][len(`"token":"`) : len(m[0])-1]
	sum := sha256.Sum256([]byte(token))
	if v := queryText(t, db, `select token_verifier from workflow_api_access where workflow_id = $1`, wfOrg); v != hex.EncodeToString(sum[:]) {
		t.Errorf("verifier %s", v)
	}
	if w := call(f, "POST", path, `{"action":"enable"}`, admin); w.Code != 409 || !strings.Contains(w.Body.String(), "api_access_already_enabled") {
		t.Errorf("enable twice: %d %s", w.Code, w.Body)
	}
	w = call(f, "POST", path, `{"action":"rotate"}`, admin)
	if m2 := tokenPattern.FindStringSubmatch(w.Body.String()); w.Code != 200 || m2 == nil || m2[1] == m[1] || strings.Contains(w.Body.String(), `"tokenRotatedAt":null`) {
		t.Errorf("rotate: %d %s", w.Code, w.Body)
	}
	if w := call(f, "GET", path, "", admin); strings.Contains(w.Body.String(), `"token"`) || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Errorf("GET after enable: %s", w.Body)
	}

	for _, c := range []struct{ body, want string }{
		{`{}`, `{"code":"empty_limits","message":"At least one limit is required."}`},
		{`not json`, `{"code":"empty_limits","message":"At least one limit is required."}`},
		{`{"limits":{}}`, `{"code":"empty_limits","message":"At least one limit is required."}`},
		{`{"limits":null}`, `{"code":"invalid_limits","message":"Limits must be a JSON object."}`},
		{`{"limits":[1]}`, `{"code":"invalid_limits","message":"Limits must be a JSON object."}`},
		{`{"limits":{"zzz":1,"queueCap":0}}`, `{"code":"unknown_limit","message":"Unknown workflow API limit: zzz."}`},
		{`{"limits":{"queueCap":0,"zzz":1}}`, `{"code":"limit_out_of_range","message":"queueCap must be between 1 and 250."}`},
		{`{"limits":{"queueCap":0,"7":1}}`, `{"code":"unknown_limit","message":"Unknown workflow API limit: 7."}`},
		{`{"limits":{"queueCap":"5"}}`, `{"code":"invalid_limit","message":"queueCap must be an integer."}`},
		{`{"limits":{"queueCap":1.5}}`, `{"code":"invalid_limit","message":"queueCap must be an integer."}`},
		{`{"limits":{"queueCap":1e400}}`, `{"code":"invalid_limit","message":"queueCap must be an integer."}`},
		{`{"limits":{"queueCap":true}}`, `{"code":"invalid_limit","message":"queueCap must be an integer."}`},
		{`{"limits":{"batchChunkSize":50,"batchMaxRecords":20}}`, `{"code":"invalid_batch_limits","message":"batchChunkSize cannot exceed batchMaxRecords."}`},
		{`{"limits":{"batchMaxRecords":20}}`, `{"code":"invalid_batch_limits","message":"batchChunkSize cannot exceed batchMaxRecords."}`},
		{`{"includeTelemetry":"yes"}`, `{"code":"invalid_telemetry","message":"includeTelemetry must be true or false."}`},
		{`{"includeTelemetry":null}`, `{"code":"invalid_telemetry","message":"includeTelemetry must be true or false."}`},
		{`{"limits":{"queueCap":0},"includeTelemetry":"yes"}`, `{"code":"limit_out_of_range","message":"queueCap must be between 1 and 250."}`},
	} {
		if w := call(f, "PATCH", path, c.body, admin); w.Code != 400 || w.Body.String() != `{"error":`+c.want+`}` {
			t.Errorf("PATCH %s: %d %s", c.body, w.Code, w.Body)
		}
	}
	w = call(f, "PATCH", path, `{"limits":{"queueCap":3.0,"batchMaxRecords":250},"includeTelemetry":true}`, admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"queueCap":3,`) || !strings.Contains(w.Body.String(), `"batchMaxRecords":250,`) ||
		!strings.Contains(w.Body.String(), `"includeTelemetry":true`) || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Errorf("PATCH: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PATCH", "/api/v1/workflows/"+wfOwned+"/api-access", `{"limits":{"retentionHours":24}}`, admin); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"retentionHours":24}`) || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Errorf("PATCH without a row: %d %s", w.Code, w.Body)
	}
	if v := queryText(t, db, `select max_tool_calls || ',' || retention_hours from workflow_api_access where workflow_id = $1`, wfOwned); v != "150,24" {
		t.Errorf("inserted row: %s", v)
	}
	if w := call(f, "DELETE", path, "", admin); w.Code != 200 || !strings.Contains(w.Body.String(), `{"access":{"workflowId":"`+wfOrg+`","enabled":false,"tokenPrefix":"onk_wf_`) {
		t.Errorf("DELETE: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/workflows/"+wfOther+"/api-access", "", admin); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Errorf("DELETE without a row: %d %s", w.Code, w.Body)
	}
	events := queryText(t, db, `select string_agg(entity_id || ':' || event || ':' || payload::text, ' ' order by seq) from audit_chain
		where org_id = 'org' and entity_kind = 'workflow_api_access'`)
	wantEvents := regexp.MustCompile(`^` + wfOrg + `:enabled:\{"actorRole": "admin", "actorUserId": "admin", "tokenPrefix": "onk_wf_[0-9a-f]{12}"\} ` +
		wfOrg + `:token_rotated:\{[^}]+\} ` +
		wfOrg + `:limits_updated:\{"fields": \["batchMaxRecords", "includeTelemetry", "queueCap"\], "actorRole": "admin", "actorUserId": "admin"\} ` +
		wfOwned + `:limits_updated:\{"fields": \["retentionHours"\], [^}]+\} ` +
		wfOrg + `:disabled:\{"actorRole": "admin", "actorUserId": "admin"\}$`)
	if !wantEvents.MatchString(events) {
		t.Errorf("audit: %s", events)
	}
}

func TestWorkflowAPIAccessDetails(t *testing.T) {
	f, db := workflowsFront(t)
	admin := sessionCookie("admin")
	seed(t, db, `update workflow_definition set output_contract = '{"apiBatch":{"version":1,"compiled":true,"compiler":"workflow","columns":[{"name":"SKU","path":"sku"}]}}'
		where id = $1`, wfHeld)
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, config) values ('org', 'public-url', 'public-url', '{"url":"https://saved.example.com"}')`)
	for i, run := range []string{runOrg, runAPI} {
		seed(t, db, `insert into workflow_api_admission (org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash, execution_mode,
			request_payload, request_bytes, expires_at, created_at) values ('org', $1, $2, repeat($3, 64), repeat('a', 64), 'single', $4, 10,
			now() + interval '1 day', now() - interval '1 hour' * (2 - $5::int))`,
			wfHeld, run, string(rune('0'+i)), []string{`{"older":true}`, `{"sku":"A-1","n":2.50}`}[i], i)
	}
	w := call(f, "GET", "/api/v1/workflows/"+wfHeld+"/api-access", "", admin)
	for _, part := range []string{
		`"batch":{"available":true,"recordsField":"records","columns":["SKU"]},"createdAt":"`,
		`"lastRequest":{"n":2.5,"sku":"A-1"},"publicUrl":"https://saved.example.com"}`,
		`"limits":{"requestLimitPerMinute":30,"pollLimitPerMinute":120,"queueCap":25,"concurrencyCap":2,"batchMaxRecords":1000,"batchChunkSize":100,` +
			`"maxRequestBytes":262144,"maxResultBytes":262144,"maxArtifactBytes":10485760,"maxRuntimeSeconds":600,"maxModelCalls":8,"maxToolCalls":32,`,
	} {
		if w.Code != 200 || !strings.Contains(w.Body.String(), part) {
			t.Errorf("GET lacks %s\n%s", part, w.Body)
		}
	}
}
