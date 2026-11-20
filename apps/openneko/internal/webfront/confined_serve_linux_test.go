package webfront

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/files"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/neko/apps/openneko/internal/native"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

var (
	openNekoBuild     sync.Once
	openNekoBinary    string
	openNekoBuildFail string
)

// releaseBinary builds openneko as the release does, with CGO_ENABLED=0.
func releaseBinary(t *testing.T) string {
	t.Helper()
	openNekoBuild.Do(func() {
		dir, err := os.MkdirTemp("", "openneko-bin-")
		if err != nil {
			openNekoBuildFail = err.Error()
			return
		}
		openNekoBinary = filepath.Join(dir, "openneko")
		cmd := exec.Command("go", "build", "-o", openNekoBinary, "./cmd/openneko")
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			openNekoBuildFail = string(out)
		}
	})
	if openNekoBuildFail != "" {
		t.Fatalf("openneko build: %s", openNekoBuildFail)
	}
	return openNekoBinary
}

type runningServe struct {
	socket, addr, gjAddr string
	tokenKey             []byte
	state                confine.State
}

var listenLine = regexp.MustCompile(`openneko serve listening on (\S+)`)

func freeTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// confinedServe starts openneko serve in a one-account layout, in production
// mode, with a GraphJin child and the sandbox at sandboxURL.
func confinedServe(t *testing.T, l native.Layout, home, dsn, sandboxURL string) runningServe {
	t.Helper()
	bin := releaseBinary(t)
	rs := runningServe{socket: native.ServeEnv(l)["OPENNEKO_SERVE_SOCKET"], gjAddr: freeTCP(t), tokenKey: bytes.Repeat([]byte{7}, 32)}
	creds := l.Credentials()
	if err := os.WriteFile(filepath.Join(creds, "token.key"), []byte(hex.EncodeToString(rs.tokenKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(creds, "database.url"), []byte(dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "graphjin", "customer.sources.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	conf := strings.NewReplacer(
		"REPLACE_WITH_BASE64_32_BYTE_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"REPLACE_WITH_PER_ORG_SECRET_B64", "c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LXNlY3JldA==",
	).Replace(string(seed))
	if err := os.WriteFile(l.GraphJin(), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"OPENNEKO_ENV=production", "GO_ENV=agentic", "OPENNEKO_SANDBOX_URL=" + sandboxURL}
	for k, v := range native.ServeEnv(l) {
		env = append(env, k+"="+v)
	}
	env = append(env, "OPENNEKO_SERVE_ADDR=127.0.0.1:0", "OPENNEKO_GRAPHJIN_ADDR="+rs.gjAddr)
	if duck := os.Getenv("OPENNEKO_TEST_DUCKDB"); duck != "" {
		env = append(env, "OPENNEKO_DUCKDB="+duck)
	}
	logs := &lockedBuffer{}
	cmd := exec.Command(bin, "serve", "--subscriptions-addr=")
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		t.Logf("serve log:\n%s", logs.String())
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		st, err := serveConfinement(ctx, rs.socket)
		cancel()
		if m := listenLine.FindStringSubmatch(logs.String()); err == nil && m != nil {
			rs.state, rs.addr = st, m[1]
			return rs
		}
		select {
		case <-exited:
			t.Fatalf("serve exited: %s", logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not answer: %v\n%s", err, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func filesCall(t *testing.T, rs runningServe, method, path string, body []byte) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, "http://"+rs.addr+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+files.MintToken(rs.tokenKey, "org-door", "user-1", time.Now().Add(time.Minute)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != 200 {
		t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
	}
	return out
}

// TestConfinedServeDoesItsWork runs a confined serve, as production does in a
// one-account layout, through a turn with a tool call, a terminal command in
// the sandbox, a host call, an embedding call, a GraphJin query and the file
// tools. The GraphJin config has no table, so a GraphQL answer of any kind
// shows that the child runs.
func TestConfinedServeDoesItsWork(t *testing.T) {
	st := confine.Available()
	if st.ABI < 3 {
		if os.Getenv("CI") != "" {
			t.Fatalf("this runner cannot prove the confinement: %s, Landlock ABI %d", confine.Kernel(), st.ABI)
		}
		t.Skipf("%s gives Landlock ABI %d", confine.Kernel(), st.ABI)
	}
	box := startRecipeSandbox(t, false)
	pool := testpg.Pool(t, "0121_file_content.sql")
	dsn := testpg.URL(t)
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + "search_path=" + url.QueryEscape(pool.Config().ConnConfig.RuntimeParams["search_path"])

	base := t.TempDir()
	l := native.Layout{Root: filepath.Join(base, "data")}
	if err := l.Prepare(); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config", "openneko"), 0o700); err != nil {
		t.Fatal(err)
	}

	clearAxEnv(t, nil)
	fake := &fakeProviders{replies: []string{
		`{"javascriptCode":"final('Find reference', {})"}`,
		`{"javascriptCode":"const found = await mcp_neko_memory_search({query:'reference'}); const ran = await terminal({command:'echo sandbox-ran > from-sandbox.txt && cat from-sandbox.txt'}); final('Report', {reference:'REF-42', found, ran});"}`,
		"Answer: REF-42",
	}}
	model := httptest.NewServer(fake)
	t.Cleanup(model.Close)
	target, _ := url.Parse(model.URL)
	embedder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer planted-embed-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fakeJSON(w, map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 2, 3}}}})
	}))
	t.Cleanup(embedder.Close)
	embedTarget, _ := url.Parse(embedder.URL)
	door := &llm.KeyDoor{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			return toFake{embedTarget}.RoundTrip(r)
		}
		return toFake{target}.RoundTrip(r)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	doorPath := native.ServeEnv(l)["OPENNEKO_KEY_DOOR_SOCKET"]
	if _, err := llm.ListenDoor(ctx, doorPath, door); err != nil {
		t.Fatal(err)
	}

	rs := confinedServe(t, l, home, dsn, box.URL)
	if !rs.state.Confined || rs.state.Mechanism != "landlock" {
		t.Fatalf("serve reports %+v", rs.state)
	}
	t.Logf("serve: %s", rs.state)
	if runner := serveRunner(t, rs.socket); runner != "process" {
		t.Fatalf("serve runs turns %q, want each in an agent runner process", runner)
	}

	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", l.AgentRuns())
	t.Setenv("HOME", home)
	t.Setenv("OPENNEKO_KEY_DOOR_SOCKET", doorPath)
	hostWS, err := turnWorkspace("org-door", "thread-door", "run-door")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageTurn(hostWS, stageOptions{Terminal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stage.Dir) })
	tools := &fakeTools{reply: []any{}}
	p := doorProviders[0]
	a := &API{Door: door, doorPath: doorPath}
	res, err := a.runAgentCore(context.Background(), agentCoreInput{
		Backend: &llm.AxBackendConfig{Route: p.route("model-a")}, Prompt: "Find reference",
		OrgID: "org-door", ThreadID: "thread-door", RunID: "run-door",
		Workspace: stage.Workspace, Keys: map[string]string{"MODEL_API_KEY": "planted-" + p.name}, Socket: rs.socket,
		NativeDelegationOff: true, Shell: true,
		FilesEnabled:      func() bool { return false },
		toolBackend:       func(turnBinding) toolBackend { return tools },
		withoutScriptData: true,
		Emit:              func(context.Context, any) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || !strings.Contains(res.FinalText, "REF-42") {
		t.Fatalf("result %+v", res)
	}
	checkDoorCalls(t, p, "model-a", fake.all(), "planted-"+p.name)
	tools.mu.Lock()
	called := len(tools.calls)
	tools.mu.Unlock()
	if called == 0 {
		t.Fatal("the turn called no tool")
	}
	if b, err := os.ReadFile(filepath.Join(stage.Workspace.OrgRoot, "from-sandbox.txt")); err != nil || strings.TrimSpace(string(b)) != "sandbox-ran" {
		t.Fatalf("the sandbox command left %q, %v", b, err)
	}

	t.Setenv("OPENNEKO_SERVE_SOCKET", rs.socket)
	fake.mu.Lock()
	fake.replies, fake.served = []string{"Reply: READY"}, 0
	fake.mu.Unlock()
	host := llm.Settings{Source: "org", Provider: "openai-compatible", Model: "fixture", Enabled: true,
		Config: map[string]any{"url": "https://models.example.test/v1"}, Secrets: map[string]string{"apiKey": "planted-host-key"}}
	if err := a.hostCalls().TestPrimary(context.Background(), "org-door", host); err != nil {
		t.Fatalf("host call through the confined serve: %v", err)
	}
	t.Setenv("NEKO_EMBEDDING_URL", "https://embed.example.test/v1")
	t.Setenv("NEKO_EMBEDDING_MODEL", "fixture")
	t.Setenv("NEKO_EMBEDDING_API_KEY", "planted-embed-key")
	t.Setenv("NEKO_EMBEDDING_DIMENSIONS", "3")
	if v, err := a.EmbedText(context.Background(), "a memory"); err != nil || len(v) != 3 {
		t.Fatalf("embedding through the confined serve: %v, %v", v, err)
	}

	gql := func() (int, string) {
		res, err := http.Post("http://"+rs.gjAddr+"/api/v1/graphql", "application/json", strings.NewReader(`{"query":"{ __typename }"}`))
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, body := gql()
		if code != 0 && (strings.Contains(body, `"errors"`) || strings.Contains(body, `"data"`)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the GraphJin child of the confined serve did not answer: %d %s", code, body)
		}
		time.Sleep(100 * time.Millisecond)
	}

	filesCall(t, rs, http.MethodPut, "/v1/files/content?path=policies/travel.md", []byte("Travel needs approval from finance."))
	if out := filesCall(t, rs, http.MethodPost, "/v1/files/search", []byte(`{"query":"approval"}`)); !strings.Contains(string(mustJSON(out)), "travel.md") {
		t.Errorf("search: %v", out)
	}
	if out := filesCall(t, rs, http.MethodPost, "/v1/files/read", []byte(`{"path":"policies/travel.md"}`)); !strings.Contains(string(mustJSON(out)), "finance") {
		t.Errorf("read: %v", out)
	}
	if _, err := exec.LookPath(envOrDefault("OPENNEKO_TEST_DUCKDB", "duckdb")); err == nil {
		filesCall(t, rs, http.MethodPut, "/v1/files/content?path=q4_prices.csv", []byte("item,price\na,2\nb,3\n"))
		if out := filesCall(t, rs, http.MethodPost, "/v1/files/sql", []byte(`{"sql":"select sum(price) as total from q4_prices"}`)); !strings.Contains(string(mustJSON(out)), "5") {
			t.Errorf("sql: %v", out)
		}
	} else if os.Getenv("CI") != "" {
		t.Fatal("no DuckDB CLI: the sheet query of the confined serve did not run")
	}
}

func serveRunner(t *testing.T, socket string) string {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"type":"status"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Result struct {
			Runner string `json:"runner"`
		} `json:"result"`
	}
	if err := json.NewDecoder(conn).Decode(&frame); err != nil {
		t.Fatal(err)
	}
	return frame.Result.Runner
}

func envOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
