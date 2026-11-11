// Package webfront serves the static web app and applies the request gate
// from apps/web/src/proxy.ts. Requests for the API and the pages that still
// render on the server go to Node.
package webfront

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

var (
	fileExtension = regexp.MustCompile(`\.[a-zA-Z0-9]+$`)
	serverPage    = func() *regexp.Regexp {
		alternatives := make([]string, len(ServerPages))
		for i, page := range ServerPages {
			alternatives[i] = regexp.MustCompile(`:[^/]+`).ReplaceAllString(regexp.QuoteMeta(page), `[^/]+`)
		}
		return regexp.MustCompile(`^(?:` + strings.Join(alternatives, "|") + `)/?$`)
	}()
)

// ServerPages must equal serverPages in apps/web/src/spa/server-pages.ts;
// TestServerPagesMatchWeb compares them.
var ServerPages = []string{
	"/chat/connectors",
	"/profile",
	"/admin",
	"/admin/access",
	"/admin/rules",
	"/admin/rules/:policyId",
	"/admin/settings",
	"/admin/settings/agent",
	"/admin/settings/channels",
	"/admin/settings/data",
	"/admin/settings/graphjin",
	"/admin/settings/models",
	"/admin/settings/packs",
	"/admin/settings/packs/magento/operations",
	"/admin/settings/repository",
	"/admin/settings/research",
	"/admin/settings/security",
	"/admin/settings/signin",
	"/admin/settings/spend",
	"/admin/settings/sso",
	"/admin/settings/workflows",
	"/admin/users",
	"/admin/users/groups/:groupId",
	"/device",
	"/integrations",
	"/onboarding",
}

type Front struct {
	App  fs.FS
	Gate *Gate
	// API answers the routes that moved to Go; nil passes them to Node.
	API *API
	// Node answers the API and the server pages; nil answers 502.
	Node http.Handler
}

// AppVersion reads the version that build:spa writes beside the app, or
// returns fallback when the build holds none.
func AppVersion(dir, fallback string) string {
	raw, err := os.ReadFile(path.Join(dir, "version.json"))
	if err != nil {
		return fallback
	}
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Version == "" {
		return fallback
	}
	return v.Version
}

// Check confirms that the app folder holds a built app.
func Check(dir string) error {
	if _, err := os.Stat(path.Join(dir, "index.html")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New(dir + " holds no index.html; run `pnpm --filter @neko/web build:spa`")
		}
		return err
	}
	return nil
}

func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/_next/") || fileExtension.MatchString(p) {
		if !f.serveFile(w, r, p, true) {
			f.node(w, r)
		}
		return
	}
	if !f.Gate.Allow(w, r) {
		return
	}
	if p == "/" {
		http.Redirect(w, r, "/chat", http.StatusFound)
		return
	}
	if h := f.API.Handler(r); h != nil {
		h(w, r)
		return
	}
	if p == "/api" || strings.HasPrefix(p, "/api/") || serverPage.MatchString(p) ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead) {
		f.node(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "frame-src 'self'")
	f.serveFile(w, r, "/index.html", false)
}

func (f *Front) serveFile(w http.ResponseWriter, r *http.Request, p string, asset bool) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	name := strings.TrimPrefix(path.Clean(p), "/")
	if !fs.ValidPath(name) {
		return false
	}
	info, err := fs.Stat(f.App, name)
	if err != nil || info.IsDir() {
		if !asset {
			http.Error(w, "the web app is not built", http.StatusInternalServerError)
		}
		return false
	}
	switch {
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, f.App, name)
	return true
}

func (f *Front) node(w http.ResponseWriter, r *http.Request) {
	if f.Node != nil {
		f.Node.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusBadGateway, "the Node server is not connected")
		return
	}
	http.Error(w, "the Node server is not connected", http.StatusBadGateway)
}
