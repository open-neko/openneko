package webfront

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type packRequest struct {
	method, uri, contentType, actor, body string
}

type packWorker struct {
	mu     sync.Mutex
	calls  []packRequest
	status int
	reply  string
}

func (p *packWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/auth/status" {
		_, _ = w.Write([]byte(liveScalekit))
		return
	}
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, packRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("X-OpenNeko-Actor"), string(body)})
	w.WriteHeader(p.status)
	_, _ = w.Write([]byte(p.reply))
}

func (p *packWorker) take() []packRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls := p.calls
	p.calls = nil
	return calls
}

func packFront(t *testing.T) (*Front, *packWorker) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	wk := &packWorker{status: 200, reply: `{"ok":true}`}
	srv := httptest.NewServer(wk)
	t.Cleanup(srv.Close)
	g := newGate(t, srv.URL)
	g.DB = db
	return &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example", SecretsDir: t.TempDir()},
		Node: &recorder{},
	}, wk
}

func TestAdminPacksAdminOnly(t *testing.T) {
	f, wk := packFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/packs", ""},
		{"POST", "/api/v1/admin/packs/upload", "ZIP"},
		{"GET", "/api/v1/admin/packs/magento/status", ""},
		{"POST", "/api/v1/admin/packs/magento/install", `{}`},
		{"GET", "/api/v1/admin/packs/magento/store-management", ""},
		{"POST", "/api/admin/packs/magento/store-management", `{}`},
		{"GET", "/api/v1/admin/reckon-webhooks", ""},
		{"POST", "/api/v1/admin/reckon-webhooks", `{}`},
		{"DELETE", "/api/v1/admin/reckon-webhooks/x", ""},
	} {
		header := map[string]string{"Content-Type": "application/zip"}
		for k, v := range sessionCookie("member") {
			header[k] = v
		}
		if w := call(f, c.method, c.path, c.body, header); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
		if w := call(f, c.method, c.path, c.body, nil); w.Code == 200 || w.Code == 201 {
			t.Errorf("%s %s as anonymous: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls: %v", calls)
	}
}

func TestAdminPackRelay(t *testing.T) {
	f, wk := packFront(t)
	admin := sessionCookie("admin")
	wk.reply = `{"packId":"magento","status":"ready"}`
	if w := call(f, "GET", "/api/v1/admin/packs/magento/doctor?version=1.2.0+rc", "", admin); w.Code != 200 || decode(t, w)["status"] != "blocked" {
		t.Errorf("doctor: %d %s", w.Code, w.Body.String())
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("doctor calls the worker: %v", calls)
	}
	for _, p := range []string{"/api/v1/admin/packs/magento/erase", "/api/v1/admin/packs/Magento/status", "/api/v1/admin/packs/magento/install"} {
		if w := call(f, "GET", p, "", admin); w.Code != 404 || w.Body.String() != `{"error":"unknown pack operation"}` {
			t.Errorf("GET %s: %d %s", p, w.Code, w.Body.String())
		}
	}
	if w := call(f, "POST", "/api/v1/admin/packs/magento/doctor", `{}`, admin); w.Code != 404 {
		t.Errorf("POST doctor: %d", w.Code)
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls for unknown operations: %v", calls)
	}
}

