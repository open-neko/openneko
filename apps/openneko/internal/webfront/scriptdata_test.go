package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func decodeJS(t *testing.T, s string) any {
	t.Helper()
	v, err := jsParse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

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

func TestScriptDataToolIsHiddenFromTheModel(t *testing.T) {
	tools := &fakeTools{
		reply:   decodeJS(t, `{"content":[{"type":"text","text":"{\"data\":{\"a\":1}}"}]}`),
		gjTools: decodeJS(t, `[{"name":"execute_graphql","inputSchema":{"type":"object"}}]`),
	}
	b := turnBinding{OrgID: "org-1", RunID: "run-1", Kind: "work"}
	s := newToolServer(graphjinTools(b, tools))
	call := json.RawMessage(`{"name":"script_data_query","arguments":{"query":"{ a }","variables":{"x":1}}}`)
	if _, rpcErr := s.handle(context.Background(), "tools/call", call); rpcErr == nil || !strings.Contains(rpcErr.Message, "unknown tool script_data_query") {
		t.Fatalf("a run with no script data: %v", rpcErr)
	}
	dataCall, err := scriptDataCall(b, tools)
	if err != nil {
		t.Fatal(err)
	}
	s.scriptData = scriptDataTool(dataCall)
	listed, rpcErr := s.handle(context.Background(), "tools/list", nil)
	if rpcErr != nil || strings.Contains(string(mustJSON(listed)), "script_data") {
		t.Fatalf("tools/list: %s %v", mustJSON(listed), rpcErr)
	}
	result, rpcErr := s.handle(context.Background(), "tools/call", call)
	if rpcErr != nil || string(mustJSON(decodeAny(result.(json.RawMessage)))) != `{"content":[{"text":"{\"data\":{\"a\":1}}","type":"text"}]}` {
		t.Fatalf("tools/call: %s %v", result, rpcErr)
	}
	if got := string(mustJSON(tools.calls[len(tools.calls)-1])); got != `{"input":{"arguments":{"query":"{ a }","variables":{"x":1}},"name":"execute_graphql","orgId":"org-1","runId":"run-1"},"method":"callGraphjinTool"}` {
		t.Fatalf("calls: %s", got)
	}
	if _, rpcErr := s.handle(context.Background(), "tools/call", json.RawMessage(`{"name":"script_data_query","arguments":{}}`)); rpcErr == nil {
		t.Fatal("an empty query passed")
	}
	tools.throws = "No data source is available to this run."
	if _, rpcErr := s.handle(context.Background(), "tools/call", call); rpcErr == nil || !strings.Contains(rpcErr.Message, "No data source is available to this run.") {
		t.Fatalf("a refusal: %v", rpcErr)
	}
}

func TestScriptDataBrokerPolicy(t *testing.T) {
	tools := &fakeTools{reply: decodeJS(t, `{"content":[{"type":"text","text":"{\"data\":{\"a\":1}}"}]}`)}
	governed := turnBinding{OrgID: "org-1", RunID: "run-1", Kind: "work", GraphjinToolPolicy: "direct-governed"}
	broker, err := scriptDataBroker(governed, tools)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker(context.Background(), sandbox.DataQuery{Query: " "}); err == nil || !strings.Contains(err.Error(), "requires execute_graphql.query") {
		t.Fatalf("the governed policy checks the query: %v", err)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("calls: %v", tools.calls)
	}
	if _, err := scriptDataBroker(turnBinding{Kind: "work", GraphjinToolPolicy: "other"}, tools); err == nil {
		t.Fatal("an unknown policy passed")
	}
	agent, _ := scriptDataBroker(turnBinding{OrgID: "org-1", RunID: "run-9", Kind: "agent-job"}, tools)
	if data, err := agent(context.Background(), sandbox.DataQuery{Query: "{ a }", Namespace: "sales"}); err != nil || string(data) != `{"a":1}` {
		t.Fatalf("agent job: %s %v", data, err)
	}
	if got := string(mustJSON(tools.calls)); got != `[{"input":{"arguments":{"namespace":"sales","query":"{ a }"},"name":"execute_graphql","orgId":"org-1"},"method":"callGraphjinTool"}]` {
		t.Fatalf("an agent job calls with no run: %s", got)
	}
}

var (
	sandboxBuild     sync.Once
	sandboxBinary    string
	sandboxBuildFail string
)

// startRecipeSandbox runs the sandbox recipe as this user, on a Unix socket
// or, with a key, on TCP.
func startRecipeSandbox(t *testing.T, tcp bool) sandbox.Config {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	sandboxBuild.Do(func() {
		dir, err := os.MkdirTemp("", "openneko-sandbox-bin-")
		if err != nil {
			sandboxBuildFail = err.Error()
			return
		}
		sandboxBinary = filepath.Join(dir, "openneko-sandbox")
		cmd := exec.Command("go", "build", "-o", sandboxBinary, ".")
		cmd.Dir = "../../../../recipes/sandbox"
		if out, err := cmd.CombinedOutput(); err != nil {
			sandboxBuildFail = string(out)
		}
	})
	if sandboxBuildFail != "" {
		t.Fatalf("sandbox build: %s", sandboxBuildFail)
	}
	root := t.TempDir()
	cfg := sandbox.Config{URL: "unix://" + filepath.Join(root, "sandbox.sock")}
	listen := cfg.URL
	if tcp {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listen = ln.Addr().String()
		ln.Close()
		cfg = sandbox.Config{URL: "http://" + listen, Key: "sandbox-key-1"}
	}
	cmd := exec.Command(sandboxBinary, "--listen", listen)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "OPENNEKO_SANDBOX_WORKSPACES=" + filepath.Join(root, "runs"),
		"OPENNEKO_SANDBOX_HOMES=" + filepath.Join(root, "homes"), "OPENNEKO_SANDBOX_KEY=" + cfg.Key}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := sandbox.Probe(ctx, cfg)
		cancel()
		if err == nil {
			return cfg
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sandbox did not start: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type scriptRun struct {
	tools  *fakeTools
	client *sandbox.Client
	ws     agentWorkspace
}

func newScriptRun(t *testing.T, cfg sandbox.Config, reply string) *scriptRun {
	t.Helper()
	org := t.TempDir()
	run := filepath.Join(org, "runs", "r1")
	ws := agentWorkspace{OrgRoot: org, RunRoot: run, ArtifactRoot: filepath.Join(run, "artifacts"), BinRoot: filepath.Join(run, "bin")}
	if err := installDataClient(ws.BinRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ws.ArtifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := &fakeTools{reply: decodeJS(t, `{"content":[{"type":"text","text":`+jsStringify(reply, false)+`}]}`)}
	broker, err := scriptDataBroker(turnBinding{OrgID: "org-1", RunID: "run-1234567890abcdef", Kind: "work"}, tools)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sandbox.NewClient(cfg, org)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetData(run, broker, sandbox.DefaultDataLimits); err != nil {
		t.Fatal(err)
	}
	return &scriptRun{tools: tools, client: client, ws: ws}
}

func (r *scriptRun) exec(t *testing.T, command string) (int, string) {
	t.Helper()
	res, err := r.client.ExecIn(context.Background(), r.ws.RunRoot, command, "",
		map[string]string{"RUN_DIR": r.ws.RunRoot, "ARTIFACT_DIR": r.ws.ArtifactRoot, "PYTHONPATH": r.ws.BinRoot}, 60, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	return res.ExitCode, res.Output
}

const reportScript = `python3 -c 'import json
from neko_data import query
orders = query("query { orders { id } }", {"x": 1})["orders"]
open("artifacts/orders.json", "w").write(json.dumps(orders))
print(len(orders), json.dumps(query("query { leads { email } }")))'`

func TestScriptDataInALocalSandbox(t *testing.T) {
	r := newScriptRun(t, startRecipeSandbox(t, false), `{"data":{"orders":[{"id":1},{"id":2}],"leads":[{"email":"a@b.co"}]}}`)
	code, out := r.exec(t, reportScript)
	if code != 0 || out != "2 {\"orders\": [{\"id\": 1}, {\"id\": 2}], \"leads\": [{\"email\": \"a@b.co\"}]}\n" {
		t.Fatalf("script: %d %q", code, out)
	}
	if got := string(mustJSON(r.tools.calls)); got != `[{"input":{"arguments":{"query":"query { orders { id } }","variables":{"x":1}},"name":"execute_graphql","orgId":"org-1","runId":"run-1234567890abcdef"},"method":"callGraphjinTool"},`+
		`{"input":{"arguments":{"query":"query { leads { email } }"},"name":"execute_graphql","orgId":"org-1","runId":"run-1234567890abcdef"},"method":"callGraphjinTool"}]` {
		t.Fatalf("calls: %s", got)
	}
	if strings.Join(r.tools.audits, ",") != "/v1/graphjin/tools/call,/v1/graphjin/tools/call" {
		t.Fatalf("audits: %v", r.tools.audits)
	}
	if saved, err := os.ReadFile(filepath.Join(r.ws.ArtifactRoot, "orders.json")); err != nil || string(saved) != `[{"id": 1}, {"id": 2}]` {
		t.Fatalf("artifact: %s %v", saved, err)
	}

	again, out2 := r.exec(t, reportScript)
	if again != 0 || out2 != out || len(r.tools.calls) != 2 {
		t.Fatalf("a rerun reads the record: %d %q, %d calls", again, out2, len(r.tools.calls))
	}

	r.tools.throws = "No data source is available to this run."
	code, out = r.exec(t, `python3 -c 'from neko_data import query
query("query { stock { sku } }")'`)
	if code == 0 || !strings.Contains(out, "NekoDataError: No data source is available to this run.") {
		t.Fatalf("a refused query: %d %s", code, out)
	}
}

func TestScriptDataInAnExternalSandbox(t *testing.T) {
	cfg := startRecipeSandbox(t, true)
	r := newScriptRun(t, cfg, `{"result":"{\"data\":{\"leads\":[{\"email\":\"a@b.co\"}]}}"}`)
	code, out := r.exec(t, `"$RUN_DIR/bin/neko-query" 'query { leads { email } }'`)
	if code != 0 || out != "{\"leads\": [{\"email\": \"a@b.co\"}]}\n" {
		t.Fatalf("neko-query: %d %q", code, out)
	}

	r = newScriptRun(t, cfg, `{"data":null,"errors":[{"message":"invalid query: no selectors found"}]}`)
	code, out = r.exec(t, `"$RUN_DIR/bin/neko-query" 'query { __typename }'`)
	if code != 1 || !strings.Contains(out, "neko-query: [{\"message\":\"invalid query: no selectors found\"}]") {
		t.Fatalf("GraphQL errors: %d %q", code, out)
	}

	bad := sandbox.Config{URL: cfg.URL, Key: "wrong"}
	if err := sandbox.Probe(context.Background(), bad); err == nil {
		t.Fatal("the TCP sandbox took a wrong key")
	}
}

func TestScriptDataSandboxHoldsNoSecret(t *testing.T) {
	planted := "planted-broker-token-7f3a"
	t.Setenv("OPENNEKO_BROKER_TOKEN", planted)
	t.Setenv("OPENNEKO_SERVE_TOKEN", planted)
	t.Setenv("OPENNEKO_GRAPHJIN_TOKEN", planted)
	r := newScriptRun(t, startRecipeSandbox(t, false), `{"data":{"orders":[{"id":1}]}}`)
	code, out := r.exec(t, `python3 -c 'from neko_data import query; query("query { orders { id } }")' && env && echo FILES && find "$RUN_DIR" -type s && grep -rl `+planted+` "$RUN_DIR" "$HOME" ; echo DONE`)
	if code != 0 || !strings.Contains(out, "FILES\nDONE") || strings.Contains(out, planted) {
		t.Fatalf("sandbox output: %d %s", code, out)
	}
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "NODE_PATH": true, "SKILL_DIR": true,
		"RUN_DIR": true, "ARTIFACT_DIR": true, "PYTHONPATH": true, "PWD": true, "SHLVL": true, "_": true, "OLDPWD": true}
	envPart, _, _ := strings.Cut(out, "FILES\n")
	for _, line := range strings.Split(strings.TrimSpace(envPart), "\n") {
		name, _, _ := strings.Cut(line, "=")
		if !allowed[name] {
			t.Errorf("the sandbox env holds %s", line)
		}
	}
	record, err := os.ReadFile(filepath.Join(r.ws.RunRoot, ".openneko", "script-data.jsonl"))
	if err != nil || !strings.Contains(string(record), `"rowCount":1`) {
		t.Fatalf("record: %s %v", record, err)
	}
}

func TestScriptDataOutsideOpenNeko(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	var got []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, fmt.Sprint(r.URL.Path, " ", body["query"], " ", body["variables"]))
		_, _ = w.Write([]byte(`{"data":{"orders":[{"id":5}]}}`))
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	bin := filepath.Join(t.TempDir(), "bin")
	if err := installDataClient(bin); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-c", `import json
from neko_data import query
print(json.dumps(query("query { orders { id } }", {"n": 2})))`)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONPATH=" + bin, "NEKO_DATA_GRAPHJIN_URL=http://" + ln.Addr().String() + "/api/v1/graphql"}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != `{"orders": [{"id": 5}]}` || len(got) != 1 || got[0] != "/api/v1/graphql query { orders { id } } map[n:2]" {
		t.Fatalf("%s %v %v", out, err, got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(bin)); len(entries) != 1 {
		t.Fatalf("the GraphJin transport wrote request files: %v", entries)
	}
}

func TestScriptDataInAWorkflowScriptStep(t *testing.T) {
	cfg := startRecipeSandbox(t, false)
	root := t.TempDir()
	ws := stepTestWorkspace(t, root)
	script := filepath.Join(ws.SkillsRoot, "stock", "report.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("import json, os\nfrom neko_data import query\nrows = query('query { stock { sku } }')['stock']\n"+
		"print(len(rows), os.environ.get('OPENNEKO_WORKFLOW_INPUT'))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &fakeTools{reply: decodeJS(t, `{"content":[{"type":"text","text":"{\"data\":{\"stock\":[{\"sku\":\"A\"},{\"sku\":\"B\"}]}}"}]}`)}
	broker, err := scriptDataBroker(turnBinding{OrgID: "org-1", RunID: "run-1", Kind: "workflow", GraphjinToolPolicy: "direct-governed"}, tools)
	if err != nil {
		t.Fatal(err)
	}
	steps := decodeJS(t, `[{"id":"s1","description":"Stock","script":{"skill":"stock","command":["python3","${SKILL_DIR}/report.py"]}}]`)
	log := &eventLog{}
	results, err := runScriptSteps(context.Background(), scriptStepsInput{Steps: jsArray(steps), Workspace: ws,
		Input: decodeJS(t, `{"store":"north"}`), Allowed: []string{"stock"}, Sandbox: &cfg, Data: broker, Emit: log.emit})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "ok" || results[0].OutputTail != `2 {"store":"north"}` {
		t.Fatalf("results: %s", mustJSON(results))
	}
	if got := string(mustJSON(tools.calls)); got != `[{"input":{"arguments":{"query":"query { stock { sku } }"},"name":"execute_graphql","orgId":"org-1","runId":"run-1"},"method":"callGraphjinTool"}]` {
		t.Fatalf("calls: %s", got)
	}

	results, err = runScriptSteps(context.Background(), scriptStepsInput{Steps: jsArray(steps), Workspace: ws,
		Input: decodeJS(t, `{}`), Allowed: []string{"stock"}, Sandbox: &cfg, Emit: log.emit})
	if err != nil || len(results) != 1 || results[0].Status != "failed" || !strings.Contains(results[0].OutputTail, "ModuleNotFoundError") {
		t.Fatalf("a run with no data broker gets no client: %s %v", mustJSON(results), err)
	}
}

func TestDeclaredQueriesInTheRealSandbox(t *testing.T) {
	cfg := startRecipeSandbox(t, false)
	ws := stepTestWorkspace(t, t.TempDir())
	skill := filepath.Join(ws.SkillsRoot, "stock")
	for name, content := range map[string]string{
		"report.py": "import json, os, sys\nd = os.path.join(os.environ['RUN_DIR'], '.neko-data', 'queries')\n" +
			"rows = json.load(open(os.path.join(d, 'stock.json')))['stock']\nprint(len(rows), sys.argv[1:])\n",
		"report.py" + sandbox.DeclaredSuffix: `{"queries": {"stock": {"query": "query ($store: String!) { stock(where: {store: {eq: $store}}) { sku } }", "params": {"store": {}}}}}`,
		"plain.py":                           "print('no data')\n",
	} {
		if err := os.MkdirAll(skill, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tools := &fakeTools{reply: decodeJS(t, `{"content":[{"type":"text","text":"{\"data\":{\"stock\":[{\"sku\":\"A\"},{\"sku\":\"B\"}]}}"}]}`)}
	broker, err := scriptDataBroker(turnBinding{OrgID: "org-1", RunID: "run-1", Kind: "workflow", GraphjinToolPolicy: "direct-governed"}, tools)
	if err != nil {
		t.Fatal(err)
	}
	steps := decodeJS(t, `[{"id":"s1","description":"Stock","script":{"skill":"stock","command":["python3","${SKILL_DIR}/report.py","--store","${input.store}"]}},`+
		`{"id":"s2","description":"Plain","script":{"skill":"stock","command":["python3","${SKILL_DIR}/plain.py"]}}]`)
	log := &eventLog{}
	results, err := runScriptSteps(context.Background(), scriptStepsInput{Steps: jsArray(steps), Workspace: ws,
		Input: decodeJS(t, `{"store":"north"}`), Allowed: []string{"stock"}, Sandbox: &cfg, Data: broker, Emit: log.emit})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Status != "ok" || results[0].OutputTail != `2 ['--store', 'north']` ||
		results[1].Status != "ok" || results[1].OutputTail != "no data" {
		t.Fatalf("results: %s", mustJSON(results))
	}
	if got := string(mustJSON(tools.calls)); got != `[{"input":{"arguments":{"query":"query ($store: String!) { stock(where: {store: {eq: $store}}) { sku } }","variables":{"store":"north"}},"name":"execute_graphql","orgId":"org-1","runId":"run-1"},"method":"callGraphjinTool"}]` {
		t.Fatalf("calls: %s", got)
	}

	tools.throws = "No data source is available to this run."
	results, err = runScriptSteps(context.Background(), scriptStepsInput{Steps: jsArray(steps), Workspace: ws,
		Input: decodeJS(t, `{"store":"south"}`), Allowed: []string{"stock"}, Sandbox: &cfg, Data: broker, Emit: log.emit})
	if err != nil || len(results) != 2 || results[0].Status != "failed" ||
		!strings.Contains(results[0].OutputTail, "did not start: its declared query stock failed: No data source is available to this run.") {
		t.Fatalf("a refused declared query: %s %v", mustJSON(results), err)
	}
}
