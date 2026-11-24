package webfront

import (
	"net/http/httptest"
	"testing"
)

func TestAppURLKeepsBrowserLinksOffThePublicURL(t *testing.T) {
	t.Setenv("OPENNEKO_APP_URL", "")
	a := &API{PublicURL: "https://proxy.example", AppURL: "http://tail.example:4000/"}
	r := httptest.NewRequest("GET", "http://tail.example:4000/signin", nil)

	if got := a.redirectURI(r); got != "http://tail.example:4000/api/auth/callback" {
		t.Errorf("redirectURI = %q", got)
	}
	if a.secureCookie(r) {
		t.Error("secureCookie = true for an http app URL; the browser would drop the cookie")
	}
	if got := a.passkeyRP(r).id; got != "tail.example" {
		t.Errorf("passkey RP id = %q", got)
	}
	if got := a.publicBaseURL(r); got != "https://proxy.example" {
		t.Errorf("publicBaseURL = %q; API callers and webhooks keep the public URL", got)
	}
}

func TestAppURLFallsBackToThePublicURL(t *testing.T) {
	t.Setenv("OPENNEKO_APP_URL", "")
	a := &API{PublicURL: "https://proxy.example"}
	r := httptest.NewRequest("GET", "https://proxy.example/signin", nil)

	if got := a.redirectURI(r); got != "https://proxy.example/api/auth/callback" {
		t.Errorf("redirectURI = %q", got)
	}
	if !a.secureCookie(r) {
		t.Error("secureCookie = false for an https public URL")
	}
}

func TestAppURLFromTheEnvironment(t *testing.T) {
	t.Setenv("OPENNEKO_APP_URL", "http://tail.example:4000")
	a := &API{PublicURL: "https://proxy.example"}
	r := httptest.NewRequest("GET", "http://tail.example:4000/", nil)
	if got := a.appBaseURL(r); got != "http://tail.example:4000" {
		t.Errorf("appBaseURL = %q", got)
	}
}
