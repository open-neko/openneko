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
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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
	brokerKeyDoor = "keydoor"
	brokerSandbox = "sandbox"
	brokerDial    = 10 * time.Second
	runnerStop    = 5 * time.Second
)

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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Runner[0], s.Runner[1:]...)
	cmd.Env = append([]string{}, s.RunnerEnv...)
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{linkChild, brokerChild}
	cmd.Cancel = func() error { return link.Close() }
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
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	go s.serveBroker(ctx, broker)

	enc := json.NewEncoder(link)
	setup := RunnerSetup{RequireDoor: s.RequireDoor, Path: s.Getenv("PATH"), SandboxURL: s.Sandbox.URL, SandboxKey: s.Sandbox.Key}
	if enc.Encode(Frame{Type: "runner", Runner: &setup}) == nil && enc.Encode(start) == nil {
		go func() {
			_, _ = io.Copy(link, reader)
			_ = link.CloseWrite()
		}()
	}
	exited := false
	from := bufio.NewReaderSize(link, 64<<10)
	for {
		line, err := readLine(from)
		if err != nil {
			break
		}
		exited = bytes.HasPrefix(line, []byte(`{"type":"exit"`))
		if _, err := conn.Write(append(line, '\n')); err != nil {
			break
		}
	}
	cancel()
	runErr := <-waited
	if !exited {
		code := 2
		frame := Frame{Type: "exit", Code: &code, Error: "the agent runner stopped before the turn ended"}
		if runErr != nil {
			frame.Error += ": " + runErr.Error()
		}
		_ = json.NewEncoder(conn).Encode(frame)
	}
	return nil
}

// serveBroker answers the runner's requests for a connection with a
// connected descriptor.
func (s *Server) serveBroker(ctx context.Context, broker *net.UnixConn) {
	requests := bufio.NewReader(broker)
	for {
		line, err := requests.ReadString('\n')
		if err != nil {
			return
		}
		conn, err := s.brokerDial(ctx, strings.TrimSpace(line))
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

func (s *Server) brokerDial(ctx context.Context, name string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, brokerDial)
	defer cancel()
	switch name {
	case brokerKeyDoor:
		return keydoor.Dial(ctx)
	case brokerSandbox:
		if !s.Sandbox.Enabled() {
			return nil, errors.New("the terminal is off: no sandbox URL")
		}
		return s.Sandbox.DialService(ctx)
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

// runnerRun replaces the harness in a test's runner.
var runnerRun func(context.Context, harness.Turn) (int, error)

// RunRunner is the agent runner: one turn, on the links that serve passed
// as descriptors 3 and 4. It reaches a model only through the key door and
// the sandbox only through connections that the broker passes, and opens
// no connection itself.
func RunRunner(ctx context.Context) error {
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
		srv.Sandbox = sandbox.Config{URL: setup.SandboxURL, Key: setup.SandboxKey, Dial: broker.dialer(brokerSandbox)}
	}
	srv.defaults()
	srv.handleStream(ctx, link, reader)
	return nil
}
