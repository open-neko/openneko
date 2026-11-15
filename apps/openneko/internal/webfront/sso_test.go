package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const nodeStateCookie = "abcState|L2NoYXQ_eD0x.PW77raE5KABLed-9mPUcO_mSWYfRSFrmM9H9xqsXucA"

type ssoWorker struct {
	mu       sync.Mutex
	identity string
	fail     bool
	calls    []map[string]any
}

func (s *ssoWorker) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
	}
	reply := func(code int, body string) *http.Response {
		return &http.Response{
			StatusCode: code,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	switch r.URL.Path {
	case "/oauth/token":
		vals, _ := url.ParseQuery(string(raw))
		s.calls = append(s.calls, map[string]any{
			"path":        "/oauth/token",
			"code":        vals.Get("code"),
			"redirectUri": vals.Get("redirect_uri"),
		})
		if s.fail {
			return reply(http.StatusInternalServerError, `{"error":"provider down"}`), nil
		}
		return reply(http.StatusOK, `{"access_token":"at-1"}`), nil
	case "/userinfo":
		if s.fail {
			return reply(http.StatusInternalServerError, `{"error":"provider down"}`), nil
		}
		var parsed map[string]any
		_ = json.Unmarshal([]byte(s.identity), &parsed)
		if orgID, ok := parsed["orgId"]; ok {
			parsed["organization_id"] = orgID
		}
		encoded, _ := json.Marshal(parsed)
		return reply(http.StatusOK, string(encoded)), nil
	case "/emails":
		var payload struct {
			To   []string `json:"to"`
			Text string   `json:"text"`
		}
		_ = json.Unmarshal(raw, &payload)
		to := ""
		if len(payload.To) > 0 {
			to = payload.To[0]
		}
		s.calls = append(s.calls, map[string]any{
			"path":  "/emails",
			"to":    to,
			"text":  payload.Text,
			"state": extractCallbackParam(payload.Text, "state"),
			"code":  extractCallbackParam(payload.Text, "code"),
		})
		if s.fail {
			return reply(http.StatusUnprocessableEntity, "sender not verified"), nil
		}
		return reply(http.StatusOK, `{"id":"em_1"}`), nil
	default:
		return reply(http.StatusNotFound, `{"error":"not found"}`), nil
	}
}

func extractCallbackParam(text, key string) string {
	for _, line := range strings.Split(text, string(byte(10))) {
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			if u, err := url.Parse(strings.TrimSpace(line)); err == nil {
				return u.Query().Get(key)
			}
		}
	}
	return ""
}

func newSSOFront(t *testing.T, status string, db *pgxpool.Pool) (*Front, *ssoWorker) {
	wk := &ssoWorker{}
	g := newGate(t)
	g.PublicURL = "https://neko.example"
	seedSignInSecrets(t, g.SecretsDir, status)
	g.DB = db
	return &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example", SecretsDir: g.SecretsDir, SignInClient: &http.Client{Transport: wk}},
	}, wk
}

