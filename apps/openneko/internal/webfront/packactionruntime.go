package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

type packApiOperation struct {
	OperationID       string `json:"operationId"`
	MutationRoot      string `json:"mutationRoot"`
	Reversible        bool   `json:"reversible"`
	IdempotencyHeader string `json:"idempotencyHeader"`
}

type packPersonalOAuthConnection struct {
	Key           string `json:"key"`
	ProviderLabel string `json:"providerLabel"`
}

type installedPackActionRow struct {
	ID              string
	PackID          string
	Config          map[string]any
	Definition      *jsObj
	Enabled         bool
	Readiness       string
	ReadinessReason *string
	InstallStatus   string
	Source          map[string]any
	Connections     []packPersonalOAuthConnection
}

func parsePersonalOAuthConnections(rawConfig []byte) []packPersonalOAuthConnection {
	var parsed struct {
		UserOAuth []packPersonalOAuthConnection `json:"_userOAuth"`
	}
	_ = json.Unmarshal(rawConfig, &parsed)
	return parsed.UserOAuth
}

type actionRequestRecord struct {
	ID          string
	OrgID       string
	ActorUserID *string
	ActorRole   *string
	ActorBack   *string
	Scope       string
	Kind        string
	Target      *string
	Payload     *jsObj
	Status      string
	WorkRunID   *string
	Summary     *string
	ApprovedBy  *string
	ApprovedAt  *time.Time
}

type actionExecutionOutcome struct {
	Status             string
	ExternalRef        *string
	CommandOrOperation *string
	ChangesetID        *string
	Result             *jsObj
}

