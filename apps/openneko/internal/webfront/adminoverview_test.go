package webfront

import (
	"encoding/json"
	"testing"
)

func TestAdminOverview(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	if w := call(f, "GET", "/api/v1/admin/overview", "", sessionCookie("member")); w.Code != 403 {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/overview", "", nil); w.Code == 200 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	read := func() adminOverview {
		t.Helper()
		w := call(f, "GET", "/api/v1/admin/overview", "", sessionCookie("admin"))
		var o adminOverview
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &o) != nil {
			t.Fatalf("admin: %d %s", w.Code, w.Body.String())
		}
		return o
	}
	if o := read(); o.SetupComplete || o.DataReady || o.PrimaryReady || o.ResearchOn || o.CustomWorkflowBudget ||
		o.EnabledSources != 0 || o.Policies.Total != 0 || o.Workflows.Total != 0 || o.PendingActions != 0 {
		t.Errorf("empty org: %+v", o)
	}
	seed(t, db, `update organization set setup_complete_at = now() where id = 'org'`)
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode) values
		('org', 'graphjin', 'main', 'http://127.0.0.1:1/api/v1/graphql', true, true, 'jwt'),
		('org', 'graphjin', 'side', 'http://127.0.0.1:2/api/v1/graphql', true, false, 'none'),
		('org', 'graphjin', 'off', 'http://127.0.0.1:3/api/v1/graphql', false, false, 'jwt')`)
	seed(t, db, `insert into action_policy (org_id, name, mode, enabled) values ('org', 'a', 'auto', true), ('org', 'b', 'auto', false)`)
	seed(t, db, `insert into workflow_definition (org_id, name, enabled) values ('org', 'a', true), ('org', 'b', false), ('org', 'c', false)`)
	seed(t, db, `insert into workflow_api_org_limits (org_id, rolling_token_budget, rolling_cost_micros_budget) values ('org', 5000, 5000)`)
	var users int
	if err := db.QueryRow(t.Context(), `select count(*)::int from app_user where org_id = 'org' and disabled_at is null`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	o := read()
	if !o.SetupComplete || !o.DataReady || !o.CustomWorkflowBudget || o.ActiveUsers != users ||
		o.EnabledSources != 2 || o.JwtSources != 1 || o.Policies != (countPair{2, 1}) || o.Workflows != (countPair{3, 1}) {
		t.Errorf("seeded org: %+v", o)
	}
}
