package marketplace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleMarketplace = `{
  "name": "test",
  "owner": "tester",
  "description": "test marketplace",
  "plugins": [
    {
      "name": "p1",
      "title": "P1",
      "description": "the first",
      "source": "github:test/p1",
      "versions": [
        {"version": "1.0.0", "integrity": "sha512-a", "permissions": {"network": [], "env": []}, "capabilities": {}, "publishedAt": "2024-01-01"},
        {"version": "1.1.0", "integrity": "sha512-b", "permissions": {"network": ["api.example"], "env": []}, "capabilities": {}, "publishedAt": "2024-02-01"},
        {"version": "1.2.0-rc1", "integrity": "sha512-c", "permissions": {"network": [], "env": []}, "capabilities": {}, "publishedAt": "2024-03-01"},
        {"version": "0.9.0", "integrity": "sha512-d", "permissions": {"network": [], "env": []}, "capabilities": {}, "publishedAt": "2023-12-01", "yanked": true}
      ]
    }
  ]
}`

func TestFetchSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleMarketplace))
	}))
	t.Cleanup(srv.Close)

	c := NewClient()
	m, err := c.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "test" || len(m.Plugins) != 1 {
		t.Fatalf("unexpected: %+v", m)
	}
}

func TestFetchBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	if _, err := c.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchBadShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"plugins":"not-an-array"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	if _, err := c.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("expected shape error")
	}
}

func TestFindPlugin(t *testing.T) {
	m := &Marketplace{Plugins: []Plugin{{Name: "a"}, {Name: "b"}}}
	if FindPlugin(m, "b") == nil {
		t.Fatal("expected to find b")
	}
	if FindPlugin(m, "z") != nil {
		t.Fatal("should not find z")
	}
}

func TestPickInstallLatestNonYanked(t *testing.T) {
	p := &Plugin{
		Name: "p",
		Versions: []Version{
			{Version: "1.0.0"},
			{Version: "1.1.0"},
			{Version: "1.2.0-rc1"},
			{Version: "0.9.0", Yanked: true},
		},
	}
	v, err := PickInstallVersion(p, "")
	if err != nil {
		t.Fatal(err)
	}
	// Mirrors the TS pickInstallVersion: highest non-yanked, prereleases
	// included (1.2.0-rc1 > 1.1.0). Operators wanting only stable should
	// pin via --version.
	if v.Version != "1.2.0-rc1" {
		t.Fatalf("expected 1.2.0-rc1, got %s", v.Version)
	}
}

func TestPickInstallSkipsPrereleaseWhenStableHigher(t *testing.T) {
	p := &Plugin{
		Name: "p",
		Versions: []Version{
			{Version: "2.0.0"},
			{Version: "2.0.0-rc1"},
			{Version: "1.9.0"},
		},
	}
	v, err := PickInstallVersion(p, "")
	if err != nil {
		t.Fatal(err)
	}
	// Same major.minor.patch: stable beats prerelease.
	if v.Version != "2.0.0" {
		t.Fatalf("expected 2.0.0, got %s", v.Version)
	}
}

func TestPickInstallSpecific(t *testing.T) {
	p := &Plugin{
		Name: "p",
		Versions: []Version{
			{Version: "1.0.0"},
			{Version: "1.1.0"},
		},
	}
	v, err := PickInstallVersion(p, "1.0.0")
	if err != nil || v.Version != "1.0.0" {
		t.Fatalf("expected 1.0.0, got %v %v", v, err)
	}
}

func TestPickInstallAllYanked(t *testing.T) {
	p := &Plugin{Name: "p", Versions: []Version{{Version: "1.0.0", Yanked: true}}}
	if _, err := PickInstallVersion(p, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestPickInstallMissingRequested(t *testing.T) {
	p := &Plugin{Name: "p", Versions: []Version{{Version: "1.0.0"}}}
	if _, err := PickInstallVersion(p, "9.9.9"); err == nil {
		t.Fatal("expected error")
	}
}