// packActionPreflight matches registerPackActionPreflight() in
// apps/worker/src/packs/action-preflight.ts.
func (a *API) packActionPreflight(ctx context.Context, orgID, kind string) error {
	var enabled bool
	var readiness string
	var reason *string
	err := a.DB.QueryRow(ctx, `
		select enabled, readiness, readiness_reason
		  from pack_action_definition
		 where org_id = $1 and kind = $2
		 limit 1`, orgID, kind).Scan(&enabled, &readiness, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return packs.AssertPackActionReady(kind, packs.PackActionReadiness{
		Enabled:   enabled,
		Readiness: readiness,
		Reason:    reason,
	})
}

// packConnectionBindings matches packConnectionBindings() in
// packages/llm/src/graphjin/pack-user-connections.ts.
func (a *API) packConnectionBindings(ctx context.Context, orgID, userID, installID string) ([]connectionBinding, error) {
	var active bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from app_user where id = $1 and org_id = $2 and disabled_at is null)`,
		userID, orgID).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, errors.New("Sign in with an active account to manage personal connections")
	}
	var rawConfig json.RawMessage
	err := a.DB.QueryRow(ctx, `select config from pack_install where id::text = $1 and org_id = $2 and status = 'installed'`,
		installID, orgID).Scan(&rawConfig)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("The pack is no longer installed")
	}
	if err != nil {
		return nil, err
	}
	connections := parsePersonalOAuthConnections(rawConfig)
	bindings := make([]connectionBinding, 0, len(connections))
	for _, conn := range connections {
		var rev string
		err := a.withConnection(ctx, orgID, userID, installID, conn.Key, func(row connectionRow, _ func(string, ...any) error) error {
			if row.credentials == nil || *row.credentials == "" {
				return fmt.Errorf("Connect %s on Integrations before requesting this action", conn.ProviderLabel)
			}
			rev = row.revision
			return nil
		})
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, connectionBinding{
			InstallID:     installID,
			ConnectionKey: conn.Key,
			Revision:      rev,
		})
	}
	return bindings, nil
}

// installedPackAction matches installedAction() in
// apps/worker/src/packs/declarative-action-runtime.ts.
func (a *API) installedPackAction(ctx context.Context, orgID, kind string) (*installedPackActionRow, error) {
	var row installedPackActionRow
	var rawConfig, rawDef, rawSource []byte
	err := a.DB.QueryRow(ctx, `
		select i.id::text, i.pack_id, i.config, d.definition,
		       d.enabled, d.readiness, d.readiness_reason,
		       i.status, i.config->'_runtime'->'source'
		  from pack_action_definition d
		  join pack_artifact a
		    on a.org_id = d.org_id
		   and a.artifact_kind = 'action'
		   and a.target_ref = d.kind
		  join pack_install i
		    on i.id = a.pack_install_id
		   and i.org_id = a.org_id
		 where d.org_id = $1 and d.kind = $2
		 order by i.installed_at desc nulls last
		 limit 1`, orgID, kind).Scan(
		&row.ID, &row.PackID, &rawConfig, &rawDef,
		&row.Enabled, &row.Readiness, &row.ReadinessReason,
		&row.InstallStatus, &rawSource,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("no installed pack owns action %s", kind)
	}
	if err != nil {
		return nil, err
	}
	if !row.Enabled || row.Readiness != "ready" || row.InstallStatus != "installed" {
		reason := row.InstallStatus
		if row.ReadinessReason != nil {
			reason = *row.ReadinessReason
		}
		return nil, fmt.Errorf("pack action %s is unavailable: %s", kind, reason)
	}
	if len(rawSource) > 0 && string(bytes.TrimSpace(rawSource)) != "null" {
		_ = json.Unmarshal(rawSource, &row.Source)
	}
	if len(row.Source) == 0 {
		return nil, fmt.Errorf("pack action %s has no reviewed data source binding", kind)
	}
	row.Config = map[string]any{}
	if len(rawConfig) > 0 {
		_ = json.Unmarshal(rawConfig, &row.Config)
		row.Connections = parsePersonalOAuthConnections(rawConfig)
	}
	if parsed, err := jsParse(rawDef); err == nil {
		row.Definition, _ = parsed.(*jsObj)
	}
	if row.Definition == nil {
		row.Definition = &jsObj{vals: map[string]any{}}
	}
	return &row, nil
}

// packConnectionPreflight matches registerPackConnectionPreflight() in
// apps/worker/src/packs/declarative-action-runtime.ts.
func (a *API) packConnectionPreflight(ctx context.Context, req *actionRequestRecord) (*actionRequestRecord, error) {
	var owned bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from pack_action_definition where org_id = $1 and kind = $2)`,
		req.OrgID, req.Kind).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return req, nil
	}
	action, err := a.installedPackAction(ctx, req.OrgID, req.Kind)
	if err != nil {
		return nil, err
	}
	adapter, _ := jsGet(action.Definition, "adapter").(*jsObj)
	if jsGet(adapter, "kind") != "graphjin_api_operation" || len(action.Connections) == 0 {
		return req, nil
	}
	if req.ActorUserID == nil || *req.ActorUserID == "" {
		return nil, errors.New("A personal pack action requires a requesting user")
	}
	bindings, err := a.packConnectionBindings(ctx, req.OrgID, *req.ActorUserID, action.ID)
	if err != nil {
		return nil, err
	}
	nextPayload := cloneJSObj(req.Payload)
	rawBindings := make([]any, 0, len(bindings))
	for _, b := range bindings {
		rawBindings = append(rawBindings, jsObjOf(
			"installId", b.InstallID,
			"connectionKey", b.ConnectionKey,
			"revision", b.Revision,
		))
	}
	nextPayload.set("_packConnections", rawBindings)
	if err := a.updateActionRequestPayload(ctx, req.OrgID, req.ID, req.Kind, nextPayload); err != nil {
		return nil, err
	}
	req.Payload = nextPayload
	return req, nil
}

func cloneJSObj(src *jsObj) *jsObj {
	out := &jsObj{vals: map[string]any{}}
	if src == nil {
		return out
	}
	for _, k := range src.ordered() {
		out.set(k, src.vals[k])
	}
	return out
}

// updateActionRequestPayload matches updateActionRequestPayload() in
// packages/llm/src/workflows/action-store.ts.
func (a *API) updateActionRequestPayload(ctx context.Context, orgID, id, kind string, payload *jsObj) error {
	tag, err := a.DB.Exec(ctx, `
		update action_request
		   set payload = $3::jsonb, updated_at = now()
		 where org_id = $1 and id::text = $2`,
		orgID, id, jsStringify(payload, false))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("action_request %s not found", id)
	}
	sorted := make([]string, 0, len(payload.vals))
	for k := range payload.vals {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	keys := make([]any, len(sorted))
	for i, k := range sorted {
		keys[i] = k
	}
	a.recordAudit(ctx, auditEvent{
		OrgID:      orgID,
		EntityKind: "action_request",
		EntityID:   id,
		Event:      "payload_prepared",
		Payload: map[string]any{
			"kind":        kind,
			"payloadKeys": keys,
		},
	})
	return nil
}

