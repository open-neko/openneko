package webfront

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestActionExecuteRunsAllKindsInGoWithoutWorker(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	var workerCalls int
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer worker.Close()

	var webhookBody, webhookHeader string
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		webhookBody = string(raw)
		webhookHeader = r.Header.Get("X-Custom")
		w.Header().Set("X-Request-Id", "req-99")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"received":true}`))
	}))
	defer hookSrv.Close()

	a := &API{Gate: &Gate{WorkerURL: worker.URL}, DB: db}
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values
		('aaaaaaaa-0000-4000-8000-000000000001', 'org', 'external', 'send_webhook', 'approved', $1),
		('aaaaaaaa-0000-4000-8000-000000000002', 'org', 'external', 'shop.refund', 'approved', '{}')`,
		jsStringify(jsObjOf("url", hookSrv.URL+"/hook", "method", "POST", "headers", jsObjOf("X-Custom", "yes"), "body", jsObjOf("hello", "world")), false))

	if err := a.ActionExecute(t.Context(), "org", "aaaaaaaa-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if workerCalls != 0 {
		t.Fatalf("worker received %d calls, expected 0", workerCalls)
	}
	if webhookBody != `{"hello":"world"}` || webhookHeader != "yes" {
		t.Fatalf("webhook got body=%q header=%q", webhookBody, webhookHeader)
	}
	if got := rowText(t, db, `select r.status || '|' || e.status || '|' || coalesce(e.external_ref, '') || '|' || coalesce(e.command_or_operation, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = 'aaaaaaaa-0000-4000-8000-000000000001'`); got != "executed|succeeded|req-99|POST "+hookSrv.URL+"/hook" {
		t.Fatalf("webhook outcome: %s", got)
	}

	// Unsupported method fails closed.
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values
		('aaaaaaaa-0000-4000-8000-000000000003', 'org', 'external', 'send_webhook', 'approved', $1)`,
		jsStringify(jsObjOf("url", hookSrv.URL+"/hook", "method", "TRACE"), false))
	_ = a.ActionExecute(t.Context(), "org", "aaaaaaaa-0000-4000-8000-000000000003")
	if got := rowText(t, db, `select r.status || '|' || coalesce(e.error, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = 'aaaaaaaa-0000-4000-8000-000000000003'`); !strings.Contains(got, `does not support method "TRACE"`) {
		t.Fatalf("unsupported webhook method: %s", got)
	}

	// Unknown pack kind and eval-only kinds fail in Go in release mode without calling the worker.
	_ = a.ActionExecute(t.Context(), "org", "aaaaaaaa-0000-4000-8000-000000000002")
	if workerCalls != 0 {
		t.Fatalf("a pack kind went to the worker: %d", workerCalls)
	}
	if got := rowText(t, db, `select r.status || '|' || e.status || '|' || coalesce(e.error, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = 'aaaaaaaa-0000-4000-8000-000000000002'`); got != `failed|failed|no adapter registered for kind "shop.refund"` {
		t.Fatalf("pack kind without a definition: %s", got)
	}
	for _, evalKind := range []string{"eval_send_notice", "openneko_eval_mutation", "openneko_eval_priority"} {
		var evalReqID string
		if err := db.QueryRow(t.Context(), `insert into action_request (org_id, scope, kind, status, payload) values ('org', 'external', $1, 'approved', '{}') returning id::text`, evalKind).Scan(&evalReqID); err != nil {
			t.Fatal(err)
		}
		_ = a.ActionExecute(t.Context(), "org", evalReqID)
		if got := rowText(t, db, `select r.status || '|' || coalesce(e.error, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = $1`, evalReqID); got != `failed|no adapter registered for kind "`+evalKind+`"` {
			t.Fatalf("eval kind %s should stay out of release mode: %s", evalKind, got)
		}
	}
}

func TestCodeActionAdaptersInGo(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	agentsRoot := t.TempDir()
	t.Setenv("OPENNEKO_AGENTS_ROOT", agentsRoot)

	var ghAuth, ghPath, ghBody string
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghAuth = r.Header.Get("Authorization")
		ghPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		ghBody = string(raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/acme/widgets/issues/42"}`))
	}))
	defer ghSrv.Close()
	t.Setenv("OPENNEKO_GITHUB_API_URL", ghSrv.URL)
	t.Setenv("OPENNEKO_GITHUB_TOKEN", "ghs_test_token")

	a := &API{DB: db}
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values
		('bbbbbbbb-0000-4000-8000-000000000001', 'org', 'external', 'code_create_issue', 'approved', $1),
		('bbbbbbbb-0000-4000-8000-000000000002', 'org', 'external', 'code_create_issue', 'approved', $2),
		('bbbbbbbb-0000-4000-8000-000000000003', 'org', 'external', 'code_draft_patch', 'approved', $3)`,
		jsStringify(jsObjOf("repo", "acme/widgets", "title", "Fix null pointer", "body", "Details", "labels", []any{"bug", "urgent"}), false),
		jsStringify(jsObjOf("repo", "not-a-repo-slug", "title", "Bad repo"), false),
		jsStringify(jsObjOf("title", "Fix off by one", "summary", "Clamp index", "patch", `--- a/x.go
