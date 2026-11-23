package webfront

import (
	"context"
	"testing"
)

func TestSeedDefaultActionPolicies(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	seed(t, db, `delete from action_policy where org_id = 'org'`)
	seed(t, db, `insert into action_policy (org_id, name, applies_to_kinds, applies_to_scopes, mode, priority, enabled) values
		('org', 'code_action_default', '{code_create_issue}', '{internal}', 'auto_approve', 10, true),
		('org', 'external_default', '{}', '{external}', 'never', 950, false)`)
	for range 2 {
		if err := a.seedDefaultActionPolicies(ctx, "org"); err != nil {
			t.Fatal(err)
		}
	}
	admins := queryText(t, db, `select id::text from user_group where org_id = 'org' and slug = 'administrators'`)
	for name, want := range map[string]string{
		"external_default":                 "approval_required|{external}|{}|950|-|2",
		"user_management_default":          "approval_required|{internal,external}|{user_admin}|90|" + admins + "|1",
		"group_management_default":         "approval_required|{internal,external}|{group_admin}|90|" + admins + "|1",
		"code_action_default":              "auto_approve|{internal}|{code_create_issue}|10|-|1",
		"source_config_management_default": "approval_required|{internal,external}|{source_config_admin}|93|" + admins + "|1",
		"data_source_management_default":   "approval_required|{internal,external}|{data_source_admin}|96|" + admins + "|1",
		"channel_management_default":       "approval_required|{internal,external}|{channel_admin}|95|" + admins + "|1",
	} {
		got := queryText(t, db, `select max(mode) filter (where enabled) || '|' || max(applies_to_scopes::text) filter (where enabled) || '|' ||
			max(applies_to_kinds::text) filter (where enabled) || '|' || max(priority) filter (where enabled) || '|' ||
			coalesce(max(approver_group_id::text) filter (where enabled), '-') || '|' || count(*)
			from action_policy where org_id = 'org' and name = $1`, name)
		if got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if n := queryText(t, db, `select count(*)::text from action_policy where org_id = 'org'`); n != "8" {
		t.Errorf("policies: %s", n)
	}
	seed(t, db, `delete from user_group where org_id = 'org' and slug = 'administrators'`)
	seed(t, db, `delete from action_policy where org_id = 'org' and name = 'user_management_default'`)
	if err := a.seedDefaultActionPolicies(ctx, "org"); err == nil || err.Error() != "built-in group administrators is missing" {
		t.Errorf("no administrators group: %v", err)
	}
}