func isMagentoV2ActionKind(kind string) bool {
	return kind == "magento.undo_changeset" ||
		kind == "magento.financial_handoff" ||
		strings.HasPrefix(kind, "magento.manage_")
}

func (a *API) shouldCreateActionRequestInGo(ctx context.Context, orgID, kind string) (bool, error) {
	if a.Gate == nil || a.Gate.WorkerURL == "" {
		return true, nil
	}
	if isMagentoV2ActionKind(kind) {
		return true, nil
	}
	var enabled, hasArtifact bool
	var readiness string
	err := a.DB.QueryRow(ctx, `
		select d.enabled, d.readiness,
		       exists (
		         select 1 from pack_artifact a
		          where a.org_id = d.org_id and a.artifact_kind = 'action' and a.target_ref = d.kind
		       )
		  from pack_action_definition d
		 where d.org_id = $1 and d.kind = $2
		 limit 1`, orgID, kind).Scan(&enabled, &readiness, &hasArtifact)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !enabled || readiness != "ready" || hasArtifact, nil
}

// createActionRequestInGo matches createActionRequest() in
// packages/llm/src/workflows/action-store.ts for Go-handled action requests.
func (a *API) createActionRequestInGo(ctx context.Context, in *jsObj) (*jsObj, error) {
	orgID, _ := in.vals["orgId"].(string)
	scope, _ := in.vals["scope"].(string)
	kind, _ := in.vals["kind"].(string)
	status, _ := in.vals["status"].(string)
	target := optString(in.vals["target"])
	riskLevel := optString(in.vals["riskLevel"])
	policyID := optString(in.vals["policyId"])
	payloadVal := in.vals["payload"]

	spec, err := a.actionTargetSpec(ctx, orgID, kind)
	if err != nil {
		return nil, err
	}
	if spec != nil {
		targets := deriveActionTargets(*spec, payloadVal)
		if len(targets) > 0 {
			joined := strings.Join(targets, ", ")
			target = &joined
		} else {
			target = nil
		}
		if status == "approved" {
			policies, err := a.listEnabledPolicies(ctx, orgID)
			if err != nil {
				return nil, err
			}
			decision := evaluateActionPolicy(policySubject{
				scope:      scope,
				kind:       kind,
				target:     target,
				targets:    targets,
				hasTargets: true,
				riskLevel:  riskLevel,
			}, policies)
			dec, _ := decision.vals["decision"].(string)
			if dec == "deny" || dec == "no_policy" {
				reason, _ := decision.vals["reason"].(string)
				return nil, fmt.Errorf("action %s is not allowed for its targets: %s", kind, reason)
			}
			if pol, ok := decision.vals["policy"].(*jsObj); ok {
				policyID = optString(pol.vals["id"])
			}
			if dec == "allow" {
				status = "approved"
			} else {
				status = "pending_approval"
			}
		}
	}

	actorUserID := optString(in.vals["actorUserId"])
	actorRole := optString(in.vals["actorRole"])
	actorBackend := optString(in.vals["actorBackend"])
	workRunID := optString(in.vals["workRunId"])
	if workRunID != nil && *workRunID != "" && (actorRole == nil || actorBackend == nil) {
		var runUser, runRole, runBack *string
		if err := a.DB.QueryRow(ctx, `select actor_user_id, actor_role, backend from work_run where id::text = $1 limit 1`,
			*workRunID).Scan(&runUser, &runRole, &runBack); err == nil {
			if actorUserID == nil {
				actorUserID = runUser
			}
			if actorRole == nil {
				actorRole = runRole
			}
			if actorBackend == nil {
				actorBackend = runBack
			}
		}
	}

	payloadObj, ok := payloadVal.(*jsObj)
	if !ok || payloadObj == nil {
		if m, isMap := payloadVal.(map[string]any); isMap {
			payloadObj, _ = jsValueOf(m).(*jsObj)
		}
		if payloadObj == nil {
			payloadObj = &jsObj{vals: map[string]any{}}
		}
	}

	var minutesSaved *int
	if v, ok := in.vals["minutesSaved"].(float64); ok {
		n := int(v)
		minutesSaved = &n
	} else if v, ok := in.vals["minutesSaved"].(int); ok {
		n := v
		minutesSaved = &n
	}

	var id string
	err = a.DB.QueryRow(ctx, `
		insert into action_request (
			org_id, actor_user_id, actor_role, actor_backend,
			workflow_run_id, triggered_by_observation_id, policy_id,
			scope, kind, target, payload, risk_level, status,
			summary, intent, minutes_saved, minutes_saved_basis,
			work_run_id, requested_by_run_id
		) values (
			$1, $2, $3, $4,
			$5::uuid, $6::uuid, $7::uuid,
			$8, $9, $10, $11::jsonb, $12, $13,
			$14, $15, $16, $17,
			$18::uuid, $19::uuid
		) returning id::text`,
		orgID,
		nullableStringPtr(actorUserID),
		nullableStringPtr(actorRole),
		nullableStringPtr(actorBackend),
		nullableStringPtr(optString(in.vals["workflowRunId"])),
		nullableStringPtr(optString(in.vals["triggeredByObservationId"])),
		nullableStringPtr(policyID),
		scope,
		kind,
		nullableStringPtr(target),
		jsStringify(payloadObj, false),
		nullableStringPtr(riskLevel),
		status,
		nullableStringPtr(optString(in.vals["summary"])),
		nullableStringPtr(optString(in.vals["intent"])),
		minutesSaved,
		nullableStringPtr(optString(in.vals["minutesSavedBasis"])),
		nullableStringPtr(workRunID),
		nullableStringPtr(optString(in.vals["requestedByRunId"])),
	).Scan(&id)
	if err != nil {
		return nil, err
	}

	a.recordAudit(ctx, auditEvent{
		OrgID:      orgID,
		EntityKind: "action_request",
		EntityID:   id,
		Event:      "created:" + status,
		Payload: map[string]any{
			"kind":         kind,
			"scope":        scope,
			"target":       nullableStringPtr(target),
			"status":       status,
			"actorUserId":  nullableStringPtr(actorUserID),
			"actorRole":    nullableStringPtr(actorRole),
			"actorBackend": nullableStringPtr(actorBackend),
		},
	})

	record := &actionRequestRecord{
		ID:          id,
		OrgID:       orgID,
		ActorUserID: actorUserID,
		Summary:     optString(in.vals["summary"]),
		ActorRole:   actorRole,
		ActorBack:   actorBackend,
		Scope:       scope,
		Kind:        kind,
		Target:      target,
		Payload:     payloadObj,
		Status:      status,
		WorkRunID:   workRunID,
	}

	if err := a.runActionRequestCreatedHooks(ctx, record); err != nil {
		msg := err.Error()
		_, _ = a.DB.Exec(ctx, `
			update action_request
			   set status = 'failed', failure_reason = $2, rejection_reason = null, updated_at = now()
			 where id::text = $1`, id, "action preflight failed: "+msg)
		a.recordAudit(ctx, auditEvent{
			OrgID:      orgID,
			EntityKind: "action_request",
			EntityID:   id,
			Event:      "preflight_failed",
			Payload: map[string]any{
				"kind":  kind,
				"error": msg,
			},
		})
		return nil, err
	}

	return jsObjOf("id", record.ID, "status", record.Status), nil
}

