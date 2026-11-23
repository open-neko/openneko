package webfront

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type cookieCase struct {
	name, envURL, storedURL, proto string
	secure                         bool
}

var cookieCases = []cookieCase{
	{"default install behind a TLS proxy", "http://localhost:3000", "", "https", true},
	{"stored https URL over env http", "http://localhost:3000", "https://openneko.example.com", "", true},
	{"plain http LAN request with http URL", "http://192.168.1.20:3000", "", "", false},
	{"plain http loopback request with http URL", "http://localhost:3000", "", "", false},
	{"malformed URL over plain http", "http://bad host:3000", "", "", false},
	{"malformed URL behind a TLS proxy", "http://bad host:3000", "", "https", true},
	{"https URL with a path", "https://openneko.example.com/neko/", "", "", true},
	{"stored http URL over env https", "https://openneko.example.com", "http://192.168.1.20:3000", "", false},
}

func applyCookieCase(t *testing.T, f *Front, db *pgxpool.Pool, c cookieCase) map[string]string {
	t.Helper()
	t.Setenv("OPENNEKO_ENV", "production")
	t.Setenv("OPENNEKO_PUBLIC_URL", c.envURL)
	f.API.PublicURL, f.Gate.PublicURL = "", ""
	seed(t, db, `delete from llm_provider_config where scope = 'public-url'`)
	if c.storedURL != "" {
		seed(t, db, `insert into llm_provider_config (org_id, scope, provider, config)
			values ('org', 'public-url', 'public-url', jsonb_build_object('url', $1::text))`, c.storedURL)
	}
	header := map[string]string{}
	if c.proto != "" {
		header["X-Forwarded-Proto"] = c.proto
	}
	return header
}

func checkSecure(t *testing.T, c cookieCase, w *httptest.ResponseRecorder, name string) {
	t.Helper()
	got := cookieFrom(w, name)
	if got == nil {
		t.Fatalf("%s: no %s cookie: %d %v %s", c.name, name, w.Code, w.Header(), w.Body.String())
	}
	if got.Secure != c.secure {
		t.Errorf("%s: %s Secure %v, want %v", c.name, name, got.Secure, c.secure)
	}
}

func TestSignInCookiesFollowTheRequestAndPublicURL(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, wk := newSSOFront(t, `{"provider":{"pluginName":"scalekit","provisioning":"automatic"}}`, db)
	for _, c := range cookieCases {
		header := applyCookieCase(t, f, db, c)
		begin := call(f, "GET", "/api/v1/auth/begin?returnTo=%2Fwork", "", header)
		checkSecure(t, c, begin, StateCookie)
		loc, err := url.Parse(begin.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		wk.identity = `{"sub":"sub-ada","email":"ada@example.com","name":"Ada","orgId":"tenant-1","groups":[]}`
		header["Cookie"] = cookieFrom(begin, StateCookie).String()
		callback := call(f, "GET", "/api/auth/callback?code=c1&state="+loc.Query().Get("state"), "", header)
		checkSecure(t, c, callback, SessionCookie)
	}
}

func TestConnectStateCookieFollowsTheRequestAndPublicURL(t *testing.T) {
	f, _ := connectFront(t)
	for _, c := range cookieCases {
		header := applyCookieCase(t, f, f.API.DB, c)
		header["Cookie"] = sessionCookie("admin")["Cookie"]
		checkSecure(t, c, call(f, "GET", "/api/v1/integrations/connect/scalekit/start", "", header), ConnectStateCookie)
	}
}

func TestPackOAuthCookieFollowsTheRequestAndPublicURL(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values ('org', 'drive', '1', 'installed', 'h', $1)`, driveConfig)
	if err := writeKeepingSignIn(secrets.Store{"pack.drive": {"OAUTH_ACCOUNT_CLIENT_ID": "cid.apps", "DRIVE_CLIENT_SECRET": "shh"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	for _, c := range cookieCases {
		header := applyCookieCase(t, f, db, c)
		r := httptest.NewRequest("POST", "/api/v1/my/pack-accounts/drive/account", nil)
		for k, v := range header {
			r.Header.Set(k, v)
		}
		header["Cookie"] = sessionCookie("member")["Cookie"]
		callback, err := url.Parse(f.API.packCallbackURI(r, "drive", "account"))
		if err != nil {
			if w := call(f, "POST", "/api/v1/my/pack-accounts/drive/account", "", header); w.Code == http.StatusOK || cookieFrom(w, PackOAuthCookie) != nil {
				t.Errorf("%s: begin with a malformed public URL gave %d and a cookie", c.name, w.Code)
			}
			continue
		}
		header["Origin"] = originOf(callback)
		w := call(f, "POST", "/api/v1/my/pack-accounts/drive/account", "", header)
		if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "must use HTTPS except on localhost") &&
			callback.Scheme == "http" && !isLoopbackHostname(callback.Hostname()) {
			if cookieFrom(w, PackOAuthCookie) != nil {
				t.Errorf("%s: a refused begin set the cookie", c.name)
			}
			continue
		}
		if w.Code != http.StatusOK {
			t.Fatalf("%s: begin %d %s", c.name, w.Code, w.Body.String())
		}
		checkSecure(t, c, w, PackOAuthCookie)
	}
}
