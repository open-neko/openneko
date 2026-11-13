package webfront

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

// neko_graphjin relays the org's GraphJin MCP tools under the run's
// identity, as packages/llm/src/data-access does.

const (
	graphjinAgentTool      = "ask_graphjin_agent"
	graphjinDirectGoverned = "direct-governed"
	graphjinMCPProtocol    = "2026-07-28"
)

var directGovernedTools = []string{"graphql_help", "query_catalog", "validate_where_clause", "execute_graphql"}

func parseGraphjinToolPolicy(value string) (string, error) {
	mode := jsTrim(value)
	if mode == "" || mode == graphjinDirectGoverned {
		return mode, nil
	}
	return "", errors.New("unsupported GraphJin MCP tool policy: " + mode)
}

func applyGraphjinToolPolicy(tools []any, policy string) []any {
	out := []any{}
	for _, entry := range tools {
		tool, _ := entry.(*jsObj)
		name, _ := jsGet(tool, "name").(string)
		if policy == "" {
			if name != graphjinAgentTool {
				out = append(out, entry)
			}
			continue
		}
		if tool == nil || !containsString(directGovernedTools, name) {
			continue
		}
		if name == "execute_graphql" {
			parts := []string{}
			if d, ok := jsNonEmptyString(tool.vals["description"]); ok {
				parts = append(parts, d)
			}
			parts = append(parts, "OpenNeko exposes direct execution only; GraphJin enforces source and operation authorization.")
			tool.set("description", strings.Join(parts, " "))
		}
		out = append(out, tool)
	}
	return out
}

// assertGraphjinToolCall checks the call itself: an MCP caller may call a
// name it never listed.
func assertGraphjinToolCall(policy, name string, args *jsObj) error {
	if policy == "" {
		if name == graphjinAgentTool {
			return errors.New("GraphJin MCP tool " + graphjinAgentTool + " is not available")
		}
		return nil
	}
	if !containsString(directGovernedTools, name) {
		return fmt.Errorf("GraphJin MCP %s policy blocked tool %s", policy, jsStringify(name, false))
	}
	if name != "execute_graphql" {
		return nil
	}
	if q, ok := jsGet(args, "query").(string); !ok || jsTrim(q) == "" {
		return errors.New("GraphJin MCP direct-governed policy requires execute_graphql.query to be a non-empty string")
	}
	return nil
}

func graphjinTools(b turnBinding, be toolBackend) toolGroup {
	caller := graphjinCaller{OrgID: b.OrgID}
	if b.Kind != "agent-job" {
		caller.RunID = b.runID()
	}
	return toolGroup{dynamic: &dynamicTools{
		server: "neko_graphjin",
		list: func(ctx context.Context) ([]any, error) {
			be.audit(ctx, "/v1/graphjin/tools/list")
			result, err := traceGraphjinToolsList(b.trace(), func() (any, error) { return be.listGraphjinTools(ctx, caller) })
			if err != nil {
				return nil, err
			}
			return applyGraphjinToolPolicy(jsArray(result), b.GraphjinToolPolicy), nil
		},
		call: func(ctx context.Context, name string, args *jsObj) (any, error) {
			if err := assertGraphjinToolCall(b.GraphjinToolPolicy, name, args); err != nil {
				return nil, err
			}
			be.audit(ctx, "/v1/graphjin/tools/call")
			in := graphjinToolCall{OrgID: caller.OrgID, RunID: caller.RunID, Name: name, Arguments: args}
			return traceGraphjinToolCall(b.trace(), name, args, func() (any, error) { return be.callGraphjinTool(ctx, in) })
		},
	}}
}

type graphjinCaller struct {
	OrgID string  `json:"orgId"`
	RunID *string `json:"runId,omitempty"`
}

type graphjinToolCall struct {
	OrgID     string  `json:"orgId"`
	RunID     *string `json:"runId,omitempty"`
	Name      string  `json:"name"`
	Arguments *jsObj  `json:"arguments,omitempty"`
}

