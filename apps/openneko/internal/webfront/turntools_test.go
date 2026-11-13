package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeTools struct {
	mu         sync.Mutex
	reply      any
	throws     string
	calls      []any
	audits     []string
	gjTools    any
	listThrows string
}

func (f *fakeTools) record(method string, in any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, map[string]any{"method": method, "input": decodeAny(mustJSON(in))})
	if f.throws != "" {
		return nil, errors.New(f.throws)
	}
	return f.reply, nil
}

func (f *fakeTools) recordID(method string, in any) (string, error) {
	v, err := f.record(method, in)
	id, _ := jsGet(v, "id").(string)
	return id, err
}

func (f *fakeTools) audit(_ context.Context, path string) {
	f.mu.Lock()
	f.audits = append(f.audits, path)
	f.mu.Unlock()
}
func (f *fakeTools) searchMemory(_ context.Context, in memorySearchIn) (any, error) {
	return f.record("searchWorkMemoryByContext", in)
}
func (f *fakeTools) rememberMemory(_ context.Context, in memoryRememberIn) (string, error) {
	return f.recordID("rememberWorkMemory", in)
}
func (f *fakeTools) updateMemory(_ context.Context, in memoryUpdateIn) (string, error) {
	return f.recordID("updateWorkMemory", in)
}
func (f *fakeTools) searchSkills(_ context.Context, in skillSearchIn) (any, error) {
	return f.record("searchSkillsForRun", in)
}
func (f *fakeTools) searchFiles(_ context.Context, in fileSearchIn) (any, error) {
	return f.record("searchFilesForRun", in)
}
func (f *fakeTools) readFile(_ context.Context, in fileReadIn) (any, error) {
	return f.record("readFileForRun", in)
}
func (f *fakeTools) queryFiles(_ context.Context, in fileSQLIn) (any, error) {
	return f.record("queryFileTablesForRun", in)
}
func (f *fakeTools) listGraphjinTools(_ context.Context, in graphjinCaller) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, map[string]any{"method": "listGraphjinTools", "input": decodeAny(mustJSON(in))})
	if f.listThrows != "" {
		return nil, errors.New(f.listThrows)
	}
	raw, err := jsParse([]byte(jsStringify(f.gjTools, false)))
	return raw, err
}
func (f *fakeTools) callGraphjinTool(_ context.Context, in graphjinToolCall) (any, error) {
	return f.record("callGraphjinTool", in)
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func decodeAny(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	return v
}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

var fixtureBinding = turnBinding{OrgID: "11111111-1111-4111-8111-111111111111", ThreadID: "22222222-2222-4222-8222-222222222222",
	RunID: "33333333-3333-4333-8333-333333333333", Kind: "work"}

func callTool(t *testing.T, s *toolServer, name string, args any) (string, *rpcError) {
	t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	result, rpcErr := s.handle(context.Background(), "tools/call", mustJSON(params))
	if rpcErr != nil {
		return "", rpcErr
	}
	return string(result.(json.RawMessage)), nil
}

func TestTurnToolsMatchNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/turntools.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		GraphjinTools json.RawMessage
		Lists         []struct {
			Name           string
			Servers        []string
			MemoryReadOnly bool
			Policy         string
			GraphjinTools  json.RawMessage
			ListThrows     string
			Tools          json.RawMessage
		}
		Calls []struct {
			Name           string
			Servers        []string
			MemoryReadOnly bool
			Policy         string
			ListFirst      bool
			GraphjinTools  json.RawMessage
			ListThrows     string
			Tool           string
			Arguments      json.RawMessage
			Reply          *struct {
				Value  json.RawMessage
				Throws string
			}
			Result       json.RawMessage
			Error        string
			ControlPlane json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Lists) != 6 || len(fixture.Calls) < 55 {
		t.Fatalf("fixture holds %d lists and %d calls", len(fixture.Lists), len(fixture.Calls))
	}
	gjTools := func(own json.RawMessage) any {
		if len(own) == 0 {
			own = fixture.GraphjinTools
		}
		v, err := jsParse(own)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, list := range fixture.Lists {
		b := fixtureBinding
		b.MemoryReadOnly = list.MemoryReadOnly
		b.GraphjinToolPolicy = list.Policy
		s, err := turnTools(b, &fakeTools{gjTools: gjTools(list.GraphjinTools), listThrows: list.ListThrows}, list.Servers)
		if err != nil {
			t.Fatal(err)
		}
		result, rpcErr := s.handle(context.Background(), "tools/list", nil)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if got, want := string(result.(json.RawMessage)), `{"tools":`+compactJSON(t, list.Tools)+`}`; got != want {
			t.Errorf("%s: tools/list\n got %s\nwant %s", list.Name, got, want)
		}
	}
	for _, c := range fixture.Calls {
		b := fixtureBinding
		b.MemoryReadOnly = c.MemoryReadOnly
		b.GraphjinToolPolicy = c.Policy
		fake := &fakeTools{gjTools: gjTools(c.GraphjinTools), listThrows: c.ListThrows}
		if c.Reply != nil {
			fake.throws = c.Reply.Throws
			if len(c.Reply.Value) > 0 {
				if fake.reply, err = jsParse(c.Reply.Value); err != nil {
					t.Fatal(err)
				}
			}
		}
		s, err := turnTools(b, fake, c.Servers)
		if err != nil {
			t.Fatal(err)
		}
		if c.ListFirst {
			if _, rpcErr := s.handle(context.Background(), "tools/list", nil); rpcErr != nil {
				t.Fatal(rpcErr)
			}
		}
		var args any
		_ = json.Unmarshal(c.Arguments, &args)
		got, rpcErr := callTool(t, s, c.Tool, args)
		if c.Error != "" {
			if rpcErr == nil || fmt.Sprintf("MCP error %d: %s", rpcErr.Code, rpcErr.Message) != c.Error {
				t.Errorf("%s: error %v, want %q", c.Name, rpcErr, c.Error)
			}
		} else if rpcErr != nil || !reflect.DeepEqual(decodeAny([]byte(got)), decodeAny(c.Result)) {
			t.Errorf("%s:\n got %s %v\nwant %s", c.Name, got, rpcErr, compactJSON(t, c.Result))
		}
		calls := fake.calls
		if calls == nil {
			calls = []any{}
		}
		if !reflect.DeepEqual(decodeAny(mustJSON(calls)), decodeAny(c.ControlPlane)) {
			t.Errorf("%s: control plane calls\n got %s\nwant %s", c.Name, mustJSON(calls), compactJSON(t, c.ControlPlane))
		}
		if len(fake.audits) != len(fake.calls) {
			t.Errorf("%s: %d audit rows for %d control plane calls", c.Name, len(fake.audits), len(fake.calls))
		}
	}
}

func dropKeys(o *jsObj, keys ...string) *jsObj {
	out := &jsObj{}
	for _, k := range o.ordered() {
		if !containsString(keys, k) {
			out.set(k, o.vals[k])
		}
	}
	return out
}

func TestTurnTraceMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/turntrace.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name      string
		Operation string
		Request   struct {
			Query     string
			Limit     *int
			Name      string
			Arguments json.RawMessage
		}
		Reply struct {
			Value  json.RawMessage
			Throws string
		}
		Secrets []string
		Traced  []struct {
			Event   json.RawMessage
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 13 {
		t.Fatalf("fixture holds %d cases", len(cases))
	}
	for _, c := range cases {
		runID := "run-" + c.Name
		var got []string
		var scrub func(string) string
		if c.Secrets != nil {
			scrub = secretScrubber(c.Secrets)
		}
		unregister := registerTraceSink(runID, func(event, content *jsObj) {
			got = append(got, jsStringify(dropKeys(event, "timestamp", "durationMs"), false))
			if content != nil {
				got = append(got, jsStringify(content, false))
			}
		}, scrub)
		reply, _ := jsParse(c.Reply.Value)
		execute := func() (any, error) {
			if c.Reply.Throws != "" {
				return nil, errors.New(c.Reply.Throws)
			}
			return reply, nil
		}
		binding := traceBinding{RunID: runID, OrgID: "org-1", Kind: "work"}
		var args *jsObj
		if len(c.Request.Arguments) > 0 {
			parsed, err := jsParse(c.Request.Arguments)
			if err != nil {
				t.Fatal(err)
			}
			args = parsed.(*jsObj)
		}
		for range 2 {
			switch c.Operation {
			case "memory.search":
				_, _ = traceMemorySearch(binding, c.Request.Query, c.Request.Limit, execute)
			case "files.search":
				_, _ = traceFileSearch(binding, c.Request.Query, c.Request.Limit, execute)
			case "graphjin.tools_list":
				_, _ = traceGraphjinToolsList(binding, execute)
			case "graphjin.call":
				_, _ = traceGraphjinToolCall(binding, c.Request.Name, args, execute)
			default:
				t.Fatalf("operation %s", c.Operation)
			}
		}
		unregister()
		var want []string
		for _, tr := range c.Traced {
			want = append(want, compactJSON(t, tr.Event))
			if len(tr.Content) > 0 {
				want = append(want, compactJSON(t, tr.Content))
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", c.Name, got, want)
		}
	}
	if _, err := traceMemorySearch(traceBinding{RunID: "no-sink"}, "q", nil, func() (any, error) { return []any{}, nil }); err != nil {
		t.Fatal(err)
	}
	traceSinks.Lock()
	defer traceSinks.Unlock()
	if len(traceSinks.runs) != 0 {
		t.Fatalf("sinks left: %d", len(traceSinks.runs))
	}
}

func TestSecretScrubber(t *testing.T) {
	scrub := secretScrubber([]string{"short", "sk-0123456789", "sk-0123456789-long", "a.b*c+d(e)f"})
	got := scrub("key sk-0123456789-long and sk-0123456789 and a.b*c+d(e)f, short stays")
	if got != "key [REDACTED] and [REDACTED] and [REDACTED], short stays" {
		t.Fatal(got)
	}
	if secretScrubber(nil)("sk-0123456789") != "sk-0123456789" {
		t.Fatal("an empty scrubber changed the text")
	}
}

type pipeEnd struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (p pipeEnd) Close() error {
	for _, c := range p.closers {
		_ = c.Close()
	}
	return nil
}

// The harness reaches the tools with the MCP Go SDK client, so the server
// must work with that client and with calls that overlap.
func TestTurnToolsWithMCPClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeTools{reply: []any{}}
	s, err := turnTools(fixtureBinding, fake, []string{"neko_memory", "neko_files"})
	if err != nil {
		t.Fatal(err)
	}
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- s.serve(ctx, toServer, fromServer)
		fromServer.Close()
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "ax-harness", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: toClient, Writer: pipeEnd{Writer: fromClient, closers: []io.Closer{fromClient}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "memory_search,memory_save,memory_update,files_search,files_read_text,files_query_tables" {
		t.Fatalf("tools: %v", names)
	}
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_search", Arguments: map[string]any{"query": fmt.Sprintf("q%d revenue", i)}})
			if err != nil {
				results[i] = err.Error()
				return
			}
			results[i] = res.Content[0].(*mcp.TextContent).Text
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r != `{"ok":true,"results":[]}` {
			t.Fatalf("call %d: %s", i, r)
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "files_read_text", Arguments: map[string]any{"path": "a", "maxChars": 5}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "Too small: expected number to be >=1000") {
		t.Fatalf("bad arguments: %v %+v", err, res)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "graphjin_execute_graphql", Arguments: map[string]any{}}); err == nil ||
		!strings.Contains(err.Error(), "mcp-bridge: unknown tool graphjin_execute_graphql") {
		t.Fatalf("unknown tool: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 8 || len(fake.audits) != 8 {
		t.Fatalf("%d calls, %d audits", len(fake.calls), len(fake.audits))
	}
	if _, err := turnTools(fixtureBinding, fake, []string{"neko_ui"}); err == nil {
		t.Fatal("a server that is not ported must be an error")
	}
}

const (
	runAdminTools = "b1000000-0000-4000-8000-000000000002"
	runPackTools  = "b1000000-0000-4000-8000-000000000003"
)

func TestTurnToolsDatabase(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	t.Setenv("OPENNEKO_SERVE_URL", "")
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values ($1, 'org', $2, 'ax', 'admin', 'admin')`,
		runAdminTools, threadOther)
	member := turnBinding{OrgID: "org", ThreadID: threadMine, RunID: runMine, Kind: "work"}
	s, err := turnTools(member, a.runTools(member), []string{"neko_memory", "neko_guides", "neko_files"})
	if err != nil {
		t.Fatal(err)
	}

	const planted = "sk-planted-secret-0001"
	var events, contents []string
	unregister := registerTraceSink(runMine, func(event, content *jsObj) {
		events = append(events, jsStringify(event, false))
		contents = append(contents, jsStringify(content, false))
	}, secretScrubber([]string{planted}))
	seed(t, db, `update work_memory set text = 'Revenue is net of refunds; key `+planted+`' where id = $1`, memTeam)
	got, rpcErr := callTool(t, s, "memory_search", map[string]any{"query": "refunds revenue"})
	unregister()
	if rpcErr != nil || !strings.Contains(got, memTeam) || !strings.Contains(got, `\"source\":\"saved_memory\"`) ||
		!strings.Contains(got, `\"score\":0.`) || strings.Contains(got, "Use weeks") {
		t.Fatalf("memory search: %s %v", got, rpcErr)
	}
	if len(events) != 1 || !strings.Contains(events[0], `"operation":"memory.search"`) || !strings.Contains(events[0], `"id":"`+memTeam+`","contentDigest":"sha256:`) ||
		!strings.Contains(events[0], `"layer":"team"`) || strings.Contains(events[0], planted) {
		t.Fatalf("trace events: %v", events)
	}
	if !strings.Contains(contents[0], "[REDACTED]") || strings.Contains(contents[0], planted) {
		t.Fatalf("step content: %v", contents)
	}

	got, _ = callTool(t, s, "memory_save", map[string]any{"text": "  Fiscal year starts in April  "})
	var saved struct{ MemoryID string }
	var text struct{ Content []struct{ Text string } }
	_ = json.Unmarshal([]byte(got), &text)
	_ = json.Unmarshal([]byte(text.Content[0].Text), &saved)
	m, err := a.getMemory(ctx, db, "org", saved.MemoryID)
	if err != nil || m == nil || m.Text != "Fiscal year starts in April" || deref(m.UserID) != "member" || !m.Pinned ||
		m.Kind != "business_rule" || m.Scope != "global" || deref(m.SourceRunID) != runMine || !m.IntegrityOK || m.Confidence != 0.8 {
		t.Fatalf("saved memory %+v %v from %s", m, err, got)
	}
	got, _ = callTool(t, s, "memory_save", map[string]any{"text": "This thread tracks Q4", "kind": "thread_note", "scope": "thread", "pinned": false})
	_ = json.Unmarshal([]byte(got), &text)
	_ = json.Unmarshal([]byte(text.Content[0].Text), &saved)
	if m, _ = a.getMemory(ctx, db, "org", saved.MemoryID); m == nil || deref(m.ScopeID) != threadMine || m.Pinned || m.ExpiresAt == nil {
		t.Fatalf("thread memory %+v", m)
	}

	got, _ = callTool(t, s, "memory_update", map[string]any{"memoryId": memTeam, "text": "Revenue is net of refunds and tax"})
	var override string
	if err := db.QueryRow(ctx, `select id::text from work_memory where user_id = 'member' and overrides_origin_id = $1`, memTeam).Scan(&override); err != nil ||
		!strings.Contains(got, override) {
		t.Fatalf("member update: %s %v", got, err)
	}
	var teamText string
	_ = db.QueryRow(ctx, `select text from work_memory where id = $1`, memTeam).Scan(&teamText)
	var overrideRun string
	_ = db.QueryRow(ctx, `select run_id::text from work_memory_event where memory_id = $1 and action = 'override'`, override).Scan(&overrideRun)
	if strings.Contains(teamText, "and tax") || overrideRun != runMine {
		t.Fatalf("a member's edit must not change the team row: %q, event run %q", teamText, overrideRun)
	}
	got, _ = callTool(t, s, "memory_update", map[string]any{"memoryId": memOther, "text": "Use months instead"})
	if !strings.Contains(got, `No saved memory `+memOther+` is visible to this run.`) || !strings.Contains(got, `"isError":true`) {
		t.Fatalf("hidden memory: %s", got)
	}

	admin := turnBinding{OrgID: "org", ThreadID: threadOther, RunID: runAdminTools, Kind: "work"}
	as, _ := turnTools(admin, a.runTools(admin), []string{"neko_memory", "neko_guides"})
	got, _ = callTool(t, as, "memory_update", map[string]any{"memoryId": memDatabase, "text": "AOV excludes tax and shipping"})
	_ = db.QueryRow(ctx, `select text from work_memory where id = $1`, memDatabase).Scan(&teamText)
	if !strings.Contains(got, memDatabase) || teamText != "AOV excludes tax and shipping" {
		t.Fatalf("admin update: %s, row %q", got, teamText)
	}

	seed(t, db, `insert into skill_embedding (org_id, skill_name, document) values
		('org', 'refund-report', 'refund-report'||chr(10)||'Refund report by month'),
		('org', 'graphjin-config', 'graphjin-config'||chr(10)||'Refund source config'),
		('org', 'xlsx', 'xlsx'||chr(10)||'Refund spreadsheet'),
		('org', 'churn', 'churn'||chr(10)||'Customer churn')`)
	got, _ = callTool(t, as, "guides_find", map[string]any{"query": "refund report", "limit": 5})
	if !strings.Contains(got, `\"name\":\"refund-report\"`) || !strings.Contains(got, `graphjin-config`) || strings.Contains(got, "xlsx") || strings.Contains(got, "churn") {
		t.Fatalf("admin guides: %s", got)
	}
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type = 'skill'`)
	got, _ = callTool(t, s, "guides_find", map[string]any{"query": "refund report"})
	if got != `{"content":[{"type":"text","text":"{\"skills\":[]}"}]}` {
		t.Fatalf("a member with no skill grant must find nothing: %s", got)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'skill', '*' from user_group where org_id = 'org' and slug = 'everyone'`)
	got, _ = callTool(t, s, "guides_find", map[string]any{"query": "refund report"})
	if !strings.Contains(got, "refund-report") || strings.Contains(got, "graphjin-config") {
		t.Fatalf("member guides: %s", got)
	}

	got, _ = callTool(t, s, "files_search", map[string]any{"query": "net revenue"})
	if got != `{"content":[{"type":"text","text":"File search is off: OPENNEKO_SERVE_URL is not set."}],"isError":true}` {
		t.Fatalf("files off: %s", got)
	}
	var served []string
	serve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		served = append(served, r.URL.Path+" "+string(body)+" "+fmt.Sprint(strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if r.URL.Path == "/v1/files/sql" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"only SELECT or WITH queries are allowed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"matches":[{"path":"q3.xlsx","score":2.50,"passages":[{"location":"<p1>","snippet":"net"}]}]}` + "\n"))
	}))
	defer serve.Close()
	t.Setenv("OPENNEKO_SERVE_URL", serve.URL+"/")
	got, _ = callTool(t, s, "files_search", map[string]any{"query": "net revenue", "limit": 2})
	if got != `{"content":[{"type":"text","text":"{\"matches\":[{\"path\":\"q3.xlsx\",\"score\":2.5,\"passages\":[{\"location\":\"<p1>\",\"snippet\":\"net\"}]}]}"}]}` {
		t.Fatalf("files search: %s", got)
	}
	_, _ = callTool(t, s, "files_read_text", map[string]any{"path": "q3.xlsx", "offset": 0, "maxChars": 2000})
	got, _ = callTool(t, s, "files_query_tables", map[string]any{"sql": "drop table x"})
	if got != `{"content":[{"type":"text","text":"only SELECT or WITH queries are allowed"}],"isError":true}` {
		t.Fatalf("files sql refusal: %s", got)
	}
	if strings.Join(served, "\n") != `/v1/files/search {"limit":2,"query":"net revenue"} true
/v1/files/read {"max_chars":2000,"path":"q3.xlsx"} true
/v1/files/sql {"sql":"drop table x"} true` {
		t.Fatalf("serve calls:\n%s", strings.Join(served, "\n"))
	}

	rows, err := db.Query(ctx, `select path, coalesce(actor_user_id, ''), coalesce(actor_role, ''), coalesce(backend, '') from control_plane_audit
		where run_id = $1 order by id`, runMine)
	if err != nil {
		t.Fatal(err)
	}
	var audits []string
	for rows.Next() {
		var path, user, role, backend string
		_ = rows.Scan(&path, &user, &role, &backend)
		audits = append(audits, path+" "+user+" "+role+" "+backend)
	}
	want := "/v1/memory/search member member test,/v1/memory/remember member member test,/v1/memory/remember member member test," +
		"/v1/memory/update member member test,/v1/memory/update member member test,/v1/skills/search member member test," +
		"/v1/skills/search member member test,/v1/files/search member member test,/v1/files/search member member test," +
		"/v1/files/read member member test,/v1/files/sql member member test"
	if strings.Join(audits, ",") != want {
		t.Fatalf("audit rows:\n%s", strings.Join(audits, ","))
	}
}

