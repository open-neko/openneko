//go:build !windows

package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/embedding"
	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
	"github.com/open-neko/openneko/apps/ax-harness/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const runnerChildEnv = "AX_SERVE_TEST_RUNNER"

func TestMain(m *testing.M) {
	switch os.Getenv(runnerChildEnv) {
	case "":
		os.Exit(m.Run())
	case "turn":
	case "fds":
		runnerRun = reportDescriptors
	case "hang":
		runnerRun = func(ctx context.Context, _ harness.Turn) (int, error) {
			<-ctx.Done()
			return 1, ctx.Err()
		}
	case "hostile-sandbox":
		runnerRun = hostileSandbox
	case "door-only":
		os.Exit(checkDoorOnly())
	case "hostile-exit", "hostile-hold", "hostile-broker", "hostile-orphan":
		hostileRunner(os.Getenv(runnerChildEnv))
	}
	if err := RunRunner(context.Background(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

// reportDescriptors writes the runner's open descriptors as one event.
func reportDescriptors(_ context.Context, turn harness.Turn) (int, error) {
	dir := "/proc/self/fd"
	if runtime.GOOS != "linux" {
		dir = "/dev/fd"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 2, err
	}
	var open []string
	for _, entry := range entries {
		var stat syscall.Stat_t
		var fd int
		if _, err := fmt.Sscan(entry.Name(), &fd); err != nil || syscall.Fstat(fd, &stat) != nil {
			continue
		}
		kind := "other"
		if stat.Mode&syscall.S_IFMT == syscall.S_IFSOCK {
			kind = "socket"
		}
		open = append(open, fmt.Sprintf("%d:%s", fd, kind))
	}
	line, _ := json.Marshal(map[string]any{"type": "fds", "open": open, "env": os.Environ()})
	_, err = turn.Out.Write(append(line, '\n'))
	return 0, err
}

// checkDoorOnly runs in a runner-like process: after SetDialer, a model,
// an embedding or a sandbox that is not reached through a passed connection
// must be refused.
func checkDoorOnly() int {
	keydoor.SetDialer(func(context.Context) (net.Conn, error) { return nil, errors.New("no door in this check") })
	sandbox.RequirePassedConnections()
	failures := 0
	if _, err := inference.NewClient(inference.Route{Provider: "openai", Model: "m", URL: "http://127.0.0.1:9/v1"}, "k", inference.NoRetry); !errors.Is(err, keydoor.ErrNotDoor) {
		fmt.Println("a model URL that is not the door was accepted:", err)
		failures++
	}
	if _, err := inference.NewClient(inference.Route{Provider: "openai", Model: "m"}, "k", inference.NoRetry); !errors.Is(err, keydoor.ErrNotDoor) {
		fmt.Println("the provider's own URL was accepted:", err)
		failures++
	}
	if _, err := inference.NewClient(inference.Route{Provider: "openai", Model: "m", URL: keydoor.URL("primary")}, "k", inference.NoRetry); err != nil {
		fmt.Println("the door URL was refused:", err)
		failures++
	}
	if _, err := embedding.Embed(context.Background(), embedding.Request{URL: "http://127.0.0.1:9/v1", Model: "e", Input: []string{"x"}}, "test-embedding-key"); err == nil || err.Error() != keydoor.ErrNotDoor.Error() {
		fmt.Println("an embedding URL that is not the door was accepted:", err)
		failures++
	}
	if _, err := sandbox.NewClient(sandbox.Config{URL: "unix:///tmp/sandbox.sock"}, "/tmp"); err == nil {
		fmt.Println("a sandbox that is not a passed connection was accepted")
		failures++
	}
	if _, err := sandbox.NewClient(sandbox.Config{URL: "unix:///tmp/sandbox.sock", Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("x") }}, "/tmp"); err != nil {
		fmt.Println("a sandbox with a passed connection was refused:", err)
		failures++
	}
	keydoor.SetSocket("/tmp/elsewhere.sock")
	if !keydoor.DoorOnly() {
		fmt.Println("SetSocket undid the door-only rule")
		failures++
	}
	return failures
}

func runnerServer(t *testing.T, mode string, server *Server) *Server {
	t.Helper()
	server.Runner = []string{os.Args[0]}
	server.RunnerEnv = []string{runnerChildEnv + "=" + mode, "PATH=" + os.Getenv("PATH")}
	if server.Run == nil {
		server.Run = func(context.Context, harness.Turn) (int, error) {
			t.Error("the turn ran inside serve, not in the runner")
			return 2, nil
		}
	}
	return server
}

// fakeDoor is a key door on a Unix socket in front of the fixture model.
func fakeDoor(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := filepath.Join(testsock.Dir(t), keydoor.SocketName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	keydoor.SetSocket(path)
	t.Cleanup(func() { keydoor.SetSocket("") })
	return path
}

func waitExit(t *testing.T, c *client) (Frame, []json.RawMessage) {
	t.Helper()
	var events []json.RawMessage
	timeout := time.After(60 * time.Second)
	for {
		select {
		case frame, ok := <-c.frames:
			if !ok {
				t.Fatal("the link closed before an exit frame")
			}
			if frame.Type == "exit" {
				return frame, events
			}
			if frame.Type == "event" {
				events = append(events, frame.Event)
			}
		case <-timeout:
			t.Fatal("no exit frame")
		}
	}
}

func TestRunnerRunsAWholeTurnThroughTheDoorDescriptor(t *testing.T) {
	model, keys := fixtureModel(t)
	var paths []string
	var mu sync.Mutex
	fakeDoor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		model.Config.Handler.ServeHTTP(w, r)
	}))
	token := keydoor.TokenPrefix + strings.Repeat("a", 22) + "." + strings.Repeat("b", 43)
	socket := startServer(t, runnerServer(t, "turn", &Server{RequireDoor: true, Getenv: os.Getenv}))
	listed := make(chan struct{}, 1)
	c := dial(t, socket, Frame{
		Type: "start",
		Environ: []string{"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1",
			"HARNESS_MODEL_URL=" + keydoor.URL("primary"), "HARNESS_MODEL=fixture"},
		Spec: json.RawMessage(spec), Bridge: true, ModelKeys: map[string]string{"MODEL_API_KEY": token},
	}, func(c *client) { c.serveTools(t, listed) })
	exit, events := waitExit(t, c)
	if exit.Code == nil || *exit.Code != 0 {
		t.Fatalf("exit = %+v", exit)
	}
	select {
	case <-listed:
	default:
		t.Fatal("the runner never listed the worker's tools over the link")
	}
	if last := string(events[len(events)-1]); !strings.Contains(last, `"run.finished"`) || !strings.Contains(last, "REF-42") {
		t.Fatalf("last event = %s", last)
	}
	if len(*keys) == 0 {
		t.Fatal("the model got no call through the door")
	}
	for i, key := range *keys {
		if key != "Bearer "+token || !strings.HasPrefix(paths[i], "/r/primary/") {
			t.Fatalf("call %d reached the door at %s with %q", i, paths[i], key)
		}
	}
}

