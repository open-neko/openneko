package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

var allowedMagentoCapKeys = map[string]bool{
	"maxRowsPerChangeset":  true,
	"maxPriceDeltaPercent": true,
	"maxDiscountPercent":   true,
	"maxCouponCount":       true,
	"maxProjectedExposure": true,
	"maxDailyAutoActions":  true,
	"maxStoreCredit":       true,
	"minPromotionDays":     true,
	"skuCooldownSeconds":   true,
}

func (a *API) readMagentoStoreManagement(ctx context.Context, orgID string) (*jsObj, error) {
	type controlRow struct {
		domain      string
		riskClass   int
		enabled     bool
		autoExecute bool
		caps        any
		scope       any
		updatedAt   time.Time
	}
	var controls []controlRow
	rows, err := a.DB.Query(ctx, `select domain, risk_class, enabled, auto_execute, caps::text, scope::text, updated_at
		from magento_store_control where org_id = $1 order by domain`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r controlRow
		var rawCaps, rawScope []byte
		if err := rows.Scan(&r.domain, &r.riskClass, &r.enabled, &r.autoExecute, &rawCaps, &rawScope, &r.updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.caps, _ = jsParse(rawCaps)
		r.scope, _ = jsParse(rawScope)
		controls = append(controls, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type ruleRow struct {
		id              string
		name            string
		instruction     string
		domain          string
		actionKind      string
		compiledPolicy  any
		dailyCap        int
		cooldownSeconds int
		enabled         bool
		suspendedReason *string
		lastFiredAt     *time.Time
	}
	var rules []ruleRow
	rows, err = a.DB.Query(ctx, `select id::text, name, instruction, domain, action_kind, compiled_policy::text,
		daily_cap, cooldown_seconds, enabled, suspended_reason, last_fired_at
		from magento_auto_rule where org_id = $1 order by updated_at desc`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r ruleRow
		var rawPolicy []byte
		if err := rows.Scan(&r.id, &r.name, &r.instruction, &r.domain, &r.actionKind, &rawPolicy,
			&r.dailyCap, &r.cooldownSeconds, &r.enabled, &r.suspendedReason, &r.lastFiredAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.compiledPolicy, _ = jsParse(rawPolicy)
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type csRow struct {
		id                string
		domain            string
		operationID       string
		riskClass         int
		status            string
		summary           string
		bulkUUID          *string
		projectedExposure *string
		inverseOfID       *string
		scope             any
		capSnapshot       any
		createdAt         time.Time
		reconciledAt      *time.Time
	}
	var changesets []csRow
	var csIDs []string
	rows, err = a.DB.Query(ctx, `select id::text, domain, operation_id, risk_class, status, summary,
		bulk_uuid, projected_exposure::text, inverse_of_id::text, scope::text, cap_snapshot::text, created_at, reconciled_at
		from action_changeset where org_id = $1 order by created_at desc limit 20`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r csRow
		var rawScope, rawSnap []byte
		if err := rows.Scan(&r.id, &r.domain, &r.operationID, &r.riskClass, &r.status, &r.summary,
			&r.bulkUUID, &r.projectedExposure, &r.inverseOfID, &rawScope, &rawSnap, &r.createdAt, &r.reconciledAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.scope, _ = jsParse(rawScope)
		r.capSnapshot, _ = jsParse(rawSnap)
		changesets = append(changesets, r)
		csIDs = append(csIDs, r.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rowsByChangeset := map[string][]packs.MagentoActivityRow{}
	if len(csIDs) > 0 {
		rows, err = a.DB.Query(ctx, `select changeset_id::text, entity_ref, before_image::text, after_image::text
			from action_changeset_row where changeset_id = any($1::uuid[])`, csIDs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var csID, entityRef string
			var rawBefore, rawAfter []byte
			if err := rows.Scan(&csID, &entityRef, &rawBefore, &rawAfter); err != nil {
				rows.Close()
				return nil, err
			}
			var beforeVal, afterVal any
			_ = json.Unmarshal(rawBefore, &beforeVal)
			_ = json.Unmarshal(rawAfter, &afterVal)
			rowsByChangeset[csID] = append(rowsByChangeset[csID], packs.MagentoActivityRow{
				EntityRef:   entityRef,
				BeforeImage: beforeVal,
				AfterImage:  afterVal,
			})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	type handoffRow struct {
		id          string
		kind        string
		entityRef   string
		status      string
		draft       any
		evidence    any
		draftMap    map[string]any
		evidenceMap map[string]any
		createdAt   time.Time
		completedAt *time.Time
	}
	var handoffs []handoffRow
	rows, err = a.DB.Query(ctx, `select id::text, kind, entity_ref, status, draft::text, evidence::text, created_at, completed_at
		from magento_financial_handoff where org_id = $1 order by created_at desc limit 20`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r handoffRow
		var rawDraft, rawEv []byte
		if err := rows.Scan(&r.id, &r.kind, &r.entityRef, &r.status, &rawDraft, &rawEv, &r.createdAt, &r.completedAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.draft, _ = jsParse(rawDraft)
		r.evidence, _ = jsParse(rawEv)
		_ = json.Unmarshal(rawDraft, &r.draftMap)
		_ = json.Unmarshal(rawEv, &r.evidenceMap)
		handoffs = append(handoffs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type defRow struct {
		definition *jsObj
		readiness  string
		reason     *string
	}
	var actionDefs []defRow
	rows, err = a.DB.Query(ctx, `select definition::text, readiness, readiness_reason
		from pack_action_definition where org_id = $1 and enabled = true`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r defRow
		var rawDef []byte
		if err := rows.Scan(&rawDef, &r.readiness, &r.reason); err != nil {
			rows.Close()
			return nil, err
		}
		parsed, _ := jsParse(rawDef)
		r.definition, _ = parsed.(*jsObj)
		if r.definition == nil {
			r.definition = &jsObj{vals: map[string]any{}}
		}
		actionDefs = append(actionDefs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type domainReadiness struct {
		readiness string
		reason    *string
	}
	readinessByDomain := map[string]domainReadiness{}
	for _, action := range actionDefs {
		adapter, ok := action.definition.vals["adapter"].(*jsObj)
		if !ok {
			continue
		}
		if adapter.vals["kind"] == "magento_financial_handoff" {
			continue
		}
		domain := jsStringOrEmpty(action.definition.vals["domain"])
		if _, exists := readinessByDomain[domain]; !exists || action.readiness == "blocked" {
			readinessByDomain[domain] = domainReadiness{readiness: action.readiness, reason: action.reason}
		}
	}

	operations := []any{}
	for _, action := range actionDefs {
		domain := jsStringOrEmpty(action.definition.vals["domain"])
		adapter, ok := action.definition.vals["adapter"].(*jsObj)
		if !ok {
			continue
		}
		declared, ok := adapter.vals["operations"].(*jsObj)
		if !ok {
			continue
		}
		for _, name := range declared.ordered() {
			op, ok := declared.vals[name].(*jsObj)
			if !ok {
				continue
			}
			defClass := packs.MagentoRiskClass(magentoNum(op.vals["defaultClass"]))
			resultMode := "sync"
			if op.vals["resultMode"] != nil {
				resultMode = jsStringOf(op.vals["resultMode"])
			}
			operations = append(operations, jsObjOf(
				"name", name,
				"domain", domain,
				"operationId", jsStringOrEmpty(op.vals["operationId"]),
				"executionMode", string(packs.MagentoExecutionModeFor(defClass)),
				"reversible", packTruthy(op.vals["reversible"]),
				"resultMode", resultMode,
			))
		}
	}

	controlsOut := make([]any, 0, len(controls))
	for _, c := range controls {
		dr, hasDR := readinessByDomain[c.domain]
		readiness := "blocked"
		var reason any = "change_access_unavailable"
		readinessMsg := "View-only access could not be checked because the reporting connection is unavailable."
		if hasDR {
			readiness = dr.readiness
			reason = nullablePtr(dr.reason)
			rStr := ""
			if dr.reason != nil {
				rStr = *dr.reason
			}
			readinessMsg = packs.OperatorReadinessDetail(rStr)
		}
		controlsOut = append(controlsOut, jsObjOf(
			"domain", c.domain,
			"automationEligible", c.riskClass == 2,
			"enabled", c.enabled,
			"autoExecute", c.autoExecute,
			"caps", c.caps,
			"scope", c.scope,
			"readiness", readiness,
			"readinessReason", reason,
			"readinessMessage", readinessMsg,
			"updatedAt", isoMillis(c.updatedAt),
		))
	}

	rulesOut := make([]any, 0, len(rules))
	for _, r := range rules {
		var policyMap any
		if o, ok := r.compiledPolicy.(*jsObj); ok {
			policyMap = o.vals
		}
		rulesOut = append(rulesOut, jsObjOf(
			"id", r.id,
			"name", r.name,
			"instruction", r.instruction,
			"domain", r.domain,
			"actionKind", r.actionKind,
			"compiledPolicy", r.compiledPolicy,
			"dailyCap", r.dailyCap,
			"cooldownSeconds", r.cooldownSeconds,
			"enabled", r.enabled,
			"suspendedReason", nullablePtr(r.suspendedReason),
			"lastFiredAt", timeOrNull(r.lastFiredAt),
			"isTest", packs.IsMagentoTestRule(r.name, policyMap),
		))
	}

	changesetsOut := make([]any, 0, len(changesets))
	activityChangesets := make([]packs.MagentoActivityChangeset, 0, len(changesets))
	for _, cs := range changesets {
		mode := string(packs.MagentoExecutionModeFor(packs.MagentoRiskClass(cs.riskClass)))
		createdISO := isoMillis(cs.createdAt)
		var reconciledISO *string
		if cs.reconciledAt != nil {
			s := isoMillis(*cs.reconciledAt)
			reconciledISO = &s
		}
		changesetsOut = append(changesetsOut, jsObjOf(
			"id", cs.id,
			"domain", cs.domain,
			"operationId", cs.operationID,
			"status", cs.status,
			"summary", cs.summary,
			"bulkUuid", nullablePtr(cs.bulkUUID),
			"projectedExposure", nullablePtr(cs.projectedExposure),
			"inverseOfId", nullablePtr(cs.inverseOfID),
			"scope", cs.scope,
			"capSnapshot", cs.capSnapshot,
			"createdAt", createdISO,
			"reconciledAt", nullablePtr(reconciledISO),
			"executionMode", mode,
		))
		var scopeMap, snapMap map[string]any
		if o, ok := cs.scope.(*jsObj); ok {
			scopeMap = o.vals
		}
		if o, ok := cs.capSnapshot.(*jsObj); ok {
			snapMap = o.vals
		}
		activityChangesets = append(activityChangesets, packs.MagentoActivityChangeset{
			ID:            cs.id,
			Domain:        cs.domain,
			OperationID:   cs.operationID,
			ExecutionMode: mode,
			Status:        cs.status,
			Summary:       cs.summary,
			BulkUUID:      cs.bulkUUID,
			InverseOfID:   cs.inverseOfID,
			Scope:         scopeMap,
			CapSnapshot:   snapMap,
			CreatedAt:     createdISO,
			ReconciledAt:  reconciledISO,
			Rows:          rowsByChangeset[cs.id],
		})
	}

	handoffsOut := make([]any, 0, len(handoffs))
	activityHandoffs := make([]packs.MagentoActivityHandoff, 0, len(handoffs))
	for _, h := range handoffs {
		createdISO := isoMillis(h.createdAt)
		var completedISO *string
		if h.completedAt != nil {
			s := isoMillis(*h.completedAt)
			completedISO = &s
		}
		handoffsOut = append(handoffsOut, jsObjOf(
			"id", h.id,
			"kind", h.kind,
			"entityRef", h.entityRef,
			"status", h.status,
			"draft", h.draft,
			"evidence", h.evidence,
			"createdAt", createdISO,
			"completedAt", nullablePtr(completedISO),
		))
		activityHandoffs = append(activityHandoffs, packs.MagentoActivityHandoff{
			ID:          h.id,
			Kind:        h.kind,
			EntityRef:   h.entityRef,
			Status:      h.status,
			Draft:       h.draftMap,
			Evidence:    h.evidenceMap,
			CreatedAt:   createdISO,
			CompletedAt: completedISO,
		})
	}

	builtActivity := packs.BuildMagentoActivity(activityChangesets, activityHandoffs)
	rawActivity, _ := json.Marshal(builtActivity)
	parsedActivity, _ := jsParse(rawActivity)

	return jsObjOf(
		"controls", controlsOut,
		"rules", rulesOut,
		"changesets", changesetsOut,
		"handoffs", handoffsOut,
		"activity", parsedActivity,
		"operations", operations,
		"handoffOnly", jsObjOf(
			"executePath", false,
			"handoffKinds", []any{
				"online_refund",
				"return_approval",
				"financial_configuration",
				"store_credit_over_cap",
			},
		),
	), nil
}

func (a *API) updateMagentoStoreManagement(ctx context.Context, orgID string, input *jsObj) (*jsObj, error) {
	action := jsStringOrEmpty(input.vals["action"])
	var actorUserID *string
	if s, ok := input.vals["actorUserId"].(string); ok {
		actorUserID = &s
	}

	switch action {
	case "update_domain":
		domain := jsStringOrEmpty(input.vals["domain"])
		if !slices.Contains([]string{"catalog", "inventory", "orders", "promotions", "content", "customers"}, domain) {
			return nil, errors.New("unknown Magento change domain")
		}
		var riskClass int
		var enabled, autoExecute bool
		var rawCaps []byte
		err := a.DB.QueryRow(ctx, `select risk_class, enabled, auto_execute, caps::text
			from magento_store_control where org_id = $1 and domain = $2 limit 1`, orgID, domain).
			Scan(&riskClass, &enabled, &autoExecute, &rawCaps)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("Magento %s control is not installed", domain)
		}
		if err != nil {
			return nil, err
		}
		if b, ok := input.vals["enabled"].(bool); ok {
			enabled = b
		}
		if b, ok := input.vals["autoExecute"].(bool); ok {
			if b && riskClass != 2 {
				return nil, errors.New("Automatic execution is not available for this Magento domain")
			}
			autoExecute = b
		}
		capsVal, _ := jsParse(rawCaps)
		capsObj := jsSpread(capsVal)
		if inCaps, exists := input.vals["caps"]; exists && inCaps != nil {
			inCapsObj, ok := inCaps.(*jsObj)
			if !ok {
				return nil, errors.New("Magento caps must be an object")
			}
			for _, key := range inCapsObj.ordered() {
				if !allowedMagentoCapKeys[key] {
					return nil, fmt.Errorf("unknown Magento cap %s", key)
				}
				num := magentoNum(inCapsObj.vals[key])
				if math.IsNaN(num) || math.IsInf(num, 0) || num < 0 {
					return nil, fmt.Errorf("Magento cap %s must be non-negative", key)
				}
				capsObj.set(key, num)
			}
		}
		if _, err := a.DB.Exec(ctx, `update magento_store_control
			set enabled = $3, auto_execute = $4, caps = $5::jsonb, updated_by_user_id = $6, updated_at = now()
			where org_id = $1 and domain = $2`,
			orgID, domain, enabled, autoExecute, jsStringify(capsObj, false), actorUserID); err != nil {
			return nil, err
		}

	case "create_rule":
		var name, instruction, actionKind string
		if s, ok := input.vals["name"].(string); ok {
			name = jsTrim(s)
		}
		if s, ok := input.vals["instruction"].(string); ok {
			instruction = jsTrim(s)
		}
		domain := jsStringOrEmpty(input.vals["domain"])
		if s, ok := input.vals["actionKind"].(string); ok {
			actionKind = jsTrim(s)
		}
		policySource := "admin_plain_language"
		if input.vals["source"] == "acceptance_test" {
			policySource = "acceptance_test"
		}
		dailyCap := magentoNum(input.vals["dailyCap"])
		cooldownRaw := any(float64(0))
		if v, exists := input.vals["cooldownSeconds"]; exists && v != nil {
			cooldownRaw = v
		}
		cooldownSeconds := magentoNum(cooldownRaw)
		if name == "" || utf16Len(name) > 120 || instruction == "" || utf16Len(instruction) > 1000 {
			return nil, errors.New("Magento automatic rule needs a concise name and instruction")
		}
		if !isSafeInt(dailyCap) || dailyCap < 1 || !isSafeInt(cooldownSeconds) || cooldownSeconds < 0 {
			return nil, errors.New("Magento automatic rule caps are invalid")
		}
		var ctrlEnabled, ctrlAuto bool
		var ctrlRisk int
		var rawCaps []byte
		err := a.DB.QueryRow(ctx, `select enabled, auto_execute, risk_class, caps::text
			from magento_store_control where org_id = $1 and domain = $2 limit 1`, orgID, domain).
			Scan(&ctrlEnabled, &ctrlAuto, &ctrlRisk, &rawCaps)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!ctrlEnabled || !ctrlAuto || ctrlRisk != 2)) {
			return nil, fmt.Errorf("Magento %s automatic execution is not enabled", domain)
		}
		if err != nil {
			return nil, err
		}
		var rawDef []byte
		err = a.DB.QueryRow(ctx, `select definition::text from pack_action_definition
			where org_id = $1 and kind = $2 and enabled = true limit 1`, orgID, actionKind).Scan(&rawDef)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("Magento automatic rule action does not belong to this domain")
		}
		if err != nil {
			return nil, err
		}
		defVal, _ := jsParse(rawDef)
		defObj, _ := defVal.(*jsObj)
		if defObj == nil || jsStringOrEmpty(defObj.vals["domain"]) != domain {
			return nil, errors.New("Magento automatic rule action does not belong to this domain")
		}
		capsVal, _ := jsParse(rawCaps)
		var controlDailyCap float64
		if capsObj, ok := capsVal.(*jsObj); ok && capsObj.vals["maxDailyAutoActions"] != nil {
			controlDailyCap = magentoNum(capsObj.vals["maxDailyAutoActions"])
		}
		if controlDailyCap > 0 && dailyCap > controlDailyCap {
			return nil, fmt.Errorf("Rule daily cap exceeds the domain ceiling of %s", jsNumberText(controlDailyCap))
		}
		compiledPolicy := jsObjOf(
			"version", 1,
			"source", policySource,
			"condition", "watcher_finding",
			"dailyCap", int(dailyCap),
			"cooldownSeconds", int(cooldownSeconds),
		)
		ruleEnabled := packTruthy(input.vals["enabled"])
		if _, err := a.DB.Exec(ctx, `insert into magento_auto_rule
			(org_id, name, instruction, domain, action_kind, compiled_policy, daily_cap, cooldown_seconds, enabled, created_by_user_id)
			values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10)
			on conflict (org_id, name) do update set
				instruction = excluded.instruction,
				domain = excluded.domain,
				action_kind = excluded.action_kind,
				compiled_policy = excluded.compiled_policy,
				daily_cap = excluded.daily_cap,
				cooldown_seconds = excluded.cooldown_seconds,
				enabled = excluded.enabled,
				suspended_reason = null,
				updated_at = now()`,
			orgID, name, instruction, domain, actionKind, jsStringify(compiledPolicy, false),
			int(dailyCap), int(cooldownSeconds), ruleEnabled, actorUserID); err != nil {
			return nil, err
		}

	case "set_rule_status":
		ruleID, _ := input.vals["ruleId"].(string)
		ruleEnabled, okEnabled := input.vals["enabled"].(bool)
		if ruleID == "" || !okEnabled {
			return nil, errors.New("Magento rule status needs ruleId and enabled")
		}
		var suspended *string
		if !ruleEnabled {
			s := "suspended_by_admin"
			suspended = &s
		}
		if _, err := a.DB.Exec(ctx, `update magento_auto_rule
			set enabled = $3, suspended_reason = $4, updated_at = now()
			where id::text = $1 and org_id = $2`, ruleID, orgID, ruleEnabled, suspended); err != nil {
			return nil, err
		}

	default:
		return nil, errors.New("unknown Magento store-management action")
	}

	return a.readMagentoStoreManagement(ctx, orgID)
}

func nullablePtr[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func timeOrNull(t *time.Time) any {
	if t == nil {
		return nil
	}
	return isoMillis(*t)
}

func isSafeInt(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && math.Trunc(f) == f && math.Abs(f) <= 9007199254740991
}

func magentoNum(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case int:
		return float64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		s := jsTrim(t)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}
