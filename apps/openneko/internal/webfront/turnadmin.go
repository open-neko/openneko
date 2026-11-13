package webfront

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

type orgIn struct {
	OrgID string `json:"orgId"`
}

type auditTrailIn struct {
	OrgID string  `json:"orgId"`
	RunID *string `json:"runId"`
	Limit *int    `json:"limit,omitempty"`
}

// MarshalJSON leaves out the payload key when noPayload is set, as
// proposeAdminAction() sends none.
func (p policyIn) MarshalJSON() ([]byte, error) {
	if p.noPayload {
		return json.Marshal(struct {
			OrgID     string  `json:"orgId"`
			Scope     string  `json:"scope"`
			Kind      string  `json:"kind"`
			Target    *string `json:"target"`
			RiskLevel *string `json:"riskLevel"`
		}{p.OrgID, p.Scope, p.Kind, p.Target, p.RiskLevel})
	}
	type plain policyIn
	return json.Marshal(plain(p))
}

func trimmedID() *zStr { return &zStr{trim: true, min: num(1)} }

var intentField = zRequired("intent", zText(1, 500))

var userChangeShape = &zObj{fields: []zField{
	zRequired("action", zEnum("", "invite", "set_role", "deactivate", "reactivate")),
	zOptional("email", &zStr{trim: true, email: true}),
	zOptional("userId", trimmedID()),
	zOptional("role", zEnum("", "admin", "member")),
	intentField,
}}

var groupChangeShape = &zObj{fields: []zField{
	zRequired("action", zEnum("", "create_group", "delete_group", "add_member", "remove_member", "add_idp_rule", "remove_idp_rule")),
	zOptional("name", zText(1, 120)),
	zOptional("description", &zStr{trim: true, max: num(500)}),
	zOptional("groupId", trimmedID()),
	zOptional("userId", trimmedID()),
	zOptional("ssoGroupId", trimmedID()),
	zOptional("ruleId", trimmedID()),
	intentField,
}}

var itemGrantShape = &zObj{fields: []zField{
	zRequired("action", zEnum("", "grant_item", "revoke_item")),
	zRequired("groupId", trimmedID()),
	zRequired("itemType", zEnum("", "skill", "workflow", "metric", "dashboard", "watcher",
		"team_memory", "data_source", "saved_query", "api_operation", "action", "integration", "channel", "pack")),
	zRequired("itemId", zText(1, 500)),
	intentField,
}}

var rowFilterSchema = &zLazy{def: "__schema0"}

func init() {
	scalar := zUnion{&zStr{}, zPrim("number"), zPrim("boolean")}
	rowFilterSchema.inner = zUnion{
		&zObj{fields: []zField{zRequired("and", &zArr{elem: rowFilterSchema, min: num(1)})}},
		&zObj{fields: []zField{zRequired("or", &zArr{elem: rowFilterSchema, min: num(1)})}},
		&zObj{fields: []zField{
			zRequired("column", &zStr{min: num(1)}),
			zRequired("op", zEnum("", "eq", "neq", "gt", "gte", "lt", "lte", "in", "nin", "is_null")),
			zRequired("value", zUnion{&zStr{}, zPrim("number"), zPrim("boolean"), &zArr{elem: scalar},
				&zObj{fields: []zField{zRequired("var", zEnum("", "user_id", "user_groups", "account_id"))}}}),
		}},
	}
}

var dataAccessShape = &zObj{defs: []*zLazy{rowFilterSchema}, fields: []zField{
	zRequired("action", zEnum("", "set_table_access", "remove_table_access")),
	zOptional("groupId", trimmedID()),
	zOptional("source", trimmedID()),
	zOptional("table", trimmedID()),
	zOptional("columns", &zArr{elem: trimmedID(), min: num(1)}),
	zOptional("rowFilter", rowFilterSchema),
	zOptional("ruleId", trimmedID()),
	intentField,
}}

var channelChangeShape = &zObj{fields: []zField{
	zRequired("action", zEnum("", "link", "unlink", "block", "unblock")),
	zRequired("identityId", trimmedID()),
	zOptional("appUserId", trimmedID()),
	intentField,
}}

