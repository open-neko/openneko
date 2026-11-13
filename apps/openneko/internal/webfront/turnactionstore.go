package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

// policyTargetSpec is ActionTargetSpec in packages/llm/src/workflows/action-targets.ts.
type policyTargetSpec struct {
	fields      []string
	emailDomain bool
}

var builtinTargetSpecs = map[string]policyTargetSpec{"send_webhook": {fields: []string{"url"}}}

func parseTargetSpec(v any) *policyTargetSpec {
	fields, ok := jsGet(v, "fields").([]any)
	if !ok {
		return nil
	}
	spec := &policyTargetSpec{emailDomain: jsGet(v, "as") == "email_domain"}
	for _, f := range fields {
		if s, ok := f.(string); ok && s != "" {
			spec.fields = append(spec.fields, s)
		}
	}
	if len(spec.fields) == 0 {
		return nil
	}
	return spec
}

var bracketedEmail = regexp.MustCompile(`<([^>]*)>`)

func emailDomain(address string) string {
	if m := bracketedEmail.FindStringSubmatch(address); m != nil {
		address = m[1]
	}
	email := strings.ToLower(jsTrim(address))
	at := strings.LastIndex(email, "@")
	if at > 0 && at < len(email)-1 {
		return strings.TrimSuffix(email[at+1:], ".")
	}
	return "invalid:" + email
}

func payloadField(payload any, field string) (any, bool) {
	current := payload
	for _, key := range strings.Split(field, ".") {
		obj, ok := current.(*jsObj)
		if !ok {
			return nil, false
		}
		if current, ok = obj.vals[key]; !ok {
			return nil, false
		}
	}
	return current, true
}

func utf16Compare(x, y string) int {
	return slices.Compare(utf16.Encode([]rune(x)), utf16.Encode([]rune(y)))
}

// deriveActionTargets is deriveActionTargets(): the payload's targets,
// without duplicates, in sorted order.
func deriveActionTargets(spec policyTargetSpec, payload any) []string {
	seen := map[string]bool{}
	values := []string{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	for _, field := range spec.fields {
		raw, _ := payloadField(payload, field)
		list, isList := raw.([]any)
		if !isList {
			list = nil
			if raw != nil {
				list = []any{raw}
			}
		}
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				text = jsStringify(item, false)
			}
			if spec.emailDomain {
				for _, part := range strings.Split(text, ",") {
					if part = jsTrim(part); part != "" {
						add(emailDomain(part))
					}
				}
			} else if trimmed := jsTrim(text); trimmed != "" {
				add(trimmed)
			}
		}
	}
	slices.SortFunc(values, utf16Compare)
	return values
}

type policySubject struct {
	scope, kind string
	target      *string
	targets     []string
	hasTargets  bool
	riskLevel   *string
}

func (a *API) actionTargetSpec(ctx context.Context, orgID, kind string) (*policyTargetSpec, error) {
	if spec, ok := builtinTargetSpecs[kind]; ok {
		return &spec, nil
	}
	var raw []byte
	err := a.DB.QueryRow(ctx, `select definition -> 'targets' from pack_action_definition where org_id = $1 and kind = $2 limit 1`,
		orgID, kind).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || raw == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := jsParse(raw)
	if err != nil {
		return nil, err
	}
	return parseTargetSpec(v), nil
}

func policyWire(orgID string, p actionPolicy) *jsObj {
	parsed := func(raw storedJSON) any {
		if raw == nil {
			return nil
		}
		v, _ := jsParse(raw)
		return v
	}
	list := func(values []string) []any {
		out := make([]any, len(values))
		for i, v := range values {
			out[i] = v
		}
		return out
	}
	return jsObjOf("id", p.ID, "orgId", orgID, "name", p.Name, "description", p.Description,
		"appliesToKinds", list(p.AppliesToKinds), "appliesToScopes", list(p.AppliesToScopes), "mode", p.Mode,
		"riskThresholdAutoApprove", nullableText(p.RiskThresholdAutoApprove), "allowedTargets", parsed(p.AllowedTargets),
		"deniedTargets", parsed(p.DeniedTargets), "limits", parsed(p.Limits), "approverGroupId", nullableText(p.ApproverGroupID),
		"priority", float64(p.Priority), "enabled", p.Enabled, "createdByThreadId", nullableText(p.CreatedByThreadID.(*string)),
		"createdByRunId", nullableText(p.CreatedByRunID.(*string)), "createdAt", p.CreatedAt, "updatedAt", p.UpdatedAt)
}

