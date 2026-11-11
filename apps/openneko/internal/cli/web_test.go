package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("OPENNEKO_TEST_FAKE_NODE") {
	case "":
		os.Exit(m.Run())
	case "exit3":
		os.Exit(3)
	case "serve":
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		ln, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("HOSTNAME"), os.Getenv("PORT")))
		if err != nil {
			os.Exit(2)
		}
		go func() {
			_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "node %s %s", r.URL.Path, r.Header.Get("X-Forwarded-Host"))
			}))
		}()
		<-sig
		os.Exit(0)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func runWeb(t *testing.T, ctx context.Context, fake string) (string, <-chan error) {
	t.Helper()
	app := t.TempDir()
	if err := os.WriteFile(filepath.Join(app, "index.html"), []byte("<html>app</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENNEKO_DATABASE_URL_FILE", "")
	t.Setenv("OPENNEKO_TEST_FAKE_NODE", fake)
	addr := freeAddr(t)
	cmd := newWebCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--addr", addr, "--dir", app, "--node-addr", freeAddr(t), "--worker-admin-url", "http://127.0.0.1:1", "--", os.Args[0]})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	return "http://" + addr, done
}

func TestWebPassesToNodeAndStopsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base, done := runWeb(t, ctx, "serve")

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
			t.Fatalf("Node did not answer through web: %v %s", err, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.HasPrefix(body, []byte("node /api/v1/version 127.0.0.1:")) {
		t.Fatalf("body %q", body)
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

func TestWebStopsWithNodeExitCode(t *testing.T) {
	_, done := runWeb(t, context.Background(), "exit3")
	select {
	case err := <-done:
		if ExitCodeFor(err) != 3 {
			t.Fatalf("exit code %d, error %v", ExitCodeFor(err), err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("web did not stop when Node stopped")
	}
}
