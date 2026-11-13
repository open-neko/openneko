package webfront

import (
	"context"
	"math"
	"strings"
)

type policyIn struct {
	OrgID     string  `json:"orgId"`
	Scope     string  `json:"scope"`
	Kind      string  `json:"kind"`
	Target    *string `json:"target"`
	Payload   any     `json:"payload"`
	RiskLevel *string `json:"riskLevel"`
}

type actionRefIn struct {
	OrgID           string `json:"orgId"`
	ActionRequestID string `json:"actionRequestId"`
}

type actionWaitIn struct {
	OrgID           string `json:"orgId"`
	ActionRequestID string `json:"actionRequestId"`
	TimeoutMs       int    `json:"timeoutMs"`
}

type outputIn struct {
	OrgID               string  `json:"orgId"`
	WorkflowRunID       string  `json:"workflowRunId"`
	WorkRunID           string  `json:"workRunId"`
	Kind                string  `json:"kind"`
	Title               *string `json:"title,omitempty"`
	Body                *string `json:"body,omitempty"`
	Payload             any     `json:"payload,omitempty"`
	ArtifactPath        *string `json:"artifactPath"`
	Scope               *string `json:"scope"`
	Topic               *string `json:"topic"`
	Mood                *string `json:"mood"`
	TimeWindowStart     *string `json:"timeWindowStart"`
	TimeWindowEnd       *string `json:"timeWindowEnd"`
	FreshnessTTLSeconds *int    `json:"freshnessTtlSeconds"`
}

// packActionDescriptor is PackActionDescriptor in packages/llm/src/work/tools.ts.
// DefaultMode is absent, a mode, or a map from scope to mode.
type packActionDescriptor struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Scope       string `json:"scope,omitempty"`
	DefaultMode any    `json:"default_mode,omitempty"`
	Example     any    `json:"example,omitempty"`
}

func (d packActionDescriptor) modeFor(scope string) string {
	switch m := d.DefaultMode.(type) {
	case string:
		return m
	case map[string]any:
		s, _ := m[scope].(string)
		return s
	}
	return ""
}

func (d packActionDescriptor) deniedEverywhere() bool {
	switch m := d.DefaultMode.(type) {
	case string:
		return m == "deny"
	case map[string]any:
		if len(m) == 0 {
			return false
		}
		for _, v := range m {
			if v != "deny" {
				return false
			}
		}
		return true
	}
	return false
}

func (d packActionDescriptor) needsIntent() bool {
	switch m := d.DefaultMode.(type) {
	case string:
		return m == "ask"
	case map[string]any:
		for _, v := range m {
			if v == "ask" {
				return true
			}
		}
	}
	return false
}

func (d packActionDescriptor) modeHint() string {
	switch m := d.DefaultMode.(type) {
	case string:
		if m == "auto" {
			return "Auto-approved by default — runs without user confirmation. The tool waits for execution; use its returned outcome to answer the user. Operators can override via /admin/rules."
		}
		return "Asks the user for approval before running. You MUST set `intent` to a clear one-sentence explanation."
	case map[string]any:
		var parts []string
		for _, scope := range []string{"external", "internal"} {
			if mode := d.modeFor(scope); mode != "" {
				parts = append(parts, scope+": "+mode)
			}
		}
		hint := strings.Join(parts, ", ")
		if d.needsIntent() {
			hint += " — set `intent` whenever the call might land in ask-mode."
		}
		return hint
	}
	return "Approval policy decided per-call."
}

var riskLevels = []string{"low", "medium", "high", "critical"}

