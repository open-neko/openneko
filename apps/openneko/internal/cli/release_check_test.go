package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func TestReleaseCheckSchedulerReadsTheAdminSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "admin.sock")
	body := `{"status":"ok","lastSuccessAt":null}`
	code := http.StatusOK
	stop, err := serveWebAdmin(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/scheduler" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx := context.Background()
	if err := checkScheduler(ctx, socket); err == nil || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("no tick yet: err = %v", err)
	}
	body = `{"status":"ok","lastSuccessAt":"2026-11-15T09:00:00.000Z"}`
	if err := checkScheduler(ctx, socket); err != nil {
		t.Fatalf("healthy: %v", err)
	}
	code, body = http.StatusServiceUnavailable, `{"status":"degraded","lastSuccessAt":"2026-11-15T09:00:00.000Z"}`
	if err := checkScheduler(ctx, socket); err == nil {
		t.Fatal("a degraded scheduler passed")
	}
	if err := checkScheduler(ctx, filepath.Join(t.TempDir(), "none.sock")); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("no socket: err = %v", err)
	}
}

func TestReleaseCheckServeSocket(t *testing.T) {
	ctx := context.Background()
	if err := checkServeSocket(ctx, ""); err == nil {
		t.Fatal("an unset socket passed")
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "serve.sock")
	if err := checkServeSocket(ctx, socket); err == nil {
		t.Fatal("a missing socket passed")
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	if err := checkServeSocket(ctx, socket); err == nil || !strings.Contains(err.Error(), "did not report") {
		t.Fatalf("a socket that gives no status: err = %v", err)
	}
	_ = ln.Close()
	for i, c := range []struct {
		server *serve.Server
		want   string
	}{
		{&serve.Server{RequireDoor: true}, ""},
		{&serve.Server{RequireDoor: true, KeyEnv: []string{"OPENAI_API_KEY", "HF_TOKEN"}}, "OPENAI_API_KEY, HF_TOKEN"},
		{&serve.Server{RequireDoor: false}, "development mode"},
	} {
		path := filepath.Join(dir, fmt.Sprintf("serve-%d.sock", i))
		ln, err := serve.Listen(path)
		if err != nil {
			t.Fatal(err)
		}
		sctx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = c.server.Serve(sctx, ln); close(done) }()
		err = checkServeSocket(ctx, path)
		stop()
		<-done
		if c.want == "" && err != nil {
			t.Fatalf("a serve with no key: %v", err)
		}
		if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Fatalf("want an error naming %q, got %v", c.want, err)
		}
	}
}

func TestReleaseCheckPacksNeedsABuiltinPack(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "admin.sock")
	body := `{"packs":[{"id":"magento","source":"embedded"},{"id":"mine","source":"uploaded"}]}`
	code := http.StatusOK
	stop, err := serveWebAdmin(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/admin/packs" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx := context.Background()
	if n, err := checkBuiltinPacks(ctx, socket); err != nil || n != 1 {
		t.Fatalf("one built-in pack: n = %d, err = %v", n, err)
	}
	for name, c := range map[string]struct {
		code int
		body string
	}{
		"empty":         {http.StatusOK, `{"packs":[]}`},
		"uploaded only": {http.StatusOK, `{"packs":[{"id":"mine","source":"uploaded"}]}`},
		"error":         {http.StatusInternalServerError, `{"error":"no database"}`},
		"not JSON":      {http.StatusOK, `ok`},
	} {
		code, body = c.code, c.body
		if _, err := checkBuiltinPacks(ctx, socket); err == nil {
			t.Errorf("%s: the check passed", name)
		}
	}
	if _, err := checkBuiltinPacks(ctx, filepath.Join(t.TempDir(), "none.sock")); err == nil {
		t.Fatal("no socket: the check passed")
	}
}

