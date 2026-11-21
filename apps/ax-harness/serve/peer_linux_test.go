package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func statusAnswered(t *testing.T, socket string) bool {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(Frame{Type: "status"}); err != nil {
		return false
	}
	var reply Frame
	err = json.NewDecoder(conn).Decode(&reply)
	if err != nil && err != io.EOF {
		t.Logf("read: %v", err)
	}
	return err == nil && reply.Type == "status"
}

// TestServeRefusesAPeerItIsToldTo: the agent socket closes a connection
// from a process that RefusePeer names, before it reads a frame.
func TestServeRefusesAPeerItIsToldTo(t *testing.T) {
	asked := make(chan int, 4)
	refused := &Server{Log: io.Discard, RefusePeer: func(pid int) bool { asked <- pid; return true }}
	if statusAnswered(t, startServer(t, refused)) {
		t.Fatal("serve answered a process that RefusePeer names")
	}
	if pid := <-asked; pid != os.Getpid() {
		t.Fatalf("RefusePeer was asked about %d, want this process %d", pid, os.Getpid())
	}
	kept := &Server{Log: io.Discard, RefusePeer: func(int) bool { return false }}
	if !statusAnswered(t, startServer(t, kept)) {
		t.Fatal("serve did not answer a process that RefusePeer keeps")
	}
}

const peerExitEnv = "OPENNEKO_TEST_PEER_EXIT"

// TestPeerExitChild connects to the agent socket and exits at once.
func TestPeerExitChild(t *testing.T) {
	socket := os.Getenv(peerExitEnv)
	if socket == "" {
		t.Skip("helper process")
	}
	if _, err := net.Dial("unix", socket); err != nil {
		t.Fatal(err)
	}
}

// TestServeRefusesAPeerThatExitedDuringTheCheck: a peer that passed the
// check by pid but exited before it ended is refused, because its pid may
// already name another process.
func TestServeRefusesAPeerThatExitedDuringTheCheck(t *testing.T) {
	reaped := make(chan struct{})
	var log lockedBuffer
	srv := &Server{Log: &log, RefusePeer: func(int) bool { <-reaped; return false }}
	socket := startServer(t, srv)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPeerExitChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), peerExitEnv+"="+socket)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	close(reaped)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(log.String(), fmt.Sprintf("refused a connection on its agent socket from process %d", cmd.Process.Pid)) {
			return
		}
	}
	t.Fatalf("serve kept a connection from a process that exited: log %q", log.String())
}