var riskSeverity = map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}

func targetPatterns(v any) []string {
	list, ok := jsGet(v, "patterns").([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, p := range list {
		if s, ok := p.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func patternMatches(target, pattern string) bool {
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(target, pattern[1:])
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(target, pattern[:len(pattern)-1])
	}
	return target == pattern
}

func anyPatternMatches(target string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return patternMatches(target, p) })
}

// evaluateActionPolicy is evaluateActionPolicy() in
// packages/llm/src/workflows/policy-engine.ts.
func evaluateActionPolicy(s policySubject, policies []*jsObj) *jsObj {
	ordered := slices.Clone(policies)
	slices.SortStableFunc(ordered, func(x, y *jsObj) int {
		return int(x.vals["priority"].(float64) - y.vals["priority"].(float64))
	})
	targets := s.targets
	if !s.hasTargets {
		targets = nil
		if s.target != nil && *s.target != "" {
			targets = []string{*s.target}
		}
	}
	shown := strings.Join(targets, ", ")
	for _, p := range ordered {
		scopes, kinds := p.vals["appliesToScopes"].([]any), p.vals["appliesToKinds"].([]any)
		if len(scopes) > 0 && !slices.Contains(scopes, any(s.scope)) || len(kinds) > 0 && !slices.Contains(kinds, any(s.kind)) {
			continue
		}
		name := p.vals["name"].(string)
		if denied := targetPatterns(p.vals["deniedTargets"]); denied != nil && slices.ContainsFunc(targets, func(t string) bool { return anyPatternMatches(t, denied) }) {
			mode := "observe_only"
			if p.vals["mode"] == "never" {
				mode = "never"
			}
			return jsObjOf("decision", "deny", "policy", p, "mode", mode, "reason", `target "`+shown+`" is in policy "`+name+`" denied_targets`)
		}
		if allowed := targetPatterns(p.vals["allowedTargets"]); allowed != nil &&
			(len(targets) == 0 || slices.ContainsFunc(targets, func(t string) bool { return !anyPatternMatches(t, allowed) })) {
			if jsGet(p.vals["allowedTargets"], "on_miss") == "next" {
				continue
			}
			return jsObjOf("decision", "deny", "policy", p, "mode", "observe_only", "reason", `target "`+shown+`" is not in policy "`+name+`" allowed_targets`)
		}
		return decisionFromMode(p, name, s.riskLevel)
	}
	return jsObjOf("decision", "no_policy", "reason", "no_matching_policy")
}

func decisionFromMode(p *jsObj, name string, riskLevel *string) *jsObj {
	switch mode, _ := p.vals["mode"].(string); mode {
	case "never", "observe_only":
		return jsObjOf("decision", "deny", "policy", p, "mode", mode, "reason", `policy "`+name+`" mode=`+mode)
	case "draft_only":
		return jsObjOf("decision", "needs_approval", "policy", p, "mode", "draft_only",
			"reason", `policy "`+name+`" mode=draft_only — operator approves before execute`)
	case "approval_required":
		return jsObjOf("decision", "needs_approval", "policy", p, "mode", "approval_required",
			"reason", `policy "`+name+`" mode=approval_required`)
	case "auto_approve":
		threshold, _ := p.vals["riskThresholdAutoApprove"].(string)
		if threshold != "" && riskLevel != nil && *riskLevel != "" {
			level, known := riskSeverity[*riskLevel]
			limit, limitKnown := riskSeverity[threshold]
			if known && limitKnown && level > limit {
				return jsObjOf("decision", "needs_approval", "policy", p, "mode", "approval_required",
					"reason", "risk="+*riskLevel+` exceeds policy "`+name+`" auto-approve threshold (`+threshold+")")
			}
		}
		return jsObjOf("decision", "allow", "policy", p, "mode", "auto_approve")
	default:
		return jsObjOf("decision", "deny", "policy", p, "mode", "never", "reason", `policy "`+name+`" has unknown mode "`+mode+`"`)
	}
}

// evaluatePolicy is InProcessControlPlane.evaluateActionPolicy(): the org's
// enabled rules against the request, with the targets read from the payload
// when the kind names them.
func (r *runTools) evaluatePolicy(ctx context.Context, in policyIn) (any, error) {
	a := r.api
	rows, err := a.DB.Query(ctx, `select `+actionPolicyColumns+` from action_policy where org_id = $1 and enabled order by priority`, in.OrgID)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (actionPolicy, error) { return scanActionPolicy(row) })
	if err != nil {
		return nil, err
	}
	policies := make([]*jsObj, len(found))
	for i, p := range found {
		policies[i] = policyWire(in.OrgID, p)
	}
	subject := policySubject{scope: in.Scope, kind: in.Kind, target: in.Target, riskLevel: in.RiskLevel}
	spec, err := a.actionTargetSpec(ctx, in.OrgID, in.Kind)
	if err != nil {
		return nil, err
	}
	if spec != nil {
		subject.targets, subject.hasTargets = deriveActionTargets(*spec, in.Payload), true
		subject.target = nil
		if len(subject.targets) > 0 {
			joined := strings.Join(subject.targets, ", ")
			subject.target = &joined
		}
	}
	return evaluateActionPolicy(subject, policies), nil
}