func TestReleaseCheckSandbox(t *testing.T) {
	ctx := context.Background()
	run := t.TempDir()
	serve, check := filepath.Join(run, "serve.sock"), filepath.Join(run, "config-check.sock")
	if state, err := checkSandboxSocket(ctx, "", serve, check); state != "off" || err != nil {
		t.Fatalf("no sandbox: %q %v", state, err)
	}
	if state, err := checkSandboxSocket(ctx, "https://sandbox.example:8443", serve, check); state != "remote" || err != nil {
		t.Fatalf("remote sandbox: %q %v", state, err)
	}
	shared := filepath.Join(run, "sandbox.sock")
	ln, err := net.Listen("unix", shared)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := checkSandboxSocket(ctx, "unix://"+shared, serve, check); err == nil || !strings.Contains(err.Error(), "folder of serve's sockets") {
		t.Fatalf("a sandbox socket beside serve's passed: %v", err)
	}
	own := filepath.Join(t.TempDir(), "sandbox.sock")
	if _, err := checkSandboxSocket(ctx, "unix://"+own, serve, check); err == nil {
		t.Fatal("a sandbox socket with nothing listening passed")
	}
	ln2, err := net.Listen("unix", own)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	if state, err := checkSandboxSocket(ctx, "unix://"+own, serve, check); state != "ok" || err != nil {
		t.Fatalf("own socket folder: %q %v", state, err)
	}
}

func TestReleaseCheckSkills(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		cmd := newReleaseCheckCmd()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--skills"})
		err := cmd.Execute()
		return out.String(), err
	}
	t.Setenv("OPENNEKO_BUILTIN_SKILLS_ROOT", filepath.Join(root, "packages", "llm", "assets", "builtin-skills"))
	t.Setenv("OPENNEKO_SANDBOX_SKILLS_ROOT", filepath.Join(root, "recipes", "sandbox", "skills"))
	if out, err := run(); err != nil || !strings.Contains(out, "bundled_skills=6\n") {
		t.Fatalf("repository skills: %q %v", out, err)
	}
	t.Setenv("OPENNEKO_SANDBOX_SKILLS_ROOT", t.TempDir())
	if out, err := run(); err == nil || !strings.Contains(err.Error(), "xlsx") {
		t.Fatalf("no sandbox skills: %q %v", out, err)
	}
}

func TestReleaseCheckSandboxEgress(t *testing.T) {
	ctx := context.Background()
	sock := filepath.Join(t.TempDir(), "sandbox.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var answer atomic.Value
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(answer.Load().(string)))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	cfg := sandbox.Config{URL: "unix://" + sock, Key: "k"}
	for body, want := range map[string]string{
		`{"status":"ok","egress":"closed"}`: "closed",
		`{"status":"ok","egress":"open"}`:   "open",
		`{"status":"ok"}`:                   "unknown",
	} {
		answer.Store(body)
		if got := checkSandboxEgress(ctx, cfg); got != want {
			t.Errorf("%s: sandbox_egress=%s, want %s", body, got, want)
		}
	}
	answer.Store(`{"status":"ok","egress":"closed"}`)
	if got := checkSandboxEgress(ctx, sandbox.Config{URL: cfg.URL, Key: "wrong"}); got != "unknown" {
		t.Errorf("a refused key gave sandbox_egress=%s, want unknown", got)
	}

	t.Setenv("OPENNEKO_SANDBOX_URL", cfg.URL)
	t.Setenv("OPENNEKO_SANDBOX_KEY", "k")
	t.Setenv("OPENNEKO_SERVE_SOCKET", filepath.Join(t.TempDir(), "serve.sock"))
	t.Setenv("OPENNEKO_CONFIG_CHECK_SOCKET", "")
	run := func() (string, error) {
		cmd := newReleaseCheckCmd()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--sandbox"})
		err := cmd.Execute()
		return out.String(), err
	}
	for _, c := range []struct{ body, out, err string }{
		{`{"status":"ok","egress":"closed"}`, "sandbox_egress=closed\n", ""},
		{`{"status":"ok","egress":"open"}`, "sandbox_egress=open\n", "has a route out"},
		{`{"status":"ok"}`, "sandbox_egress=unknown\n", "did not report that its network is closed"},
	} {
		answer.Store(c.body)
		out, err := run()
		if !strings.Contains(out, c.out) || (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: %q %v, want %q and an error with %q", c.body, out, err, c.out, c.err)
		}
	}
	t.Setenv("OPENNEKO_SANDBOX_EGRESS_BY_OPERATOR", "1")
	answer.Store(`{"status":"ok","egress":"open"}`)
	if out, err := run(); err != nil || !strings.Contains(out, "sandbox_egress=open\nsandbox_egress_by_operator=on\n") {
		t.Errorf("operator enforces egress: %q %v", out, err)
	}
}
