package webfront

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	wfOrg      = "10000000-0000-4000-8000-000000000001"
	wfHeld     = "10000000-0000-4000-8000-000000000002"
	wfOwned    = "10000000-0000-4000-8000-000000000003"
	wfOther    = "10000000-0000-4000-8000-000000000004"
	runOrg     = "20000000-0000-4000-8000-000000000001"
	runHeld    = "20000000-0000-4000-8000-000000000002"
	runOwned   = "20000000-0000-4000-8000-000000000003"
	runOther   = "20000000-0000-4000-8000-000000000004"
	arRunless  = "30000000-0000-4000-8000-000000000001"
	arSource   = "30000000-0000-4000-8000-000000000002"
	arOrgRun   = "30000000-0000-4000-8000-000000000003"
	arHeldRun  = "30000000-0000-4000-8000-000000000004"
	arOwnedRun = "30000000-0000-4000-8000-000000000005"
	arOtherRun = "30000000-0000-4000-8000-000000000006"
	arExternal = "30000000-0000-4000-8000-000000000007"
	arPack     = "30000000-0000-4000-8000-000000000008"
	arFired    = "30000000-0000-4000-8000-000000000009"
	arUnsure   = "30000000-0000-4000-8000-00000000000a"
	arRetry    = "30000000-0000-4000-8000-00000000000b"
	policyID   = "40000000-0000-4000-8000-000000000001"
	outputID   = "50000000-0000-4000-8000-000000000001"
)

