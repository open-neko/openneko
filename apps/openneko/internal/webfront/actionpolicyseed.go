package webfront

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type defaultActionPolicy struct {
	name, description string
	kinds, scopes     []string
	adminApprover     bool
	priority          int
}

var defaultActionPolicies = []defaultActionPolicy{
	{"external_default", "Operator must approve any external state-changing operation before it executes.",
		[]string{}, []string{"external"}, false, 950},
	{"user_management_default", "Inviting, role changes, and deactivation proposed from chat require an admin's approval.",
		[]string{"user_admin"}, []string{"internal", "external"}, true, 90},
	{"group_management_default", "Group, membership, item grant and data access changes proposed from chat require an admin's approval.",
		[]string{"group_admin"}, []string{"internal", "external"}, true, 90},
	{"code_action_default", "Filing issues and drafting patches always require operator approval; OpenNeko never applies code changes itself.",
		[]string{"code_create_issue", "code_draft_patch"}, []string{"internal", "external"}, false, 94},
	{"source_config_management_default", "Adding roles, changing source access, or registering a source on the customer data engine, proposed from chat, requires an admin's approval.",
		[]string{"source_config_admin"}, []string{"internal", "external"}, true, 93},
	{"data_source_management_default", "Registering, enabling/disabling or removing a data source proposed from chat requires an admin's approval.",
		[]string{"data_source_admin"}, []string{"internal", "external"}, true, 96},
	{"channel_management_default", "Linking, unlinking or blocking a channel identity proposed from chat requires an admin's approval.",
		[]string{"channel_admin"}, []string{"internal", "external"}, true, 95},
}

// SeedDefaultActionPolicies matches seedDefaultActionPolicies() in
// packages/llm/src/workflows/policy-engine.ts for the instance org.
func (a *API) SeedDefaultActionPolicies(ctx context.Context) error {
	orgID, err := a.OrgID(ctx)
	if err != nil {
		return err
	}
	return a.seedDefaultActionPolicies(ctx, orgID)
}

func (a *API) seedDefaultActionPolicies(ctx context.Context, orgID string) error {
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('openneko.action_policy.seed'), hashtext($1))`, orgID); err != nil {
			return err
		}
		for _, p := range defaultActionPolicies {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from action_policy where org_id = $1 and name = $2 and enabled)`,
				orgID, p.name).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			var group *string
			if p.adminApprover {
				err := tx.QueryRow(ctx, `select id::text from user_group where org_id = $1 and slug = 'administrators'`, orgID).Scan(&group)
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("built-in group administrators is missing")
				}
				if err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `
				insert into action_policy (org_id, name, description, applies_to_kinds, applies_to_scopes, mode,
					risk_threshold_auto_approve, allowed_targets, denied_targets, limits, approver_group_id, priority, enabled)
				values ($1, $2, $3, $4, $5, 'approval_required', null, null, null, '{}'::jsonb, $6::uuid, $7, true)`,
				orgID, p.name, p.description, p.kinds, p.scopes, group, p.priority); err != nil {
				return err
			}
		}
		return nil
	})
}