func TestEntitlementActorForRun(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	const install = "90000000-0000-4000-8000-000000000001"
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config) values ($1, 'org', 'shop', '1', 'installed', 'h', '{}'::jsonb)`, install)
	seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash) values
		($1, 'org', 'workflow', 'stock', 'Stock check', 'h', 'h'), ($1, 'org', 'skill', 'stock-guide', 'stock-guide', 'h', 'h')`, install)
	var workflowID string
	if err := db.QueryRow(ctx, `insert into workflow_definition (org_id, name) values ('org', 'Stock check') returning id`).Scan(&workflowID); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_role) values ($1, 'org', $2, 'ax', 'service'),
		($3, 'org', $2, 'ax', 'member'), ($4, 'org', $2, 'ax', null)`, runPackTools, threadMine, runAdminTools, "b1000000-0000-4000-8000-000000000004")
	seed(t, db, `insert into workflow_run (org_id, workflow_id, thread_id, work_run_id, trigger_kind) values ('org', $1, $2, $3, 'cron')`,
		workflowID, threadMine, runPackTools)
	for _, c := range []struct {
		run  string
		want *actor
	}{
		{runPackTools, &actor{OrgID: "org", PackID: "shop"}},
		{runAdminTools, &actor{OrgID: "org"}},
		{"b1000000-0000-4000-8000-000000000004", &actor{OrgID: "org", Admin: true}},
		{runMine, &actor{OrgID: "org", UserID: "member"}},
		{unknownUUID, nil},
	} {
		got, err := a.entitlementActorForRun(ctx, "org", c.run)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v %v, want %+v", c.run, got, err, c.want)
		}
	}
	held, err := a.heldItems(ctx, actor{OrgID: "org", PackID: "shop"}, "skill")
	if err != nil || held.All || len(held.IDs) != 1 || !held.IDs["stock-guide"] {
		t.Fatalf("pack actor skills: %+v %v", held, err)
	}
	if held, _ = a.heldItems(ctx, actor{OrgID: "org", PackID: "shop"}, "team_memory"); held.All || len(held.IDs) != 0 {
		t.Fatalf("pack actor team memory: %+v", held)
	}
}