var dataSourceChangeShape = &zObj{fields: []zField{
	zRequired("action", zEnum("", "register", "enable", "disable", "set_default", "remove")),
	zRequired("name", &zStr{trim: true, min: num(1), max: num(64), pattern: regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`),
		patternMsg: "lowercase letters, digits, dashes", lengthFirst: true}),
	zOptional("label", &zStr{trim: true, max: num(120)}),
	zOptional("sourceKind", zEnum("", "graphjin", "database", "api", "files", "code")),
	intentField,
}}

func adminFail(message string) toolResult {
	return toolText(jsObjOf("ok", false, "error", message))
}

// withoutIntent is the payload of `const { intent, ...payload } = args`.
func withoutIntent(args *jsObj) *jsObj { return dropKeysOf(args, "intent") }

func dropKeysOf(o *jsObj, keys ...string) *jsObj {
	out := &jsObj{}
	for _, k := range o.ordered() {
		if !containsString(keys, k) {
			out.set(k, o.vals[k])
		}
	}
	return out
}

// firstPresent is a ?? b ?? ... over the keys of args, then fallback.
func firstPresent(args *jsObj, fallback string, keys ...string) string {
	for _, k := range keys {
		if v, ok := args.vals[k]; ok && v != nil {
			return jsTemplate(v)
		}
	}
	return fallback
}

// copyTruthy is `...(args.k ? { k: args.k } : {})` for each key.
func copyTruthy(out, args *jsObj, keys ...string) *jsObj {
	for _, k := range keys {
		if v, ok := args.vals[k]; ok && jsTruthy(v) {
			out.set(k, v)
		}
	}
	return out
}

// proposeAdmin is proposeAdminAction() in packages/llm/src/work/tools.ts: an
// internal, high-risk action request that the org's rules decide.
func (b turnBinding) proposeAdmin(ctx context.Context, be toolBackend, kind, target, intent string, payload *jsObj) (toolResult, error) {
	high := "high"
	decision, err := b.evaluatePolicy(ctx, be, policyIn{OrgID: b.OrgID, Scope: "internal", Kind: kind, Target: &target, RiskLevel: &high, noPayload: true})
	if err != nil {
		return toolResult{}, err
	}
	verdict, _ := jsField(decision, "decision")
	if verdict != "allow" && verdict != "needs_approval" {
		reason := "policy denies this change from chat"
		if r, ok := jsField(decision, "reason"); ok && jsTruthy(r) {
			reason = jsTemplate(r)
		}
		return toolText(jsObjOf("ok", false, "decision", "denied", "reason", reason)), nil
	}
	status := "pending_approval"
	if verdict == "allow" {
		status = "approved"
	}
	var policyID any
	if policy, ok := jsField(decision, "policy"); ok && jsTruthy(policy) {
		policyID, _ = jsField(policy, "id")
	}
	in := jsObjOf("orgId", b.OrgID, "scope", "internal", "kind", kind, "target", target, "payload", payload,
		"riskLevel", high, "status", status, "policyId", policyID, "summary", intent, "intent", intent,
		"workRunId", nullIfEmpty(b.RunID), "requestedByRunId", nil)
	be.audit(ctx, "/v1/action/request")
	request, err := be.createActionRequest(ctx, in)
	if err != nil {
		return toolResult{}, err
	}
	requestID, _ := jsField(request, "id")
	if status == "approved" {
		be.audit(ctx, "/v1/action/enqueue")
		if err := be.enqueueAction(ctx, actionRefIn{OrgID: b.OrgID, ActionRequestID: jsStringOf(requestID)}); err != nil {
			return toolResult{}, err
		}
	}
	emitted := "pending_approval"
	if status == "approved" {
		emitted = "auto_approved"
	}
	if err := be.emitEvent(ctx, jsObjOf("type", "action_request_emit", "action_request_id", requestID, "kind", kind,
		"scope", "internal", "decision", emitted, "summary", intent, "risk_level", high)); err != nil {
		return toolResult{}, err
	}
	note := "Approval-gated: tell the operator what you proposed and end your turn — the approval card is rendered inline."
	if status == "approved" {
		note = "Queued for execution; the result lands in this run."
	}
	return toolText(jsObjOf("ok", true, "decision", status, "action_request_id", requestID, "note", note)), nil
}

func adminList(ctx context.Context, be toolBackend, path string, list func() (any, error)) (toolResult, error) {
	be.audit(ctx, path)
	result, err := list()
	if err != nil {
		return toolResult{}, err
	}
	return toolText(result), nil
}

// userManagerTools is buildUserManagerServer().
func userManagerTools(b turnBinding, be toolBackend) []toolDef {
	const server = "neko_user_manager"
	org := orgIn{OrgID: b.OrgID}
	return []toolDef{
		{server: server, name: "list_users", shape: &zObj{},
			description: joinWords("List the org's users and synchronized SSO groups with stable IDs,",
				"membership, role, disabled state, and last login. Use before",
				"proposing any user, group, or access change."),
			call: func(ctx context.Context, _ *jsObj) (toolResult, error) {
				return adminList(ctx, be, "/v1/users/list", func() (any, error) { return be.listUsers(ctx, org) })
			}},
		{server: server, name: "request_user_change", shape: userChangeShape,
			description: joinWords("Propose a user-management change: invite (email + role),",
				"set_role (userId + role admin|member), deactivate or reactivate",
				"(userId). NEVER applies directly — files an action request that an",
				"ADMIN must approve on the approvals surface. Tell the operator what",
				"you proposed and end your turn."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				action := stringArg(args, "action")
				if action == "invite" && (!jsTruthy(args.vals["email"]) || !jsTruthy(args.vals["role"])) {
					return adminFail("invite needs email + role"), nil
				}
				if action != "invite" && !jsTruthy(args.vals["userId"]) {
					return adminFail(action + " needs userId"), nil
				}
				payload := copyTruthy(jsObjOf("action", action), args, "email", "userId", "role")
				return b.proposeAdmin(ctx, be, "user_admin", firstPresent(args, action, "email", "userId"),
					stringArg(args, "intent"), payload)
			}},
		{server: server, name: "list_groups", shape: &zObj{},
			description: joinWords("List OpenNeko groups with their members (and whether each membership",
				"is local or from an IdP rule), the items each group holds, IdP rules,",
				"and each group's table data access. Use before proposing any group,",
				"item grant or data access change."),
			call: func(ctx context.Context, _ *jsObj) (toolResult, error) {
				return adminList(ctx, be, "/v1/groups/list", func() (any, error) { return be.listGroups(ctx, org) })
			}},
		{server: server, name: "request_group_change", shape: groupChangeShape,
			description: joinWords("Propose a group change: create_group (name, description?),",
				"delete_group (groupId), add_member or remove_member (groupId + userId),",
				"add_idp_rule (ssoGroupId + groupId) or remove_idp_rule (ruleId).",
				"NEVER applies directly — files an action request an ADMIN must approve.",
				"Tell the operator what you proposed and end your turn."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				action := stringArg(args, "action")
				needs := map[string][]string{
					"create_group": {"name"}, "delete_group": {"groupId"},
					"add_member": {"groupId", "userId"}, "remove_member": {"groupId", "userId"},
					"add_idp_rule": {"groupId", "ssoGroupId"}, "remove_idp_rule": {"ruleId"},
				}[action]
				var missing []string
				for _, key := range needs {
					if !jsTruthy(args.vals[key]) {
						missing = append(missing, key)
					}
				}
				if len(missing) > 0 {
					return adminFail(action + " needs " + strings.Join(missing, " + ")), nil
				}
				return b.proposeAdmin(ctx, be, "group_admin", firstPresent(args, action, "groupId", "name", "ruleId"),
					stringArg(args, "intent"), withoutIntent(args))
			}},
		{server: server, name: "request_item_grant", shape: itemGrantShape,
			description: joinWords("Propose granting or revoking one item for a group. itemType is one of",
				"skill, workflow, metric, dashboard,",
				"watcher, team_memory, data_source, saved_query, api_operation, action,",
				"integration, channel, pack. itemId '*' means every current and future",
				"item of the type. NEVER applies directly — an ADMIN must approve."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				return b.proposeAdmin(ctx, be, "group_admin", stringArg(args, "itemType")+":"+stringArg(args, "itemId"),
					stringArg(args, "intent"), withoutIntent(args))
			}},
		{server: server, name: "request_data_access_change", shape: dataAccessShape,
			description: joinWords("Propose which table rows and columns a group reads through GraphJin:",
				"set_table_access (groupId, source, table as name or schema.name, columns,",
				"rowFilter?) or remove_table_access (ruleId). rowFilter is a tree of",
				"{column, op, value} conditions joined with {and: []} or {or: []}; value",
				"may be {var: 'user_id'} or {var: 'user_groups'}. NEVER applies directly",
				"— an ADMIN must approve."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				payload := withoutIntent(args)
				action := stringArg(args, "action")
				if action == "set_table_access" {
					for _, key := range []string{"groupId", "source", "table", "columns"} {
						if !jsTruthy(payload.vals[key]) {
							return adminFail("set_table_access needs groupId + source + table + columns"), nil
						}
					}
				}
				if action == "remove_table_access" && !jsTruthy(payload.vals["ruleId"]) {
					return adminFail("remove_table_access needs ruleId"), nil
				}
				target := firstPresent(payload, action, "ruleId")
				if jsTruthy(payload.vals["table"]) {
					target = jsTemplate(jsLookup(payload, "source")) + ":" + jsTemplate(payload.vals["table"])
				}
				return b.proposeAdmin(ctx, be, "group_admin", target, stringArg(args, "intent"), payload)
			}},
	}
}

// channelManagerTools is buildChannelManagerServer().
func channelManagerTools(b turnBinding, be toolBackend) []toolDef {
	const server = "neko_channel_manager"
	return []toolDef{
		{server: server, name: "list_channels", shape: &zObj{},
			description: joinWords("List the org's channel workspaces (Slack team /",
				"Telegram bot bindings) and every channel identity seen, with link",
				"status (linked|unverified|blocked) and the app_user it acts as.",
				"Use before proposing any change, and to answer 'who can reach the",
				"agent from chat?'."),
			call: func(ctx context.Context, _ *jsObj) (toolResult, error) {
				return adminList(ctx, be, "/v1/channels/list", func() (any, error) { return be.listChannels(ctx, orgIn{OrgID: b.OrgID}) })
			}},
		{server: server, name: "request_channel_change", shape: channelChangeShape,
			description: joinWords("Propose a channel-identity change: link (identityId + appUserId),",
				"unlink, block or unblock (identityId). NEVER applies directly —",
				"files an action request that an ADMIN must approve on the approvals",
				"surface. Tell the operator what you proposed and end your turn."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				action, identity := stringArg(args, "action"), stringArg(args, "identityId")
				if action == "link" && !jsTruthy(args.vals["appUserId"]) {
					return adminFail("link needs appUserId"), nil
				}
				payload := copyTruthy(jsObjOf("action", action, "identityId", identity), args, "appUserId")
				return b.proposeAdmin(ctx, be, "channel_admin", identity, stringArg(args, "intent"), payload)
			}},
	}
}

// dataSourceManagerTools is buildDataSourceManagerServer().
func dataSourceManagerTools(b turnBinding, be toolBackend) []toolDef {
	const server = "neko_data_source_manager"
	return []toolDef{
		{server: server, name: "list_data_sources", shape: &zObj{},
			description: joinWords("List the org's registered data sources: name, label, auth mode,",
				"default flag, enabled state and server hostname. No connection",
				"strings or credentials are ever returned. Use before proposing",
				"any change."),
			call: func(ctx context.Context, _ *jsObj) (toolResult, error) {
				return adminList(ctx, be, "/v1/datasources/list", func() (any, error) { return be.listDataSources(ctx, orgIn{OrgID: b.OrgID}) })
			}},
		{server: server, name: "request_data_source_change", shape: dataSourceChangeShape,
			description: joinWords("Propose a data-source registry change: register (name + optional",
				"label — creates a DISABLED placeholder; the admin completes the",
				"connection in Settings, credentials never pass through chat),",
				"enable, disable, set_default, or remove (name). NEVER applies",
				"directly — files an action request that an ADMIN must approve.",
				"Tell the operator what you proposed and end your turn."),
			call: func(ctx context.Context, args *jsObj) (toolResult, error) {
				name := stringArg(args, "name")
				payload := copyTruthy(jsObjOf("action", stringArg(args, "action"), "name", name), args, "label", "sourceKind")
				return b.proposeAdmin(ctx, be, "data_source_admin", name, stringArg(args, "intent"), payload)
			}},
	}
}

// auditTools is buildAuditViewerServer().
func auditTools(b turnBinding, be toolBackend) []toolDef {
	return []toolDef{{server: "neko_audit", name: "audit_trail",
		shape: &zObj{fields: []zField{zOptional("limit", zIntRange(1, 200))}},
		description: joinWords("ADMIN ONLY. The org's audit trail: recent action requests (who",
			"proposed what via which agent backend, and what happened),",
			"behavioral alerts (SEC7 thresholds), and a 24h summary of",
			"control-plane gateway calls per run. Use to answer 'what has the",
			"agent been doing?' and 'who approved that?'."),
		call: func(ctx context.Context, args *jsObj) (toolResult, error) {
			be.audit(ctx, "/v1/audit/list")
			result, err := be.listAuditTrail(ctx, auditTrailIn{OrgID: b.OrgID, RunID: b.runID(), Limit: optionalInt(args, "limit")})
			if err != nil {
				return toolResult{}, err
			}
			if jsTruthy(jsGet(result, "denied")) {
				return toolText(jsObjOf("ok", false, "error", "audit trail is admin-only")), nil
			}
			out := jsObjOf("ok", true)
			if o, ok := result.(*jsObj); ok {
				for _, k := range o.ordered() {
					out.set(k, o.vals[k])
				}
			}
			return toolText(out), nil
		}}}
}