func cookieFrom(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestAuthBegin(t *testing.T) {
	f, _ := newSSOFront(t, `{"provider":null}`, nil)
	if w := call(f, "GET", "/api/v1/auth/begin", "", nil); w.Code != 503 || w.Body.String() != `{"error":"sign-in is not set up"}` {
		t.Errorf("off: %d %s", w.Code, w.Body.String())
	}

	seedSignInSecrets(t, f.Gate.SecretsDir, `{"provider":{"pluginName":"scalekit","provisioning":"automatic"}}`)
	f.Gate.Reset()
	w := call(f, "GET", "/api/auth/begin?returnTo=%2Fchat%3Fx%3D1&loginHint=%20a%40example.com%20", "", nil)
	c := cookieFrom(w, StateCookie)
	if w.Code != 302 || c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != stateTTLSeconds || c.Path != "/" {
		t.Fatalf("begin %d %v", w.Code, w.Header())
	}
	state, path, ok := f.API.readAndClearState(httptest.NewRecorder(), &http.Request{Header: http.Header{"Cookie": {c.String()}}})
	if !ok || len(state) != 32 || path != "/chat?x=1" {
		t.Errorf("state cookie %q %q %v", state, path, ok)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Host != "env.scalekit.com" || loc.Path != "/oauth/authorize" ||
		loc.Query().Get("redirect_uri") != "https://neko.example/api/auth/callback" ||
		loc.Query().Get("login_hint") != "a@example.com" ||
		loc.Query().Get("state") != state {
		t.Errorf("location %q", w.Header().Get("Location"))
	}

	w = call(f, "GET", "/api/v1/auth/begin?returnTo=%2F%2Fevil.example", "", nil)
	loc2, _ := url.Parse(w.Header().Get("Location"))
	if _, path, _ := f.API.readAndClearState(httptest.NewRecorder(), &http.Request{Header: http.Header{"Cookie": {cookieFrom(w, StateCookie).String()}}}); path != "/" || loc2.Query().Has("login_hint") {
		t.Errorf("external return path %q, location %q", path, w.Header().Get("Location"))
	}

	if err := secrets.Write(secrets.Store{
		"signin": {"PROVIDER": "scalekit"},
		"signin.scalekit": {
			"SCALEKIT_ENVIRONMENT_URL": "http://insecure.scalekit.com",
			"SCALEKIT_CLIENT_ID":       "cid",
			"SCALEKIT_CLIENT_SECRET":   "sec",
		},
	}, f.Gate.SecretsDir); err != nil {
		t.Fatal(err)
	}
	f.Gate.Reset()
	if w := call(f, "GET", "/api/v1/auth/begin", "", nil); w.Code != 502 || !strings.Contains(decode(t, w)["error"].(string), "SCALEKIT_ENVIRONMENT_URL must be an https URL") {
		t.Errorf("begin failure %d %s", w.Code, w.Body.String())
	}
}

func TestAuthBeginManual(t *testing.T) {
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into app_user (id, org_id, email) values ('u1', 'org', 'Known@Example.com')`)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'u1', 'local' from user_group where org_id = 'org' and slug = 'administrators'`)
	seed(t, db, `insert into app_user (id, org_id, email, disabled_at) values ('u2', 'org', 'gone@example.com', now())`)
	f, wk := newSSOFront(t, `{"provider":{"pluginName":"magic-link","provisioning":"manual","loginHintRequired":true}}`, db)

	if w := call(f, "GET", "/api/v1/auth/begin", "", nil); w.Code != 302 || w.Header().Get("Location") != "/signin?error=enter+your+email+address+to+sign+in" {
		t.Errorf("no email: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, email := range []string{"stranger@example.com", "gone@example.com"} {
		w := call(f, "GET", "/api/v1/auth/begin?loginHint="+url.QueryEscape(email), "", nil)
		if w.Header().Get("Location") != "/signin?notice=link-sent" || cookieFrom(w, StateCookie) != nil {
			t.Errorf("%s: %q", email, w.Header().Get("Location"))
		}
	}
	if len(wk.calls) != 0 {
		t.Fatalf("the email provider got %d calls for unknown addresses", len(wk.calls))
	}
	w := call(f, "GET", "/api/v1/auth/begin?loginHint=known%40example.com", "", nil)
	if len(wk.calls) != 1 || w.Header().Get("Location") != "https://neko.example/signin?notice=link-sent" {
		t.Errorf("known address: %q, %d calls", w.Header().Get("Location"), len(wk.calls))
	}
	wk.fail = true
	if w := call(f, "GET", "/api/v1/auth/begin?loginHint=known%40example.com", "", nil); w.Code != 302 || w.Header().Get("Location") != "/signin?notice=link-sent" {
		t.Errorf("send failure: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestStateCookieFromNode(t *testing.T) {
	a := &API{Gate: newGate(t)}
	w := httptest.NewRecorder()
	state, path, ok := a.readAndClearState(w, &http.Request{Header: http.Header{"Cookie": {StateCookie + "=" + nodeStateCookie}}})
	if !ok || state != "abcState" || path != "/chat?x=1" {
		t.Fatalf("got %q %q %v", state, path, ok)
	}
	if c := cookieFrom(w, StateCookie); c == nil || c.MaxAge >= 0 {
		t.Error("the state cookie was not cleared")
	}
	if v, _ := a.stateCookieValue("abcState", "/chat?x=1"); v != nodeStateCookie {
		t.Errorf("Go wrote %q", v)
	}
	for _, bad := range []string{nodeStateCookie + "x", "abcState|L2NoYXQ_eD0x", ".x", "|x." + macOf(testSecret, "|x")} {
		if _, _, ok := a.readAndClearState(httptest.NewRecorder(), &http.Request{Header: http.Header{"Cookie": {StateCookie + "=" + bad}}}); ok {
			t.Errorf("%q passed", bad)
		}
	}
}

func signIn(t *testing.T, f *Front, wk *ssoWorker, identity string) *httptest.ResponseRecorder {
	t.Helper()
	begin := call(f, "GET", "/api/v1/auth/begin?returnTo=%2Fwork", "", nil)
	loc, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	wk.identity = identity
	return call(f, "GET", "/api/auth/callback?code=c1&state="+state, "", map[string]string{"Cookie": cookieFrom(begin, StateCookie).String()})
}

func TestAuthCallbackChecks(t *testing.T) {
	f, wk := newSSOFront(t, `{"provider":{"pluginName":"scalekit"}}`, nil)
	state := map[string]string{"Cookie": StateCookie + "=" + nodeStateCookie}
	for target, want := range map[string]string{
		"/api/v1/auth/callback?error=access_denied":                           "/signin?error=access_denied",
		"/api/v1/auth/callback?error=x&error_description=consent%20denied%21": "/signin?error=consent%20denied!",
		"/api/v1/auth/callback?code=c1":                                       "/signin?error=missing+code+or+state+on+callback",
		"/api/v1/auth/callback?code=c1&state=other":                           "/signin?error=state+mismatch",
	} {
		if w := call(f, "GET", target, "", state); w.Code != 302 || w.Header().Get("Location") != want {
			t.Errorf("%s: %d %q", target, w.Code, w.Header().Get("Location"))
		}
	}
	if w := call(f, "GET", "/api/v1/auth/callback?code=c1&state=abcState", "", nil); w.Header().Get("Location") != "/signin?error=state+cookie+missing+or+expired" {
		t.Errorf("no cookie: %q", w.Header().Get("Location"))
	}
	wk.fail = true
	w := call(f, "GET", "/api/v1/auth/callback?code=c1&state=abcState", "", state)
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/signin?error=Scalekit%20token%20exchange%20returned%20HTTP%20500") || cookieFrom(w, SessionCookie) != nil {
		t.Errorf("provider failure: %q", loc)
	}
	if wk.calls[0]["code"] != "c1" || wk.calls[0]["redirectUri"] != "https://neko.example/api/auth/callback" {
		t.Errorf("complete call %v", wk.calls[0])
	}
}

func TestAuthCallbackSignsIn(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into app_user (id, org_id, email, name) values ('old', 'org', 'Bo@Example.com', 'Bo')`)
	f, wk := newSSOFront(t, `{"provider":{"pluginName":"scalekit","provisioning":"automatic"}}`, db)

	w := signIn(t, f, wk, `{"sub":"sub-ada","email":"ada@example.com","name":"Ada","orgId":" tenant-1 ","groups":["Finance",{"id":" eng ","name":" Engineering "},{"id":"  "},"Finance"]}`)
	session := cookieFrom(w, SessionCookie)
	if w.Code != 302 || w.Header().Get("Location") != "/work" || session == nil || !session.Secure || session.MaxAge != sessionTTLSeconds {
		t.Fatalf("callback %d %v", w.Code, w.Header())
	}
	var ada, source string
	if err := db.QueryRow(ctx, `select id, source from app_user where sub = 'sub-ada'`).Scan(&ada, &source); err != nil || source != "scalekit" {
		t.Fatalf("user %q %q: %v", ada, source, err)
	}
	if f.Gate.SessionUserID(session.Value) != ada {
		t.Error("the session cookie does not verify")
	}
	if role := decode(t, call(f, "GET", "/api/v1/auth/session", "", map[string]string{"Cookie": session.String()}))["role"]; role != "admin" {
		t.Errorf("first user role %v", role)
	}
	var groups, audit string
	if err := db.QueryRow(ctx, `
		select string_agg(g.external_id || '=' || coalesce(g.display_name, '-') || '@' || g.tenant_id || '/' || g.provider, ',' order by g.external_id collate "C"),
		       (select array_to_string(external_group_ids, ',') from sso_group_sync_audit where user_id = $1)
		from sso_group_membership m join sso_group g on g.id = m.group_id where m.user_id = $1`, ada).Scan(&groups, &audit); err != nil {
		t.Fatal(err)
	}
	if groups != "Finance=Finance@tenant-1/scalekit" || audit != "Finance" {
		t.Errorf("groups %q audit %q", groups, audit)
	}

	seed(t, db, `insert into user_group (org_id, slug, name, kind) values ('org', 'finance', 'Finance', 'custom')`)
	seed(t, db, `insert into idp_group_rule (org_id, sso_group_id, user_group_id)
		select 'org', s.id, g.id from sso_group s, user_group g where s.external_id = 'Finance' and g.slug = 'finance'`)
	w = signIn(t, f, wk, `{"sub":"sub-bo","email":"bo@example.com","orgId":"tenant-1","groups":["Finance"]}`)
	var bo string
	var name *string
	if err := db.QueryRow(ctx, `select id, name from app_user where sub = 'sub-bo'`).Scan(&bo, &name); err != nil || bo != "old" || name == nil || *name != "Bo" {
		t.Fatalf("email match %q %v: %v", bo, name, err)
	}
	inGroup := func(user, slug string) bool {
		var in bool
		_ = db.QueryRow(ctx, `select exists (select 1 from user_group_membership m join user_group g on g.id = m.group_id
			where m.user_id = $1 and g.slug = $2)`, user, slug).Scan(&in)
		return in
	}
	if !inGroup("old", "finance") || inGroup("old", "administrators") {
		t.Error("rule membership or admin bootstrap is wrong for the second user")
	}
	signIn(t, f, wk, `{"sub":"sub-bo","email":"bo2@example.com","name":null,"orgId":"tenant-1","groups":[]}`)
	var email string
	_ = db.QueryRow(ctx, `select email, name from app_user where id = 'old'`).Scan(&email, &name)
	if email != "bo2@example.com" || name != nil || inGroup("old", "finance") {
		t.Errorf("subject match: %q %v, finance %v", email, name, inGroup("old", "finance"))
	}

	w = signIn(t, f, wk, `{"sub":"sub-other","email":"ADA@example.com"}`)
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/signin?error=app_user.email%20ADA%40example.com%20is%20already%20bound") || cookieFrom(w, SessionCookie) != nil {
		t.Errorf("bound email: %q", loc)
	}
	if w := signIn(t, f, wk, `{"sub":"","email":"x@example.com"}`); !strings.HasPrefix(w.Header().Get("Location"), "/signin?error=Scalekit%20userinfo%20returned%20no%20sub") {
		t.Errorf("no subject: %q", w.Header().Get("Location"))
	}
}

func TestSignInKeepsLastAdministrator(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	f, wk := newSSOFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	signIn(t, f, wk, `{"sub":"s1","email":"a@example.com","groups":["Admins"]}`)
	seed(t, db, `insert into idp_group_rule (org_id, sso_group_id, user_group_id)
		select 'org', s.id, g.id from sso_group s, user_group g where s.external_id = 'Admins' and g.slug = 'administrators'`)
	signIn(t, f, wk, `{"sub":"s1","email":"a@example.com","groups":["Admins"]}`)
	seed(t, db, `delete from user_group_membership where source = 'local'`)
	w := signIn(t, f, wk, `{"sub":"s1","email":"a@example.com","groups":[]}`)
	var sources string
	_ = db.QueryRow(ctx, `select string_agg(source, ',') from user_group_membership`).Scan(&sources)
	if w.Header().Get("Location") != "/work" || !strings.HasPrefix(sources, "rule:") {
		t.Errorf("lockout: %q, memberships %q", w.Header().Get("Location"), sources)
	}
}

func TestManualProvisioningRefusesNewUser(t *testing.T) {
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into app_user (id, org_id, email) values ('u1', 'org', 'known@example.com')`)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'u1', 'local' from user_group where org_id = 'org' and slug = 'administrators'`)
	f, wk := newSSOFront(t, `{"provider":{"pluginName":"magic-link","provisioning":"manual"}}`, db)
	begin := call(f, "GET", "/api/v1/auth/begin?loginHint=known%40example.com", "", nil)
	state := wk.calls[0]["state"].(string)
	token, err := mintMagicLinkToken("new@example.com", state, strings.Repeat("k", 64), 600, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := call(f, "GET", "/api/v1/auth/callback?code="+token+"&state="+state, "", map[string]string{"Cookie": cookieFrom(begin, StateCookie).String()})
	if loc := w.Header().Get("Location"); loc != "/signin?error="+encodeURIComponent("no user is provisioned for new@example.com; ask an administrator to add you") {
		t.Errorf("got %q", loc)
	}
	var n int
	_ = db.QueryRow(context.Background(), `select count(*) from app_user`).Scan(&n)
	if n != 1 {
		t.Errorf("%d users", n)
	}
}
