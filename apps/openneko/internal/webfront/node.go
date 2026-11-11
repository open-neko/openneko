package webfront

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// NodeProxy passes a request to the Node server with the client's Host
// header and forwarded headers, so Node builds the same URLs and reads the
// same client address as when it took the request itself.
func NodeProxy(target *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			if v, ok := r.In.Header["X-Forwarded-For"]; ok {
				r.Out.Header["X-Forwarded-For"] = v
			}
			r.SetXForwarded()
			for _, h := range []string{"X-Forwarded-Host", "X-Forwarded-Proto"} {
				if v := r.In.Header.Get(h); v != "" {
					r.Out.Header.Set(h, v)
				}
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusBadGateway, "the Node server did not answer")
				return
			}
			http.Error(w, "the Node server did not answer", http.StatusBadGateway)
		},
	}
}
