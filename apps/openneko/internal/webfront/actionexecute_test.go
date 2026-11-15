package webfront

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestActionExecuteRunsAllKindsInGo(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
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
	allowLoopbackWebhooks(t)

	a := &API{Gate: &Gate{}, DB: db}
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values
		('aaaaaaaa-0000-4000-8000-000000000001', 'org', 'external', 'send_webhook', 'approved', $1),
		('aaaaaaaa-0000-4000-8000-000000000002', 'org', 'external', 'shop.refund', 'approved', '{}')`,
		jsStringify(jsObjOf("url", hookSrv.URL+"/hook", "method", "POST", "headers", jsObjOf("X-Custom", "yes"), "body", jsObjOf("hello", "world")), false))

	if err := a.ActionExecute(t.Context(), "org", "aaaaaaaa-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
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

	// Unknown pack kind and eval-only kinds fail in Go in release mode.
	_ = a.ActionExecute(t.Context(), "org", "aaaaaaaa-0000-4000-8000-000000000002")
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
	home := t.TempDir()
	t.Setenv("HOME", home)

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
	patchFile := filepath.Join(orgAgentRoot("org"), "artifacts", "patches", "bbbbbbbb-fix-off-by-one.patch")
	if !strings.HasPrefix(patchFile, home) {
		t.Fatalf("patch folder %s is outside the org agent root", patchFile)
	}
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
		if err := db.QueryRow(ctx, `insert into action_request (org_id, scope, kind, status, actor_user_id, approved_by_user_id, payload)
			values ('org', 'org', $1, 'approved', 'member', 'admin', $2::jsonb) returning id::text`, kind, payload).Scan(&id); err != nil {
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

	var applied []string
	a.GroupGrantsScheduled = func(org string) { applied = append(applied, org) }
	a.GroupGrantsDelay = time.Hour
	t.Cleanup(a.stopGroupGrantsTimers)
	scheduled := func(want int, step string) {
		t.Helper()
		if len(applied) != want {
			t.Fatalf("%s: GraphJin applies scheduled %v, want %d", step, applied, want)
		}
		for _, org := range applied {
			if org != "org" {
				t.Fatalf("%s: GraphJin apply scheduled for org %q", step, org)
			}
		}
	}

	st, res := execAction("group_admin", `{"action":"create_group","name":"Finance Analysts","description":"Quarter close"}`)
	if st != "executed" {
		t.Fatalf("create_group failed: %s", res)
	}
	groupID := jsStr(jsGet(mustParse(t, []byte(res)), "groupId"))
	if groupID == "" || jsStr(jsGet(mustParse(t, []byte(res)), "slug")) != "finance-analysts" {
		t.Fatalf("create_group result: %s", res)
	}
	scheduled(0, "create_group")
	if st, res := execAction("group_admin", `{"action":"add_member","groupId":"`+groupID+`","userId":"member"}`); st != "executed" {
		t.Fatalf("add_member: %s", res)
	}
	if st, res := execAction("group_admin", `{"action":"grant_item","groupId":"`+groupID+`","itemType":"skill","itemId":"margin-audit"}`); st != "executed" || !strings.Contains(res, `"created": true`) {
		t.Fatalf("grant_item: %s", res)
	}
	scheduled(0, "grant skill")
	if st, res := execAction("group_admin", `{"action":"grant_item","groupId":"`+groupID+`","itemType":"data_source","itemId":"erp"}`); st != "executed" {
		t.Fatalf("grant data_source: %s", res)
	}
	scheduled(1, "grant data_source")
	if got := rowText(t, db, `select coalesce(created_by_user_id, '') || '|' || (action_request_id is not null)::text from item_grant where org_id = 'org' and item_type = 'data_source' and group_id = $1::uuid`, groupID); got != "member|true" {
		t.Fatalf("grant actor: %s", got)
	}
	if st, errText := execAction("group_admin", `{"action":"grant_item","groupId":"`+groupID+`","itemType":"nothing","itemId":"x"}`); st != "failed" || !strings.Contains(errText, "unknown item type") {
		t.Fatalf("unknown item type: %s %s", st, errText)
	}
	if st, errText := execAction("group_admin", `{"action":"set_table_access","groupId":"`+groupID+`","source":"erp","table":"orders","columns":["id"],"rowFilter":{"column":"id","op":"like","value":1}}`); st != "failed" || !strings.Contains(errText, "op must be one of") {
		t.Fatalf("bad row filter: %s %s", st, errText)
	}
	st, res = execAction("group_admin", `{"action":"set_table_access","groupId":"`+groupID+`","source":"erp","table":"sales.orders","columns":["id","total","id"],"rowFilter":{"column":"owner_id","op":"eq","value":{"var":"user_id"}}}`)
	if st != "executed" {
		t.Fatalf("set_table_access: %s", res)
	}
	ruleID := jsStr(jsGet(mustParse(t, []byte(res)), "ruleId"))
	if got := rowText(t, db, `select table_schema || '|' || table_name || '|' || array_to_string(columns, ',') || '|' || coalesce(created_by_user_id, '') from data_access_rule where id = $1::uuid`, ruleID); got != "sales|orders|id,total|member" {
		t.Fatalf("data access rule: %s", got)
	}
	scheduled(2, "set_table_access")
	if st, res := execAction("group_admin", `{"action":"remove_table_access","ruleId":"`+ruleID+`"}`); st != "executed" || !strings.Contains(res, `"removed": true`) {
		t.Fatalf("remove_table_access: %s", res)
	}
	scheduled(3, "remove_table_access")
	if st, res := execAction("group_admin", `{"action":"revoke_item","groupId":"`+groupID+`","itemType":"data_source","itemId":"erp"}`); st != "executed" || !strings.Contains(res, `"removed": true`) {
		t.Fatalf("revoke_item: %s", res)
	}
	scheduled(4, "revoke data_source")
	if st, res := execAction("group_admin", `{"action":"remove_member","groupId":"`+groupID+`","userId":"member"}`); st != "executed" || !strings.Contains(res, `"stillMember": false`) {
		t.Fatalf("remove_member: %s", res)
	}
	var ssoGroupID string
	if err := db.QueryRow(ctx, `insert into sso_group (org_id, provider, tenant_id, external_id, display_name)
		values ('org', 'scalekit', 'ten-1', 'ext-fin', 'IdP Finance') returning id::text`).Scan(&ssoGroupID); err != nil {
		t.Fatal(err)
	}
	st, res = execAction("group_admin", `{"action":"add_idp_rule","ssoGroupId":"`+ssoGroupID+`","groupId":"`+groupID+`"}`)
	if st != "executed" {
		t.Fatalf("add_idp_rule: %s", res)
	}
	idpRuleID := jsStr(jsGet(mustParse(t, []byte(res)), "id"))
	if st, res := execAction("group_admin", `{"action":"remove_idp_rule","ruleId":"`+idpRuleID+`"}`); st != "executed" {
		t.Fatalf("remove_idp_rule: %s", res)
	}
	if st, res := execAction("group_admin", `{"action":"delete_group","groupId":"`+groupID+`"}`); st != "executed" {
		t.Fatalf("delete_group: %s", res)
	}
	scheduled(5, "delete_group")
	if got := rowText(t, db, `select string_agg(slug, ',' order by slug) from user_group where org_id = 'org'`); got != "administrators,everyone" {
		t.Fatalf("groups after delete: %s", got)
	}
	if st, errText := execAction("group_admin", `{"action":"upsert_data_access_rule","groupId":"`+groupID+`"}`); st != "failed" || errText != `group_admin: unknown action "upsert_data_access_rule"` {
		t.Fatalf("unknown group action: %s %s", st, errText)
	}

	if st, errText := execAction("user_admin", `{"action":"deactivate","userId":"admin"}`); st != "failed" || !strings.Contains(errText, "administrator") {
		t.Fatalf("deactivate sole admin: %s %s", st, errText)
	}
	if st, res := execAction("user_admin", `{"action":"invite","email":"New@Example.com","role":"admin"}`); st != "executed" || !strings.Contains(res, `"role": "admin"`) {
		t.Fatalf("invite: %s", res)
	}
	if got := rowText(t, db, `select count(*)::text from user_group_membership m join user_group g on g.id = m.group_id join app_user u on u.id = m.user_id
		where u.email = 'new@example.com' and g.slug = 'administrators'`); got != "1" {
		t.Fatalf("invited admin membership: %s", got)
	}
	if st, res := execAction("user_admin", `{"action":"invite","email":"new@example.com","role":"member"}`); st != "executed" || !strings.Contains(res, `"alreadyExisted": true`) {
		t.Fatalf("second invite: %s", res)
	}
	if st, res := execAction("user_admin", `{"action":"set_role","userId":"member","role":"admin"}`); st != "executed" {
		t.Fatalf("promote member: %s", res)
	}
	if st, res := execAction("user_admin", `{"action":"set_role","userId":"member","role":"member"}`); st != "executed" {
		t.Fatalf("demote member: %s", res)
	}
	if st, errText := execAction("user_admin", `{"action":"set_role","userId":"nobody","role":"admin"}`); st != "failed" || errText != "user nobody not found" {
		t.Fatalf("set_role unknown user: %s %s", st, errText)
	}
	if st, res := execAction("user_admin", `{"action":"deactivate","userId":"member"}`); st != "executed" {
		t.Fatalf("deactivate member: %s", res)
	}
	if got := rowText(t, db, `select (disabled_at is not null)::text from app_user where id = 'member'`); got != "true" {
		t.Fatalf("member disabled: %s", got)
	}
	if st, res := execAction("user_admin", `{"action":"reactivate","userId":"member"}`); st != "executed" {
		t.Fatalf("reactivate member: %s", res)
	}
	if st, errText := execAction("user_admin", `{"action":"deactivate","userId":"nobody"}`); st != "failed" || errText != "user nobody not found" {
		t.Fatalf("deactivate unknown user: %s %s", st, errText)
	}

	var identityID string
	if err := db.QueryRow(ctx, `insert into channel_identity (org_id, channel_plugin, workspace_id, channel_user_id, display_name)
		values ('org', 'slack', 'T123', 'U123', 'Alice') returning id::text`).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	identity := func() string {
		return rowText(t, db, `select status || '|' || coalesce(app_user_id, '') || '|' || (verified_at is not null)::text from channel_identity where id = $1::uuid`, identityID)
	}
	if st, res := execAction("channel_admin", `{"action":"link","identityId":"`+identityID+`","appUserId":"member"}`); st != "executed" || identity() != "linked|member|true" {
		t.Fatalf("link: %s %s", res, identity())
	}
	if st, res := execAction("channel_admin", `{"action":"block","identityId":"`+identityID+`"}`); st != "executed" || identity() != "blocked|member|true" {
		t.Fatalf("block: %s %s", res, identity())
	}
	if st, res := execAction("channel_admin", `{"action":"unblock","identityId":"`+identityID+`"}`); st != "executed" || identity() != "unverified||false" {
		t.Fatalf("unblock: %s %s", res, identity())
	}
	seed(t, db, `update app_user set disabled_at = now() where id = 'member'`)
	if st, errText := execAction("channel_admin", `{"action":"link","identityId":"`+identityID+`","appUserId":"member"}`); st != "failed" || !strings.Contains(errText, "unknown or disabled") {
		t.Fatalf("link to a disabled user: %s %s", st, errText)
	}
	seed(t, db, `update app_user set disabled_at = null where id = 'member'`)
	if st, errText := execAction("channel_admin", `{"action":"unlink","identityId":"00000000-0000-4000-8000-000000000000"}`); st != "failed" || !strings.Contains(errText, "not found") {
		t.Fatalf("unlink unknown identity: %s %s", st, errText)
	}

	if st, res := execAction("data_source_admin", `{"action":"register","name":"weird","sourceKind":"blockchain"}`); st != "executed" {
		t.Fatalf("register weird: %s", res)
	}
	if got := rowText(t, db, `select kind || '|' || enabled::text || '|' || graphql_url from data_source where org_id = 'org' and name = 'weird'`); got != "graphjin|false|" {
		t.Fatalf("placeholder: %s", got)
	}
	if st, res := execAction("data_source_admin", `{"action":"register","name":"Warehouse","label":"Warehouse","sourceKind":"api"}`); st != "executed" {
		t.Fatalf("register: %s", res)
	}
	if st, res := execAction("data_source_admin", `{"action":"register","name":"Warehouse"}`); st != "executed" || !strings.Contains(res, `"alreadyExisted": true`) {
		t.Fatalf("register again: %s", res)
	}
	for _, step := range []string{"enable", "set_default"} {
		if st, res := execAction("data_source_admin", `{"action":"`+step+`","name":"Warehouse"}`); st != "executed" {
			t.Fatalf("%s: %s", step, res)
		}
	}
	if got := rowText(t, db, `select string_agg(name || ':' || enabled::text || ':' || is_default::text, ',' order by name) from data_source where org_id = 'org'`); got != "Warehouse:true:true,weird:false:false" {
		t.Fatalf("registry: %s", got)
	}
	if st, errText := execAction("data_source_admin", `{"action":"remove","name":"Warehouse"}`); st != "failed" || !strings.Contains(errText, "default") {
		t.Fatalf("remove default: %s %s", st, errText)
	}
	if st, res := execAction("data_source_admin", `{"action":"remove","name":"weird"}`); st != "executed" {
		t.Fatalf("remove: %s", res)
	}
	if st, errText := execAction("data_source_admin", `{"action":"disable","name":"missing"}`); st != "failed" || errText != "data source missing not found" {
		t.Fatalf("disable missing: %s %s", st, errText)
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

func allowLoopbackWebhooks(t *testing.T) {
	t.Helper()
	blocked := webhookAddressBlocked
	webhookAddressBlocked = func(ip net.IP) bool { return !ip.IsLoopback() && blocked(ip) }
	t.Cleanup(func() { webhookAddressBlocked = blocked })
}

func TestWebhookAdapterRefusesLocalAddressesAndRedirects(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	a := &API{DB: db}
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/hop" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	run := func(url string) string {
		t.Helper()
		var id string
		if err := db.QueryRow(t.Context(), `insert into action_request (org_id, scope, kind, status, payload)
			values ('org', 'external', 'send_webhook', 'approved', $1::jsonb) returning id::text`,
			jsStringify(jsObjOf("url", url), false)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		_ = a.ActionExecute(t.Context(), "org", id)
		return rowText(t, db, `select r.status || '|' || coalesce(e.error, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = $1`, id)
	}
	for _, url := range []string{target.URL + "/hook", "http://localhost:9/hook", "http://169.254.169.254/latest/meta-data/", "http://[::1]:9/", "http://0.0.0.0:9/"} {
		if got := run(url); !strings.HasPrefix(got, "failed|") || !strings.Contains(got, "OpenNeko does not send a webhook to the loopback") {
			t.Fatalf("%s: %s", url, got)
		}
	}
	if hits != 0 {
		t.Fatalf("a blocked webhook reached the server %d times", hits)
	}
	allowLoopbackWebhooks(t)
	if got := run(target.URL + "/hop"); got != "failed|webhook to "+target.URL+"/hop returned 302 Found: " {
		t.Fatalf("redirect: %s", got)
	}
	if hits != 1 {
		t.Fatalf("the redirect was followed: %d requests", hits)
	}
	if got := run(target.URL + "/hook"); got != "executed|" {
		t.Fatalf("allowed webhook: %s", got)
	}
}

func TestSourceConfigAdminRefusesWhenCapabilityIsOff(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	a := &API{DB: db}
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, enabled, config, secrets)
		values ('org', 'graphjin-config', 'graphjin-config', false, '{"sourceConfigEnabled": false}', '{}')`)
	var id string
	if err := db.QueryRow(t.Context(), `insert into action_request (org_id, scope, kind, status, approved_by_user_id, payload)
		values ('org', 'internal', 'source_config_admin', 'approved', 'admin',
		'{"action":"add_role","name":"analyst","match":"role = ''analyst''","preview":{"valid":true,"catalogRevision":"rev-1","patchHash":"x"}}') returning id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_ = a.ActionExecute(t.Context(), "org", id)
	if got := rowText(t, db, `select r.status || '|' || coalesce(e.error, '') from action_request r join action_execution e on e.action_request_id = r.id where r.id = $1`, id); got != "failed|source_config_admin: GraphJin config capability is disabled" {
		t.Fatalf("capability off: %s", got)
	}
}
