//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const (
	runnerProbeEnv = "OPENNEKO_TEST_RUNNER_PROBE"
	probeTurnEnv   = "OPENNEKO_TEST_PROBE_TURN"
)

// floodTarget is above the runner's cap of 256 threads.
const floodTarget = 400

type runnerProbeIn struct {
	Mode                          string
	ABI                           int
	Secret, Home, Outside, Victim string
	TCP, TCP6, UDP                string
	Stream, Datagram, Abstract    string
	Docker, DBus                  string
	Parent, Web                   int
	Shm                           int
	NProc                         uint64
	ParentAddr                    uintptr
	Loosen, Source                string
}

// TestRunnerProbeChild is the agent runner of TestConfinedRunner: the real
// runner code with the harness replaced by the checks.
func TestRunnerProbeChild(t *testing.T) {
	raw := os.Getenv(runnerProbeEnv)
	if raw == "" {
		t.Skip("helper process")
	}
	var in runnerProbeIn
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	setProbeMode(in)
	serve.SetRunnerTurn(func(ctx context.Context, turn harness.Turn) (int, error) {
		workspace := ""
		for _, e := range turn.Environ {
			if v, ok := strings.CutPrefix(e, "OPENNEKO_HARNESS_WORKSPACE_DIR="); ok {
				workspace = v
			}
		}
		var res probeResult
		turnMode := in.Mode
		for _, e := range turn.Environ {
			if v, ok := strings.CutPrefix(e, probeTurnEnv+"="); ok {
				turnMode = v
			}
		}
		if hold, ok := strings.CutPrefix(turnMode, "hold:"); ok {
			res.Threads = holdThreads(hold)
		} else if strings.HasPrefix(turnMode, "flood") {
			res.Threads = floodThreads(floodTarget)
		} else {
			res = runnerChecks(ctx, in, workspace)
		}
		b, _ := json.Marshal(res)
		_, err := turn.Out.Write(append(b, '\n'))
		return 0, err
	})
	if err := serve.RunRunner(context.Background(), confineRunner); err != nil {
		t.Fatal(err)
	}
}

// runnerTargets are the things outside the runner that the checks try to
// reach. The test makes its own, so the control proves each check on any host.
type runnerTargets struct {
	in        runnerProbeIn
	workspace string
	tmp       string
}

// runRunnerProbe starts a serve with the test binary as its agent runner and
// sends one turn; it returns what the runner's checks found, or the turn's
// error.
func runRunnerProbe(t *testing.T, tg runnerTargets, mode string) (probeResult, string) {
	t.Helper()
	res, msg, err := runnerTurn(startProbeServe(t, tg, mode, 1), tg, mode)
	if err != nil {
		t.Fatal(err)
	}
	return res, msg
}

// startProbeServe starts a serve that runs up to turns runners of the test
// binary at once, and returns its socket.
func startProbeServe(t *testing.T, tg runnerTargets, mode string, turns int) string {
	t.Helper()
	in := tg.in
	in.Mode = mode
	raw, _ := json.Marshal(in)
	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	ln, err := serve.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: turns, ConfineRunner: mode != "control" && !strings.HasPrefix(mode, "flood"),
		Runner:    []string{os.Args[0], "-test.run=^TestRunnerProbeChild$", "-test.count=1"},
		RunnerEnv: []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + tg.tmp, runnerProbeEnv + "=" + string(raw)}}
	if !srv.ConfineRunner {
		srv.RunUnconfinedForTest()
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() { stop(); <-done })
	return sock
}

// runnerTurn sends one turn and returns what the runner's checks found, or
// the turn's error. turnMode, when given, chooses the work of this turn.
func runnerTurn(sock string, tg runnerTargets, mode string, turnMode ...string) (probeResult, string, error) {
	in := tg.in
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return probeResult{}, "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	environ := []string{"OPENNEKO_HARNESS_WORKSPACE_DIR=" + tg.workspace, "OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1"}
	for _, m := range turnMode {
		environ = append(environ, probeTurnEnv+"="+m)
	}
	start := serve.Frame{Type: "start", Spec: json.RawMessage(`{}`), Environ: environ}
	if err := json.NewEncoder(conn).Encode(start); err != nil {
		return probeResult{}, "", err
	}
	var res probeResult
	dec := json.NewDecoder(conn)
	for {
		var f serve.Frame
		if err := dec.Decode(&f); err != nil {
			return res, "", fmt.Errorf("%s: the turn ended with no exit frame: %w", mode, err)
		}
		switch f.Type {
		case "event":
			_ = json.Unmarshal(f.Event, &res)
		case "exit":
			removeProbeLeftovers(in)
			_ = os.Remove(in.Outside)
			_ = os.Remove(filepath.Join(tg.workspace, "tool"))
			_ = os.WriteFile(in.Victim, []byte("keep me"), 0o600)
			return res, f.Error, nil
		}
	}
}

func requireRunnerConfinement(t *testing.T) confine.State {
	t.Helper()
	st := confine.RunnerAvailable()
	t.Logf("%s, %s, gaps %v", confine.Kernel(), st, st.Gaps)
	if !st.Confined {
		if os.Getenv("CI") != "" || os.Getenv("OPENNEKO_TEST_REQUIRE_CONFINE") != "" {
			t.Fatalf("this host cannot prove the agent runner's confinement: %s", st.Reason)
		}
		t.Skip(st.Reason)
	}
	return st
}

type probeResult struct {
	Notes   []string          `json:"notes"`
	Refused map[string]bool   `json:"refused"`
	Allowed map[string]bool   `json:"allowed"`
	Limited map[string]bool   `json:"limited"`
	Threads int               `json:"threads"`
	Errors  map[string]string `json:"errors,omitempty"`
}

