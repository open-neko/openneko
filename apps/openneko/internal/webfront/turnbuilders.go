package webfront

import (
	"context"
	"regexp"
	"strings"
)

type workflowListIn struct {
	OrgID string  `json:"orgId"`
	Limit int     `json:"limit"`
	RunID *string `json:"runId"`
}

type workflowDeleteIn struct {
	OrgID      string  `json:"orgId"`
	WorkflowID string  `json:"workflowId"`
	RunID      *string `json:"runId"`
}

type policyListIn struct {
	OrgID string `json:"orgId"`
	Limit int    `json:"limit"`
}

var (
	policyModes    = []string{"observe_only", "draft_only", "auto_approve", "approval_required", "never"}
	watchOps       = []string{"gt", "gte", "lt", "lte", "eq", "ne", "changed"}
	skillSlug      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	newlinePattern = regexp.MustCompile(`[\r\n]`)
)

const (
	scriptStepDescription = "Makes this a deterministic script step. It runs before the agent turn, from the run directory, " +
		"without a shell. Arguments may use ${SKILL_DIR}, ${RUN_DIR}, ${ARTIFACT_DIR}, and " +
		"${input.<field>} or ${input.<field>:-default} from the trigger payload. The script " +
		"queries data with neko_data or neko-query."
	saveWorkflowHint = "The workflow was saved but the data trigger was not wired. Fix the trigger and call create_workflow again with the same name."
)

func zOptional(name string, typ zType) zField { return zField{name: name, typ: typ, optional: true} }

func zRequired(name string, typ zType) zField { return zField{name: name, typ: typ} }

func zMax(max float64) *zStr { return &zStr{max: num(max)} }

func zTexts(min, max float64) *zArr {
	return &zArr{elem: zText(1, 120), min: num(min), max: num(max)}
}

func zIntRange(min, max float64) *zInt { return &zInt{min: num(min), max: num(max)} }

// workflowSaveShape is WORKFLOW_SAVE_SCHEMA in packages/llm/src/workflows/fence-schemas.ts.
var workflowSaveShape = &zObj{fields: []zField{
	zRequired("name", zText(1, 120)),
	zOptional("description", zMax(2000)),
	zOptional("goal", zMax(2000)),
	zOptional("systemPromptOverlay", zMax(16000)),
	zRequired("steps", &zArr{min: num(1), max: num(40), elem: &zObj{fields: []zField{
		zRequired("id", zText(1, 60)),
		zRequired("description", zText(1, 2000)),
		zOptional("script", &zObj{desc: scriptStepDescription, fields: []zField{
			zRequired("skill", &zStr{trim: true, max: num(64), pattern: skillSlug}),
			zRequired("command", &zArr{elem: zMax(2000), min: num(1), max: num(64)}),
			zOptional("timeoutSeconds", zIntRange(1, 3600)),
		}}),
	}}}),
	zOptional("batch", zNullable{&zObj{fields: []zField{
		zOptional("recordsField", &zStr{trim: true, pattern: recordsFieldPattern}),
		zRequired("columns", &zArr{min: num(1), max: num(64), elem: &zObj{fields: []zField{
			zRequired("name", &zStr{trim: true, min: num(1), max: num(80), refine: func(s string) bool { return !newlinePattern.MatchString(s) }}),
			zRequired("path", zText(1, 256)),
			zOptional("default", zUnion{&zStr{}, zPrim("number"), zPrim("boolean"), zPrim("null")}),
		}}}),
	}}}),
	zOptional("triggers", &zObj{fields: []zField{
		zOptional("cron", zText(1, 120)),
		zOptional("timezone", zText(1, 80)),
		zOptional("enabled", zPrim("boolean")),
		zOptional("when", &zObj{fields: []zField{
			zRequired("table", zText(1, 120)),
			zOptional("where", &zRecord{key: &zStr{}, val: zAny{}}),
			zOptional("select", &zArr{elem: zText(1, 120)}),
			zRequired("primary_key", zTexts(1, 8)),
			zOptional("version_column", zText(1, 120)),
			zOptional("enabled", zPrim("boolean")),
			zOptional("idempotency_key_template", zMax(200)),
			zOptional("acknowledge_mutation_loop", zPrim("boolean")),
		}}),
		zOptional("watch", &zObj{fields: []zField{
			zRequired("query", &zStr{trim: true, min: num(3), max: num(8000),
				descLast: "The exact GraphQL query already executed successfully through GraphJin. Copy it without rewriting it."}),
			zRequired("value_path", &zStr{trim: true, min: num(1), max: num(400),
				descLast: "Dot-separated path under the successful GraphJin response data. Include numeric array segments, for example orders.0.count; bracket notation is not supported."}),
			zRequired("op", zEnum("", watchOps...)),
			zOptional("threshold", zUnion{zPrim("number"), &zStr{}}),
			zOptional("cadence_seconds", zIntRange(60, 86400)),
			{name: "debounce_seconds", typ: zIntRange(0, 604800), optional: true,
				desc: "Quiet period in seconds after the watch fires; further matches inside it stay silent. Default 3600. Use 0 when the operator wants to hear about every change."},
			zOptional("severity", zEnum("", riskLevels...)),
		}}),
	}}),
}}