var actionRequestShape = &zObj{fields: []zField{
	{name: "scope", typ: zEnum("", "internal", "external")},
	{name: "kind", typ: zText(1, 120)},
	{name: "target", typ: zText(1, 1024), optional: true},
	{name: "payload", typ: &zRecord{key: &zStr{}, val: zAny{}}, optional: true},
	{name: "risk_level", typ: zEnum("", riskLevels...), optional: true},
	{name: "summary", typ: zText(1, 2000)},
	{name: "minutes_saved", typ: &zInt{min: num(0), max: num(600)}, optional: true,
		desc: "Conservative estimate of the minutes a competent human would spend doing this one action by hand (e.g. send email ~8, file refund ~15). Omit if unsure."},
	{name: "basis", typ: &zStr{trim: true, max: num(160)}, optional: true, desc: "One short line naming why minutes_saved is what it is."},
}}

var outputKinds = []string{"report", "summary", "briefing_card_proposal", "chart", "table", "file", "message_draft", "finding", "observation", "recommendation"}

var workflowOutputShape = &zObj{fields: []zField{
	{name: "kind", typ: zEnum("", outputKinds...)},
	{name: "title", typ: &zStr{max: num(240)}, optional: true},
	{name: "body", typ: &zStr{max: num(64_000)}, optional: true},
	{name: "payload", typ: &zRecord{key: &zStr{}, val: zAny{}}, optional: true},
	{name: "artifactPath", typ: &zStr{max: num(1024)}, optional: true},
	{name: "scope", typ: &zStr{max: num(120)}, optional: true},
	{name: "topic", typ: &zStr{max: num(120)}, optional: true},
	{name: "mood", typ: zEnum("", "good", "watch", "act"), optional: true},
	{name: "timeWindowStart", typ: &zStr{datetime: true}, optional: true},
	{name: "timeWindowEnd", typ: &zStr{datetime: true}, optional: true},
	{name: "freshnessTtlSeconds", typ: &zInt{positive: true, max: num(31_536_000)}, optional: true},
}}

func setPresent(out *jsObj, key string, from any, fromKey string) {
	if v, ok := jsField(from, fromKey); ok {
		out.set(key, v)
	}
}

func optionalString(args *jsObj, name string) *string {
	if s, ok := args.vals[name].(string); ok {
		return &s
	}
	return nil
}

func jsOr(v any, present bool, fallback any) any {
	if !present || v == nil {
		return fallback
	}
	return v
}

// clampActionMinutes is clampActionMinutes() in packages/llm/src/workflows/value.ts.
func clampActionMinutes(v any, present bool) any {
	f, ok := v.(float64)
	if !present || !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	if f < 0 {
		return 0.0
	}
	return math.Min(math.Floor(f+0.5), 120)
}

// jsISODate is new Date(text).toISOString() for a text that zod's datetime
// check passed: seconds default to 0, and the fraction is cut to milliseconds.
func jsISODate(text string) string {
	body := strings.TrimSuffix(text, "Z")
	seconds, fraction := "00", ""
	if len(body) > 16 {
		rest := body[17:]
		seconds, fraction, _ = strings.Cut(rest, ".")
	}
	return body[:16] + ":" + seconds + "." + (fraction + "000")[:3] + "Z"
}

func (b turnBinding) evaluatePolicy(ctx context.Context, be toolBackend, in policyIn) (any, error) {
	be.audit(ctx, "/v1/policy/evaluate")
	return traceActionPolicy(b.trace(), in.Scope, in.Kind, in.Target, in.RiskLevel, func() (any, error) {
		return be.evaluatePolicy(ctx, in)
	})
}

