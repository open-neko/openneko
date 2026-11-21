package webfront

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestRunGraphjinMutationsAreRefused(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	const adminRun = "b1000000-0000-4000-8000-0000000000d1"
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'test', 'admin', 'admin')`, adminRun, threadMine)
	var (
		mu    sync.Mutex
		calls []string
	)
	saved := map[string]string{
		"orders_by_day": `{"name":"orders_by_day","operation":"query","query":"query orders_by_day { orders { id } }"}`,
		"refund_order":  `{"name":"refund_order","operation":"mutation","query":"mutation refund_order { stripe_refund(call: {}) { id } }"}`,
		"hidden":        `{"name":"hidden","query":"# a note\nmutation hidden { x { id } }"}`,
		"opaque":        `{"name":"opaque"}`,
	}
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &req)
		mu.Lock()
		calls = append(calls, req.Method+" "+req.Params.Name)
		mu.Unlock()
		var result any
		switch {
		case req.Method == "tools/list":
			tools := []any{}
			for _, name := range []string{"execute_graphql", "execute_saved_query", "update_current_config", "apply_schema_changes",
				"save_workflow", "test_database_connection", "discover_databases", "apply_database_setup", "query_catalog"} {
				tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		case req.Params.Name == "get_saved_query":
			text, ok := saved[req.Params.Arguments["name"].(string)]
			if !ok {
				result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "failed to get query"}}}
				break
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		default:
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"data":{}}`}}}
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer gj.Close()
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, is_default, enabled, auth_mode)
		values ('org', 'graphjin', 'shop', $1, $2, true, true, 'jwt')`, gj.URL+"/api/v1/graphql", gj.URL+"/api/v1/mcp")
	rt := a.runTools(turnBinding{OrgID: "org", RunID: adminRun})
	runID := adminRun
	sent := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}

	listed, err := rt.listGraphjinTools(t.Context(), graphjinCaller{OrgID: "org", RunID: &runID})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range applyGraphjinToolPolicy(jsArray(listed), "") {
		names = append(names, jsGet(tool, "name").(string))
	}
	if strings.Join(names, ",") != "execute_graphql,execute_saved_query,query_catalog" {
		t.Fatalf("a run lists %v", names)
	}
	sent()

	call := func(name string, args map[string]any) error {
		in, _ := jsParse(mustJSON(args))
		_, err := rt.callGraphjinTool(t.Context(), graphjinToolCall{OrgID: "org", RunID: &runID, Name: name, Arguments: in.(*jsObj)})
		return err
	}
	for _, c := range []struct {
		name  string
		args  map[string]any
		want  string
		calls string
	}{
		{"execute_graphql", map[string]any{"query": "mutation { stripe_refund(call: {charge: \"ch_1\"}) { id } }"}, "GraphJin mutation blocked", ""},
		{"execute_graphql", map[string]any{"query": "{ orders { id } }"}, "", "tools/call execute_graphql"},
		{"execute_saved_query", map[string]any{"name": "refund_order"}, "GraphJin saved mutation blocked", "tools/call get_saved_query"},
		{"execute_saved_query", map[string]any{"name": "hidden"}, "GraphJin saved mutation blocked", "tools/call get_saved_query"},
		{"execute_saved_query", map[string]any{"name": "opaque"}, "GraphJin saved mutation blocked", "tools/call get_saved_query"},
		{"execute_saved_query", map[string]any{"name": "missing"}, "GraphJin saved mutation blocked", "tools/call get_saved_query"},
		{"execute_saved_query", map[string]any{"name": "orders_by_day"}, "", "tools/call get_saved_query,tools/call execute_saved_query"},
		{"update_current_config", map[string]any{"config": map[string]any{}}, "not available to a run", ""},
		{"test_database_connection", map[string]any{"host": "collect.example"}, "not available to a run", ""},
		{"save_workflow", map[string]any{"name": "w", "code": "x"}, "not available to a run", ""},
	} {
		err := call(c.name, c.args)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s %v: %v", c.name, c.args, err)
		}
		if got := strings.Join(sent(), ","); got != c.calls {
			t.Errorf("%s %v sent %q, want %q", c.name, c.args, got, c.calls)
		}
	}
	if err := assertGraphjinToolCall("", "apply_schema_changes", nil); err == nil {
		t.Error("the call check let a run name a schema tool it never listed")
	}
}
