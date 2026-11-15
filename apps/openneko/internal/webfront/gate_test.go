package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	testSecret = "0123456789abcdef0123456789abcdef"
	nodeCookie = "user-1.4102444800.fuhUjYixR83Qta_i7HSQrajXFOuU8O4oLA3uhQmX9Is"
)

func seedSignInSecrets(t *testing.T, dir, status string) {
	t.Helper()
	var parsed struct {
		Provider *struct {
			PluginName string `json:"pluginName"`
		} `json:"provider"`
		Pending *struct {
			PluginName    string   `json:"pluginName"`
			MissingEnv    []string `json:"missingEnv"`
			ConfiguredEnv []string `json:"configuredEnv"`
		} `json:"pending"`
	}
	_ = json.Unmarshal([]byte(status), &parsed)
	store := secrets.Store{}
	switch {
	case parsed.Provider != nil:
		id := parsed.Provider.PluginName
		if id != magicLinkPlugin {
			id = scalekitPlugin
		}
		store[signInSectionName] = map[string]string{"PROVIDER": id}
		if id == magicLinkPlugin {
			store[signInProviderSection(id)] = map[string]string{
				"MAGIC_LINK_SIGNING_SECRET": strings.Repeat("k", 64),
				"MAGIC_LINK_FROM":           "signin@example.com",
				"RESEND_API_KEY":            "re_test",
			}
		} else {
			store[signInProviderSection(id)] = map[string]string{
				"SCALEKIT_ENVIRONMENT_URL": "https://env.scalekit.com",
				"SCALEKIT_CLIENT_ID":       "cid",
				"SCALEKIT_CLIENT_SECRET":   "sec",
			}
		}
	case parsed.Pending != nil:
		id := parsed.Pending.PluginName
		if id != magicLinkPlugin {
			id = scalekitPlugin
		}
		store[signInSectionName] = map[string]string{"PROVIDER": id}
		sec := map[string]string{}
		if id == magicLinkPlugin {
			sec["MAGIC_LINK_SIGNING_SECRET"] = strings.Repeat("k", 64)
			for _, k := range parsed.Pending.ConfiguredEnv {
				if k != "MAGIC_LINK_SIGNING_SECRET" {
					sec[k] = "test-value"
				}
			}
		} else {
			sec["SCALEKIT_ENVIRONMENT_URL"] = "https://env.scalekit.com"
			sec["SCALEKIT_CLIENT_ID"] = "cid"
			sec["SCALEKIT_CLIENT_SECRET"] = "sec"
			for _, k := range parsed.Pending.MissingEnv {
				delete(sec, k)
			}
		}
		store[signInProviderSection(id)] = sec
	}
	raw, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	file := secrets.Path(dir)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func corruptSignInSecrets(t *testing.T, dir string) {
	t.Helper()
	file := secrets.Path(dir)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newGate(t *testing.T, workerURL string) *Gate {
	return &Gate{
		WorkerURL:     workerURL,
		SecretsDir:    t.TempDir(),
		SessionSecret: func() (string, error) { return testSecret, nil },
		MarkerPath:    filepath.Join(t.TempDir(), "auth-gate.json"),
	}
}

func run(g *Gate, method, target string, header map[string]string) (*httptest.ResponseRecorder, bool) {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	return w, g.Allow(w, r)
}

func TestSessionCookieFromNode(t *testing.T) {
	g := newGate(t, "")
	if got := g.SessionUserID(nodeCookie); got != "user-1" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"", "user-1.4102444800", "user-1.4102444801.fuhUjYixR83Qta_i7HSQrajXFOuU8O4oLA3uhQmX9Is", nodeCookie + "x", ".4102444800.x"} {
		if got := g.SessionUserID(bad); got != "" {
			t.Errorf("%q: got %q", bad, got)
		}
	}
	g.Now = func() time.Time { return time.Unix(4102444801, 0) }
	if got := g.SessionUserID(nodeCookie); got != "" {
		t.Errorf("expired cookie gave %q", got)
	}
	short := &Gate{SessionSecret: func() (string, error) { return "short", nil }}
	if got := short.SessionUserID(nodeCookie); got != "" {
		t.Errorf("short secret gave %q", got)
	}
}

func TestDefaultSessionSecretMatchesNode(t *testing.T) {
	t.Setenv("OPENNEKO_SESSION_SECRET", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := config.Dir("")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret-key"), []byte("test-secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DefaultSessionSecret()
	if err != nil || got != "UtOWLPGYU3qF2tWBPCYgTXp+i7m6JLTH1EAN76Mph98=" {
		t.Fatalf("got %q %v", got, err)
	}
	t.Setenv("OPENNEKO_SESSION_SECRET", testSecret)
	if got, _ := DefaultSessionSecret(); got != testSecret {
		t.Fatalf("env secret: got %q", got)
	}
}

func TestGateOpenWithoutSignIn(t *testing.T) {
	g := newGate(t, "")
	for _, p := range []string{"/chat", "/admin/settings", "/api/v1/settings/models"} {
		if _, ok := run(g, "GET", p, nil); !ok {
			t.Errorf("%s refused on an install with no sign-in", p)
		}
	}
	w, ok := run(g, "GET", "/nope", nil)
	if ok || w.Code != 404 || w.Body.Len() != 0 {
		t.Errorf("/nope: %d %q", w.Code, w.Body.String())
	}
	w, ok = run(g, "GET", "/api/briefing", nil)
	if ok || w.Code != 404 || !strings.Contains(w.Body.String(), `"not found"`) {
		t.Errorf("/api/briefing: %d %q", w.Code, w.Body.String())
	}
}

func TestGateWithSignIn(t *testing.T) {
	g := newGate(t, "")
	seedSignInSecrets(t, g.SecretsDir, liveScalekit)

	w, ok := run(g, "GET", "/chat/a%20b?x=1&y=%C3%A9!*()", nil)
	if ok || w.Code != 302 || w.Header().Get("Location") != "/signin?returnTo=%2Fchat%2Fa%2520b%3Fx%3D1%26y%3D%25C3%25A9!*()" {
		t.Errorf("no session: %d %q", w.Code, w.Header().Get("Location"))
	}
	w, ok = run(g, "GET", "/api/v1/work", nil)
	if ok || w.Code != 302 {
		t.Errorf("API with no Authorization: %d", w.Code)
	}
	w, ok = run(g, "GET", "/api/v1/work", map[string]string{"Authorization": "Bearer onk_dev_nope"})
	if ok || w.Code != 401 || !strings.Contains(w.Body.String(), "sign in again") {
		t.Errorf("bad token: %d %q", w.Code, w.Body.String())
	}
	for _, p := range []string{"/signin", "/api/v1/auth/callback", "/api/v1/hooks/x", "/api/workflows/w/runs"} {
		if _, ok := run(g, "POST", p, nil); !ok {
			t.Errorf("%s refused", p)
		}
	}
	cookie := map[string]string{"Cookie": SessionCookie + "=" + nodeCookie}
	if _, ok := run(g, "GET", "/chat", cookie); !ok {
		t.Error("valid session refused")
	}
	g.admins = map[string]adminProbe{"user-1": {admin: false, at: time.Now()}}
	w, ok = run(g, "GET", "/admin/settings", cookie)
	if ok || w.Code != 302 || w.Header().Get("Location") != "/" {
		t.Errorf("non-admin page: %d %q", w.Code, w.Header().Get("Location"))
	}
	w, ok = run(g, "GET", "/api/v1/settings/models", cookie)
	if ok || w.Code != 403 || !strings.Contains(w.Body.String(), "admin only") {
		t.Errorf("non-admin API: %d %q", w.Code, w.Body.String())
	}
	if _, ok := run(g, "GET", "/api/v1/settings/persona", cookie); !ok {
		t.Error("persona refused to a signed-in user")
	}
	g.admins["user-1"] = adminProbe{admin: true, at: time.Now()}
	if _, ok := run(g, "GET", "/admin/settings", cookie); !ok {
		t.Error("admin refused")
	}
}

func TestGateMarker(t *testing.T) {
	g := newGate(t, "")
	seedSignInSecrets(t, g.SecretsDir, liveScalekit)
	if _, ok := run(g, "GET", "/chat", nil); ok {
		t.Fatal("gate open with a live provider")
	}
	raw, err := os.ReadFile(g.MarkerPath)
	if err != nil || string(raw) != `{"provider":{"pluginName":"scalekit","providerLabel":"Scalekit","provisioning":"automatic","loginHintRequired":false}}` {
		t.Fatalf("marker %q %v", raw, err)
	}

	down := newGate(t, "")
	corruptSignInSecrets(t, down.SecretsDir)
	down.MarkerPath = g.MarkerPath
	if _, ok := run(down, "GET", "/chat", nil); ok {
		t.Error("secrets unreadable after sign-in was live: the gate must stay closed")
	}

	fresh := newGate(t, "")
	corruptSignInSecrets(t, fresh.SecretsDir)
	if _, ok := run(fresh, "GET", "/chat", nil); !ok {
		t.Error("secrets unreadable on an install that never had sign-in: the gate must stay open")
	}

	seedSignInSecrets(t, g.SecretsDir, `{"provider":null}`)
	g.provider = nil
	if _, ok := run(g, "GET", "/chat", nil); !ok {
		t.Error("sign-in turned off: the gate must open")
	}
	if raw, _ := os.ReadFile(g.MarkerPath); string(raw) != `{"provider":null}` {
		t.Errorf("marker after sign-in off: %q", raw)
	}
}

func TestGateReadsNodeMarker(t *testing.T) {
	g := newGate(t, "")
	corruptSignInSecrets(t, g.SecretsDir)
	if err := os.WriteFile(g.MarkerPath, []byte(`{"provider":{"pluginName":"entra","tenant":"t"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := run(g, "GET", "/work", nil); ok {
		t.Error("a marker written by Node must close the gate")
	}
}

func TestGateDatabase(t *testing.T) {
	pool := testpg.Pool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		create table organization (id text primary key, solo_admin_user_id text, created_at timestamptz not null default now());
		create table app_user (id text primary key, org_id text not null, email text not null default 'a@example.com', disabled_at timestamptz);
		create table app_device (id text primary key, org_id text not null, user_id text not null,
			token_prefix text not null, token_verifier text not null, revoked_at timestamptz,
			last_used_at timestamptz not null default now());
		create table user_group (id text primary key, org_id text not null, slug text not null);
		create table user_group_membership (group_id text not null, user_id text not null);
		insert into organization (id, created_at) values ('org', now() - interval '1 day'), ('org2', now());
		insert into app_user (id, org_id, disabled_at) values ('admin', 'org', null), ('member', 'org', null), ('gone', 'org', now());
		insert into user_group values ('g-admin', 'org', 'administrators'), ('g-ops', 'org', 'ops');
		insert into user_group_membership values ('g-admin', 'admin'), ('g-ops', 'member'), ('g-admin', 'gone');`)
	if err != nil {
		t.Fatal(err)
	}
	token := func(prefix string) string { return "onk_dev_" + prefix + "_" + strings.Repeat("A", 43) }
	device := func(id, org, user, prefix, extra string) {
		sum := sha256.Sum256([]byte(token(prefix)))
		if _, err := pool.Exec(ctx, `insert into app_device (id, org_id, user_id, token_prefix, token_verifier) values ($1,$2,$3,$4,$5)`,
			id, org, user, "onk_dev_"+prefix, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
		if extra != "" {
			if _, err := pool.Exec(ctx, `update app_device set `+extra+` where id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	device("d1", "org", "member", "aaaaaaaaaaaa", "last_used_at = now() - interval '2 minutes'")
	device("d2", "org", "member", "bbbbbbbbbbbb", "revoked_at = now()")
	device("d3", "org", "gone", "cccccccccccc", "")
	device("d4", "org", "member", "dddddddddddd", "last_used_at = now() - interval '31 days'")
	device("d5", "org2", "member", "eeeeeeeeeeee", "")
	device("d6", "org", "admin", "ffffffffffff", "")

	g := newGate(t, "")
	seedSignInSecrets(t, g.SecretsDir, liveScalekit)
	g.DB = pool
	if got := g.deviceUserID(ctx, "Bearer "+token("aaaaaaaaaaaa")); got != "member" {
		t.Errorf("valid device: %q", got)
	}
	var fresh bool
	if err := pool.QueryRow(ctx, `select last_used_at > now() - interval '10 seconds' from app_device where id = 'd1'`).Scan(&fresh); err != nil || !fresh {
		t.Errorf("last_used_at not moved: %v %v", fresh, err)
	}
	for _, auth := range []string{
		"Bearer " + token("bbbbbbbbbbbb"),
		"Bearer " + token("cccccccccccc"),
		"Bearer " + token("dddddddddddd"),
		"Bearer " + token("eeeeeeeeeeee"),
		"Bearer onk_dev_aaaaaaaaaaaa_" + strings.Repeat("B", 43),
		"Basic " + token("aaaaaaaaaaaa"),
	} {
		if got := g.deviceUserID(ctx, auth); got != "" {
			t.Errorf("%s: got %q", auth, got)
		}
	}

	if _, ok := run(g, "GET", "/api/v1/settings/models", map[string]string{"Authorization": "bearer " + token("ffffffffffff")}); !ok {
		t.Error("admin device refused on an admin route")
	}
	w, ok := run(g, "GET", "/api/v1/settings/models", map[string]string{"Authorization": "Bearer " + token("aaaaaaaaaaaa")})
	if ok || w.Code != 403 {
		t.Errorf("member device on an admin route: %d", w.Code)
	}
	if !g.isAdministrator(ctx, "admin") || g.isAdministrator(ctx, "member") || g.isAdministrator(ctx, "gone") {
		t.Error("administrator check")
	}
}

func writeKeepingSignIn(store secrets.Store, dir string) error {
	old, _ := secrets.Read(dir)
	for section, values := range old {
		if _, set := store[section]; !set && (section == signInSectionName || strings.HasPrefix(section, signInSectionName+".")) {
			store[section] = values
		}
	}
	return secrets.Write(store, dir)
}

func TestGateKeepsMembersOutOnARealInstall(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret-key"), []byte("install-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &Gate{SecretsDir: dir, DB: db, PublicURL: "https://neko.example",
		SessionSecret: func() (string, error) { return testSecret, nil },
		MarkerPath:    filepath.Join(t.TempDir(), "auth-gate.json")}
	a := &API{Gate: g, DB: db, Version: "5.0.0", SecretsDir: dir, PublicURL: "https://neko.example"}
	f := &Front{App: fstest.MapFS{"index.html": {Data: []byte("app")}}, Gate: g, API: a, Node: &recorder{}}

	if err := a.selectSignInProvider(magicLinkPlugin); err != nil {
		t.Fatal(err)
	}
	from, key := "OpenNeko <signin@acme.com>", "re_live_123"
	if err := a.setSignInProviderValues(magicLinkPlugin, map[string]*string{"MAGIC_LINK_FROM": &from, "RESEND_API_KEY": &key}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(secrets.Path(dir))
	if err != nil || !strings.Contains(string(raw), `"PROVIDER": "enc:v1:`) {
		t.Fatalf("the sign-in section is not encrypted: %s %v", raw, err)
	}

	member, admin := sessionCookie("member"), sessionCookie("admin")
	check := func(stage string) {
		t.Helper()
		g.Reset()
		if !g.Status(context.Background()).Live() {
			t.Fatalf("%s: sign-in is not live", stage)
		}
		for _, path := range []string{"/api/v1/admin/packs", "/api/v1/admin/channels/settings", "/api/v1/settings/models"} {
			if w := call(f, "GET", path, "", member); w.Code != 403 {
				t.Errorf("%s: member GET %s: %d", stage, path, w.Code)
			}
			if w := call(f, "GET", path, "", nil); w.Code != 302 {
				t.Errorf("%s: anonymous GET %s: %d", stage, path, w.Code)
			}
		}
	}
	check("after sign-in setup")
	if w := call(f, "POST", "/api/v1/admin/channels/settings", `{"channel":"telegram","values":{"TELEGRAM_BOT_TOKEN":"123:abc"}}`, admin); w.Code != 200 {
		t.Fatalf("admin channel save: %d %s", w.Code, w.Body.String())
	}
	check("after a channel save")
}
