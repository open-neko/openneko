package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func TestServeAgentTurnsAnswersOnTheSocket(t *testing.T) {
	cleanServeEnv(t)
	socket := filepath.Join(testsock.Dir(t), "serve.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done, err := serveAgentTurns(ctx, io.Discard, socket, 2, sandbox.Config{}, confine.State{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o007 != 0 {
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

func TestServeAgentTurnsRefusesABadSandboxURL(t *testing.T) {
	cleanServeEnv(t)
	socket := filepath.Join(testsock.Dir(t), "serve.sock")
	if _, err := serveAgentTurns(context.Background(), io.Discard, socket, 1, sandbox.Config{URL: "ftp://sandbox"}, confine.State{}); err == nil {
		t.Fatal("serve started with a sandbox URL it cannot use")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("serve opened its socket before the sandbox URL was checked: %v", err)
	}
}

func TestServeAgentTurnsWarnsWhenTheSandboxSharesItsSocketFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a unix:// sandbox URL is for Linux and macOS; Windows has no local sandbox (WIN0 A9)")
	}
	cleanServeEnv(t)
	dir := testsock.Dir(t)
	for _, tc := range []struct {
		url  string
		warn bool
	}{
		{"unix://" + filepath.Join(dir, "sandbox.sock"), true},
		{"unix://" + filepath.Join(dir, "sandbox", "sandbox.sock"), false},
		{"https://sandbox.example:8443", false},
	} {
		socket := filepath.Join(dir, "serve.sock")
		var out strings.Builder
		ctx, cancel := context.WithCancel(context.Background())
		done, err := serveAgentTurns(ctx, &out, socket, 1, sandbox.Config{URL: tc.url}, confine.State{})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		<-done
		if got := strings.Contains(out.String(), "warning: the sandbox socket"); got != tc.warn {
			t.Errorf("%s: warning = %v, want %v\n%s", tc.url, got, tc.warn, out.String())
		}
	}
}