func (a *API) listEnabledPolicies(ctx context.Context, orgID string) ([]*jsObj, error) {
	rows, err := a.DB.Query(ctx, `select `+actionPolicyColumns+` from action_policy where org_id = $1 and enabled order by priority`, orgID)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (actionPolicy, error) { return scanActionPolicy(row) })
	if err != nil {
		return nil, err
	}
	policies := make([]*jsObj, len(found))
	for i, p := range found {
		policies[i] = policyWire(orgID, p)
	}
	return policies, nil
}

func (a *API) runActionRequestCreatedHooks(ctx context.Context, req *actionRequestRecord) error {
	if err := a.packActionPreflight(ctx, req.OrgID, req.Kind); err != nil {
		return err
	}
	if _, err := a.packConnectionPreflight(ctx, req); err != nil {
		return err
	}
	return a.magentoActionCreated(ctx, req)
}

func optString(v any) *string {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	return &s
}

func nullableStringPtr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

// approvedPackConnections matches approvedConnections() in
// apps/worker/src/packs/declarative-action-runtime.ts.
func (a *API) approvedPackConnections(ctx context.Context, req *actionRequestRecord, action *installedPackActionRow) ([]connectionBinding, error) {
	if len(action.Connections) == 0 {
		return nil, nil
	}
	if req.ActorUserID == nil || *req.ActorUserID == "" {
		return nil, errors.New("A personal pack action requires a requesting user")
	}
	current, err := a.packConnectionBindings(ctx, req.OrgID, *req.ActorUserID, action.ID)
	if err != nil {
		return nil, err
	}
	saved, ok := jsGet(req.Payload, "_packConnections").([]any)
	if !ok || len(saved) != len(current) {
		return nil, errors.New("The account connection changed; request approval again")
	}
	for _, b := range current {
		matched := false
		for _, item := range saved {
			obj, ok := item.(*jsObj)
			if !ok || obj == nil {
				continue
			}
			if obj.vals["installId"] == b.InstallID &&
				obj.vals["connectionKey"] == b.ConnectionKey &&
				obj.vals["revision"] == b.Revision {
				matched = true
				break
			}
		}
		if !matched {
			return nil, errors.New("The account connection changed; request approval again")
		}
	}
	return current, nil
}

