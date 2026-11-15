package webfront

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type scalekitStub struct {
	mu          sync.Mutex
	srv         *httptest.Server
	wellKnown   int
	tokenStatus int
	tokenReply  string
	registered  []map[string]any
	exchanged   []url.Values
}

func newScalekitStub(t *testing.T) *scalekitStub {
	s := &scalekitStub{tokenReply: `{"access_token":"at-mgmt","refresh_token":"rt-mgmt","expires_in":3600}`}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "GET" && r.URL.Path == "/.well-known/oauth-protected-resource":
			_, _ = w.Write([]byte(`{"authorization_servers":["` + s.srv.URL + `/as",""],"scopes_supported":["wks:read",7,"org:write"]}`))
		case r.Method == "GET" && r.URL.Path == "/as/.well-known/oauth-authorization-server" && s.wellKnown != 0:
			w.WriteHeader(s.wellKnown)
			_, _ = w.Write([]byte("not here"))
		case r.Method == "GET" && (r.URL.Path == "/as" || r.URL.Path == "/as/.well-known/oauth-authorization-server"):
			_, _ = w.Write([]byte(`{"authorization_endpoint":"` + s.srv.URL + `/authorize?prompt=consent","token_endpoint":"` + s.srv.URL + `/token","registration_endpoint":"` + s.srv.URL + `/register"}`))
		case r.Method == "POST" && r.URL.Path == "/register" && r.Header.Get("Content-Type") == "application/json":
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			s.registered = append(s.registered, body)
			_, _ = w.Write([]byte(`{"client_id":"dcr-client-1"}`))
		case r.Method == "POST" && r.URL.Path == "/token" && r.Header.Get("Content-Type") == "application/x-www-form-urlencoded":
			form, _ := url.ParseQuery(string(raw))
			s.exchanged = append(s.exchanged, form)
			if s.tokenStatus != 0 {
				w.WriteHeader(s.tokenStatus)
			}
			_, _ = w.Write([]byte(s.tokenReply))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func connectFront(t *testing.T) (*Front, *scalekitStub) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	stub := newScalekitStub(t)
	g := newGate(t)
	seedSignInSecrets(t, g.SecretsDir, liveScalekit)
	store, err := secrets.Read(g.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	if store["signin.scalekit"] == nil {
		store["signin.scalekit"] = map[string]string{}
	}
	store["signin.scalekit"]["SCALEKIT_MCP_URL"] = stub.srv.URL + "/"
	if err := secrets.Write(store, g.SecretsDir); err != nil {
		t.Fatal(err)
	}
	g.DB = db
	return &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API: &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example", SecretsDir: g.SecretsDir,
			SignInClient: stub.srv.Client()},
		Node: &recorder{},
	}, stub
}

func withHeader(base map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for bk, bv := range base {
		out[bk] = bv
	}
	return out
}

