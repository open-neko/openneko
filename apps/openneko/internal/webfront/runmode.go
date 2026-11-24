package webfront

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Development reports whether openneko web runs in development mode. Production
// is the default. NODE_ENV is read only when OPENNEKO_ENV is unset, so a compose
// file from 5.0 still works for one release.
func Development() bool {
	if mode := strings.TrimSpace(os.Getenv("OPENNEKO_ENV")); mode != "" {
		return mode == "development"
	}
	return strings.TrimSpace(os.Getenv("NODE_ENV")) == "development"
}

// secureCookie gives Secure unless a plain-http request meets an app URL that
// is not https, because a browser drops a Secure cookie on plain http.
func (a *API) secureCookie(r *http.Request) bool {
	if strings.HasPrefix(a.appURL(r.Context()), "https://") {
		return true
	}
	if a.Development {
		return false
	}
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-Proto"), ",", 2)[0])
	return strings.EqualFold(proto, "https")
}

// CookieWarning names an app URL that is http on a host other than loopback.
func (a *API) CookieWarning(ctx context.Context) string {
	u, err := url.Parse(a.appURL(ctx))
	if err != nil || u.Scheme != "http" || isLoopbackHostname(u.Hostname()) {
		return ""
	}
	return fmt.Sprintf("openneko web: warning: the app URL is http (%s), so cookies on plain-http requests are not Secure and can be read on the network; use https", u.Host)
}
