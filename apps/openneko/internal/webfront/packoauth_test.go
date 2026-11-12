package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func packStateFrom(t *testing.T, c *http.Cookie) map[string]any {
	t.Helper()
	body, mac, _ := strings.Cut(c.Value, ".")
	if mac != macOf(testSecret, body) {
		t.Fatal("cookie MAC")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(body)
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func packCookie(t *testing.T, a *API, st map[string]any) string {
	t.Helper()
	v, err := a.packStateCookie(st)
	if err != nil {
		t.Fatal(err)
	}
	return PackOAuthCookie + "=" + v
}

func plusCookie(session map[string]string, cookie string) map[string]string {
	return map[string]string{"Cookie": session["Cookie"] + "; " + cookie}
}

func TestPackOAuthAdminRoutes(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, wk := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	wk.reply["/admin/packs/drive/oauth/account/status"] = `{"configured":true,"connected":false}`
	wk.reply["/admin/packs/drive/oauth/account/disconnect"] = `{"disconnected":true}`
	wk.reply["/admin/packs/drive/oauth/account/configure"] = `{"saved":true}`
	const route = "/api/v1/pack-accounts/drive/account"

	for _, c := range []struct {
		method, path, body string
		header             map[string]string
		code, want         string
	}{
		{"GET", route, "", member, "403", `{"error":"admin only"}`},
		{"POST", route + "/start", `{"clientId":"x"}`, member, "403", `{"error":"admin only"}`},
		{"GET", "/api/v1/pack-accounts/Drive/account", "", admin, "400", `{"error":"invalid pack OAuth connection"}`},
		{"POST", "/api/v1/pack-accounts/drive/a_b", `{"clientId":"x"}`, admin, "400", `{"error":"Invalid pack connection"}`},
		{"GET", route, "", admin, "200", `{"configured":true,"connected":false,"callbackUrl":"https://neko.example/api/pack-accounts/drive/account/callback"}`},
		{"GET", "/api/pack-accounts/drive/other", "", admin, "404", `{"error":"Worker returned HTTP 404","callbackUrl":"https://neko.example/api/pack-accounts/drive/other/callback"}`},
		{"DELETE", route, "", admin, "200", `{"disconnected":true}`},
		{"POST", route, `not json`, admin, "400", `{"error":"Client ID is required"}`},
		{"POST", route, `{"clientId":7}`, admin, "400", `{"error":"Client ID is required"}`},
		{"POST", route, `{"clientSecret":"s","clientId":"cid","extra":1}`, admin, "200", `{"saved":true}`},
		{"POST", route + "/start", `{"clientId":""}`, admin, "400", `{"error":"clientId is required"}`},
	} {
		w := call(f, c.method, c.path, c.body, c.header)
		if w.Result().Status[:3] != c.code || w.Body.String() != c.want {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	want := []string{
		"GET /admin/packs/drive/oauth/account/status",
		"GET /admin/packs/drive/oauth/other/status",
		"POST /admin/packs/drive/oauth/account/disconnect {}",
		`POST /admin/packs/drive/oauth/account/configure {"clientId":"cid","clientSecret":"s"}`,
	}
	if strings.Join(wk.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("worker calls:\n%s", strings.Join(wk.calls, "\n"))
	}

	wk.fail = "down"
	w := call(f, "POST", route+"/start", `{"clientId":"cid","clientSecret":"s"}`, admin)
	if w.Code != 502 || cookieFrom(w, PackOAuthCookie) != nil {
		t.Errorf("failed begin: %d %s, cookie %v", w.Code, w.Body.String(), cookieFrom(w, PackOAuthCookie))
	}
	wk.fail = ""
	wk.calls = nil
	wk.reply["/admin/packs/drive/oauth/account/begin"] = `{"authorizationUrl":"https://accounts.example.com/auth"}`
	w = call(f, "POST", route+"/start", `{"clientId":"cid","clientSecret":"s","returnTo":"//evil.test"}`, admin)
	if w.Code != 200 || w.Body.String() != `{"authorizationUrl":"https://accounts.example.com/auth"}` {
		t.Fatalf("begin: %d %s", w.Code, w.Body.String())
	}
	c := cookieFrom(w, PackOAuthCookie)
	if c == nil || !c.HttpOnly || c.MaxAge != 600 {
		t.Fatalf("cookie %+v", c)
	}
	st := packStateFrom(t, c)
	if st["returnPath"] != "/admin/settings/packs" || st["userId"] != "admin" || st["orgId"] != "org" || st["personal"] != nil {
		t.Errorf("state %v", st)
	}
	var begun map[string]string
	_ = json.Unmarshal([]byte(strings.TrimPrefix(wk.calls[0], "POST /admin/packs/drive/oauth/account/begin ")), &begun)
	if begun["state"] != st["state"] || begun["clientSecret"] != "s" ||
		begun["redirectUri"] != "https://neko.example/api/pack-accounts/drive/account/callback" ||
		begun["codeChallenge"] != pkceS256(st["codeVerifier"].(string)) {
		t.Errorf("begin body %v", begun)
	}

	state := map[string]any{"packId": "drive", "connectionKey": "account", "state": "st", "codeVerifier": "cv",
		"returnPath": "/admin/settings/packs?tab=oauth", "userId": "admin", "orgId": "org",
		"expiresAt": time.Now().Add(time.Minute).UnixMilli()}
	cb := route + "/callback?code=c1&state=st"
	cookie := packCookie(t, f.API, state)
	if w := call(f, "GET", cb, "", admin); w.Code != 400 || w.Body.String() != `{"error":"OAuth callback state is missing or invalid"}` {
		t.Errorf("no cookie: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", route+"/callback?code=c1&state=other", "", plusCookie(admin, cookie)); w.Code != 400 {
		t.Errorf("state mismatch: %d", w.Code)
	}
	if w := call(f, "GET", cb, "", plusCookie(member, cookie)); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
		t.Errorf("member: %d %s", w.Code, w.Body.String())
	}
	state["userId"] = "someone"
	if w := call(f, "GET", cb, "", plusCookie(admin, packCookie(t, f.API, state))); w.Code != 403 || w.Body.String() != `{"error":"OAuth session changed"}` {
		t.Errorf("changed session: %d %s", w.Code, w.Body.String())
	}
	state["userId"] = "admin"
	state["expiresAt"] = time.Now().Add(-time.Second).UnixMilli()
	if w := call(f, "GET", cb, "", plusCookie(admin, packCookie(t, f.API, state))); w.Code != 400 {
		t.Errorf("expired: %d", w.Code)
	}
	state["expiresAt"] = time.Now().Add(time.Minute).UnixMilli()
	wk.calls = nil
	if w := call(f, "GET", cb, "", plusCookie(admin, cookie)); w.Code != 404 || w.Body.String() != `{"error":"Worker returned HTTP 404"}` {
		t.Errorf("worker refusal: %d %s", w.Code, w.Body.String())
	}
	wk.reply["/admin/packs/drive/oauth/account/complete"] = `{"connected":true}`
	w = call(f, "GET", cb, "", plusCookie(admin, cookie))
	if w.Code != 302 || w.Header().Get("Location") != "/admin/settings/packs?tab=oauth&pack=drive&connected=account" {
		t.Errorf("complete: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c := cookieFrom(w, PackOAuthCookie); c == nil || c.MaxAge != -1 {
		t.Errorf("cookie not cleared: %+v", c)
	}
	if len(wk.calls) != 2 || wk.calls[1] != `POST /admin/packs/drive/oauth/account/complete {"code":"c1","codeVerifier":"cv","redirectUri":"https://neko.example/api/pack-accounts/drive/account/callback"}` {
		t.Errorf("complete calls %v", wk.calls)
	}
}

type fakeProvider struct {
	mu      sync.Mutex
	token   string
	account string
	forms   []url.Values
	bearer  string
}

func (p *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.URL.Path {
	case "/token":
		_ = r.ParseForm()
		p.forms = append(p.forms, r.PostForm)
		_, _ = w.Write([]byte(p.token))
	case "/userinfo":
		p.bearer = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(p.account))
	default:
		http.NotFound(w, r)
	}
}

func TestPackOAuthPersonalCallback(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	provider := &fakeProvider{
		token:   `{"access_token":"at-1","refresh_token":"rt-1","expires_in":"3600"}`,
		account: `{"sub":"u-77","email":"m@example.com"}`,
	}
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	cfg := strings.Replace(driveConfig, `"scopes"`, `"tokenUrl":"`+srv.URL+`/token","userInfoUrl":"`+srv.URL+
		`/userinfo","accountIdField":"sub","accountLabelField":"email","scopes"`, 1)
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values ('org', 'drive', '1', 'installed', 'h', $1)`, cfg)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secrets.json"),
		[]byte(`{"pack.drive":{"OAUTH_ACCOUNT_CLIENT_ID":"cid.apps","DRIVE_CLIENT_SECRET":"shh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	admin, member := sessionCookie("admin"), sessionCookie("member")
	begin := func() (string, string) {
		t.Helper()
		w := call(f, "POST", "/api/v1/my/pack-accounts/drive/account", "", map[string]string{"Cookie": member["Cookie"], "Origin": "https://neko.example"})
		if w.Code != 200 {
			t.Fatalf("begin: %d %s", w.Code, w.Body.String())
		}
		c := cookieFrom(w, PackOAuthCookie)
		return PackOAuthCookie + "=" + c.Value, packStateFrom(t, c)["state"].(string)
	}
	finish := func(session map[string]string, cookie, state string) string {
		t.Helper()
		w := call(f, "GET", "/api/pack-accounts/drive/account/callback?code=code-1&state="+state, "", plusCookie(session, cookie))
		if w.Code != 302 {
			t.Fatalf("callback: %d %s", w.Code, w.Body.String())
		}
		return w.Header().Get("Location")
	}

	cookie, state := begin()
	if got := finish(admin, cookie, state); got != "/integrations?error=Sign+in+as+the+user+who+started+this+connection" {
		t.Errorf("other user: %s", got)
	}
	if len(provider.forms) != 0 {
		t.Fatal("the provider was called for another user")
	}
	if got := finish(member, cookie, state); got != "/integrations?connected=account" {
		t.Fatalf("connect: %s", got)
	}
	form := provider.forms[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != "cid.apps" || form.Get("client_secret") != "shh" ||
		form.Get("code") != "code-1" || form.Get("code_verifier") == "" ||
		form.Get("redirect_uri") != "https://neko.example/api/pack-accounts/drive/account/callback" || provider.bearer != "Bearer at-1" {
		t.Errorf("provider saw %v %q", form, provider.bearer)
	}
	var sealed, accountID, label string
	var pending bool
	if err := db.QueryRow(ctx, `select credentials, account_id, account_label, pending_state_hash is not null from pack_user_connection where user_id = 'member'`).
		Scan(&sealed, &accountID, &label, &pending); err != nil {
		t.Fatal(err)
	}
	plain, err := config.MaybeDecryptValue(f.API.SecretsDir, sealed)
	if err != nil || !config.IsEncrypted(sealed) || accountID != "u-77" || label != "m@example.com" || pending {
		t.Fatalf("row: %v %s %s %v", err, accountID, label, pending)
	}
	var tokens map[string]any
	_ = json.Unmarshal([]byte(plain), &tokens)
	expires, _ := time.Parse(time.RFC3339Nano, tokens["expiresAt"].(string))
	if tokens["accessToken"] != "at-1" || tokens["refreshToken"] != "rt-1" || time.Until(expires) < 59*time.Minute ||
		!strings.HasSuffix(tokens["expiresAt"].(string), "Z") || len(tokens["scopes"].([]any)) != 2 {
		t.Errorf("tokens %s", plain)
	}
	if got := finish(member, cookie, state); got != "/integrations?error=OAuth+connection+expired%3B+connect+again" {
		t.Errorf("replay: %s", got)
	}

	provider.token = `{"access_token":"at-2","expires_in":3600}`
	cookie, state = begin()
	if got := finish(member, cookie, state); got != "/integrations?error=Grant+all+requested+permissions+and+offline+access%2C+then+connect+again" {
		t.Errorf("no refresh token: %s", got)
	}
	provider.token = `{"access_token":"at-3","refresh_token":"rt","expires_in":3600}`
	provider.account = `{"sub":"u-77"}`
	cookie, state = begin()
	if got := finish(member, cookie, state); got != "/integrations?error=OAuth+account+response+did+not+contain+the+declared+identity+fields" {
		t.Errorf("no label: %s", got)
	}
	srv.Close()
	cookie, state = begin()
	if got := finish(member, cookie, state); got != "/integrations?error=OAuth+code+exchange+failed%3A+the+provider+did+not+answer" {
		t.Errorf("provider down: %s", got)
	}
	if err := db.QueryRow(ctx, `select account_label from pack_user_connection where user_id = 'member'`).Scan(&label); err != nil || label != "m@example.com" {
		t.Errorf("a failed reconnect changed the saved account: %s %v", label, err)
	}
}

func TestReadPackStateFromNode(t *testing.T) {
	a := &API{Gate: newGate(t, "")}
	r := httptest.NewRequest("GET", "/api/pack-accounts/drive/account/callback", nil)
	r.Header.Set("Cookie", PackOAuthCookie+"="+goPackState)
	got := a.readAndClearPackState(httptest.NewRecorder(), r)
	if got == nil || got["personal"] != true || got["userId"] != "u1" || got["returnPath"] != "/integrations" {
		t.Errorf("got %v", got)
	}
	r.Header.Set("Cookie", PackOAuthCookie+"="+goPackState[:len(goPackState)-1]+"x")
	if a.readAndClearPackState(httptest.NewRecorder(), r) != nil {
		t.Error("a bad MAC was accepted")
	}
}

func TestWithSearch(t *testing.T) {
	add := searchParams{{"pack", "drive"}, {"connected", "a b"}}
	for in, want := range map[string]string{
		"/admin/settings/packs": "/admin/settings/packs?pack=drive&connected=a+b",
		"/x?pack=old&y=1#top":   "/x?pack=drive&y=1&connected=a+b#top",
		`/\evil.test`:           "/admin/settings/packs?pack=drive&connected=a+b",
	} {
		if got := withSearch(in, add); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