func storeWorkspaceCredential(t *testing.T, dir string) {
	t.Helper()
	cred := &scalekitOperatorCredential{Tokens: map[string]any{"access_token": "at"}, ConnectedAt: "2026-05-22T10:00:00Z"}
	if err := writeScalekitOperatorCredential(dir, cred); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationsList(t *testing.T) {
	f, _ := connectFront(t)
	if w := call(f, "GET", "/api/v1/integrations/list", "", nil); w.Code == 200 {
		t.Errorf("anonymous: %d", w.Code)
	}
	disconnected := `{"connectors":[],"workspace":[{"pluginId":"scalekit","pluginName":"scalekit","providerLabel":"Scalekit workspace","scopes":["wks:read","wks:write","env:read","env:write","org:read","org:write"],"flow":"mcp-oauth","credentialScope":"deployment","connected":false,"connectedAt":null}]}`
	if w := call(f, "GET", "/api/integrations/list", "", sessionCookie("member")); w.Code != 200 || w.Body.String() != disconnected {
		t.Errorf("not connected: %d %s", w.Code, w.Body.String())
	}
	storeWorkspaceCredential(t, f.API.SecretsDir)
	connected := strings.Replace(disconnected, `"connected":false,"connectedAt":null`, `"connected":true,"connectedAt":"2026-05-22T10:00:00Z"`, 1)
	if w := call(f, "GET", "/api/v1/integrations/list", "", sessionCookie("admin")); w.Code != 200 || w.Body.String() != connected {
		t.Errorf("connected: %d %s", w.Code, w.Body.String())
	}
	seed(t, f.API.DB, `delete from item_grant where org_id = 'org' and item_type = 'integration'`)
	if w := call(f, "GET", "/api/v1/integrations/list", "", sessionCookie("member")); w.Code != 200 || w.Body.String() != `{"connectors":[],"workspace":[]}` {
		t.Errorf("not held: %d %s", w.Code, w.Body.String())
	}
}

func TestIntegrationDisconnect(t *testing.T) {
	f, _ := connectFront(t)
	member, admin := sessionCookie("member"), sessionCookie("admin")
	storeWorkspaceCredential(t, f.API.SecretsDir)
	if err := writeKeepingSignIn(secrets.Store{"channel.slack": {"SLACK_BOT_TOKEN": "xoxb-1"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	if w := call(f, "POST", "/api/v1/integrations/disconnect/scalekit", "", member); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
		t.Errorf("deployment as member: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/integrations/disconnect/other", "", member); w.Code != 200 || w.Body.String() != `{"removed":false}` {
		t.Errorf("unknown: %d %s", w.Code, w.Body.String())
	}
	if cred, _ := readScalekitOperatorCredential(f.API.SecretsDir); cred == nil {
		t.Fatal("credential removed by a refused call")
	}
	if w := call(f, "POST", "/api/integrations/disconnect/scalekit", "", admin); w.Code != 200 || w.Body.String() != `{"removed":true}` {
		t.Errorf("admin: %d %s", w.Code, w.Body.String())
	}
	if cred, _ := readScalekitOperatorCredential(f.API.SecretsDir); cred != nil {
		t.Errorf("credential still stored: %+v", cred)
	}
	raw, _ := os.ReadFile(secrets.Path(f.API.SecretsDir))
	if strings.Contains(string(raw), `"deployment"`) {
		t.Errorf("empty slot kept: %s", raw)
	}
	if store, _ := secrets.Read(f.API.SecretsDir); store["channel.slack"]["SLACK_BOT_TOKEN"] != "xoxb-1" || store["signin"]["PROVIDER"] != "scalekit" {
		t.Errorf("other sections lost: %v", store)
	}
	if w := call(f, "POST", "/api/integrations/disconnect/scalekit", "", admin); w.Body.String() != `{"removed":false}` {
		t.Errorf("second disconnect: %s", w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/integrations/disconnect/scalekit", "", member); w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestIntegrationConnectStart(t *testing.T) {
	f, stub := connectFront(t)
	member, admin := sessionCookie("member"), sessionCookie("admin")
	if w := call(f, "GET", "/api/v1/integrations/connect/missing/start", "", member); w.Code != 404 || w.Body.String() != `{"error":"connect plugin \"missing\" not installed"}` {
		t.Errorf("missing: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/integrations/connect/scalekit/start", "", member); w.Code != 403 {
		t.Errorf("deployment as member: %d %s", w.Code, w.Body.String())
	}

	w := call(f, "GET", "/api/integrations/connect/scalekit/start?returnTo=//evil.example", "", admin)
	if w.Code != 302 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	stored := f.API.readAndClearConnectState(httptest.NewRecorder(), cookieRequest(w.Result().Cookies()[0].Value))
	if stored == nil || stored.OperatorID != "deployment" || stored.ReturnPath != "/integrations" || len(stored.State) != 32 || len(stored.CodeVerifier) != 43 {
		t.Fatalf("stored %+v", stored)
	}
	sum := sha256.Sum256([]byte(stored.CodeVerifier))
	redirect := "https://neko.example/api/integrations/connect/scalekit/callback"
	want := stub.srv.URL + "/authorize?prompt=consent&response_type=code&client_id=dcr-client-1&redirect_uri=" + url.QueryEscape(redirect) +
		"&state=" + stored.State + "&code_challenge=" + base64.RawURLEncoding.EncodeToString(sum[:]) + "&code_challenge_method=S256&scope=wks%3Aread+org%3Awrite"
	if got := w.Header().Get("Location"); got != want {
		t.Errorf("location\n got %s\nwant %s", got, want)
	}
	if len(stub.registered) != 1 || stub.registered[0]["client_name"] != "openneko-scalekit" || stub.registered[0]["redirect_uris"].([]any)[0] != redirect ||
		stub.registered[0]["token_endpoint_auth_method"] != "none" {
		t.Errorf("registration %v", stub.registered)
	}
	state, err := parseMCPOAuthState(stored.OAuthState)
	if err != nil || state.ClientID != "dcr-client-1" || state.TokenEndpoint != stub.srv.URL+"/token" || state.ClientSecret != nil ||
		state.CodeVerifier != stored.CodeVerifier || state.MCPURL != stub.srv.URL || state.AuthorizationServerURL != stub.srv.URL+"/as" ||
		strings.Join(state.Scopes, ",") != "wks:read,org:write" || state.RedirectURI != redirect {
		t.Errorf("oauthState %+v %v", state, err)
	}

	stub.wellKnown = 404
	if w := call(f, "GET", "/api/v1/integrations/connect/scalekit/start", "", admin); w.Code != 302 {
		t.Errorf("raw URL fallback: %d %s", w.Code, w.Body.String())
	}
	stub.wellKnown = 500
	w = call(f, "GET", "/api/v1/integrations/connect/scalekit/start", "", admin)
	if w.Code != 502 || w.Body.String() != `{"error":"connect begin failed: MCP OAuth authorization server discovery returned non-JSON (HTTP 500)"}` || len(w.Result().Cookies()) != 0 {
		t.Errorf("begin failure: %d %s", w.Code, w.Body.String())
	}

}

func TestBeginConnectRefusesPlainHTTP(t *testing.T) {
	for _, mcp := range []string{"http://mcp.example", " ", "https://"} {
		if _, err := mcpResourceMetadataURL(mcp); err == nil {
			t.Errorf("%q accepted", mcp)
		}
	}
	if got, _ := mcpResourceMetadataURL("https://mcp.example/a/b?x=1/"); got != "https://mcp.example/.well-known/oauth-protected-resource" {
		t.Errorf("metadata URL %s", got)
	}
}

func timeIn(seconds int) time.Time { return time.Now().Add(time.Duration(seconds) * time.Second) }

func cookieRequest(value string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: ConnectStateCookie, Value: value})
	return r
}

func TestIntegrationConnectCallback(t *testing.T) {
	f, stub := connectFront(t)
	oauthState, _ := json.Marshal(mcpOAuthState{TokenEndpoint: stub.srv.URL + "/token", ClientID: "dcr-client-1", CodeVerifier: "state-verifier",
		Scopes: []string{"wks:read"}, RedirectURI: "https://neko.example/cb"})
	state := connectState{"scalekit", "deployment", "st", "verifier", "/settings/sso", base64.RawURLEncoding.EncodeToString(oauthState)}
	good, err := f.API.connectStateCookie(state)
	if err != nil {
		t.Fatal(err)
	}
	callback := "/api/integrations/connect/scalekit/callback"
	with := func(value string) map[string]string {
		return map[string]string{"Cookie": ConnectStateCookie + "=" + value}
	}
	for _, c := range []struct {
		query, cookie, location string
	}{
		{"?error=access_denied&error_description=No+thanks", good, "/integrations?error=No%20thanks"},
		{"?error=access_denied", good, "/integrations?error=access_denied"},
		{"?code=c", good, "/integrations?error=missing+code+or+state"},
		{"?code=c&state=st", "", "/integrations?error=state+cookie+missing+or+expired"},
		{"?code=c&state=st", good + "x", "/integrations?error=state+cookie+missing+or+expired"},
		{"?code=c&state=other", good, "/integrations?error=state+mismatch"},
	} {
		var header map[string]string
		if c.cookie != "" {
			header = with(c.cookie)
		}
		w := call(f, "GET", callback+c.query, "", header)
		if w.Code != 302 || w.Header().Get("Location") != c.location {
			t.Errorf("%s: %d %q", c.query, w.Code, w.Header().Get("Location"))
		}
	}
	if w := call(f, "GET", "/api/v1/integrations/connect/other/callback?code=c&state=st", "", with(good)); w.Header().Get("Location") != "/integrations?error=plugin+mismatch" {
		t.Errorf("mismatch: %q", w.Header().Get("Location"))
	}
	if len(stub.exchanged) != 0 {
		t.Errorf("refused callbacks reached the token endpoint: %v", stub.exchanged)
	}

	stub.tokenStatus, stub.tokenReply = 400, `bad code at-leaked`
	w := call(f, "GET", callback+"?code=c&state=st", "", with(good))
	if w.Header().Get("Location") != "/settings/sso?error="+encodeURIComponent("connect complete failed: MCP OAuth code exchange returned non-JSON (HTTP 400)") {
		t.Errorf("complete failure: %q", w.Header().Get("Location"))
	}
	if cred, _ := readScalekitOperatorCredential(f.API.SecretsDir); cred != nil {
		t.Errorf("failed exchange stored a credential")
	}

	stub.tokenStatus, stub.tokenReply = 0, `{"access_token":"at-mgmt","refresh_token":"rt-mgmt","expires_in":3600}`
	stub.exchanged = nil
	w = call(f, "GET", callback+"?code=c&state=st", "", with(good))
	if w.Code != 302 || w.Header().Get("Location") != "/settings/sso?connected=scalekit" {
		t.Errorf("success: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c := w.Result().Cookies(); len(c) != 1 || c[0].Name != ConnectStateCookie || c[0].MaxAge >= 0 {
		t.Errorf("cookie not cleared: %v", c)
	}
	if len(stub.exchanged) != 1 {
		t.Fatalf("exchange %v", stub.exchanged)
	}
	form := stub.exchanged[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != "dcr-client-1" || form.Get("code") != "c" ||
		form.Get("code_verifier") != "verifier" || form.Get("redirect_uri") != "https://neko.example"+callback || form.Has("client_secret") {
		t.Errorf("exchange form %v", form)
	}
	cred, err := readScalekitOperatorCredential(f.API.SecretsDir)
	if err != nil || cred == nil || cred.Tokens["access_token"] != "at-mgmt" || cred.Tokens["refresh_token"] != "rt-mgmt" ||
		cred.Tokens["token_endpoint"] != stub.srv.URL+"/token" || cred.Tokens["client_id"] != "dcr-client-1" || cred.Tokens["client_secret"] != "" ||
		cred.ProviderLabel != "Scalekit workspace" || strings.Join(cred.Scopes, ",") != "wks:read" || !scalekitCredentialNearExpiry(cred, timeIn(3600)) || scalekitCredentialNearExpiry(cred, timeIn(3000)) {
		t.Errorf("credential %+v %v", cred, err)
	}
	raw, _ := os.ReadFile(secrets.Path(f.API.SecretsDir))
	if strings.Contains(string(raw), "at-mgmt") || strings.Contains(string(raw), "rt-mgmt") {
		t.Errorf("tokens stored in clear")
	}
	if w := call(f, "GET", "/api/v1/integrations/list", "", sessionCookie("admin")); !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Errorf("list after connect: %s", w.Body.String())
	}
}

func TestConnectStateCookieFromNode(t *testing.T) {
	f, _ := connectFront(t)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"pluginName":"scalekit","operatorId":"deployment","state":"s","codeVerifier":"v","returnPath":"/integrations"}`))
	node := payload + "." + macOf(testSecret, payload)
	got := f.API.readAndClearConnectState(httptest.NewRecorder(), cookieRequest(node))
	if got == nil || *got != (connectState{"scalekit", "deployment", "s", "v", "/integrations", ""}) {
		t.Errorf("node cookie: %+v", got)
	}
	for _, body := range []string{
		`{"pluginName":"scalekit","operatorId":"deployment","state":"s","codeVerifier":"v"}`,
		`{"pluginName":"scalekit","operatorId":"deployment","state":"s","codeVerifier":"v","returnPath":null}`,
		`{"pluginName":"scalekit","operatorId":"deployment","state":"s","codeVerifier":"v","returnPath":"/","oauthState":1}`,
		`[]`,
	} {
		p := base64.RawURLEncoding.EncodeToString([]byte(body))
		if got := f.API.readAndClearConnectState(httptest.NewRecorder(), cookieRequest(p+"."+macOf(testSecret, p))); got != nil {
			t.Errorf("%s: %+v", body, got)
		}
	}
	if s, _ := url.QueryUnescape(encodeURIComponent("a b/@")); s != "a b/@" {
		t.Errorf("encode %q", s)
	}
}

func TestMCPOAuthStateFromNode(t *testing.T) {
	node := base64.RawURLEncoding.EncodeToString([]byte(`{"mcpUrl":"https://mcp.scalekit.com","authorizationServerUrl":"https://as.scalekit.com","tokenEndpoint":"https://as.scalekit.com/token","clientId":"dcr-client-1","clientSecret":null,"codeVerifier":"v","scopes":["wks:read",3],"redirectUri":"https://app.example.com/cb"}`))
	s, err := parseMCPOAuthState(node)
	if err != nil || s.ClientSecret != nil || strings.Join(s.Scopes, ",") != "wks:read" || s.RedirectURI != "https://app.example.com/cb" || s.MCPURL != "https://mcp.scalekit.com" {
		t.Errorf("node state %+v %v", s, err)
	}
	for raw, want := range map[string]string{
		"":    "missing oauthState — begin_connect was not run (or its state was lost)",
		"%%%": "oauthState is not valid base64url JSON",
		base64.RawURLEncoding.EncodeToString([]byte(`{"clientId":"c"}`)): "oauthState is missing required fields",
	} {
		if _, err := parseMCPOAuthState(raw); err == nil || err.Error() != want {
			t.Errorf("%q: %v", raw, err)
		}
	}
}
