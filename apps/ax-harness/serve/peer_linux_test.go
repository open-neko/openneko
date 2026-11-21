package serve

import (
	"encoding/json"
	"io"
	"net"
	"os"
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