func TestAdminPackWrites(t *testing.T) {
	f, wk := packFront(t)
	admin := sessionCookie("admin")
	for _, action := range []string{"install", "configure", "upgrade"} {
		w := call(f, "POST", "/api/v1/admin/packs/magento/"+action, `{"actorUserId":"forged","inputs":{"database.host":"db"}}`, admin)
		if d, _ := decode(t, w)["diagnostic"].(map[string]any); w.Code != 400 || d["phase"] != action || len(wk.take()) != 0 {
			t.Errorf("Magento %s stays in Go: %d %s", action, w.Code, w.Body.String())
		}
	}
	for _, action := range []string{"install", "configure", "upgrade"} {
		w := call(f, "POST", "/api/v1/admin/packs/service-health/"+action, `{"version":"0.1.0","reviewHash":"exact-review","idempotencyKey":"k-1"}`, admin)
		if d, _ := decode(t, w)["diagnostic"].(map[string]any); w.Code != 400 || d["phase"] != action || len(wk.take()) != 0 {
			t.Errorf("%s stays in Go: %d %s", action, w.Code, w.Body.String())
		}
	}
	if w := call(f, "POST", "/api/v1/admin/packs/magento/configure", "", admin); w.Code != 400 || !strings.Contains(w.Body.String(), "must be installed") || len(wk.take()) != 0 {
		t.Errorf("empty body: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/admin/packs/magento/uninstall", "", admin); w.Code != 400 || !strings.Contains(w.Body.String(), "pack magento is not installed") || len(wk.take()) != 0 {
		t.Errorf("uninstall goes to Go: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct {
		path, body string
		code       int
		message    string
	}{
		{"/api/v1/admin/packs/magento/install", "{broken", 400, "request body must be valid JSON"},
		{"/api/v1/admin/packs/magento/install", "[1]", 400, "request body must be an object"},
		{"/api/v1/admin/packs/magento/install", "null", 400, "request body must be an object"},
		{"/api/v1/admin/packs/magento/install", `{"v":"` + strings.Repeat("x", 65<<10) + `"}`, 413, "request is too large"},
		{"/api/v1/admin/packs/magento/store-management", "{broken", 400, "request body must be valid JSON"},
		{"/api/v1/admin/packs/magento/store-management", `{"v":"` + strings.Repeat("x", 65<<10) + `"}`, 413, "request is too large"},
	} {
		r := httptest.NewRequest("POST", c.path, io.NopCloser(strings.NewReader(c.body)))
		r.ContentLength = -1
		for k, v := range admin {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		if w.Code != c.code || decode(t, w)["error"] != c.message {
			t.Errorf("%s %.20q: %d %s", c.path, c.body, w.Code, w.Body.String())
		}
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls for refused bodies: %d", len(calls))
	}
}

func TestMagentoStoreManagement(t *testing.T) {
	f, wk := packFront(t)
	admin := sessionCookie("admin")
	ctx := context.Background()
	db := f.API.DB
	f.API.Gate.WorkerURL = "http://127.0.0.1:1"

	seed(t, db, `insert into magento_store_control (org_id, domain, risk_class, enabled, auto_execute, caps, scope) values
		('org', 'catalog', 2, true, false, '{"maxRowsPerChangeset":50,"maxDailyAutoActions":10,"skuCooldownSeconds":3600}'::jsonb, '{"storeCode":"all"}'::jsonb),
		('org', 'promotions', 1, false, false, '{"maxRowsPerChangeset":10,"maxDiscountPercent":25}'::jsonb, '{}'::jsonb)`)
	seed(t, db, `insert into pack_action_definition (org_id, kind, definition, definition_hash, enabled, readiness, readiness_reason) values
		('org', 'magento.manage_catalog', '{"kind":"magento.manage_catalog","domain":"catalog","adapter":{"kind":"magento_changeset","operations":{"product_update":{"operationId":"magentoUpdateProduct","defaultClass":2,"reversible":true,"resultMode":"sync"}}}}'::jsonb, 'h1', true, 'ready', 'ready'),
		('org', 'magento.financial_handoff', '{"kind":"magento.financial_handoff","domain":"orders","adapter":{"kind":"magento_financial_handoff"}}'::jsonb, 'h2', true, 'ready', 'ready')`)
	var csID string
	if err := db.QueryRow(ctx, `insert into action_changeset (org_id, domain, scope, operation_id, risk_class, status, summary, idempotency_key, cap_snapshot)
		values ('org', 'catalog', '{"storeCode":"all"}'::jsonb, 'product_update', 2, 'applied', 'Update price', 'idem-key-1234', '{}'::jsonb) returning id::text`).Scan(&csID); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into action_changeset_row (changeset_id, row_index, entity_ref, operation_id, before_image, expected_current, after_image, status)
		values ($1, 0, 'SKU-1', 'product_update', '{"price":100}'::jsonb, '{"price":100}'::jsonb, '{"product":{"price":105}}'::jsonb, 'applied')`, csID)
	seed(t, db, `insert into magento_financial_handoff (org_id, kind, entity_ref, status, draft, evidence)
		values ('org', 'online_refund', '000000101', 'ready_for_human', '{"amount":50}'::jsonb, '{"reason":"damaged"}'::jsonb)`)

	w := call(f, "GET", "/api/v1/admin/packs/magento/store-management", "", admin)
	if w.Code != 200 {
		t.Fatalf("GET store-management: %d %s", w.Code, w.Body.String())
	}
	got := decode(t, w)
	controls := got["controls"].([]any)
	if len(controls) != 2 || controls[0].(map[string]any)["domain"] != "catalog" ||
		controls[0].(map[string]any)["readiness"] != "ready" ||
		controls[0].(map[string]any)["readinessMessage"] != "Changes can run under the configured store limits." ||
		controls[1].(map[string]any)["domain"] != "promotions" ||
		controls[1].(map[string]any)["readiness"] != "blocked" ||
		controls[1].(map[string]any)["readinessReason"] != "change_access_unavailable" {
		t.Fatalf("controls %v", controls)
	}
	ops := got["operations"].([]any)
	if len(ops) != 1 || ops[0].(map[string]any)["name"] != "product_update" ||
		ops[0].(map[string]any)["executionMode"] != "controlled_automation_eligible" {
		t.Fatalf("operations %v", ops)
	}
	activity := got["activity"].([]any)
	if len(activity) != 2 {
		t.Fatalf("activity %v", activity)
	}

	// Enable autoExecute and update cap on catalog.
	w = call(f, "POST", "/api/v1/admin/packs/magento/store-management",
		`{"action":"update_domain","domain":"catalog","autoExecute":true,"caps":{"maxRowsPerChangeset":25},"actorUserId":"forged"}`, admin)
	if w.Code != 200 {
		t.Fatalf("POST update_domain: %d %s", w.Code, w.Body.String())
	}
	var updatedBy string
	if err := db.QueryRow(ctx, `select updated_by_user_id from magento_store_control where org_id = 'org' and domain = 'catalog'`).Scan(&updatedBy); err != nil || updatedBy != "admin" {
		t.Errorf("updated_by_user_id = %q (%v)", updatedBy, err)
	}

	// Reject autoExecute on Class 1 domain.
	w = call(f, "POST", "/api/v1/admin/packs/magento/store-management",
		`{"action":"update_domain","domain":"promotions","autoExecute":true}`, admin)
	if w.Code != 400 || decode(t, w)["error"] != "Automatic execution is not available for this Magento domain" ||
		decode(t, w)["diagnostic"].(map[string]any)["phase"] != "store_management" {
		t.Errorf("promotions autoExecute: %d %s", w.Code, w.Body.String())
	}

	// Create automatic rule.
	w = call(f, "POST", "/api/v1/admin/packs/magento/store-management",
		`{"action":"create_rule","name":"Low stock","instruction":"Update copy","domain":"catalog","actionKind":"magento.manage_catalog","dailyCap":5,"cooldownSeconds":600,"enabled":true}`, admin)
	if w.Code != 200 {
		t.Fatalf("POST create_rule: %d %s", w.Code, w.Body.String())
	}
	rules := decode(t, w)["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["name"] != "Low stock" || rules[0].(map[string]any)["enabled"] != true {
		t.Fatalf("rules %v", rules)
	}
	ruleID := rules[0].(map[string]any)["id"].(string)

	// Exceed daily cap ceiling.
	w = call(f, "POST", "/api/v1/admin/packs/magento/store-management",
		`{"action":"create_rule","name":"Too high","instruction":"Update copy","domain":"catalog","actionKind":"magento.manage_catalog","dailyCap":15,"cooldownSeconds":600,"enabled":true}`, admin)
	if w.Code != 400 || decode(t, w)["error"] != "Rule daily cap exceeds the domain ceiling of 10" {
		t.Errorf("cap ceiling: %d %s", w.Code, w.Body.String())
	}

	// Suspend rule.
	w = call(f, "POST", "/api/v1/admin/packs/magento/store-management",
		`{"action":"set_rule_status","ruleId":"`+ruleID+`","enabled":false}`, admin)
	if w.Code != 200 {
		t.Fatalf("POST set_rule_status: %d %s", w.Code, w.Body.String())
	}
	rules = decode(t, w)["rules"].([]any)
	if rules[0].(map[string]any)["enabled"] != false || rules[0].(map[string]any)["suspendedReason"] != "suspended_by_admin" {
		t.Errorf("suspended rule %v", rules[0])
	}

	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker should not be called for store-management: %v", calls)
	}
}

func TestAdminPackUpload(t *testing.T) {
	f, wk := packFront(t)
	upload := func(contentType, body string) *httptest.ResponseRecorder {
		header := map[string]string{"Content-Type": contentType, "X-OpenNeko-Actor": "forged"}
		for k, v := range sessionCookie("admin") {
			header[k] = v
		}
		return call(f, "POST", "/api/v1/admin/packs/upload", body, header)
	}
	if w := upload("application/json", "ZIP"); w.Code != 415 || w.Body.String() != `{"error":"Choose a ZIP pack archive."}` {
		t.Errorf("wrong type: %d %s", w.Code, w.Body.String())
	}
	if w := upload("application/zip", strings.Repeat("x", 16<<20+1)); w.Code != 413 || w.Body.String() != `{"error":"request is too large"}` {
		t.Errorf("too large: %d %s", w.Code, w.Body.String())
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Fatalf("worker calls before a valid upload: %d", len(calls))
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if w := upload("application/zip; charset=binary", "ZIP"); w.Code != 400 || decode(t, w)["diagnostic"].(map[string]any)["phase"] != "upload" {
		t.Errorf("upload of a file that is not a ZIP: %d %s", w.Code, w.Body.String())
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls for an upload: %v", calls)
	}
}

func TestAdminCompatWebhooks(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")
	ctx := context.Background()
	var workflowID, otherID string
	if err := db.QueryRow(ctx, `insert into workflow_definition (org_id, name) values ('org', 'Daily lead union') returning id`).Scan(&workflowID); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into organization (id, name) values ('other', 'Other')`)
	if err := db.QueryRow(ctx, `insert into workflow_definition (org_id, name) values ('other', 'Theirs') returning id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	const token = "compat-token-0123456789abcdef"
	if w := call(f, "GET", "/api/v1/admin/reckon-webhooks", "", admin); w.Body.String() != `{"webhooks":[]}` {
		t.Errorf("empty list %s", w.Body.String())
	}
	body := func(fields string) string {
		return `{"reckonWorkflowId":" 01JIMPORT ","workflowId":"` + workflowID + `","token":"` + token + `"` + fields + `}`
	}
	for _, c := range []struct{ body, message string }{
		{`not json`, "reckonWorkflowId, workflowId and token are required."},
		{`{"reckonWorkflowId":"a","workflowId":"b"}`, "reckonWorkflowId, workflowId and token are required."},
		{`{"reckonWorkflowId":"a b","workflowId":"` + workflowID + `","token":"` + token + `"}`, "The Reckon workflow id must be 1 to 120 plain identifier characters."},
		{`{"reckonWorkflowId":"a","workflowId":"` + workflowID + `","token":"  short-token-15c  "}`, "The webhook token must be at least 16 characters."},
		{body(`,"batchChunkSize":1.5`), "The batch chunk size must be a whole number from 1 to 1000."},
		{body(`,"batchChunkSize":1001`), "The batch chunk size must be a whole number from 1 to 1000."},
		{body(`,"params":["date","bad-name"]`), "Each parameter name must be 1 to 64 letters, numbers or underscores."},
		{body(`,"params":[7]`), "Each parameter name must be 1 to 64 letters, numbers or underscores."},
		{`{"reckonWorkflowId":"a","workflowId":"nope","token":"` + token + `"}`, "Workflow not found."},
		{`{"reckonWorkflowId":"a","workflowId":"` + otherID + `","token":"` + token + `"}`, "Workflow not found."},
	} {
		if w := call(f, "POST", "/api/v1/admin/reckon-webhooks", c.body, admin); w.Code != 400 || decode(t, w)["error"] != c.message {
			t.Errorf("%.40s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	var count int
	if err := db.QueryRow(ctx, `select (select count(*) from compat_webhook) + (select count(*) from workflow_api_access)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows after refusals: %d %v", count, err)
	}

	w := call(f, "POST", "/api/v1/admin/reckon-webhooks", body(`,"params":["date"]`), admin)
	got := decode(t, w)
	hook, _ := got["webhook"].(map[string]any)
	if w.Code != 201 || hook["reckonWorkflowId"] != "01JIMPORT" || hook["workflowId"] != workflowID || hook["workflowName"] != "Daily lead union" ||
		hook["enabled"] != true || hook["batchChunkSize"] != 1000.0 || len(hook["params"].([]any)) != 1 || strings.Contains(w.Body.String(), token) {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	var digest string
	var params []string
	if err := db.QueryRow(ctx, `select token_sha256, params from compat_webhook where reckon_workflow_id = '01JIMPORT'`).Scan(&digest, &params); err != nil ||
		digest != sha256Hex(token) || len(params) != 1 || params[0] != "date" {
		t.Errorf("stored %q %v %v", digest, params, err)
	}
	var enabled bool
	var tokens, runtime, tools, requests, chunk int64
	if err := db.QueryRow(ctx, `select enabled, max_tokens_per_run, max_runtime_seconds, max_tool_calls, request_limit_per_minute, batch_chunk_size
		from workflow_api_access where workflow_id = $1`, workflowID).Scan(&enabled, &tokens, &runtime, &tools, &requests, &chunk); err != nil ||
		!enabled || tokens != 3000000 || runtime != 1800 || tools != 1024 || requests != 60 || chunk != 100 {
		t.Errorf("access %v %d %d %d %d %d %v", enabled, tokens, runtime, tools, requests, chunk, err)
	}
	var prefix string
	if err := db.QueryRow(ctx, `select token_prefix from workflow_api_access where workflow_id = $1`, workflowID).Scan(&prefix); err != nil {
		t.Fatal(err)
	}

	if w := call(f, "POST", "/api/v1/admin/reckon-webhooks", body(`,"batchChunkSize":250,"enabled":false`), admin); w.Code != 201 {
		t.Fatalf("reimport: %d %s", w.Code, w.Body.String())
	}
	list := decode(t, call(f, "GET", "/api/v1/admin/reckon-webhooks", "", admin))["webhooks"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["batchChunkSize"] != 250.0 || list[0].(map[string]any)["enabled"] != false || list[0].(map[string]any)["params"] != nil {
		t.Errorf("list after reimport %v", list)
	}
	var samePrefix string
	if err := db.QueryRow(ctx, `select token_prefix from workflow_api_access where workflow_id = $1`, workflowID).Scan(&samePrefix); err != nil || samePrefix != prefix {
		t.Errorf("reimport issued a new API token: %q %q %v", prefix, samePrefix, err)
	}

	if w := call(f, "DELETE", "/api/v1/admin/reckon-webhooks/unknown", "", admin); w.Code != 404 || w.Body.String() != `{"error":"Webhook not found."}` {
		t.Errorf("unknown delete: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/reckon-webhooks/01JIMPORT", "", admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/reckon-webhooks", "", admin); w.Body.String() != `{"webhooks":[]}` {
		t.Errorf("list after delete %s", w.Body.String())
	}
	var events []string
	rows, err := db.Query(ctx, `select event || ' ' || (payload->>'actorUserId') || ' ' || coalesce(payload->>'batchChunkSize', '-')
		from audit_chain where org_id = 'org' and entity_kind = 'compat_webhook' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		events = append(events, e)
	}
	rows.Close()
	if strings.Join(events, "|") != "imported admin 1000|imported admin 250|removed admin -" {
		t.Errorf("audit %v", events)
	}
}
