package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type setupWorker struct {
	mu     sync.Mutex
	status string
	reply  map[string]string
	fail   string
	calls  []string
}

func (s *setupWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/admin/auth/status" {
		_, _ = w.Write([]byte(s.status))
		return
	}
	body := ""
	if r.Method == http.MethodPost {
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		encoded, _ := json.Marshal(v)
		body = " " + string(encoded)
	}
	s.calls = append(s.calls, r.Method+" "+r.URL.RequestURI()+body)
	if s.fail != "" {
		http.Error(w, s.fail, http.StatusBadGateway)
		return
	}
	reply, ok := s.reply[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(reply))
}

func newSetupFront(t *testing.T, status string, db *pgxpool.Pool) (*Front, *setupWorker) {
	wk := &setupWorker{status: status, reply: map[string]string{}}
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

func seedOrg(t *testing.T, db *pgxpool.Pool) {
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into app_user (id, org_id, email, name) values ('admin', 'org', 'a@example.com', 'Ada'), ('member', 'org', 'm@example.com', null)`)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'admin', 'local' from user_group where org_id = 'org' and slug = 'administrators'`)
}

func TestSSOSetupRoutes(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, wk := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	wk.reply["/admin/sso/setup/status"] = `{"status":"connected","portalLink":null}`
	wk.reply["/admin/sso/setup/environments"] = `{}`
	wk.reply["/admin/sso/setup/organizations"] = `{"organizations":[{"id":"o1","name":"<Acme>"}]}`
	wk.reply["/admin/sso/setup/credentials-info"] = `{"environmentUrl":"https://env.example","clientId":"cid"}`
	wk.reply["/admin/sso/setup/ids"] = `{"ok":true}`
	wk.reply["/admin/sso/setup/credentials"] = `{"ok":true}`
	wk.reply["/admin/sso/setup/generate-portal-link"] = `{"portalLink":"https://portal.example/x","expiresAt":null}`
	wk.reply["/admin/sso/setup/check"] = `{"status":"connected","connection":null,"lastError":null,"setupCompletedAt":null}`

	for _, who := range []map[string]string{nil, member} {
		for _, p := range []string{"/api/v1/sso/setup/status", "/api/sso/setup/environments"} {
			if w := call(f, "GET", p, "", who); w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
				t.Errorf("%s as %v: %d %s", p, who, w.Code, w.Body.String())
			}
		}
		if w := call(f, "POST", "/api/v1/sso/setup/check", "", who); w.Code != 403 {
			t.Errorf("check as %v: %d", who, w.Code)
		}
	}
	if len(wk.calls) != 0 {
		t.Fatalf("a refused caller reached the worker: %v", wk.calls)
	}

	for _, c := range []struct{ method, path, body, code, want string }{
		{"GET", "/api/v1/sso/setup/status", "", "200", `{"status":"connected","portalLink":null}`},
		{"GET", "/api/v1/sso/setup/environments", "", "200", `{"environments":[]}`},
		{"GET", "/api/v1/sso/setup/organizations", "", "400", `{"error":"environmentId query parameter is required"}`},
		{"GET", "/api/v1/sso/setup/organizations?environmentId=env%201", "", "200", `{"organizations":[{"id":"o1","name":"<Acme>"}]}`},
		{"GET", "/api/v1/sso/setup/credentials-info", "", "400", `{"error":"environmentId query parameter is required"}`},
		{"GET", "/api/v1/sso/setup/credentials-info?environmentId=env1", "", "200", `{"environmentUrl":"https://env.example","clientId":"cid"}`},
		{"POST", "/api/v1/sso/setup/ids", `{}`, "400", `{"error":"environmentId (string) is required"}`},
		{"POST", "/api/v1/sso/setup/ids", `{"environmentId":"env1","organizationId":""}`, "400", `{"error":"organizationId (string) is required"}`},
		{"POST", "/api/v1/sso/setup/ids", `{"environmentId":"env1","organizationId":"o1","tier":7}`, "200", `{"ok":true}`},
		{"POST", "/api/v1/sso/setup/credentials", `not json`, "400", `{"error":"environmentUrl (string) is required"}`},
		{"POST", "/api/v1/sso/setup/credentials", `{"environmentUrl":"https://env.example","clientId":"cid"}`, "400", `{"error":"clientSecret (string) is required"}`},
		{"POST", "/api/v1/sso/setup/credentials", `{"environmentUrl":"https://env.example","clientId":"cid","clientSecret":"s3cret"}`, "200", `{"ok":true}`},
		{"POST", "/api/v1/sso/setup/generate-portal-link", "", "200", `{"portalLink":"https://portal.example/x","expiresAt":null}`},
		{"POST", "/api/v1/sso/setup/check", "", "200", `{"status":"connected","connection":null,"lastError":null,"setupCompletedAt":null}`},
	} {
		w := call(f, c.method, c.path, c.body, admin)
		if got := w.Body.String(); w.Result().Status[:3] != c.code || got != c.want {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, w.Code, got)
		}
	}
	want := []string{
		"GET /admin/sso/setup/status?orgId=org",
		"GET /admin/sso/setup/environments?orgId=org",
		"GET /admin/sso/setup/organizations?orgId=org&environmentId=env%201",
		"GET /admin/sso/setup/credentials-info?orgId=org&environmentId=env1",
		`POST /admin/sso/setup/ids {"environmentId":"env1","orgId":"org","organizationId":"o1","tier":"dev"}`,
		`POST /admin/sso/setup/credentials {"clientId":"cid","clientSecret":"s3cret","environmentUrl":"https://env.example","orgId":"org"}`,
		`POST /admin/sso/setup/generate-portal-link {"orgId":"org"}`,
		`POST /admin/sso/setup/check {"orgId":"org"}`,
	}
	if strings.Join(wk.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("worker calls:\n%s", strings.Join(wk.calls, "\n"))
	}

	wk.fail = `{"error":"scalekit down"}`
	if w := call(f, "GET", "/api/v1/sso/setup/status", "", admin); w.Code != 500 || w.Body.String() != `{"error":"SSO setup status failed (502): scalekit down"}` {
		t.Errorf("worker error: %d %s", w.Code, w.Body.String())
	}
	wk.fail = "plain text"
	if w := call(f, "POST", "/api/v1/sso/setup/check", "", admin); w.Body.String() != `{"error":"SSO connection check failed (502): plain text\n"}` {
		t.Errorf("worker text: %s", w.Body.String())
	}
	if w := call(f, "PUT", "/api/v1/sso/setup/check", "", admin); w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Errorf("PUT: %d %q", w.Code, w.Header().Get("Allow"))
	}
}

