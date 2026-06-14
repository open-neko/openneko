package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recorded struct {
	method string
	path   string
	body   map[string]any
}

// mockSetupServer answers every endpoint the onboarding flow touches and
// records each request so tests can assert the call sequence and bodies.
func mockSetupServer(changed bool, recs *[]recorded) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*recs = append(*recs, recorded{r.Method, r.URL.Path, body})
		enc := json.NewEncoder(w)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/admin/change-password":
			_ = enc.Encode(map[string]bool{"changed": changed})
		case "GET /api/settings/data-source":
			_ = enc.Encode(map[string]any{"source": "unset"})
		case "GET /api/settings/agent":
			_ = enc.Encode(map[string]any{
				"agent": map[string]any{"source": "default", "backend": "hermes", "globalCap": 5},
				"options": []map[string]string{
					{"value": "hermes", "label": "Hermes", "description": "any provider"},
					{"value": "claude-agent", "label": "Claude Agent", "description": "anthropic"},
				},
				"defaults": map[string]any{"globalCap": 5},
			})
		case "GET /api/settings/provider":
			_ = enc.Encode(map[string]any{
				"primary":  map[string]any{"scope": "primary", "source": "default", "provider": "anthropic", "model": "", "enabled": false, "config": map[string]any{}},
				"research": map[string]any{"scope": "research", "source": "default", "provider": "disabled", "model": "", "enabled": false, "config": map[string]any{}},
				"options": map[string]any{
					"primary":  []map[string]string{{"value": "anthropic", "label": "Anthropic", "description": "claude"}},
					"research": []map[string]string{{"value": "disabled", "label": "Disabled", "description": "off"}, {"value": "perplexity", "label": "Perplexity", "description": "research"}},
				},
				"defaults": map[string]any{
					"primary":  map[string]string{"anthropic": "claude-opus-4-7"},
					"research": map[string]string{"perplexity": "sonar"},
				},
				"fields": map[string]any{
					"primary":  map[string]any{"anthropic": []map[string]any{{"key": "apiKey", "label": "API key", "kind": "secret", "required": true}}},
					"research": map[string]any{"perplexity": []map[string]any{{"key": "apiKey", "label": "API key", "kind": "secret"}}},
				},
			})
		default:
			_ = enc.Encode(map[string]any{"ok": true})
		}
	}))
}

func findWhere(recs []recorded, method, path string, pred func(map[string]any) bool) (recorded, bool) {
	for _, rec := range recs {
		if rec.method == method && rec.path == path && (pred == nil || pred(rec.body)) {
			return rec, true
		}
	}
	return recorded{}, false
}

