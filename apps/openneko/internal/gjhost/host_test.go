package gjhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func seedConfig(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "graphjin", "customer.sources.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	conf := strings.NewReplacer(
		"REPLACE_WITH_BASE64_32_BYTE_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"REPLACE_WITH_PER_ORG_SECRET_B64", "c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LXNlY3JldA==",
	).Replace(string(raw))
	file := filepath.Join(dir, "agentic.yml")
	if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func answers(addr string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	res, err := c.Post("http://"+addr+"/api/v1/graphql", "application/json", strings.NewReader(`{"query":"{ __typename }"}`))
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestMain(m *testing.M) {
	if file := os.Getenv("GJHOST_TEST_CHILD_CONFIG"); file != "" {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			cancel()
		}()
		if err := Serve(ctx, file, os.Getenv("GJHOST_TEST_CHILD_ADDR"), os.Stderr); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testCommand(file string) func(string) *exec.Cmd {
	return func(socket string) *exec.Cmd {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(ChildEnv(os.Environ()), "GJHOST_TEST_CHILD_CONFIG="+file, "GJHOST_TEST_CHILD_ADDR=unix:"+socket)
		return cmd
	}
}

func runSupervisor(t *testing.T, file string) (addr string, log *syncBuffer, cancel func() error) {
	t.Helper()
	addr = freeAddr(t)
	log = &syncBuffer{}
	s := &Supervisor{ConfigFile: file, Addr: addr, Log: log, Poll: 50 * time.Millisecond,
		MinBackoff: 100 * time.Millisecond, MaxBackoff: 400 * time.Millisecond, Command: testCommand(file)}
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- s.Run(ctx) }()
	var once sync.Once
	var runErr error
	cancel = func() error {
		once.Do(func() {
			stop()
			select {
			case runErr = <-stopped:
			case <-time.After(10 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
		return runErr
	}
	t.Cleanup(func() { _ = cancel() })
	return addr, log, cancel
}

func state(t *testing.T, file string) State {
	t.Helper()
	st, err := ReadState(file)
	if err != nil {
		return State{}
	}
	return st
}

func TestSupervisorServesRestartsAndStops(t *testing.T) {
	t.Setenv("GO_ENV", "agentic")
	dir := t.TempDir()
	file := seedConfig(t, dir)
	if err := os.WriteFile(filepath.Join(dir, staleAgentKey), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, log, cancel := runSupervisor(t, file)

	waitFor(t, "graphjin to answer", func() bool { return answers(addr) })
	waitFor(t, "state running", func() bool { return state(t, file).State == StateRunning })
	if _, err := os.Stat(filepath.Join(dir, staleAgentKey)); !os.IsNotExist(err) {
		t.Fatalf("stale agent key file still exists: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, RestartFile), []byte("token-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart ack", func() bool { return readToken(filepath.Join(dir, RestartAckFile)) == "token-1" })
	if !answers(addr) {
		t.Fatal("graphjin must answer when the restart is acknowledged")
	}
	waitFor(t, "a second child after the restart token", func() bool { return strings.Count(log.String(), "graphjin listening") >= 2 })

	if err := os.WriteFile(filepath.Join(dir, PingFile), []byte("ping-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ping ack", func() bool { return readToken(filepath.Join(dir, PingAckFile)) == "ping-1" })

	if err := cancel(); err != nil {
		t.Fatal(err)
	}
	if answers(addr) {
		t.Fatal("graphjin still answers after Run returned")
	}
	if st := state(t, file); st.State != StateStopped {
		t.Fatalf("state after stop = %+v", st)
	}
}

func TestSupervisorOutlivesAGraphJinCrashAndRecovers(t *testing.T) {
	t.Setenv("GO_ENV", "agentic")
	dir := t.TempDir()
	file := seedConfig(t, dir)
	good, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(good), "\nauth:\n  type: jwt\n", "\nauth:\n  type: no-such-auth\n", 1)
	if bad == string(good) {
		t.Fatal("seed config has no top-level auth block")
	}
	if err := os.WriteFile(file, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, log, cancel := runSupervisor(t, file)

	waitFor(t, "two failed starts", func() bool {
		st := state(t, file)
		return st.State == StateRestarting && st.Restarts >= 2
	})
	st := state(t, file)
	if !strings.Contains(st.Reason, "unknown auth type") || strings.Contains(st.Reason, "\x1b") {
		t.Fatalf("the state must carry GraphJin's last output; got %+v\nlog:\n%s", st, log)
	}
	res, err := http.Post("http://"+addr+"/api/v1/graphql", "application/json", strings.NewReader(`{"query":"{ __typename }"}`))
	if err != nil {
		t.Fatalf("the GraphJin address must stay open while the engine is down: %v", err)
	}
	var body struct {
		Errors []struct{ Message string } `json:"errors"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || len(body.Errors) != 1 ||
		!strings.HasPrefix(body.Errors[0].Message, "GraphJin is restarting: ") ||
		!strings.Contains(body.Errors[0].Message, "unknown auth type") {
		t.Fatalf("status %d, body %+v", res.StatusCode, body)
	}

	if err := os.WriteFile(file, good, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "graphjin to answer after the config is fixed", func() bool { return answers(addr) })
	waitFor(t, "state running", func() bool { return state(t, file).State == StateRunning })
	if err := cancel(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorKeepsAcknowledgedTokenAcrossStarts(t *testing.T) {
	t.Setenv("GO_ENV", "agentic")
	dir := t.TempDir()
	file := seedConfig(t, dir)
	for name, v := range map[string]string{RestartFile: "old", RestartAckFile: "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	addr, log, _ := runSupervisor(t, file)
	waitFor(t, "graphjin to answer", func() bool { return answers(addr) })
	waitFor(t, "the child's first line", func() bool { return strings.Contains(log.String(), "graphjin listening") })
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(log.String(), "graphjin listening"); n != 1 {
		t.Fatalf("an acknowledged token must not restart graphjin; starts=%d log:\n%s", n, log)
	}
}

func TestChildEnvHoldsNoServeSecrets(t *testing.T) {
	env := ChildEnv([]string{
		"PATH=/bin", "GO_ENV=agentic", "GJ_LOG_LEVEL=info", "TZ=UTC",
		"OPENNEKO_SERVE_CREDENTIALS=/credentials", "OPENNEKO_SERVE_SOCKET=/run/openneko/serve.sock",
		"OPENNEKO_SANDBOX_KEY=k", "OPENNEKO_SANDBOX_URL=http://sandbox", "OPENNEKO_SECRET_KEY_FILE=/s",
		"OPENNEKO_DATABASE_URL_FILE=/d", "NEKO_PG_PASSWORD=p", "GRAPHJIN_AGENT_API_KEY=k", "ANTHROPIC_API_KEY=k",
	})
	want := []string{"PATH=/bin", "GO_ENV=agentic", "GJ_LOG_LEVEL=info", "TZ=UTC"}
	if strings.Join(env, "|") != strings.Join(want, "|") {
		t.Fatalf("child env = %v, want %v", env, want)
	}
}

func TestChildExitsWhenItsStdinCloses(t *testing.T) {
	t.Setenv("GO_ENV", "agentic")
	dir := t.TempDir()
	file := seedConfig(t, dir)
	sock := filepath.Join(t.TempDir(), "gj.sock")
	cmd := testCommand(file)(sock)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "child socket", func() bool { return dialable(sock) })
	stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not exit after its stdin closed")
	}
}
