package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPolicyEngineMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/policyengine.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Decisions []struct {
			Name    string
			Subject struct {
				Scope, Kind       string
				Target, RiskLevel *string
				Targets           *[]string
			}
			Policies []json.RawMessage
			Decision json.RawMessage
		}
		Targets []struct {
			Name string
			Spec struct {
				Fields []string
				As     string
			}
			Payload json.RawMessage
			Targets []string
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Decisions) < 24 || len(fixture.Targets) < 7 {
		t.Fatalf("fixture holds %d decisions and %d target cases", len(fixture.Decisions), len(fixture.Targets))
	}
	for _, c := range fixture.Decisions {
		s := policySubject{scope: c.Subject.Scope, kind: c.Subject.Kind, target: c.Subject.Target, riskLevel: c.Subject.RiskLevel}
		if c.Subject.Targets != nil {
			s.targets, s.hasTargets = *c.Subject.Targets, true
		}
		policies := make([]*jsObj, len(c.Policies))
		for i, p := range c.Policies {
			v, err := jsParse(p)
			if err != nil {
				t.Fatal(err)
			}
			policies[i] = v.(*jsObj)
		}
		if got, want := jsStringify(evaluateActionPolicy(s, policies), false), compactJSON(t, c.Decision); got != want {
			t.Errorf("%s:\n got %s\nwant %s", c.Name, got, want)
		}
	}
	for _, c := range fixture.Targets {
		payload, err := jsParse(c.Payload)
		if err != nil {
			t.Fatal(err)
		}
		got := deriveActionTargets(policyTargetSpec{fields: c.Spec.Fields, emailDomain: c.Spec.As == "email_domain"}, payload)
		if !slices.Equal(got, c.Targets) {
			t.Errorf("%s: targets %q, want %q", c.Name, got, c.Targets)
		}
	}
}

type fakeActionWorker struct {
	mu        sync.Mutex
	db        *pgxpool.Pool
	creates   []map[string]any
	delivered []map[string]any
	status    int
	reply     string
}

func (w *fakeActionWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	w.mu.Lock()
	defer w.mu.Unlock()
	switch r.URL.Path {
	case "/admin/workflow-outputs/deliver":
		w.delivered = append(w.delivered, in)
		rw.WriteHeader(http.StatusAccepted)
	case "/admin/action-requests/create":
		w.creates = append(w.creates, in)
		if w.status != 0 {
			rw.WriteHeader(w.status)
			_, _ = rw.Write([]byte(w.reply))
			return
		}
		var id string
		err := w.db.QueryRow(r.Context(), `insert into action_request (org_id, scope, kind, target, payload, status, summary, intent, work_run_id, workflow_run_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid, $10::uuid) returning id::text`, in["orgId"], in["scope"], in["kind"], in["target"],
			mustJSON(in["payload"]), in["status"], in["summary"], in["intent"], in["workRunId"], in["workflowRunId"]).Scan(&id)
		if err != nil {
			rw.WriteHeader(500)
			_, _ = rw.Write([]byte(`{"error":` + jsStringify(err.Error(), false) + `}`))
			return
		}
		_, _ = rw.Write([]byte(`{"id":"` + id + `","status":"` + in["status"].(string) + `"}`))
	default:
		http.NotFound(rw, r)
	}
}

func (w *fakeActionWorker) lastCreate() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.creates[len(w.creates)-1]
}

func (w *fakeActionWorker) deliveries() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.delivered)
}

const (
	actionWorkflow = "c0000000-0000-4000-8000-000000000001"
	actionWfRun    = "c1000000-0000-4000-8000-000000000001"
)

