package webfront

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuleProposals(t *testing.T) {
	f, db := memoryFront(t)
	seedRiver(t, db, f)
	a := f.API
	const workflowWork = "b1000000-0000-4000-8000-0000000000f1"
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values
		($1, 'org', $3, 'test', 'admin', 'admin'), ($2, 'org', $3, 'test', 'admin', 'admin')`, builderRunAdmin, workflowWork, threadMine)
	seed(t, db, `with w as (insert into workflow_definition (org_id, name, owner_user_id) values ('org', 'Nightly', 'admin') returning id)
		insert into workflow_run (org_id, workflow_id, thread_id, work_run_id, trigger_kind) select 'org', id, $1, $2, 'cron' from w`, threadMine, workflowWork)
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority) values
		('org', 'Ask for Slack', '{slack.message}', '{external}', 'approval_required', 50),
		('org', 'Ask for email', '{email.send}', '{external}', 'approval_required', 60),
		('org', 'Everything else', '{}', '{}', 'approval_required', 90)`)
	tools := func(runID string) *toolServer {
		b := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runID, Kind: "work"}
		servers := []string{"neko_rule_builder"}
		if runID == workflowWork {
			b.WorkflowRunID = scalar(t, db, `select id::text from workflow_run where work_run_id = $1`, workflowWork)
			servers = append(servers, "neko_action")
		}
		s, err := turnTools(b, a.runTools(b), servers)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tool := func(runID, name string, args map[string]any) string {
		t.Helper()
		out, rpcErr := callTool(t, tools(runID), name, args)
		if rpcErr != nil {
			t.Fatalf("%s: %v", name, rpcErr)
		}
		return out
	}
	rule := map[string]any{"name": "Post to Slack at once", "applies_to_kinds": []any{"slack.message"}, "applies_to_scopes": []any{"external"},
		"mode": "auto_approve", "priority": 1}

	if out := tool(workflowWork, "rule_builder_save_rule", rule); !strings.Contains(out, "A workflow run cannot save an approval rule.") {
		t.Fatalf("a scheduled run of an admin's workflow saved a rule: %s", out)
	}
	if got := scalar(t, db, `select count(*)::text from action_policy_proposal`); got != "0" {
		t.Fatalf("a scheduled run left a proposal: %s", got)
	}

	tool(builderRunAdmin, "rule_builder_save_rule", rule)
	tool(builderRunAdmin, "rule_builder_save_rule", map[string]any{"name": "Ask for email", "applies_to_kinds": []any{"email.send"}, "mode": "never"})
	if got := scalar(t, db, `select count(*)::text from action_policy where name = 'Post to Slack at once' or mode = 'never'`); got != "0" {
		t.Fatalf("a proposal took effect: %s", got)
	}
	list := func(who map[string]string) (int, []policyProposal) {
		w := call(f, "GET", "/api/v1/policies/proposals", "", who)
		var body struct {
			Proposals []struct {
				policyProposal
				Rule json.RawMessage `json:"rule"`
			}
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		out := make([]policyProposal, len(body.Proposals))
		for i, p := range body.Proposals {
			out[i] = p.policyProposal
			out[i].Rule = storedJSON(p.Rule)
		}
		return w.Code, out
	}
	if code, _ := list(sessionCookie("member")); code != 403 {
		t.Fatalf("a member listed the proposals: %d", code)
	}
	code, proposals := list(sessionCookie("admin"))
	if code != 200 || len(proposals) != 2 {
		t.Fatalf("proposals: %d %+v", code, proposals)
	}
	byName := map[string]policyProposal{}
	for _, p := range proposals {
		byName[p.Name] = p
	}
	fast := byName["Post to Slack at once"]
	var overridden []string
	for _, o := range fast.Overrides {
		overridden = append(overridden, o.Name)
	}
	if fast.Replaces != nil || strings.Join(overridden, ",") != "Ask for Slack,Everything else" || deref(fast.ProposedBy) != "a@example.com" ||
		!strings.Contains(string(fast.Rule), `"mode":"auto_approve"`) || !strings.Contains(string(fast.Rule), `"priority":1`) {
		t.Errorf("the Slack proposal: %+v %s", fast, fast.Rule)
	}
	if email := byName["Ask for email"]; email.Replaces == nil || email.Replaces.Name != "Ask for email" || email.Replaces.Mode != "approval_required" {
		t.Errorf("the email proposal does not show the rule it replaces: %+v", email)
	}

	if w := call(f, "POST", "/api/v1/policies/proposals/"+byName["Ask for email"].ID+"/reject", "", sessionCookie("admin")); w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	if got := scalar(t, db, `select mode from action_policy where name = 'Ask for email'`); got != "approval_required" {
		t.Errorf("a rejected proposal changed the rule: %s", got)
	}
	if w := call(f, "POST", "/api/v1/policies/proposals/"+byName["Ask for email"].ID+"/confirm", "", sessionCookie("admin")); w.Code != 404 {
		t.Errorf("a rejected proposal was confirmed: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", "/api/v1/policies/proposals/"+fast.ID+"/confirm", "", sessionCookie("admin")); w.Code != 200 {
		t.Fatalf("confirm: %d %s", w.Code, w.Body)
	}
	if got := scalar(t, db, `select string_agg(event, ',' order by id) from audit_chain where entity_kind = 'action_policy_proposal'`); got !=
		"policy:proposed,policy:proposed,policy:rejected,policy:confirmed" {
		t.Errorf("audit: %s", got)
	}

	seed(t, db, `update action_policy set risk_threshold_auto_approve = 'low' where name = 'Post to Slack at once'`)
	request := func() string {
		t.Helper()
		return tool(workflowWork, "action_request", map[string]any{"scope": "external", "kind": "slack.message",
			"payload": map[string]any{"text": "hi"}, "summary": "Say hi", "risk_level": "low"})
	}
	if out := request(); strings.Contains(out, "auto_approved") || !strings.Contains(out, "pending_approval") {
		t.Fatalf("the model's own risk estimate passed an auto-approve threshold: %s", out)
	}
	decision := func() any {
		t.Helper()
		b := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: workflowWork, Kind: "work"}
		d, err := a.runTools(b).evaluatePolicy(t.Context(), policyIn{OrgID: "org", Scope: "external", Kind: "slack.message", Payload: &jsObj{}})
		if err != nil {
			t.Fatal(err)
		}
		v, _ := jsField(d, "decision")
		return v
	}
	if d := decision(); d != "needs_approval" {
		t.Fatalf("an action with no defined risk passed a threshold: %v", d)
	}
	seed(t, db, `insert into pack_action_definition (org_id, kind, definition, definition_hash, readiness)
		values ('org', 'slack.message', '{"kind":"slack.message","riskLevel":"high"}', 'h', 'ready')`)
	if d := decision(); d != "needs_approval" {
		t.Fatalf("a high-risk action passed a low threshold: %v", d)
	}
	seed(t, db, `update pack_action_definition set definition = '{"kind":"slack.message","riskLevel":"low"}' where kind = 'slack.message'`)
	if d := decision(); d != "allow" {
		t.Fatalf("an action whose definition is low risk: %v", d)
	}
}

