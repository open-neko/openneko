package webfront

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	builderRunAdmin   = "b1000000-0000-4000-8000-000000000002"
	builderRunService = "b1000000-0000-4000-8000-000000000003"
	builderThreadBot  = "b0000000-0000-4000-8000-000000000003"
)

func TestTurnBuildersDatabase(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values
		($1, 'org', $3, 'test', 'admin', 'admin'), ($2, 'org', $3, 'test', null, 'service')`, builderRunAdmin, builderRunService, threadMine)
	seed(t, db, `insert into work_thread (id, org_id, created_by_user_id) values ($1, 'org', 'member')`, builderThreadBot)
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type = 'workflow'`)
	seed(t, db, `insert into bot (org_id, owner_user_id, name, coat, pattern, thread_id) values ('org', 'member', 'Neko', 'ink', 'solid', $1)`, builderThreadBot)

	var events []string
	server := func(threadID, runID string, servers ...string) *toolServer {
		t.Helper()
		b := turnBinding{OrgID: "org", ThreadID: threadID, RunID: runID, Kind: "work"}
		tools := a.runTools(b)
		tools.events = func(_ context.Context, event any) error {
			events = append(events, jsStringify(event, false))
			return nil
		}
		s, err := turnTools(b, tools, servers)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	call := func(s *toolServer, tool string, args map[string]any) string {
		t.Helper()
		out, rpcErr := callTool(t, s, tool, args)
		if rpcErr != nil {
			t.Fatalf("%s: %v", tool, rpcErr)
		}
		return out
	}
	member := server(threadMine, runMine, "neko_workflow_builder", "neko_rule_builder")
	admin := server(threadMine, builderRunAdmin, "neko_workflow_builder", "neko_rule_builder")
	steps := []any{map[string]any{"id": "s1", "description": "Update the products table"}}
	when := map[string]any{"table": "products", "primary_key": []any{"id"}, "where": map[string]any{"qty": map[string]any{"lt": 5}}}
	batch := map[string]any{"columns": []any{map[string]any{"name": "Order", "path": "order.id"}}}

	out := call(member, "workflow_builder_create_workflow", map[string]any{"name": " Stock check ", "steps": steps, "batch": batch,
		"triggers": map[string]any{"cron": "0 9 * * *", "when": when}})
	if !strings.Contains(out, `\"ok\":false,\"action\":\"created\"`) || !strings.Contains(out, `\"code\":\"no_data_source\"`) ||
		!strings.Contains(out, `"isError":true`) {
		t.Fatalf("no data source: %s", out)
	}
	var wfID, origin, contract, tz string
	var createdRun *string
	if err := db.QueryRow(ctx, `select id::text, origin_id::text, output_contract::text, cron_timezone, created_by_run_id::text
		from workflow_definition where org_id = 'org' and name = 'Stock check' and owner_user_id = ''`).
		Scan(&wfID, &origin, &contract, &tz, &createdRun); err != nil {
		t.Fatal(err)
	}
	if origin != wfID || tz != "UTC" || deref(createdRun) != runMine ||
		contract != `{"apiBatch": {"columns": [{"name": "Order", "path": "order.id"}], "version": 1, "compiled": true, "compiler": "workflow", "recordsField": "records"}}` {
		t.Fatalf("saved row: %s %s %s %v", origin, tz, contract, createdRun)
	}
	if got := scalar(t, db, `select scope || ' ' || summary || ' ' || coalesce(commit_sha, 'no-commit') from config_change where artifact_ref = 'Stock check'`); got != "team Added workflow: Stock check no-commit" {
		t.Fatalf("team change row: %s", got)
	}
	if len(events) != 1 || !strings.Contains(events[0], `"label":"Created workflow"`) {
		t.Fatalf("events: %v", events)
	}

	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url) values ('org', 'graphjin', 'erp', 'http://gj.invalid/api/v1/graphql')`)
	if out := call(member, "workflow_builder_create_workflow", map[string]any{"name": "Stock check", "steps": steps}); !strings.Contains(out,
		`A workflow named \"Stock check\" exists and this run cannot change it. Choose another name.`) {
		t.Fatalf("member without the grant: %s", out)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'workflow', $1 from user_group where org_id = 'org' and slug = 'everyone'`, wfID)
	if out := call(member, "workflow_builder_create_workflow", map[string]any{"name": "Stock check", "steps": steps,
		"triggers": map[string]any{"when": when}}); !strings.Contains(out, `\"code\":\"mutation_loop\"`) ||
		!strings.Contains(out, `the mutation verb \\\"update\\\"`) {
		t.Fatalf("mutation loop: %s", out)
	}
	if contract := scalar(t, db, `select coalesce(output_contract::text, 'null') from workflow_definition where id = $1`, wfID); !strings.Contains(contract, "apiBatch") {
		t.Fatalf("an update without batch keeps the contract: %s", contract)
	}
	events = nil
	keyed := map[string]any{"table": "products", "primary_key": []any{"id"}, "where": map[string]any{"qty": map[string]any{"lt": 5}},
		"idempotency_key_template": "{id}", "enabled": false}
	out = call(member, "workflow_builder_create_workflow", map[string]any{"name": "Stock check", "steps": steps, "batch": nil,
		"triggers": map[string]any{"when": keyed, "watch": map[string]any{"query": "{ products { count } }", "value_path": "products.0.count",
			"op": "lt", "threshold": 5, "cadence_seconds": 60, "debounce_seconds": 0}}})
	if !strings.Contains(out, `\"ok\":true,\"action\":\"updated\",\"workflowId\":\"`+wfID) || !strings.Contains(out, `\"triggerId\":`) {
		t.Fatalf("keyed trigger: %s", out)
	}
	if len(events) != 2 || !strings.Contains(events[1], "**Idempotency key:** `{id}`") {
		t.Fatalf("trigger card: %v", events)
	}
	if got := scalar(t, db, `select count(*)::text || ' ' || bool_and(not enabled)::text || ' ' || min(idempotency_key_template)
		from subscription where workflow_id = $1`, wfID); got != "1 true {id}" {
		t.Fatalf("subscription: %s", got)
	}
	if got := scalar(t, db, `select coalesce(output_contract::text, 'null') || ' ' || coalesce(cron, 'none') from workflow_definition where id = $1`, wfID); got != "null none" {
		t.Fatalf("batch null and no cron: %s", got)
	}
	call(member, "workflow_builder_create_workflow", map[string]any{"name": "Stock check", "steps": steps,
		"triggers": map[string]any{"watch": map[string]any{"query": "{ products { count } }", "value_path": "products.0.count",
			"op": "changed", "severity": "high"}}})
	if got := scalar(t, db, `select count(*)::text || ' ' || min(op) || ' ' || min(cadence_seconds)::text || ' ' || min(debounce_seconds)::text
		|| ' ' || min(severity) || ' ' || coalesce(min(threshold::text), 'null') || ' ' || min(description)
		from watcher where org_id = 'org' and name = 'Stock check'`); got != `1 changed 300 3600 high null Watcher for workflow "Stock check"` {
		t.Fatalf("watcher upsert: %s", got)
	}
	bad := map[string]any{"columns": []any{map[string]any{"name": "A", "path": "a"}, map[string]any{"name": "A", "path": "b"}}}
	if out := call(member, "workflow_builder_create_workflow", map[string]any{"name": "Stock check", "steps": steps, "batch": bad}); !strings.Contains(out,
		"The workflow batch definition is invalid or unsafe.") {
		t.Fatalf("bad batch: %s", out)
	}

	seed(t, db, `insert into workflow_definition (org_id, name, updated_at) values ('org', 'Hidden', now() + interval '1 hour')`)
	seed(t, db, `insert into workflow_definition (org_id, name, owner_user_id) values ('org', 'Theirs', 'other')`)
	if out := call(member, "workflow_builder_list_workflows", map[string]any{}); !strings.Contains(out, `\"when\":null`) {
		t.Fatalf("a disabled trigger is no when: %s", out)
	}
	seed(t, db, `update subscription set enabled = true where workflow_id = $1`, wfID)
	var traced []string
	unregister := registerTraceSink(runMine, func(event, _ *jsObj) { traced = append(traced, jsStringify(event, false)) }, nil)
	out = call(member, "workflow_builder_list_workflows", map[string]any{})
	unregister()
	if !strings.Contains(out, `\"total\":1,\"returned\":1`) || !strings.Contains(out, `\"when\":{\"table\":\"products\"`) ||
		strings.Contains(out, "Hidden") || strings.Contains(out, "Theirs") {
		t.Fatalf("member list: %s", out)
	}
	if len(traced) != 1 || !strings.Contains(traced[0], `"operation":"workflow.list","evidence":{"requestedLimit":50,"total":1,"returnedCount":1`) {
		t.Fatalf("trace: %v", traced)
	}
	if out := call(admin, "workflow_builder_list_workflows", map[string]any{"limit": 2}); !strings.Contains(out, `\"total\":3,\"returned\":2,\"workflows\":[{\"id\":`) ||
		!strings.Contains(out, `\"name\":\"Hidden\"`) || !strings.Contains(out, `\"when\":null`) {
		t.Fatalf("admin list: %s", out)
	}
	service := server(threadMine, builderRunService, "neko_workflow_builder")
	if out := call(service, "workflow_builder_list_workflows", map[string]any{}); !strings.Contains(out, `\"total\":3`) {
		t.Fatalf("service list: %s", out)
	}

	hidden := scalar(t, db, `select id::text from workflow_definition where name = 'Hidden'`)
	for _, id := range []string{hidden, "not-a-uuid", unknownUUID} {
		if out := call(member, "workflow_builder_delete_workflow", map[string]any{"workflowId": id}); !strings.Contains(out, `not_found`) {
			t.Fatalf("delete %s: %s", id, out)
		}
	}
	events = nil
	if out := call(member, "workflow_builder_delete_workflow", map[string]any{"workflowId": wfID}); !strings.Contains(out,
		`\"deleted\":{\"id\":\"`+wfID+`\",\"name\":\"Stock check\"}`) || len(events) != 1 {
		t.Fatalf("delete: %s %v", out, events)
	}
	if got := scalar(t, db, `select (select count(*) from workflow_definition where id = $1)::text || ' ' ||
		(select count(*) from subscription where workflow_id = $1)::text || ' ' || (select count(*) from watcher where workflow_id = $1)::text`, wfID); got != "0 0 0" {
		t.Fatalf("cascade: %s", got)
	}

	bot := server(builderThreadBot, runMine, "neko_workflow_builder")
	if out := call(bot, "workflow_builder_create_workflow", map[string]any{"name": "Daily recap", "steps": steps}); !strings.Contains(out, `\"ok\":true`) {
		t.Fatalf("bot chat: %s", out)
	}
	if got := scalar(t, db, `select owner_user_id from workflow_definition where name = 'Daily recap'`); got != "member" {
		t.Fatalf("bot chat owner: %s", got)
	}
	if _, err := os.Stat(filepath.Join(orgAgentRoot("org"), "workflows")); !os.IsNotExist(err) {
		t.Fatalf("a workflow save wrote a file: %v", err)
	}
	if got := scalar(t, db, `select scope || ' ' || user_id || ' ' || summary from config_change where artifact_ref = 'Daily recap'`); got != "user member Added workflow: Daily recap" {
		t.Fatalf("config change: %s", got)
	}

	events = nil
	out = call(member, "rule_builder_save_rule", map[string]any{"name": "slack_auto", "applies_to_kinds": []any{"slack_post"},
		"mode": "auto_approve", "approver_role": "admin", "allowed_targets": map[string]any{"patterns": []any{"C*"}}})
	if !strings.Contains(out, `\"ok\":true,\"action\":\"created\",\"ruleId\":\"`) || len(events) != 1 || !strings.Contains(events[0], "**Scopes:** external") {
		t.Fatalf("rule create: %s %v", out, events)
	}
	if got := scalar(t, db, `select p.applies_to_scopes::text || ' ' || p.priority::text || ' ' || p.enabled::text || ' ' || p.limits::text || ' ' ||
		p.allowed_targets::text || ' ' || coalesce(p.denied_targets::text, 'null') || ' ' || (p.approver_group_id = g.id)::text || ' ' || p.created_by_run_id::text
		from action_policy p join user_group g on g.org_id = p.org_id and g.slug = 'administrators' where p.name = 'slack_auto'`); got !=
		`{external} 100 true {} {"patterns": ["C*"]} null true `+runMine {
		t.Fatalf("rule row: %s", got)
	}
	out = call(admin, "rule_builder_save_rule", map[string]any{"name": "slack_auto", "applies_to_kinds": []any{}, "applies_to_scopes": []any{"internal"},
		"mode": "never", "priority": 5, "enabled": false, "description": "Off"})
	if !strings.Contains(out, `\"action\":\"updated\"`) {
		t.Fatalf("rule update: %s", out)
	}
	if got := scalar(t, db, `select p.mode || ' ' || p.applies_to_scopes::text || ' ' || p.priority::text || ' ' || p.enabled::text || ' ' ||
		coalesce(p.allowed_targets::text, 'null') || ' ' || p.description || ' ' || (p.approver_group_id is not null)::text || ' ' || p.created_by_run_id::text
		from action_policy p where p.name = 'slack_auto'`); got != "never {internal} 5 false null Off true "+runMine {
		t.Fatalf("rule updated row: %s", got)
	}
	seed(t, db, `delete from action_policy where org_id = 'org' and name <> 'slack_auto'`)
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority) values
		('org', 'b_rule', '{}', '{external}', 'never', 5), ('org', 'a_rule', '{}', '{external}', 'never', 5), ('org', 'z_rule', '{}', '{}', 'never', 1)`)
	if out := call(member, "rule_builder_list_rules", map[string]any{"limit": 3}); !strings.Contains(out, `\"total\":4,\"returned\":3`) ||
		!regexpOrder(out, "z_rule", "a_rule", "b_rule") || strings.Contains(out, "slack_auto") {
		t.Fatalf("rule list: %s", out)
	}

	var audits string
	if err := db.QueryRow(ctx, `select string_agg(distinct path, ',' order by path) from control_plane_audit where run_id = $1`, runMine).Scan(&audits); err != nil ||
		audits != "/v1/events,/v1/rule/list,/v1/rule/save,/v1/workflow/delete,/v1/workflow/list,/v1/workflow/save" {
		t.Fatalf("audits: %s %v", audits, err)
	}
}

func regexpOrder(text string, parts ...string) bool {
	at := 0
	for _, p := range parts {
		i := strings.Index(text[at:], p)
		if i < 0 {
			return false
		}
		at += i + len(p)
	}
	return true
}

func scalar(t *testing.T, db *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var out *string
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return deref(out)
}