// sourceConfigGate is requireSourceConfigAccess() for a run; "" when the run
// may use the source config tools.
func (a *API) sourceConfigGate(ctx context.Context, orgID, runID string) (string, error) {
	if enabled, err := a.sourceConfigEnabled(ctx, orgID); err != nil || !enabled {
		return "GraphJin config capability is disabled.", err
	}
	if runID != "" {
		var runOrg string
		err := a.DB.QueryRow(ctx, `select org_id from work_run where id::text = $1`, runID).Scan(&runOrg)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && runOrg != orgID {
			return "GraphJin config run is not available.", nil
		}
		if err != nil {
			return "", err
		}
	}
	if admin, err := a.runSourceConfigAdmin(ctx, orgID, runID); err != nil || !admin {
		return "GraphJin config tools are admin-only.", err
	}
	return "", nil
}

// createActionRequest is InProcessControlPlane.createActionRequest(): the
// run's grant checks, then the worker, which stores the request and runs the
// pack preflight.
func (r *runTools) createActionRequest(ctx context.Context, in *jsObj) (any, error) {
	a := r.api
	orgID, _ := in.vals["orgId"].(string)
	kind, _ := in.vals["kind"].(string)
	scope, _ := in.vals["scope"].(string)
	runID, _ := in.vals["workRunId"].(string)
	if kind == "source_config_admin" {
		denied, err := a.sourceConfigGate(ctx, orgID, runID)
		if err != nil {
			return nil, err
		}
		if denied != "" {
			return nil, errors.New("source_config_admin: " + denied)
		}
	}
	if runID != "" {
		granted := scope == "external"
		if !granted {
			if err := a.DB.QueryRow(ctx, `select exists (select 1 from pack_action_definition where org_id = $1 and kind = $2)`,
				orgID, kind).Scan(&granted); err != nil {
				return nil, err
			}
		}
		if granted {
			act, err := a.entitlementActorForRun(ctx, orgID, runID)
			if err != nil {
				return nil, err
			}
			held := heldSet{}
			if act != nil {
				if held, err = a.heldItems(ctx, *act, "action"); err != nil {
					return nil, err
				}
			}
			if !held.has(kind) {
				return nil, fmt.Errorf("action %s is not available to this run", kind)
			}
		}
	}
	status, reply, err := a.workerDo(ctx, http.MethodPost, "/admin/action-requests/create", 65*time.Second, []byte(jsStringify(in, false)))
	if err != nil {
		return nil, err
	}
	var body struct {
		ID     any `json:"id"`
		Status any `json:"status"`
		Error  any `json:"error"`
	}
	parseErr := json.Unmarshal(reply, &body)
	if status < 200 || status > 299 {
		if msg, ok := body.Error.(string); ok && parseErr == nil {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("worker action preflight failed with status %d", status)
	}
	id, idOK := body.ID.(string)
	state, stateOK := body.Status.(string)
	if parseErr != nil || !idOK || id == "" || !stateOK {
		return nil, errors.New("worker action preflight returned an invalid response")
	}
	return jsObjOf("id", id, "status", state), nil
}

func (r *runTools) enqueueAction(ctx context.Context, in actionRefIn) error {
	tx, err := r.api.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := r.api.enqueueActionExecute(ctx, tx, in.OrgID, in.ActionRequestID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// waitForAction is InProcessControlPlane.waitForActionExecution(): it polls
// the request until it runs, fails or the time is up.
func (r *runTools) waitForAction(ctx context.Context, in actionWaitIn) (any, error) {
	timeout := time.Duration(min(max(in.TimeoutMs, 1000), 120_000)) * time.Millisecond
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var status string
		var rejection *string
		err := r.api.DB.QueryRow(ctx, `select status, rejection_reason from action_request where org_id = $1 and id::text = $2`,
			in.OrgID, in.ActionRequestID).Scan(&status, &rejection)
		if errors.Is(err, pgx.ErrNoRows) {
			return jsObjOf("status", "failed", "error", "Action request disappeared before execution completed."), nil
		}
		if err != nil {
			return nil, err
		}
		if status == "executed" || status == "failed" || status == "rejected" {
			var execStatus, ref, operation, execError *string
			var result []byte
			err := r.api.DB.QueryRow(ctx, `select status, result, external_ref, command_or_operation, error from action_execution
				where action_request_id::text = $1 order by created_at desc limit 1`, in.ActionRequestID).
				Scan(&execStatus, &result, &ref, &operation, &execError)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if status != "executed" {
				message := "Action execution failed."
				if status == "rejected" {
					message = "Action request was rejected."
				}
				for _, s := range []*string{rejection, execError} {
					if s != nil {
						message = *s
					}
				}
				return jsObjOf("status", "failed", "error", message), nil
			}
			switch deref(execStatus) {
			case "partially_applied":
				return jsObjOf("status", "failed", "error", "The action was partly applied. Review its details before any retry."), nil
			case "reconcile_required":
				return jsObjOf("status", "failed", "error", "The action outcome needs reconciliation. Review its details before any retry."), nil
			}
			var outcome any
			if result != nil {
				if outcome, err = jsParse(result); err != nil {
					return nil, err
				}
			}
			return jsObjOf("status", "succeeded", "outcome", jsObjOf("result", outcome, "externalRef", nullableText(ref),
				"commandOrOperation", nullableText(operation))), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return jsObjOf("status", "timeout"), nil
}

const outputColumns = `id::text, org_id, workflow_run_id::text, work_run_id::text, kind, title, body, payload, artifact_path, scope,
	topic, mood, time_window_start, time_window_end, freshness_ttl_seconds, seen_count, last_seen_at, created_at`

func scanOutputWire(row pgx.Row) (*jsObj, error) {
	var id, org, workflowRun, workRun, kind, title, body string
	var payload []byte
	var artifact, scope, topic, mood *string
	var start, end *time.Time
	var ttl *int
	var seen int
	var lastSeen, created time.Time
	if err := row.Scan(&id, &org, &workflowRun, &workRun, &kind, &title, &body, &payload, &artifact, &scope, &topic, &mood,
		&start, &end, &ttl, &seen, &lastSeen, &created); err != nil {
		return nil, err
	}
	parsed, err := jsParse(payload)
	if err != nil {
		return nil, err
	}
	var freshness any
	if ttl != nil {
		freshness = float64(*ttl)
	}
	return jsObjOf("id", id, "orgId", org, "workflowRunId", workflowRun, "workRunId", workRun, "kind", kind, "title", title,
		"body", body, "payload", parsed, "artifactPath", nullableText(artifact), "scope", nullableText(scope),
		"topic", nullableText(topic), "mood", nullableText(mood), "timeWindowStart", nullableText(jsTimePtr(start)),
		"timeWindowEnd", nullableText(jsTimePtr(end)), "freshnessTtlSeconds", freshness, "seenCount", float64(seen),
		"lastSeenAt", jsTime(lastSeen), "createdAt", jsTime(created)), nil
}

// emitOutput is emitWorkflowOutput(): an identical finding inside 24 hours
// raises the first card's count. The worker then sends the output to the
// bound channels.
func (r *runTools) emitOutput(ctx context.Context, in outputIn) (any, error) {
	a := r.api
	now := jsNow()
	var dedupe *string
	if title := jsTrim(deref(in.Title)); title != "" {
		var workflowID string
		err := a.DB.QueryRow(ctx, `select workflow_id::text from workflow_run where id::text = $1`, in.WorkflowRunID).Scan(&workflowID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			sum := sha256.Sum256([]byte(workflowID + "\x00" + in.Kind + "\x00" + strings.ToLower(title)))
			key := hex.EncodeToString(sum[:])
			dedupe = &key
		}
	}
	var output *jsObj
	var err error
	if dedupe != nil {
		output, err = scanOutputWire(a.DB.QueryRow(ctx, `update workflow_output set seen_count = seen_count + 1, last_seen_at = $3
			where id = (select id from workflow_output where org_id = $1 and dedupe_key = $2 and created_at >= $4
			  order by created_at desc limit 1)
			returning `+outputColumns, in.OrgID, *dedupe, now, now.Add(-24*time.Hour)))
		if errors.Is(err, pgx.ErrNoRows) {
			output, err = nil, nil
		}
	}
	if output == nil && err == nil {
		payload := in.Payload
		if payload == nil {
			payload = &jsObj{}
		}
		output, err = scanOutputWire(a.DB.QueryRow(ctx, `insert into workflow_output (org_id, workflow_run_id, work_run_id, kind, title, body,
			payload, artifact_path, scope, topic, mood, time_window_start, time_window_end, freshness_ttl_seconds, dedupe_key, last_seen_at)
			values ($1, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12::timestamptz, $13::timestamptz, $14, $15, $16)
			returning `+outputColumns, in.OrgID, in.WorkflowRunID, in.WorkRunID, in.Kind, deref(in.Title), deref(in.Body),
			jsStringify(payload, false), in.ArtifactPath, in.Scope, in.Topic, in.Mood, in.TimeWindowStart, in.TimeWindowEnd,
			in.FreshnessTTLSeconds, dedupe, now))
	}
	if err != nil {
		return nil, err
	}
	body := []byte(jsStringify(jsObjOf("orgId", in.OrgID, "output", output), false))
	go func() {
		status, reply, err := a.workerDo(context.WithoutCancel(ctx), http.MethodPost, "/admin/workflow-outputs/deliver", workerAdminTimeout, body)
		if err == nil && status != http.StatusAccepted {
			err = fmt.Errorf("worker answered %d: %s", status, strings.TrimSpace(string(reply)))
		}
		if err != nil {
			log.Printf("[workflow-output] delivery hook failed: %v", err)
		}
	}()
	return output, nil
}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

// operationContract is operationContract() in
// packages/llm/src/work/pack-action-descriptors.ts.
func operationContract(definition any) string {
	operations, ok := jsGet(jsGet(definition, "adapter"), "operations").(*jsObj)
	if !ok {
		return ""
	}
	var entries []string
	for _, name := range operations.ordered() {
		op := operations.vals[name]
		var details []string
		if path, ok := jsGet(op, "readPath").(string); ok {
			var keys []string
			for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
				keys = append(keys, m[1])
			}
			if len(keys) > 0 {
				details = append(details, "row.path keys: "+strings.Join(keys, ", "))
			}
		}
		if key, ok := jsGet(op, "bodyKey").(string); ok {
			details = append(details, "row.body key: "+key)
		}
		if reversible, ok := jsGet(op, "reversible").(bool); ok {
			details = append(details, "reversible: "+map[bool]string{true: "yes", false: "no"}[reversible])
		}
		if len(details) > 0 {
			name += " (" + strings.Join(details, "; ") + ")"
		}
		entries = append(entries, name)
	}
	if len(entries) == 0 {
		return ""
	}
	return ` Each row uses {"entity_ref":"...","path":{...},"body":{...}}. Supported operations: ` + strings.Join(entries, "; ") + "."
}

// packActionDescriptors is listPackActionDescriptors(): the org's ready and
// enabled pack actions, each asking for approval by default.
func (a *API) packActionDescriptors(ctx context.Context, orgID string) ([]packActionDescriptor, error) {
	rows, err := a.DB.Query(ctx, `select definition from pack_action_definition where org_id = $1 and enabled and readiness = 'ready'`, orgID)
	if err != nil {
		return nil, err
	}
	definitions, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	out := []packActionDescriptor{}
	for _, raw := range definitions {
		definition, err := jsParse(raw)
		if err != nil {
			return nil, err
		}
		kind, _ := jsGet(definition, "kind").(string)
		description, _ := jsGet(definition, "description").(string)
		if kind == "" || description == "" {
			continue
		}
		switch schema := jsGet(definition, "inputSchema").(type) {
		case *jsObj, []any:
			description += " Payload schema: " + jsStringify(schema, false) + "."
		}
		d := packActionDescriptor{Kind: kind, Scope: "external", DefaultMode: "ask", Description: description + operationContract(definition)}
		if example, ok := jsGet(definition, "example").(*jsObj); ok {
			d.Example = example
		}
		out = append(out, d)
	}
	return out, nil
}