// webSecret stands for a secret in web's memory.
var webSecret = "planted-secret-in-web-memory"

func dial(network, addr string) error {
	c, err := net.DialTimeout(network, addr, 3*time.Second)
	if err == nil {
		c.Close()
	}
	return err
}

func sendUDP(addr string) error {
	c, err := net.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte("x"))
	return err
}

func sendUnixgram(path string) error {
	c, err := net.Dial("unixgram", path)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte("x"))
	return err
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o755)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// serveFakeDoor answers "pong" over HTTP on the key door socket, so the runner proves
// that the broker still passes it the door.
func serveFakeDoor(t *testing.T) {
	t.Helper()
	door := filepath.Join(t.TempDir(), "keydoor.sock")
	ln, err := net.Listen("unix", door)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") }),
		ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	keydoor.SetSocket(door)
}

// acceptAll listens on addr and closes each connection; it returns nil when
// this host cannot listen there.
func acceptAll(t *testing.T, network, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln
}

// keyDoorAnswers asks the fake key door for "pong" through the broker.
func keyDoorAnswers(out *probeResult) bool {
	client, err := keydoor.Client()
	if err == nil {
		var res *http.Response
		if res, err = client.Get("http://door/ping"); err == nil {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if string(body) != "pong" {
				err = fmt.Errorf("the door answered %q", body)
			}
		}
	}
	if err != nil {
		out.Notes = append(out.Notes, "key door: "+err.Error())
	}
	return err == nil
}

// floodThreads pins goroutines to threads until the process has n threads,
// as native code that floods threads would. It returns the count reached.
// Each goroutine holds its own thread once it counts itself.
func floodThreads(n int) int {
	block := make(chan struct{})
	var pinned atomic.Int64
	for range n {
		go func() {
			runtime.LockOSThread()
			pinned.Add(1)
			<-block
		}()
	}
	deadline := time.Now().Add(20 * time.Second)
	for int(pinned.Load()) < n && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	return int(pinned.Load())
}

const threadLine = "could not start another thread"

// TestRunnerThreadFloodStaysInTheRunner floods threads in a runner: the
// runner stops at its cap, the turn ends with one clear line, and the
// account can still start threads. The runner sets its limits with or
// without confinement.
func TestRunnerThreadFloodStaysInTheRunner(t *testing.T) {
	tg := newRunnerTargets(t)
	open, msg := runRunnerProbe(t, tg, "flood-nolimits")
	if open.Threads < floodTarget {
		t.Fatalf("with no limits the runner reached %d threads of %d (%s), so the check proves nothing", open.Threads, floodTarget, msg)
	}
	capped, msg := runRunnerProbe(t, tg, "flood")
	if capped.Threads != 0 || !strings.Contains(msg, threadLine) {
		t.Fatalf("the limited runner reached %d threads, turn error %q", capped.Threads, msg)
	}
	if got := floodThreads(64); got < 64 {
		t.Fatalf("after the flood this account started too few threads: %d", got)
	}
}

// holdThreads runs "wait:before:after": it pins before threads, says it is
// ready, waits for the file wait, pins after threads more and returns the
// count it pinned.
func holdThreads(spec string) int {
	f := strings.Split(spec, ":")
	if len(f) != 3 {
		return -1
	}
	before, _ := strconv.Atoi(f[1])
	after, _ := strconv.Atoi(f[2])
	n := floodThreads(before)
	_ = os.WriteFile(f[0]+".ready", nil, 0o600)
	for deadline := time.Now().Add(60 * time.Second); !fileExists(f[0]) && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	return n + floodThreads(after)
}

type turnResult struct {
	res probeResult
	msg string
	err error
}

func heldTurn(t *testing.T, sock string, tg runnerTargets, wait string, before, after int) <-chan turnResult {
	t.Helper()
	out := make(chan turnResult, 1)
	go func() {
		res, msg, err := runnerTurn(sock, tg, "flood", fmt.Sprintf("hold:%s:%d:%d", wait, before, after))
		out <- turnResult{res, msg, err}
	}()
	for deadline := time.Now().Add(60 * time.Second); !fileExists(wait + ".ready"); {
		select {
		case got := <-out:
			t.Fatalf("the runner that holds %d threads ended before it was ready: %d threads, turn error %q, %v", before, got.res.Threads, got.msg, got.err)
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("the runner that holds %d threads did not get ready", before)
		}
	}
	return out
}

// TestAThreadFloodEndsOnlyItsOwnTurn: three runners at once. The first
// starts and waits; the second holds 200 threads; the third floods and ends
// with the thread line. The first can then start its threads and both end
// well: each runner leaves room in the account's task limit for every
// runner beside it.
func TestAThreadFloodEndsOnlyItsOwnTurn(t *testing.T) {
	tg := newRunnerTargets(t)
	sock := startProbeServe(t, tg, "flood", 3)
	dir := t.TempDir()
	firstGo, heldGo := filepath.Join(dir, "first"), filepath.Join(dir, "held")
	first := heldTurn(t, sock, tg, firstGo, 0, 64)
	held := heldTurn(t, sock, tg, heldGo, 200, 0)
	flood, msg, err := runnerTurn(sock, tg, "flood", "flood")
	if err != nil || flood.Threads != 0 || !strings.Contains(msg, threadLine) {
		t.Fatalf("the flooding runner: %d threads, turn error %q, %v", flood.Threads, msg, err)
	}
	for path, ch := range map[string]<-chan turnResult{firstGo: first, heldGo: held} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		want := map[string]int{firstGo: 64, heldGo: 200}[path]
		if got := <-ch; got.err != nil || got.msg != "" || got.res.Threads != want {
			t.Errorf("%s runner: %d threads of %d, turn error %q, %v", filepath.Base(path), got.res.Threads, want, got.msg, got.err)
		}
	}
}
