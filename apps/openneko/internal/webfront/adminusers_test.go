package webfront

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestAdminUsersAdminOnly(t *testing.T) {
	f, _, wk := groupsFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/admin/users", `{"email":"x@example.com","role":"member"}`},
		{"PATCH", "/api/v1/admin/users/member", `{"role":"admin"}`},
		{"GET", "/api/v1/admin/users/member/access", ""},
		{"GET", "/api/v1/admin/change-password", ""},
		{"POST", "/api/v1/admin/change-password", `{"password":"A-safe-passphrase"}`},
		{"GET", "/api/v1/admin/devices", ""},
		{"DELETE", "/api/v1/admin/devices/00000000-0000-4000-8000-000000000001", ""},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Header.Set("Cookie", sessionCookie("member")["Cookie"])
		w := httptest.NewRecorder()
		f.API.Handler(r)(w, r)
		if w.Code != 403 || w.Body.String() != `{"error":"admin only"}` {
			t.Errorf("%s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
		if w := call(f, c.method, c.path, c.body, nil); w.Code == 200 || w.Code == 201 {
			t.Errorf("anonymous %s %s: %d", c.method, c.path, w.Code)
		}
	}
	if len(wk.calls) != 0 {
		t.Errorf("worker calls: %v", wk.calls)
	}
}

func TestAdminUserCreate(t *testing.T) {
	f, db, wk := groupsFront(t)
	admin := sessionCookie("admin")
	ctx := context.Background()

	w := call(f, "POST", "/api/v1/admin/users", `{"email":"  New.Person@Example.COM ","name":"  Neo  ","role":"member"}`, admin)
	if w.Code != 201 || !strings.HasPrefix(w.Body.String(), `{"user":{"email":"new.person@example.com","id":"usr_`) ||
		!strings.HasSuffix(w.Body.String(), `","name":"Neo","role":"member"}}`) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var sub *string
	var name string
	if err := db.QueryRow(ctx, `select sub, name from app_user where email = 'new.person@example.com'`).Scan(&sub, &name); err != nil || sub != nil || name != "Neo" {
		t.Errorf("row: %v %v %q", err, sub, name)
	}
	if w := call(f, "POST", "/api/v1/admin/users", `{"email":"NEW.person@example.com","role":"admin"}`, admin); w.Code != 409 ||
		w.Body.String() != `{"error":"a user with email new.person@example.com already exists"}` {
		t.Errorf("duplicate: %d %s", w.Code, w.Body.String())
	}

	w = call(f, "POST", "/api/v1/admin/users", `{"email":"boss@example.com","role":"admin","name":"`+strings.Repeat("é", 210)+`"}`, admin)
	id, _ := decode(t, w)["user"].(map[string]any)["id"].(string)
	if w.Code != 201 || !strings.HasPrefix(id, "usr_") || len(id) != 16 {
		t.Fatalf("admin create: %d %s", w.Code, w.Body.String())
	}
	if role, _ := f.API.role(ctx, "org", id); role != "admin" {
		t.Errorf("role %q", role)
	}
	if err := db.QueryRow(ctx, `select name from app_user where id = $1`, id).Scan(&name); err != nil || len([]rune(name)) != 200 {
		t.Errorf("name cut: %d %v", len([]rune(name)), err)
	}

	for _, c := range []struct{ body, want string }{
		{`nope`, `{"error":"invalid JSON body"}`},
		{`{"role":"member"}`, `{"error":"a valid email address is required"}`},
		{`{"email":"a@b","role":"member"}`, `{"error":"a valid email address is required"}`},
		{`{"email":"a b@c.d","role":"member"}`, `{"error":"a valid email address is required"}`},
		{`{"email":"a@@c.d","role":"member"}`, `{"error":"a valid email address is required"}`},
		{`{"email":"` + strings.Repeat("a", 316) + `@c.de","role":"member"}`, `{"error":"a valid email address is required"}`},
		{`{"email":"a@c.d","role":"owner"}`, `{"error":"role must be admin or member"}`},
		{`[]`, `{"error":"a valid email address is required"}`},
	} {
		if w := call(f, "POST", "/api/v1/admin/users", c.body, admin); w.Code != 400 || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	if w := call(f, "POST", "/api/v1/admin/users", `{"email":"a@b.c.","role":"member"}`, admin); w.Code != 201 {
		t.Errorf("a@b.c.: %d %s", w.Code, w.Body.String())
	}

	wk.reply["/admin/directory/users"] = `{"ok":true}`
	w = call(f, "POST", "/api/v1/admin/users", `{"email":"dir@example.com","name":"Dee","role":"member","addToDirectory":true}`, admin)
	if w.Code != 201 || strings.Contains(w.Body.String(), "directoryError") {
		t.Errorf("directory: %d %s", w.Code, w.Body.String())
	}
	if last := wk.calls[len(wk.calls)-1]; last != `POST /admin/directory/users {"email":"dir@example.com","name":"Dee"}` {
		t.Errorf("worker call %q", last)
	}
	wk.fail = "boom"
	w = call(f, "POST", "/api/v1/admin/users", `{"email":"dir2@example.com","role":"member","addToDirectory":true}`, admin)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"directoryError":"Worker returned HTTP 502"`) {
		t.Errorf("directory failure: %d %s", w.Code, w.Body.String())
	}
	wk.fail = ""
	delete(wk.reply, "/admin/directory/users")
	if w := call(f, "POST", "/api/v1/admin/users", `{"email":"dir3@example.com","role":"member","addToDirectory":true}`, admin); !strings.Contains(w.Body.String(), `"directoryError":"HTTP 404"`) &&
		!strings.Contains(w.Body.String(), `"directoryError":"Worker returned HTTP 404"`) {
		t.Errorf("directory 404: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminUserCreateNeedsSignIn(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":null}`, db)
	if w := call(f, "POST", "/api/v1/admin/users", `{"email":"x@example.com","role":"member"}`, nil); w.Code != 409 ||
		w.Body.String() != `{"error":"set up sign-in before you add people"}` {
		t.Errorf("no sign-in: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "PATCH", "/api/v1/admin/users/member", `{"disabled":true}`, nil); w.Code != 409 {
		t.Errorf("patch with no sign-in: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminUserClaimsSoloAccount(t *testing.T) {
	pending := testpg.Migrated(t)
	seed(t, pending, `insert into organization (id, name) values ('org', 'Org')`)
	f, _ := newSetupFront(t, `{"provider":null,"pending":{"pluginName":"magic-link"}}`, pending)
	session := decode(t, call(f, "GET", "/api/v1/auth/session", "", nil))
	soloID := session["user"].(map[string]any)["id"].(string)
	w := call(f, "POST", "/api/v1/admin/users", `{"email":"Owner@Example.com","name":"Own","role":"admin","updateSoloAccount":true}`, nil)
	if w.Code != 200 || w.Body.String() != `{"user":{"email":"owner@example.com","id":"`+soloID+`","name":"Own","role":"admin"}}` {
		t.Fatalf("claim: %d %s", w.Code, w.Body.String())
	}
	var users int
	_ = pending.QueryRow(context.Background(), `select count(*)::int from app_user`).Scan(&users)
	if users != 1 {
		t.Errorf("users after claim: %d", users)
	}
	if w := call(f, "POST", "/api/v1/admin/users", `{"email":"again@example.com","role":"admin","updateSoloAccount":true}`, nil); w.Code != 409 ||
		w.Body.String() != `{"error":"This account cannot be updated here. Reload the page."}` {
		t.Errorf("second claim: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminUserPatch(t *testing.T) {
	f, db, _ := groupsFront(t)
	admin := sessionCookie("admin")
	ctx := context.Background()
	patch := func(id, body string) (int, string) {
		w := call(f, "PATCH", "/api/v1/admin/users/"+id, body, admin)
		return w.Code, w.Body.String()
	}
	for _, c := range []struct {
		id, body string
		code     int
		want     string
	}{
		{"admin", `{"role":"member"}`, 409, `{"error":"refusing to remove the last active administrator"}`},
		{"admin", `{"disabled":true}`, 409, `{"error":"refusing to remove the last active administrator"}`},
		{"ghost", `{"role":"member"}`, 404, `{"error":"user not found"}`},
		{"member", `{}`, 400, `{"error":"nothing to change — pass role and/or disabled"}`},
		{"member", `{"role":null}`, 400, `{"error":"role must be admin or member"}`},
		{"member", `{"disabled":"yes"}`, 400, `{"error":"disabled must be a boolean"}`},
		{"member", `nope`, 400, `{"error":"invalid JSON body"}`},
		{"member", `{"disabled":true}`, 200, `{"user":{"disabled":true,"id":"member","role":"member"}}`},
		{"member", `{"disabled":false}`, 200, `{"user":{"disabled":false,"id":"member","role":"member"}}`},
		{"member", `{"role":"admin"}`, 200, `{"user":{"disabled":false,"id":"member","role":"admin"}}`},
		{"admin", `{"role":"member","disabled":true}`, 200, `{"user":{"disabled":true,"id":"admin","role":"member"}}`},
	} {
		code, body := patch(c.id, c.body)
		if code != c.code || jsonCanon(t, body) != jsonCanon(t, c.want) {
			t.Errorf("%s %s: %d %s", c.id, c.body, code, body)
		}
	}
	var disabled bool
	if err := db.QueryRow(ctx, `select disabled_at is not null from app_user where id = 'admin'`).Scan(&disabled); err != nil || !disabled {
		t.Errorf("admin disabled: %v %v", disabled, err)
	}

	seed(t, db, `insert into app_user (id, org_id, email) values ('ruled', 'org', 'r@example.com')`)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'ruled', 'rule:00000000-0000-4000-8000-000000000001' from user_group where org_id = 'org' and slug = 'administrators'`)
	member := sessionCookie("member")
	w := call(f, "PATCH", "/api/v1/admin/users/ruled", `{"role":"member"}`, member)
	if w.Code != 409 || w.Body.String() != `{"error":"this user is an administrator through an IdP rule; change the rule instead"}` {
		t.Errorf("rule admin: %d %s", w.Code, w.Body.String())
	}
}

func jsonCanon(t *testing.T, s string) string {
	t.Helper()
	v, err := jsParse([]byte(s))
	if err != nil {
		return s
	}
	return jsStringify(v, false)
}

func TestAdminUserAccess(t *testing.T) {
	f, db, _ := groupsFront(t)
	admin := sessionCookie("admin")
	var groupID string
	if err := db.QueryRow(context.Background(), `insert into user_group (org_id, slug, name, kind) values ('org', 'finance', 'Finance', 'custom') returning id::text`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source) values ('org', $1, 'member', 'local')`, groupID)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id) values ('org', $1, 'skill', 'docx'), ('org', $1, 'skill', '*')`, groupID)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'skill', 'docx' from user_group where org_id = 'org' and slug = 'everyone'`)

	w := call(f, "GET", "/api/v1/admin/users/member/access", "", admin)
	got := decode(t, w)
	items := got["items"].([]any)
	if w.Code != 200 || got["administrator"] != false || fmt.Sprint(got["groupSlugs"]) != "[everyone finance]" {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}
	var skills []string
	for _, it := range items {
		item := it.(map[string]any)
		if item["itemType"] == "skill" {
			var names []string
			for _, g := range item["groups"].([]any) {
				names = append(names, g.(map[string]any)["name"].(string))
			}
			skills = append(skills, fmt.Sprint(item["itemId"], names))
		}
	}
	if fmt.Sprint(skills) != "[*[Everyone Finance] docx[Everyone Finance]]" {
		t.Errorf("skills %v in %s", skills, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/users/admin/access", "", admin); !strings.Contains(w.Body.String(), `"administrator":true`) {
		t.Errorf("admin: %s", w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/users/ghost/access", "", admin); w.Code != 200 || w.Body.String() != `{"administrator":false,"groupSlugs":[],"items":[]}` {
		t.Errorf("ghost: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminDevices(t *testing.T) {
	f, db, _ := groupsFront(t)
	admin := sessionCookie("admin")
	ctx := context.Background()
	var active, idle string
	if err := db.QueryRow(ctx, `insert into app_device (org_id, user_id, name, token_prefix, token_verifier, created_at, last_used_at)
		values ('org', 'member', 'Work phone', 'onk_dev_aaaaaaaaaaaa', 'v1', '2026-11-01T00:00:00Z', now()) returning id::text`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `insert into app_device (org_id, user_id, name, token_prefix, token_verifier, created_at, last_used_at)
		values ('org', 'admin', 'Old tablet', 'onk_dev_bbbbbbbbbbbb', 'v2', '2026-01-01T00:00:00Z', now() - interval '40 days') returning id::text`).Scan(&idle); err != nil {
		t.Fatal(err)
	}
	w := call(f, "GET", "/api/v1/admin/devices", "", admin)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "token_verifier") || strings.Contains(body, `"v1"`) ||
		!strings.Contains(body, `"createdAt":"2026-11-01T00:00:00.000Z"`) || !strings.Contains(body, `"userEmail":"m@example.com","userName":null`) {
		t.Fatalf("list: %d %s", w.Code, body)
	}
	devices := decode(t, w)["devices"].([]any)
	if len(devices) != 2 || devices[0].(map[string]any)["status"] != "active" || devices[1].(map[string]any)["status"] != "idle" {
		t.Errorf("statuses: %s", body)
	}

	if w := call(f, "DELETE", "/api/v1/admin/devices/"+active, "", admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{active, "not-a-uuid", strings.Repeat("-", 36), "00000000-0000-4000-8000-000000000009"} {
		if w := call(f, "DELETE", "/api/v1/admin/devices/"+id, "", admin); w.Code != 404 || w.Body.String() != `{"error":"no active device with this id"}` {
			t.Errorf("revoke %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	var by string
	var payload []byte
	if err := db.QueryRow(ctx, `select d.revoked_by_user_id, a.payload::text from app_device d
		join audit_chain a on a.entity_id = d.id::text and a.event = 'revoked' and a.entity_kind = 'device'
		where d.id = $1`, active).Scan(&by, &payload); err != nil || by != "admin" ||
		!strings.Contains(string(payload), `"byUserId": "admin"`) || !strings.Contains(string(payload), `"userId": "member"`) ||
		!strings.Contains(string(payload), `"deviceName": "Work phone"`) {
		t.Errorf("audit: %v %q %s", err, by, payload)
	}
	devices = decode(t, call(f, "GET", "/api/v1/admin/devices", "", admin))["devices"].([]any)
	if last := devices[len(devices)-1].(map[string]any); last["id"] != active || last["status"] != "revoked" || last["revokedAt"] == nil {
		t.Errorf("revoked last: %v", devices)
	}
}

func TestChangePassword(t *testing.T) {
	f, db, wk := groupsFront(t)
	admin := sessionCookie("admin")
	ctx := context.Background()
	role := fmt.Sprintf("neko_pw_%d", time.Now().UnixNano())
	seed(t, db, `create role `+role+` login password 'secret'`)
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `drop role if exists `+role) })
	f.API.DBRole = role
	dir := f.API.SecretsDir
	wk.reply["/admin/reconnect"] = `{"ok":true}`

	if w := call(f, "GET", "/api/v1/admin/change-password", "", admin); w.Body.String() != `{"changed":false}` {
		t.Errorf("before: %s", w.Body.String())
	}
	for _, c := range []struct{ body, want string }{
		{`{"password":"  short  "}`, `{"error":"Password must be at least 8 characters."}`},
		{`nope`, `{"error":"Password must be at least 8 characters."}`},
		{`{"password":"PassWord"}`, `{"error":"That password is too common. Pick something else."}`},
		{`{"password":"it's-a-long-one"}`, `{"error":"Password contains characters we can't safely apply (quotes, backslashes, newlines)."}`},
		{`{"password":"back\\slash-long"}`, `{"error":"Password contains characters we can't safely apply (quotes, backslashes, newlines)."}`},
	} {
		if w := call(f, "POST", "/api/v1/admin/change-password", c.body, admin); w.Code != 400 || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}

	w := call(f, "POST", "/api/v1/admin/change-password", `{"password":" A-safe-passphrase-2026 "}`, admin)
	if w.Code != 200 || w.Body.String() != `{"ok":true,"workerNotified":true}` {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	if last := wk.calls[len(wk.calls)-1]; last != "POST /admin/reconnect null" {
		t.Errorf("worker call %q", last)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if strings.Contains(string(raw), "A-safe-passphrase-2026") || !strings.Contains(string(raw), "enc:v1:") {
		t.Errorf("config file holds %s", raw)
	}
	if local, _ := config.ReadLocal(dir); local.Pg == nil || local.Pg.Password != "A-safe-passphrase-2026" {
		t.Errorf("config password %+v", local.Pg)
	}
	cc := db.Config().ConnConfig.Copy()
	cc.User, cc.Password = role, "A-safe-passphrase-2026"
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		t.Errorf("new password login: %v", err)
	} else {
		_ = conn.Close(ctx)
	}
	if w := call(f, "GET", "/api/v1/admin/change-password", "", admin); w.Body.String() != `{"changed":true}` {
		t.Errorf("after: %s", w.Body.String())
	}

	restarted := make(chan struct{})
	f.API.Restart = func() { close(restarted) }
	t.Setenv("OPENNEKO_RESTART_WEB_ON_PASSWORD_CHANGE", "1")
	delete(wk.reply, "/admin/reconnect")
	if w := call(f, "POST", "/api/v1/admin/change-password", `{"password":"Another-safe-one"}`, admin); w.Body.String() != `{"ok":true,"workerNotified":false}` {
		t.Errorf("worker down: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Error("web did not restart")
	}
	t.Setenv("OPENNEKO_RESTART_WEB_ON_PASSWORD_CHANGE", "")

	f.API.DBRole = role + "_missing"
	if w := call(f, "POST", "/api/v1/admin/change-password", `{"password":"Never-stored-pass"}`, admin); w.Code != 500 ||
		!strings.Contains(w.Body.String(), "does not exist") {
		t.Errorf("missing role: %d %s", w.Code, w.Body.String())
	}
	if local, _ := config.ReadLocal(dir); local.Pg.Password != "Another-safe-one" {
		t.Errorf("a rejected password was stored: %q", local.Pg.Password)
	}

	if err := config.WriteLocalDatabaseURL(dir, "postgres://neko:pw@db.example/neko"); err != nil {
		t.Fatal(err)
	}
	if w := call(f, "POST", "/api/v1/admin/change-password", `{"password":"Never-stored-pass"}`, admin); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "connects with a database URL") {
		t.Errorf("by URL: %d %s", w.Code, w.Body.String())
	}
}