func TestNoPolicyDecisions(t *testing.T) {
	f, db := workflowsFront(t)
	seedRiver(t, db, f)
	const mine, theirs = "30000000-0000-4000-8000-0000000000d1", "30000000-0000-4000-8000-0000000000d2"
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload, actor_user_id) values
		($1, 'org', 'internal', 'note', 'pending_approval', '{}', 'member'), ($2, 'org', 'internal', 'note', 'pending_approval', '{}', 'other')`, mine, theirs)
	decide := func(id string, who map[string]string) int {
		return call(f, "PATCH", "/api/v1/action-requests/"+id, `{"decision":"reject"}`, who).Code
	}
	if code := decide(theirs, sessionCookie("member")); code != 403 && code != 404 {
		t.Errorf("a member decided another user's request that no rule covers: %d", code)
	}
	if got := scalar(t, db, `select status from action_request where id = $1`, theirs); got != "pending_approval" {
		t.Errorf("the refused decision changed the row: %s", got)
	}
	if code := decide(mine, sessionCookie("member")); code != 200 {
		t.Errorf("the requester's own decision: %d", code)
	}
	if code := decide(theirs, sessionCookie("admin")); code != 200 {
		t.Errorf("an admin's decision: %d", code)
	}
}

func TestBuiltinActionsHaveARiskLevel(t *testing.T) {
	f, db := memoryFront(t)
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority, risk_threshold_auto_approve) values
		('org', 'Small changes at once', '{}', '{}', 'auto_approve', 1, 'medium')`)
	b := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runMine, Kind: "work"}
	for kind, want := range map[string]string{
		"code_draft_patch":  "allow",
		"code_create_issue": "allow",
		"send_webhook":      "needs_approval",
		"user_admin":        "needs_approval",
		"made_up_kind":      "needs_approval",
	} {
		d, err := f.API.runTools(b).evaluatePolicy(t.Context(), policyIn{OrgID: "org", Scope: "internal", Kind: kind, Payload: &jsObj{}})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := jsField(d, "decision"); got != want {
			t.Errorf("%s: %v, want %s (%s)", kind, got, want, jsStringify(d, false))
		}
	}
}