func actionTools(b turnBinding, be toolBackend) []toolDef {
	return []toolDef{{server: "neko_action", name: "request",
		description: joinWords("Propose a state-changing operation. Workflows decide; actions mutate.",
			"Route every real-world or internal state change through this tool so",
			"policy can gate it and preserve an auditable approval path.",
			"Use scope 'external' for outbound mutations and 'internal' for",
			"policy-governed OpenNeko writes. Include the complete operation payload,",
			"a concise human-readable summary, and an honest risk estimate.",
			"A denial is final for this run; surface its reason instead of retrying."),
		shape: actionRequestShape,
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			scope, kind, summary := stringArg(args, "scope"), stringArg(args, "kind"), stringArg(args, "summary")
			payload, hasPayload := args.vals["payload"]
			risk, hasRisk := args.vals["risk_level"]
			target, hasTarget := args.vals["target"]
			decision, err := b.evaluatePolicy(ctx, be, policyIn{OrgID: b.OrgID, Scope: scope, Kind: kind,
				Target: optionalString(args, "target"), Payload: jsOr(payload, hasPayload, nil), RiskLevel: optionalString(args, "risk_level")})
			if err != nil {
				return toolResult{}, err
			}
			policy, _ := jsField(decision, "policy")
			switch d, _ := jsField(decision, "decision"); d {
			case "deny":
				out := jsObjOf("ok", false, "decision", "denied")
				setPresent(out, "reason", decision, "reason")
				setPresent(out, "policy", policy, "name")
				return toolText(out), nil
			case "no_policy":
				return toolText(jsObjOf("ok", false, "decision", "denied", "reason",
					"no policy matches this scope/kind — refuse for safety. Ask the operator to define a policy first.")), nil
			}
			status := "pending_approval"
			if d, _ := jsField(decision, "decision"); d == "allow" {
				status = "approved"
			}
			policyID, _ := jsField(policy, "id")
			basis, hasBasis := args.vals["basis"]
			minutes, hasMinutes := args.vals["minutes_saved"]
			in := jsObjOf("orgId", b.OrgID, "workflowRunId", b.WorkflowRunID, "workRunId", b.RunID,
				"requestedByRunId", b.WorkflowRunID, "triggeredByObservationId", nullIfEmpty(b.ObservationID),
				"policyId", policyID, "scope", scope, "kind", kind, "target", jsOr(target, hasTarget, nil),
				"payload", jsOr(payload, hasPayload, &jsObj{}), "riskLevel", jsOr(risk, hasRisk, nil), "summary", summary,
				"minutesSaved", clampActionMinutes(minutes, hasMinutes), "minutesSavedBasis", jsOr(basis, hasBasis, nil), "status", status)
			be.audit(ctx, "/v1/action/request")
			request, err := be.createActionRequest(ctx, in)
			if err != nil {
				return toolResult{}, err
			}
			requestID, _ := jsField(request, "id")
			requestStatus, _ := jsField(request, "status")
			event := jsObjOf("type", "action_request_emit", "action_request_id", requestID, "kind", kind, "scope", scope)
			if hasRisk {
				event.set("risk_level", risk)
			}
			emitted := "pending_approval"
			if requestStatus == "approved" {
				emitted = "auto_approved"
			}
			event.set("decision", emitted)
			event.set("summary", summary)
			if err := be.emitEvent(ctx, event); err != nil {
				return toolResult{}, err
			}
			if requestStatus == "approved" {
				be.audit(ctx, "/v1/action/enqueue")
				if err := be.enqueueAction(ctx, actionRefIn{OrgID: b.OrgID, ActionRequestID: jsStringOf(requestID)}); err != nil {
					return toolResult{}, err
				}
				out := jsObjOf("ok", true, "decision", "auto_approved", "status", "queued_for_execution", "actionRequestId", requestID)
				setPresent(out, "policy", policy, "name")
				return toolText(out), nil
			}
			out := jsObjOf("ok", true, "decision", "needs_approval", "status", "pending_approval", "actionRequestId", requestID)
			setPresent(out, "policy", policy, "name")
			setPresent(out, "reason", decision, "reason")
			return toolText(out), nil
		}}}
}

