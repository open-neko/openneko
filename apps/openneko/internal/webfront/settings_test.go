package webfront

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const identityID = "a0000000-0000-4000-8000-000000000001"

func settingsFront(t *testing.T) (*Front, *pgxpool.Pool) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	t.Setenv("HOME", t.TempDir())
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seed(t, db, `insert into app_user (id, org_id, email, disabled_at) values ('gone', 'org', 'g@example.com', now())`)
	seed(t, db, `insert into channel_identity (id, org_id, channel_plugin, channel_user_id, display_name, first_seen_at, created_at, updated_at)
		values ($1, 'org', 'slack', 'U1', 'Sam', '2026-11-01T00:00:00.5Z', '2026-11-01T00:00:00Z', '2026-11-02T00:00:00Z'),
		('a0000000-0000-4000-8000-000000000002', 'org', 'telegram', 'T1', null, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z', '2026-11-03T00:00:00Z')`,
		identityID)
	return f, db
}

func TestSettingsAdminOnly(t *testing.T) {
	f, _ := settingsFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/settings/channel-identities", ""},
		{"PATCH", "/api/v1/settings/channel-identities/" + identityID, `{"action":"block"}`},
		{"GET", "/api/v1/settings/data-sources", ""},
		{"PUT", "/api/v1/settings/data-sources", `{"name":"x"}`},
		{"GET", "/api/v1/settings/source-secrets", ""},
		{"PUT", "/api/v1/settings/source-secrets", `{"name":"x","value":"planted"}`},
		{"DELETE", "/api/v1/settings/source-secrets?name=x", ""},
		{"GET", "/api/v1/settings/install-policy", ""},
		{"PATCH", "/api/v1/settings/install-policy", `{"allowGitUrlInstalls":true}`},
		{"GET", "/api/v1/settings/skill-learn", ""},
		{"PATCH", "/api/v1/settings/skill-learn", `{"enabled":true}`},
	} {
		if w := call(f, c.method, c.path, c.body, sessionCookie("member")); w.Code != 403 || w.Body.String() != "{\"error\":\"admin only\"}\n" {
			t.Errorf("gate %s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Header.Set("Cookie", sessionCookie("member")["Cookie"])
		w := httptest.NewRecorder()
		f.API.Handler(r)(w, r)
		if w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
			t.Errorf("handler %s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

func TestChannelIdentities(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	w := call(f, "GET", "/api/v1/settings/channel-identities", "", admin)
	want := `{"identities":[{"id":"a0000000-0000-4000-8000-000000000002","org_id":"org","channel_plugin":"telegram","workspace_id":"",` +
		`"channel_user_id":"T1","app_user_id":null,"display_name":null,"email":null,"status":"unverified",` +
		`"first_seen_at":"2026-11-01T00:00:00.000Z","verified_at":null,"created_at":"2026-11-01T00:00:00.000Z","updated_at":"2026-11-03T00:00:00.000Z"},` +
		`{"id":"` + identityID + `","org_id":"org","channel_plugin":"slack","workspace_id":"","channel_user_id":"U1","app_user_id":null,` +
		`"display_name":"Sam","email":null,"status":"unverified","first_seen_at":"2026-11-01T00:00:00.500Z","verified_at":null,` +
		`"created_at":"2026-11-01T00:00:00.000Z","updated_at":"2026-11-02T00:00:00.000Z"}]}`
	if w.Code != 200 || w.Body.String() != want {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}
	state := func() string {
		return queryText(t, db, `select coalesce(app_user_id, '-') || ',' || status || ',' || (verified_at is not null) from channel_identity where id = $1`, identityID)
	}
	path := "/api/v1/settings/channel-identities/" + identityID
	for _, c := range []struct{ body, code, contains, state string }{
		{`{"action":"link","appUserId":"gone"}`, "400", `{"error":"unknown or disabled user"}`, "-,unverified,false"},
		{`{"action":"link","appUserId":"nobody"}`, "400", `{"error":"unknown or disabled user"}`, "-,unverified,false"},
		{`{"action":"link","appUserId":"member"}`, "200", `"app_user_id":"member","display_name":"Sam","email":null,"status":"linked"`, "member,linked,true"},
		{`{"action":"block"}`, "200", `"status":"blocked"`, "member,blocked,true"},
		{`{"action":"unblock"}`, "200", `"app_user_id":null,`, "-,unverified,false"},
		{`{"action":"link","appUserId":"admin"}`, "200", `"status":"linked"`, "admin,linked,true"},
		{`{"action":"unlink"}`, "200", `"verified_at":null`, "-,unverified,false"},
		{`{"action":"drop"}`, "400", `{"error":"action must be link|unlink|block|unblock"}`, "-,unverified,false"},
		{`not json`, "400", `{"error":"action must be link|unlink|block|unblock"}`, "-,unverified,false"},
		{`null`, "500", `{"error":"internal server error"}`, "-,unverified,false"},
	} {
		w := call(f, "PATCH", path, c.body, admin)
		if strconv.Itoa(w.Code) != c.code || !strings.Contains(w.Body.String(), c.contains) || state() != c.state {
			t.Errorf("%s: %d %s, state %s", c.body, w.Code, w.Body, state())
		}
	}
	if w := call(f, "PATCH", path, `{"action":"link","appUserId":"member"}`, admin); !strings.HasPrefix(w.Body.String(), `{"identity":{"id":"`+identityID+`","org_id":"org"`) {
		t.Errorf("shape: %s", w.Body)
	}
	for _, id := range []string{"a0000000-0000-4000-8000-0000000000ff", "nope"} {
		if w := call(f, "PATCH", "/api/v1/settings/channel-identities/"+id, `{"action":"drop"}`, admin); w.Code != 404 || w.Body.String() != `{"error":"not found"}` {
			t.Errorf("%s: %d %s", id, w.Code, w.Body)
		}
	}
}

func TestDataSources(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	if w := call(f, "GET", "/api/v1/settings/data-sources", "", admin); w.Body.String() != `{"sources":[]}` {
		t.Errorf("empty: %s", w.Body)
	}
	for _, body := range []string{`{"name":"Not Valid"}`, `{"name":"-x"}`, `{}`, `not json`, `{"name":"` + strings.Repeat("a", 65) + `"}`} {
		if w := call(f, "PUT", "/api/v1/settings/data-sources", body, admin); w.Code != 400 || w.Body.String() != `{"error":"name must be lowercase letters, digits, dashes"}` {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := call(f, "PUT", "/api/v1/settings/data-sources", `null`, admin); w.Code != 500 {
		t.Errorf("null: %d", w.Code)
	}
	put := func(body string) string {
		w := call(f, "PUT", "/api/v1/settings/data-sources", body, admin)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		return w.Body.String()
	}
	row := func(name string) string {
		return queryText(t, db, `select concat_ws('|', kind, coalesce(label, '-'), graphql_url, coalesce(mcp_url, '-'), coalesce(subscription_url, '-'),
			auth_mode, is_default::text, enabled::text) from data_source where org_id = 'org' and name = $1`, name)
	}
	got := put(`{"name":" \u00a0shop\ufeff","kind":"database","label":"Shop","graphqlUrl":"http://g/graphql","authMode":"jwt","isDefault":true}`)
	id := queryText(t, db, `select id::text from data_source where name = 'shop'`)
	if got != `{"ok":true,"id":"`+id+`","name":"shop"}` || row("shop") != "database|Shop|http://g/graphql|-|-|jwt|true|true" {
		t.Errorf("insert: %s %s", got, row("shop"))
	}
	put(`{"name":"crm","kind":"bogus","mcpUrl":"http://m","subscriptionUrl":"ws://s","enabled":0,"authMode":"JWT"}`)
	if row("crm") != "graphjin|-||http://m|ws://s|none|false|true" {
		t.Errorf("crm: %s", row("crm"))
	}
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url) values ('org', 'api', 'erp', '')`)
	seed(t, db, `update data_source set updated_at = '2026-11-01T00:00:00Z'`)
	put(`{"name":"crm","kind":"api","enabled":false,"isDefault":true,"graphqlUrl":5}`)
	if old := queryText(t, db, `select count(*)::text from data_source where updated_at < now() - interval '1 day'`); old != "0" {
		t.Errorf("rows not touched by the default switch: %s", old)
	}
	if row("crm") != "graphjin|-||-|-|none|true|false" || row("shop") != "database|Shop|http://g/graphql|-|-|jwt|false|true" {
		t.Errorf("update: %s / %s", row("crm"), row("shop"))
	}
	w := call(f, "GET", "/api/v1/settings/data-sources", "", admin)
	if !strings.Contains(w.Body.String(), `"name":"crm","label":null,"kind":"graphjin","graphqlUrl":"","mcpUrl":null,"subscriptionUrl":null,"authMode":"none","isDefault":true,"enabled":false,"updatedAt":"`) ||
		!strings.Contains(w.Body.String(), `"name":"shop","label":"Shop","kind":"database","graphqlUrl":"http://g/graphql",`) {
		t.Errorf("list: %s", w.Body)
	}
}

func TestSourceSecrets(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	for _, c := range []struct{ body, want string }{
		{`{"name":"has space","value":"v"}`, `{"error":"name must be letters, digits, . _ - (no spaces)"}`},
		{`{"name":"` + strings.Repeat("a", 129) + `","value":"v"}`, `{"error":"name must be letters, digits, . _ - (no spaces)"}`},
		{`not json`, `{"error":"name must be letters, digits, . _ - (no spaces)"}`},
		{`{"name":"db.pw"}`, `{"error":"value is required"}`},
		{`{"name":"db.pw","value":""}`, `{"error":"value is required"}`},
	} {
		if w := call(f, "PUT", "/api/v1/settings/source-secrets", c.body, admin); w.Code != 400 || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	const planted = "planted-db-password-7c1e"
	if w := call(f, "PUT", "/api/v1/settings/source-secrets", `{"name":" db.pw ","value":"`+planted+`","description":"Shop DB"}`, admin); w.Body.String() != `{"ok":true,"name":"db.pw"}` {
		t.Errorf("put: %s", w.Body)
	}
	stored := queryText(t, db, `select value_enc from data_source_secret where org_id = 'org' and name = 'db.pw'`)
	if plain, err := config.MaybeDecryptValue(f.API.SecretsDir, stored); !strings.HasPrefix(stored, "enc:v1:") || err != nil || plain != planted {
		t.Errorf("stored %q: %q %v", stored, plain, err)
	}
	call(f, "PUT", "/api/v1/settings/source-secrets", `{"name":"db.pw","value":"second"}`, admin)
	call(f, "PUT", "/api/v1/settings/source-secrets", `{"name":"api_key","value":"x","description":7}`, admin)
	stored = queryText(t, db, `select value_enc from data_source_secret where name = 'db.pw'`)
	if plain, _ := config.MaybeDecryptValue(f.API.SecretsDir, stored); plain != "second" {
		t.Errorf("second: %q", plain)
	}
	seed(t, db, `update data_source_secret set updated_at = '2026-11-01T00:00:00Z' where name = 'db.pw'`)
	w := call(f, "GET", "/api/v1/settings/source-secrets", "", admin)
	if !strings.HasPrefix(w.Body.String(), `{"secrets":[{"name":"api_key","description":null,"updatedAt":"`) ||
		!strings.HasSuffix(w.Body.String(), `{"name":"db.pw","description":"Shop DB","updatedAt":"2026-11-01T00:00:00.000Z"}]}`) ||
		strings.Contains(w.Body.String(), planted) || strings.Contains(w.Body.String(), "second") {
		t.Errorf("list: %s", w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/settings/source-secrets?name=+", "", admin); w.Code != 400 || w.Body.String() != `{"error":"name is required"}` {
		t.Errorf("no name: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/settings/source-secrets?name=%20db.pw", "", admin); w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %s", w.Body)
	}
	if n := queryText(t, db, `select string_agg(name, ',') from data_source_secret`); n != "api_key" {
		t.Errorf("left: %s", n)
	}
}

func TestInstallPolicy(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	get := func() string { return call(f, "GET", "/api/v1/settings/install-policy", "", admin).Body.String() }
	if got := get(); got != `{"policy":{"allowGitUrlInstalls":false},"source":"default"}` {
		t.Errorf("default: %s", got)
	}
	for _, c := range []struct{ body, code, want string }{
		{`{"allowGitUrlInstalls":"yes","allowUnverified":true}`, "200", `{"policy":{"allowGitUrlInstalls":false},"source":"org"}`},
		{`{"allowGitUrlInstalls":true}`, "200", `{"policy":{"allowGitUrlInstalls":true},"source":"org"}`},
		{`[1]`, "200", `{"policy":{"allowGitUrlInstalls":true},"source":"org"}`},
		{`{`, "400", `{"error":"request body is not valid JSON"}`},
		{`null`, "400", `{"error":"Cannot read properties of null (reading 'allowGitUrlInstalls')"}`},
		{`{"allowGitUrlInstalls":false}`, "200", `{"policy":{"allowGitUrlInstalls":false},"source":"org"}`},
	} {
		if w := call(f, "PATCH", "/api/v1/settings/install-policy", c.body, admin); strconv.Itoa(w.Code) != c.code || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	if got := get(); got != `{"policy":{"allowGitUrlInstalls":false},"source":"org"}` {
		t.Errorf("read back: %s", got)
	}
	if got := queryText(t, db, `select count(*) || ',' || min(provider) || ',' || bool_and(enabled) || ',' || min(config::text) || ',' || min(secrets::text)
		from llm_provider_config where scope = 'install-policy'`); got != `1,install-policy,true,{"allowGitUrlInstalls": false},{}` {
		t.Errorf("row: %s", got)
	}
}

func TestSkillLearnSettings(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	if w := call(f, "GET", "/api/v1/settings/skill-learn", "", admin); w.Body.String() != `{"enabled":false,"source":"default"}` {
		t.Errorf("default: %s", w.Body)
	}
	for _, c := range []struct{ body, code, want string }{
		{`{"enabled":"yes"}`, "400", `{"error":"enabled must be a boolean"}`},
		{`[true]`, "400", `{"error":"Skill learning payload must be an object"}`},
		{`null`, "400", `{"error":"Skill learning payload must be an object"}`},
		{`{`, "400", `{"error":"request body is not valid JSON"}`},
		{`{"zeta":1,"enabled":true,"2":0,"alpha":2}`, "400", `{"error":"Unsupported skill learning setting: 2, zeta, alpha"}`},
		{`{"enabled":true}`, "200", `{"enabled":true,"source":"org"}`},
		{`{"enabled":false}`, "200", `{"enabled":false,"source":"org"}`},
		{`{"enabled":true}`, "200", `{"enabled":true,"source":"org"}`},
	} {
		if w := call(f, "PATCH", "/api/v1/settings/skill-learn", c.body, admin); strconv.Itoa(w.Code) != c.code || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	if w := call(f, "GET", "/api/v1/settings/skill-learn", "", admin); w.Body.String() != `{"enabled":true,"source":"org"}` {
		t.Errorf("read back: %s", w.Body)
	}
	if got := queryText(t, db, `select enabled::text from skill_learn_org where org_id = 'org'`); got != "true" {
		t.Errorf("row: %s", got)
	}
}