// policySaveShape is POLICY_SAVE_SCHEMA.
var policySaveShape = &zObj{fields: []zField{
	zRequired("name", zText(1, 120)),
	zOptional("description", zMax(2000)),
	zRequired("applies_to_kinds", zTexts(0, 40)),
	{name: "applies_to_scopes", typ: &zArr{elem: zEnum("", "internal", "external"), min: num(0), max: num(8)},
		def: func() any { return []any{"external"} }},
	zRequired("mode", zEnum("", policyModes...)),
	zOptional("risk_threshold_auto_approve", zEnum("", riskLevels...)),
	{name: "allowed_targets", typ: &zRecord{key: &zStr{}, val: zAny{}}, optional: true,
		desc: `{"patterns": [...], "on_miss"?: "next"}. Every target must match a pattern. A pattern is exact ("acme.com"), a prefix ("https://hooks.acme.com/*"), or subdomains only ("*.acme.com"). Email actions match lowercase recipient domains. Without on_miss, a miss is denied; with "next", a miss falls to the next rule, usually one that asks.`},
	{name: "denied_targets", typ: &zRecord{key: &zStr{}, val: zAny{}}, optional: true,
		desc: `{"patterns": [...]}. Denies the action when any target matches.`},
	{name: "limits", typ: &zRecord{key: &zStr{}, val: zAny{}}, def: func() any { return &jsObj{} }},
	zOptional("approver_role", &zStr{trim: true, max: num(120)}),
	{name: "priority", typ: zIntRange(0, 10000), def: func() any { return 100.0 }},
	{name: "enabled", typ: zPrim("boolean"), def: func() any { return true }},
}}

var listLimitShape = &zObj{fields: []zField{zOptional("limit", zIntRange(1, 200))}}

func listLimit(args *jsObj) int {
	if limit := optionalInt(args, "limit"); limit != nil {
		return *limit
	}
	return 50
}

// confirmationCard is confirmationCard() in packages/llm/src/workflows/builder-cards.ts.
func confirmationCard(surfaceID, label, title, body string) any {
	return []any{
		jsObjOf("version", "v1.0", "createSurface", jsObjOf("surfaceId", surfaceID, "catalogId", "urn:app:catalog:briefing:v1")),
		jsObjOf("version", "v1.0", "updateComponents", jsObjOf("surfaceId", surfaceID, "components", []any{
			jsObjOf("id", "root", "component", "Confirmation", "label", label, "title", title, "children", []any{"body"}),
			jsObjOf("id", "body", "component", "Markdown", "text", body),
		})),
	}
}

func surfaceEvent(messages any) *jsObj { return jsObjOf("type", "surface", "messages", messages) }

