package cli

import (
	"context"
	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func leaveStaleSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("no stale socket at %s: %v %v", path, info, err)
	}
}

func TestListenersReplaceAStaleSocket(t *testing.T) {
	cleanServeEnv(t)
	dir := testsock.Dir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listeners := map[string]func(string) error{
		"web-admin.sock": func(path string) error {
			stop, err := serveWebAdmin(path, http.NotFoundHandler())
			if err == nil {
				t.Cleanup(stop)
			}
			return err
		},
		"serve.sock": func(path string) error {
			_, err := serveAgentTurns(ctx, io.Discard, path, 1, sandbox.Config{}, confine.State{})
			return err
		},
		"keydoor.sock": func(path string) error {
			_, err := llm.ListenDoor(ctx, path, &llm.KeyDoor{})
			return err
		},
	}
	for name, listen := range listeners {
		path := filepath.Join(dir, name)
		leaveStaleSocket(t, path)
		if err := listen(path); err != nil {
			t.Fatalf("%s over a stale socket: %v", name, err)
		}
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = conn.Close()
	}

	plain := filepath.Join(dir, "plain.sock")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := serveWebAdmin(plain, http.NotFoundHandler()); err == nil || !strings.Contains(err.Error(), "is not a socket") {
		t.Fatalf("the web admin socket replaced a plain file: %v", err)
	}
}
