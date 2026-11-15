package webfront

import (
	"fmt"
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

// CookiePolicy takes Secure from the scheme of OPENNEKO_PUBLIC_URL, because a
// browser drops a Secure cookie on plain HTTP. With no public URL, production
// mode gives Secure.
func CookiePolicy() (secure bool, warning string) {
	raw := strings.TrimSpace(os.Getenv("OPENNEKO_PUBLIC_URL"))
	if raw == "" {
		return !Development(), ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return !Development(), ""
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true, ""
	case "http":
		if isLoopbackHostname(u.Hostname()) {
			return false, ""
		}
		return false, fmt.Sprintf("openneko web: warning: OPENNEKO_PUBLIC_URL is http (%s), so the session cookie is not Secure and can be read on the network; use https", u.Host)
	}
	return !Development(), ""
}