+++ b/x.go
@@ -1 +1 @@
-1
+2`), false))

	if err := a.ActionExecute(t.Context(), "org", "bbbbbbbb-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if ghAuth != "Bearer ghs_test_token" || ghPath != "/repos/acme/widgets/issues" || !strings.Contains(ghBody, `"labels":["bug","urgent"]`) {
		t.Fatalf("github request: auth=%q path=%q body=%s", ghAuth, ghPath, ghBody)
	}
	if got := rowText(t, db, `select status || '|' || coalesce(external_ref, '') from action_execution where action_request_id = 'bbbbbbbb-0000-4000-8000-000000000001'`); got != "succeeded|https://github.com/acme/widgets/issues/42" {
		t.Fatalf("github execution: %s", got)
	}

	_ = a.ActionExecute(t.Context(), "org", "bbbbbbbb-0000-4000-8000-000000000002")
	if got := rowText(t, db, `select status || '|' || coalesce(error, '') from action_execution where action_request_id = 'bbbbbbbb-0000-4000-8000-000000000002'`); !strings.Contains(got, "owner/name") {
		t.Fatalf("invalid repo validation: %s", got)
	}

	if err := a.ActionExecute(t.Context(), "org", "bbbbbbbb-0000-4000-8000-000000000003"); err != nil {
		t.Fatal(err)
	}
	patchFile := filepath.Join(agentsRoot, "orgs", "org", "artifacts", "patches", "bbbbbbbb-fix-off-by-one.patch")
	written, err := os.ReadFile(patchFile)
	if err != nil {
		t.Fatalf("read patch artifact: %v", err)
	}
	if !strings.Contains(string(written), "# Title: Fix off by one") || !strings.Contains(string(written), "never applies patches") || !strings.Contains(string(written), "+++ b/x.go") {
		t.Fatalf("patch content: %s", string(written))
	}
}

func TestAdminActionAdaptersInGo(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := t.Context()

	execAction := func(kind, payload string) (string, string) {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, `insert into action_request (org_id, scope, kind, status, approved_by_user_id, payload)
			values ('org', 'org', $1, 'approved', 'admin', $2::jsonb) returning id::text`, kind, payload).Scan(&id); err != nil {
			t.Fatal(err)
		}
		_ = a.ActionExecute(ctx, "org", id)
		var status, result, errText string
		if err := db.QueryRow(ctx, `select r.status, coalesce(e.result::text, ''), coalesce(e.error, '')
			from action_request r left join action_execution e on e.action_request_id = r.id where r.id = $1`, id).
			Scan(&status, &result, &errText); err != nil {
			t.Fatal(err)
		}
		if status != "executed" {
			return status, errText
		}
		return status, result
	}

	// group_admin lifecycle: create_group, add_member, grant_item, upsert_data_access_rule, delete_data_access_rule, revoke_item, remove_member, delete_group
	st, res := execAction("group_admin", `{"action":"create_group","name":"Finance Analysts","description":"Quarter close"}`)
	if st != "executed" {
		t.Fatalf("create_group failed: %s", res)
	}
	groupObj, _ := jsGet(mustParse(t, []byte(res)), "group").(*jsObj)
	groupID := ""
	if groupObj != nil {
		groupID = jsStr(groupObj.vals["id"])
	}
	if groupID == "" {
		t.Fatalf("missing group id: %s", res)
	}
	if st, res := execAction("group_admin", `{"action":"add_member","groupId":"`+groupID+`","userId":"member"}`); st != "executed" {
		t.Fatalf("add_member: %s", res)
	}
	if st, res := execAction("group_admin", `{"action":"grant_item","groupId":"`+groupID+`","itemType":"skill","itemId":"margin-audit"}`); st != "executed" {
		t.Fatalf("grant_item: %s", res)
	}
	st, res = execAction("group_admin", `{"action":"upsert_data_access_rule","groupId":"`+groupID+`","source":"erp","tableSchema":"sales","tableName":"orders","columns":["id","total"],"rowFilter":{"column":"owner_id","op":"eq","value":"u-1"}}`)
	if st != "executed" {
		t.Fatalf("upsert_data_access_rule: %s", res)
	}
	ruleObj, _ := jsGet(mustParse(t, []byte(res)), "rule").(*jsObj)
	ruleID := ""
	if ruleObj != nil {
		ruleID = jsStr(ruleObj.vals["id"])
	}
	if st, res := execAction("group_admin", `{"action":"delete_data_access_rule","ruleId":"`+ruleID+`"}`); st != "executed" {
		t.Fatalf("delete_data_access_rule: %s", res)
	}
	if st, res := execAction("group_admin", `{"action":"revoke_item","groupId":"`+groupID+`","itemType":"skill","itemId":"margin-audit"}`); st != "executed" {
		t.Fatalf("revoke_item: %s", res)
	}
	if st, res := execAction("group_admin", `{"action":"remove_member","groupId":"`+groupID+`","userId":"member"}`); st != "executed" {
		t.Fatalf("remove_member: %s", res)
	}
	var ssoGroupID string
	if err := db.QueryRow(ctx, `insert into sso_group (org_id, provider, tenant_id, external_id, display_name)
		values ('org', 'scalekit', 'ten-1', 'ext-fin', 'IdP Finance') returning id::text`).Scan(&ssoGroupID); err != nil {
		t.Fatal(err)
	}
	st, res = execAction("group_admin", `{"action":"add_idp_rule","ssoGroupId":"`+ssoGroupID+`","userGroupId":"`+groupID+`"}`)
	if st != "executed" {
		t.Fatalf("add_idp_rule: %s", res)
	}
	idpRuleID, _ := jsGet(mustParse(t, []byte(res)), "id").(string)
	if st, res := execAction("group_admin", `{"action":"remove_idp_rule","ruleId":"`+idpRuleID+`"}`); st != "executed" {
		t.Fatalf("remove_idp_rule: %s", res)
	}
	if st, res := execAction("group_admin", `{"action":"delete_group","groupId":"`+groupID+`"}`); st != "executed" {
		t.Fatalf("delete_group: %s", res)
	}

	// user_admin: refuse to demote/disable last active admin, allow promoting member and disabling/enabling member
	if st, errText := execAction("user_admin", `{"action":"disable","userId":"admin"}`); st != "failed" || !strings.Contains(errText, "administrator") {
		t.Fatalf("disable sole admin: %s %s", st, errText)
	}
	if st, res := execAction("user_admin", `{"action":"set_role","userId":"member","role":"admin"}`); st != "executed" {
		t.Fatalf("promote member: %s", res)
	}
	if st, res := execAction("user_admin", `{"action":"set_role","userId":"member","role":"member"}`); st != "executed" {
		t.Fatalf("demote member: %s", res)
	}
	if st, res := execAction("user_admin", `{"action":"disable","userId":"member"}`); st != "executed" {
		t.Fatalf("disable member: %s", res)
	}
	if st, res := execAction("user_admin", `{"action":"enable","userId":"member"}`); st != "executed" {
		t.Fatalf("enable member: %s", res)
	}

	// channel_admin: upsert_workspace, link_identity, block_identity, reset_identity, remove_workspace
	var identityID string
	if err := db.QueryRow(ctx, `insert into channel_identity (org_id, channel_plugin, workspace_id, channel_user_id, display_name)
		values ('org', 'slack', 'T123', 'U123', 'Alice') returning id::text`).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	st, res = execAction("channel_admin", `{"action":"upsert_workspace","channelPlugin":"slack","workspaceId":"T123","workspaceName":"Acme"}`)
	if st != "executed" {
		t.Fatalf("upsert_workspace: %s", res)
	}
	wsID, _ := jsGet(mustParse(t, []byte(res)), "id").(string)
	seed(t, db, `insert into organization (id, name) values ('other-org', 'Other') on conflict do nothing`)
	seed(t, db, `insert into channel_workspace (org_id, channel_plugin, workspace_id) values ('other-org', 'slack', 'T-OTHER')`)
	if st, errText := execAction("channel_admin", `{"action":"upsert_workspace","channelPlugin":"slack","workspaceId":"T-OTHER"}`); st != "failed" || !strings.Contains(errText, "already bound to another org") {
		t.Fatalf("cross-org workspace should fail: %s %s", st, errText)
	}
	if st, res := execAction("channel_admin", `{"action":"link_identity","identityId":"`+identityID+`","userEmail":"m@example.com"}`); st != "executed" {
		t.Fatalf("link_identity: %s", res)
	}
	if st, res := execAction("channel_admin", `{"action":"block_identity","identityId":"`+identityID+`"}`); st != "executed" {
		t.Fatalf("block_identity: %s", res)
	}
	if st, res := execAction("channel_admin", `{"action":"reset_identity","identityId":"`+identityID+`"}`); st != "executed" {
		t.Fatalf("reset_identity: %s", res)
	}
	if st, res := execAction("channel_admin", `{"action":"remove_workspace","id":"`+wsID+`"}`); st != "executed" {
		t.Fatalf("remove_workspace: %s", res)
	}

	// data_source_admin: register_placeholder, refuse enable without URL, set_enabled after URL, set_default, remove
	st, res = execAction("data_source_admin", `{"action":"register_placeholder","name":"warehouse","kind":"graphjin","label":"Warehouse"}`)
	if st != "executed" {
		t.Fatalf("register_placeholder: %s", res)
	}
	dsID, _ := jsGet(mustParse(t, []byte(res)), "id").(string)
	if st, errText := execAction("data_source_admin", `{"action":"set_enabled","id":"`+dsID+`","enabled":true}`); st != "failed" || !strings.Contains(errText, "until its GraphQL URL is configured") {
		t.Fatalf("enable unconfigured source: %s %s", st, errText)
	}
	seed(t, db, `update data_source set graphql_url = 'http://warehouse.example/api/v1/graphql' where id = $1`, dsID)
	if st, res := execAction("data_source_admin", `{"action":"set_enabled","id":"`+dsID+`","enabled":true}`); st != "executed" {
		t.Fatalf("set_enabled: %s", res)
	}
	if st, res := execAction("data_source_admin", `{"action":"set_default","id":"`+dsID+`"}`); st != "executed" {
		t.Fatalf("set_default: %s", res)
	}
	if st, res := execAction("data_source_admin", `{"action":"remove","id":"`+dsID+`"}`); st != "executed" {
		t.Fatalf("remove: %s", res)
	}
}

func TestSourceConfigAdminAdapterInGo(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := t.Context()
	t.Setenv("OPENNEKO_GRAPHJIN_URL", "http://graphjin.internal:8089")

	var mu sync.Mutex
	revision := "rev-10"
	sourcesJSON := `[{"name":"erp","kind":"database","type":"postgres","read_only":true},{"name":"orders_api","kind":"api","type":"openapi","read_only":true}]`
	applyErr := ""
	specFile, _ := managedOpenAPISpecFilename("org", "orders_api")
	opPrefix := strings.TrimSuffix(specFile, ".yaml") + "_"
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		mu.Lock()
		rev := revision
		srcs := sourcesJSON
		aErr := applyErr
		mu.Unlock()
		switch {
		case strings.Contains(body, "gj_catalog"):
			_, _ = w.Write([]byte(`{"data":{"gj_catalog":[{"id":"` + opPrefix + `listOrders","name":"` + opPrefix + `listOrders","summary":"List orders"}]}}`))
		case strings.Contains(body, `mode: \"preview\"`):
			_, _ = w.Write([]byte(`{"data":{"gj_config":{"valid":true,"preview_id":"prev-1","change_summary_json":"{\"added\":\"item\"}","errors_json":null}}}`))
		case strings.Contains(body, `mode: \"apply\"`):
			if aErr != "" {
				_, _ = w.Write([]byte(`{"errors":[{"message":` + jsStringify(aErr, false) + `}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"gj_config":{"applied":true,"catalog_revision":"rev-11","errors_json":null}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"gj_config":{"catalog_revision":` + jsStringify(rev, false) + `,"sources":` + srcs + `}}}`))
		}
	}))
	defer gj.Close()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "graphjin.yml")
	if err := os.WriteFile(configFile, []byte(`sources:
  - name: erp
    kind: database
    type: postgres
    read_only: true
  - name: orders_api
    kind: api
    type: openapi
    read_only: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", configFile)

	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, auth_mode, is_default, enabled) values
		($1, 'org', 'graphjin', 'erp', $2, 'service', true, true)`, sourceMain, gj.URL+"/api/v1/graphql")

	execSourceConfig := func(payloadObj *jsObj) (string, string) {
		t.Helper()
		credFree := &jsObj{vals: map[string]any{}}
		for _, k := range payloadObj.ordered() {
			if k != "preview" && k != "specSummary" {
				credFree.set(k, payloadObj.vals[k])
			}
		}
		hash := graphjinConfigPatchHash(credFree)
		payloadObj.set("preview", jsObjOf("valid", true, "catalogRevision", "rev-10", "patchHash", hash))
		var id string
		if err := db.QueryRow(ctx, `insert into action_request (org_id, scope, kind, status, approved_by_user_id, payload)
			values ('org', 'org', 'source_config_admin', 'approved', 'admin', $1::jsonb) returning id::text`,
			jsStringify(payloadObj, false)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		_ = a.ActionExecute(ctx, "org", id)
		var status, result, errText string
		if err := db.QueryRow(ctx, `select r.status, coalesce(e.result::text, ''), coalesce(e.error, '')
			from action_request r left join action_execution e on e.action_request_id = r.id where r.id = $1`, id).
			Scan(&status, &result, &errText); err != nil {
			t.Fatal(err)
		}
		if status != "executed" {
			return status, errText
		}
		return status, result
	}

	// 1. Live preview+apply (add_role) and YAML persistence
	st, res := execSourceConfig(jsObjOf("action", "add_role", "name", "analyst", "match", "role = 'analyst'", "comment", "Analysts"))
	if st != "executed" {
		t.Fatalf("add_role failed: %s", res)
	}
	diskYAML, _ := os.ReadFile(configFile)
	if !strings.Contains(string(diskYAML), "name: analyst") {
		t.Fatalf("role was not persisted to disk: %s", string(diskYAML))
	}

	// Tampered preview patchHash and stale catalogRevision fail closed.
	var badID string
	if err := db.QueryRow(ctx, `insert into action_request (org_id, scope, kind, status, approved_by_user_id, payload)
		values ('org', 'org', 'source_config_admin', 'approved', 'admin',
		'{"action":"add_role","name":"tampered","match":"role = ''tampered''","preview":{"valid":true,"catalogRevision":"rev-10","patchHash":"deadbeef"}}'::jsonb) returning id::text`).Scan(&badID); err != nil {
		t.Fatal(err)
	}
	_ = a.ActionExecute(ctx, "org", badID)
	if got := rowText(t, db, `select r.status || '|' || coalesce(e.error, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = $1`, badID); !strings.Contains(got, "no longer matches its preview") {
		t.Fatalf("tampered patchHash should fail: %s", got)
	}

	// 2. Governed writes go only to OpenAPI sources and fail closed on database sources
	if st, errText := execSourceConfig(jsObjOf("action", "enable_api_writes", "source", "erp", "spec", "orders.yaml", "operation", "createOrder", "allowedRoles", []any{"analyst"})); st != "failed" || !strings.Contains(errText, "read-only") {
		t.Fatalf("enable_api_writes on database source should fail closed: %s %s", st, errText)
	}
	if st, res := execSourceConfig(jsObjOf("action", "enable_api_writes", "source", "orders_api", "spec", "orders.yaml", "operation", "createOrder", "allowedRoles", []any{"analyst"})); st != "executed" {
		t.Fatalf("enable_api_writes on OpenAPI source failed: %s", res)
	}

	// 3. Password scrubbing on apply failure & never leaking password in result
	secretVal := "s3cr3t-db-p@ss-99"
	enc, err := config.EncryptValue(a.SecretsDir, secretVal)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into data_source_secret (org_id, name, value_enc) values ('org', 'crm-pass', $1)`, enc)
	mu.Lock()
	applyErr = "apply failed with password " + secretVal
	mu.Unlock()
	st, errText := execSourceConfig(jsObjOf("action", "register_source", "name", "crm", "kind", "database", "type", "postgres", "host", "crm.db", "port", float64(5432), "dbname", "crm", "user", "reader", "secretRef", "crm-pass"))
	if st != "failed" || strings.Contains(errText, secretVal) || !strings.Contains(errText, "[REDACTED]") {
		t.Fatalf("password was not scrubbed from failure: %s %s", st, errText)
	}
	mu.Lock()
	applyErr = ""
	mu.Unlock()
	st, res = execSourceConfig(jsObjOf("action", "register_source", "name", "crm", "kind", "database", "type", "postgres", "host", "crm.db", "port", float64(5432), "dbname", "crm", "user", "reader", "secretRef", "crm-pass"))
	if st != "executed" || strings.Contains(res, secretVal) {
		t.Fatalf("register_source database failed or leaked password: %s %s", st, res)
	}

	// 4. Managed OpenAPI source registration stages spec file and activates asset
	specContent := `openapi: 3.0.3
info:
  title: Billing API
  version: 1.0.0
servers:
  - url: https://api.example.com
paths:
  /invoices:
    get:
      operationId: listInvoices
      responses:
        '200':
          description: ok
`
	sum := sha256.Sum256([]byte(specContent))
	var specAssetID string
	if err := db.QueryRow(ctx, `insert into openapi_spec_asset (org_id, source_type, original_name, content, checksum_sha256, title, base_url, operation_count)
		values ('org', 'upload', 'billing.yaml', $1, $2, 'Billing API', 'https://api.example.com', 1) returning id::text`,
		specContent, hex.EncodeToString(sum[:])).Scan(&specAssetID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sourcesJSON = `[{"name":"erp","kind":"database","type":"postgres","read_only":true}]`
	mu.Unlock()
	st, res = execSourceConfig(jsObjOf("action", "register_source", "name", "orders_api", "kind", "api", "specAssetId", specAssetID))
	if st != "executed" {
		t.Fatalf("register_source api failed: %s", res)
	}
	stagedSpec, err := os.ReadFile(filepath.Join(dir, "specs", specFile))
	if err != nil || string(stagedSpec) != specContent {
		t.Fatalf("staged OpenAPI spec mismatch: %v %s", err, string(stagedSpec))
	}
}
