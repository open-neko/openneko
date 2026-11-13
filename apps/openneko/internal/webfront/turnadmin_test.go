package webfront

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	adminRun    = "b1000000-0000-4000-8000-0000000000a1"
	adminGroup  = "c1000000-0000-4000-8000-0000000000a1"
	adminSSO    = "c1000000-0000-4000-8000-0000000000a2"
	adminSource = "c1000000-0000-4000-8000-0000000000a3"
)

func riverSchema(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var schema string
	if err := db.QueryRow(context.Background(), `select current_schema() || '_river'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestTurnAdminDatabase(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seedRiver(t, db, f)
	worker := &fakeActionWorker{db: db}
	srv := httptest.NewServer(worker)
	defer srv.Close()
	a.Gate.WorkerURL = srv.URL
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'admin', 'admin')`, adminRun, threadMine)
	seed(t, db, `update app_user set disabled_at = '2026-11-02T03:04:05.678Z', last_login_at = '2026-11-01T00:00:00Z' where id = 'other'`)
	seed(t, db, `insert into sso_group (id, org_id, provider, tenant_id, external_id, display_name) values ($1, 'org', 'okta', 't1', 'g-fin', 'Finance IdP')`, adminSSO)
	seed(t, db, `insert into sso_group_membership (org_id, group_id, user_id) values ('org', $1, 'member')`, adminSSO)
	seed(t, db, `insert into user_group (id, org_id, slug, name) values ($1, 'org', 'finance', 'Finance')`, adminGroup)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source) values ('org', $1, 'member', 'local'), ('org', $1, 'member', 'rule:c1000000-0000-4000-8000-0000000000a9')`, adminGroup)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id) values ('org', $1, 'skill', '*')`, adminGroup)
	seed(t, db, `insert into data_access_rule (org_id, group_id, source, table_schema, table_name, columns, row_filter) values
		('org', $1, 'erp', 'sales', 'orders', '{id,total}', '{"column":"owner_id","op":"eq","value":{"var":"user_id"}}'),
		('org', $1, 'erp', '', 'regions', '{name}', null)`, adminGroup)
	seed(t, db, `insert into idp_group_rule (org_id, sso_group_id, user_group_id) values ('org', $1, $2)`, adminSSO, adminGroup)
	seed(t, db, `insert into channel_workspace (org_id, channel_plugin, workspace_id, created_at) values ('org', 'slack', 'T1', '2026-11-01T00:00:00Z')`)
	seed(t, db, `insert into channel_identity (org_id, channel_plugin, workspace_id, channel_user_id, app_user_id, display_name, status, first_seen_at)
		values ('org', 'slack', 'T1', 'U1', 'member', 'Mia', 'linked', '2026-11-01T00:00:00Z')`)
	seed(t, db, `insert into data_source (id, org_id, kind, name, label, graphql_url, mcp_url, auth_mode, is_default, enabled) values
		($1, 'org', 'graphjin', 'erp', 'ERP', 'https://svc:planted-pass@DB.Example.com:8443/api/v1/graphql', 'https://db.example.com/mcp', 'service', true, true),
		(gen_random_uuid(), 'org', 'graphjin', 'bare', null, 'not a url', '', 'service', false, false)`, adminSource)
	seed(t, db, `insert into organization (id, name) values ('other-org', 'Other')`)
	seed(t, db, `insert into behavior_alert (org_id, kind, subject, observed, threshold, window_seconds, created_at) values
		('org', 'action_burst', 'member', 12, 10, 300, '2026-11-12T00:00:00Z')`)
	seed(t, db, `insert into action_request (org_id, scope, kind, target, payload, status, summary, actor_user_id, actor_role, actor_backend, created_at) values
		('org', 'internal', 'user_admin', 'x@example.com', '{}', 'executed', 'Old', 'admin', 'admin', 'test', '2026-11-01T00:00:00Z'),
		('org', 'internal', 'group_admin', 'g', '{}', 'pending_approval', 'New', 'admin', 'admin', 'test', '2026-11-02T00:00:00Z'),
		('other-org', 'internal', 'group_admin', 'g', '{}', 'pending_approval', 'Hidden', null, null, null, '2026-11-03T00:00:00Z')`)

	r := a.runTools(turnBinding{OrgID: "org", RunID: adminRun})
	list := func(name string, call func() (any, error)) string {
		t.Helper()
		v, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return jsStringify(v, false)
	}
	org := orgIn{OrgID: "org"}

	users := list("users", func() (any, error) { return r.listUsers(ctx, org) })
	for _, want := range []string{
		`{"id":"admin","email":"a@example.com","name":"Ada","disabledAt":null,"lastLoginAt":null,"role":"admin","groupIds":[]}`,
		`{"id":"member","email":"m@example.com","name":null,"disabledAt":null,"lastLoginAt":null,"role":"member","groupIds":["` + adminSSO + `"]}`,
		`{"id":"other","email":"o@example.com","name":null,"disabledAt":"2026-11-02T03:04:05.678Z","lastLoginAt":"2026-11-01T00:00:00.000Z","role":"member","groupIds":[]}`,
		`"groups":[{"id":"` + adminSSO + `","provider":"okta","tenantId":"t1","externalId":"g-fin","displayName":"Finance IdP","active":true}]`,
	} {
		if !strings.Contains(users, want) {
			t.Errorf("users: no %s in %s", want, users)
		}
	}
	if !strings.HasPrefix(users, `{"users":[`) {
		t.Errorf("users key order: %s", users)
	}

	groups := list("groups", func() (any, error) { return r.listGroups(ctx, org) })
	for _, want := range []string{
		`{"id":"` + adminGroup + `","slug":"finance","name":"Finance","kind":"custom","memberCount":1,"members":[{"userId":"member","email":"m@example.com","sources":["local","rule:c1000000-0000-4000-8000-0000000000a9"]}],"grants":[{"itemType":"skill","itemId":"*"}],"dataAccess":[{"id":`,
		`"source":"erp","table":"regions","columns":["name"],"rowFilter":null}`,
		`"source":"erp","table":"sales.orders","columns":["id","total"],"rowFilter":{"op":"eq","value":{"var":"user_id"},"column":"owner_id"}}`,
		`"idpRules":[{"id":`,
		`"idpGroupName":"Finance IdP","ssoGroupId":"` + adminSSO + `","userGroupId":"` + adminGroup + `","userGroupName":"Finance"}],"groupDataAccessEnabled":false}`,
	} {
		if !strings.Contains(groups, want) {
			t.Errorf("groups: no %s in %s", want, groups)
		}
	}
	if !regexp.MustCompile(`"slug":"everyone","name":"[^"]+","kind":"[^"]+","memberCount":\d+,"members":\[\],`).MatchString(groups) {
		t.Errorf("the everyone group lists no members: %s", groups)
	}

	channels := list("channels", func() (any, error) { return r.listChannels(ctx, org) })
	if !strings.HasPrefix(channels, `{"workspaces":[{"channelPlugin":"slack","workspaceId":"T1","createdAt":"2026-11-01T00:00:00.000Z"}],"identities":[{"id":`) ||
		!strings.HasSuffix(channels, `"channelPlugin":"slack","workspaceId":"T1","channelUserId":"U1","displayName":"Mia","email":null,"status":"linked","appUserId":"member","firstSeenAt":"2026-11-01T00:00:00.000Z","verifiedAt":null}]}`) {
		t.Errorf("channels: %s", channels)
	}

	sources := list("sources", func() (any, error) { return r.listDataSources(ctx, org) })
	if strings.Contains(sources, "planted-pass") || strings.Contains(sources, "8443") || strings.Contains(sources, "graphql") {
		t.Errorf("sources leak the connection URL: %s", sources)
	}
	for _, want := range []string{
		`{"id":"` + adminSource + `","name":"erp","label":"ERP","kind":"graphjin","authMode":"service","isDefault":true,"enabled":true,"host":"db.example.com","hasMcp":true}`,
		`"name":"bare","label":null,"kind":"graphjin","authMode":"service","isDefault":false,"enabled":false,"host":null,"hasMcp":false}`,
	} {
		if !strings.Contains(sources, want) {
			t.Errorf("sources: no %s in %s", want, sources)
		}
	}

	r.audit(ctx, "/v1/users/list")
	trail := list("audit", func() (any, error) {
		limit := 1
		return r.listAuditTrail(ctx, auditTrailIn{OrgID: "org", RunID: &r.binding.RunID, Limit: &limit})
	})
	if !strings.HasPrefix(trail, `{"requests":[{"id":`) || !strings.Contains(trail, `"kind":"group_admin","target":"g","status":"pending_approval","scope":"internal","summary":"New","actorUserId":"admin","actorRole":"admin","actorBackend":"test","createdAt":"2026-11-02T00:00:00.000Z"}],"alerts":[{"id":`) ||
		!strings.Contains(trail, `"kind":"action_burst","subject":"member","observed":12,"threshold":10,"windowSeconds":300,"acknowledgedAt":null,"createdAt":"2026-11-12T00:00:00.000Z"}],"gatewaySummary":[{"runId":"`+adminRun+`","backend":"test","actorRole":"admin","calls":1}]}`) ||
		strings.Contains(trail, "Hidden") {
		t.Errorf("audit trail: %s", trail)
	}
	full := list("audit", func() (any, error) {
		limit := 0
		return r.listAuditTrail(ctx, auditTrailIn{OrgID: "org", RunID: &r.binding.RunID, Limit: &limit})
	})
	if strings.Count(full, `"summary":`) != 1 {
		t.Errorf("a limit under 1 reads 1 row: %s", full)
	}
	full = list("audit", func() (any, error) { return r.listAuditTrail(ctx, auditTrailIn{OrgID: "org", RunID: &r.binding.RunID}) })
	if strings.Count(full, `"summary":`) != 2 {
		t.Errorf("the default limit reads both rows: %s", full)
	}
	member := runMine
	for _, run := range []*string{nil, &member} {
		got := list("audit", func() (any, error) { return r.listAuditTrail(ctx, auditTrailIn{OrgID: "org", RunID: run}) })
		if got != `{"denied":true}` {
			t.Errorf("audit trail for run %v: %s", run, got)
		}
	}

	seed(t, db, `delete from action_policy where org_id = 'org'`)
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority, enabled) values
		('org', 'user_admin_ask', '{user_admin}', '{internal}', 'approval_required', 100, true),
		('org', 'channel_auto', '{channel_admin}', '{internal}', 'auto_approve', 100, true),
		('org', 'no_sources', '{data_source_admin}', '{internal}', 'never', 100, true)`)
	b := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: adminRun, Kind: "chat"}
	tools := a.runTools(b)
	var events []string
	tools.events = func(_ context.Context, event any) error {
		events = append(events, jsStringify(event, false))
		return nil
	}
	s, err := turnTools(b, tools, []string{"neko_user_manager", "neko_channel_manager", "neko_data_source_manager", "neko_audit"})
	if err != nil {
		t.Fatal(err)
	}
	var traced []string
	unregister := registerTraceSink(adminRun, func(event, _ *jsObj) { traced = append(traced, jsStringify(event, false)) }, nil)
	out, rpcErr := callTool(t, s, "user_manager_request_user_change", map[string]any{"action": "invite", "email": "new@example.com", "role": "member", "intent": "New hire"})
	if rpcErr != nil || !strings.Contains(out, `\"decision\":\"pending_approval\"`) {
		t.Fatalf("invite: %s %v", out, rpcErr)
	}
	create := worker.lastCreate()
	if create["kind"] != "user_admin" || create["scope"] != "internal" || create["status"] != "pending_approval" || create["riskLevel"] != "high" ||
		create["workRunId"] != adminRun || create["intent"] != "New hire" || jsStringify(jsFromGo(create["payload"]), false) != `{"action":"invite","email":"new@example.com","role":"member"}` {
		t.Errorf("invite request: %v", create)
	}
	out, rpcErr = callTool(t, s, "channel_manager_request_channel_change", map[string]any{"action": "block", "identityId": "ci-1", "intent": "Spam"})
	if rpcErr != nil || !strings.Contains(out, `\"decision\":\"approved\"`) {
		t.Fatalf("block: %s %v", out, rpcErr)
	}
	var queued int
	if err := db.QueryRow(ctx, `select count(*)::int from `+riverSchema(t, db)+`.river_job where kind = 'action_execute'`).Scan(&queued); err != nil || queued != 1 {
		t.Errorf("an approved change goes on the queue: %d %v", queued, err)
	}
	out, rpcErr = callTool(t, s, "data_source_manager_request_data_source_change", map[string]any{"action": "remove", "name": "erp", "intent": "Old"})
	if rpcErr != nil || !strings.Contains(out, `\"reason\":\"policy \\\"no_sources\\\" mode=never\"`) {
		t.Fatalf("denied: %s %v", out, rpcErr)
	}
	out, rpcErr = callTool(t, s, "audit_audit_trail", map[string]any{"limit": 5})
	unregister()
	if rpcErr != nil || !strings.HasPrefix(out, `{"content":[{"type":"text","text":"{\"ok\":true,\"requests\":[`) {
		t.Fatalf("audit tool: %s %v", out, rpcErr)
	}
	if len(events) != 2 || !strings.Contains(events[0], `"kind":"user_admin","scope":"internal","decision":"pending_approval"`) ||
		!strings.Contains(events[1], `"kind":"channel_admin","scope":"internal","decision":"auto_approved"`) {
		t.Errorf("events: %v", events)
	}
	if len(traced) != 3 {
		t.Errorf("one action.policy trace event per change: %v", traced)
	}
	rows, err := db.Query(ctx, `select path from control_plane_audit where run_id = $1 order by id`, adminRun)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if got := strings.Join(paths, ","); got != "/v1/users/list,/v1/policy/evaluate,/v1/action/request,/v1/events,/v1/policy/evaluate,/v1/action/request,/v1/action/enqueue,/v1/events,/v1/policy/evaluate,/v1/audit/list" {
		t.Errorf("audit paths: %s", got)
	}
}