// jsTemplate is `${v}` for a JSON value.
func jsTemplate(v any) string {
	if _, ok := v.(jsUndefined); ok {
		return "undefined"
	}
	switch x := v.(type) {
	case *jsObj:
		return "[object Object]"
	case float64:
		return jsNumberText(x)
	}
	return jsStringOf(v)
}

func jsOrText(v any, fallback string) string {
	if jsTruthy(v) {
		return jsTemplate(v)
	}
	return fallback
}

func jsJoin(list []any, sep string) string {
	parts := make([]string, len(list))
	for i, e := range list {
		if e != nil {
			parts[i] = jsTemplate(e)
		}
	}
	return strings.Join(parts, sep)
}

func jsLookup(v any, key string) any {
	if val, ok := jsField(v, key); ok {
		return val
	}
	return jsUndefined{}
}

func workflowSavedCard(workflow any, action any) any {
	verb := "Updated"
	if action == "created" {
		verb = "Created"
	}
	id, name := jsTemplate(jsLookup(workflow, "id")), jsTemplate(jsLookup(workflow, "name"))
	cron := ""
	if c := jsGet(workflow, "cron"); jsTruthy(c) {
		cron = " (cron `" + jsTemplate(c) + "` " + jsTemplate(jsLookup(workflow, "cronTimezone")) + ")"
	}
	steps := len(jsArray(jsGet(workflow, "steps")))
	return confirmationCard("workflow-save-"+id, verb+" workflow", name, strings.Join([]string{
		"**" + name + "** — " + jsNumberText(float64(steps)) + " step(s)" + cron + ".",
		"",
		jsOrText(jsGet(workflow, "description"), "_No description._"),
		"",
		"[Open detail](/workflows?id=" + id + ")",
	}, "\n"))
}

func workflowDeletedCard(id string, name any) any {
	title := jsTemplate(name)
	return confirmationCard("workflow-delete-"+id, "Deleted workflow", title, strings.Join([]string{
		"**" + title + "** is gone, along with its triggers, run history, and proposed actions.",
		"",
		"[Open workflows](/workflows)",
	}, "\n"))
}

func subscriptionSavedCard(subscription any, workflowName any) any {
	filter := jsGet(subscription, "filter")
	table := "?"
	if t, ok := jsGet(filter, "table").(string); ok {
		table = t
	}
	pkList := "?"
	if pk, ok := jsGet(filter, "primary_key").([]any); ok {
		pkList = jsJoin(pk, ", ")
	}
	whereSummary := "_(no filter)_"
	if where := jsGet(filter, "where"); jsTruthy(where) {
		var keys []any
		switch w := where.(type) {
		case *jsObj:
			for _, k := range w.ordered() {
				keys = append(keys, k)
			}
		case []any:
			for i := range w {
				keys = append(keys, jsNumberText(float64(i)))
			}
		case string:
			for i := range utf16Len(w) {
				keys = append(keys, jsNumberText(float64(i)))
			}
		}
		if len(keys) > 0 {
			whereSummary = "`" + jsJoin(keys, "`, `") + "`"
		}
	}
	name := jsTemplate(workflowName)
	lines := []string{
		"**" + name + "** will fire on changes to `" + table + "` (pk: `" + pkList + "`).",
		"",
		"**Filter columns:** " + whereSummary,
	}
	if template := jsGet(subscription, "idempotencyKeyTemplate"); jsTruthy(template) {
		lines = append(lines, "**Idempotency key:** `"+jsTemplate(template)+"`")
	}
	lines = append(lines, "", "[Open detail](/workflows?id="+jsTemplate(jsLookup(subscription, "workflowId"))+")")
	return confirmationCard("subscription-save-"+jsTemplate(jsLookup(subscription, "id")), "Trigger added", name, strings.Join(lines, "\n"))
}