func outputTools(b turnBinding, be toolBackend) []toolDef {
	return []toolDef{{server: "neko_workflow_output", name: "emit",
		description: joinWords("Persist a workflow output — the thing this run produced.",
			"Use a descriptive kind and tag it with a scope, optional topic,",
			"and an honest mood ('good', 'watch', or 'act') so people and",
			"downstream workflows can discover it."),
		shape: workflowOutputShape,
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			in := outputIn{OrgID: b.OrgID, WorkflowRunID: b.WorkflowRunID, WorkRunID: b.RunID, Kind: stringArg(args, "kind"),
				Title: optionalString(args, "title"), Body: optionalString(args, "body"), ArtifactPath: optionalString(args, "artifactPath"),
				Scope: optionalString(args, "scope"), Topic: optionalString(args, "topic"), Mood: optionalString(args, "mood"),
				FreshnessTTLSeconds: optionalInt(args, "freshnessTtlSeconds")}
			if payload, ok := args.vals["payload"]; ok {
				in.Payload = payload
			}
			for name, field := range map[string]**string{"timeWindowStart": &in.TimeWindowStart, "timeWindowEnd": &in.TimeWindowEnd} {
				if text := optionalString(args, name); text != nil {
					iso := jsISODate(*text)
					*field = &iso
				}
			}
			be.audit(ctx, "/v1/workflow-output/emit")
			output, err := be.emitOutput(ctx, in)
			if err != nil {
				return toolResult{}, err
			}
			id, _ := jsField(output, "id")
			kind, _ := jsField(output, "kind")
			if err := be.emitEvent(ctx, jsObjOf("type", "output_emit", "output_id", id, "kind", kind)); err != nil {
				return toolResult{}, err
			}
			return toolText(jsObjOf("ok", true, "outputId", id, "kind", kind)), nil
		}}}
}

const (
	intentAskDescription      = "Single-sentence natural-language description of what you're about to do and why, written for the user reviewing the approval request. e.g. 'Send Sarah the Q2 revenue summary so she has it before tomorrow's board prep.'"
	intentOptionalDescription = "Optional one-sentence intent. Only surfaced if a runtime policy escalates this call to ask-mode."
	packTargetDescription     = "Canonical resource identifier this action targets (e.g. a channel id, a record id). Optional; some kinds don't need one."
	packRiskDescription       = "Best-effort blast-radius estimate for this specific invocation. Influences risk-threshold-based policy rules."
	packPayloadDescription    = "Action-specific payload. See the tool description for the expected shape. When a trusted discovery tool returns a payload for this action, preserve that complete object exactly unless the operator requested specific changes."
)

// packActionTools is buildPackActionServer(): one tool per pack action kind
// that is not denied in every scope.
func packActionTools(b turnBinding, be toolBackend) []toolDef {
	var tools []toolDef
	for _, d := range b.PackActions {
		if d.deniedEverywhere() {
			continue
		}
		scope := d.Scope
		if scope == "" {
			scope = "external"
		}
		intent := zField{name: "intent", typ: &zStr{min: num(3), max: num(500), descLast: intentAskDescription}}
		if !d.needsIntent() {
			intent = zField{name: "intent", typ: &zStr{min: num(3), max: num(500)}, optional: true, desc: intentOptionalDescription}
		}
		shape := &zObj{fields: []zField{intent,
			{name: "target", typ: &zStr{}, optional: true, desc: packTargetDescription},
			{name: "risk_level", typ: zEnum("", riskLevels...), optional: true, desc: packRiskDescription},
			{name: "payload", typ: &zRecord{key: &zStr{}, val: zAny{}}, def: func() any { return &jsObj{} }, desc: packPayloadDescription},
		}}
		tools = append(tools, toolDef{server: "neko_pack_actions", name: d.Kind, description: d.Description + "\n\n" + d.modeHint(), shape: shape,
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				return b.runPackAction(ctx, be, d.Kind, scope, args)
			}})
	}
	return tools
}

