package serve

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeBroker(t *testing.T, answer string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "brk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "b.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if _, err := readNameLine(c, maxNameLine); err != nil {
			return
		}
		if _, err := c.Write([]byte(answer)); err != nil {
			return
		}
		_, _ = io.Copy(c, c)
	}()
	return path
}

// TestBrokerDialLeavesNoByteOfItsAnswer: the connection that the dialer
// gives back starts with the target's first byte. An HTTP client closes a
// connection on which a byte comes before its request.
func TestBrokerDialLeavesNoByteOfItsAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := socketBroker{path: fakeBroker(t, "+\n")}.dialer(brokerKeyDoor)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping" {
		t.Fatalf("the first bytes after the broker's answer are %q, want %q", got, "ping")
	}
}

func TestBrokerDialGivesTheRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := socketBroker{path: fakeBroker(t, "-the terminal is off: no sandbox URL\n")}.dialer(brokerSandbox)(ctx)
	if err == nil || err.Error() != "the terminal is off: no sandbox URL" {
		t.Fatalf("err = %v", err)
	}
}