// normalizeListedTools gives each tool as the MCP SDK's ToolSchema parses
// it: known keys in schema order, unknown keys dropped, input and output
// schemas keep their extra keys after type, properties and required.
func normalizeListedTools(tools []any) ([]any, error) {
	out := make([]any, 0, len(tools))
	for i, entry := range tools {
		tool, _ := entry.(*jsObj)
		name, nameOK := jsGet(tool, "name").(string)
		input, inputOK := jsGet(tool, "inputSchema").(*jsObj)
		if !nameOK || !inputOK || input.vals["type"] != "object" {
			return nil, fmt.Errorf("GraphJin MCP tools/list returned an invalid tool at index %d", i)
		}
		t := jsObjOf("name", name)
		for _, k := range []string{"title", "icons", "description"} {
			if v, ok := tool.vals[k]; ok && v != nil {
				t.set(k, v)
			}
		}
		t.set("inputSchema", normalizeToolSchema(input))
		if output, ok := tool.vals["outputSchema"].(*jsObj); ok {
			t.set("outputSchema", normalizeToolSchema(output))
		}
		if a, ok := tool.vals["annotations"].(*jsObj); ok {
			t.set("annotations", pickKeys(a, "title", "readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"))
		}
		if e, ok := tool.vals["execution"].(*jsObj); ok {
			t.set("execution", pickKeys(e, "taskSupport"))
		}
		if m, ok := tool.vals["_meta"]; ok && m != nil {
			t.set("_meta", m)
		}
		out = append(out, t)
	}
	return out, nil
}

func normalizeToolSchema(s *jsObj) *jsObj {
	known := []string{"type", "properties", "required"}
	out := pickKeys(s, known...)
	for _, k := range s.ordered() {
		if !containsString(known, k) {
			out.set(k, s.vals[k])
		}
	}
	return out
}

func pickKeys(o *jsObj, keys ...string) *jsObj {
	out := &jsObj{}
	for _, k := range keys {
		if v, ok := o.vals[k]; ok && v != nil {
			out.set(k, v)
		}
	}
	return out
}

// normalizeCallResult is CallToolResultSchema.parse(): content defaults to
// an empty list and text blocks keep only their known keys.
func normalizeCallResult(v any) (*jsObj, error) {
	r, ok := v.(*jsObj)
	if !ok || r == nil {
		return nil, errors.New("GraphJin MCP tools/call returned an invalid result")
	}
	out := &jsObj{}
	if m, ok := r.vals["_meta"]; ok && m != nil {
		out.set("_meta", m)
	}
	content := []any{}
	for _, item := range jsArray(r.vals["content"]) {
		if block, ok := item.(*jsObj); ok && block.vals["type"] == "text" {
			item = pickKeys(block, "type", "text", "annotations", "_meta")
		}
		content = append(content, item)
	}
	out.set("content", content)
	for _, k := range []string{"structuredContent", "isError"} {
		if v, ok := r.vals[k]; ok && v != nil {
			out.set(k, v)
		}
	}
	for _, k := range r.ordered() {
		if !containsString([]string{"_meta", "content", "structuredContent", "isError"}, k) {
			out.set(k, r.vals[k])
		}
	}
	return out, nil
}

var schemaMapKeywords = []string{"$defs", "definitions", "dependentSchemas", "patternProperties", "properties"}
var schemaArrayKeywords = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
var schemaSingleKeywords = []string{"additionalItems", "additionalProperties", "contains", "contentSchema", "else", "if",
	"items", "not", "propertyNames", "then", "unevaluatedItems", "unevaluatedProperties"}

