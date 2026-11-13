package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func jsText(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func jsJSONText(v any) *string {
	if v == nil {
		return nil
	}
	if _, ok := v.(jsUndefined); ok {
		return nil
	}
	text := jsStringify(v, false)
	return &text
}

func jsStrings(v any) []string {
	out := []string{}
	for _, e := range jsArray(v) {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// runWorkflowFilter is runWorkflowFilter() in packages/llm/src/work/entitlement-scope.ts.
func (a *API) runWorkflowFilter(ctx context.Context, orgID, runID string) (func(id, owner string) bool, error) {
	if runID == "" {
		return func(string, string) bool { return true }, nil
	}
	act, err := a.entitlementActorForRun(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	if act == nil || (act.UserID == "" && !act.Admin && act.PackID == "") {
		return func(string, string) bool { return false }, nil
	}
	held, err := a.heldItems(ctx, *act, "workflow")
	if err != nil {
		return nil, err
	}
	service := act.Admin && act.UserID == "" && act.PackID == ""
	return func(id, owner string) bool {
		if act.UserID != "" && owner == act.UserID {
			return true
		}
		if owner != "" {
			return service || held.All
		}
		return held.has(id)
	}, nil
}

// botChatOwner is botChatOwner() in packages/llm/src/work/bot.ts: "" when the
// thread is not a bot chat.
func (a *API) botChatOwner(ctx context.Context, orgID, threadID string) (string, error) {
	if threadID == "" {
		return "", nil
	}
	var owner *string
	err := a.DB.QueryRow(ctx, `select t.created_by_user_id from bot b join work_thread t on t.id = b.thread_id
		where b.org_id = $1 and b.thread_id::text = $2 limit 1`, orgID, threadID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return deref(owner), err
}

// compileBatch is compileWorkflowApiBatchContract().
func compileBatch(batch any) (*jsObj, error) {
	recordsField, ok := jsGet(batch, "recordsField").(string)
	if !ok {
		recordsField = "records"
	}
	draft := jsObjOf("apiBatch", jsObjOf("version", 1.0, "compiled", true, "compiler", "workflow",
		"recordsField", recordsField, "columns", jsLookup(batch, "columns")))
	field, columns, ok := compiledBatch([]byte(jsStringify(draft, false)))
	if !ok {
		return nil, errors.New("The workflow batch definition is invalid or unsafe.")
	}
	list := make([]any, len(columns))
	for i, c := range columns {
		col := jsObjOf("name", c.Name, "path", c.Path)
		if c.HasDefault {
			col.set("default", c.Default)
		}
		list[i] = col
	}
	return jsObjOf("version", 1.0, "compiled", true, "compiler", "workflow", "recordsField", field, "columns", list), nil
}

// outputContractFor is resolveOutputContract() in saveWorkflow(): the tool
// never sends an output contract, so the base is the stored one.
func outputContractFor(stored []byte, in *jsObj) ([]byte, error) {
	batch, present := in.vals["batch"]
	if !present {
		return stored, nil
	}
	next := &jsObj{}
	if base, err := jsParse(stored); err == nil {
		if obj, ok := base.(*jsObj); ok {
			next = obj
		}
	}
	if batch == nil {
		if _, ok := next.vals["apiBatch"]; ok {
			delete(next.vals, "apiBatch")
			next.keys = slices.DeleteFunc(next.keys, func(k string) bool { return k == "apiBatch" })
		}
	} else {
		compiled, err := compileBatch(batch)
		if err != nil {
			return nil, err
		}
		next.set("apiBatch", compiled)
	}
	if len(next.keys) == 0 {
		return nil, nil
	}
	return []byte(jsStringify(next, false)), nil
}

// saveWorkflowDefinition is saveWorkflow() in packages/llm/src/workflows/store.ts.
func (a *API) saveWorkflowDefinition(ctx context.Context, in *jsObj, owner string) (workflowRecord, string, error) {
	orgID, _ := in.vals["orgId"].(string)
	name, _ := in.vals["name"].(string)
	triggers := in.vals["triggers"]
	cron := jsText(jsGet(triggers, "cron"))
	timezone := "UTC"
	if tz := jsText(jsGet(triggers, "timezone")); tz != nil {
		timezone = *tz
	}
	cronEnabled := true
	if enabled, ok := jsGet(triggers, "enabled").(bool); ok {
		cronEnabled = enabled
	}
	steps := jsStringify(jsLookup(in, "steps"), false)
	description, goal, overlay := jsText(in.vals["description"]), jsText(in.vals["goal"]), jsText(in.vals["systemPromptOverlay"])
	existing, err := scanWorkflowRecord(a.DB.QueryRow(ctx, `select `+workflowRecordColumns+` from workflow_definition
		where org_id = $1 and owner_user_id = $2 and name = $3 limit 1`, orgID, owner, name))
	switch {
	case err == nil:
		contract, err := outputContractFor(existing.OutputContract, in)
		if err != nil {
			return workflowRecord{}, "", err
		}
		saved, err := scanWorkflowRecord(a.DB.QueryRow(ctx, `update workflow_definition set description = coalesce($2, description),
			system_prompt_overlay = coalesce($3, system_prompt_overlay), steps = $4::jsonb, goal = coalesce($5, goal), cron = $6,
			cron_timezone = $7, cron_enabled = $8, output_contract = $9::jsonb, updated_at = now()
			where id = $1 returning `+workflowRecordColumns,
			existing.ID, description, overlay, steps, goal, cron, timezone, cronEnabled, nullableJSON(contract)))
		return saved, "updated", err
	case !errors.Is(err, pgx.ErrNoRows):
		return workflowRecord{}, "", err
	}
	contract, err := outputContractFor(nil, in)
	if err != nil {
		return workflowRecord{}, "", err
	}
	var newID string
	if err := a.DB.QueryRow(ctx, `insert into workflow_definition (org_id, owner_user_id, name, description, system_prompt_overlay,
		steps, goal, cron, cron_timezone, cron_enabled, network_hosts, daily_run_budget, output_contract, created_by_thread_id,
		created_by_run_id)
		values ($1, $2, $3, coalesce($4, ''), coalesce($5, ''), $6::jsonb, coalesce($7, ''), $8, $9, $10, '{}', null, $11::jsonb, $12, $13)
		returning id::text`,
		orgID, owner, name, description, overlay, steps, goal, cron, timezone, cronEnabled, nullableJSON(contract),
		jsText(in.vals["createdByThreadId"]), jsText(in.vals["createdByRunId"])).Scan(&newID); err != nil {
		return workflowRecord{}, "", err
	}
	saved, err := scanWorkflowRecord(a.DB.QueryRow(ctx, `update workflow_definition set origin_id = id where id = $1
		returning `+workflowRecordColumns, newID))
	return saved, "created", err
}

func nullableJSON(raw []byte) *string {
	if raw == nil {
		return nil
	}
	s := string(raw)
	return &s
}

// recordWorkflowChange writes the config_change row for a saved workflow: a
// personal workflow as its owner's change, a team workflow as a team change.
func (a *API) recordWorkflowChange(ctx context.Context, wf workflowRecord, verb string) {
	c := configChange{OrgID: wf.OrgID, Kind: "workflow", Ref: wf.Name, Summary: verb + " workflow: " + wf.Name}
	if wf.OwnerUserID != "" {
		c.Actor, c.Scope, c.UserID = wf.OwnerUserID, "user", wf.OwnerUserID
	}
	a.insertConfigChange(ctx, c)
}

var watcherOps = []string{"gt", "gte", "lt", "lte", "eq", "ne", "changed"}

// upsertWatcher is upsertWatcher() in packages/llm/src/workflows/watchers.ts.
func (a *API) upsertWatcher(ctx context.Context, orgID string, wf workflowRecord, watch any) (string, error) {
	op, _ := jsGet(watch, "op").(string)
	if !slices.Contains(watcherOps, op) {
		return "", fmt.Errorf("Invalid watcher op: %s", op)
	}
	threshold := jsJSONText(jsGet(watch, "threshold"))
	cadence, debounce := 300.0, 3600.0
	if v, ok := jsGet(watch, "cadence_seconds").(float64); ok {
		cadence = v
	}
	if v, ok := jsGet(watch, "debounce_seconds").(float64); ok {
		debounce = v
	}
	severity := "medium"
	if s := jsText(jsGet(watch, "severity")); s != nil {
		severity = *s
	}
	query, _ := jsGet(watch, "query").(string)
	valuePath, _ := jsGet(watch, "value_path").(string)
	description := `Watcher for workflow "` + wf.Name + `"`
	var id string
	err := a.DB.QueryRow(ctx, `with found as (select id from watcher where org_id = $1 and name = $2 limit 1),
		changed as (
		  update watcher set workflow_id = $3, description = $4, enabled = true, query = $5, value_path = $6, op = $7,
		    threshold = $8::jsonb, cadence_seconds = $9, debounce_seconds = $10, severity = $11, updated_at = now()
		  where id = (select id from found) returning id),
		added as (
		  insert into watcher (org_id, name, workflow_id, description, enabled, query, value_path, op, threshold,
		    cadence_seconds, debounce_seconds, severity)
		  select $1, $2, $3, $4, true, $5, $6, $7, $8::jsonb, $9, $10, $11 where not exists (select 1 from found)
		  returning id)
		select id::text from changed union all select id::text from added`,
		orgID, wf.Name, wf.ID, description, query, valuePath, op, threshold, int(max(60, cadence)), int(max(0, debounce)),
		severity).Scan(&id)
	return id, err
}

func pgMessage(err error) string {
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return err.Error()
}

func triggerFailure(code, message string) *jsObj { return jsObjOf("code", code, "message", message) }

// saveWorkflow is the control plane's saveWorkflowWithTrigger(): the run may
// not change a workflow it cannot see, the workflow is always saved, and a
// trigger that fails comes back as triggerError.
func (r *runTools) saveWorkflow(ctx context.Context, in *jsObj) (any, error) {
	a := r.api
	orgID, _ := in.vals["orgId"].(string)
	name, _ := in.vals["name"].(string)
	threadID, _ := in.vals["createdByThreadId"].(string)
	runID, _ := in.vals["createdByRunId"].(string)
	owner, err := a.botChatOwner(ctx, orgID, threadID)
	if err != nil {
		return nil, err
	}
	if runID != "" {
		var existingID string
		err := a.DB.QueryRow(ctx, `select id::text from workflow_definition where org_id = $1 and name = $2 and owner_user_id = $3
			order by updated_at desc limit 1`, orgID, name, owner).Scan(&existingID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if existingID != "" {
			visible, err := a.runWorkflowFilter(ctx, orgID, runID)
			if err != nil {
				return nil, err
			}
			if !visible(existingID, owner) {
				return nil, fmt.Errorf(`A workflow named "%s" exists and this run cannot change it. Choose another name.`, name)
			}
		}
	}
	saved, action, err := a.saveWorkflowDefinition(ctx, in, owner)
	if err != nil {
		return nil, err
	}
	verb := "Updated"
	if action == "created" {
		verb = "Added"
	}
	a.recordWorkflowChange(ctx, saved, verb)
	out := jsObjOf("action", action, "workflow", jsFromGo(saved))
	triggers := in.vals["triggers"]
	if watch := jsGet(triggers, "watch"); watch != nil {
		id, err := a.upsertWatcher(ctx, orgID, saved, watch)
		if err != nil {
			out.set("triggerError", triggerFailure("invalid_watcher", pgMessage(err)))
			return out, nil
		}
		out.set("watcher", jsObjOf("id", id, "workflowId", saved.ID, "name", saved.Name))
	}
	when := jsGet(triggers, "when")
	if when == nil {
		return out, nil
	}
	filter := &jsObj{}
	copyPresent(filter, when.(*jsObj), "table", "where", "select", "primary_key", "version_column")
	filterJSON := jsStringify(filter, false)
	sc, ok := parseSourceChange(decodeJSON([]byte(filterJSON)))
	if !ok {
		out.set("triggerError", triggerFailure("invalid_trigger",
			"data trigger rejected — `table` and `primary_key` must be valid column identifiers and `primary_key` non-empty."))
		return out, nil
	}
	var hasSource bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from data_source where org_id = $1)`, orgID).Scan(&hasSource); err != nil {
		return nil, err
	}
	if !hasSource {
		out.set("triggerError", triggerFailure("no_data_source",
			"no data source is configured for this org — a data-change trigger needs one."))
		return out, nil
	}
	template, _ := jsGet(when, "idempotency_key_template").(string)
	if !jsTruthy(jsGet(when, "acknowledge_mutation_loop")) && template == "" {
		if keyword, reason := mutationLoopText(sc.Table, saved.Goal, saved.Description, saved.Steps); keyword != "" {
			out.set("triggerError", triggerFailure("mutation_loop", reason))
			return out, nil
		}
	}
	enabled := true
	if v, ok := jsGet(when, "enabled").(bool); ok {
		enabled = v
	}
	var sub subscriptionOut
	var stored []byte
	var created, updated time.Time
	err = a.DB.QueryRow(ctx, `insert into subscription (org_id, workflow_id, source_kind, filter, enabled, debounce_ms,
		max_concurrent_runs, max_chain_depth_override, idempotency_key_template)
		values ($1, $2, 'source_change', $3::jsonb, $4, 0, 5, null, $5)
		returning id::text, workflow_id::text, source_kind, filter, enabled, debounce_ms, max_concurrent_runs,
		max_chain_depth_override, idempotency_key_template, created_at, updated_at`,
		orgID, saved.ID, filterJSON, enabled, jsText(jsGet(when, "idempotency_key_template"))).Scan(&sub.ID, &sub.WorkflowID,
		&sub.SourceKind, &stored, &sub.Enabled, &sub.DebounceMs, &sub.MaxConcurrentRuns, &sub.MaxChainDepthOverride,
		&sub.IdempotencyKeyTemplate, &created, &updated)
	if err != nil {
		return nil, err
	}
	sub.Filter, sub.CreatedAt, sub.UpdatedAt = stored, jsTime(created), jsTime(updated)
	out.set("subscription", jsFromGo(sub))
	return out, nil
}

func decodeJSON(raw []byte) any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

// listWorkflows is listWorkflowsWithTriggers(): the workflows the run can
// see, newest first, each with the filter of its latest enabled data trigger.
func (r *runTools) listWorkflows(ctx context.Context, in workflowListIn) (any, error) {
	a := r.api
	visible, err := a.runWorkflowFilter(ctx, in.OrgID, deref(in.RunID))
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(ctx, `select `+workflowRecordColumns+`, (select s.filter from subscription s
		where s.org_id = w.org_id and s.workflow_id = w.id and s.source_kind = 'source_change' and s.enabled
		order by s.created_at desc limit 1)
		from workflow_definition w where org_id = $1 order by updated_at desc`, in.OrgID)
	if err != nil {
		return nil, err
	}
	type listed struct {
		workflowRecord
		When storedJSON `json:"when"`
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (listed, error) {
		var when []byte
		w, err := scanWorkflowRecord(scanTail{row, &when})
		if when == nil {
			when = []byte("null")
		}
		return listed{w, storedJSON(when)}, err
	})
	if err != nil {
		return nil, err
	}
	all = slices.DeleteFunc(all, func(w listed) bool { return !visible(w.ID, w.OwnerUserID) })
	slice := all[:min(len(all), max(in.Limit, 0))]
	workflows := make([]any, len(slice))
	for i, w := range slice {
		workflows[i] = jsFromGo(w)
	}
	return jsObjOf("total", float64(len(all)), "workflows", workflows), nil
}

// scanTail scans a row whose last columns follow the ones the inner scan reads.
type scanTail struct {
	row  pgx.Row
	tail *[]byte
}

func (s scanTail) Scan(dest ...any) error { return s.row.Scan(append(dest, s.tail)...) }

// deleteWorkflow is the control plane's deleteWorkflow(): a workflow the run
// cannot see is not found.
func (r *runTools) deleteWorkflow(ctx context.Context, in workflowDeleteIn) (any, error) {
	a := r.api
	notFound := jsObjOf("found", false, "name", nil)
	if !isUUID(in.WorkflowID) {
		return notFound, nil
	}
	var owner string
	err := a.DB.QueryRow(ctx, `select owner_user_id from workflow_definition where org_id = $1 and id = $2`,
		in.OrgID, in.WorkflowID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound, nil
	}
	if err != nil {
		return nil, err
	}
	visible, err := a.runWorkflowFilter(ctx, in.OrgID, deref(in.RunID))
	if err != nil {
		return nil, err
	}
	if !visible(in.WorkflowID, owner) {
		return notFound, nil
	}
	var name string
	err = a.DB.QueryRow(ctx, `delete from workflow_definition where org_id = $1 and id = $2 returning name`,
		in.OrgID, in.WorkflowID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound, nil
	}
	if err != nil {
		return nil, err
	}
	return jsObjOf("found", true, "name", name), nil
}

func policyRecord(p actionPolicy, orgID string) any {
	out := jsFromGo(p).(*jsObj)
	out.set("orgId", orgID)
	return out
}

// upsertPolicy is upsertActionPolicyByName() in packages/llm/src/workflows/action-store.ts.
func (r *runTools) upsertPolicy(ctx context.Context, in *jsObj) (any, error) {
	a := r.api
	orgID, _ := in.vals["orgId"].(string)
	name, _ := in.vals["name"].(string)
	description, _ := in.vals["description"].(string)
	priority, _ := in.vals["priority"].(float64)
	enabled, _ := in.vals["enabled"].(bool)
	limits := jsJSONText(in.vals["limits"])
	if limits == nil {
		empty := "{}"
		limits = &empty
	}
	args := []any{orgID, name, description, jsStrings(in.vals["appliesToKinds"]), jsStrings(in.vals["appliesToScopes"]),
		in.vals["mode"], jsText(in.vals["riskThresholdAutoApprove"]), jsJSONText(in.vals["allowedTargets"]),
		jsJSONText(in.vals["deniedTargets"]), *limits, int(priority), enabled}
	var existingID string
	err := a.DB.QueryRow(ctx, `select id::text from action_policy where org_id = $1 and name = $2 limit 1`, orgID, name).Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if existingID != "" {
		policy, err := scanActionPolicy(a.DB.QueryRow(ctx, `update action_policy set description = $3, applies_to_kinds = $4,
			applies_to_scopes = $5, mode = $6, risk_threshold_auto_approve = $7, allowed_targets = $8::jsonb,
			denied_targets = $9::jsonb, limits = $10::jsonb, priority = $11, enabled = $12, updated_at = now()
			where org_id = $1 and name = $2 and id = $13 returning `+actionPolicyColumns, append(args, existingID)...))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("action_policy %s disappeared", existingID)
		}
		if err != nil {
			return nil, err
		}
		return jsObjOf("action", "updated", "policy", policyRecord(policy, orgID)), nil
	}
	var approverGroup *string
	if in.vals["approverRole"] == "admin" {
		var id string
		err := a.DB.QueryRow(ctx, `select id::text from user_group where org_id = $1 and slug = 'administrators'`, orgID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("built-in group administrators is missing")
		}
		if err != nil {
			return nil, err
		}
		approverGroup = &id
	}
	policy, err := scanActionPolicy(a.DB.QueryRow(ctx, `insert into action_policy (org_id, name, description, applies_to_kinds,
		applies_to_scopes, mode, risk_threshold_auto_approve, allowed_targets, denied_targets, limits, priority, enabled,
		approver_group_id, created_by_thread_id, created_by_run_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11, $12, $13, $14, $15)
		returning `+actionPolicyColumns, append(args, approverGroup, jsText(in.vals["createdByThreadId"]),
		jsText(in.vals["createdByRunId"]))...))
	if err != nil {
		return nil, err
	}
	return jsObjOf("action", "created", "policy", policyRecord(policy, orgID)), nil
}

// listPolicies is listActionPolicies(): every rule, by priority then name.
func (r *runTools) listPolicies(ctx context.Context, in policyListIn) (any, error) {
	rows, err := r.api.DB.Query(ctx, `select `+actionPolicyColumns+` from action_policy where org_id = $1
		order by priority, name`, in.OrgID)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (actionPolicy, error) { return scanActionPolicy(row) })
	if err != nil {
		return nil, err
	}
	policies := make([]any, 0, len(all))
	for _, p := range all[:min(len(all), max(in.Limit, 0))] {
		policies = append(policies, policyRecord(p, in.OrgID))
	}
	return jsObjOf("total", float64(len(all)), "policies", policies), nil
}
