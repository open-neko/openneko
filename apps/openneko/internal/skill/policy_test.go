package skill

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPolicyIsSecureByDefault(t *testing.T) {
	if DefaultPolicy.AllowGitURLInstalls {
		t.Errorf("default policy should not allow git-URL installs")
	}
}

func TestFetchPolicyReturnsParsedPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/install-policy" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"policy": map[string]any{"allowGitUrlInstalls": true},
			"source": "org",
		})
	}))
	defer srv.Close()

	t.Setenv("WORKER_ADMIN_URL", srv.URL)

	pol, source, err := FetchPolicy(context.Background())
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if source != "org" {
		t.Errorf("expected source=org, got %q", source)
	}
	if !pol.AllowGitURLInstalls {
		t.Errorf("expected allowGitUrlInstalls=true")
	}
}

func TestFetchPolicyReturnsDefaultsWhenWorkerUnreachable(t *testing.T) {
	t.Setenv("WORKER_ADMIN_URL", "http://127.0.0.1:1") // closed port
	pol, source, _ := FetchPolicy(context.Background())
	if source != "unreachable" {
		t.Errorf("expected source=unreachable, got %q", source)
	}
	if pol.AllowGitURLInstalls {
		t.Errorf("unreachable worker should not grant git-URL installs — got %+v", pol)
	}
}

func TestFetchPolicyReturnsDefaultsOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("WORKER_ADMIN_URL", srv.URL)
	pol, source, err := FetchPolicy(context.Background())
	if err == nil {
		t.Fatalf("expected an error on 500")
	}
	if source != "unreachable" {
		t.Errorf("expected source=unreachable, got %q", source)
	}
	if pol.AllowGitURLInstalls {
		t.Errorf("expected fallback to default on 500")
	}
}

func TestFetchPolicyOverWorkerSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "policy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "worker.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"policy": map[string]any{"allowGitUrlInstalls": r.URL.Path == "/admin/install-policy"}, "source": "org"})
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	t.Setenv("WORKER_ADMIN_URL", "unix://"+socket)

	pol, source, err := FetchPolicy(context.Background())
	if err != nil || source != "org" || !pol.AllowGitURLInstalls {
		t.Fatalf("got %+v %q %v", pol, source, err)
	}
}
