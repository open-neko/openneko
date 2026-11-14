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
		"index.html":      {Data: []byte("<html>app</html>")},
		"assets/main.js":  {Data: []byte("js")},
		"icon.png":        {Data: []byte("png")},
		"connectors/x.md": {Data: []byte("md")},
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
		{"GET", "/assets/main.js", "js", "public, max-age=31536000, immutable"},
		{"GET", "/icon.png", "png", ""},
		{"GET", "/connectors/x.md", "md", ""},
		{"GET", "/assets/gone.js", "node /assets/gone.js", ""},
		{"GET", "/_next/static/x", "node /_next/static/x", ""},
		{"GET", "/api/v1/work", "node /api/v1/work", ""},
		{"GET", "/api/work", "node /api/work", ""},
		{"GET", "/admin/settings/models", "<html>app</html>", "no-cache"},
		{"GET", "/admin/settings/workflows", "node /admin/settings/workflows", ""},
		{"GET", "/admin/users/groups/g1", "node /admin/users/groups/g1", ""},
		{"GET", "/device", "node /device", ""},
		{"POST", "/chat", "node /chat", ""},
	}
	for _, c := range cases {
		w := get(f, c.method, c.path)
		if w.Code != 200 || w.Body.String() != c.body || w.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s %s: %d %q cache %q", c.method, c.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
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
	if w := get(f, "GET", "/admin"); w.Code != 502 {
		t.Errorf("server page: %d", w.Code)
	}
	if w := get(f, "GET", "/chat"); w.Code != 200 {
		t.Errorf("static page: %d", w.Code)
	}
}

func TestCheck(t *testing.T) {
	if err := Check(t.TempDir()); err == nil || !strings.Contains(err.Error(), "build:spa") {
		t.Errorf("empty folder: %v", err)
	}
}