// normalizeBooleanSchemas turns the boolean schemas that JSON Schema allows
// into the equal object schemas: true is {} and false is {"not": {}}.
func normalizeBooleanSchemas(v any) any {
	switch t := v.(type) {
	case bool:
		if t {
			return &jsObj{}
		}
		return jsObjOf("not", &jsObj{})
	case *jsObj:
		out := &jsObj{}
		for _, k := range t.ordered() {
			child := t.vals[k]
			switch {
			case containsString(schemaMapKeywords, k):
				if m, ok := child.(*jsObj); ok {
					mapped := &jsObj{}
					for _, name := range m.ordered() {
						mapped.set(name, normalizeBooleanSchemas(m.vals[name]))
					}
					child = mapped
				}
			case containsString(schemaArrayKeywords, k):
				if list, ok := child.([]any); ok {
					child = mapEach(list, normalizeBooleanSchemas)
				}
			case containsString(schemaSingleKeywords, k):
				if list, ok := child.([]any); ok {
					child = mapEach(list, normalizeBooleanSchemas)
				} else {
					child = normalizeBooleanSchemas(child)
				}
			}
			out.set(k, child)
		}
		return out
	}
	return v
}

func mapEach(list []any, f func(any) any) []any {
	out := make([]any, len(list))
	for i, v := range list {
		out[i] = f(v)
	}
	return out
}

var graphjinMCPSequence atomic.Int64

var graphjinMCPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return errors.New("redirect refused")
}}

func graphjinMCPRequest(ctx context.Context, endpoint string, headers map[string]string, method string, params *jsObj) (any, error) {
	params.set("_meta", jsObjOf(
		"io.modelcontextprotocol/protocolVersion", graphjinMCPProtocol,
		"io.modelcontextprotocol/clientCapabilities", &jsObj{},
		"io.modelcontextprotocol/clientInfo", jsObjOf("name", "openneko", "version", "1.0.0")))
	body := jsObjOf("jsonrpc", "2.0", "id", float64(graphjinMCPSequence.Add(1)), "method", method, "params", params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(jsStringify(body, false)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json, text/event-stream")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("MCP-Protocol-Version", graphjinMCPProtocol)
	req.Header.Set("Mcp-Method", method)
	if name, ok := params.vals["name"].(string); ok && method == "tools/call" {
		req.Header.Set("Mcp-Name", name)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := graphjinMCPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	text := string(bytes.ToValidUTF8(raw, []byte("�")))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, errors.New(jsTrim(fmt.Sprintf("GraphJin MCP %s failed: %d %s", method, res.StatusCode, jsSlice(text, 1000))))
	}
	reply, err := parseMCPResponse(text, res.Header.Get("content-type"))
	if err != nil {
		return nil, err
	}
	if e, ok := reply.vals["error"].(*jsObj); ok && e != nil {
		code := "unknown"
		if c, ok := e.vals["code"]; ok && c != nil {
			code = jsStringOf(c)
		}
		message := "unknown error"
		if m, ok := e.vals["message"].(string); ok {
			message = m
		}
		detail := ""
		if d, ok := e.vals["data"]; ok {
			detail = " " + jsSlice(jsStringify(d, false), 1000)
		}
		return nil, fmt.Errorf("GraphJin MCP %s error %s: %s%s", method, code, message, detail)
	}
	result, ok := reply.vals["result"]
	if !ok {
		return nil, fmt.Errorf("GraphJin MCP %s returned no result", method)
	}
	return result, nil
}

var sseEventBreak = regexp.MustCompile(`\r?\n\r?\n`)
var sseLineBreak = regexp.MustCompile(`\r?\n`)

func parseMCPResponse(text, contentType string) (*jsObj, error) {
	var parsed any
	if strings.Contains(contentType, "text/event-stream") {
		parsed = parseMCPEventStream(text)
	} else if v, err := jsParse([]byte(text)); err == nil {
		parsed = v
	} else {
		parsed = parseMCPEventStream(text)
	}
	obj, ok := parsed.(*jsObj)
	if !ok || obj == nil {
		return nil, errors.New("GraphJin MCP returned an invalid JSON-RPC response")
	}
	return obj, nil
}

func parseMCPEventStream(text string) any {
	var replies []*jsObj
	for _, event := range sseEventBreak.Split(text, -1) {
		var data []string
		for _, line := range sseLineBreak.Split(event, -1) {
			if rest, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, jsTrimStart(rest))
			}
		}
		joined := jsTrim(strings.Join(data, "\n"))
		if joined == "" {
			continue
		}
		if v, err := jsParse([]byte(joined)); err == nil {
			if obj, ok := v.(*jsObj); ok {
				replies = append(replies, obj)
			}
		}
	}
	for i := len(replies) - 1; i >= 0; i-- {
		_, hasResult := replies[i].vals["result"]
		_, hasError := replies[i].vals["error"]
		if hasResult || hasError {
			return replies[i]
		}
	}
	return nil
}

