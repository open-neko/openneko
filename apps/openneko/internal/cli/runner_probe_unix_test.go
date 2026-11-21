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
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

const runnerProbeEnv = "OPENNEKO_TEST_RUNNER_PROBE"

// floodTarget is above the runner's thread cap of 256 more tasks.
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
		if strings.HasPrefix(in.Mode, "flood") {
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
	in := tg.in
	in.Mode = mode
	raw, _ := json.Marshal(in)
	sock := filepath.Join(testsock.Dir(t), "serve.sock")
	ln, err := serve.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &serve.Server{Getenv: os.Getenv, MaxTurns: 1, ConfineRunner: mode != "control",
		Runner:    []string{os.Args[0], "-test.run=^TestRunnerProbeChild$", "-test.count=1"},
		RunnerEnv: []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + tg.tmp, runnerProbeEnv + "=" + string(raw)}}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { stop(); <-done }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	start := serve.Frame{Type: "start", Spec: json.RawMessage(`{}`), Environ: []string{"OPENNEKO_HARNESS_WORKSPACE_DIR=" + tg.workspace,
		"OPENNEKO_MCP_ORG_ID=org-1", "OPENNEKO_MCP_THREAD_ID=thread-1"}}
	if err := json.NewEncoder(conn).Encode(start); err != nil {
		t.Fatal(err)
	}
	var res probeResult
	dec := json.NewDecoder(conn)
	for {
		var f serve.Frame
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("%s: the turn ended with no exit frame: %v", mode, err)
		}
		switch f.Type {
		case "event":
			_ = json.Unmarshal(f.Event, &res)
		case "exit":
			removeProbeLeftovers(in)
			_ = os.Remove(in.Outside)
			_ = os.Remove(filepath.Join(tg.workspace, "tool"))
			_ = os.WriteFile(in.Victim, []byte("keep me"), 0o600)
			return res, f.Error
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
