package webfront

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func newFront(t *testing.T) *Front {
	app := fstest.MapFS{
		"index.html":           {Data: []byte("<html>app</html>")},
		"manifest.webmanifest": {Data: []byte(`{"name":"OpenNeko"}`)},
		"sw.js":                {Data: []byte("self.addEventListener('fetch',()=>{})")},
		"version.json":         {Data: []byte(`{"version":"5.0.0"}`)},
		"assets/main.js":       {Data: []byte("js")},
		"icon.png":             {Data: []byte("png")},
		"connectors/x.md":      {Data: []byte("md")},
	}
	return &Front{App: app, Gate: newGate(t)}
}

func get(f *Front, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestFront(t *testing.T) {
	f := newFront(t)
	cases := []struct{ method, path, body, cache string }{
		{"GET", "/chat", "<html>app</html>", "no-cache"},
		{"GET", "/workflows/wf1", "<html>app</html>", "no-cache"},
		{"HEAD", "/work", "", "no-cache"},
		{"GET", "/manifest.webmanifest", `{"name":"OpenNeko"}`, "no-cache"},
		{"GET", "/sw.js", "self.addEventListener('fetch',()=>{})", "no-cache"},
		{"GET", "/version.json", `{"version":"5.0.0"}`, "no-cache"},
		{"GET", "/assets/main.js", "js", "public, max-age=31536000, immutable"},
		{"GET", "/icon.png", "png", ""},
		{"GET", "/connectors/x.md", "md", ""},
		{"GET", "/admin/settings/models", "<html>app</html>", "no-cache"},
		{"GET", "/admin/settings/workflows", "<html>app</html>", "no-cache"},
		{"GET", "/admin/users", "<html>app</html>", "no-cache"},
		{"GET", "/admin/users/groups/g1", "<html>app</html>", "no-cache"},
		{"GET", "/device", "<html>app</html>", "no-cache"},
		{"GET", "/integrations", "<html>app</html>", "no-cache"},
		{"GET", "/chat/connectors", "<html>app</html>", "no-cache"},
		{"GET", "/onboarding", "<html>app</html>", "no-cache"},
		{"GET", "/admin/settings/packs/magento/operations", "<html>app</html>", "no-cache"},
	}
	for _, c := range cases {
		w := get(f, c.method, c.path)
		if w.Code != 200 || w.Body.String() != c.body || w.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s %s: %d %q cache %q", c.method, c.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
	}
	if ct := get(f, "GET", "/manifest.webmanifest").Header().Get("Content-Type"); ct != "application/manifest+json; charset=utf-8" {
		t.Errorf("manifest Content-Type %q", ct)
	}
	if ct := get(f, "GET", "/sw.js").Header().Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
		t.Errorf("sw.js Content-Type %q", ct)
	}
	for _, path := range []string{"/chat", "/assets/main.js", "/manifest.webmanifest", "/api/v1/version", "/nope", "/health"} {
		h := get(f, "GET", path).Header()
		csp := map[string]string{}
		for _, d := range strings.Split(h.Get("Content-Security-Policy"), ";") {
			name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
			csp[name] = value
		}
		for name, want := range map[string]string{
			"default-src": "'self'",
			"img-src":     "'self' data: blob:",
			"connect-src": "'self' https://tiles.openfreemap.org",
			"frame-src":   "'self'",
			"font-src":    "'self' data:",
			"style-src":   "'self' 'unsafe-inline'",
			"media-src":   "'self' blob:",
			"object-src":  "'none'",
		} {
			if csp[name] != want {
				t.Errorf("%s: CSP %s = %q, want %q", path, name, csp[name], want)
			}
		}
		if h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: Referrer-Policy %q, X-Content-Type-Options %q", path, h.Get("Referrer-Policy"), h.Get("X-Content-Type-Options"))
		}
	}
	if w := get(f, "GET", "/"); w.Code != 302 || w.Header().Get("Location") != "/chat" {
		t.Errorf("/: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, c := range [][2]string{
		{"GET", "/nope"},
		{"GET", "/assets/../../etc/passwd"},
		{"GET", "/assets/gone.js"},
		{"GET", "/_next/static/x"},
		{"GET", "/api/v1/work"},
		{"GET", "/api/work"},
		{"POST", "/chat"},
	} {
		if w := get(f, c[0], c[1]); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", c[0], c[1], w.Code)
		}
	}
}

func TestFrontGatesPages(t *testing.T) {
	f := newFront(t)
	f.Gate = newGate(t)
	seedSignInSecrets(t, f.Gate.SecretsDir, liveScalekit)
	for _, p := range []string{"/admin/settings", "/api/v1/work", "/chat"} {
		if w := get(f, "GET", p); w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), "/signin?returnTo=") {
			t.Errorf("%s: %d %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	if w := get(f, "GET", "/signin"); w.Code != 200 || w.Body.String() != "<html>app</html>" {
		t.Errorf("/signin: %d %q", w.Code, w.Body.String())
	}
}

func TestCheck(t *testing.T) {
	if err := Check(t.TempDir()); err == nil || !strings.Contains(err.Error(), "build:spa") {
		t.Errorf("empty folder: %v", err)
	}
}