// executeDeclarativePackAction matches declarativePackActionAdapter in
// apps/worker/src/packs/declarative-action-runtime.ts.
func (a *API) executeDeclarativePackAction(ctx context.Context, req *actionRequestRecord) (*actionExecutionOutcome, error) {
	serializedPayload := jsStringify(req.Payload, false)
	var plainPayload any
	_ = json.Unmarshal([]byte(serializedPayload), &plainPayload)
	if err := packs.AssertSafePackActionPayload(len(serializedPayload), plainPayload); err != nil {
		return nil, err
	}

	action, err := a.installedPackAction(ctx, req.OrgID, req.Kind)
	if err != nil {
		return nil, err
	}
	adapter, _ := jsGet(action.Definition, "adapter").(*jsObj)
	operationsObj, _ := jsGet(adapter, "operations").(*jsObj)
	if jsGet(adapter, "kind") != "graphjin_api_operation" || operationsObj == nil {
		return nil, fmt.Errorf("no declarative API adapter exists for action %s", req.Kind)
	}
	operationName, ok := jsGet(req.Payload, "operation").(string)
	if !ok {
		return nil, fmt.Errorf("pack action %s requested an unsupported operation", req.Kind)
	}
	opRaw, exists := operationsObj.vals[operationName]
	if !exists {
		return nil, fmt.Errorf("pack action %s requested an unsupported operation", req.Kind)
	}
	opObj, _ := opRaw.(*jsObj)
	if opObj == nil {
		return nil, fmt.Errorf("pack action %s requested an unsupported operation", req.Kind)
	}
	op := packApiOperation{
		OperationID:       packString(opObj.vals["operationId"]),
		MutationRoot:      packString(opObj.vals["mutationRoot"]),
		Reversible:        opObj.vals["reversible"] == true,
		IdempotencyHeader: packString(opObj.vals["idempotencyHeader"]),
	}
	if !graphqlName.MatchString(op.MutationRoot) {
		return nil, fmt.Errorf("pack action %s has an invalid mutation root", req.Kind)
	}

	pathObj, err := requireOptionalObject(req.Payload, "path", "pack action path")
	if err != nil {
		return nil, err
	}
	queryObj, err := requireOptionalObject(req.Payload, "query", "pack action query")
	if err != nil {
		return nil, err
	}
	bodyVal, hasBody, err := lockPackActionJSBody(req.Kind, req.Payload, jsGet(adapter, "lockedBody"), action.Config)
	if err != nil {
		return nil, err
	}

	headersObj := &jsObj{vals: map[string]any{}}
	if op.IdempotencyHeader != "" {
		headersObj.set(op.IdempotencyHeader, req.ID)
	}

	if len(action.Source) == 0 {
		return nil, fmt.Errorf("pack action %s has no reviewed data source binding", req.Kind)
	}
	source, err := a.resolveSelectedPackSource(ctx, req.OrgID, action.Source)
	if err != nil {
		return nil, err
	}

	bindings, err := a.approvedPackConnections(ctx, req, action)
	if err != nil {
		return nil, err
	}

	token, err := a.mintGraphjinToken(graphjinClaims{
		orgID:  req.OrgID,
		userID: deref(req.ActorUserID),
		role:   "pack_api_executor",
	}, 60*time.Second)
	if err != nil {
		return nil, err
	}

	callObj := &jsObj{vals: map[string]any{}}
	if len(pathObj.vals) > 0 {
		callObj.set("path", pathObj)
	}
	if len(queryObj.vals) > 0 {
		callObj.set("query", queryObj)
	}
	if len(headersObj.vals) > 0 {
		callObj.set("headers", headersObj)
	}
	if hasBody {
		callObj.set("body", bodyVal)
	}

	gqlQuery := fmt.Sprintf("mutation ExecutePackAction($call: JSON!) { %s(call: $call) { ok status_code operation_id request_id response_json } }", op.MutationRoot)
	data, messages, err := a.packActionGraphjinQuery(ctx, source.GraphqlURL, "pack_api_executor", token, bindings, gqlQuery, jsObjOf("call", callObj))
	if err != nil {
		return nil, err
	}
	respObj, _ := jsGet(data, op.MutationRoot).(*jsObj)
	if len(messages) > 0 || respObj == nil {
		if len(messages) > 0 {
			return nil, errors.New(strings.Join(messages, "; "))
		}
		return nil, errors.New("pack API mutation returned no result")
	}

	okVal, _ := respObj.vals["ok"].(bool)
	rawStatus, hasStatus := respObj.vals["status_code"]
	statusCode := 500
	if hasStatus && rawStatus != nil {
		statusCode = int(jsValueNumber(rawStatus))
	}
	if !okVal || statusCode >= 400 {
		statusLabel := "unknown"
		if hasStatus && rawStatus != nil {
			statusLabel = jsTemplate(rawStatus)
		}
		return nil, fmt.Errorf("pack API mutation failed with HTTP %s", statusLabel)
	}
	if !hasStatus || rawStatus == nil {
		statusCode = 200
	}

	var externalRef *string
	if reqID, ok := respObj.vals["request_id"].(string); ok && reqID != "" {
		externalRef = &reqID
	}
	cmdOp := op.OperationID
	if opID, ok := respObj.vals["operation_id"].(string); ok && opID != "" {
		cmdOp = opID
	}
	resultObj := jsObjOf(
		"operation", operationName,
		"statusCode", float64(statusCode),
	)
	if respJSON, exists := respObj.vals["response_json"]; exists {
		resultObj.set("response", parseResponseJSON(respJSON))
	}
	return &actionExecutionOutcome{
		Status:             "succeeded",
		ExternalRef:        externalRef,
		CommandOrOperation: &cmdOp,
		Result:             resultObj,
	}, nil
}

