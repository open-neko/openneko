package cli

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func runWeb(t *testing.T, ctx context.Context) (string, <-chan error) {
	t.Helper()
	app := t.TempDir()
	if err := os.WriteFile(filepath.Join(app, "index.html"), []byte("<html>app</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENNEKO_DATABASE_URL_FILE", "")
	addr := freeAddr(t)
	cmd := newWebCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--addr", addr, "--dir", app, "--jobs=false"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	return "http://" + addr, done
}

func TestWebServesAppAndAPIAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base, done := runWeb(t, ctx)

	var body []byte
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := http.Get(base + "/api/v1/version")
		if err == nil {
			body, _ = io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("web did not answer /api/v1/version: %v %s", err, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(body), `"version"`) {
		t.Fatalf("version body %q", body)
	}
	res, err := http.Get(base + "/chat")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(page) != "<html>app</html>" {
		t.Fatalf("app page %q", page)
	}
	res, err = http.Get(base + "/api/v1/work/config/history")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("removed route status %d, want 404", res.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("web stopped with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("web did not stop")
	}
}
