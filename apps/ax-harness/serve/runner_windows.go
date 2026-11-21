package serve

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/ownacl"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

// RunnerSupported reports whether this system builds the agent runner.
const RunnerSupported = true

// RunnerLinksEnv names the folder that holds a runner's two sockets: the
// turn's link and the broker.
const RunnerLinksEnv = "OPENNEKO_RUNNER_LINKS"

const (
	linkSocket   = "link.sock"
	brokerSocket = "broker.sock"
	maxNameLine  = 64
)

// sioAFUnixGetPeerPID is SIO_AF_UNIX_GETPEERPID of afunix.h.
const sioAFUnixGetPeerPID = 0x58000100

// runInRunner runs one turn in a new runner process. Windows passes no
// socket to a child, so serve listens on two sockets in a folder of the
// turn's own, and takes a connection on them only from the runner's
// process. It returns an error only when the runner did not start.
func (s *Server) runInRunner(ctx context.Context, conn io.Writer, reader *bufio.Reader, start Frame) error {
	startRunner := s.StartRunner
	if startRunner == nil {
		if s.ConfineRunner {
			return errors.New("the agent runner did not start: this serve has no way to start a confined runner on Windows")
		}
		startRunner = startPlainRunner
	}
	gate, err := s.turnGate(start.Environ)
	if err != nil {
		return fmt.Errorf("terminal unavailable: %w", err)
	}
	if gate != nil {
		defer gate.Close()
	}
	links, err := os.MkdirTemp("", "openneko-runner-")
	if err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	defer os.RemoveAll(links)
	if err := ownacl.Set(links); err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	linkLn, err := net.Listen("unix", filepath.Join(links, linkSocket))
	if err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	defer linkLn.Close()
	brokerLn, err := net.Listen("unix", filepath.Join(links, brokerSocket))
	if err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	defer brokerLn.Close()

	logOut := s.runnerLog
	if logOut == nil {
		logOut = os.Stderr
	}
	stderr := newRunnerLog(logOut, fmt.Sprintf("agent runner %d", s.runnerSeq.Add(1)))
	defer stderr.Close()
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, errRead)
		_ = errRead.Close()
		close(copied)
	}()
	defer func() { <-copied }()

	ctx, cancel := context.WithTimeout(ctx, runnerDeadline(start.Spec))
	defer cancel()
	proc, err := startRunner(RunnerStart{
		Args:    append([]string{}, s.Runner...),
		Env:     append(append([]string{}, s.RunnerEnv...), RunnerLinksEnv+"="+links),
		Stderr:  errWrite,
		Folders: RunnerFolders(start.Environ),
		Links:   []string{filepath.Join(links, linkSocket), filepath.Join(links, brokerSocket)},
		Limits:  runnerLimitsFor(start.Spec, s.MaxTurns),
	})
	_ = errWrite.Close()
	if err != nil {
		return fmt.Errorf("the agent runner did not start: %w", err)
	}
	defer proc.Close()
	pid := proc.Pid()
	stop := context.AfterFunc(ctx, func() {
		_ = proc.Kill()
		_ = linkLn.Close()
		_ = brokerLn.Close()
	})
	defer stop()
	waited := make(chan error, 1)
	go func() {
		waited <- proc.Wait()
		cancel()
	}()
	reap := func() { _ = proc.Kill() }

	link, err := s.acceptFrom(linkLn, pid)
	if err != nil {
		cancel()
		s.relayTurn(conn, reader, start, closedLink{}, nil, cancel, waited, reap, stderr)
		return nil
	}
	defer link.Close()
	_ = linkLn.Close()
	stopLink := context.AfterFunc(ctx, func() { _ = link.Close() })
	defer stopLink()
	go s.serveBrokerSockets(ctx, brokerLn, pid, gate)
	s.relayTurn(conn, reader, start, link, gate, cancel, waited, reap, stderr)
	return nil
}

// closedLink stands for a runner that never connected.
type closedLink struct{}

func (closedLink) Read([]byte) (int, error)  { return 0, io.EOF }
func (closedLink) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// acceptFrom returns the first connection on ln from process pid. It closes
// a connection from any other process.
func (s *Server) acceptFrom(ln net.Listener, pid int) (net.Conn, error) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return nil, err
		}
		if from, err := peerPID(c); err != nil || from != pid {
			fmt.Fprintf(s.logOut(), "serve refused a connection on an agent runner's socket from process %d: %v\n", from, err)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// peerPID asks Windows for the process at the other end of a local socket.
func peerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a local socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid uint32
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		var n uint32
		ioErr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &n, nil, 0)
	}); err != nil {
		return 0, err
	}
	if ioErr != nil {
		return 0, fmt.Errorf("Windows names no process for the socket: %w", ioErr)
	}
	return int(pid), nil
}