func listGraphjinMCPTools(ctx context.Context, endpoint string, headers map[string]string) ([]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tools := []any{}
	cursor := ""
	for range 100 {
		params := &jsObj{}
		if cursor != "" {
			params.set("cursor", cursor)
		}
		result, err := graphjinMCPRequest(ctx, endpoint, headers, "tools/list", params)
		if err != nil {
			return nil, err
		}
		page, _ := result.(*jsObj)
		listed, ok := jsGet(page, "tools").([]any)
		if !ok {
			return nil, errors.New("GraphJin MCP tools/list returned no tools")
		}
		for i, tool := range listed {
			if obj, ok := tool.(*jsObj); ok {
				for _, k := range []string{"inputSchema", "outputSchema"} {
					if v, ok := obj.vals[k]; ok {
						obj.set(k, normalizeBooleanSchemas(v))
					}
				}
				listed[i] = obj
			}
		}
		normal, err := normalizeListedTools(listed)
		if err != nil {
			return nil, err
		}
		tools = append(tools, normal...)
		next, _ := jsGet(page, "nextCursor").(string)
		if next == "" {
			return tools, nil
		}
		cursor = next
	}
	return nil, errors.New("GraphJin MCP tools/list exceeded 100 pages")
}

func callGraphjinMCPTool(ctx context.Context, endpoint string, headers map[string]string, name string, args *jsObj) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	if args == nil {
		args = &jsObj{}
	}
	result, err := graphjinMCPRequest(ctx, endpoint, headers, "tools/call", jsObjOf("name", name, "arguments", args))
	if err != nil {
		return nil, err
	}
	return normalizeCallResult(result)
}

func (a *API) runHoldsDataSource(ctx context.Context, orgID string, runID *string) error {
	if runID == nil {
		return nil
	}
	act, err := a.entitlementActorForRun(ctx, orgID, *runID)
	if err != nil {
		return err
	}
	held := heldSet{IDs: map[string]bool{}}
	if act != nil {
		if held, err = a.heldItems(ctx, *act, "data_source"); err != nil {
			return err
		}
	}
	if !held.All && len(held.IDs) == 0 {
		return errors.New("No data source is available to this run.")
	}
	return nil
}

func (a *API) savedQueryRefusal(ctx context.Context, in graphjinToolCall) (*jsObj, error) {
	if in.Name != "execute_saved_query" || in.RunID == nil {
		return nil, nil
	}
	act, err := a.entitlementActorForRun(ctx, in.OrgID, *in.RunID)
	if err != nil {
		return nil, err
	}
	name := jsStringOrEmpty(jsGet(in.Arguments, "name"))
	if act != nil {
		held, err := a.heldItems(ctx, *act, "saved_query")
		if err != nil {
			return nil, err
		}
		if held.has(name) {
			return nil, nil
		}
	}
	return toolResultObj(toolResult{isError: true, text: `Saved query "` + name + `" is not available to this run.`}), nil
}

var graphqlMutationWord = regexp.MustCompile(`(?i)\bmutation\b`)

var nonDatabaseSourceKinds = []string{"api", "file", "graphjin"}