func (b turnBinding) runPackAction(ctx context.Context, be toolBackend, kind, scope string, args *jsObj) (toolResult, error) {
	var target, riskLevel any
	if t, ok := args.vals["target"].(string); ok && t != "" {
		target = t
	}
	risk := optionalString(args, "risk_level")
	if risk != nil {
		riskLevel = *risk
	}
	payload := jsOr(args.vals["payload"], true, &jsObj{})
	in := policyIn{OrgID: b.OrgID, Scope: scope, Kind: kind, Payload: payload, RiskLevel: risk}
	if target != nil {
		t := target.(string)
		in.Target = &t
	}
	decision, err := b.evaluatePolicy(ctx, be, in)
	if err != nil {
		return toolResult{}, err
	}
	policy, _ := jsField(decision, "policy")
	verdict, _ := jsField(decision, "decision")
	switch verdict {
	case "deny":
		out := jsObjOf("ok", false, "decision", "denied")
		setPresent(out, "reason", decision, "reason")
		setPresent(out, "policy", policy, "name")
		return toolText(out), nil
	case "no_policy":
		return toolText(jsObjOf("ok", false, "decision", "denied", "reason",
			"no policy matches this scope/kind — refuse for safety. Operator must define a policy in /admin/rules first.")), nil
	}
	intent := "Auto-fired " + kind + " (no explicit intent supplied)."
	if s, ok := args.vals["intent"].(string); ok && s != "" {
		intent = s
	}
	status := "pending_approval"
	if verdict == "allow" {
		status = "approved"
	}
	policyID, _ := jsField(policy, "id")
	be.audit(ctx, "/v1/action/request")
	request, err := be.createActionRequest(ctx, jsObjOf("orgId", b.OrgID, "scope", scope, "kind", kind, "target", target,
		"payload", payload, "riskLevel", riskLevel, "status", status, "policyId", policyID, "summary", intent, "intent", intent,
		"workRunId", b.RunID, "requestedByRunId", nil))
	if err != nil {
		return toolResult{}, err
	}
	requestID, _ := jsField(request, "id")
	event := jsObjOf("type", "action_request_emit", "action_request_id", requestID, "kind", kind, "scope", scope)
	if verdict == "allow" {
		event.set("decision", "auto_approved")
	} else {
		event.set("decision", "pending_approval")
		event.set("intent", intent)
	}
	event.set("summary", intent)
	if riskLevel != nil {
		event.set("risk_level", riskLevel)
	}
	if err := be.emitEvent(ctx, event); err != nil {
		return toolResult{}, err
	}
	if verdict != "allow" {
		out := jsObjOf("ok", true, "decision", "pending_approval", "action_request_id", requestID)
		setPresent(out, "policy", policy, "name")
		out.set("intent", intent)
		out.set("note", "User-approval gate. Tell the user briefly what you've requested and end your turn — the approval card is rendered inline; their decision will land in a follow-up turn.")
		return toolText(out), nil
	}
	ref := jsStringOf(requestID)
	be.audit(ctx, "/v1/action/enqueue")
	if err := be.enqueueAction(ctx, actionRefIn{OrgID: b.OrgID, ActionRequestID: ref}); err != nil {
		return toolResult{}, err
	}
	be.audit(ctx, "/v1/action/wait")
	execution, err := be.waitForAction(ctx, actionWaitIn{OrgID: b.OrgID, ActionRequestID: ref, TimeoutMs: 45_000})
	if err != nil {
		return toolResult{}, err
	}
	out := jsObjOf("ok", true, "decision", "auto_approved", "action_request_id", requestID)
	setPresent(out, "policy", policy, "name")
	switch s, _ := jsField(execution, "status"); s {
	case "succeeded":
		out.set("status", "executed")
		setPresent(out, "outcome", execution, "outcome")
		out.set("note", "Use the returned outcome to answer the user's request now.")
	case "failed":
		out.set("ok", false)
		out.set("status", "failed")
		setPresent(out, "error", execution, "error")
		out.set("note", "Tell the user the action failed. Do not claim that results are still queued.")
	default:
		out.set("status", "running")
		out.set("note", "Execution is still running. Do not claim completion or invent a result; tell the user it is taking longer than expected and the outcome will appear inline.")
	}
	return toolText(out), nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