func policySavedCard(policy any, action any) any {
	verb := "Updated"
	if action == "created" {
		verb = "Created"
	}
	scopes := jsJoin(jsArray(jsGet(policy, "appliesToScopes")), ", ")
	if scopes == "" {
		scopes = "—"
	}
	kinds := "_(all kinds)_"
	if list := jsArray(jsGet(policy, "appliesToKinds")); len(list) > 0 {
		quoted := make([]any, len(list))
		for i, k := range list {
			quoted[i] = "`" + jsTemplate(k) + "`"
		}
		kinds = jsJoin(quoted, ", ")
	}
	id, name := jsTemplate(jsLookup(policy, "id")), jsTemplate(jsLookup(policy, "name"))
	return confirmationCard("policy-save-"+id, verb+" rule", name, strings.Join([]string{
		"**Mode:** `" + jsTemplate(jsLookup(policy, "mode")) + "` • **Scopes:** " + scopes + " • **Priority:** " + jsTemplate(jsLookup(policy, "priority")),
		"",
		"**Kinds:** " + kinds,
		"",
		jsOrText(jsGet(policy, "description"), "_No description._"),
		"",
		"[Open detail](/admin/rules/" + id + ")",
	}, "\n"))
}

func copyPresent(out *jsObj, from *jsObj, keys ...string) {
	for _, k := range keys {
		if v, ok := from.vals[k]; ok {
			out.set(k, v)
		}
	}
}

