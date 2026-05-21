package install

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/plugin/manifest"
	"github.com/open-neko/neko/apps/openneko/internal/plugin/marketplace"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantRef  string
	}{
		{"foo", "foo", ""},
		{"@scope/foo", "@scope/foo", ""},
		{"foo@official", "foo", "official"},
		{"@scope/foo@official", "@scope/foo", "official"},
		{"foo@https://example.com/m.json", "foo", "https://example.com/m.json"},
		{"foo@", "foo@", ""},
	}
	for _, c := range cases {
		got, err := ParseSpec(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != c.wantName || got.MarketplaceRef != c.wantRef {
			t.Fatalf("ParseSpec(%q): got %+v want name=%q ref=%q", c.in, got, c.wantName, c.wantRef)
		}
	}
	if _, err := ParseSpec(""); err == nil {
		t.Fatal("expected error on empty spec")
	}
}

func newTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

const sampleMP = `{
  "name": "test",
  "owner": "tester",
  "description": "test marketplace",
  "plugins": [
    {
      "name": "p1",
      "title": "P1",
      "description": "test plugin",
      "source": "github:test/p1",
      "versions": [
        {"version": "1.0.0", "integrity": "sha512-x", "permissions": {"network": ["api.example"], "env": []}, "capabilities": {}, "publishedAt": "2024-01-01"}
      ]
    }
  ]
}`

