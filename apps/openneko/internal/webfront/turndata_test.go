package webfront

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeGraphjinMCP struct {
	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
}

func (g *fakeGraphjinMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	g.mu.Lock()
	g.requests = append(g.requests, body)
	g.headers = append(g.headers, r.Header.Clone())
	g.mu.Unlock()
	params, _ := body["params"].(map[string]any)
	reply := func(result string) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}
	if body["method"] == "tools/list" {
		if params["cursor"] == nil {
			w.Header().Set("content-type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\r\n\r\n" +
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"execute_graphql\",\"description\":\"Run.\"," +
				"\"inputSchema\":{\"type\":\"object\",\"properties\":{\"query\":true,\"variables\":false},\"additionalProperties\":false}}]," +
				"\"nextCursor\":\"p2\"}}\n\n: keepalive\n\n"))
			return
		}
		reply(`{"tools":[{"name":"query_catalog","inputSchema":{"type":"object"}},{"name":"ask_graphjin_agent","inputSchema":{"type":"object"}}]}`)
		return
	}
	args, _ := params["arguments"].(map[string]any)
	switch args["query"] {
	case "{ boom }":
		w.WriteHeader(500)
		_, _ = w.Write([]byte("boom"))
	case "{ bad }":
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"bad where","data":{"field":"x"}}}`))
	default:
		reply(`{"content":[{"type":"text","text":"{\"data\":{\"orders\":[{\"id\":1}]}}","extra":1}]}`)
	}
}

func (g *fakeGraphjinMCP) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

func (g *fakeGraphjinMCP) last() (map[string]any, http.Header) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requests[len(g.requests)-1], g.headers[len(g.headers)-1]
}

func tokenClaims(t *testing.T, header string) map[string]any {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(header, "Bearer "), ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", header)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims
}

func TestTurnDataDatabase(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	gj := &fakeGraphjinMCP{}
	srv := httptest.NewServer(gj)
	defer srv.Close()
	t.Setenv("OPENNEKO_GRAPHJIN_URL", "http://127.0.0.1:8089")
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", "")
	t.Setenv("NODE_ENV", "")

	member := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runMine, Kind: "work"}
	s, err := turnTools(member, a.runTools(member), []string{"neko_memory", "neko_graphjin"})
	if err != nil {
		t.Fatal(err)
	}
	listNames := func() string {
		t.Helper()
		result, rpcErr := s.handle(ctx, "tools/list", nil)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		var out struct{ Tools []struct{ Name string } }
		_ = json.Unmarshal(result.(json.RawMessage), &out)
		var names []string
		for _, tool := range out.Tools {
			names = append(names, tool.Name)
		}
		return strings.Join(names, ",")
	}
	execute := func(args map[string]any) (string, *rpcError) {
		return callTool(t, s, "graphjin_execute_graphql", args)
	}

	seed(t, db, `delete from item_grant where org_id = 'org' and item_type in ('data_source', 'saved_query')`)
	if _, err := a.runTools(member).listGraphjinTools(ctx, graphjinCaller{OrgID: "org", RunID: &member.RunID}); err == nil ||
		err.Error() != "No data source is available to this run." {
		t.Fatalf("a run with no data source: %v", err)
	}
	if names := listNames(); names != "memory_search,memory_save,memory_update" {
		t.Fatalf("a failed list keeps the other tools: %s", names)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'data_source', 'shop' from user_group where org_id = 'org' and slug = 'everyone'`)
	if _, err := a.runTools(member).listGraphjinTools(ctx, graphjinCaller{OrgID: "org", RunID: &member.RunID}); err == nil ||
		err.Error() != "no enabled GraphJin MCP endpoint configured" {
		t.Fatalf("no source: %v", err)
	}
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, auth_mode, enabled, is_default)
		values ('org', 'graphjin', 'shop', $1, $2, 'jwt', true, true)`, srv.URL+"/api/v1/graphql", srv.URL+"/api/v1/mcp")

	if names := listNames(); names != "memory_search,memory_save,memory_update,graphjin_execute_graphql,graphjin_query_catalog" {
		t.Fatalf("tools: %s", names)
	}
	result, _ := s.handle(ctx, "tools/list", nil)
	if !strings.Contains(string(result.(json.RawMessage)), `"inputSchema":{"type":"object","properties":{"query":{},"variables":{"not":{}}},"additionalProperties":{"not":{}}}`) {
		t.Fatalf("boolean schemas: %s", result)
	}
	if gj.count() != 4 {
		t.Fatalf("two lists of two pages each: %d requests", gj.count())
	}
	body, h := gj.last()
	if body["method"] != "tools/list" || body["params"].(map[string]any)["cursor"] != "p2" || h.Get("Mcp-Method") != "tools/list" ||
		h.Get("MCP-Protocol-Version") != "2026-07-28" || h.Get("Mcp-Name") != "" {
		t.Fatalf("list request: %v %v", body, h)
	}
	claims := tokenClaims(t, h.Get("Authorization"))
	if claims["sub"] != "member" || claims["role"] != "member" || rawJSON(claims["roles"]) != `["member"]` || claims["groups"] != nil ||
		claims["org_id"] != "org" {
		t.Fatalf("member token: %v", claims)
	}

	var finance string
	if err := db.QueryRow(ctx, `insert into user_group (org_id, slug, name, kind) values ('org', 'finance', 'Finance', 'custom') returning id::text`).Scan(&finance); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source) values ('org', $1, 'member', 'local')`, finance)
	seed(t, db, `insert into data_access_rule (org_id, group_id, source, table_name, columns) values ('org', $1, 'shop', 'orders', '{id}')`, finance)
	seed(t, db, `insert into data_access_settings (org_id, group_grants_enabled) values ('org', true)`)

	const planted = "sk-planted-gj-0001"
	var events, contents []string
	unregister := registerTraceSink(runMine, func(event, content *jsObj) {
		events = append(events, jsStringify(event, false))
		contents = append(contents, jsStringify(content, false))
	}, secretScrubber([]string{planted}))
	got, rpcErr := execute(map[string]any{"query": "{ orders { id } }", "variables": map[string]any{"key": planted}})
	unregister()
	if rpcErr != nil || got != `{"content":[{"type":"text","text":"{\"data\":{\"orders\":[{\"id\":1}]}}"}]}` {
		t.Fatalf("execute: %s %v", got, rpcErr)
	}
	body, h = gj.last()
	params := body["params"].(map[string]any)
	if h.Get("Mcp-Name") != "execute_graphql" || params["name"] != "execute_graphql" || rawJSON(params["arguments"]) != `{"query":"{ orders { id } }","variables":{"key":"`+planted+`"}}` ||
		rawJSON(params["_meta"].(map[string]any)["io.modelcontextprotocol/clientInfo"]) != `{"name":"openneko","version":"1.0.0"}` {
		t.Fatalf("call request: %v %v", body, h)
	}
	claims = tokenClaims(t, h.Get("Authorization"))
	if rawJSON(claims["roles"]) != `["member","og_finance"]` || rawJSON(claims["groups"]) != `["everyone","finance"]` {
		t.Fatalf("group claims: %v", claims)
	}
	if len(events) != 1 || !strings.Contains(events[0], `"operation":"graphjin.execute"`) || !strings.Contains(events[0], `"operationType":"query"`) ||
		strings.Contains(events[0], planted) || !strings.Contains(contents[0], `"rows":[{"id":1}]`) || !strings.Contains(contents[0], "[REDACTED]") ||
		strings.Contains(contents[0], planted) {
		t.Fatalf("trace: %v %v", events, contents)
	}

	before := gj.count()
	_, rpcErr = execute(map[string]any{"query": "mutation { delete_orders { id } }"})
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "GraphJin mutation blocked: OPENNEKO_GRAPHJIN_CONFIG is not set") {
		t.Fatalf("mutation with no config: %v", rpcErr)
	}
	config := filepath.Join(t.TempDir(), "graphjin.yml")
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", config)
	_ = os.WriteFile(config, []byte("sources:\n  - name: shop\n    type: postgres\n  - name: crm\n    kind: api\n"), 0o600)
	_, rpcErr = execute(map[string]any{"query": "mutation { delete_orders { id } }"})
	if rpcErr == nil || !strings.Contains(rpcErr.Message, `GraphJin mutation blocked: GraphJin database source "shop" is not read_only`) {
		t.Fatalf("mutation on a writable source: %v", rpcErr)
	}
	if gj.count() != before {
		t.Fatal("a blocked mutation reached GraphJin")
	}
	_ = os.WriteFile(config, []byte("sources:\n  - name: shop\n    read_only: true\n  - name: crm\n    kind: api\n"), 0o600)
	if _, rpcErr = execute(map[string]any{"query": "mutation { crm_create_note(text: \"x\") { id } }"}); rpcErr != nil || gj.count() != before+1 {
		t.Fatalf("mutation with read-only databases: %v", rpcErr)
	}

	before = gj.count()
	got, rpcErr = callTool(t, s, "graphjin_execute_saved_query", map[string]any{"name": "top"})
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "unknown tool graphjin_execute_saved_query") || gj.count() != before+2 {
		t.Fatalf("an unlisted tool: %s %v", got, rpcErr)
	}
	refusal, err := a.runTools(member).callGraphjinTool(ctx, graphjinToolCall{OrgID: "org", RunID: &member.RunID, Name: "execute_saved_query",
		Arguments: jsObjOf("name", "top")})
	if err != nil || jsStringify(refusal, false) != `{"content":[{"type":"text","text":"Saved query \"top\" is not available to this run."}],"isError":true}` {
		t.Fatalf("saved query refusal: %s %v", jsStringify(refusal, false), err)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'saved_query', 'top' from user_group where org_id = 'org' and slug = 'everyone'`)
	before = gj.count()
	if _, err := a.runTools(member).callGraphjinTool(ctx, graphjinToolCall{OrgID: "org", RunID: &member.RunID, Name: "execute_saved_query",
		Arguments: jsObjOf("name", "top")}); err != nil || gj.count() != before+1 {
		t.Fatalf("a held saved query: %v", err)
	}

	if _, rpcErr = execute(map[string]any{"query": "{ boom }"}); rpcErr == nil || rpcErr.Message != "MCP error -32603: GraphJin MCP tools/call failed: 500 boom" {
		t.Fatalf("HTTP failure: %v", rpcErr)
	}
	if _, rpcErr = execute(map[string]any{"query": "{ bad }"}); rpcErr == nil ||
		rpcErr.Message != `MCP error -32603: GraphJin MCP tools/call error -32000: bad where {"field":"x"}` {
		t.Fatalf("JSON-RPC error: %v", rpcErr)
	}

	seed(t, db, `update data_source set auth_mode = 'development' where org_id = 'org'`)
	if _, rpcErr = execute(map[string]any{"query": "{ orders { id } }"}); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, h = gj.last(); h.Get("Authorization") != "" || h.Get("X-User-ID") != "member" || h.Get("X-User-Role") != "member" {
		t.Fatalf("development headers: %v", h)
	}
	t.Setenv("NODE_ENV", "production")
	if _, rpcErr = execute(map[string]any{"query": "{ orders { id } }"}); rpcErr == nil || !strings.Contains(rpcErr.Message, "forbidden in production") {
		t.Fatalf("development auth in production: %v", rpcErr)
	}
	t.Setenv("OPENNEKO_GRAPHJIN_URL", srv.URL)
	if _, rpcErr = execute(map[string]any{"query": "{ orders { id } }"}); rpcErr == nil || !strings.Contains(rpcErr.Message, "refusing to expose the OpenNeko internal GraphJin MCP") {
		t.Fatalf("internal GraphJin: %v", rpcErr)
	}
	seed(t, db, `update data_source set kind = 'postgres' where org_id = 'org'`)
	if _, rpcErr = execute(map[string]any{"query": "{ orders { id } }"}); rpcErr == nil || !strings.Contains(rpcErr.Message, "is not a GraphJin server") {
		t.Fatalf("not GraphJin: %v", rpcErr)
	}

	rows, err := db.Query(ctx, `select path from control_plane_audit where run_id = $1 and path like '/v1/graphjin/%' order by id`, runMine)
	if err != nil {
		t.Fatal(err)
	}
	var audits []string
	for rows.Next() {
		var path string
		_ = rows.Scan(&path)
		audits = append(audits, strings.TrimPrefix(path, "/v1/graphjin/tools/"))
	}
	if strings.Join(audits, ",") != "list,list,list,call,call,call,call,list,call,call,call,call,call,call" {
		t.Fatalf("audit rows: %v", audits)
	}

	const install = "90000000-0000-4000-8000-000000000001"
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config) values ($1, 'org', 'shop', '1', 'installed', 'h', '{}'::jsonb)`, install)
	seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash) values
		($1, 'org', 'source', 'shop', 'shop', 'h', 'h'), ($1, 'org', 'saved_query', 'top', 'low_stock', 'h', 'h')`, install)
	pack := actor{OrgID: "org", PackID: "shop"}
	if held, err := a.heldItems(ctx, pack, "data_source"); err != nil || held.All || !held.IDs["shop"] || len(held.IDs) != 1 {
		t.Fatalf("pack sources: %+v %v", held, err)
	}
	if held, err := a.heldItems(ctx, pack, "saved_query"); err != nil || !held.has("low_stock") || held.has("top") {
		t.Fatalf("pack saved queries: %+v %v", held, err)
	}
	if held, err := a.heldItems(ctx, actor{OrgID: "org", PackID: "other"}, "data_source"); err != nil || len(held.IDs) != 0 {
		t.Fatalf("another pack: %+v %v", held, err)
	}
}

func rawJSON(v any) string { return string(mustJSON(v)) }

func TestGraphjinMutationCheck(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ name, query, config, want string }{
		{"query", "{ orders { id } }", "-", ""},
		{"mutation word in a name", "{ mutations_log { id } }", "-", ""},
		{"no config", "Mutation { x }", "-", "GraphJin mutation blocked: OPENNEKO_GRAPHJIN_CONFIG is not set, so the host cannot confirm database sources are read-only"},
		{"no sources", "mutation { x }", "database: {}\n", ""},
		{"null sources", "mutation { x }", "sources:\n", ""},
		{"all read-only", "mutation { x }", "sources:\n  - name: a\n    read_only: true\n  - name: b\n    kind: file\n  - name: c\n    kind: graphjin\n", ""},
		{"quoted true", "mutation { x }", "sources:\n  - name: a\n    read_only: \"true\"\n", `GraphJin mutation blocked: GraphJin database source "a" is not read_only`},
		{"yes is text", "mutation { x }", "sources:\n  - name: a\n    read_only: yes\n", `GraphJin mutation blocked: GraphJin database source "a" is not read_only`},
		{"unnamed", "mutation { x }", "sources:\n  - kind: postgres\n", `GraphJin mutation blocked: GraphJin database source "<unnamed>" is not read_only`},
		{"kind not text", "mutation { x }", "sources:\n  - name: a\n    kind: [api]\n", `GraphJin mutation blocked: GraphJin database source "a" is not read_only`},
		{"sources a map", "mutation { x }", "sources:\n  a: 1\n", "GraphJin mutation blocked: GraphJin config sources must be a YAML list"},
		{"entry not a map", "mutation { x }", "sources:\n  - a\n", "GraphJin mutation blocked: GraphJin config source entries must be YAML objects"},
		{"not YAML", "mutation { x }", "sources: [\n", "GraphJin mutation blocked: GraphJin config is not valid YAML"},
		{"empty file", "mutation { x }", "", "GraphJin mutation blocked: GraphJin config is not valid YAML"},
	} {
		path := ""
		if c.config != "-" {
			path = filepath.Join(dir, strings.ReplaceAll(c.name, " ", "_")+".yml")
			_ = os.WriteFile(path, []byte(c.config), 0o600)
		}
		got := ""
		if err := assertGraphjinMutationAllowed(c.query, path); err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	err := assertGraphjinMutationAllowed("mutation { x }", filepath.Join(dir, "missing.yml"))
	if err == nil || !strings.HasPrefix(err.Error(), "GraphJin mutation blocked: cannot read ") {
		t.Fatal(err)
	}
}

func TestGraphjinInternalURL(t *testing.T) {
	t.Setenv("OPENNEKO_GRAPHJIN_URL", "")
	for url, want := range map[string]bool{
		"http://127.0.0.1:8089/api/v1/mcp": true,
		"http://127.0.0.1:8090/api/v1/mcp": false,
		"https://gj.example.com/mcp":       false,
		"not a url":                        true,
	} {
		if isInternalGraphjinURL(url) != want {
			t.Errorf("%s: want %v", url, want)
		}
	}
	t.Setenv("OPENNEKO_GRAPHJIN_URL", "http://GraphJin:80")
	if !isInternalGraphjinURL("http://graphjin/api/v1/graphql") || isInternalGraphjinURL("http://graphjin:8443/api") {
		t.Fatal("the scheme's own port must drop and the host must compare in lower case")
	}
}

func TestGraphjinToolPolicyParse(t *testing.T) {
	for in, want := range map[string]string{"": "", "  ": "", " direct-governed ": "direct-governed"} {
		if got, err := parseGraphjinToolPolicy(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := parseGraphjinToolPolicy("open"); err == nil || err.Error() != "unsupported GraphJin MCP tool policy: open" {
		t.Fatal(err)
	}
	if _, err := turnTools(turnBinding{GraphjinToolPolicy: "open"}, &fakeTools{}, nil); err == nil {
		t.Fatal("an unknown policy must fail closed")
	}
}