// assertGraphjinMutationAllowed forwards a mutation only when the config
// proves every database source is read-only. A config that cannot be read
// blocks it.
func assertGraphjinMutationAllowed(query, configPath string) error {
	if !graphqlMutationWord.MatchString(query) {
		return nil
	}
	path := jsTrim(configPath)
	if path == "" {
		return errors.New("GraphJin mutation blocked: OPENNEKO_GRAPHJIN_CONFIG is not set, so the host cannot confirm database sources are read-only")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("GraphJin mutation blocked: cannot read %s: %v", path, err)
	}
	if err := assertDatabaseSourcesReadOnly(raw); err != nil {
		return errors.New("GraphJin mutation blocked: " + err.Error())
	}
	return nil
}

func assertDatabaseSourcesReadOnly(raw []byte) error {
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("GraphJin config is not valid YAML")
	}
	sources := yamlField(doc.Content[0], "sources")
	if sources == nil || sources.Tag == "!!null" {
		return nil
	}
	if sources.Kind != yaml.SequenceNode {
		return errors.New("GraphJin config sources must be a YAML list")
	}
	for _, item := range sources.Content {
		if item.Kind != yaml.MappingNode {
			return errors.New("GraphJin config source entries must be YAML objects")
		}
		if kind := yamlField(item, "kind"); kind != nil && kind.Tag == "!!str" && containsString(nonDatabaseSourceKinds, kind.Value) {
			continue
		}
		readOnly := yamlField(item, "read_only")
		var on bool
		if readOnly == nil || readOnly.Tag != "!!bool" || readOnly.Decode(&on) != nil || !on {
			name := "<unnamed>"
			if n := yamlField(item, "name"); n != nil && n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
				name = n.Value
			}
			return fmt.Errorf(`GraphJin database source "%s" is not read_only`, name)
		}
	}
	return nil
}

func yamlField(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func isInternalGraphjinURL(candidate string) bool {
	internal := os.Getenv("OPENNEKO_GRAPHJIN_URL")
	if internal == "" {
		internal = "http://127.0.0.1:8089"
	}
	c, ok := jsURLHost(candidate)
	if !ok {
		return true
	}
	i, ok := jsURLHost(internal)
	return !ok || c == i
}

// jsURLHost is new URL(s).host: the port drops when it is the scheme's own.
func jsURLHost(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port == "" || (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		return host, true
	}
	return host + ":" + port, true
}

type graphjinPrincipal struct {
	userID, role string
}

func (a *API) graphjinCallerPrincipal(ctx context.Context, orgID string, runID *string) (graphjinPrincipal, error) {
	if runID == nil {
		return graphjinPrincipal{role: "service"}, nil
	}
	var userID, role *string
	err := a.DB.QueryRow(ctx, `select actor_user_id, actor_role from work_run where id::text = $1 and org_id = $2 limit 1`,
		*runID, orgID).Scan(&userID, &role)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return graphjinPrincipal{}, err
	}
	switch deref(role) {
	case "admin", "member":
		return graphjinPrincipal{userID: deref(userID), role: *role}, nil
	case "service":
		return graphjinPrincipal{role: "service"}, nil
	}
	return graphjinPrincipal{}, errors.New("GraphJin read actor has an invalid role")
}