func seedDaily(t *testing.T, db *pgxpool.Pool) {
	seedOrg(t, db)
	seed(t, db, `insert into app_user (id, org_id, email) values ('other', 'org', 'o@example.com')`)
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type in ('workflow', 'action', 'pack')`)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'workflow', $1 from user_group where org_id = 'org' and slug = 'everyone'`, wfHeld)
	seed(t, db, `insert into workflow_definition (id, org_id, name, owner_user_id) values
		($1, 'org', 'Org flow', ''), ($2, 'org', 'Held flow', ''), ($3, 'org', 'Mine', 'member'), ($4, 'org', 'Theirs', 'other')`,
		wfOrg, wfHeld, wfOwned, wfOther)
	seed(t, db, `insert into work_thread (id, org_id) values ('60000000-0000-4000-8000-000000000001', 'org')`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend)
		select ('70000000-0000-4000-8000-00000000000' || n)::uuid, 'org', '60000000-0000-4000-8000-000000000001', 'test'
		from generate_series(1, 4) n`)
	for i, wf := range []string{wfOrg, wfHeld, wfOwned, wfOther} {
		run := []string{runOrg, runHeld, runOwned, runOther}[i]
		seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, started_at, created_at)
			values ($1, 'org', $2, '60000000-0000-4000-8000-000000000001', $3, 'manual',
			  '2026-11-01T08:00:00.123456Z', '2026-11-01T07:00:00Z')`, run, wf, "70000000-0000-4000-8000-00000000000"+string(rune('1'+i)))
	}
	seed(t, db, `insert into action_policy (id, org_id, name, mode) values ($1, 'org', 'Auto small', 'auto')`, policyID)
	seed(t, db, `insert into workflow_output (id, org_id, workflow_run_id, work_run_id, kind, title)
		values ($1, 'org', $2, '70000000-0000-4000-8000-000000000001', 'finding', 'Late order')`, outputID, runOrg)
	seed(t, db, `insert into pack_action_definition (org_id, kind, definition, definition_hash) values ('org', 'shop.refund', '{}', 'h')`)
	for _, a := range []struct{ id, run, scope, kind, risk, status, created string }{
		{arRunless, "", "admin.apps", "user_admin", "medium", "pending_approval", "2026-11-02T10:00:00Z"},
		{arSource, "", "admin.sources", "source_config_admin", "critical", "pending_approval", "2026-11-02T11:00:00Z"},
		{arOrgRun, runOrg, "internal", "note", "low", "pending_approval", "2026-11-02T12:00:00Z"},
		{arHeldRun, runHeld, "internal", "note", "high", "pending_approval", "2026-11-02T09:00:00Z"},
		{arOwnedRun, runOwned, "internal", "note", "", "pending_approval", "2026-11-02T13:00:00Z"},
		{arOtherRun, runOther, "internal", "note", "high", "pending_approval", "2026-11-02T08:00:00Z"},
		{arExternal, "", "external", "http.post", "high", "pending_approval", "2026-11-02T07:00:00Z"},
		{arPack, "", "internal", "shop.refund", "high", "pending_approval", "2026-11-02T06:00:00Z"},
		{arFired, runOrg, "internal", "note", "low", "executed", "2026-11-03T06:00:00Z"},
		{arUnsure, runOrg, "internal", "note", "low", "executed", "2026-11-03T07:00:00Z"},
		{arRetry, "", "admin.apps", "user_admin", "low", "failed", "2026-11-03T08:00:00Z"},
	} {
		seed(t, db, `insert into action_request (id, org_id, workflow_run_id, scope, kind, risk_level, status, payload, created_at, updated_at)
			values ($1, 'org', nullif($2, '')::uuid, $3, $4, nullif($5, ''), $6, '{"b":1,"a":[2]}', $7, $7)`,
			a.id, a.run, a.scope, a.kind, a.risk, a.status, a.created)
	}
	seed(t, db, `update action_request set policy_id = $2, approved_at = '2026-11-03T06:00:01.5Z',
		triggered_by_observation_id = null, minutes_saved = 12 where id = $1`, arFired, policyID)
	seed(t, db, `insert into action_execution (org_id, action_request_id, executor, status, result, created_at) values
		('org', $1, 'host', 'succeeded', '{"status":"ok"}', '2026-11-03T06:01:00Z'),
		('org', $2, 'host', 'succeeded', '{"status":"reconcile_required"}', '2026-11-03T07:01:00Z'),
		('org', $3, 'magento', 'failed', null, '2026-11-03T08:01:00Z')`, arFired, arUnsure, arRetry)
	seed(t, db, `update action_execution set error = 'connection refused', started_at = '2026-11-03T08:00:30Z' where action_request_id = $1`, arRetry)
	seed(t, db, `insert into action_changeset (id, org_id, action_request_id, domain, operation_id, risk_class, idempotency_key, status)
		values ('80000000-0000-4000-8000-000000000001', 'org', $1, 'catalog', 'op', 1, 'k', 'failed')`, arRetry)
	seed(t, db, `insert into action_changeset_row (changeset_id, row_index, entity_ref, operation_id, status)
		values ('80000000-0000-4000-8000-000000000001', 0, 'sku-1', 'op', 'failed')`)
	seed(t, db, `insert into observation (org_id, consumer_kind, consumer_workflow_id, consumer_user_id, title, status, created_at) values
		('org', 'workflow', $1, null, 'org flow', 'active', '2026-11-04T01:00:00Z'),
		('org', 'workflow', $2, null, 'held flow', 'active', '2026-11-04T02:00:00Z'),
		('org', 'workflow', $3, null, 'theirs', 'active', '2026-11-04T03:00:00Z'),
		('org', 'user', null, 'member', 'for member', 'resolved', '2026-11-04T04:00:00Z'),
		('org', 'user', null, 'other', 'for other', 'active', '2026-11-04T05:00:00Z'),
		('org', 'user', null, null, 'for all', 'active', '2026-11-04T06:00:00Z')`, wfOrg, wfHeld, wfOther)
}