func TestRunMarketplaceHappy(t *testing.T) {
	srv := newTestServer(t, sampleMP)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	cfgDir := t.TempDir()

	called := false
	var ran []string
	res, err := Run(context.Background(), Options{
		RepoRoot:            dir,
		Spec:                "p1",
		TrustedMarketplaces: []TrustedMarketplace{{Name: "test", URL: srv.URL}},
		SecretsConfigDir:    cfgDir,
		NpmRunner: func(_ context.Context, args []string, cwd string) error {
			called = true
			ran = args
			if cwd != dir {
				t.Fatalf("npm cwd = %q want %q", cwd, dir)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("npm runner not invoked")
	}
	if len(ran) != 2 || ran[0] != "install" || ran[1] != "p1@1.0.0" {
		t.Fatalf("unexpected npm args: %v", ran)
	}
	if res.Name != "p1" || res.Version != "1.0.0" || res.Source != "marketplace" {
		t.Fatalf("unexpected result: %+v", res)
	}

	mfst, err := manifest.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mfst == nil || len(mfst.Plugins) != 1 || mfst.Plugins[0].Name != "p1" {
		t.Fatalf("manifest not written: %+v", mfst)
	}
}

const sampleMPWithEnv = `{
  "name": "test",
  "owner": "tester",
  "description": "test marketplace",
  "plugins": [
    {
      "name": "p1",
      "title": "P1",
      "description": "test plugin",
      "source": "github:test/p1",
      "versions": [
        {"version": "1.0.0", "integrity": "sha512-x", "permissions": {"network": [], "env": [{"key": "API_KEY", "description": "key"}]}, "capabilities": {}, "publishedAt": "2024-01-01"}
      ]
    }
  ]
}`

func TestRunMarketplacePromptsForEnv(t *testing.T) {
	srv := newTestServer(t, sampleMPWithEnv)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	cfgDir := t.TempDir()

	promptCalls := 0
	res, err := Run(context.Background(), Options{
		RepoRoot:            dir,
		Spec:                "p1",
		TrustedMarketplaces: []TrustedMarketplace{{Name: "test", URL: srv.URL}},
		SecretsConfigDir:    cfgDir,
		NpmRunner: func(_ context.Context, _ []string, _ string) error {
			return nil
		},
		EnvPrompt: func(plugin string, req marketplace.EnvRequirement) (string, error) {
			promptCalls++
			if plugin != "p1" || req.Key != "API_KEY" {
				t.Fatalf("unexpected prompt: %s %+v", plugin, req)
			}
			return "the-secret", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if promptCalls != 1 {
		t.Fatalf("expected 1 prompt, got %d", promptCalls)
	}
	if len(res.EnvSaved) != 1 || res.EnvSaved[0] != "API_KEY" {
		t.Fatalf("expected API_KEY saved, got %+v", res)
	}
	store, err := secrets.Read(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	if store["p1"]["API_KEY"] != "the-secret" {
		t.Fatalf("secret not persisted: %+v", store)
	}
}

func TestRunMarketplaceMissingTrusted(t *testing.T) {
	srv := newTestServer(t, sampleMP)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	_, err := Run(context.Background(), Options{
		RepoRoot:            dir,
		Spec:                "p1@otherref",
		TrustedMarketplaces: []TrustedMarketplace{{Name: "test", URL: srv.URL}},
		NpmRunner: func(_ context.Context, _ []string, _ string) error {
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("expected 'not trusted' error, got %v", err)
	}
}

func TestRunMarketplaceNotFound(t *testing.T) {
	srv := newTestServer(t, sampleMP)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	_, err := Run(context.Background(), Options{
		RepoRoot:            dir,
		Spec:                "no-such-plugin",
		TrustedMarketplaces: []TrustedMarketplace{{Name: "test", URL: srv.URL}},
		NpmRunner: func(_ context.Context, _ []string, _ string) error {
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got %v", err)
	}
}

func TestRunMarketplaceDuplicate(t *testing.T) {
	srv1 := newTestServer(t, sampleMP)
	srv2 := newTestServer(t, sampleMP)
	t.Cleanup(srv1.Close)
	t.Cleanup(srv2.Close)
	dir := t.TempDir()
	_, err := Run(context.Background(), Options{
		RepoRoot: dir,
		Spec:     "p1",
		TrustedMarketplaces: []TrustedMarketplace{
			{Name: "mp1", URL: srv1.URL},
			{Name: "mp2", URL: srv2.URL},
		},
		NpmRunner: func(_ context.Context, _ []string, _ string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "multiple trusted") {
		t.Fatalf("expected multiple-trusted error, got %v", err)
	}
}

func TestRunUnverified(t *testing.T) {
	dir := t.TempDir()
	// Pre-populate node_modules/plug/package.json so the install path
	// doesn't actually call npm (npm runner is mocked).
	pkgDir := filepath.Join(dir, "node_modules", "plug")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := map[string]any{
		"version":    "1.2.3",
		"_integrity": "sha512-localpnpmtest",
		"openneko": map[string]any{
			"permissions": map[string]any{"network": []string{"api.x"}, "env": []any{}},
			"capabilities": map[string]any{
				"action": map[string]any{
					"kinds": []map[string]any{{"kind": "test_action", "description": "for tests"}},
				},
			},
		},
	}
	raw, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Options{
		RepoRoot:   dir,
		Spec:       "plug",
		Unverified: true,
		NpmRunner: func(_ context.Context, _ []string, _ string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "unverified" || res.Version != "1.2.3" || res.Integrity != "sha512-localpnpmtest" {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestRunUnverifiedCopiesBundledSkill(t *testing.T) {
	dir := t.TempDir()
	skillsDir := t.TempDir()
	pkgDir := filepath.Join(dir, "node_modules", "plug")
	if err := os.MkdirAll(filepath.Join(pkgDir, "skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := map[string]any{
		"version":    "0.1.0",
		"_integrity": "sha512-localpnpmtest",
		"openneko": map[string]any{
			"runner": "./dist/run.js",
			"skill":  "./skill",
			"permissions": map[string]any{
				"network": []string{"api.x"},
				"env":     []any{},
			},
			"capabilities": map[string]any{
				"action": map[string]any{
					"kinds": []map[string]any{{"kind": "x", "description": "x"}},
				},
			},
		},
	}
	raw, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	skillMd := "---\nname: vendor-x\ndescription: Operate against Vendor X.\n---\nbody"
	if err := os.WriteFile(filepath.Join(pkgDir, "skill", "SKILL.md"), []byte(skillMd), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Options{
		RepoRoot:         dir,
		Spec:             "plug",
		Unverified:       true,
		SkillsInstallDir: skillsDir,
		NpmRunner: func(_ context.Context, _ []string, _ string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDest := filepath.Join(skillsDir, "vendor-x")
	if res.SkillInstalledAt != wantDest {
		t.Fatalf("SkillInstalledAt: got %q want %q", res.SkillInstalledAt, wantDest)
	}
	body, err := os.ReadFile(filepath.Join(wantDest, "SKILL.md"))
	if err != nil {
		t.Fatalf("could not read copied SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), "name: vendor-x") {
		t.Fatalf("SKILL.md content not as expected: %s", body)
	}
}

func TestRunUnverifiedNoSkillFieldLeavesSkillInstalledAtEmpty(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "node_modules", "plug")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := map[string]any{
		"version":    "0.1.0",
		"_integrity": "sha512-localpnpmtest",
		"openneko": map[string]any{
			"capabilities": map[string]any{
				"action": map[string]any{
					"kinds": []map[string]any{{"kind": "x", "description": "x"}},
				},
			},
		},
	}
	raw, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{
		RepoRoot:   dir,
		Spec:       "plug",
		Unverified: true,
		NpmRunner: func(_ context.Context, _ []string, _ string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SkillInstalledAt != "" {
		t.Fatalf("expected empty SkillInstalledAt, got %q", res.SkillInstalledAt)
	}
}

func TestRunUnverifiedFallbackIntegrity(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "node_modules", "plug")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := map[string]any{
		"version": "0.1.0",
		// no _integrity field -> fallback to sha512-unknown
		"openneko": map[string]any{
			"capabilities": map[string]any{"action": map[string]any{"kinds": []map[string]any{{"kind": "k", "description": "d"}}}},
		},
	}
	raw, _ := json.Marshal(pkg)
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Options{
		RepoRoot:   dir,
		Spec:       "plug",
		Unverified: true,
		NpmRunner: func(_ context.Context, _ []string, _ string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Integrity != "sha512-unknown" {
		t.Fatalf("expected sha512-unknown fallback, got %s", res.Integrity)
	}
}