// graphjinGroupClaims is graphjinGroupClaims() in packages/db/src/data-access.ts.
func (a *API) graphjinGroupClaims(ctx context.Context, orgID, userID string) (roles, groups []string, err error) {
	rows, err := a.DB.Query(ctx, `select distinct g.slug,
		  exists (select 1 from data_access_rule r where r.group_id = g.id) or exists (
		    select 1 from item_grant i where i.group_id = g.id and i.item_type = 'api_operation') as has_rules
		from user_group g
		join app_user u on u.id = $2 and u.org_id = g.org_id and u.disabled_at is null
		where g.org_id = $1
		  and (g.slug = 'everyone' or exists (select 1 from user_group_membership m where m.group_id = g.id and m.user_id = u.id))
		order by g.slug`, orgID, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	roles, groups = []string{}, []string{}
	for rows.Next() {
		var slug string
		var hasRules bool
		if err := rows.Scan(&slug, &hasRules); err != nil {
			return nil, nil, err
		}
		if hasRules && slug != "administrators" {
			roles = append(roles, "og_"+slug)
		}
		groups = append(groups, slug)
	}
	return roles, groups, rows.Err()
}

func (a *API) graphjinCallerToken(ctx context.Context, orgID string, runID *string) (string, error) {
	p, err := a.graphjinCallerPrincipal(ctx, orgID, runID)
	if err != nil {
		return "", err
	}
	claims := graphjinClaims{orgID: orgID, userID: p.userID, role: p.role}
	if p.userID != "" && p.role == "member" {
		on, err := a.groupGrantsEnabled(ctx, orgID)
		if err != nil {
			return "", err
		}
		if on {
			if claims.groupRoles, claims.groups, err = a.graphjinGroupClaims(ctx, orgID, p.userID); err != nil {
				return "", err
			}
			claims.withGroups = true
		}
	}
	return a.mintGraphjinToken(claims, 5*time.Minute)
}

func (a *API) graphjinMCPAccess(ctx context.Context, orgID string, runID *string) (string, map[string]string, error) {
	var mcpURL *string
	var graphqlURL, kind, authMode string
	err := a.DB.QueryRow(ctx, `select mcp_url, graphql_url, kind, auth_mode from data_source
		where org_id = $1 and enabled = true order by is_default desc, created_at limit 1`, orgID).
		Scan(&mcpURL, &graphqlURL, &kind, &authMode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, err
	}
	if deref(mcpURL) == "" {
		return "", nil, errors.New("no enabled GraphJin MCP endpoint configured")
	}
	if kind != "graphjin" {
		return "", nil, errors.New("the default enabled data source is not a GraphJin server")
	}
	if isInternalGraphjinURL(graphqlURL) || isInternalGraphjinURL(*mcpURL) {
		return "", nil, errors.New("refusing to expose the OpenNeko internal GraphJin MCP")
	}
	headers := map[string]string{}
	switch authMode {
	case "jwt":
		token, err := a.graphjinCallerToken(ctx, orgID, runID)
		if err != nil {
			return "", nil, err
		}
		headers["authorization"] = "Bearer " + token
	case "development":
		if os.Getenv("NODE_ENV") == "production" {
			return "", nil, errors.New("GraphJin development header auth is forbidden in production")
		}
		p, err := a.graphjinCallerPrincipal(ctx, orgID, runID)
		if err != nil {
			return "", nil, err
		}
		headers["X-User-ID"] = orDefault(p.userID, "openneko-service")
		headers["X-User-Role"] = p.role
	}
	return *mcpURL, headers, nil
}

func (r *runTools) listGraphjinTools(ctx context.Context, in graphjinCaller) (any, error) {
	if err := r.api.runHoldsDataSource(ctx, in.OrgID, in.RunID); err != nil {
		return nil, err
	}
	endpoint, headers, err := r.api.graphjinMCPAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	return listGraphjinMCPTools(ctx, endpoint, headers)
}

func (r *runTools) callGraphjinTool(ctx context.Context, in graphjinToolCall) (any, error) {
	refusal, err := r.api.savedQueryRefusal(ctx, in)
	if err != nil || refusal != nil {
		return refusal, err
	}
	if err := r.api.runHoldsDataSource(ctx, in.OrgID, in.RunID); err != nil {
		return nil, err
	}
	if in.Name == "execute_graphql" {
		if err := assertGraphjinMutationAllowed(jsStringOrEmpty(jsGet(in.Arguments, "query")), os.Getenv("OPENNEKO_GRAPHJIN_CONFIG")); err != nil {
			return nil, err
		}
	}
	endpoint, headers, err := r.api.graphjinMCPAccess(ctx, in.OrgID, in.RunID)
	if err != nil {
		return nil, err
	}
	return callGraphjinMCPTool(ctx, endpoint, headers, in.Name, in.Arguments)
}

// jsStringOrEmpty is String(v ?? "").
func jsStringOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	return jsStringOf(v)
}