func titles(t *testing.T, body []byte, list, key string) string {
	t.Helper()
	var v map[string]json.RawMessage
	var items []map[string]any
	if err := json.Unmarshal(body, &v); err != nil || json.Unmarshal(v[list], &items) != nil {
		t.Fatalf("%s: %v", body, err)
	}
	var out []string
	for _, item := range items {
		s, _ := item[key].(string)
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestObservations(t *testing.T) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	for _, c := range []struct {
		path string
		who  map[string]string
		want string
	}{
		{"/api/v1/observations", admin, "for all,theirs,held flow,org flow"},
		{"/api/v1/observations", member, "for all,for member,held flow"},
		{"/api/observations?status=resolved", member, "for member"},
		{"/api/v1/observations?limit=3", admin, "for all"},
		{"/api/v1/observations?limit=0", admin, "for all"},
		{"/api/v1/observations?limit=x", admin, "for all,theirs,held flow,org flow"},
		{"/api/v1/observations?consumerWorkflowId=" + wfHeld, admin, "held flow"},
		{"/api/v1/observations?consumerWorkflowId=nope", admin, ""},
	} {
		w := call(f, "GET", c.path, "", c.who)
		if got := titles(t, w.Body.Bytes(), "observations", "title"); w.Code != 200 || got != c.want {
			t.Errorf("%s: %d %s", c.path, w.Code, got)
		}
	}
	w := call(f, "GET", "/api/v1/observations?status=resolved", "", member)
	var v struct{ Observations []map[string]any }
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	o := v.Observations[0]
	if o["createdAt"] != "2026-11-04T04:00:00.000Z" || o["consumerWorkflowId"] != nil || o["consumerUserId"] != "member" || o["body"] != nil {
		t.Errorf("observation fields: %v", o)
	}
}

func TestApprovals(t *testing.T) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	admin, member := sessionCookie("admin"), sessionCookie("member")

	for _, c := range []struct {
		who  map[string]string
		want string
	}{{admin, `{"count":8}`}, {member, `{"count":3}`}} {
		if w := call(f, "GET", "/api/v1/approvals?countOnly=true", "", c.who); w.Body.String() != c.want {
			t.Errorf("count: %s", w.Body.String())
		}
	}

	for _, c := range []struct {
		path string
		who  map[string]string
		want string
	}{
		{"/api/v1/approvals", admin, strings.Join([]string{arSource, arHeldRun, arOtherRun, arExternal, arPack, arRunless, arOrgRun, arOwnedRun}, ",")},
		{"/api/approvals?filter=bogus", member, strings.Join([]string{arHeldRun, arRunless, arOwnedRun}, ",")},
		{"/api/v1/approvals?filter=fired", admin, arFired},
		{"/api/v1/approvals?filter=failed", admin, strings.Join([]string{arRetry, arUnsure}, ",")},
		{"/api/v1/approvals?filter=all", member, strings.Join([]string{arRetry, arOwnedRun, arRunless, arHeldRun}, ",")},
	} {
		w := call(f, "GET", c.path, "", c.who)
		if got := titles(t, w.Body.Bytes(), "actions", "id"); w.Code != 200 || got != c.want {
			t.Errorf("%s: %d\n got %s\nwant %s", c.path, w.Code, got, c.want)
		}
	}

	w := call(f, "GET", "/api/v1/approvals?filter=fired", "", admin)
	want := `{"actions":[{"id":"` + arFired + `","workflowRunId":"` + runOrg + `","workflow":{"id":"` + wfOrg + `","name":"Org flow"},` +
		`"triggeredByObservation":null,"kind":"note","target":null,"payload":{"a":[2],"b":1},"riskLevel":"low","summary":null,` +
		`"scope":"internal","status":"executed","minutesSaved":12,"approvedAt":"2026-11-03T06:00:01.500Z","approverKind":"policy",` +
		`"approverLabel":"Auto small","rejectionReason":null,"failureReason":null,"outcome":"completed","executionError":null,` +
		`"runAt":"2026-11-01T08:00:00.123Z","createdAt":"2026-11-03T06:00:00.000Z"}],"count":1,"filter":"fired"}`
	if w.Body.String() != want {
		t.Errorf("fired:\n got %s\nwant %s", w.Body.String(), want)
	}
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, manifest_hash)
		values ('90000000-0000-4000-8000-000000000001', 'org', 'shop', '1.0.0', 'h')`)
	seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash)
		values ('90000000-0000-4000-8000-000000000001', 'org', 'action', 'refund', 'shop.refund', 'h', 'h')`)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'pack', 'shop' from user_group where org_id = 'org' and slug = 'everyone'`)
	if w := call(f, "GET", "/api/v1/approvals?countOnly=true", "", member); w.Body.String() != `{"count":4}` {
		t.Errorf("count with the pack grant: %s", w.Body.String())
	}
	w = call(f, "GET", "/api/v1/approvals?filter=failed", "", admin)
	if !strings.Contains(w.Body.String(), `"outcome":"needs_check"`) || !strings.Contains(w.Body.String(), `"executionError":"connection refused"`) {
		t.Errorf("failed: %s", w.Body.String())
	}
}

func TestActionRequests(t *testing.T) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	admin, member := sessionCookie("admin"), sessionCookie("member")

	for _, c := range []struct {
		path string
		who  map[string]string
		want string
	}{
		{"/api/v1/action-requests?status=pending_approval&limit=3", admin, strings.Join([]string{arOwnedRun, arOrgRun, arSource}, ",")},
		{"/api/v1/action-requests?workflowRunId=" + runOrg, member, ""},
		{"/api/v1/action-requests?workflowRunId=" + runHeld, member, arHeldRun},
		{"/api/v1/action-requests?workflowRunId=bad", admin, ""},
		{"/api/action-requests?status=bogus", member, strings.Join([]string{arRetry, arOwnedRun, arRunless, arHeldRun}, ",")},
	} {
		w := call(f, "GET", c.path, "", c.who)
		if got := titles(t, w.Body.Bytes(), "actionRequests", "id"); w.Code != 200 || got != c.want {
			t.Errorf("%s: %d\n got %s\nwant %s", c.path, w.Code, got, c.want)
		}
	}

	for _, c := range []struct {
		id   string
		who  map[string]string
		code int
	}{
		{arSource, member, 404}, {arOrgRun, member, 404}, {arOtherRun, member, 404}, {arExternal, member, 404},
		{arPack, member, 404}, {"not-a-uuid", admin, 404}, {"30000000-0000-4000-8000-0000000000ff", admin, 404},
		{arHeldRun, member, 200}, {arSource, admin, 200},
	} {
		w := call(f, "GET", "/api/v1/action-requests/"+c.id, "", c.who)
		if w.Code != c.code || (c.code == 404 && w.Body.String() != `{"error":"not found"}`) {
			t.Errorf("%s: %d %s", c.id, w.Code, w.Body.String())
		}
	}

	seed(t, db, `update action_request set triggered_by_observation_id = null, approved_by_user_id = 'admin',
		approved_at = '2026-11-03T08:00:00Z' where id = $1`, arRetry)
	w := call(f, "GET", "/api/v1/action-requests/"+arRetry, "", member)
	want := `{"actionRequest":{"id":"` + arRetry + `","workflowRunId":null,"triggeredByObservationId":null,"policyId":null,` +
		`"scope":"admin.apps","kind":"user_admin","target":null,"payload":{"a":[2],"b":1},"riskLevel":"low","status":"failed",` +
		`"summary":null,"approvedByUserId":"admin","approvedAt":"2026-11-03T08:00:00.000Z","rejectionReason":null,"failureReason":null,` +
		`"createdAt":"2026-11-03T08:00:00.000Z","updatedAt":"2026-11-03T08:00:00.000Z"},"executions":[{"id":"`
	if !strings.HasPrefix(w.Body.String(), want) {
		t.Fatalf("detail:\n got %s\nwant %s", w.Body.String(), want)
	}
	tail := `"executor":"magento","commandOrOperation":null,"payload":null,"result":null,"externalRef":null,"status":"failed",` +
		`"error":"connection refused","startedAt":"2026-11-03T08:00:30.000Z","finishedAt":null,"createdAt":"2026-11-03T08:01:00.000Z"}],` +
		`"workflow":null,"policy":null,"upstreamOutput":null,"approverKind":"operator","canRetry":true}`
	if !strings.HasSuffix(w.Body.String(), tail) {
		t.Errorf("detail tail: %s", w.Body.String())
	}

	seed(t, db, `alter table action_request drop constraint if exists action_request_triggered_by_observation_id_fkey`)
	seed(t, db, `update action_request set triggered_by_observation_id = $2 where id = $1`, arFired, outputID)
	w = call(f, "GET", "/api/v1/action-requests/"+arFired, "", admin)
	for _, part := range []string{
		`"workflow":{"id":"` + wfOrg + `","name":"Org flow"}`,
		`"policy":{"id":"` + policyID + `","name":"Auto small","mode":"auto"}`,
		`"upstreamOutput":{"id":"` + outputID + `","title":"Late order","workflowRunId":"` + runOrg + `"}`,
		`"approverKind":"policy","canRetry":false}`,
	} {
		if !strings.Contains(w.Body.String(), part) {
			t.Errorf("detail lacks %s: %s", part, w.Body.String())
		}
	}

	if w := call(f, "PATCH", "/api/v1/action-requests/"+arHeldRun, `{"decision":"approve"}`, admin); w.Code != 418 {
		t.Errorf("PATCH stayed in Go: %d", w.Code)
	}
	if hits := f.Node.(*recorder).hits; len(hits) != 1 || hits[0] != "PATCH /api/v1/action-requests/"+arHeldRun {
		t.Errorf("node hits: %v", hits)
	}
}

func TestJSJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"b": 1.50, "a": [1e2, -0.0, 12345678901234567890], "10": true, "2": null, "01": "x<"}`: `{"2":null,"10":true,"b":1.5,"a":[100,0,12345678901234567000],"01":"x<"}`,
		`"s"`:              `"s"`,
		`{"a": 1, "a": 2}`: `{"a":2}`,
		`[]`:               `[]`,
	} {
		got, err := jsJSON([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("%s:\n got %q\nwant %q %v", in, got, want, err)
		}
	}
}
