package webfront

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const workspaceRoute = "/api/v1/pack-accounts/google-workspace/workspace"

// deploymentWorkspacePack copies the Google Workspace pack with a
// deployment-scope connection whose token and account endpoints are the
// fake provider.
func deploymentWorkspacePack(t *testing.T, provider string) {
	t.Helper()
	root := t.TempDir()
	dst := filepath.Join(root, "google-workspace")
	if err := os.CopyFS(dst, os.DirFS(filepath.Join("..", "..", "..", "..", "packs", "google-workspace"))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dst, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(provider, "https://")
	manifest := strings.NewReplacer(
		"    scope: user\n", "    scope: deployment\n",
		"https://oauth2.googleapis.com/token", provider+"/token",
		"https://openidconnect.googleapis.com/v1/userinfo", provider+"/userinfo",
		"  network:\n", "  network:\n    - "+strings.Split(host, ":")[0]+"\n",
	).Replace(string(raw))
	if err := os.WriteFile(filepath.Join(dst, "pack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_PACKS_DIR", root)
}

func oauthStatusOf(t *testing.T, body string) packOAuthState {
	t.Helper()
	var st packOAuthState
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("status %s: %v", body, err)
	}
	return st
}

func TestPackOAuthAdminFlow(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	provider := &fakeProvider{account: `{"sub":"u-1","email":"owner@example.com"}`}
	srv := httptest.NewTLSServer(provider)
	t.Cleanup(srv.Close)
	saved := providerClient
	providerClient = srv.Client()
	t.Cleanup(func() { providerClient = saved })
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	deploymentWorkspacePack(t, srv.URL)
	f, wk := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	var bodies []string
	send := func(method, target, body string, header map[string]string) *httptest.ResponseRecorder {
		w := call(f, method, target, body, header)
		bodies = append(bodies, w.Body.String(), w.Header().Get("Location"))
		return w
	}

	if w := send("GET", workspaceRoute, "", member); w.Code != 403 {
		t.Errorf("member: %d", w.Code)
	}
	w := send("GET", workspaceRoute, "", admin)
	st := oauthStatusOf(t, w.Body.String())
	if w.Code != 200 || st.Scope != "deployment" || st.Configured || st.ClientID != nil || st.Connected || st.Account != nil ||
		st.CallbackURL != "https://neko.example/api/pack-accounts/google-workspace/workspace/callback" || len(st.Scopes) != 10 {
		t.Errorf("status: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"clientId":null,`) || !strings.Contains(w.Body.String(), `"expiresAt":null,`) {
		t.Errorf("status nulls: %s", w.Body.String())
	}
	if w := send("GET", "/api/v1/pack-accounts/google-workspace/other", "", admin); w.Code != 400 ||
		!strings.Contains(w.Body.String(), `"error":"pack google-workspace does not declare OAuth connection other","diagnostic":{"code":"pack_operation_failed","phase":"oauth"`) {
		t.Errorf("unknown connection: %d %s", w.Code, w.Body.String())
	}
	if w := send("GET", "/api/v1/pack-accounts/drive/workspace", "", admin); w.Code != 400 {
		t.Errorf("unknown pack: %d %s", w.Code, w.Body.String())
	}
	if w := send("POST", workspaceRoute, `{"clientId":"  "}`, admin); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "OAuth client ID and client secret are required") {
		t.Errorf("empty configure: %d %s", w.Code, w.Body.String())
	}
	w = send("POST", workspaceRoute, `{"clientId":" cid ","clientSecret":" s3cret-value "}`, admin)
	st = oauthStatusOf(t, w.Body.String())
	if w.Code != 200 || !st.Configured || st.ClientID == nil || *st.ClientID != "cid" || st.CallbackURL != "" {
		t.Errorf("configure: %d %s", w.Code, w.Body.String())
	}
	store, err := secrets.Read(f.API.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	if s := store["pack.google-workspace"]; s["OAUTH_WORKSPACE_CLIENT_ID"] != "cid" || s["GOOGLE_WORKSPACE_OAUTH_CLIENT_SECRET"] != "s3cret-value" {
		t.Errorf("stored client: %v", store)
	}
	raw, _ := os.ReadFile(secrets.Path(f.API.SecretsDir))
	if bytes.Contains(raw, []byte("s3cret-value")) {
		t.Error("client secret stored in plain text")
	}

	w = send("POST", workspaceRoute+"/start", `{"clientId":"cid","returnTo":"//evil.test"}`, admin)
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var begun struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &begun)
	cookie := cookieFrom(w, PackOAuthCookie)
	state := packStateFrom(t, cookie)
	au, _ := url.Parse(begun.AuthorizationURL)
	q := au.Query()
	if au.Host != "accounts.google.com" || q.Get("client_id") != "cid" || q.Get("state") != state["state"] ||
		q.Get("code_challenge") != pkceS256(state["codeVerifier"].(string)) || q.Get("access_type") != "offline" ||
		q.Get("redirect_uri") != "https://neko.example/api/pack-accounts/google-workspace/workspace/callback" ||
		state["returnPath"] != "/admin/settings/packs" || state["userId"] != "admin" {
		t.Errorf("start: %s, state %v", begun.AuthorizationURL, state)
	}

	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values
		('org', 'google-workspace', '0.2.0', 'installed', 'h', '{"keep":1,"_oauth":{"other":{"key":"other"}}}')`)
	callback := func() *httptest.ResponseRecorder {
		st := map[string]any{"packId": "google-workspace", "connectionKey": "workspace", "state": "st", "codeVerifier": "cv",
			"returnPath": "/admin/settings/packs?tab=oauth", "userId": "admin", "orgId": "org",
			"expiresAt": time.Now().Add(time.Minute).UnixMilli()}
		return send("GET", workspaceRoute+"/callback?code=c1&state=st", "", plusCookie(admin, packCookie(t, f.API, st)))
	}
	allScopes := strings.Join(st.Scopes, " ")
	provider.token = `{"access_token":"at-secret-1","expires_in":3600,"scope":"` + allScopes + `"}`
	if w := callback(); w.Code != 400 || !strings.Contains(w.Body.String(), "did not return a refresh token") {
		t.Errorf("no refresh token: %d %s", w.Code, w.Body.String())
	}
	provider.token = `{"access_token":"at-secret-1","refresh_token":"rt-secret-1","expires_in":3600,"scope":"openid"}`
	if w := callback(); w.Code != 400 || !strings.Contains(w.Body.String(), "did not grant required scope(s): https://www.googleapis.com/auth/userinfo.email") {
		t.Errorf("missing scope: %d %s", w.Code, w.Body.String())
	}
	provider.token = `{"access_token":"at-secret-1","refresh_token":"rt-secret-1","expires_in":3600,"scope":"` + allScopes + `"}`
	wk.fail = "graphjin is down"
	if w := callback(); w.Code != 400 || !strings.Contains(w.Body.String(), `"error":"Worker returned HTTP 502"`) {
		t.Errorf("sync failure: %d %s", w.Code, w.Body.String())
	}
	store, _ = secrets.Read(f.API.SecretsDir)
	if store["pack.google-workspace"]["OAUTH_WORKSPACE_SYNC_PENDING"] != "true" {
		t.Errorf("pending mark after a failed sync: %v", store["pack.google-workspace"])
	}
	wk.fail = ""
	wk.reply["/admin/packs/google-workspace/oauth/workspace/sync"] = `{"synced":true}`
	wk.calls = nil
	w = callback()
	if w.Code != 302 || w.Header().Get("Location") != "/admin/settings/packs?tab=oauth&pack=google-workspace&connected=workspace" {
		t.Fatalf("complete: %d %s %q", w.Code, w.Body.String(), w.Header().Get("Location"))
	}
	if strings.Join(wk.calls, "\n") != `POST /admin/packs/google-workspace/oauth/workspace/sync {"enabled":true}` {
		t.Errorf("worker calls %v", wk.calls)
	}
	form := provider.forms[len(provider.forms)-1]
	if form.Get("client_secret") != "s3cret-value" || form.Get("code_verifier") != "cv" || form.Get("code") != "c1" {
		t.Errorf("token form %v", form)
	}
	store, _ = secrets.Read(f.API.SecretsDir)
	s := store["pack.google-workspace"]
	if s["GOOGLE_WORKSPACE_ACCESS_TOKEN"] != "at-secret-1" || s["GOOGLE_WORKSPACE_REFRESH_TOKEN"] != "rt-secret-1" ||
		s["OAUTH_WORKSPACE_ACCOUNT_ID"] != "u-1" || s["OAUTH_WORKSPACE_SCOPES"] != allScopes || s["OAUTH_WORKSPACE_EXPIRES_AT"] == "" {
		t.Errorf("stored tokens %v", s)
	}
	if _, ok := s["OAUTH_WORKSPACE_SYNC_PENDING"]; ok {
		t.Error("pending mark stays after the sync")
	}
	var config string
	if err := db.QueryRow(t.Context(), `select config::text from pack_install where pack_id = 'google-workspace'`).Scan(&config); err != nil {
		t.Fatal(err)
	}
	want := `{"keep": 1, "_oauth": {"other": {"key": "other"}, "workspace": {"key": "workspace", "scopes": [`
	if !strings.HasPrefix(config, want) || !strings.Contains(config, `"accountId": "u-1", "accountLabel": "owner@example.com", "providerLabel": "Google Workspace"`) {
		t.Errorf("binding %s", config)
	}
	w = send("GET", workspaceRoute, "", admin)
	st = oauthStatusOf(t, w.Body.String())
	if !st.Connected || st.Account == nil || st.Account.Label != "owner@example.com" || st.ExpiresAt == nil {
		t.Errorf("connected status %s", w.Body.String())
	}

	wk.calls = nil
	if w := send("DELETE", workspaceRoute, "", admin); w.Code != 200 || w.Body.String() != `{"disconnected":true}` {
		t.Errorf("disconnect: %d %q", w.Code, w.Body.String())
	}
	if strings.Join(wk.calls, "\n") != `POST /admin/packs/google-workspace/oauth/workspace/sync {"enabled":false}` {
		t.Errorf("disconnect calls %v", wk.calls)
	}
	store, _ = secrets.Read(f.API.SecretsDir)
	if s := store["pack.google-workspace"]; s["GOOGLE_WORKSPACE_ACCESS_TOKEN"] != "" || s["OAUTH_WORKSPACE_ACCOUNT_ID"] != "" || s["OAUTH_WORKSPACE_CLIENT_ID"] != "cid" {
		t.Errorf("after disconnect %v", s)
	}
	_ = db.QueryRow(t.Context(), `select config::text from pack_install where pack_id = 'google-workspace'`).Scan(&config)
	if config != `{"keep": 1, "_oauth": {"other": {"key": "other"}}}` {
		t.Errorf("binding after disconnect %s", config)
	}
	if w := send("DELETE", workspaceRoute, "", admin); w.Body.String() != `{"disconnected":false}` {
		t.Errorf("second disconnect %s", w.Body.String())
	}

	everything := strings.Join(bodies, "\n") + logs.String()
	for _, secret := range []string{"s3cret-value", "at-secret-1", "rt-secret-1"} {
		if strings.Contains(everything, secret) {
			t.Errorf("%s appears in a response or the log", secret)
		}
	}
}

func TestPackOAuthPersonalConnectionRefusals(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	abs, _ := filepath.Abs(filepath.Join("..", "..", "..", "..", "packs"))
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	admin := sessionCookie("admin")
	if w := call(f, "POST", workspaceRoute, `{"clientId":"cid","clientSecret":"s"}`, admin); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "Install this pack before configuring its OAuth client") {
		t.Errorf("configure before install: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", workspaceRoute+"/start", `{"clientId":"cid","clientSecret":"s"}`, admin); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "Connect your personal account on Integrations") || cookieFrom(w, PackOAuthCookie) != nil {
		t.Errorf("personal start: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config) values
		('00000000-0000-0000-0000-0000000000a1', 'org', 'google-workspace', '0.2.0', 'installed', 'h', '{}')`)
	seed(t, db, `insert into pack_user_connection (org_id, user_id, pack_install_id, connection_key) values
		('org', 'member', '00000000-0000-0000-0000-0000000000a1', 'workspace')`)
	countConnections := func() int {
		var n int
		_ = db.QueryRow(t.Context(), `select count(*) from pack_user_connection`).Scan(&n)
		return n
	}
	if w := call(f, "POST", workspaceRoute, `{"clientId":"cid","clientSecret":"s"}`, admin); w.Code != 200 || countConnections() != 0 {
		t.Errorf("new client: %d %s, %d connections", w.Code, w.Body.String(), countConnections())
	}
	seed(t, db, `insert into pack_user_connection (org_id, user_id, pack_install_id, connection_key) values
		('org', 'member', '00000000-0000-0000-0000-0000000000a1', 'workspace')`)
	if w := call(f, "POST", workspaceRoute, `{"clientId":"cid"}`, admin); w.Code != 200 || countConnections() != 1 {
		t.Errorf("same client: %d %s, %d connections", w.Code, w.Body.String(), countConnections())
	}
}

func TestPackAuthorizationURLMatchesNode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "packoauth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(filepath.Join("..", "..", "..", "..", "packs"))
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	for id, url := range want {
		packID, key, _ := strings.Cut(id, "/")
		bundle, err := packBundle("org", packID, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range bundle.Manifest.OAuth {
			if c.Key != key {
				continue
			}
			p := &packOAuth{packID: packID, conn: c}
			got, err := authorizationURL(p.connection(), "client id+1", "https://neko.example/api/pack-accounts/x/y/callback", "state~1", "challenge_-1")
			if err != nil || got != url {
				t.Errorf("%s:\n got %s\nwant %s", id, got, url)
			}
		}
	}
}