func requireOptionalObject(payload *jsObj, key, label string) (*jsObj, error) {
	if payload == nil {
		return &jsObj{vals: map[string]any{}}, nil
	}
	val, exists := payload.vals[key]
	if !exists {
		return &jsObj{vals: map[string]any{}}, nil
	}
	obj, ok := val.(*jsObj)
	if !ok || obj == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	return obj, nil
}

func lockPackActionJSBody(kind string, payload *jsObj, rawLocked any, config map[string]any) (any, bool, error) {
	var bodyVal any
	hasBody := false
	if payload != nil {
		bodyVal, hasBody = payload.vals["body"]
	}
	lockedObj, _ := rawLocked.(*jsObj)
	if lockedObj == nil || len(lockedObj.vals) == 0 {
		return bodyVal, hasBody, nil
	}
	out := &jsObj{vals: map[string]any{}}
	if hasBody {
		obj, ok := bodyVal.(*jsObj)
		if !ok || obj == nil {
			return nil, false, errors.New("pack action body must be an object")
		}
		for _, k := range obj.ordered() {
			out.set(k, obj.vals[k])
		}
	}
	for _, field := range lockedObj.ordered() {
		spec, _ := lockedObj.vals[field].(*jsObj)
		input := packString(jsGet(spec, "input"))
		val, exists := config[input]
		if !exists || val == nil || val == "" {
			return nil, false, fmt.Errorf("pack action %s needs the pack input %s for body field %s", kind, input, field)
		}
		out.set(field, jsValueOf(val))
	}
	return out, true, nil
}

