package webfront

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"time"
)

// workflowVersionHash covers what an API or webhook run of the workflow
// does: its text and steps, its schedule, watchers and data triggers, its
// output and input contract, the owner's own skills and the team skills
// its script steps run.
func (a *API) workflowVersionHash(ctx context.Context, orgID, workflowID string) (string, error) {
	var name, description, goal, overlay, steps, cronTZ, owner string
	var cron, contract *string
	var cronOn bool
	var hosts []string
	if err := a.DB.QueryRow(ctx, `select name, description, goal, system_prompt_overlay, steps::text, cron, cron_timezone, cron_enabled,
		output_contract::text, coalesce(network_hosts, '{}'), owner_user_id from workflow_definition where org_id = $1 and id = $2`, orgID, workflowID).
		Scan(&name, &description, &goal, &overlay, &steps, &cron, &cronTZ, &cronOn, &contract, &hosts, &owner); err != nil {
		return "", err
	}
	collect := func(sql string) ([]string, error) {
		rows, err := a.DB.Query(ctx, sql, orgID, workflowID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
	watchers, err := collect(`select jsonb_build_array(name, enabled, query, value_path, op, threshold, cadence_seconds, activation, variables_json)::text
		from watcher where org_id = $1 and workflow_id = $2 order by name`)
	if err != nil {
		return "", err
	}
	triggers, err := collect(`select jsonb_build_array(source_kind, filter, enabled)::text from subscription
		where org_id = $1 and workflow_id = $2 order by id`)
	if err != nil {
		return "", err
	}
	orgRoot := orgAgentRoot(orgID)
	skills := map[string]skillFileHashes{}
	if owner != "" {
		skills["own"] = hashSkillTree(memberSkillsRoot(orgRoot, owner))
	}
	parsed, _ := jsParse([]byte(steps))
	var scripted []string
	for _, step := range jsArray(parsed) {
		if skill := jsTemplate(jsProp(jsGet(step, "script"), "skill")); skill != "" && !slices.Contains(scripted, skill) {
			scripted = append(scripted, skill)
		}
	}
	slices.Sort(scripted)
	for _, skill := range scripted {
		tree := skillFileHashes{}
		for p, h := range hashSkillTree(filepath.Join(orgRoot, "skills")) {
			if skillOf(p) == skill {
				tree[p] = h
			}
		}
		skills["team:"+skill] = tree
	}
	content, err := json.Marshal(map[string]any{"name": name, "description": description, "goal": goal, "overlay": overlay,
		"steps": steps, "cron": cron, "cronTimezone": cronTZ, "cronEnabled": cronOn, "contract": contract, "hosts": hosts,
		"watchers": watchers, "triggers": triggers, "skills": skills})
	if err != nil {
		return "", err
	}
	return sha256Hex(string(content)), nil
}

// workflowAccessHeld says whether API and webhook runs wait for an admin.
// A workflow with API access from before the approved hash has no hash; it
// takes the current version as approved on its first check. The caller
// may hold the workflow's row lock, so the record skips a locked row.
func (a *API) workflowAccessHeld(ctx context.Context, orgID, workflowID string) (bool, string, error) {
	var held *time.Time
	var approved *string
	if err := a.DB.QueryRow(ctx, `select access_held_at, access_approved_hash from workflow_definition where org_id = $1 and id = $2`,
		orgID, workflowID).Scan(&held, &approved); err != nil {
		return false, "", err
	}
	current, err := a.workflowVersionHash(ctx, orgID, workflowID)
	if err != nil {
		return false, "", err
	}
	if held == nil && approved == nil {
		_, err := a.DB.Exec(ctx, `update workflow_definition set access_approved_hash = $3 where id = (select id from workflow_definition
			where org_id = $1 and id = $2 and access_approved_hash is null and access_held_at is null for update skip locked)`, orgID, workflowID, current)
		return false, current, err
	}
	return held != nil || deref(approved) != current, current, nil
}
