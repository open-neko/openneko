package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDataClientMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("../../../worker/src/agent-sandbox/data-client.ts")
	if err != nil {
		t.Fatal(err)
	}
	parts := regexp.MustCompile("(?s)String\\.raw`(.*?)`;").FindAllStringSubmatch(string(raw), -1)
	if len(parts) != 2 || parts[0][1] != nekoDataPy || parts[1][1] != nekoQueryCLI {
		t.Fatal("dataclient/neko_data.py and dataclient/neko-query differ from apps/worker/src/agent-sandbox/data-client.ts")
	}
}

func TestDataSocketEnabled(t *testing.T) {
	for _, c := range []struct {
		kind string
		read bool
		want bool
	}{{"work", false, true}, {"", false, true}, {"workflow", false, true}, {"agent-job", false, false}, {"agent-job", true, true}, {"other", true, false}} {
		if got := dataSocketEnabled(c.kind, c.read); got != c.want {
			t.Errorf("%q %v: %v", c.kind, c.read, got)
		}
	}
}

type dataRun struct {
	tools  *fakeTools
	socket *dataSocket
	env    []string
	bin    string
}

func startDataRun(t *testing.T, b turnBinding, payload string) *dataRun {
	t.Helper()
	dir := t.TempDir()
	tools := &fakeTools{
		reply:   decodeJS(t, `{"content":[{"type":"text","text":`+jsStringify(payload, false)+`}]}`),
		gjTools: decodeJS(t, `[{"name":"execute_graphql","inputSchema":{"type":"object"}},{"name":"execute_saved_query","inputSchema":{"type":"object"}}]`),
	}
	s, err := startDataSocket(dir, b, tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	bin := filepath.Join(dir, "bin")
	if err := installDataClient(bin, s.path); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "OPENNEKO_DATA_SOCKET=") && !strings.HasPrefix(kv, "PYTHONPATH=") {
			env = append(env, kv)
		}
	}
	env = append(env, "OPENNEKO_DATA_SOCKET="+s.path, "PYTHONPATH="+bin)
	return &dataRun{tools: tools, socket: s, env: env, bin: bin}
}

func decodeJS(t *testing.T, s string) any {
	t.Helper()
	v, err := jsParse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (d *dataRun) run(t *testing.T, env []string, name string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (d *dataRun) post(t *testing.T, path string, headers map[string]string, body string) (int, string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", d.socket.path)
	}}}
	req, _ := http.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, strings.TrimSpace(string(raw))
}

func TestDataSocketScripts(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	member := turnBinding{OrgID: "org-1", RunID: "run-1234567890abcdef", Kind: "work"}

	d := startDataRun(t, member, `{"data":{"orders":[{"id":1},{"id":2}]},"next":null}`)
	if info, err := os.Stat(d.socket.path); err != nil || info.Mode().Perm() != 0o600 || filepath.Base(d.socket.path) != "neko-data-run123456789.sock" {
		t.Fatalf("socket %s: %v %v", d.socket.path, info.Mode(), err)
	}
	out, stderr, err := d.run(t, d.env, "python3", "-c", "import json\nfrom neko_data import query\nprint(json.dumps(query('query { orders { id } }', {'x': 1})))")
	if err != nil || strings.TrimSpace(out) != `{"orders": [{"id": 1}, {"id": 2}]}` {
		t.Fatalf("query: %q %s %v", out, stderr, err)
	}
	if got := string(mustJSON(d.tools.calls)); got != `[{"input":{"arguments":{"query":"query { orders { id } }","variables":{"x":1}},"name":"execute_graphql",`+
		`"orgId":"org-1","runId":"run-1234567890abcdef"},"method":"callGraphjinTool"}]` {
		t.Fatalf("calls: %s", got)
	}
	if strings.Join(d.tools.audits, ",") != "/v1/graphjin/tools/call" {
		t.Fatalf("audits: %v", d.tools.audits)
	}

	scrubbed := []string{"PYTHONPATH=" + d.bin, "PATH=" + os.Getenv("PATH")}
	if out, stderr, err := d.run(t, scrubbed, "python3", "-c", "import json\nfrom neko_data import query\nprint(json.dumps(query('{ a }')))"); err != nil ||
		strings.TrimSpace(out) != `{"orders": [{"id": 1}, {"id": 2}]}` {
		t.Fatalf("with no socket variable: %q %s %v", out, stderr, err)
	}

	d = startDataRun(t, member, `{"result":"{\"data\":{\"leads\":[{\"email\":\"a@b.co\"}]}}"}`)
	if out, stderr, err := d.run(t, d.env, filepath.Join(d.bin, "neko-query"), "query { leads { email } }"); err != nil ||
		strings.TrimSpace(out) != `{"leads": [{"email": "a@b.co"}]}` {
		t.Fatalf("neko-query: %q %s %v", out, stderr, err)
	}
	if info, _ := os.Stat(filepath.Join(d.bin, "neko-query")); info.Mode().Perm() != 0o755 {
		t.Fatalf("neko-query mode %v", info.Mode())
	}

	d = startDataRun(t, member, `{"data":null,"errors":[{"message":"invalid query: no selectors found"}]}`)
	var exit *exec.ExitError
	if _, stderr, err := d.run(t, d.env, filepath.Join(d.bin, "neko-query"), "query { __typename }"); !errors.As(err, &exit) || exit.ExitCode() != 1 ||
		!strings.Contains(stderr, "no selectors found") {
		t.Fatalf("GraphQL errors: %s %v", stderr, err)
	}

	d.tools.throws = "No data source is available to this run."
	if _, stderr, err := d.run(t, d.env, "python3", "-c", "from neko_data import query\nquery('{ a }')"); err == nil ||
		!strings.Contains(stderr, "NekoDataError: No data source is available to this run.") {
		t.Fatalf("a refused call: %s %v", stderr, err)
	}

	governed := member
	governed.GraphjinToolPolicy = "direct-governed"
	d = startDataRun(t, governed, `{"data":{}}`)
	if _, stderr, err := d.run(t, d.env, "python3", "-c", "from neko_data import call_tool\ncall_tool('execute_saved_query', {'name': 'x'})"); err == nil ||
		!strings.Contains(stderr, "NekoDataError") || len(d.tools.calls) != 0 {
		t.Fatalf("policy: %s %v %v", stderr, err, d.tools.calls)
	}

	path := d.socket.path
	if err := d.socket.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file after close: %v", err)
	}
}