// workflowBuilderTools is buildWorkflowBuilderServer().
func workflowBuilderTools(b turnBinding, be toolBackend) []toolDef {
	return []toolDef{
		{server: "neko_workflow_builder", name: "create_workflow", shape: workflowSaveShape,
			description: joinWords("Create or update a workflow. Upserts by name within the org — if a",
				"workflow with the same name already exists, it is updated in place.",
				"Use this when the operator asks to set up, modify, or rename a task or",
				"pipeline. Pass `triggers.cron` to run it on a schedule, or",
				"pass `triggers.watch` for a periodic scalar or aggregate condition.",
				"For a watch, first execute the GraphJin query successfully, then copy",
				"that exact query and derive `value_path` from its response data using",
				"dot-separated segments (including numeric array indexes such as `.0.`).",
				"Do not use bracket notation. `triggers.when` is instead for matching",
				"changed source rows and requires table/primary-key semantics.",
				"pass `batch` to define the workflow's own record field and CSV output",
				"columns for batch API execution. Batch support belongs to the workflow",
				"and does not require a skill (a workflow may use zero, one, or many).",
				"Pass `triggers.when` to fire it when a row in the operator's data source",
				"matches a filter (e.g. 'when stock dips below reorder point') — for the",
				"latter, introspect the schema first with the GraphJin MCP",
				"(`list_tables`, `describe_table`) to confirm the table, columns, and",
				"primary key. After saving, narrate the change in a sentence; the tool",
				"also emits a confirmation card the operator can click."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				in := jsObjOf("orgId", b.OrgID)
				copyPresent(in, args, "name", "description", "goal", "systemPromptOverlay", "steps", "batch", "triggers")
				setNonNil(in, "createdByThreadId", b.threadID())
				setNonNil(in, "createdByRunId", b.runID())
				be.audit(ctx, "/v1/workflow/save")
				result, err := be.saveWorkflow(ctx, in)
				if err != nil {
					return toolResult{}, err
				}
				workflow, subscription := jsGet(result, "workflow"), jsGet(result, "subscription")
				if err := be.emitEvent(ctx, surfaceEvent(workflowSavedCard(workflow, jsGet(result, "action")))); err != nil {
					return toolResult{}, err
				}
				if jsTruthy(subscription) {
					if err := be.emitEvent(ctx, surfaceEvent(subscriptionSavedCard(subscription, jsLookup(workflow, "name")))); err != nil {
						return toolResult{}, err
					}
				}
				triggerError, _ := jsField(result, "triggerError")
				out := jsObjOf("ok", !jsTruthy(triggerError))
				setPresent(out, "action", result, "action")
				setPresent(out, "workflowId", workflow, "id")
				setPresent(out, "name", workflow, "name")
				if jsTruthy(subscription) {
					setPresent(out, "triggerId", subscription, "id")
				}
				if jsTruthy(triggerError) {
					out.set("triggerError", triggerError)
					out.set("hint", saveWorkflowHint)
					return toolResult{text: jsStringify(out, false), isError: true}, nil
				}
				return toolText(out), nil
			}},
		{server: "neko_workflow_builder", name: "list_workflows", shape: listLimitShape,
			description: joinWords("List the workflows defined in this org so you can answer questions",
				"like 'what was the workflow we set up last week to summarize sales' or",
				"look up the exact name/steps of a workflow before updating it via",
				"`create_workflow`. Returns full bodies (steps, cron, data trigger,",
				"description, system prompt overlay), ordered by most recently updated."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				limit := listLimit(args)
				be.audit(ctx, "/v1/workflow/list")
				result, err := traceWorkflowList(b.trace(), &limit, func() (any, error) {
					return be.listWorkflows(ctx, workflowListIn{OrgID: b.OrgID, Limit: limit, RunID: b.runID()})
				})
				if err != nil {
					return toolResult{}, err
				}
				rows := jsArray(jsGet(result, "workflows"))
				workflows := make([]any, len(rows))
				for i, w := range rows {
					row := &jsObj{}
					for _, k := range []string{"id", "name", "description", "enabled", "status", "goal", "systemPromptOverlay", "steps"} {
						setPresent(row, k, w, k)
					}
					row.set("batch", listedBatch(jsGet(w, "outputContract")))
					for _, k := range []string{"cron", "cronTimezone", "cronEnabled", "when", "updatedAt", "createdAt"} {
						setPresent(row, k, w, k)
					}
					workflows[i] = row
				}
				out := jsObjOf("ok", true)
				setPresent(out, "total", result, "total")
				out.set("returned", float64(len(rows)))
				out.set("workflows", workflows)
				return toolText(out), nil
			}},
		{server: "neko_workflow_builder", name: "delete_workflow",
			shape: &zObj{fields: []zField{zRequired("workflowId", &zStr{min: num(1), descLast: "The id of the workflow to delete (from list_workflows)."})}},
			description: joinWords("Permanently delete a workflow by its id. This also drops its triggers,",
				"run history, outputs, and proposed/executed actions (hard cascade — no",
				"undo). Because it is destructive, you MUST confirm with the operator in",
				"the thread BEFORE calling this — name the workflow you're about to",
				"remove and wait for an explicit yes. Resolve the id first with",
				"`list_workflows` (match on the name the operator referenced or the id",
				"carried by an @mention); never guess. On success the tool emits a",
				"confirmation card."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				id := stringArg(args, "workflowId")
				be.audit(ctx, "/v1/workflow/delete")
				result, err := be.deleteWorkflow(ctx, workflowDeleteIn{OrgID: b.OrgID, WorkflowID: id, RunID: b.runID()})
				if err != nil {
					return toolResult{}, err
				}
				if !jsTruthy(jsGet(result, "found")) {
					return toolResult{isError: true, text: jsStringify(jsObjOf("ok", false, "error", "not_found",
						"hint", "No workflow with that id in this org. Call list_workflows to get the current ids."), false)}, nil
				}
				name := jsLookup(result, "name")
				if jsTruthy(name) {
					if err := be.emitEvent(ctx, surfaceEvent(workflowDeletedCard(id, name))); err != nil {
						return toolResult{}, err
					}
				}
				deleted := jsObjOf("id", id)
				setPresent(deleted, "name", result, "name")
				return toolText(jsObjOf("ok", true, "deleted", deleted)), nil
			}},
	}
}

// listedBatch is the batch field of list_workflows: the recordsField and
// columns of parseCompiledWorkflowBatchContract(), or null.
func listedBatch(outputContract any) any {
	recordsField, columns, ok := compiledBatch([]byte(jsStringify(outputContract, false)))
	if !ok {
		return nil
	}
	list := make([]any, len(columns))
	for i, c := range columns {
		col := jsObjOf("name", c.Name, "path", c.Path)
		if c.HasDefault {
			col.set("default", c.Default)
		}
		list[i] = col
	}
	return jsObjOf("recordsField", recordsField, "columns", list)
}

