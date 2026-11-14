package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type fakeScalekitDirectory struct {
	mu      sync.Mutex
	tokens  int
	calls   []string
	created []string
	badAuth bool
}

func (f *fakeScalekitDirectory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/oauth/token" {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_secret") != "sec" {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
			return
		}
		f.tokens++
		_, _ = io.WriteString(w, `{"access_token":"tok"}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		f.badAuth = true
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+r.URL.RawQuery)
	const org = "/api/v1/organizations/o1"
	page := r.URL.Query().Get("page_token")
	switch r.Method + " " + r.URL.Path {
	case "GET " + org + "/directories":
		_, _ = io.WriteString(w, `{"directories":[{"id":"d2"},{"id":"d1","enabled":true},{"id":"dx","enabled":false}]}`)
	case "GET " + org + "/directories/d1/groups":
		if page == "" {
			_, _ = io.WriteString(w, `{"groups":[{"id":"g1","display_name":"Ops"}],"next_page_token":"p2"}`)
		} else {
			_, _ = io.WriteString(w, `{"groups":[{"id":"g2"},{"display_name":"  "}]}`)
		}
	case "GET " + org + "/directories/d1/users":
		_, _ = io.WriteString(w, `{"users":[
			{"id":"u1","email":"A@Example.com"},
			{"id":"u2","email":"new@example.com","given_name":"Nia","family_name":"Wu","groups":[{"display_name":"Ops"},{"id":"g2"},{"id":"unknown"}]},
			{"id":"u3","email":"gone@example.com","user_detail":{"active":false}},
			{"id":"u5","email":"back@example.com","userDetail":{"active":true}},
			{"id":"","email":"noid@example.com"}
		]}`)
	case "GET " + org + "/directories/d2/groups":
		_, _ = io.WriteString(w, `{"groups":[]}`)
	case "GET " + org + "/directories/d2/users":
		_, _ = io.WriteString(w, `{"users":[{"id":"u4","emails":["Four@example.com"],"preferredUsername":"four"}]}`)
	case "POST " + org + "/users":
		raw, _ := io.ReadAll(r.Body)
		f.created = append(f.created, string(raw)+" "+r.URL.RawQuery)
		if strings.Contains(string(raw), "fail@") {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_argument","error_description":"email taken"}`)
			return
		}
		var body struct {
			Email string `json:"email"`
		}
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"user":{"id":"sk_1","email":"`+body.Email+`","user_profile":{"name":"Dee"}}}`)
	default:
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":"not_found"}`)
	}
}

func scalekitDirectoryFront(t *testing.T, orgID string) (*Front, *fakeScalekitDirectory, *setupWorker) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, wk := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	fake := &fakeScalekitDirectory{}
	srv := httptest.NewTLSServer(fake)
	t.Cleanup(srv.Close)
	f.API.SignInClient = srv.Client()
	f.API.Gate.WorkerURL = "http://127.0.0.1:1"
	sec := map[string]string{
		"SCALEKIT_ENVIRONMENT_URL": srv.URL,
		"SCALEKIT_CLIENT_ID":       "cid",
		"SCALEKIT_CLIENT_SECRET":   "sec",
	}
	if orgID != "" {
		sec["SCALEKIT_ORGANIZATION_ID"] = orgID
	}
	if err := secrets.Write(secrets.Store{"signin": {"PROVIDER": "scalekit"}, "signin.scalekit": sec}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	return f, fake, wk
}