func parseResponseJSON(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	parsed, err := jsParse([]byte(s))
	if err != nil {
		return v
	}
	return parsed
}

func (a *API) packActionGraphjinQuery(
	ctx context.Context,
	endpoint, role, token string,
	bindings []connectionBinding,
	query string,
	variables *jsObj,
) (any, []string, error) {
	baseHeaders := map[string]string{
		"Content-Type":  "application/json",
		"authorization": "Bearer " + token,
	}
	if role != "" {
		baseHeaders["X-Role"] = role
	}
	resolvedHeaders, err := a.packUserConnectionHeaders(ctx, endpoint, baseHeaders, bindings)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	payload := jsStringify(jsObjOf("query", query, "variables", variables), false)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	for k, v := range resolvedHeaders {
		req.Header.Set(k, v)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirect mode is set to error")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, fmt.Errorf("graphjin query failed: %d %s", resp.StatusCode, cutUTF16(string(raw), 500))
	}
	body, err := jsParse(bytes.TrimSpace(raw))
	if err != nil {
		return nil, nil, err
	}
	var messages []string
	if list, ok := jsGet(body, "errors").([]any); ok {
		for _, item := range list {
			switch t := item.(type) {
			case string:
				messages = append(messages, t)
			case *jsObj:
				if m, ok := t.vals["message"].(string); ok {
					messages = append(messages, m)
				} else {
					messages = append(messages, jsStringify(t, false))
				}
			default:
				messages = append(messages, jsTemplate(t))
			}
		}
	}
	data, _ := jsField(body, "data")
	return data, messages, nil
}

type actionAdapter func(context.Context, *actionRequestRecord) (*actionExecutionOutcome, error)

// goActionAdapter is the Go executor of an action request, or nil when the
// worker still runs its kind.
func (a *API) goActionAdapter(ctx context.Context, orgID, actionRequestID string) (actionAdapter, error) {
	if a.DB == nil {
		return nil, nil
	}
	var kind string
	var owned bool
	err := a.DB.QueryRow(ctx, `
		select r.kind,
		       exists (select 1 from pack_action_definition d where d.org_id = r.org_id and d.kind = r.kind)
		  from action_request r
		 where r.org_id = $1 and r.id::text = $2
		 limit 1`, orgID, actionRequestID).Scan(&kind, &owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case kind == "magento.financial_handoff":
		return a.executeMagentoFinancialHandoff, nil
	case isMagentoV2ActionKind(kind):
		return a.executeMagentoChangeset, nil
	case owned:
		return a.executeDeclarativePackAction, nil
	}
	return nil, nil
}