// ruleBuilderTools is buildRuleBuilderServer().
func ruleBuilderTools(b turnBinding, be toolBackend) []toolDef {
	return []toolDef{
		{server: "neko_rule_builder", name: "save_rule", shape: policySaveShape,
			description: joinWords("Create or update an approval rule. Upserts by name within the org —",
				"if a rule with the same name already exists, it is updated in place.",
				"Use this when the operator asks to gate, allow, or deny specific action",
				"kinds (e.g. 'auto-approve low-risk slack posts', 'always ask before",
				"sending email externally'). To send without approval only to approved",
				"recipients, use allowed_targets with on_miss 'next' so any other",
				"recipient falls to the rule that asks. After saving, the tool emits a",
				"confirmation card the operator can click to open the rule."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				opt := func(name string) any { return jsOr(args.vals[name], true, nil) }
				description := args.vals["description"]
				if description == nil {
					description = ""
				}
				var role any
				if args.vals["approver_role"] == "admin" {
					role = "admin"
				}
				in := jsObjOf("orgId", b.OrgID, "name", args.vals["name"], "description", description,
					"appliesToKinds", args.vals["applies_to_kinds"], "appliesToScopes", args.vals["applies_to_scopes"],
					"mode", args.vals["mode"], "riskThresholdAutoApprove", opt("risk_threshold_auto_approve"),
					"allowedTargets", opt("allowed_targets"), "deniedTargets", opt("denied_targets"), "limits", args.vals["limits"],
					"approverRole", role, "priority", args.vals["priority"], "enabled", args.vals["enabled"])
				setNonNil(in, "createdByThreadId", b.threadID())
				setNonNil(in, "createdByRunId", b.runID())
				be.audit(ctx, "/v1/rule/save")
				result, err := be.upsertPolicy(ctx, in)
				if err != nil {
					return toolResult{}, err
				}
				policy := jsGet(result, "policy")
				if err := be.emitEvent(ctx, surfaceEvent(policySavedCard(policy, jsGet(result, "action")))); err != nil {
					return toolResult{}, err
				}
				out := jsObjOf("ok", true)
				setPresent(out, "action", result, "action")
				setPresent(out, "ruleId", policy, "id")
				setPresent(out, "name", policy, "name")
				return toolText(out), nil
			}},
		{server: "neko_rule_builder", name: "list_rules", shape: listLimitShape,
			description: joinWords("List the approval rules defined in this org so you can answer questions",
				"like 'what was that rule we set last week to auto-approve slack posts'",
				"or look up the exact name/config of a rule before updating it via",
				"`save_rule`. Returns full bodies (mode, scopes, kinds, limits), ordered",
				"by priority then name. Includes disabled rules."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				be.audit(ctx, "/v1/rule/list")
				result, err := be.listPolicies(ctx, policyListIn{OrgID: b.OrgID, Limit: listLimit(args)})
				if err != nil {
					return toolResult{}, err
				}
				rows := jsArray(jsGet(result, "policies"))
				rules := make([]any, len(rows))
				for i, p := range rows {
					row := &jsObj{}
					for _, k := range []string{"id", "name", "description", "mode", "appliesToKinds", "appliesToScopes",
						"riskThresholdAutoApprove", "allowedTargets", "deniedTargets", "limits", "approverGroupId", "priority",
						"enabled", "updatedAt", "createdAt"} {
						setPresent(row, k, p, k)
					}
					rules[i] = row
				}
				out := jsObjOf("ok", true)
				setPresent(out, "total", result, "total")
				out.set("returned", float64(len(rows)))
				out.set("rules", rules)
				return toolText(out), nil
			}},
	}
}

func setNonNil(out *jsObj, key string, v *string) {
	if v != nil {
		out.set(key, *v)
	}
}