func TestRunnerRefusesAModelThatIsNotTheDoor(t *testing.T) {
	model, keys := fixtureModel(t)
	fakeDoor(t, http.NotFoundHandler())
	socket := startServer(t, runnerServer(t, "turn", &Server{Getenv: os.Getenv}))
	c := dial(t, socket, Frame{
		Type: "start",
		Environ: []string{"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1",
			"HARNESS_MODEL_URL=" + model.URL, "HARNESS_MODEL=fixture"},
		Spec: json.RawMessage(spec), ModelKeys: map[string]string{"MODEL_API_KEY": "k"},
	})
	exit, events := waitExit(t, c)
	all := exit.Error
	for _, e := range events {
		all += string(e)
	}
	if (exit.Code != nil && *exit.Code == 0) || !strings.Contains(all, "only through the key door") {
		t.Fatalf("exit = %+v, events = %s", exit, all)
	}
	if len(*keys) != 0 {
		t.Fatalf("the runner called the model directly %d times", len(*keys))
	}
}

func TestRunnerProcessRefusesEveryWayOutButPassedConnections(t *testing.T) {
	cmd := osexec.Command(os.Args[0])
	cmd.Env = []string{runnerChildEnv + "=door-only"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestRunnerHoldsOnlyItsTwoLinks(t *testing.T) {
	t.Setenv("OPENNEKO_SANDBOX_KEY", "serve-sandbox-key")
	other, err := net.Listen("unix", filepath.Join(testsock.Dir(t), "other.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	socket := startServer(t, runnerServer(t, "fds", &Server{MaxTurns: 8}))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkRunnerHolds(t, dial(t, socket, Frame{Type: "start", Spec: json.RawMessage(spec), Environ: []string{"OPENNEKO_MCP_ORG_ID=o", "OPENNEKO_MCP_THREAD_ID=t"}}))
		}()
	}
	wg.Wait()
}

func checkRunnerHolds(t *testing.T, c *client) {
	var events []json.RawMessage
	var exit Frame
	for frame := range c.frames {
		if frame.Type == "event" {
			events = append(events, frame.Event)
		}
		if frame.Type == "exit" {
			exit = frame
			break
		}
	}
	if exit.Code == nil || *exit.Code != 0 || len(events) != 1 {
		t.Errorf("exit = %+v, events = %d", exit, len(events))
		return
	}
	var report struct {
		Open []string `json:"open"`
		Env  []string `json:"env"`
	}
	if err := json.Unmarshal(events[0], &report); err != nil {
		t.Error(err)
		return
	}
	var sockets []string
	for _, fd := range report.Open {
		if strings.HasSuffix(fd, ":socket") {
			sockets = append(sockets, fd)
		}
	}
	if len(sockets) != 2 {
		t.Errorf("the runner holds the sockets %v, want only its link and its broker; all: %v", sockets, report.Open)
	}
	for _, entry := range report.Env {
		if !strings.HasPrefix(entry, runnerChildEnv+"=") && !strings.HasPrefix(entry, "PATH=") {
			t.Errorf("the runner got %q from serve's environment", entry)
		}
	}
}

func TestRunnerStopsWhenTheCallerHangsUp(t *testing.T) {
	started := make(chan *os.Process, 1)
	server := runnerServer(t, "hang", &Server{})
	server.runnerStarted = func(p *os.Process) { started <- p }
	socket := startServer(t, server)
	c := dial(t, socket, Frame{Type: "start", Spec: json.RawMessage(spec), Environ: []string{"OPENNEKO_MCP_ORG_ID=o", "OPENNEKO_MCP_THREAD_ID=t"}})
	process := <-started
	time.Sleep(200 * time.Millisecond)
	_ = c.conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	for process.Signal(syscall.Signal(0)) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the runner kept running after the caller hung up")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunnerCrashGivesAnExitFrame(t *testing.T) {
	server := runnerServer(t, "turn", &Server{})
	server.Runner = []string{"/bin/sh", "-c", "exit 3"}
	c := dial(t, startServer(t, server), Frame{Type: "start", Spec: json.RawMessage(spec)})
	exit, _ := waitExit(t, c)
	if exit.Code == nil || *exit.Code != 2 || !strings.Contains(exit.Error, "agent runner stopped") {
		t.Fatalf("exit = %+v", exit)
	}
}

func TestBrokerPassesConnectedDescriptors(t *testing.T) {
	dir := testsock.Dir(t)
	fakeDoor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "door") }))
	box, err := net.Listen("unix", filepath.Join(dir, "sandbox.sock"))
	if err != nil {
		t.Fatal(err)
	}
	boxSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/exec") {
			_, _ = io.WriteString(w, `{"token":"run-token"}`)
			return
		}
		_, _ = io.WriteString(w, "sandbox")
	})}
	go func() { _ = boxSrv.Serve(box) }()
	defer boxSrv.Close()

	parent, child, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	serveEnd, err := unixConn(parent)
	if err != nil {
		t.Fatal(err)
	}
	runnerEnd, err := unixConn(child)
	if err != nil {
		t.Fatal(err)
	}
	defer runnerEnd.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{Sandbox: sandbox.Config{URL: "unix://" + filepath.Join(dir, "sandbox.sock")}}
	gate, err := sandbox.NewGate(s.Sandbox, sandbox.Egress{})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	go func() { s.serveBroker(ctx, serveEnd, gate); _ = serveEnd.Close() }()
	b := &brokerClient{conn: runnerEnd, in: bufio.NewReader(runnerEnd)}

	for _, c := range []struct{ name, body string }{{brokerKeyDoor, "door"}, {brokerSandbox, "sandbox"}, {brokerKeyDoor, "door"}} {
		client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return b.get(ctx, c.name) }, DisableKeepAlives: true}}
		resp, err := client.Post("http://x/v2/runs/"+gate.Run()+"/exec", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != c.body {
			t.Fatalf("%s answered %q", c.name, body)
		}
	}
	if _, err := b.get(context.Background(), "/var/run/docker.sock"); err == nil || !strings.Contains(err.Error(), "passes no connection") {
		t.Fatalf("the broker passed a connection it does not name: %v", err)
	}
	off := &Server{}
	p2, c2, _ := socketPair()
	s2, _ := unixConn(p2)
	r2, _ := unixConn(c2)
	defer r2.Close()
	go func() { off.serveBroker(ctx, s2, nil); _ = s2.Close() }()
	if _, err := (&brokerClient{conn: r2, in: bufio.NewReader(r2)}).get(context.Background(), brokerSandbox); err == nil || !strings.Contains(err.Error(), "terminal is off") {
		t.Fatalf("a sandbox with no URL gave %v", err)
	}
}

