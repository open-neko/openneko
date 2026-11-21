package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
)

const runnerChildEnv = "AX_SERVE_TEST_RUNNER"

func TestMain(m *testing.M) {
	switch os.Getenv(runnerChildEnv) {
	case "":
		os.Exit(m.Run())
	case "turn":
	case "hang":
		runnerRun = func(ctx context.Context, _ harness.Turn) (int, error) {
			<-ctx.Done()
			return 1, ctx.Err()
		}
	}
	if err := RunRunner(context.Background(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func windowsRunnerServer(t *testing.T, mode string, server *Server) *Server {
	t.Helper()
	server.Runner = []string{os.Args[0]}
	server.RunnerEnv = []string{runnerChildEnv + "=" + mode, "PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	server.Run = func(context.Context, harness.Turn) (int, error) {
		t.Error("the turn ran inside serve, not in the runner")
		return 2, nil
	}
	return server.RunUnconfinedForTest()
}

func windowsFakeDoor(t *testing.T, handler http.Handler) {
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
}

func windowsWaitExit(t *testing.T, c *client) (Frame, []json.RawMessage) {
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

// TestWindowsRunnerRunsAWholeTurnThroughItsSockets: the runner gets the turn
// on its link socket and reaches the model only through the broker socket.
func TestWindowsRunnerRunsAWholeTurnThroughItsSockets(t *testing.T) {
	model, keys := fixtureModel(t)
	var paths []string
	var mu sync.Mutex
	windowsFakeDoor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		model.Config.Handler.ServeHTTP(w, r)
	}))
	token := keydoor.TokenPrefix + strings.Repeat("a", 22) + "." + strings.Repeat("b", 43)
	socket := startServer(t, windowsRunnerServer(t, "turn", &Server{RequireDoor: true, Getenv: os.Getenv}))
	listed := make(chan struct{}, 1)
	c := dial(t, socket, Frame{
		Type: "start",
		Environ: []string{"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1",
			"HARNESS_MODEL_URL=" + keydoor.URL("primary"), "HARNESS_MODEL=fixture"},
		Spec: json.RawMessage(spec), Bridge: true, ModelKeys: map[string]string{"MODEL_API_KEY": token},
	}, func(c *client) { c.serveTools(t, listed) })
	exit, events := windowsWaitExit(t, c)
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

// TestWindowsRunnerStopsWhenTheCallerHangsUp: closing the turn's
// connection ends the runner.
func TestWindowsRunnerStopsWhenTheCallerHangsUp(t *testing.T) {
	server := windowsRunnerServer(t, "hang", &Server{Getenv: os.Getenv})
	handles := make(chan windows.Handle, 1)
	server.StartRunner = func(r RunnerStart) (RunnerProcess, error) {
		p, err := startPlainRunner(r)
		if err == nil {
			h, herr := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(p.Pid()))
			if herr != nil {
				t.Error(herr)
			}
			handles <- h
		}
		return p, err
	}
	socket := startServer(t, server)
	c := dial(t, socket, Frame{Type: "start", Environ: []string{"OPENNEKO_MCP_ORG_ID=org-1"}, Spec: json.RawMessage(spec)})
	var h windows.Handle
	select {
	case h = <-handles:
	case <-time.After(30 * time.Second):
		t.Fatal("serve started no runner")
	}
	defer windows.CloseHandle(h)
	time.Sleep(time.Second)
	c.conn.Close()
	if ev, err := windows.WaitForSingleObject(h, 30_000); err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("the runner did not end after the caller hung up: %d, %v", ev, err)
	}
}

// TestWindowsServeRefusesAConfinedTurnWithNoStarter: with confinement on
// and no way to start a confined runner, no turn runs.
func TestWindowsServeRefusesAConfinedTurnWithNoStarter(t *testing.T) {
	server := &Server{Getenv: os.Getenv, Runner: []string{os.Args[0]}, ConfineRunner: true,
		RunnerEnv: []string{runnerChildEnv + "=turn"}}
	server.Run = func(context.Context, harness.Turn) (int, error) {
		t.Error("the turn ran inside serve")
		return 2, nil
	}
	socket := startConfinedServer(t, server)
	c := dial(t, socket, Frame{Type: "start", Environ: []string{"OPENNEKO_MCP_ORG_ID=org-1"}, Spec: json.RawMessage(spec)})
	exit, _ := windowsWaitExit(t, c)
	if exit.Code == nil || *exit.Code == 0 || !strings.Contains(exit.Error, "no way to start a confined runner") {
		t.Fatalf("exit = %+v", exit)
	}
}

// TestWindowsRunnerSocketsTakeOnlyTheRunner: a runner's sockets close a
// connection from any process but the runner's.
func TestWindowsRunnerSocketsTakeOnlyTheRunner(t *testing.T) {
	path := filepath.Join(testsock.Dir(t), "link.sock")
	for _, c := range []struct {
		pid  int
		want bool
	}{{os.Getpid() + 4, false}, {os.Getpid(), true}} {
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		var log strings.Builder
		s := &Server{Log: &syncWriter{w: &log}}
		got := make(chan net.Conn, 1)
		go func() {
			conn, err := s.acceptFrom(ln, c.pid)
			if err == nil {
				got <- conn
			}
			close(got)
		}()
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if c.want {
			accepted := <-got
			if accepted == nil {
				t.Fatalf("the socket refused the runner's own process: %s", log.String())
			}
			if pid, err := peerPID(accepted); err != nil || pid != os.Getpid() {
				t.Fatalf("peer = %d, %v", pid, err)
			}
			accepted.Close()
		} else {
			if _, err := conn.Read(make([]byte, 1)); err != io.EOF && !isClosed(err) {
				t.Fatalf("a connection from another process stayed open: %v", err)
			}
			if !strings.Contains(log.String(), fmt.Sprintf("from process %d", os.Getpid())) {
				t.Fatalf("serve did not log the refused process: %q", log.String())
			}
		}
		conn.Close()
		ln.Close()
		<-got
		_ = os.Remove(path)
	}
}

func isClosed(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "forcibly closed") || strings.Contains(err.Error(), "reset"))
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprint(s.w)
}