// serveBrokerSockets answers the runner's requests for a connection. Each
// request is one connection to the broker socket that names the target;
// serve answers "+" and then carries the bytes both ways, or "-" and why.
func (s *Server) serveBrokerSockets(ctx context.Context, ln net.Listener, pid int, gate *sandbox.Gate) {
	var budget brokerBudget
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := s.acceptFrom(ln, pid)
		if err != nil {
			return
		}
		if err := budget.take(); err != nil {
			_, _ = c.Write([]byte("-" + err.Error() + "\n"))
			_ = c.Close()
			_ = ln.Close()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.brokerSocket(ctx, c, gate)
		}()
	}
}

func (s *Server) brokerSocket(ctx context.Context, c net.Conn, gate *sandbox.Gate) {
	_ = c.SetReadDeadline(time.Now().Add(brokerDial))
	name, err := readNameLine(c)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	refuse := func(err error) {
		_, _ = c.Write([]byte("-" + strings.ReplaceAll(err.Error(), "\n", " ") + "\n"))
		_ = c.Close()
	}
	switch name {
	case brokerKeyDoor:
		dctx, cancel := context.WithTimeout(ctx, brokerDial)
		door, err := keydoor.Dial(dctx)
		cancel()
		if err != nil {
			refuse(err)
			return
		}
		if _, err := c.Write([]byte("+\n")); err != nil {
			_ = c.Close()
			_ = door.Close()
			return
		}
		splice(ctx, c, door)
	case brokerSandbox:
		if gate == nil {
			refuse(errors.New("the terminal is off: no sandbox URL"))
			return
		}
		if _, err := c.Write([]byte("+\n")); err != nil {
			_ = c.Close()
			return
		}
		gate.Serve(c)
	default:
		refuse(fmt.Errorf("serve passes no connection named %q", name))
	}
}

// readNameLine reads one short line a byte at a time, so no byte after it
// is taken from the connection.
func readNameLine(r io.Reader) (string, error) {
	var b strings.Builder
	one := make([]byte, 1)
	for b.Len() <= maxNameLine {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return b.String(), nil
		}
		b.WriteByte(one[0])
	}
	return "", errors.New("the line is too long")
}

// splice carries bytes both ways until one side ends or ctx ends.
func splice(ctx context.Context, a, b net.Conn) {
	stop := context.AfterFunc(ctx, func() {
		_ = a.Close()
		_ = b.Close()
	})
	defer stop()
	done := make(chan struct{}, 2)
	for _, pair := range [][2]net.Conn{{a, b}, {b, a}} {
		go func() {
			_, _ = io.Copy(pair[0], pair[1])
			done <- struct{}{}
		}()
	}
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

// plainRunner is a runner that a test starts with no confinement.
type plainRunner struct{ p *os.Process }

func startPlainRunner(r RunnerStart) (RunnerProcess, error) {
	p, err := os.StartProcess(r.Args[0], r.Args, &os.ProcAttr{Env: r.Env, Files: []*os.File{nil, r.Stderr, r.Stderr}})
	if err != nil {
		return nil, err
	}
	return plainRunner{p}, nil
}

func (r plainRunner) Pid() int { return r.p.Pid }
func (r plainRunner) Kill() error {
	err := r.p.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
func (r plainRunner) Close() error { return nil }
func (r plainRunner) Wait() error {
	st, err := r.p.Wait()
	if err != nil {
		return err
	}
	if !st.Success() {
		return fmt.Errorf("exit status %d", st.ExitCode())
	}
	return nil
}

// socketBroker is the runner's end of the broker.
type socketBroker struct{ path string }

func (b socketBroker) dialer(name string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", b.path)
		if err != nil {
			return nil, fmt.Errorf("the broker: %w", err)
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = c.SetDeadline(deadline)
		}
		mark := make([]byte, 1)
		if _, err := c.Write([]byte(name + "\n")); err == nil {
			_, err = io.ReadFull(c, mark)
		}
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("the broker gave no answer: %v", err)
		}
		if mark[0] != '+' {
			reason, _ := bufio.NewReader(io.LimitReader(c, 4<<10)).ReadString('\n')
			_ = c.Close()
			return nil, errors.New(strings.TrimSpace(reason))
		}
		_ = c.SetDeadline(time.Time{})
		return c, nil
	}
}

// RunRunner is the agent runner: one turn, on the link socket in the folder
// that serve names in RunnerLinksEnv. It reaches a model only through the
// key door and the sandbox only through the broker, and opens no other
// connection. When serve asks, confine runs after the start frame and
// before any agent code, with the turn's folders.
func RunRunner(ctx context.Context, confine func(folders []string) error) error {
	links := os.Getenv(RunnerLinksEnv)
	if links == "" {
		return errors.New("the agent runner needs its link folder from serve")
	}
	link, err := net.Dial("unix", filepath.Join(links, linkSocket))
	if err != nil {
		return fmt.Errorf("the agent runner needs its link from serve: %w", err)
	}
	defer link.Close()
	return runRunner(ctx, link, socketBroker{path: filepath.Join(links, brokerSocket)}.dialer, confine)
}
