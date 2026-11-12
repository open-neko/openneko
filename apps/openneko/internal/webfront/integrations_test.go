package webfront

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const connectProvidersReply = `{"providers":[
	{"pluginId":"gws","pluginName":"@open-neko/connector-google-workspace","providerLabel":"Google Workspace","scopes":["gmail.send"],"flow":"oauth2-pkce","credentialScope":"operator"},
	{"pluginId":"scalekit","pluginName":"scalekit","providerLabel":"Scalekit workspace","scopes":["wks:read"],"flow":"mcp-oauth","credentialScope":"deployment"}]}`

type connectWorker struct {
	mu        sync.Mutex
	providers string
	status    map[string]int
	reply     map[string]string
	calls     map[string][]map[string]any
}

func (c *connectWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case r.URL.Path == "/admin/auth/status":
		_, _ = w.Write([]byte(liveScalekit))
		return
	case r.URL.Path == "/admin/connect/providers":
		_, _ = w.Write([]byte(c.providers))
		return
	case r.URL.Path == "/admin/connect/deployment/status":
		_, _ = w.Write([]byte(`{"connected":[{"pluginName":"scalekit","connectedAt":"2026-05-22T10:00:00Z"}]}`))
		return
	case strings.HasPrefix(r.URL.Path, "/admin/connect/status/"):
		if r.URL.Path == "/admin/connect/status/member" {
			_, _ = w.Write([]byte(`{"connected":[{"pluginName":"@open-neko/connector-google-workspace","connectedAt":"2026-05-21T10:00:00Z"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"connected":[]}`))
		}
		return
	}
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	c.calls[r.URL.Path] = append(c.calls[r.URL.Path], body)
	if s := c.status[r.URL.Path]; s != 0 {
		w.WriteHeader(s)
	}
	_, _ = w.Write([]byte(c.reply[r.URL.Path]))
}

func (c *connectWorker) take(path string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := c.calls[path]
	delete(c.calls, path)
	return calls
}

func connectFront(t *testing.T) (*Front, *connectWorker) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	wk := &connectWorker{providers: connectProvidersReply, status: map[string]int{}, reply: map[string]string{
		"/admin/connect/begin":      `{"authorizationUrl":"https://idp.example/authorize?x=1"}`,
		"/admin/connect/complete":   `{"credential":{"tokens":{},"connectedAt":"2026-11-12T00:00:00Z"}}`,
		"/admin/connect/disconnect": `{"removed":true}`,
	}, calls: map[string][]map[string]any{}}
	srv := httptest.NewServer(wk)
	t.Cleanup(srv.Close)
	g := newGate(t, srv.URL)
	g.DB = db
	return &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example", SecretsDir: t.TempDir()},
		Node: &recorder{},
	}, wk
}

func withHeader(base map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for bk, bv := range base {
		out[bk] = bv
	}
	return out
}

const gwsPath = "%40open-neko%2Fconnector-google-workspace"

