//go:build !windows

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeAgentTurnsAnswersOnTheSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "serve.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done, err := serveAgentTurns(ctx, io.Discard, socket, 2)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o007 != 0 {
		t.Fatalf("socket mode %v is open to others", info.Mode().Perm())
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(`{"type":"start"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	var frame struct {
		Type  string `json:"type"`
		Code  *int   `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "exit" || frame.Code == nil || *frame.Code != 2 || frame.Error == "" {
		t.Fatalf("frame = %s", line)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
}