func TestDataSocketTransport(t *testing.T) {
	json2 := map[string]string{"accept": "application/json, text/event-stream", "content-type": "application/json"}
	call := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"execute_graphql","arguments":{"query":"{ a }"}}}`

	workflow := startDataRun(t, turnBinding{OrgID: "org-1", RunID: "run-9", Kind: "workflow", GraphjinToolPolicy: "direct-governed"}, `{"data":{}}`)
	if status, body := workflow.post(t, "/mcp", json2, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); status != 200 ||
		!strings.Contains(body, `"name":"execute_saved_query"`) {
		t.Fatalf("a workflow run has no tool policy: %d %s", status, body)
	}
	agent := startDataRun(t, turnBinding{OrgID: "org-1", RunID: "run-9", Kind: "agent-job"}, `{"data":{}}`)
	agent.post(t, "/mcp", json2, call)
	if got := string(mustJSON(agent.tools.calls)); got != `[{"input":{"arguments":{"query":"{ a }"},"name":"execute_graphql","orgId":"org-1"},"method":"callGraphjinTool"}]` {
		t.Fatalf("an agent job calls with no run: %s", got)
	}

	d := startDataRun(t, turnBinding{OrgID: "org-1", RunID: "run-1", Kind: "work", GraphjinToolPolicy: "direct-governed"}, `{"data":{"a":1}}`)
	for _, c := range []struct {
		path    string
		headers map[string]string
		body    string
		status  int
		want    string
	}{
		{"/mcp", json2, call, 200, `{"id":7,"jsonrpc":"2.0","result":{"content":[{"text":"{\"data\":{\"a\":1}}","type":"text"}]}}`},
		{"/other", json2, call, 404, ``},
		{"/mcp?x=1", json2, call, 404, ``},
		{"/mcp", map[string]string{"accept": "application/json", "content-type": "application/json"}, call, 406,
			`{"error":{"code":-32000,"message":"Not Acceptable: Client must accept both application/json and text/event-stream"},"id":null,"jsonrpc":"2.0"}`},
		{"/mcp", map[string]string{"accept": json2["accept"], "content-type": "text/plain"}, call, 415,
			`{"error":{"code":-32000,"message":"Unsupported Media Type: Content-Type must be application/json"},"id":null,"jsonrpc":"2.0"}`},
		{"/mcp", json2, `{`, 400, `{"error":{"code":-32700,"message":"Parse error: Invalid JSON"},"id":null,"jsonrpc":"2.0"}`},
		{"/mcp", json2, `{"id":1}`, 400, `{"error":{"code":-32700,"message":"Parse error: Invalid JSON-RPC message"},"id":null,"jsonrpc":"2.0"}`},
		{"/mcp", map[string]string{"accept": json2["accept"], "content-type": "application/json", "mcp-protocol-version": "1999-01-01"}, call, 400,
			`{"error":{"code":-32000,"message":"Bad Request: Unsupported protocol version: 1999-01-01 (supported versions: 2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05, 2024-10-07)"},"id":null,"jsonrpc":"2.0"}`},
		{"/mcp", json2, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, 202, ``},
		{"/mcp", json2, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, 200,
			`{"id":2,"jsonrpc":"2.0","result":{"tools":[{"description":"OpenNeko exposes direct execution only; GraphJin enforces source and operation authorization.",` +
				`"inputSchema":{"type":"object"},"name":"execute_graphql"}]}}`},
		{"/mcp", json2, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"execute_saved_query","arguments":{}}}`, 200, `-32603`},
		{"/mcp", json2, `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`, 200,
			`{"error":{"code":-32601,"message":"Method not found"},"id":4,"jsonrpc":"2.0"}`},
		{"/mcp", json2, `[{"jsonrpc":"2.0","id":5,"method":"ping"},{"jsonrpc":"2.0","id":6,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}]`, 200,
			`[{"id":5,"jsonrpc":"2.0","result":{}},{"id":6,"jsonrpc":"2.0","result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"neko_graphjin","version":"1.0.0"}}}]`},
	} {
		status, body := d.post(t, c.path, c.headers, c.body)
		if status != c.status || (c.want != "-32603" && sortedJSON(body) != c.want) || (c.want == "-32603" && !strings.Contains(body, c.want)) {
			t.Errorf("%s %s: %d %s", c.path, c.body, status, body)
		}
	}
}

func sortedJSON(s string) string {
	if s == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	return string(mustJSON(v))
}
