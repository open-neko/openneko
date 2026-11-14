package webfront

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func newFront(t *testing.T, node http.Handler) *Front {
	app := fstest.MapFS{
		"index.html":           {Data: []byte("<html>app</html>")},
		"manifest.webmanifest": {Data: []byte(`{"name":"OpenNeko"}`)},
		"sw.js":                {Data: []byte("self.addEventListener('fetch',()=>{})")},
		"version.json":         {Data: []byte(`{"version":"5.0.0"}`)},
		"assets/main.js":       {Data: []byte("js")},
		"icon.png":             {Data: []byte("png")},
		"connectors/x.md":      {Data: []byte("md")},
	}
	return &Front{App: app, Gate: newGate(t, newWorker(t, `{"provider":null}`).srv.URL), Node: node}
}

func get(f *Front, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestFront(t *testing.T) {
	node := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("node " + r.URL.Path)) })
	f := newFront(t, node)
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
		{"GET", "/assets/gone.js", "node /assets/gone.js", ""},
		{"GET", "/_next/static/x", "node /_next/static/x", ""},
		{"GET", "/api/v1/work", "node /api/v1/work", ""},
		{"GET", "/api/work", "node /api/work", ""},
		{"GET", "/admin/settings/models", "<html>app</html>", "no-cache"},
		{"GET", "/admin/settings/workflows", "<html>app</html>", "no-cache"},
		{"GET", "/admin/users", "<html>app</html>", "no-cache"},
		{"GET", "/admin/users/groups/g1", "<html>app</html>", "no-cache"},
		{"GET", "/device", "<html>app</html>", "no-cache"},
		{"GET", "/integrations", "<html>app</html>", "no-cache"},
		{"GET", "/chat/connectors", "<html>app</html>", "no-cache"},
		{"GET", "/onboarding", "<html>app</html>", "no-cache"},
		{"GET", "/admin/settings/packs/magento/operations", "<html>app</html>", "no-cache"},
		{"POST", "/chat", "node /chat", ""},
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
	if csp := get(f, "GET", "/chat").Header().Get("Content-Security-Policy"); csp != "frame-src 'self'" {
		t.Errorf("CSP %q", csp)
	}
	if w := get(f, "GET", "/"); w.Code != 302 || w.Header().Get("Location") != "/chat" {
		t.Errorf("/: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, p := range []string{"/nope", "/assets/../../etc/passwd"} {
		if w := get(f, "GET", p); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestFrontGatesPagesBeforeNode(t *testing.T) {
	reached := false
	f := newFront(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	f.Gate = newGate(t, newWorker(t, `{"provider":{"pluginName":"oidc"}}`).srv.URL)
	for _, p := range []string{"/admin/settings", "/api/v1/work", "/chat"} {
		if w := get(f, "GET", p); w.Code != 302 || !strings.HasPrefix(w.Header().Get("Location"), "/signin?returnTo=") {
			t.Errorf("%s: %d %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	if reached {
		t.Error("a request with no session reached Node")
	}
	if w := get(f, "GET", "/signin"); w.Code != 200 || w.Body.String() != "<html>app</html>" {
		t.Errorf("/signin: %d %q", w.Code, w.Body.String())
	}
}

func TestFrontWithoutNode(t *testing.T) {
	f := newFront(t, nil)
	if w := get(f, "GET", "/api/v1/work"); w.Code != 502 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("API: %d %q", w.Code, w.Body.String())
	}
	for _, p := range []string{"/chat", "/device", "/integrations", "/chat/connectors", "/onboarding", "/admin/settings/packs/magento/operations", "/admin/settings/graphjin"} {
		if w := get(f, "GET", p); w.Code != 200 || w.Body.String() != "<html>app</html>" {
			t.Errorf("static page %s: %d %q", p, w.Code, w.Body.String())
		}
	}
}

func TestCheck(t *testing.T) {
	if err := Check(t.TempDir()); err == nil || !strings.Contains(err.Error(), "build:spa") {
		t.Errorf("empty folder: %v", err)
	}
}
