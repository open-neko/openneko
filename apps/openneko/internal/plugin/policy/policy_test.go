package policy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultPolicyIsSecureByDefault(t *testing.T) {
	if DefaultPolicy.AllowUnverified {
		t.Errorf("default policy should not allow unverified installs")
	}
	if DefaultPolicy.AllowGitURLInstalls {
		t.Errorf("default policy should not allow git-URL installs")
	}
	if DefaultPolicy.AllowSandboxedSkillEscape {
		t.Errorf("default policy should not allow sandboxed-skill escape")
	}
	if len(DefaultPolicy.AllowedMarketplaces) != 1 ||
		DefaultPolicy.AllowedMarketplaces[0] != "https://open-neko.github.io/plugins/marketplace.json" {
		t.Errorf("default policy should ship the official marketplace, got %v", DefaultPolicy.AllowedMarketplaces)
	}
}

func TestAllowsUnverified(t *testing.T) {
	p := Policy{AllowUnverified: true}
	if !p.Allows(SourceUnverified) {
		t.Errorf("expected unverified to be allowed when AllowUnverified=true")
	}
	if p.Allows(SourceGitURL) {
		t.Errorf("expected git-URL to be disallowed when AllowGitURLInstalls=false")
	}
}

func TestAllowsGitURL(t *testing.T) {
	p := Policy{AllowGitURLInstalls: true}
	if !p.Allows(SourceGitURL) {
		t.Errorf("expected git-URL to be allowed")
	}
	if p.Allows(SourceUnverified) {
		t.Errorf("expected unverified to be disallowed")
	}
}

func TestMarketplaceAllowed(t *testing.T) {
	p := Policy{AllowedMarketplaces: []string{"https://x.com", "https://y.com"}}
	if !p.MarketplaceAllowed("https://x.com") {
		t.Errorf("expected x.com to be allowed")
	}
	if p.MarketplaceAllowed("https://z.com") {
		t.Errorf("expected z.com to be disallowed")
	}
}

func TestFetchReturnsParsedPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/install-policy" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"policy": map[string]any{
				"allowUnverified":           true,
				"allowGitUrlInstalls":       false,
				"allowedMarketplaces":       []string{"https://open-neko.github.io/plugins/marketplace.json"},
				"allowSandboxedSkillEscape": true,
			},
			"source": "org",
		})
	}))
	defer srv.Close()

	t.Setenv("WORKER_ADMIN_URL", srv.URL)

	pol, source, err := Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if source != "org" {
		t.Errorf("expected source=org, got %q", source)
	}
	if !pol.AllowUnverified {
		t.Errorf("expected allowUnverified=true")
	}
	if pol.AllowGitURLInstalls {
		t.Errorf("expected allowGitUrlInstalls=false")
	}
	if !pol.AllowSandboxedSkillEscape {
		t.Errorf("expected allowSandboxedSkillEscape=true")
	}
}

func TestFetchReturnsDefaultsWhenWorkerUnreachable(t *testing.T) {
	t.Setenv("WORKER_ADMIN_URL", "http://127.0.0.1:1") // closed port
	pol, source, _ := Fetch(context.Background())
	if source != "unreachable" {
		t.Errorf("expected source=unreachable, got %q", source)
	}
	if pol.AllowUnverified {
		t.Errorf("unreachable worker should not grant unverified — got %+v", pol)
	}
	if !pol.MarketplaceAllowed("https://open-neko.github.io/plugins/marketplace.json") {
		t.Errorf("unreachable fallback should still allow the official marketplace")
	}
}

func TestFetchReturnsDefaultsOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("WORKER_ADMIN_URL", srv.URL)
	pol, source, err := Fetch(context.Background())
	if err == nil {
		t.Fatalf("expected an error on 500")
	}
	if source != "unreachable" {
		t.Errorf("expected source=unreachable, got %q", source)
	}
	if pol.AllowUnverified {
		t.Errorf("expected fallback to default (no unverified) on 500")
	}
}