func TestHeadlessConfigure(t *testing.T) {
	var recs []recorded
	srv := mockSetupServer(false, &recs)
	defer srv.Close()

	out := &bytes.Buffer{}
	cfg := Config{
		Mode: "demo", BaseURL: srv.URL, Headless: true,
		AdminPassword: "supersecret", Provider: "anthropic", ProviderKey: "sk-test", NoResearch: true,
	}
	outcome, err := Run(context.Background(), NewClient(srv.URL), out, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !outcome.Configured || outcome.Skipped {
		t.Fatalf("want Configured, got %+v", outcome)
	}

	if _, ok := findWhere(recs, "POST", "/api/admin/change-password", func(b map[string]any) bool {
		return b["password"] == "supersecret"
	}); !ok {
		t.Error("missing password change with the supplied password")
	}
	// Data source saved with demo's internal GraphJin root, canonical suffix.
	if _, ok := findWhere(recs, "PUT", "/api/settings/data-source", func(b map[string]any) bool {
		return b["graphqlUrl"] == "http://graphjin:8080/api/v1/graphql"
	}); !ok {
		t.Error("data source not saved with derived demo endpoint")
	}
	// Provider key tested before save, keyed by the provider's secret field.
	if _, ok := findWhere(recs, "POST", "/api/settings/provider/test", func(b map[string]any) bool {
		s, _ := b["secrets"].(map[string]any)
		return s != nil && s["apiKey"] == "sk-test"
	}); !ok {
		t.Error("provider key not tested with apiKey secret")
	}
	if _, ok := findWhere(recs, "PUT", "/api/settings/agent", func(b map[string]any) bool {
		return b["backend"] == "hermes"
	}); !ok {
		t.Error("agent backend not saved")
	}
	if _, ok := findWhere(recs, "PUT", "/api/settings/provider", func(b map[string]any) bool {
		return b["scope"] == "research" && b["provider"] == "disabled"
	}); !ok {
		t.Error("research not explicitly disabled")
	}
	if _, ok := findWhere(recs, "POST", "/settings/finish", nil); !ok {
		t.Error("setup not finished")
	}
}

func TestHeadlessClaudeAgentLocksAnthropic(t *testing.T) {
	var recs []recorded
	srv := mockSetupServer(true, &recs) // password already changed
	defer srv.Close()

	cfg := Config{
		Mode: "prod", BaseURL: srv.URL, Headless: true,
		Backend: "claude-agent", Provider: "openai", ProviderKey: "sk", DataURL: "http://x:8080", NoResearch: true,
	}
	if _, err := Run(context.Background(), NewClient(srv.URL), &bytes.Buffer{}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Even though Provider=openai was passed, claude-agent forces anthropic.
	if _, ok := findWhere(recs, "PUT", "/api/settings/provider", func(b map[string]any) bool {
		return b["scope"] == "primary" && b["provider"] == "anthropic"
	}); !ok {
		t.Error("claude-agent should lock the primary provider to anthropic")
	}
}

func TestHeadlessMissingFlags(t *testing.T) {
	var recs []recorded
	srv := mockSetupServer(true, &recs) // password already set, so not the blocker
	defer srv.Close()

	cfg := Config{Mode: "demo", BaseURL: srv.URL, Headless: true, Provider: "anthropic"} // no provider-key
	_, err := Run(context.Background(), NewClient(srv.URL), &bytes.Buffer{}, cfg)
	if err == nil {
		t.Fatal("expected error for missing --provider-key")
	}
}

func TestHeadlessMissingPassword(t *testing.T) {
	var recs []recorded
	srv := mockSetupServer(false, &recs) // password NOT set and none provided
	defer srv.Close()

	cfg := Config{Mode: "demo", BaseURL: srv.URL, Headless: true, Provider: "anthropic", ProviderKey: "k"}
	_, err := Run(context.Background(), NewClient(srv.URL), &bytes.Buffer{}, cfg)
	if err == nil {
		t.Fatal("expected error for missing --admin-password on a bootstrap-default DB")
	}
}

func TestPureHelpers(t *testing.T) {
	if err := validatePassword("short"); err == nil {
		t.Error("short password should fail")
	}
	if err := validatePassword("secret"); err == nil {
		t.Error("forbidden password should fail")
	}
	if err := validatePassword("longenough"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}

	if k := secretFieldKey([]Field{{Key: "X", Kind: "text"}, {Key: "tok", Kind: "secret"}}); k != "tok" {
		t.Errorf("secretFieldKey = %q, want tok", k)
	}
	if k := secretFieldKey(nil); k != "apiKey" {
		t.Errorf("secretFieldKey default = %q, want apiKey", k)
	}

	demo := defaultDataURL(Config{Mode: "demo"}, &DataSource{Source: "unset"})
	if demo != "http://graphjin:8080" {
		t.Errorf("demo default = %q", demo)
	}
	saved := defaultDataURL(Config{Mode: "demo"}, &DataSource{Source: "org", GraphqlURL: "http://h:9/api/v1/graphql"})
	if saved != "http://h:9" {
		t.Errorf("saved-source default = %q, want root", saved)
	}

	if m := modelDefault(map[string]string{"anthropic": "claude-x"}, "anthropic", ProviderConfig{}); m != "claude-x" {
		t.Errorf("modelDefault = %q", m)
	}
	if r := disabledResearch(); r.Provider != "disabled" || r.Enabled {
		t.Errorf("disabledResearch = %+v", r)
	}
	if got := filterOut([]ProviderOption{{Value: "disabled"}, {Value: "perplexity"}}, "disabled"); len(got) != 1 || got[0].Value != "perplexity" {
		t.Errorf("filterOut = %+v", got)
	}
}
