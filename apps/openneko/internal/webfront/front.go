// Package webfront serves the static web app, applies the request gate, and
// answers the /api/v1 HTTP API.
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

var fileExtension = regexp.MustCompile(`\.[a-zA-Z0-9]+$`)

type Front struct {
	App  fs.FS
	Gate *Gate
	API  *API
	// Wake answers WakePath; nil leaves it to the gate, which answers 404.
	Wake http.Handler
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

// MapTileHost serves the style, tiles, glyphs and sprites of the answer map.
const MapTileHost = "https://tiles.openfreemap.org"

// script-src keeps 'unsafe-inline': the answer tool frame is a srcdoc
// document, so it inherits this policy and runs inline script.
var ContentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self' 'unsafe-inline'",
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data: blob:",
	"font-src 'self' data:",
	"connect-src 'self' " + MapTileHost,
	"media-src 'self' blob:",
	"worker-src 'self' blob:",
	"frame-src 'self'",
	"frame-ancestors 'self'",
	"object-src 'none'",
	"base-uri 'self'",
	"manifest-src 'self'",
}, "; ")

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	p := r.URL.Path
	if p == WakePath && f.Wake != nil {
		f.Wake.ServeHTTP(w, r)
		return
	}
	if f.ServeHealth(w, r) {
		return
	}
	if !strings.HasPrefix(p, "/api/") && (strings.HasPrefix(p, "/_next/") || fileExtension.MatchString(p)) {
		if !f.serveFile(w, r, p, true) {
			http.NotFound(w, r)
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
	if p == "/api" || strings.HasPrefix(p, "/api/") || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
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
	case strings.HasSuffix(name, ".webmanifest"):
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	case name == "index.html", name == "version.json":
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, f.App, name)
	return true
}
