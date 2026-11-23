package cli

import (
	"context"
	"fmt"
	"github.com/open-neko/neko/apps/openneko/internal/confine"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testsock"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func TestReleaseCheckSchedulerReadsTheAdminSocket(t *testing.T) {
	socket := filepath.Join(testsock.Dir(t), "admin.sock")
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
	if err := checkScheduler(ctx, filepath.Join(testsock.Dir(t), "none.sock")); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("no socket: err = %v", err)
	}
}

func TestReleaseCheckServeSocket(t *testing.T) {
	ctx := context.Background()
	if _, _, _, err := checkServeSocket(ctx, ""); err == nil {
		t.Fatal("an unset socket passed")
	}
	dir := testsock.Dir(t)
	socket := filepath.Join(dir, "serve.sock")
	if _, _, _, err := checkServeSocket(ctx, socket); err == nil {
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
	if _, _, _, err := checkServeSocket(ctx, socket); err == nil || !strings.Contains(err.Error(), "did not report") {
		t.Fatalf("a socket that gives no status: err = %v", err)
	}
	_ = ln.Close()
	confined := confine.State{Mechanism: "landlock", ABI: 8, Confined: true}
	seatbelt := confine.State{Mechanism: "seatbelt", Confined: true}
	oldRunner := runnerAvailable
	defer func() { runnerAvailable = oldRunner }()
	runnerAvailable = func() confine.State { return confine.State{Mechanism: "landlock+seccomp", ABI: 8, Confined: true} }
	for i, c := range []struct {
		env            map[string]string
		state, network confine.State
		want           []string
	}{
		{nil, confined, confined, nil},
		{nil, confine.State{Reason: "on macOS 15.5 the agent runners confine themselves, and a confined process cannot start them"}, seatbelt, nil},
		{nil, confine.State{Reason: "confinement on windows is not built yet"}, confine.State{Reason: "confinement on windows is not built yet"}, nil},
		{nil, confine.State{Mechanism: "landlock", Reason: "no Landlock"}, confined, []string{"openneko serve runs unconfined", "no Landlock"}},
		{nil, confined, confine.State{Mechanism: "seatbelt", Reason: "macOS refused the profile"}, []string{"network part of openneko serve runs unconfined", "refused the profile"}},
		{nil, confined, confine.State{Mechanism: "landlock", Reason: "no Landlock"}, []string{"network part of openneko serve runs unconfined", "no Landlock"}},
		{map[string]string{"OPENAI_API_KEY": "sk-1", "HF_TOKEN": "hf"}, confined, confined, []string{"secret variables", "OPENAI_API_KEY", "HF_TOKEN"}},
		{map[string]string{"OPENNEKO_DATABASE_URL": "postgres://u:p@db/x", "NEKO_SECRET_KEY": "s", "AWS_SECRET_ACCESS_KEY": "a"}, confined, confined,
			[]string{"secret variables", "OPENNEKO_DATABASE_URL", "NEKO_SECRET_KEY", "AWS_SECRET_ACCESS_KEY"}},
		{map[string]string{"OPENNEKO_ENV": "development"}, confined, confined, []string{"development mode"}},
	} {
		cleanServeEnv(t)
		t.Setenv("OPENNEKO_ENV", "production")
		for name, v := range c.env {
			t.Setenv(name, v)
		}
		path := filepath.Join(dir, fmt.Sprintf("serve-%d.sock", i))
		sctx, stop := context.WithCancel(ctx)
		done, err := serveAgentTurns(sctx, io.Discard, path, 1, turnHostStart{sandbox: sandbox.Config{}}, c.state, c.network)
		if err != nil {
			t.Fatal(err)
		}
		got, gotNetwork, _, err := checkServeSocket(ctx, path)
		if err == nil && (got.String() != c.state.String() || gotNetwork.String() != c.network.String()) {
			t.Errorf("release-check read the confinement %q and %q, serve has %q and %q", got, gotNetwork, c.state, c.network)
		}
		stop()
		<-done
		if c.want == nil && err != nil {
			t.Fatalf("a serve with no key: %v", err)
		}
		for _, want := range c.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want an error naming %q, got %v", want, err)
			}
		}
		for name := range c.env {
			if v := os.Getenv(name); v != "" && name != "OPENNEKO_ENV" {
				t.Errorf("serve kept %s=%q in its environment", name, v)
			}
		}
	}
}

// cleanServeEnv blanks the key variables of the test's own environment for
// the test, so that a serve started in the test holds none of them.
func cleanServeEnv(t *testing.T) {
	t.Helper()
	for _, name := range serve.KeyEnvNames(os.Environ()) {
		t.Setenv(name, "")
	}
	t.Setenv("NODE_ENV", "")
}

func TestReleaseCheckPacksNeedsABuiltinPack(t *testing.T) {
	socket := filepath.Join(testsock.Dir(t), "admin.sock")
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
	if _, err := checkBuiltinPacks(ctx, filepath.Join(testsock.Dir(t), "none.sock")); err == nil {
		t.Fatal("no socket: the check passed")
	}
}

func TestReleaseCheckSandbox(t *testing.T) {
	ctx := context.Background()
	run := testsock.Dir(t)
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
	own := filepath.Join(testsock.Dir(t), "sandbox.sock")
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
	sock := filepath.Join(testsock.Dir(t), "sandbox.sock")
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
	t.Setenv("OPENNEKO_SERVE_SOCKET", filepath.Join(testsock.Dir(t), "serve.sock"))
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
