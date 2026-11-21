package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const (
	gateURL       = "http://sandbox"
	brokerKeyDoor = "keydoor"
	brokerSandbox = "sandbox"
	brokerDial    = 10 * time.Second
	runnerStop    = 5 * time.Second
	// Web stops a turn at its timeout_ms, at most 30 minutes.
	maxRunnerTurn     = 30 * time.Minute
	maxBrokerRequests = 1024
)

// runnerTurnGrace lets web's own stop come first.
var runnerTurnGrace = time.Minute

// brokerWindow is the span in which a turn may ask for maxBrokerRequests
// connections.
var brokerWindow = time.Minute

// brokerBudget counts a turn's requests for a connection.
type brokerBudget struct {
	since time.Time
	n     int
}

func (b *brokerBudget) take() error {
	if time.Since(b.since) >= brokerWindow {
		b.since, b.n = time.Now(), 0
	}
	if b.n++; b.n > maxBrokerRequests {
		return fmt.Errorf("the turn asked for more than %d connections in %s", maxBrokerRequests, brokerWindow)
	}
	return nil
}

// turnGate opens no connection: the gate reaches the sandbox when the
// runner first asks for it.
func (s *Server) turnGate(environ []string) (*sandbox.Gate, error) {
	if !s.Sandbox.Enabled() {
		return nil, nil
	}
	env := map[string]string{}
	for _, entry := range environ {
		if name, value, ok := strings.Cut(entry, "="); ok {
			env[name] = value
		}
	}
	egress, err := sandbox.TurnEgress(func(name string) string { return env[name] })
	if err != nil {
		return nil, err
	}
	return sandbox.NewGate(s.Sandbox, egress)
}

// runnerDeadline is the turn's timeout_ms, at most maxRunnerTurn, and a
// grace.
func runnerDeadline(spec json.RawMessage) time.Duration {
	var limits struct {
		TimeoutMs float64 `json:"timeout_ms"`
	}
	turn := maxRunnerTurn
	if json.Unmarshal(spec, &limits) == nil && limits.TimeoutMs > 0 {
		turn = min(turn, time.Duration(limits.TimeoutMs)*time.Millisecond)
	}
	return turn + runnerTurnGrace
}

// relayTurn sends the runner its setup and the turn on link, passes the
// runner's frames to conn and ends with one exit frame. waited gets the
// runner's end; reap then stops what the runner left before the exit frame.
func (s *Server) relayTurn(conn io.Writer, reader *bufio.Reader, start Frame, link io.ReadWriter, gate *sandbox.Gate, cancel func(), waited <-chan error, reap func(), stderr *runnerLog) {
	enc := json.NewEncoder(link)
	setup := RunnerSetup{RequireDoor: s.RequireDoor, Path: s.Getenv("PATH"), Confine: s.ConfineRunner, Turns: s.MaxTurns}
	if gate != nil {
		setup.SandboxURL, setup.SandboxRun = gateURL, gate.Run()
	}
	if enc.Encode(Frame{Type: "runner", Runner: &setup}) == nil && enc.Encode(start) == nil {
		go func() {
			_, _ = io.Copy(link, reader)
			cancel()
		}()
	}
	var exitLine []byte
	from := bufio.NewReaderSize(link, 64<<10)
	for {
		line, err := readLine(from)
		if err != nil {
			break
		}
		var frame struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &frame) == nil && frame.Type == "exit" {
			exitLine = line
			break
		}
		if _, err := conn.Write(append(line, '\n')); err != nil {
			break
		}
	}
	cancel()
	runErr := <-waited
	reap()
	if exitLine != nil {
		_, _ = conn.Write(append(exitLine, '\n'))
		return
	}
	code := 2
	frame := Frame{Type: "exit", Code: &code, Error: "the agent runner stopped before the turn ended"}
	if stderr.outOfThreads() {
		frame.Error = fmt.Sprintf("the agent runner could not start another thread (each runner may have %d, and the runners of this account share one task limit), so this turn ended", runnerThreads)
	} else if runErr != nil {
		frame.Error += ": " + runErr.Error()
	}
	_ = json.NewEncoder(conn).Encode(frame)
}

// RunnerStart is what serve gives the code that starts one runner on a
// system where the runner cannot confine itself.
type RunnerStart struct {
	Args, Env []string
	// Stderr takes the runner's output and errors.
	Stderr *os.File
	// Folders are the turn's folders: the only paths the runner may read and
	// write.
	Folders []string
	// Links are the two sockets that the runner may connect to.
	Links  []string
	Limits RunnerLimits
}