// executeActionRequest matches runActionExecute() and
// executeApprovedActionRequest() for the action kinds that run in Go.
func (a *API) executeActionRequest(ctx context.Context, orgID, actionRequestID string, run actionAdapter) error {
	req, err := a.loadActionRequestRecord(ctx, orgID, actionRequestID)
	if err != nil {
		return err
	}
	if req.Status != "approved" {
		return fmt.Errorf("action_request status=%s; expected approved", req.Status)
	}

	var execID string
	if err := a.DB.QueryRow(ctx, `
		insert into action_execution (org_id, action_request_id, executor, payload, status, started_at)
		values ($1, $2::uuid, $3, $4::jsonb, 'running', now())
		returning id::text`,
		orgID, req.ID, req.Kind, jsStringify(req.Payload, false)).Scan(&execID); err != nil {
		return err
	}

	outcome, execErr := run(ctx, req)
	var eventStatus string
	var eventOutcome *jsObj
	var eventErr string
	if execErr == nil {
		status := outcome.Status
		if status == "" {
			status = "succeeded"
		}
		eventStatus = status
		var resJSON any
		if outcome.Result != nil {
			resJSON = jsStringify(outcome.Result, false)
		}
		if _, err := a.DB.Exec(ctx, `
			update action_execution
			   set status = $2,
			       result = $3::jsonb,
			       external_ref = $4,
			       changeset_id = $5::uuid,
			       command_or_operation = $6,
			       error = null,
			       finished_at = now()
			 where id::text = $1`,
			execID, status, resJSON,
			nullableStringPtr(outcome.ExternalRef),
			nullableStringPtr(outcome.ChangesetID),
			nullableStringPtr(outcome.CommandOrOperation)); err != nil {
			return err
		}
		a.recordAudit(ctx, auditEvent{
			OrgID:      orgID,
			EntityKind: "action_execution",
			EntityID:   execID,
			Event:      "execution:" + status,
			Payload: map[string]any{
				"actionRequestId": req.ID,
				"executor":        req.Kind,
				"status":          status,
				"externalRef":     nullableStringPtr(outcome.ExternalRef),
				"error":           nil,
			},
		})
		if _, err := a.DB.Exec(ctx, `update action_request set status = 'executed', updated_at = now() where id::text = $1`, req.ID); err != nil {
			return err
		}
		eventOutcome = jsObjOf(
			"result", outcome.Result,
			"externalRef", nullableStringPtr(outcome.ExternalRef),
			"commandOrOperation", nullableStringPtr(outcome.CommandOrOperation),
		)
	} else {
		eventStatus = "failed"
		eventErr = execErr.Error()
		if _, err := a.DB.Exec(ctx, `
			update action_execution
			   set status = 'failed',
			       result = null,
			       external_ref = null,
			       error = $2,
			       finished_at = now()
			 where id::text = $1`, execID, eventErr); err != nil {
			return err
		}
		a.recordAudit(ctx, auditEvent{
			OrgID:      orgID,
			EntityKind: "action_execution",
			EntityID:   execID,
			Event:      "execution:failed",
			Payload: map[string]any{
				"actionRequestId": req.ID,
				"executor":        req.Kind,
				"status":          "failed",
				"externalRef":     nil,
				"error":           eventErr,
			},
		})
		if _, err := a.DB.Exec(ctx, `
			update action_request
			   set status = 'failed', failure_reason = $2, rejection_reason = null, updated_at = now()
			 where id::text = $1`, req.ID, eventErr); err != nil {
			return err
		}
	}

	if req.WorkRunID != nil && *req.WorkRunID != "" {
		var threadID string
		if err := a.DB.QueryRow(ctx, `
			select t.id::text
			  from work_run r
			  join work_thread t on t.id = r.thread_id and t.org_id = r.org_id
			 where r.org_id = $1 and r.id::text = $2
			 limit 1`, orgID, *req.WorkRunID).Scan(&threadID); err == nil && threadID != "" {
			ev := jsObjOf(
				"type", "action_request_result",
				"action_request_id", req.ID,
				"kind", req.Kind,
				"status", eventStatus,
			)
			if eventOutcome != nil {
				ev.set("outcome", eventOutcome)
			}
			if eventErr != "" {
				ev.set("error", eventErr)
			}
			if eventID, err := a.appendRunEvent(ctx, orgID, threadID, *req.WorkRunID, ev); err == nil {
				_, _ = a.DB.Exec(ctx, `update work_thread set updated_at = now() where org_id = $1 and id::text = $2`, orgID, threadID)
				a.notifyRun(*req.WorkRunID, ev, eventID)
			}
		}
	}
	return nil
}

func (a *API) loadActionRequestRecord(ctx context.Context, orgID, id string) (*actionRequestRecord, error) {
	var rec actionRequestRecord
	var rawPayload []byte
	err := a.DB.QueryRow(ctx, `
		select id::text, org_id, actor_user_id, actor_role, actor_backend,
		       scope, kind, target, payload, status, work_run_id::text,
		       summary, approved_by_user_id, approved_at
		  from action_request
		 where org_id = $1 and id::text = $2
		 limit 1`, orgID, id).Scan(
		&rec.ID, &rec.OrgID, &rec.ActorUserID, &rec.ActorRole, &rec.ActorBack,
		&rec.Scope, &rec.Kind, &rec.Target, &rawPayload, &rec.Status, &rec.WorkRunID,
		&rec.Summary, &rec.ApprovedBy, &rec.ApprovedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("action_request %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	if parsed, err := jsParse(rawPayload); err == nil {
		rec.Payload, _ = parsed.(*jsObj)
	}
	if rec.Payload == nil {
		rec.Payload = &jsObj{vals: map[string]any{}}
	}
	return &rec, nil
}