func TestAdminDirectorySync(t *testing.T) {
	f, fake, wk := scalekitDirectoryFront(t, "o1")
	db := f.API.DB
	admin := sessionCookie("admin")
	seed(t, db, `insert into app_user (id, org_id, email, source) values ('old', 'org', 'old@example.com', 'scalekit')`)
	seed(t, db, `insert into app_user (id, org_id, email, source, disabled_at) values ('back', 'org', 'back@example.com', 'scalekit', now())`)
	seed(t, db, `insert into sso_group (org_id, provider, tenant_id, external_id, display_name) values ('org', 'scalekit', 'o1', 'Stale', 'Stale')`)

	w := call(f, "GET", "/api/v1/admin/directory", "", admin)
	if w.Code != 200 || w.Body.String() != `{"provider":{"pluginName":"scalekit","providerLabel":"Scalekit directory","canCreateUsers":true,"canDeactivateUsers":false},"state":{"status":"never","startedAt":null,"finishedAt":null,"lastError":null,"stats":{}}}` {
		t.Errorf("status before: %d %s", w.Code, w.Body.String())
	}

	w = call(f, "POST", "/api/v1/admin/directory", "", admin)
	if w.Code != 200 || w.Body.String() != `{"stats":{"usersCreated":2,"usersDisabled":1,"usersEnabled":1,"usersMatched":2,"groups":2,"groupsDeactivated":1,"memberships":2}}` {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	if fake.badAuth {
		t.Error("an API call had no bearer token")
	}
	if fake.tokens == 0 {
		t.Error("no token request")
	}
	if len(wk.calls) != 0 {
		t.Errorf("worker calls: %v", wk.calls)
	}

	var name, source string
	var disabled bool
	if err := db.QueryRow(t.Context(), `select name, source, disabled_at is not null from app_user where email = 'new@example.com'`).Scan(&name, &source, &disabled); err != nil || name != "Nia Wu" || source != "scalekit" || disabled {
		t.Errorf("created user: %v %q %q %v", err, name, source, disabled)
	}
	if err := db.QueryRow(t.Context(), `select name from app_user where email = 'four@example.com'`).Scan(&name); err != nil || name != "four" {
		t.Errorf("second directory user: %v %q", err, name)
	}
	var n int
	_ = db.QueryRow(t.Context(), `select count(*) from app_user where email = 'gone@example.com'`).Scan(&n)
	if n != 0 {
		t.Errorf("inactive user created")
	}
	for id, want := range map[string]bool{"old": true, "back": false, "admin": false, "member": false} {
		_ = db.QueryRow(t.Context(), `select disabled_at is not null from app_user where id = $1`, id).Scan(&disabled)
		if disabled != want {
			t.Errorf("%s disabled = %v", id, disabled)
		}
	}
	rows, _ := db.Query(t.Context(), `select g.external_id from sso_group_membership m join sso_group g on g.id = m.group_id
		join app_user u on u.id = m.user_id where u.email = 'new@example.com' order by g.external_id collate "C"`)
	var groups []string
	for rows.Next() {
		var g string
		_ = rows.Scan(&g)
		groups = append(groups, g)
	}
	rows.Close()
	if strings.Join(groups, ",") != "Ops,g2" {
		t.Errorf("memberships: %v", groups)
	}
	var active bool
	_ = db.QueryRow(t.Context(), `select active from sso_group where external_id = 'Stale'`).Scan(&active)
	if active {
		t.Error("stale group still active")
	}

	w = call(f, "GET", "/api/v1/admin/directory", "", admin)
	state, _ := decode(t, w)["state"].(map[string]any)
	if state["status"] != "ok" || state["startedAt"] == nil || state["finishedAt"] == nil || state["lastError"] != nil ||
		state["stats"].(map[string]any)["usersCreated"] != float64(2) {
		t.Errorf("status after: %s", w.Body.String())
	}

	seed(t, db, `update directory_sync_state set status = 'running', started_at = now()`)
	if w := call(f, "POST", "/api/v1/admin/directory", "", admin); w.Code != 409 || w.Body.String() != `{"error":"a directory sync is already running","code":"running"}` {
		t.Errorf("running: %d %s", w.Code, w.Body.String())
	}
	if err := f.API.DirectorySync(t.Context()); err == nil || err.Error() != "directory sync failed: a directory sync is already running" {
		t.Errorf("job while running: %v", err)
	}
	seed(t, db, `update directory_sync_state set started_at = now() - interval '2 hours'`)
	if err := f.API.DirectorySync(t.Context()); err != nil {
		t.Errorf("job: %v", err)
	}
}

func TestAdminDirectoryFailures(t *testing.T) {
	f, _, wk := scalekitDirectoryFront(t, "")
	admin := sessionCookie("admin")
	w := call(f, "POST", "/api/v1/admin/directory", "", admin)
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"error":"SCALEKIT_ORGANIZATION_ID not set`) || !strings.Contains(w.Body.String(), `"code":null`) {
		t.Errorf("no organization: %d %s", w.Code, w.Body.String())
	}
	var status, lastError string
	_ = f.API.DB.QueryRow(t.Context(), `select status, last_error from directory_sync_state`).Scan(&status, &lastError)
	if status != "failed" || !strings.HasPrefix(lastError, "SCALEKIT_ORGANIZATION_ID not set") {
		t.Errorf("state: %q %q", status, lastError)
	}

	if err := secrets.Write(secrets.Store{"signin": {"PROVIDER": "magic-link"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	w = call(f, "GET", "/api/v1/admin/directory", "", admin)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"provider":null,"state":{"status":"failed",`) {
		t.Errorf("status with no provider: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/admin/directory", "", admin); w.Code != 404 || w.Body.String() != `{"error":"no directory provider is configured","code":"no_provider"}` {
		t.Errorf("sync with no provider: %d %s", w.Code, w.Body.String())
	}
	if err := f.API.DirectorySync(t.Context()); err != nil {
		t.Errorf("job with no provider: %v", err)
	}
	if len(wk.calls) != 0 {
		t.Errorf("worker calls: %v", wk.calls)
	}
}

func TestAdminUserCreateInDirectory(t *testing.T) {
	f, fake, wk := scalekitDirectoryFront(t, "o1")
	admin := sessionCookie("admin")
	w := call(f, "POST", "/api/v1/admin/users", `{"email":"dir@example.com","name":" Dee ","role":"member","addToDirectory":true}`, admin)
	if w.Code != 201 || strings.Contains(w.Body.String(), "directoryError") {
		t.Errorf("create: %d %s", w.Code, w.Body.String())
	}
	if len(fake.created) != 1 || fake.created[0] != `{"email":"dir@example.com","user_profile":{"name":"Dee"}} send_invitation_email=true` {
		t.Errorf("created: %v", fake.created)
	}
	w = call(f, "POST", "/api/v1/admin/users", `{"email":"fail@example.com","role":"member","addToDirectory":true}`, admin)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"directoryError":"Scalekit create organization user returned HTTP 400 (invalid_argument: email taken)"`) {
		t.Errorf("directory failure: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "tok") || strings.Contains(w.Body.String(), `"sec"`) {
		t.Errorf("secret in response: %s", w.Body.String())
	}
	if len(wk.calls) != 0 {
		t.Errorf("worker calls: %v", wk.calls)
	}

	if err := secrets.Write(secrets.Store{"signin": {"PROVIDER": "magic-link"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.API.CreateDirectoryUser(t.Context(), "x@example.com", nil); err == nil || err.Error() != "no directory provider is configured" {
		t.Errorf("no provider: %v", err)
	}
	if _, err := f.API.CreateDirectoryUser(t.Context(), "nope", nil); err == nil || err.Error() != "a valid email address is required" {
		t.Errorf("bad email: %v", err)
	}
}

type readOnlyDirectory struct{}

func (readOnlyDirectory) GetDirectoryProvider() *DirectoryProviderInfo {
	return &DirectoryProviderInfo{ID: "ro", Capabilities: DirectoryCapabilities{ProviderLabel: "Read-only directory"}}
}

func (readOnlyDirectory) ListDirectory(context.Context, *string) (ListDirectoryResult, error) {
	next := "again"
	return ListDirectoryResult{TenantID: "t", NextCursor: &next}, nil
}

func (readOnlyDirectory) ApplyDirectoryChange(context.Context, DirectoryChange) (ApplyDirectoryChangeResult, error) {
	return ApplyDirectoryChangeResult{}, nil
}

func TestDirectorySourceLimits(t *testing.T) {
	a := &API{DirectorySource: readOnlyDirectory{}}
	_, err := a.CreateDirectoryUser(t.Context(), "x@example.com", nil)
	var de *DirectorySyncError
	if !errors.As(err, &de) || de.Code != "unsupported" || err.Error() != "Read-only directory does not create users" {
		t.Errorf("create: %v", err)
	}
	snap, err := CollectDirectorySnapshot(t.Context(), readOnlyDirectory{}, "org")
	if err == nil || err.Error() != "directory provider returned more than 500 pages" || snap.OrgID != "" {
		t.Errorf("pages: %v", err)
	}
	if _, err := CollectDirectorySnapshot(t.Context(), nil, "org"); err == nil || err.Error() != "no directory provider is configured" {
		t.Errorf("nil source: %v", err)
	}
}

func TestScalekitDirectoryCursor(t *testing.T) {
	for _, raw := range []string{"%%%", "e30", encodeScalekitDirectoryCursor(scalekitDirectoryCursor{Directory: 0, Phase: "members"})} {
		if _, err := decodeScalekitDirectoryCursor(&raw); err == nil || err.Error() != "directory cursor is not valid" {
			t.Errorf("%q: %v", raw, err)
		}
	}
	in := scalekitDirectoryCursor{Directory: 2, Phase: "users", PageToken: "p"}
	enc := encodeScalekitDirectoryCursor(in)
	if out, err := decodeScalekitDirectoryCursor(&enc); err != nil || out != in {
		t.Errorf("round trip: %v %v", out, err)
	}
	dir, err := newScalekitDirectory(scalekitDirectoryOptions{EnvironmentURL: "https://env.example", OrganizationID: "o1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Apply(t.Context(), DirectoryChange{Op: "deactivate_user", ExternalID: "u1"}); err == nil ||
		err.Error() != `Scalekit directory does not support "deactivate_user"; the identity provider owns user status` {
		t.Errorf("deactivate: %v", err)
	}
	if _, err := newScalekitDirectory(scalekitDirectoryOptions{EnvironmentURL: "http://env.example", OrganizationID: "o1"}); err == nil {
		t.Error("http environment URL accepted")
	}
}