// RunnerProcess is one started runner.
type RunnerProcess interface {
	Pid() int
	// Wait returns when the runner has ended, with an error for an exit
	// code that is not 0.
	Wait() error
	// Kill ends the runner and every process it started.
	Kill() error
	// Close takes back what the start gave the runner, after Wait.
	Close() error
}

// RunnerLimits bound what one runner may use. Each is a hard limit that the
// runner cannot raise.
type RunnerLimits struct {
	CPUSeconds   uint64
	AddressSpace uint64
	FileSize     uint64
	OpenFiles    uint64
	// Threads caps the threads of the runner's own process.
	Threads uint64
	// AccountTasks is how many more tasks of this account the runner lets
	// the account start, for itself and the runners beside it. Native code
	// in one runner can take all of it.
	AccountTasks uint64
}

const (
	runnerAddressSpace = 8 << 30
	runnerOpenFiles    = 1024
	runnerThreads      = 256
)

// runnerLimitsFor bounds CPU time by the turn's deadline and a file by what
// one sandbox command may return.
func runnerLimitsFor(spec json.RawMessage, turns int) RunnerLimits {
	return RunnerLimits{
		CPUSeconds:   uint64(runnerDeadline(spec) / time.Second),
		AddressSpace: runnerAddressSpace,
		FileSize:     sandbox.MaxTransfer,
		OpenFiles:    runnerOpenFiles,
		Threads:      runnerThreads,
		AccountTasks: runnerThreads * uint64(max(turns, 1)),
	}
}

// limitThreads caps the runner's threads in the Go runtime, which ends the
// runner with "thread exhaustion" past the cap. macOS and Windows have no
// kernel cap for one process's threads.
func limitThreads(l RunnerLimits) {
	debug.SetMaxThreads(int(l.Threads))
}

var runnerLimitsOff bool

// SetRunnerLimitsOff stops this process's agent runner from limiting itself,
// so a proof test can show each limit holds. It panics outside a test binary.
func SetRunnerLimitsOff(off bool) {
	if !testing.Testing() {
		panic("serve.SetRunnerLimitsOff works only in a test binary")
	}
	runnerLimitsOff = off
}

// runnerRun replaces the harness in a test's runner.
var runnerRun func(context.Context, harness.Turn) (int, error)

// SetRunnerTurn replaces the harness in this process's agent runner, so a
// proof test can run its checks where agent code runs. It panics outside a
// test binary.
func SetRunnerTurn(run func(context.Context, harness.Turn) (int, error)) {
	if !testing.Testing() {
		panic("serve.SetRunnerTurn works only in a test binary")
	}
	runnerRun = run
}

// runRunner runs the one turn of a runner on link. dial gives each
// connection to the key door and the sandbox.
func runRunner(ctx context.Context, link net.Conn, dial func(name string) func(context.Context) (net.Conn, error), confine func(folders []string) error) error {
	reader := bufio.NewReaderSize(link, 64<<10)
	first, err := readFrame(reader)
	if err != nil || first.Type != "runner" || first.Runner == nil {
		return errors.New("the agent runner expected its setup from serve")
	}
	setup := *first.Runner
	keydoor.SetDialer(dial(brokerKeyDoor))
	sandbox.RequirePassedConnections()
	srv := &Server{
		Getenv:      func(name string) string { return map[string]string{"PATH": setup.Path}[name] },
		RequireDoor: setup.RequireDoor,
		MaxTurns:    1,
		Run:         runnerRun,
	}
	if setup.SandboxURL != "" {
		srv.Sandbox = sandbox.Config{URL: setup.SandboxURL, Run: setup.SandboxRun, Dial: dial(brokerSandbox)}
	}
	if setup.Confine {
		srv.confine = func(environ []string) error {
			if confine == nil {
				return errors.New("this agent runner cannot confine itself")
			}
			return confine(RunnerFolders(environ))
		}
	}
	if !setup.Confine && testing.Testing() {
		srv.testOnly = true
	}
	if !runnerLimitsOff {
		srv.limit = func(spec json.RawMessage) error {
			l := runnerLimitsFor(spec, setup.Turns)
			limitThreads(l)
			return limitRunner(l)
		}
	}
	srv.defaults()
	srv.handleStream(ctx, link, reader)
	return nil
}