// hostileSandbox is a runner that tries to choose its own egress list and
// reach other runs and routes through its sandbox link.
func hostileSandbox(ctx context.Context, turn harness.Turn) (int, error) {
	report := map[string]any{"key": turn.Sandbox.Key, "run": turn.Sandbox.Run}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return turn.Sandbox.Dial(ctx) }}}
	try := func(method, path, body string) int {
		req, _ := http.NewRequestWithContext(ctx, method, "http://sandbox"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer guessed")
		resp, err := client.Do(req)
		if err != nil {
			return -1
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	own := "/v2/runs/" + turn.Sandbox.Run
	report["own_exec"] = try(http.MethodPost, own+"/exec", `{"command":"true","egress":{"org":"org-evil","label":"x","hosts":["evil.example"]}}`)
	report["other_exec"] = try(http.MethodPost, "/v2/runs/run-0123456789abcdef/exec", `{"command":"true"}`)
	report["open_run"] = try(http.MethodPost, own, `{"hosts":["evil.example"]}`)
	report["open_other"] = try(http.MethodPost, "/v2/runs/run-0123456789abcdef", `{"hosts":["evil.example"]}`)
	report["check"] = try(http.MethodGet, "/v2/check", "")
	report["end"] = try(http.MethodDelete, own, "")
	line, _ := json.Marshal(report)
	_, err := turn.Out.Write(append(line, '\n'))
	return 0, err
}

type boxCall struct {
	method, path, auth string
	body               map[string]any
}

func TestARunnerCannotChooseTheSandboxEgress(t *testing.T) {
	const key = "serve-sandbox-key"
	var mu sync.Mutex
	var calls []boxCall
	ln, err := net.Listen("unix", filepath.Join(testsock.Dir(t), "sandbox.sock"))
	if err != nil {
		t.Fatal(err)
	}
	box := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := boxCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		_ = json.NewDecoder(r.Body).Decode(&call.body)
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		if r.Method == http.MethodPost && !strings.HasSuffix(r.URL.Path, "/exec") {
			_, _ = io.WriteString(w, `{"token":"run-token"}`)
			return
		}
		_, _ = io.WriteString(w, "{}")
	})}
	go func() { _ = box.Serve(ln) }()
	defer box.Close()

	socket := startServer(t, runnerServer(t, "hostile-sandbox", &Server{Sandbox: sandbox.Config{URL: "unix://" + ln.Addr().String(), Key: key}}))
	c := dial(t, socket, Frame{Type: "start", Spec: json.RawMessage(spec), Environ: []string{"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=t",
		"OPENNEKO_HARNESS_RUN_ID=work-run-1", "OPENNEKO_HARNESS_EGRESS_HOSTS=allowed.example, api.allowed.example:8443"}})
	exit, events := waitExit(t, c)
	if exit.Code == nil || *exit.Code != 0 || len(events) != 1 {
		t.Fatalf("exit = %+v, events = %d", exit, len(events))
	}
	var report struct {
		Key       string `json:"key"`
		Run       string `json:"run"`
		OwnExec   int    `json:"own_exec"`
		OtherExec int    `json:"other_exec"`
		OpenRun   int    `json:"open_run"`
		OpenOther int    `json:"open_other"`
		Check     int    `json:"check"`
		End       int    `json:"end"`
	}
	if err := json.Unmarshal(events[0], &report); err != nil {
		t.Fatal(err)
	}
	if report.Key != "" || report.Run == "" {
		t.Fatalf("the runner holds the sandbox key %q, run %q", report.Key, report.Run)
	}
	if report.OtherExec != http.StatusForbidden || report.OpenRun != http.StatusForbidden || report.OpenOther != http.StatusForbidden || report.Check != http.StatusForbidden {
		t.Errorf("serve passed a request outside the runner's run: %+v", report)
	}
	ended := func() bool {
		mu.Lock()
		defer mu.Unlock()
		last := calls[len(calls)-1]
		return last.method == http.MethodDelete && last.auth == "Bearer "+key
	}
	for deadline := time.Now().Add(5 * time.Second); !ended(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("serve did not end the run with its key at turn end")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("the sandbox got %+v", calls)
	}
	open := calls[0]
	hosts, _ := json.Marshal(open.body["hosts"])
	if open.method != http.MethodPost || open.path != "/v2/runs/"+report.Run || open.auth != "Bearer "+key ||
		open.body["org"] != "org-1" || open.body["label"] != "work-run-1" || string(hosts) != `["allowed.example","api.allowed.example:8443"]` {
		t.Fatalf("serve opened the run as %+v", open)
	}
	for _, call := range calls[1:3] {
		if call.auth != "Bearer run-token" {
			t.Errorf("the sandbox got %s %s with %q from the runner's link", call.method, call.path, call.auth)
		}
	}
	if calls[1].method != http.MethodPost || calls[1].path != "/v2/runs/"+report.Run+"/exec" || calls[2].method != http.MethodDelete || calls[2].path != "/v2/runs/"+report.Run {
		t.Errorf("the runner's own calls reached the sandbox as %+v", calls[1:3])
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServeLabelsTheRunnersStderr(t *testing.T) {
	var log lockedBuffer
	server := runnerServer(t, "turn", &Server{runnerLog: &log})
	server.Runner = []string{"/bin/sh", "-c", `printf 'openneko serve: forged\r\nsecond\033[2K line\n' >&2; head -c 400000 /dev/zero | tr '\000' 'a' >&2; exit 3`}
	c := dial(t, startServer(t, server), Frame{Type: "start", Spec: json.RawMessage(spec)})
	waitExit(t, c)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(log.String(), "stderr cut") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := log.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) < 3 || lines[0] != "agent runner 1: openneko serve: forged" || lines[1] != "agent runner 1: second[2K line" {
		t.Fatalf("serve did not label the runner's lines: %.300q", got)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "agent runner 1: ") || len(line) > maxRunnerLogLine+len("agent runner 1: ") {
			t.Fatalf("an unlabelled or long line: %.100q", line)
		}
		if strings.ContainsAny(line, "\r\x1b") {
			t.Fatalf("a control character reached serve's log: %q", line)
		}
	}
	if len(got) > maxRunnerLog+len(lines)*len("agent runner 1: \n")+100 || !strings.HasSuffix(got, "agent runner 1: stderr cut at 262144 bytes\n") {
		t.Fatalf("serve did not cut the runner's stderr: %d bytes, ends %q", len(got), got[max(0, len(got)-80):])
	}
}