func TestTurnActionsDatabase(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seedRiver(t, db, f)
	worker := &fakeActionWorker{db: db}
	srv := httptest.NewServer(worker)
	defer srv.Close()
	a.Gate.WorkerURL = srv.URL
	seed(t, db, `delete from action_policy where org_id = 'org'`)
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority, enabled, allowed_targets, denied_targets) values
		('org', 'external_default', '{}', '{external}', 'approval_required', 950, true, null, null),
		('org', 'slack_auto', '{slack_post}', '{}', 'auto_approve', 100, true, null, null),
		('org', 'off_rule', '{slack_post}', '{}', 'never', 1, false, null, null),
		('org', 'hooks', '{send_webhook}', '{}', 'auto_approve', 50, true, null, '{"patterns":["https://evil.example/*"]}'),
		('org', 'mail', '{shop.mail}', '{}', 'auto_approve', 60, true, '{"patterns":["acme.com"]}', null)`)
	seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash, readiness, enabled) values
		('org', 'shop.mail', 'Mail', '{"kind":"shop.mail","description":"Send a mail.","targets":{"fields":["to"],"as":"email_domain"},"inputSchema":{"type":"object","required":["to"]},"adapter":{"operations":{"send":{"readPath":"/m/{id}/{part}","bodyKey":"mail","reversible":false},"undo":{}}},"example":{"to":"a@acme.com"}}', 'h1', 'ready', true),
		('org', 'shop.blocked', 'Blocked', '{"kind":"shop.blocked","description":"Not ready."}', 'h2', 'blocked', true),
		('org', 'shop.off', 'Off', '{"kind":"shop.off","description":"Turned off."}', 'h3', 'ready', false),
		('org', 'shop.nodesc', 'No text', '{"kind":"shop.nodesc"}', 'h4', 'ready', true),
		('org', 'shop.refund', 'Refund', '{"kind":"shop.refund","description":"Refund an order.","example":[1]}', 'h5', 'ready', true)`)
	seed(t, db, `insert into workflow_definition (id, org_id, name) values ($1, 'org', 'Low stock')`, actionWorkflow)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind) values ($1, 'org', $2, $3, $4, 'manual')`,
		actionWfRun, actionWorkflow, threadMine, runMine)

	r := a.runTools(turnBinding{OrgID: "org", RunID: runMine})
	decide := func(scope, kind string, target *string, payload any) *jsObj {
		t.Helper()
		v, err := r.evaluatePolicy(ctx, policyIn{OrgID: "org", Scope: scope, Kind: kind, Target: target, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		return v.(*jsObj)
	}
	text := func(s string) *string { return &s }
	payloadOf := func(raw string) any {
		v, err := jsParse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	got := decide("external", "slack_post", text("C1"), nil)
	policy := got.vals["policy"].(*jsObj)
	if got.vals["decision"] != "allow" || policy.vals["name"] != "slack_auto" || policy.vals["orgId"] != "org" ||
		jsStringify(policy.vals["limits"], false) != "{}" || policy.vals["allowedTargets"] != nil || len(policy.ordered()) != 18 {
		t.Fatalf("slack: %s", jsStringify(got, false))
	}
	if got := decide("external", "crm_update", nil, nil); got.vals["decision"] != "needs_approval" || got.vals["reason"] != `policy "external_default" mode=approval_required` {
		t.Fatalf("default: %s", jsStringify(got, false))
	}
	if got := decide("internal", "crm_update", nil, nil); jsStringify(got, false) != `{"decision":"no_policy","reason":"no_matching_policy"}` {
		t.Fatalf("internal: %s", jsStringify(got, false))
	}
	if got := decide("external", "send_webhook", text("https://ok.example"), payloadOf(`{"url":" https://evil.example/x "}`)); got.vals["reason"] != `target "https://evil.example/x" is in policy "hooks" denied_targets` {
		t.Fatalf("webhook targets come from the payload: %s", jsStringify(got, false))
	}
	if got := decide("external", "shop.mail", text("acme.com"), payloadOf(`{"to":"Ann <ann@ACME.com>"}`)); got.vals["decision"] != "allow" {
		t.Fatalf("pack targets: %s", jsStringify(got, false))
	}
	if got := decide("external", "shop.mail", text("acme.com"), payloadOf(`{"to":"ann@acme.com, eve@other.org"}`)); got.vals["reason"] != `target "acme.com, other.org" is not in policy "mail" allowed_targets` {
		t.Fatalf("pack target miss: %s", jsStringify(got, false))
	}

	descriptors, err := a.packActionDescriptors(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(mustJSON(descriptors)); got != `[{"kind":"shop.mail","description":"Send a mail. Payload schema: {\"type\":\"object\",\"required\":[\"to\"]}. Each row uses {\"entity_ref\":\"...\",\"path\":{...},\"body\":{...}}. Supported operations: send (row.path keys: id, part; row.body key: mail; reversible: no); undo.","scope":"external","default_mode":"ask","example":{"to":"a@acme.com"}},{"kind":"shop.refund","description":"Refund an order.","scope":"external","default_mode":"ask"}]` {
		t.Fatalf("descriptors: %s", got)
	}

	b := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runMine, Kind: "workflow", WorkflowRunID: actionWfRun, PackActions: descriptors}
	tools := a.runTools(b)
	var events []string
	tools.events = func(_ context.Context, event any) error {
		events = append(events, jsStringify(event, false))
		return nil
	}
	s, err := turnTools(b, tools, []string{"neko_action", "neko_workflow_output", "neko_pack_actions"})
	if err != nil {
		t.Fatal(err)
	}
	var traced []string
	unregister := registerTraceSink(runMine, func(event, _ *jsObj) { traced = append(traced, jsStringify(event, false)) }, nil)
	out, rpcErr := callTool(t, s, "action_request", map[string]any{"scope": "external", "kind": "slack_post", "target": "C1",
		"payload": map[string]any{"text": "hi"}, "summary": "Post it", "minutes_saved": 300})
	unregister()
	if rpcErr != nil || !strings.Contains(out, `\"decision\":\"auto_approved\",\"status\":\"queued_for_execution\"`) {
		t.Fatalf("auto: %s %v", out, rpcErr)
	}
	if c := worker.lastCreate(); c["status"] != "approved" || c["workflowRunId"] != actionWfRun || c["requestedByRunId"] != actionWfRun ||
		c["minutesSaved"] != 120.0 || c["policyId"] == nil || c["triggeredByObservationId"] != nil {
		t.Fatalf("create body: %v", c)
	}
	if j := actionJobs(t, db); len(j) != 1 {
		t.Fatalf("jobs: %v", j)
	}
	if len(events) != 1 || !strings.Contains(events[0], `"decision":"auto_approved"`) || len(traced) != 1 ||
		!strings.Contains(traced[0], `"operation":"action.policy"`) || !strings.Contains(traced[0], `"decision":"allow"`) {
		t.Fatalf("events %v trace %v", events, traced)
	}
	var audits string
	if err := db.QueryRow(ctx, `select string_agg(path, ',' order by id) from control_plane_audit where run_id = $1`, runMine).Scan(&audits); err != nil ||
		audits != "/v1/policy/evaluate,/v1/action/request,/v1/events,/v1/action/enqueue" {
		t.Fatalf("audits: %s %v", audits, err)
	}

	out, _ = callTool(t, s, "action_request", map[string]any{"scope": "internal", "kind": "shop.refund", "summary": "Refund"})
	if !strings.Contains(out, `\"decision\":\"denied\"`) {
		t.Fatalf("no internal rule: %s", out)
	}
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority) values ('org', 'refunds', '{shop.refund}', '{}', 'approval_required', 70)`)
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type in ('action', 'pack')`)
	creates := len(worker.creates)
	out, _ = callTool(t, s, "action_request", map[string]any{"scope": "internal", "kind": "shop.refund", "summary": "Refund"})
	if out != `{"content":[{"type":"text","text":"action shop.refund is not available to this run"}],"isError":true}` || len(worker.creates) != creates {
		t.Fatalf("a member run without the action grant: %s", out)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id) select 'org', id, 'action', 'shop.refund' from user_group where org_id = 'org' and slug = 'everyone'`)
	out, _ = callTool(t, s, "action_request", map[string]any{"scope": "internal", "kind": "shop.refund", "summary": "Refund"})
	if !strings.Contains(out, `\"decision\":\"needs_approval\",\"status\":\"pending_approval\"`) || len(worker.creates) != creates+1 {
		t.Fatalf("granted: %s", out)
	}
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority) values ('org', 'gj', '{source_config_admin}', '{}', 'approval_required', 70)`)
	out, _ = callTool(t, s, "action_request", map[string]any{"scope": "internal", "kind": "source_config_admin", "summary": "Add a role"})
	if !strings.Contains(out, `"text":"source_config_admin: GraphJin config`) || len(worker.creates) != creates+1 {
		t.Fatalf("source config gate: %s", out)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id) select 'org', id, 'action', '*' from user_group where org_id = 'org' and slug = 'everyone'`)
	for _, c := range []struct {
		status      int
		reply, want string
	}{
		{500, `{"error":"action preflight failed: store offline"}`, "action preflight failed: store offline"},
		{502, ``, "worker action preflight failed with status 502"},
		{200, `{"id":"","status":"approved"}`, "worker action preflight returned an invalid response"},
	} {
		worker.status, worker.reply = c.status, c.reply
		out, _ = callTool(t, s, "action_request", map[string]any{"scope": "external", "kind": "slack_post", "summary": "Post it"})
		if out != `{"content":[{"type":"text","text":`+jsStringify(c.want, false)+`}],"isError":true}` {
			t.Errorf("worker %d: %s", c.status, out)
		}
	}
	worker.status = 0

	go func() {
		time.Sleep(400 * time.Millisecond)
		_, _ = db.Exec(ctx, `with r as (update action_request set status = 'executed' where kind = 'slack_post' and status = 'approved' and intent is not null returning id, org_id)
			insert into action_execution (org_id, action_request_id, executor, status, result, external_ref, command_or_operation)
			select org_id, id, 'test', 'succeeded', '{"ts":"1.2"}', 'C1/1.2', 'chat.postMessage' from r`)
	}()
	out, _ = callTool(t, s, "pack_actions_shop.refund", map[string]any{"intent": "Refund order 7", "payload": map[string]any{"order": 7}})
	if !strings.Contains(out, `\"decision\":\"pending_approval\"`) || worker.lastCreate()["intent"] != "Refund order 7" {
		t.Fatalf("pack ask: %s", out)
	}
	pack := b
	pack.PackActions = []packActionDescriptor{{Kind: "slack_post", Description: "Post.", DefaultMode: "auto"}}
	ps, err := turnTools(pack, tools, []string{"neko_pack_actions"})
	if err != nil {
		t.Fatal(err)
	}
	out, _ = callTool(t, ps, "pack_actions_slack_post", map[string]any{"target": "C1"})
	if !strings.Contains(out, `\"status\":\"executed\",\"outcome\":{\"result\":{\"ts\":\"1.2\"},\"externalRef\":\"C1/1.2\",\"commandOrOperation\":\"chat.postMessage\"}`) {
		t.Fatalf("pack auto runs and waits: %s", out)
	}

	request := func(status string, rejection *string) string {
		var id string
		if err := db.QueryRow(ctx, `insert into action_request (org_id, scope, kind, status, payload, rejection_reason) values ('org', 'external', 'x', $1, '{}', $2) returning id::text`,
			status, rejection).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	execution := func(id, status string, errText *string) {
		seed(t, db, `insert into action_execution (org_id, action_request_id, executor, status, error) values ('org', $1, 'test', $2, $3)`, id, status, errText)
	}
	partial, failed, rejected, bare, pending := request("executed", nil), request("failed", nil), request("rejected", text("Not now")), request("failed", nil), request("approved", nil)
	execution(partial, "succeeded", nil)
	time.Sleep(5 * time.Millisecond)
	execution(partial, "partially_applied", nil)
	execution(failed, "failed", text("channel_not_found"))
	for _, c := range []struct{ id, want string }{
		{partial, `{"status":"failed","error":"The action was partly applied. Review its details before any retry."}`},
		{failed, `{"status":"failed","error":"channel_not_found"}`},
		{rejected, `{"status":"failed","error":"Not now"}`},
		{bare, `{"status":"failed","error":"Action execution failed."}`},
		{unknownUUID, `{"status":"failed","error":"Action request disappeared before execution completed."}`},
		{pending, `{"status":"timeout"}`},
	} {
		started := time.Now()
		v, err := r.waitForAction(ctx, actionWaitIn{OrgID: "org", ActionRequestID: c.id, TimeoutMs: 10})
		if err != nil || jsStringify(v, false) != c.want {
			t.Errorf("wait %s: %s %v", c.id, jsStringify(v, false), err)
		}
		if c.id == pending && time.Since(started) < time.Second {
			t.Errorf("the wait lasts at least one second: %v", time.Since(started))
		}
	}

	emit := func(args map[string]any) *jsObj {
		t.Helper()
		out, rpcErr := callTool(t, s, "workflow_output_emit", args)
		if rpcErr != nil || !strings.Contains(out, `\"ok\":true`) {
			t.Fatalf("emit: %s %v", out, rpcErr)
		}
		var row *jsObj
		var id string
		_ = json.Unmarshal([]byte(jsGet(jsGet(payloadOf(out), "content").([]any)[0], "text").(string)), &struct {
			OutputID *string `json:"outputId"`
		}{&id})
		raw, err := r.api.DB.Query(ctx, `select `+outputColumns+` from workflow_output where id::text = $1`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if !raw.Next() {
			t.Fatalf("no output %s", id)
		}
		if row, err = scanOutputWire(raw); err != nil {
			t.Fatal(err)
		}
		return row
	}
	first := emit(map[string]any{"kind": "finding", "title": " Low Stock ", "timeWindowStart": "2026-11-01T00:00Z", "freshnessTtlSeconds": 60})
	again := emit(map[string]any{"kind": "finding", "title": "low stock"})
	other := emit(map[string]any{"kind": "summary", "title": "low stock", "payload": map[string]any{"a": 1}})
	untitled := emit(map[string]any{"kind": "finding"})
	if again.vals["id"] != first.vals["id"] || again.vals["seenCount"] != 2.0 || first.vals["timeWindowStart"] != "2026-11-01T00:00:00.000Z" ||
		first.vals["freshnessTtlSeconds"] != 60.0 || jsStringify(first.vals["payload"], false) != "{}" || first.vals["workRunId"] != runMine {
		t.Fatalf("dedupe: %s %s", jsStringify(first, false), jsStringify(again, false))
	}
	if other.vals["id"] == first.vals["id"] || jsStringify(other.vals["payload"], false) != `{"a":1}` || untitled.vals["id"] == first.vals["id"] {
		t.Fatalf("separate outputs: %s %s", jsStringify(other, false), jsStringify(untitled, false))
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(worker.deliveries()) < 4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	ids := map[any]bool{}
	for _, d := range worker.deliveries() {
		if d["orgId"] != "org" {
			t.Fatalf("delivery: %v", d)
		}
		ids[d["output"].(map[string]any)["id"]] = true
	}
	if len(worker.deliveries()) != 4 || !ids[first.vals["id"]] || !ids[untitled.vals["id"]] {
		t.Fatalf("deliveries: %v", worker.deliveries())
	}
}

func TestTurnActionServers(t *testing.T) {
	b := fixtureBinding
	if _, err := turnTools(b, &fakeTools{}, []string{"neko_action"}); err == nil || err.Error() != "mcp-bridge: neko_action requires workflowRunId" {
		t.Errorf("no workflow run: %v", err)
	}
	if _, err := turnTools(b, &fakeTools{}, []string{"neko_workflow_output"}); err == nil || err.Error() != "mcp-bridge: neko_workflow_output requires workflowRunId" {
		t.Errorf("no workflow run: %v", err)
	}
	b.PackActions = []packActionDescriptor{{Kind: "x", Description: "X", DefaultMode: "deny"}, {Kind: "y", Description: "Y", DefaultMode: map[string]any{"external": "deny"}}}
	if _, err := turnTools(b, &fakeTools{}, []string{"neko_pack_actions"}); err == nil || err.Error() != "mcp-bridge: no pack actions in this run" {
		t.Errorf("all denied: %v", err)
	}
	for in, want := range map[string]string{
		"2026-11-01T00:00Z":          "2026-11-01T00:00:00.000Z",
		"2026-11-01T00:00:07Z":       "2026-11-01T00:00:07.000Z",
		"2026-11-01T00:00:07.1Z":     "2026-11-01T00:00:07.100Z",
		"2026-11-01T00:00:07.98765Z": "2026-11-01T00:00:07.987Z",
	} {
		if got := jsISODate(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
