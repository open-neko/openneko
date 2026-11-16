package cli

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
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
	socket := filepath.Join(t.TempDir(), "serve.sock")
	if err := checkServeSocket(ctx, socket); err == nil {
		t.Fatal("a missing socket passed")
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := checkServeSocket(ctx, socket); err != nil {
		t.Fatal(err)
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
