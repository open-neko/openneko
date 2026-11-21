//go:build !windows

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
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/harness"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

// RunnerSupported reports whether this system builds the agent runner.
const RunnerSupported = true

// The runner finds its two links at these descriptors.
const (
	runnerLinkFD   = 3
	runnerBrokerFD = 4
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

func socketPair() (parent, child *os.File, err error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "runner-parent"), os.NewFile(uintptr(fds[1]), "runner-child"), nil
}

func unixConn(f *os.File) (*net.UnixConn, error) {
	defer f.Close()
	conn, err := net.FileConn(f)
	if err != nil {
		return nil, err
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("not a local socket")
	}
	return unix, nil
}

// runInRunner runs one turn in a new runner process. The runner gets the
// turn's link and a broker link as descriptors 3 and 4, and nothing else to
// reach: it asks the broker for each connection to the key door and the
// sandbox. It returns an error only when the runner did not start.
func (s *Server) runInRunner(ctx context.Context, conn io.Writer, reader *bufio.Reader, start Frame) error {
	linkParent, linkChild, err := socketPair()
	if err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	brokerParent, brokerChild, err := socketPair()
	if err != nil {
		_ = linkParent.Close()
		_ = linkChild.Close()
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	link, err := unixConn(linkParent)
	if err != nil {
		_ = linkChild.Close()
		_ = brokerParent.Close()
		_ = brokerChild.Close()
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	defer link.Close()
	broker, err := unixConn(brokerParent)
	if err != nil {
		_ = linkChild.Close()
		_ = brokerChild.Close()
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	defer broker.Close()

	gate, err := s.turnGate(start.Environ)
	if err != nil {
		_ = linkChild.Close()
		_ = brokerChild.Close()
		return fmt.Errorf("terminal unavailable: %w", err)
	}
	if gate != nil {
		defer gate.Close()
	}

	ctx, cancel := context.WithTimeout(ctx, runnerDeadline(start.Spec))
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Runner[0], s.Runner[1:]...)
	cmd.Env = append([]string{}, s.RunnerEnv...)
	logOut := s.runnerLog
	if logOut == nil {
		logOut = os.Stderr
	}
	stderr := newRunnerLog(logOut, fmt.Sprintf("agent runner %d", s.runnerSeq.Add(1)))
	defer stderr.Close()
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{linkChild, brokerChild}
	cmd.SysProcAttr = runnerAttr()
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = runnerStop
	err = cmd.Start()
	_ = linkChild.Close()
	_ = brokerChild.Close()
	if err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	if s.runnerStarted != nil {
		s.runnerStarted(cmd.Process)
	}
	pid := cmd.Process.Pid
	stopGroup := context.AfterFunc(ctx, func() {
		_ = killGroup(pid)
		_ = link.Close()
	})
	defer stopGroup()
	waited := make(chan error, 1)
	go func() {
		waited <- cmd.Wait()
		cancel()
	}()
	go s.serveBroker(ctx, broker, gate)

	enc := json.NewEncoder(link)
	setup := RunnerSetup{RequireDoor: s.RequireDoor, Path: s.Getenv("PATH"), Confine: s.ConfineRunner}
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
	_ = killGroup(pid)
	if exitLine != nil {
		_, _ = conn.Write(append(exitLine, '\n'))
	} else {
		code := 2
		frame := Frame{Type: "exit", Code: &code, Error: "the agent runner stopped before the turn ended"}
		if runErr != nil {
			frame.Error += ": " + runErr.Error()
		}
		_ = json.NewEncoder(conn).Encode(frame)
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

// killGroup kills the runner and every process in its group.
func killGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// serveBroker answers the runner's requests for a connection with a
// connected descriptor.
func (s *Server) serveBroker(ctx context.Context, broker *net.UnixConn, gate *sandbox.Gate) {
	requests := bufio.NewReader(broker)
	since, n := time.Now(), 0
	for {
		line, err := requests.ReadString('\n')
		if err != nil {
			return
		}
		if time.Since(since) >= brokerWindow {
			since, n = time.Now(), 0
		}
		if n++; n > maxBrokerRequests {
			_, _ = broker.Write([]byte(fmt.Sprintf("-the turn asked for more than %d connections in %s\n", maxBrokerRequests, brokerWindow)))
			_ = broker.Close()
			return
		}
		conn, err := s.brokerDial(ctx, strings.TrimSpace(line), gate)
		if err != nil {
			if _, err := broker.Write([]byte("-" + strings.ReplaceAll(err.Error(), "\n", " ") + "\n")); err != nil {
				return
			}
			continue
		}
		err = sendConn(broker, conn)
		_ = conn.Close()
		if err != nil {
			return
		}
	}
}

func (s *Server) brokerDial(ctx context.Context, name string, gate *sandbox.Gate) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, brokerDial)
	defer cancel()
	switch name {
	case brokerKeyDoor:
		return keydoor.Dial(ctx)
	case brokerSandbox:
		if gate == nil {
			return nil, errors.New("the terminal is off: no sandbox URL")
		}
		parent, child, err := socketPair()
		if err != nil {
			return nil, err
		}
		gateEnd, err := unixConn(parent)
		if err != nil {
			_ = child.Close()
			return nil, err
		}
		runnerEnd, err := unixConn(child)
		if err != nil {
			_ = gateEnd.Close()
			return nil, err
		}
		gate.Serve(gateEnd)
		return runnerEnd, nil
	}
	return nil, fmt.Errorf("serve passes no connection named %q", name)
}

func sendConn(broker *net.UnixConn, conn net.Conn) error {
	filer, ok := conn.(interface{ File() (*os.File, error) })
	if !ok {
		return errors.New("the connection has no descriptor")
	}
	f, err := filer.File()
	if err != nil {
		return err
	}
	defer f.Close()
	_, _, err = broker.WriteMsgUnix([]byte("+"), syscall.UnixRights(int(f.Fd())), nil)
	return err
}

// brokerClient is the runner's end of the broker.
type brokerClient struct {
	mu   sync.Mutex
	conn *net.UnixConn
	in   *bufio.Reader
}

func (b *brokerClient) dialer(name string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) { return b.get(ctx, name) }
}

func (b *brokerClient) get(ctx context.Context, name string) (net.Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = b.conn.SetDeadline(deadline)
		defer b.conn.SetDeadline(time.Time{})
	}
	if _, err := b.conn.Write([]byte(name + "\n")); err != nil {
		return nil, fmt.Errorf("the broker: %w", err)
	}
	mark := make([]byte, 1)
	oob := make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := b.conn.ReadMsgUnix(mark, oob)
	if err != nil || n != 1 {
		return nil, fmt.Errorf("the broker gave no answer: %v", err)
	}
	if mark[0] == '-' {
		reason, _ := b.in.ReadString('\n')
		return nil, errors.New(strings.TrimSpace(reason))
	}
	messages, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(messages) != 1 {
		return nil, errors.New("the broker passed no connection")
	}
	fds, err := syscall.ParseUnixRights(&messages[0])
	if err != nil || len(fds) != 1 {
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
		return nil, errors.New("the broker passed no connection")
	}
	f := os.NewFile(uintptr(fds[0]), name)
	defer f.Close()
	return net.FileConn(f)
}

