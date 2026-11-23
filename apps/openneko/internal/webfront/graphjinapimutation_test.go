package webfront

import (
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

const apiMutationConfig = `sources:
  - name: shop
    kind: database
    type: postgres
    read_only: %s
  - name: selection_api
    kind: api
    specs_dir: /config/specs
    specs:
      selection-api:
        base_url: http://fixture.invalid
        operations:
          selectFulfillmentRoute:
            expose_as: select_fulfillment_route
            expose_mutation: true
`

const apiMutationSpec = `openapi: 3.0.3
paths:
  /selections:
    post:
      operationId: selectFulfillmentRoute
`

const apiMutationQuery = `mutation Pick($request: JSON) { select_fulfillment_route(request: $request) { receipt } }`

func writeAPIMutationConfig(t *testing.T, readOnly string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "specs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "specs", "selection-api.yaml"), []byte(apiMutationSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "graphjin.yml")
	if err := os.WriteFile(file, []byte(strings.Replace(apiMutationConfig, "%s", readOnly, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestPlanGraphjinAPIMutation(t *testing.T) {
	file := writeAPIMutationConfig(t, "true")
	vars := map[string]any{"request": map[string]any{"body": map[string]any{"order_id": "SO-43659", "priority": "expedite"}}}
	plan, err := planGraphjinAPIMutation(file, apiMutationQuery, vars)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsStringify(plan.payload(), false); !strings.Contains(got, `"source":"selection_api","operation":"select_fulfillment_route","method":"POST","path":"/selections","body":{"request":{"body":{"order_id":"SO-43659","priority":"expedite"}}}`) {
		t.Fatalf("payload: %s", got)
	}
	if plan, err := planGraphjinAPIMutation(file, `query { orders { id } }`, nil); plan != nil || err != nil {
		t.Fatalf("a query: %v %v", plan, err)
	}
	for query, want := range map[string]string{
		`mutation { orders(update: {note: "x"}, where: {id: {eq: 1}}) { id } }`:                                     "orders is not an API operation",
		`mutation { select_fulfillment_route(request: {}) { receipt } orders(delete: true) { id } }`:                "orders is not an API operation",
		`mutation { a: select_fulfillment_route(request: {}) { receipt } b: select_fulfillment_route { receipt } }`: "one API operation",
		`mutation { ...F } fragment F on Mutation { orders { id } }`:                                                "fragment",
		`mutation { select_fulfillment_route(`:                                                                      "cannot parse",
	} {
		if _, err := planGraphjinAPIMutation(file, query, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", query, err, want)
		}
	}
	if _, err := planGraphjinAPIMutation(writeAPIMutationConfig(t, "false"), apiMutationQuery, nil); err == nil || !strings.Contains(err.Error(), `"shop" is not read_only`) {
		t.Errorf("a writable database source: %v", err)
	}
	if _, err := planGraphjinAPIMutation("", apiMutationQuery, nil); err == nil || !strings.Contains(err.Error(), "cannot prove") {
		t.Errorf("no config: %v", err)
	}
}

func TestRunGraphjinAPIMutationBecomesAnApprovalRequest(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", writeAPIMutationConfig(t, "true"))
	if err := a.seedDefaultActionPolicies(t.Context(), "org"); err != nil {
		t.Fatal(err)
	}
	var (
		mu        sync.Mutex
		mutations []string
		saved     = apiMutationQuery
	)
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
		var result any
		switch {
		case req.Method == "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "execute_graphql", "inputSchema": map[string]any{"type": "object"}},
				map[string]any{"name": "execute_saved_query", "inputSchema": map[string]any{"type": "object"}},
				map[string]any{"name": "update_current_config", "inputSchema": map[string]any{"type": "object"}},
			}}
		case req.Params.Name == "get_saved_query":
			mu.Lock()
			text, _ := json.Marshal(map[string]any{"name": "pick", "operation": "mutation", "query": saved})
			mu.Unlock()
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}}
		default:
			mu.Lock()
			mutations = append(mutations, req.Params.Name+" "+string(mustJSON(req.Params.Arguments)))
			mu.Unlock()
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"data":{"select_fulfillment_route":{"receipt":"AW-API-SELECTION-APPROVED"}}}`}}}
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer gj.Close()
	sourceID := rowText(t, db, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, is_default, enabled, auth_mode)
		values ('org', 'graphjin', 'shop', $1, $2, true, true, 'jwt') returning id::text`, gj.URL+"/api/v1/graphql", gj.URL+"/api/v1/mcp")
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'data_source', $1 from user_group where org_id = 'org' and slug = 'everyone'`, sourceID)
	sent := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := mutations
		mutations = nil
		return out
	}

	b := turnBinding{OrgID: "org", RunID: runMine, Kind: "work"}
	s := newToolServer(graphjinTools(b, a.runTools(b)))
	call := func(name string, args map[string]any) (string, string) {
		raw, _ := json.Marshal(map[string]any{"name": "graphjin_" + name, "arguments": args})
		result, rpcErr := s.handle(t.Context(), "tools/call", raw)
		if rpcErr != nil {
			return "", rpcErr.Message
		}
		return string(mustJSON(decodeAny(result.(json.RawMessage)))), ""
	}
	listed, rpcErr := s.handle(t.Context(), "tools/list", nil)
	if rpcErr != nil || strings.Contains(string(mustJSON(listed)), "update_current_config") {
		t.Fatalf("tools/list: %s %v", mustJSON(listed), rpcErr)
	}
	if _, refusal := call("update_current_config", map[string]any{"config": map[string]any{}}); refusal == "" {
		t.Fatalf("a run reached a config tool: %q", refusal)
	}

	variables := map[string]any{"request": map[string]any{"body": map[string]any{"order_id": "SO-43659", "priority": "expedite"}}}
	out, refusal := call("execute_graphql", map[string]any{"query": apiMutationQuery, "variables": variables})
	if refusal != "" || !strings.Contains(out, `pending_approval`) {
		t.Fatalf("API mutation: %s %q", out, refusal)
	}
	if got := sent(); len(got) != 0 {
		t.Fatalf("the run sent %v before an approval", got)
	}
	requestID := rowText(t, db, `select id::text from action_request where org_id = 'org' and kind = 'graphjin.api_mutation' and work_run_id = $1`, runMine)
	if got := rowText(t, db, `select status || '|' || coalesce(target, '') || '|' || (payload->>'path') || '|' || (payload->'body'->'request'->'body'->>'order_id') from action_request where id = $1`, requestID); got != "pending_approval|select_fulfillment_route|/selections|SO-43659" {
		t.Fatalf("request: %s", got)
	}
	facts := a.approvalFactsFor(t.Context(), "org", graphjinAPIMutationKind, decodeJS(t, rowText(t, db, `select payload::text from action_request where id = $1`, requestID)))
	if facts.Headline != "Change data through an API: select_fulfillment_route on selection_api" || !strings.Contains(strings.Join(facts.Lines, "\n"), "path: /selections") {
		t.Fatalf("approval card: %+v", facts)
	}

	seed(t, db, `update action_request set status = 'approved' where id = $1`, requestID)
	if err := a.ActionExecute(t.Context(), "org", requestID); err != nil {
		t.Fatal(err)
	}
	if got := sent(); len(got) != 1 || !strings.HasPrefix(got[0], "execute_graphql ") || !strings.Contains(got[0], "SO-43659") {
		t.Fatalf("approval sent %v", got)
	}
	if got := rowText(t, db, `select r.status || '|' || e.status || '|' || e.command_or_operation from action_request r join action_execution e on e.action_request_id = r.id where r.id = $1`, requestID); got != "executed|succeeded|selection_api.select_fulfillment_route" {
		t.Fatalf("outcome: %s", got)
	}
	if err := a.ActionExecute(t.Context(), "org", requestID); err == nil {
		t.Fatal("an executed request ran again")
	}
	if got := sent(); len(got) != 0 {
		t.Fatalf("a second execute sent %v", got)
	}

	if _, refusal := call("execute_graphql", map[string]any{"query": `mutation { orders(update: {note: "x"}, where: {id: {eq: 1}}) { id } }`}); !strings.Contains(refusal, "GraphJin mutation blocked") {
		t.Fatalf("a database mutation: %q", refusal)
	}
	if _, err := a.runTools(b).planGraphjinMutation(t.Context(), graphjinToolCall{OrgID: "org", Name: "execute_graphql",
		Arguments: jsObjOf("query", apiMutationQuery)}); err == nil || !strings.Contains(err.Error(), "only a run") {
		t.Fatalf("a call with no run: %v", err)
	}

	out, refusal = call("execute_saved_query", map[string]any{"name": "pick", "variables": variables})
	if refusal != "" || !strings.Contains(out, "pending_approval") {
		t.Fatalf("saved mutation: %s %q", out, refusal)
	}
	if got := sent(); len(got) != 0 {
		t.Fatalf("the saved mutation sent %v before an approval", got)
	}
	savedID := rowText(t, db, `select id::text from action_request where kind = 'graphjin.api_mutation' and payload->>'savedQuery' = 'pick'`)
	seed(t, db, `update action_request set status = 'approved' where id = $1`, savedID)
	mu.Lock()
	saved = `mutation Pick { orders(delete: true) { id } }`
	mu.Unlock()
	_ = a.ActionExecute(t.Context(), "org", savedID)
	if got := sent(); len(got) != 0 {
		t.Fatalf("a saved query changed after approval sent %v", got)
	}
	if got := rowText(t, db, `select status from action_request where id = $1`, savedID); got != "failed" {
		t.Fatalf("a changed saved query: %s", got)
	}
}