func TestIntegrationsList(t *testing.T) {
	f, _ := connectFront(t)
	if w := call(f, "GET", "/api/v1/integrations/list", "", nil); w.Code == 200 {
		t.Errorf("anonymous: %d", w.Code)
	}
	want := `{"connectors":[{"pluginId":"gws","pluginName":"@open-neko/connector-google-workspace","providerLabel":"Google Workspace","scopes":["gmail.send"],"flow":"oauth2-pkce","credentialScope":"operator","connected":true,"connectedAt":"2026-05-21T10:00:00Z"}],` +
		`"workspace":[{"pluginId":"scalekit","pluginName":"scalekit","providerLabel":"Scalekit workspace","scopes":["wks:read"],"flow":"mcp-oauth","credentialScope":"deployment","connected":true,"connectedAt":"2026-05-22T10:00:00Z"}]}`
	if w := call(f, "GET", "/api/integrations/list", "", sessionCookie("member")); w.Code != 200 || w.Body.String() != want {
		t.Errorf("member: %d %s", w.Code, w.Body.String())
	}
	w := call(f, "GET", "/api/v1/integrations/list", "", sessionCookie("admin"))
	if body := decode(t, w); w.Code != 200 || body["connectors"].([]any)[0].(map[string]any)["connected"] != false ||
		body["connectors"].([]any)[0].(map[string]any)["connectedAt"] != nil {
		t.Errorf("admin: %d %s", w.Code, w.Body.String())
	}

	seed(t, f.API.DB, `delete from item_grant where org_id = 'org' and item_type = 'integration'`)
	seed(t, f.API.DB, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'integration', 'scalekit' from user_group where org_id = 'org' and slug = 'everyone'`)
	if w := call(f, "GET", "/api/v1/integrations/list", "", sessionCookie("member")); w.Code != 200 ||
		!strings.HasPrefix(w.Body.String(), `{"connectors":[],"workspace":[{"pluginId":"scalekit"`) {
		t.Errorf("narrowed: %d %s", w.Code, w.Body.String())
	}

	f.API.Gate.WorkerURL = "http://127.0.0.1:1"
	f.Gate.WorkerURL = f.API.Gate.WorkerURL
	if w := call(f, "GET", "/api/v1/integrations/list", "", sessionCookie("member")); w.Code == 200 && w.Body.String() != `{"connectors":[],"workspace":[]}` {
		t.Errorf("worker down: %d %s", w.Code, w.Body.String())
	}
}

func TestIntegrationDisconnect(t *testing.T) {
	f, wk := connectFront(t)
	member, admin := sessionCookie("member"), sessionCookie("admin")
	if w := call(f, "POST", "/api/v1/integrations/disconnect/"+gwsPath, "", member); w.Code != 200 || w.Body.String() != `{"removed":true}` {
		t.Errorf("member: %d %s", w.Code, w.Body.String())
	}
	if got := wk.take("/admin/connect/disconnect"); len(got) != 1 || got[0]["pluginName"] != "@open-neko/connector-google-workspace" || got[0]["operatorId"] != "member" {
		t.Errorf("member body %v", got)
	}
	if w := call(f, "POST", "/api/v1/integrations/disconnect/scalekit", "", member); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
		t.Errorf("deployment as member: %d %s", w.Code, w.Body.String())
	}
	if got := wk.take("/admin/connect/disconnect"); len(got) != 0 {
		t.Errorf("member reached the worker: %v", got)
	}
	call(f, "POST", "/api/integrations/disconnect/scalekit", "", admin)
	if got := wk.take("/admin/connect/disconnect"); len(got) != 1 || got[0]["operatorId"] != "deployment" {
		t.Errorf("admin body %v", got)
	}
	wk.status["/admin/connect/disconnect"] = 500
	if w := call(f, "POST", "/api/v1/integrations/disconnect/"+gwsPath, "", member); w.Code != 200 || w.Body.String() != `{"removed":false}` {
		t.Errorf("worker refusal: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/integrations/disconnect/scalekit", "", member); w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestIntegrationConnectStart(t *testing.T) {
	f, wk := connectFront(t)
	member, admin := sessionCookie("member"), sessionCookie("admin")
	if w := call(f, "GET", "/api/v1/integrations/connect/missing/start", "", member); w.Code != 404 || w.Body.String() != `{"error":"connect plugin \"missing\" not installed"}` {
		t.Errorf("missing: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/integrations/connect/"+gwsPath+"/start", "", nil); w.Code != 401 || w.Body.String() != `{"error":"not signed in"}` {
		t.Errorf("anonymous: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/integrations/connect/scalekit/start", "", member); w.Code != 403 {
		t.Errorf("deployment as member: %d %s", w.Code, w.Body.String())
	}

	w := call(f, "GET", "/api/integrations/connect/"+gwsPath+"/start?returnTo=/chat/connectors", "", member)
	if w.Code != 302 || w.Header().Get("Location") != "https://idp.example/authorize?x=1" {
		t.Fatalf("start: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	sent := wk.take("/admin/connect/begin")
	if len(sent) != 1 || sent[0]["pluginName"] != "@open-neko/connector-google-workspace" {
		t.Fatalf("begin %v", sent)
	}
	params := sent[0]["params"].(map[string]any)
	if params["operatorId"] != "member" || params["redirectUri"] != "https://neko.example/api/integrations/connect/%40open-neko%2Fconnector-google-workspace/callback" ||
		len(params["state"].(string)) != 32 || len(params["codeVerifier"].(string)) != 43 || params["scopes"].([]any)[0] != "gmail.send" {
		t.Errorf("begin params %v", params)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != ConnectStateCookie || cookie.MaxAge != 600 || !cookie.HttpOnly || cookie.Path != "/" {
		t.Errorf("cookie %+v", cookie)
	}
	stored := f.API.readAndClearConnectState(httptest.NewRecorder(), cookieRequest(cookie.Value))
	if stored == nil || stored.OperatorID != "member" || stored.State != params["state"] || stored.CodeVerifier != params["codeVerifier"] || stored.ReturnPath != "/chat/connectors" {
		t.Errorf("stored %+v", stored)
	}

	wk.reply["/admin/connect/begin"] = `{"authorizationUrl":"https://idp.example/a","oauthState":"opaque"}`
	w = call(f, "GET", "/api/v1/integrations/connect/scalekit/start?returnTo=//evil.example", "", admin)
	stored = f.API.readAndClearConnectState(httptest.NewRecorder(), cookieRequest(w.Result().Cookies()[0].Value))
	if w.Code != 302 || stored.OperatorID != "deployment" || stored.OAuthState != "opaque" || stored.ReturnPath != "/integrations" {
		t.Errorf("deployment: %d %+v", w.Code, stored)
	}
	wk.take("/admin/connect/begin")

	seed(t, f.API.DB, `delete from item_grant where org_id = 'org' and item_type = 'integration'`)
	if w := call(f, "GET", "/api/v1/integrations/connect/"+gwsPath+"/start", "", member); w.Code != 404 {
		t.Errorf("not held: %d %s", w.Code, w.Body.String())
	}
	wk.status["/admin/connect/begin"], wk.reply["/admin/connect/begin"] = 500, "boom"
	w = call(f, "GET", "/api/v1/integrations/connect/scalekit/start", "", admin)
	if w.Code != 502 || w.Body.String() != `{"error":"connect begin failed (500): boom"}` || len(w.Result().Cookies()) != 0 {
		t.Errorf("begin failure: %d %s", w.Code, w.Body.String())
	}
}

func cookieRequest(value string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: ConnectStateCookie, Value: value})
	return r
}

func TestIntegrationConnectCallback(t *testing.T) {
	f, wk := connectFront(t)
	state := connectState{"@open-neko/connector-google-workspace", "member", "st", "verifier", "/chat/connectors", ""}
	good, err := f.API.connectStateCookie(state)
	if err != nil {
		t.Fatal(err)
	}
	callback := "/api/integrations/connect/" + gwsPath + "/callback"
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
	if w := call(f, "GET", "/api/v1/integrations/connect/scalekit/callback?code=c&state=st", "", with(good)); w.Header().Get("Location") != "/integrations?error=plugin+mismatch" {
		t.Errorf("mismatch: %q", w.Header().Get("Location"))
	}
	if got := wk.take("/admin/connect/complete"); len(got) != 0 {
		t.Errorf("refused callbacks reached the worker: %v", got)
	}

	w := call(f, "GET", callback+"?code=c&state=st", "", with(good))
	if w.Code != 302 || w.Header().Get("Location") != "/chat/connectors?connected=%40open-neko%2Fconnector-google-workspace" {
		t.Errorf("success: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c := w.Result().Cookies(); len(c) != 1 || c[0].Name != ConnectStateCookie || c[0].MaxAge >= 0 {
		t.Errorf("cookie not cleared: %v", c)
	}
	sent := wk.take("/admin/connect/complete")
	if len(sent) != 1 {
		t.Fatalf("complete %v", sent)
	}
	params := sent[0]["params"].(map[string]any)
	if params["operatorId"] != "member" || params["code"] != "c" || params["codeVerifier"] != "verifier" ||
		params["redirectUri"] != "https://neko.example/api/integrations/connect/%40open-neko%2Fconnector-google-workspace/callback" || params["oauthState"] != nil {
		t.Errorf("complete params %v", params)
	}

	state.PluginName, state.OAuthState = "scalekit", "opaque"
	withOAuth, _ := f.API.connectStateCookie(state)
	wk.status["/admin/connect/complete"], wk.reply["/admin/connect/complete"] = 400, "bad code"
	w = call(f, "GET", "/api/v1/integrations/connect/scalekit/callback?code=c&state=st", "", with(withOAuth))
	if w.Header().Get("Location") != "/chat/connectors?error=connect%20complete%20failed%20(400)%3A%20bad%20code" {
		t.Errorf("complete failure: %q", w.Header().Get("Location"))
	}
	if sent := wk.take("/admin/connect/complete"); len(sent) != 1 || sent[0]["params"].(map[string]any)["oauthState"] != "opaque" {
		t.Errorf("oauthState not sent: %v", sent)
	}

	wk.providers = `{"providers":[]}`
	if w := call(f, "GET", callback+"?code=c&state=st", "", with(good)); w.Header().Get("Location") != "/integrations?error=plugin+removed" {
		t.Errorf("removed: %q", w.Header().Get("Location"))
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