// RunnerLimits bound what one runner may use. Each is a hard limit that the
// runner sets on itself before it confines itself and cannot raise after.
type RunnerLimits struct {
	CPUSeconds   uint64
	AddressSpace uint64
	FileSize     uint64
	OpenFiles    uint64
	// Threads is how many more tasks of this account the runner may start.
	Threads uint64
}

const (
	runnerAddressSpace = 8 << 30
	runnerOpenFiles    = 1024
	runnerThreads      = 256
)

// runnerLimitsFor bounds CPU time by the turn's deadline and a file by what
// one sandbox command may return.
func runnerLimitsFor(spec json.RawMessage) RunnerLimits {
	return RunnerLimits{
		CPUSeconds:   uint64(runnerDeadline(spec) / time.Second),
		AddressSpace: runnerAddressSpace,
		FileSize:     sandbox.MaxTransfer,
		OpenFiles:    runnerOpenFiles,
		Threads:      runnerThreads,
	}
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

// RunRunner is the agent runner: one turn, on the links that serve passed
// as descriptors 3 and 4. It reaches a model only through the key door and
// the sandbox only through connections that the broker passes, and opens
// no connection itself. When serve asks, confine runs after the start frame
// and before any agent code, with the turn's folders.
func RunRunner(ctx context.Context, confine func(folders []string) error) error {
	link, err := unixConn(os.NewFile(runnerLinkFD, "runner-link"))
	if err != nil {
		return fmt.Errorf("the agent runner needs its link from serve: %w", err)
	}
	defer link.Close()
	brokerConn, err := unixConn(os.NewFile(runnerBrokerFD, "runner-broker"))
	if err != nil {
		return fmt.Errorf("the agent runner needs its broker from serve: %w", err)
	}
	defer brokerConn.Close()
	broker := &brokerClient{conn: brokerConn, in: bufio.NewReader(brokerConn)}

	reader := bufio.NewReaderSize(link, 64<<10)
	first, err := readFrame(reader)
	if err != nil || first.Type != "runner" || first.Runner == nil {
		return errors.New("the agent runner expected its setup from serve")
	}
	setup := *first.Runner
	keydoor.SetDialer(broker.dialer(brokerKeyDoor))
	sandbox.RequirePassedConnections()
	srv := &Server{
		Getenv:      func(name string) string { return map[string]string{"PATH": setup.Path}[name] },
		RequireDoor: setup.RequireDoor,
		MaxTurns:    1,
		Run:         runnerRun,
	}
	if setup.SandboxURL != "" {
		srv.Sandbox = sandbox.Config{URL: setup.SandboxURL, Run: setup.SandboxRun, Dial: broker.dialer(brokerSandbox)}
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
		srv.limit = func(spec json.RawMessage) error { return limitRunner(runnerLimitsFor(spec)) }
	}
	srv.defaults()
	srv.handleStream(ctx, link, reader)
	return nil
}
