package webfront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestCanonicalJSONMatchesNode(t *testing.T) {
	got, err := canonicalJSON(map[string]any{
		"userId":     "usr_1",
		"deviceName": "Ana's \"phone\"\n\u0001<é> 📱",
		"nested":     map[string]any{"b": []any{1, true, nil}, "a": "x"},
	})
	want := `{"deviceName":"Ana's \"phone\"\n\u0001<é> 📱","nested":{"a":"x","b":[1,true,null]},"userId":"usr_1"}`
	if err != nil || got != want {
		t.Fatalf("got %s %v", got, err)
	}
	payload := sha256Hex(got)
	if payload != "2ec243b15c7f1240d837c2c71c9ba4b6af3e497e70bc45b81cea08d04a032549" {
		t.Errorf("payload hash %s", payload)
	}
	if chain := sha256Hex(genesisHash + "\x001\x00" + payload); chain != "64da77a9a146cc0ff7bce14765dffd6959fa15ec0c7c2b6320a5677d8bf783bb" {
		t.Errorf("chain hash %s", chain)
	}
}

func TestGoRoutesInContract(t *testing.T) {
	raw, err := os.ReadFile("../../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	api := &API{}
	for route, methods := range GoRoutes {
		held := 0
		for _, h := range api.table()[route] {
			if h != nil {
				held++
			}
		}
		if held != len(methods) {
			t.Errorf("%s: GoRoutes and the handler table differ", route)
		}
		for _, m := range methods {
			if _, ok := spec.Paths[strings.TrimPrefix(route, "/api/v1")][strings.ToLower(m)]; !ok {
				t.Errorf("%s %s is not in api/openapi.yaml", m, route)
			}
			if api.table()[route][m] == nil {
				t.Errorf("%s %s has no handler", m, route)
			}
		}
	}
}

func TestNormalizeDeviceName(t *testing.T) {
	for in, want := range map[any]string{
		nil:                           "Unnamed device",
		42:                            "Unnamed device",
		" \u00a0\ufeff ":              "Unnamed device",
		"  My\t\n phone\u2003 ":       "My phone",
		strings.Repeat("ab ", 40):     strings.Repeat("ab ", 26) + "ab",
		strings.Repeat("📱", 41):       strings.Repeat("📱", 40),
		"x" + strings.Repeat("📱", 41): "x" + strings.Repeat("📱", 39),
	} {
		if got := normalizeDeviceName(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
	for in, want := range map[any]string{"bcdf-ghjk": "BCDFGHJK", " BCDF GHJK ": "BCDFGHJK", "BCDFGHJA": "", "BCD": "", 7: ""} {
		if got := normalizeUserCode(in); got != want {
			t.Errorf("user code %q: got %q", in, got)
		}
	}
}

type recorder struct{ hits []string }

func (n *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.hits = append(n.hits, r.Method+" "+r.URL.Path)
	w.WriteHeader(http.StatusTeapot)
}

func newAPIFront(t *testing.T, workerBody string, db *pgxpool.Pool) (*Front, *recorder) {
	g := newGate(t, newWorker(t, workerBody).srv.URL)
	g.DB = db
	node := &recorder{}
	return &Front{
		App:  fstest.MapFS{"index.html": {Data: []byte("app")}},
		Gate: g,
		API:  &API{Gate: g, DB: db, Version: "5.0.0", PublicURL: "https://neko.example/"},
		Node: node,
	}, node
}

func call(f http.Handler, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %q: %v", w.Code, w.Body.String(), err)
	}
	return v
}

func TestAPIRouting(t *testing.T) {
	f, node := newAPIFront(t, `{"provider":null}`, nil)
	for _, p := range []string{"/api/v1/version", "/api/version"} {
		w := call(f, "GET", p, "", nil)
		if w.Code != 200 || w.Body.String() != `{"version":"5.0.0"}` || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q %q", p, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
	}
	if w := call(f, "HEAD", "/api/v1/version", "", nil); w.Code != 200 {
		t.Errorf("HEAD: %d", w.Code)
	}
	if w := call(f, "POST", "/api/v1/version", "", nil); w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Errorf("POST: %d %q", w.Code, w.Header().Get("Allow"))
	}
	for _, p := range []string{"/api/v1/auth/sso", "/api/v1/version/", "/api/v1/work"} {
		call(f, "GET", p, "", nil)
	}
	if strings.Join(node.hits, ",") != "GET /api/v1/auth/sso,GET /api/v1/version/,GET /api/v1/work" {
		t.Errorf("Node got %v", node.hits)
	}
}

func TestAPIVersionFile(t *testing.T) {
	dir := t.TempDir()
	if got := AppVersion(dir, "dev"); got != "dev" {
		t.Errorf("no file: %q", got)
	}
	if err := os.WriteFile(dir+"/version.json", []byte(`{"version":"5.1.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := AppVersion(dir, "dev"); got != "5.1.0" {
		t.Errorf("file: %q", got)
	}
}

func TestContextRemoteRoutesGone(t *testing.T) {
	a := &API{}
	for _, c := range [][2]string{
		{"GET", "/api/v1/admin/context-remote"}, {"PUT", "/api/v1/admin/context-remote"}, {"DELETE", "/api/v1/admin/context-remote"},
		{"POST", "/api/v1/admin/context-remote/host-key"},
		{"GET", "/api/v1/admin/context-remote/publish"}, {"POST", "/api/v1/admin/context-remote/publish"},
		{"GET", "/api/v1/admin/context-remote/updates"}, {"POST", "/api/v1/admin/context-remote/updates"},
	} {
		if _, ok := GoRoutes[c[1]]; ok {
			t.Errorf("%s is still in GoRoutes", c[1])
		}
		if a.Handler(httptest.NewRequest(c[0], c[1], nil)) != nil {
			t.Errorf("%s %s still has a handler", c[0], c[1])
		}
	}
}

func TestAuthStatusRoute(t *testing.T) {
	f, _ := newAPIFront(t, `{"provider":{"pluginName":"magic-link","providerLabel":"Email link","provisioning":"manual"},"pending":null}`, nil)
	w := call(f, "GET", "/api/v1/auth/status", "", nil)
	if w.Body.String() != `{"pending":null,"provider":{"pluginName":"magic-link","providerLabel":"Email link","provisioning":"manual"}}` {
		t.Errorf("live: %q", w.Body.String())
	}
	raw, _ := os.ReadFile(f.Gate.MarkerPath)
	if string(raw) != `{"provider":{"pluginName":"magic-link","providerLabel":"Email link","provisioning":"manual"}}` {
		t.Errorf("marker %q", raw)
	}

	f, _ = newAPIFront(t, `{"provider":null,"pending":{"pluginName":"scalekit","providerLabel":"SSO","missingEnv":["SCALEKIT_CLIENT_SECRET"]}}`, nil)
	if w := call(f, "GET", "/api/v1/auth/status", "", nil); w.Body.String() != `{"pending":{"providerLabel":"SSO"},"provider":null}` {
		t.Errorf("pending: %q", w.Body.String())
	}

	down := newGate(t, "http://127.0.0.1:1")
	if err := os.WriteFile(down.MarkerPath, []byte(`{"provider":{"pluginName":"magic-link"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.Gate, f.API.Gate = down, down
	if w := call(f, "GET", "/api/v1/auth/status", "", nil); w.Body.String() != `{"pending":null,"provider":{"pluginName":"magic-link"}}` {
		t.Errorf("worker down: %q", w.Body.String())
	}
}

func sessionCookie(userID string) map[string]string {
	body := userID + "." + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(body))
	return map[string]string{"Cookie": SessionCookie + "=" + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}
}

func seed(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestSoloSession(t *testing.T) {
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into work_thread (id, org_id, channel, title) values ('00000000-0000-4000-8000-000000000001', 'org', 'web', 'old chat')`)
	f, _ := newAPIFront(t, `{"provider":null}`, db)
	w := call(f, "GET", "/api/v1/auth/session", "", nil)
	got := decode(t, w)
	u, _ := got["user"].(map[string]any)
	if u == nil || u["email"] != "" || u["name"] != "Solo administrator" || got["role"] != "admin" || got["authEnabled"] != false || got["signedIn"] != false {
		t.Fatalf("solo session %s", w.Body.String())
	}
	id := u["id"].(string)
	var owner, threadOwner string
	if err := db.QueryRow(context.Background(), `select solo_admin_user_id, (select created_by_user_id from work_thread where id = '00000000-0000-4000-8000-000000000001') from organization`).Scan(&owner, &threadOwner); err != nil || owner != id || threadOwner != id {
		t.Errorf("owner %q thread %q, want %q: %v", owner, threadOwner, id, err)
	}
	if again := decode(t, call(f, "GET", "/api/v1/auth/session", "", nil)); again["user"].(map[string]any)["id"] != id {
		t.Error("a second call made another solo administrator")
	}
}

func TestDeviceSignInThroughGo(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	seed(t, db, `insert into app_user (id, org_id, email, name) values ('admin', 'org', 'a@example.com', 'Ada'), ('member', 'org', 'm@example.com', null)`)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'admin', 'local' from user_group where org_id = 'org' and slug = 'administrators'`)
	f, _ := newAPIFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)

	anon := decode(t, call(f, "GET", "/api/v1/auth/session", "", nil))
	if anon["user"] != nil || anon["role"] != nil || anon["authEnabled"] != true || anon["signedIn"] != false {
		t.Errorf("anonymous session %v", anon)
	}

	w := call(f, "POST", "/api/v1/auth/device/code", `{"name":"  Ada's   phone "}`, nil)
	started := decode(t, w)
	code, userCode := started["deviceCode"].(string), started["userCode"].(string)
	if w.Code != 200 || len(code) != 43 || len(userCode) != 9 || started["verificationUri"] != "https://neko.example/device" ||
		started["verificationUriComplete"] != "https://neko.example/device?code="+userCode || started["interval"] != float64(5) {
		t.Fatalf("device code %d %s", w.Code, w.Body.String())
	}
	if e := started["expiresIn"].(float64); e < 598 || e > 600 {
		t.Errorf("expiresIn %v", e)
	}

	poll := func() *httptest.ResponseRecorder {
		return call(f, "POST", "/api/v1/auth/device/token", `{"deviceCode":"`+code+`"}`, nil)
	}
	if w := poll(); w.Code != 400 || decode(t, w)["error"] != "authorization_pending" {
		t.Errorf("first poll %d %s", w.Code, w.Body.String())
	}
	if w := poll(); decode(t, w)["error"] != "slow_down" {
		t.Errorf("fast poll %s", w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/auth/device/token", `nope`, nil); w.Code != 400 || w.Body.String() != `{"error":"invalid_request","message":"deviceCode is required"}` {
		t.Errorf("bad body %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/auth/device/token", `{"deviceCode":"unknown"}`, nil); decode(t, w)["error"] != "expired_token" {
		t.Errorf("unknown code %s", w.Body.String())
	}

	if w := call(f, "POST", "/api/v1/auth/device/approve", `{"userCode":"`+userCode+`","approve":true}`, nil); w.Code != 401 {
		t.Errorf("approve without a session: %d", w.Code)
	}
	admin := sessionCookie("admin")
	if w := call(f, "POST", "/api/v1/auth/device/approve", `{"userCode":"`+userCode+`","approve":"yes"}`, admin); w.Code != 400 {
		t.Errorf("approve not a boolean: %d", w.Code)
	}
	if w := call(f, "POST", "/api/v1/auth/device/approve", `{"userCode":"BCDF-GHJK","approve":true}`, admin); w.Code != 404 {
		t.Errorf("unknown user code: %d", w.Code)
	}
	w = call(f, "POST", "/api/v1/auth/device/approve", `{"userCode":"`+strings.ToLower(userCode)+`","approve":true}`, admin)
	if w.Code != 200 || w.Body.String() != `{"approved":true,"name":"Ada's phone","ok":true}` {
		t.Fatalf("approve %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/auth/device/approve", `{"userCode":"`+userCode+`","approve":true}`, admin); w.Code != 404 {
		t.Errorf("second approval: %d", w.Code)
	}

	seed(t, db, `update app_device_request set last_polled_at = null`)
	w = poll()
	issued := decode(t, w)
	token, _ := issued["token"].(string)
	if w.Code != 200 || !deviceToken.MatchString(token) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issue %d %s", w.Code, w.Body.String())
	}
	if w := poll(); decode(t, w)["error"] != "expired_token" {
		t.Errorf("token issued twice: %s", w.Body.String())
	}
	bearer := map[string]string{"Authorization": "Bearer " + token}
	session := decode(t, call(f, "GET", "/api/v1/auth/session", "", bearer))
	if session["user"].(map[string]any)["id"] != "admin" || session["role"] != "admin" || session["signedIn"] != true {
		t.Errorf("device session %v", session)
	}
	if w := call(f, "POST", "/api/v1/auth/device/approve", `{"userCode":"BCDF-GHJK","approve":true}`, bearer); w.Code != 403 {
		t.Errorf("approve from a device: %d", w.Code)
	}
	member := decode(t, call(f, "GET", "/api/v1/auth/session", "", sessionCookie("member")))
	if member["role"] != "member" || member["user"].(map[string]any)["name"] != nil {
		t.Errorf("member session %v", member)
	}

	w = call(f, "POST", "/api/v1/auth/logout", "", bearer)
	if w.Code != 200 || w.Header().Get("Set-Cookie") != "" {
		t.Errorf("device logout %d %q", w.Code, w.Header().Get("Set-Cookie"))
	}
	if w := call(f, "GET", "/api/v1/auth/session", "", bearer); decode(t, w)["user"] != nil {
		t.Error("a revoked token still signs in")
	}
	w = call(f, "POST", "/api/v1/auth/logout", "", admin)
	if c := w.Header().Get("Set-Cookie"); w.Code != 200 || !strings.HasPrefix(c, SessionCookie+"=;") || !strings.Contains(c, "Max-Age=0") {
		t.Errorf("cookie logout %d %q", w.Code, c)
	}

	w = call(f, "POST", "/api/v1/auth/device/code", `{}`, nil)
	denyCode := decode(t, w)["userCode"].(string)
	denyDevice := decode(t, w)["deviceCode"].(string)
	if w := call(f, "POST", "/api/v1/auth/device/approve", `{"userCode":"`+denyCode+`","approve":false}`, admin); w.Body.String() != `{"approved":false,"name":"Unnamed device","ok":true}` {
		t.Errorf("deny %s", w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/auth/device/token", `{"deviceCode":"`+denyDevice+`"}`, nil); decode(t, w)["error"] != "access_denied" {
		t.Errorf("denied poll %s", w.Body.String())
	}

	rows, err := db.Query(ctx, `select seq, entity_kind, event, payload::text, payload_hash, prev_hash, chain_hash from audit_chain where org_id = 'org' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	prev := genesisHash
	for rows.Next() {
		var seq int
		var kind, event, payload, payloadHash, prevHash, chainHash string
		if err := rows.Scan(&seq, &kind, &event, &payload, &payloadHash, &prevHash, &chainHash); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(payload), &p)
		canonical, _ := canonicalJSON(p)
		if sha256Hex(canonical) != payloadHash || prevHash != prev || sha256Hex(prev+"\x00"+strconv.Itoa(seq)+"\x00"+payloadHash) != chainHash {
			t.Errorf("link %d does not verify", seq)
		}
		prev = chainHash
		events = append(events, kind+":"+event+":"+canonical)
	}
	want := []string{
		`device_request:approved:{"deviceName":"Ada's phone","userId":"admin"}`,
		`device:signed_out:{"deviceName":"Ada's phone","userId":"admin"}`,
		`device_request:denied:{"deviceName":"Unnamed device","userId":"admin"}`,
	}
	if strings.Join(events, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit chain:\n%s", strings.Join(events, "\n"))
	}
}

func TestDeviceRequestLimit(t *testing.T) {
	db := testpg.Migrated(t)
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	f, _ := newAPIFront(t, `{"provider":null}`, db)
	for i := range maxPendingRequests {
		if w := call(f, "POST", "/api/v1/auth/device/code", "", nil); w.Code != 200 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := call(f, "POST", "/api/v1/auth/device/code", "", nil)
	if w.Code != 429 || decode(t, w)["error"] != tooManyDevicesMessage {
		t.Errorf("over the limit: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `update app_device_request set expires_at = now() - interval '1 second'`)
	if w := call(f, "POST", "/api/v1/auth/device/code", "", nil); w.Code != 200 {
		t.Errorf("after expiry: %d", w.Code)
	}
}
