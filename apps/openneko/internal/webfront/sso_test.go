package webfront

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const nodeStateCookie = "abcState|L2NoYXQ_eD0x.PW77raE5KABLed-9mPUcO_mSWYfRSFrmM9H9xqsXucA"

type ssoWorker struct {
	mu       sync.Mutex
	status   string
	identity string
	fail     bool
	calls    []map[string]any
}

func (s *ssoWorker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/admin/auth/status" {
		_, _ = w.Write([]byte(s.status))
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	body["path"] = r.URL.Path
	s.calls = append(s.calls, body)
	if s.fail {
		http.Error(w, `{"error":"provider down"}`, http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/admin/auth/begin":
		_, _ = w.Write([]byte(`{"authorizationUrl":"https://idp.example/authorize?state=` + body["state"].(string) + `"}`))
	case "/admin/auth/complete":
		_, _ = w.Write([]byte(`{"identity":` + s.identity + `}`))
	default:
		http.NotFound(w, r)
	}
}

func newSSOFront(t *testing.T, status string, db *pgxpool.Pool) (*Front, *ssoWorker) {
	wk := &ssoWorker{status: status}
	srv := httptest.NewServer(wk)
	t.Cleanup(srv.Close)
	g := newGate(t, srv.URL)
	g.DB = db
	return &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example", SecureCookies: true},
		Node: &recorder{},
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
	f, wk := newSSOFront(t, `{"provider":null}`, nil)
	if w := call(f, "GET", "/api/v1/auth/begin", "", nil); w.Code != 503 || w.Body.String() != `{"error":"sign-in is not set up"}` {
		t.Errorf("off: %d %s", w.Code, w.Body.String())
	}

	wk.status = `{"provider":{"pluginName":"scalekit","provisioning":"automatic"}}`
	f.Gate.provider = nil
	w := call(f, "GET", "/api/auth/begin?returnTo=%2Fchat%3Fx%3D1&loginHint=%20a%40example.com%20", "", nil)
	c := cookieFrom(w, StateCookie)
	if w.Code != 302 || c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != stateTTLSeconds || c.Path != "/" {
		t.Fatalf("begin %d %v", w.Code, w.Header())
	}
	call0 := wk.calls[0]
	if call0["redirectUri"] != "https://neko.example/api/auth/callback" || call0["loginHint"] != "a@example.com" ||
		w.Header().Get("Location") != "https://idp.example/authorize?state="+call0["state"].(string) {
		t.Errorf("worker call %v, location %q", call0, w.Header().Get("Location"))
	}
	state, path, ok := f.API.readAndClearState(httptest.NewRecorder(), &http.Request{Header: http.Header{"Cookie": {c.String()}}})
	if !ok || state != call0["state"] || len(state) != 32 || path != "/chat?x=1" {
		t.Errorf("state cookie %q %q %v", state, path, ok)
	}

	w = call(f, "GET", "/api/v1/auth/begin?returnTo=%2F%2Fevil.example", "", nil)
	if _, path, _ := f.API.readAndClearState(httptest.NewRecorder(), &http.Request{Header: http.Header{"Cookie": {cookieFrom(w, StateCookie).String()}}}); path != "/" || wk.calls[1]["loginHint"] != nil {
		t.Errorf("external return path %q, hint %v", path, wk.calls[1]["loginHint"])
	}

	wk.fail = true
	if w := call(f, "GET", "/api/v1/auth/begin", "", nil); w.Code != 502 || !strings.HasPrefix(decode(t, w)["error"].(string), "sign-in begin failed (500): ") {
		t.Errorf("worker failure %d %s", w.Code, w.Body.String())
	}
}

func TestAuthBeginManual(t *testing.T) {
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into app_user (id, org_id, email) values ('u1', 'org', 'Known@Example.com')`)
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
		t.Fatalf("the worker got %d calls for unknown addresses", len(wk.calls))
	}
	w := call(f, "GET", "/api/v1/auth/begin?loginHint=known%40example.com", "", nil)
	if len(wk.calls) != 1 || w.Header().Get("Location") == "/signin?notice=link-sent" {
		t.Errorf("known address: %q, %d calls", w.Header().Get("Location"), len(wk.calls))
	}
	wk.fail = true
	if w := call(f, "GET", "/api/v1/auth/begin?loginHint=known%40example.com", "", nil); w.Code != 302 || w.Header().Get("Location") != "/signin?notice=link-sent" {
		t.Errorf("send failure: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestStateCookieFromNode(t *testing.T) {
	a := &API{Gate: newGate(t, "")}
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
	state := wk.calls[len(wk.calls)-1]["state"].(string)
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
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/signin?error=sign-in%20complete%20failed%20(500)") || cookieFrom(w, SessionCookie) != nil {
		t.Errorf("worker failure: %q", loc)
	}
	if wk.calls[0]["code"] != "c1" || wk.calls[0]["state"] != "abcState" || wk.calls[0]["redirectUri"] != "https://neko.example/api/auth/callback" {
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
	if groups != "Finance=Finance@tenant-1/scalekit,eng=Engineering@tenant-1/scalekit" || audit != "Finance,eng" {
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
	if w := signIn(t, f, wk, `{"sub":"","email":"x@example.com"}`); !strings.HasPrefix(w.Header().Get("Location"), "/signin?error=the%20sign-in%20provider%20gave%20no") {
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
	f, wk := newSSOFront(t, `{"provider":{"pluginName":"magic-link","provisioning":"manual"}}`, db)
	begin := call(f, "GET", "/api/v1/auth/begin?loginHint=known%40example.com", "", nil)
	wk.identity = `{"sub":"ml:new","email":"new@example.com"}`
	w := call(f, "GET", "/api/v1/auth/callback?code=c&state="+wk.calls[0]["state"].(string), "", map[string]string{"Cookie": cookieFrom(begin, StateCookie).String()})
	if loc := w.Header().Get("Location"); loc != "/signin?error="+encodeURIComponent("no user is provisioned for new@example.com; ask an administrator to add you") {
		t.Errorf("got %q", loc)
	}
	var n int
	_ = db.QueryRow(context.Background(), `select count(*) from app_user`).Scan(&n)
	if n != 1 {
		t.Errorf("%d users", n)
	}
}