func TestSSOSetupSoloAndWorkerDown(t *testing.T) {
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	f, wk := newSetupFront(t, `{"provider":null}`, db)
	wk.reply["/admin/sso/setup/status"] = `{"status":"not_started"}`
	if w := call(f, "GET", "/api/v1/sso/setup/status", "", nil); w.Code != 200 || w.Body.String() != `{"status":"not_started"}` {
		t.Errorf("solo administrator: %d %s", w.Code, w.Body.String())
	}
	f.API.Gate.WorkerURL = "http://127.0.0.1:1"
	if w := call(f, "POST", "/api/v1/sso/setup/check", "", nil); w.Code != 500 || w.Body.String() != `{"error":"SSO connection check failed: the worker did not answer"}` {
		t.Errorf("worker down: %d %s", w.Code, w.Body.String())
	}
}

func TestProfileRoute(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	member := sessionCookie("member")
	if w := call(f, "GET", "/api/v1/profile", "", member); w.Code != 404 || w.Body.String() != `{"error":"no profile"}` {
		t.Errorf("no profile: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "PATCH", "/api/v1/profile", `{"businessProfile":"x"}`, member); w.Code != 404 {
		t.Errorf("patch without profile: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `insert into customer_profile (org_id, version, is_current, business_profile, company_note) values ('org', 1, false, 'old', null), ('org', 2, true, 'We sell tea.', 'Note')`)

	status := func() string {
		t.Helper()
		got := decode(t, call(f, "GET", "/api/v1/profile", "", member))
		return got["industryInsightsStatus"].(string)
	}
	if w := call(f, "GET", "/api/profile", "", member); w.Body.String() != `{"businessProfile":"We sell tea.","industryInsights":"","companyNote":"Note","industryInsightsStatus":"pending"}` {
		t.Errorf("pending: %s", w.Body.String())
	}
	seed(t, db, `insert into processing_job (org_id, kind, status, trigger) values ('org', 'industry_insights_build', 'done', 'x')`)
	if s := status(); s != "pending" {
		t.Errorf("finished job: %s", s)
	}
	seed(t, db, `insert into processing_job (org_id, kind, status, trigger) values ('org', 'industry_insights_build', 'running', 'x')`)
	if s := status(); s != "processing" {
		t.Errorf("running job: %s", s)
	}
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, enabled) values ('org', 'research', 'perplexity', false)`)
	if s := status(); s != "disabled" {
		t.Errorf("research off: %s", s)
	}
	seed(t, db, `update llm_provider_config set provider = 'other' where org_id = 'org'`)
	if s := status(); s != "processing" {
		t.Errorf("unknown research provider falls back to the default: %s", s)
	}

	for _, c := range []struct{ body, code, want string }{
		{`null`, "400", `{"error":"invalid body"}`},
		{`"text"`, "400", `{"error":"invalid body"}`},
		{`[1]`, "400", `{"error":"no fields to update"}`},
		{`{"businessProfile":7}`, "400", `{"error":"no fields to update"}`},
		{`{"industryInsights":"` + strings.Repeat("😀", 100_001) + `"}`, "413", `{"error":"industryInsights too large"}`},
		{`{"industryInsights":"Tea demand rises in winter."}`, "200", `{"businessProfile":"We sell tea.","industryInsights":"Tea demand rises in winter."}`},
		{`{"businessProfile":""}`, "200", `{"businessProfile":"","industryInsights":"Tea demand rises in winter."}`},
	} {
		w := call(f, "PATCH", "/api/v1/profile", c.body, member)
		if w.Result().Status[:3] != c.code || w.Body.String() != c.want {
			t.Errorf("PATCH %.40s: %d %.200s", c.body, w.Code, w.Body.String())
		}
	}
	if s := status(); s != "ready" {
		t.Errorf("insights stored: %s", s)
	}
	var old string
	if err := db.QueryRow(ctx, `select business_profile from customer_profile where version = 1`).Scan(&old); err != nil || old != "old" {
		t.Errorf("the old profile changed: %q %v", old, err)
	}
	if w := call(f, "GET", "/api/v1/profile", "", nil); w.Code != 302 {
		t.Errorf("anonymous: %d", w.Code)
	}
}

const driveConfig = `{"_userOAuth":[{"key":"account","providerLabel":"Drive","authorizationUrl":"https://accounts.example.com/o/oauth2/auth?prompt=login&x=a~b*c",
	"authorizationParams":{"prompt":"consent","access_type":"offline"},"clientSecret":"drive-client-secret","scopes":["openid","email https://x/y"]}]}`

func TestPersonalPackAccount(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values ('org', 'drive', '1', 'installed', 'h', $1)`, driveConfig)
	seed(t, db, `delete from item_grant where item_type = 'pack'`)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	same := func(c map[string]string) map[string]string {
		out := map[string]string{"Origin": "https://neko.example"}
		for k, v := range c {
			out[k] = v
		}
		return out
	}
	const route = "/api/v1/my/pack-accounts/drive/account"

	for _, c := range []struct {
		method, path string
		header       map[string]string
		code, want   string
	}{
		{"POST", route, map[string]string{"Origin": "https://neko.example"}, "302", ""},
		{"POST", "/api/v1/my/pack-accounts/Drive/account", same(admin), "400", `{"error":"Invalid pack connection"}`},
		{"POST", "/api/v1/my/pack-accounts/drive/account%2Fx", same(admin), "400", `{"error":"Invalid pack connection"}`},
		{"POST", route, same(member), "404", `{"error":"Pack not found"}`},
		{"POST", route, admin, "403", `{"error":"Invalid request origin"}`},
		{"DELETE", route, map[string]string{"Cookie": admin["Cookie"], "Origin": "https://elsewhere.test"}, "403", `{"error":"Invalid request origin"}`},
		{"POST", "/api/v1/my/pack-accounts/mail/account", same(admin), "400", `{"error":"This pack is not installed"}`},
		{"POST", "/api/v1/my/pack-accounts/drive/calendar", same(admin), "400", `{"error":"This personal connection is unavailable; ask an admin to upgrade the pack"}`},
		{"POST", route, same(admin), "400", `{"error":"Ask an admin to configure this connection first"}`},
		{"GET", route, same(admin), "405", ""},
	} {
		w := call(f, c.method, c.path, "", c.header)
		if w.Result().Status[:3] != c.code || (c.want != "" && w.Body.String() != c.want) {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}

	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secrets.json"),
		[]byte(`{"pack.drive":{"OAUTH_ACCOUNT_CLIENT_ID":"cid.apps","DRIVE_CLIENT_SECRET":"shh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'pack', '*' from user_group where org_id = 'org' and slug = 'everyone'`)
	w := call(f, "POST", route+"?returnTo=/chat/connectors", "", same(member))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("begin: %d %s %q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
	c := cookieFrom(w, PackOAuthCookie)
	if c == nil || !c.HttpOnly || c.MaxAge != 600 || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v", c)
	}
	body, mac, _ := strings.Cut(c.Value, ".")
	if mac != macOf(testSecret, body) {
		t.Fatal("cookie MAC")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(body)
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st["packId"] != "drive" || st["connectionKey"] != "account" || st["returnPath"] != "/chat/connectors" ||
		st["orgId"] != "org" || st["userId"] != "member" || st["personal"] != true || st["expiresAt"] == nil {
		t.Errorf("state %v", st)
	}
	state, verifier := st["state"].(string), st["codeVerifier"].(string)
	challenge := sha256.Sum256([]byte(verifier))
	want := "https://accounts.example.com/o/oauth2/auth?prompt=consent&x=a%7Eb*c&access_type=offline&response_type=code&client_id=cid.apps" +
		"&redirect_uri=https%3A%2F%2Fneko.example%2Fapi%2Fpack-accounts%2Fdrive%2Faccount%2Fcallback&scope=openid+email+https%3A%2F%2Fx%2Fy" +
		"&state=" + state + "&code_challenge=" + base64.RawURLEncoding.EncodeToString(challenge[:]) + "&code_challenge_method=S256"
	if got := decode(t, w)["authorizationUrl"]; got != want {
		t.Errorf("authorization URL\n got %v\nwant %s", got, want)
	}
	stateHash := sha256.Sum256([]byte(state))
	var pending string
	var live bool
	if err := db.QueryRow(ctx, `select pending_state_hash, pending_expires_at > now() + interval '9 minutes' from pack_user_connection where user_id = 'member'`).Scan(&pending, &live); err != nil ||
		pending != hex.EncodeToString(stateHash[:]) || !live {
		t.Errorf("pending row %q %v %v", pending, live, err)
	}

	seed(t, db, `update pack_user_connection set credentials = 'enc:v1:x', account_label = 'm@example.com'`)
	w = call(f, "DELETE", "/api/my/pack-accounts/drive/account", "", same(member))
	if w.Code != 200 || w.Body.String() != `{"disconnected":true}` {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	var cleared bool
	if err := db.QueryRow(ctx, `select credentials is null and account_label is null and pending_state_hash is null from pack_user_connection where user_id = 'member'`).Scan(&cleared); err != nil || !cleared {
		t.Errorf("disconnect left data: %v", err)
	}

	f.API.PublicURL = "http://neko.example"
	if w := call(f, "POST", route, "", map[string]string{"Cookie": admin["Cookie"], "Origin": "http://neko.example"}); w.Body.String() != `{"error":"OAuth callback must use HTTPS except on localhost"}` {
		t.Errorf("plain HTTP: %s", w.Body.String())
	}
	f.API.PublicURL = ""
	t.Setenv("OPENNEKO_PUBLIC_URL", "")
	if w := call(f, "POST", route, "", map[string]string{"Cookie": admin["Cookie"], "Origin": "https://app.example",
		"X-Forwarded-Host": "app.example, proxy", "X-Forwarded-Proto": "https"}); w.Code != 200 {
		t.Errorf("forwarded host: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `update app_user set disabled_at = now() where id = 'member'`)
	if w := call(f, "DELETE", route, "", same(member)); w.Code != 400 || w.Body.String() != `{"error":"Sign in to OpenNeko to manage your personal connections."}` {
		t.Errorf("disabled user: %d %s", w.Code, w.Body.String())
	}
}

func TestOriginOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://Neko.Example:443/x": "https://neko.example",
		"http://neko.example:80/":    "http://neko.example",
		"http://127.0.0.1:3000/a":    "http://127.0.0.1:3000",
		"http://[::1]:3000/a":        "http://[::1]:3000",
		"ftp://neko.example/":        "null",
	} {
		u, _ := url.Parse(in)
		if got := originOf(u); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

// goPackState is a cookie in the format that Node's writePackOAuthState
// wrote before the pack OAuth routes moved to Go.
const goPackState = "eyJjb2RlVmVyaWZpZXIiOiJjdiIsImNvbm5lY3Rpb25LZXkiOiJhY2NvdW50IiwiZXhwaXJlc0F0Ijo0MTAyNDQ0ODAwMDAwLCJvcmdJZCI6Im9yZyIsInBhY2tJZCI6ImRyaXZlIiwicGVyc29uYWwiOnRydWUsInJldHVyblBhdGgiOiIvaW50ZWdyYXRpb25zIiwic3RhdGUiOiJzdCIsInVzZXJJZCI6InUxIn0.yI0caLBmra1sp8q3_hh_YfWHoSNoq6t7V4o_xCev_d8"

func TestPackStateCookieForNode(t *testing.T) {
	a := &API{Gate: newGate(t, "")}
	got, err := a.packStateCookie(map[string]any{"packId": "drive", "connectionKey": "account", "state": "st", "codeVerifier": "cv",
		"returnPath": "/integrations", "orgId": "org", "userId": "u1", "personal": true, "expiresAt": int64(4102444800000)})
	if err != nil || got != goPackState {
		t.Errorf("got %s %v", got, err)
	}
}
