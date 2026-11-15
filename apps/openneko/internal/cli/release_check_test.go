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
