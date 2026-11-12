package webfront

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func groupsFront(t *testing.T) (*Front, *pgxpool.Pool, *setupWorker) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, wk := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	return f, db, wk
}

func TestAdminGroupsAdminOnly(t *testing.T) {
	f, _, wk := groupsFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/groups", ""},
		{"POST", "/api/v1/admin/groups", `{"name":"x"}`},
		{"GET", "/api/v1/admin/groups/" + identityID, ""},
		{"PATCH", "/api/v1/admin/groups/" + identityID, `{"name":"x"}`},
		{"DELETE", "/api/v1/admin/groups/" + identityID, ""},
		{"POST", "/api/v1/admin/groups/" + identityID + "/members", `{"userId":"member"}`},
		{"DELETE", "/api/v1/admin/groups/" + identityID + "/members", `{"userId":"member"}`},
		{"GET", "/api/v1/admin/idp-rules", ""},
		{"POST", "/api/v1/admin/idp-rules", `{}`},
		{"DELETE", "/api/v1/admin/idp-rules/" + identityID, ""},
		{"GET", "/api/v1/admin/directory", ""},
		{"POST", "/api/v1/admin/directory", ""},
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

func TestAdminGroups(t *testing.T) {
	f, db, wk := groupsFront(t)
	admin := sessionCookie("admin")

	w := call(f, "GET", "/api/v1/admin/groups", "", admin)
	var listed struct {
		Groups []userGroup `json:"groups"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if w.Code != 200 || len(listed.Groups) != 2 || listed.Groups[0].Slug != "administrators" || listed.Groups[1].Slug != "everyone" ||
		listed.Groups[0].MemberCount != 1 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	adminsID := listed.Groups[0].ID
	everyoneID := listed.Groups[1].ID

	w = call(f, "POST", "/api/v1/admin/groups", `{"name":"  Fínance Team ","description":" Money "}`, admin)
	created := decode(t, w)["group"].(map[string]any)
	if w.Code != 201 || created["slug"] != "fi-nance-team" || created["name"] != "Fínance Team" || created["description"] != "Money" ||
		created["kind"] != "custom" || created["memberCount"] != 0.0 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	groupID := created["id"].(string)
	for _, c := range []struct{ body, want string }{
		{`{"name":"fínance team"}`, `{"code":"conflict","error":"a group named \"fínance team\" already exists"}`},
		{`{"name":"Everyone"}`, `{"code":"conflict","error":"a group named \"Everyone\" already exists"}`},
		{`{"name":"   "}`, `{"code":"invalid","error":"group name must be 1 to 120 characters"}`},
		{`{"name":"` + strings.Repeat("é", 121) + `"}`, `{"code":"invalid","error":"group name must be 1 to 120 characters"}`},
		{`{}`, `{"error":"name is required"}`},
		{`[]`, `{"error":"name is required"}`},
		{`nope`, `{"error":"name is required"}`},
	} {
		w := call(f, "POST", "/api/v1/admin/groups", c.body, admin)
		var got, want any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		_ = json.Unmarshal([]byte(c.want), &want)
		if w.Code != 409 && w.Code != 400 || jsCanonical(got) != jsCanonical(want) {
			t.Errorf("create %s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	w = call(f, "POST", "/api/v1/admin/groups", `{"name":"Fínance-Team!"}`, admin)
	if w.Code != 201 || decode(t, w)["group"].(map[string]any)["slug"] != "fi-nance-team-2" {
		t.Errorf("second slug: %d %s", w.Code, w.Body.String())
	}

	members := "/api/v1/admin/groups/" + groupID + "/members"
	if w := call(f, "POST", members, `{"userId":"member"}`, admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("add: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", members, `{"userId":"ghost"}`, admin); w.Code != 404 || !strings.Contains(w.Body.String(), "user not found") {
		t.Errorf("add ghost: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", members, `{}`, admin); w.Code != 400 || w.Body.String() != `{"error":"userId is required"}` {
		t.Errorf("add no user: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/admin/groups/"+everyoneID+"/members", `{"userId":"member"}`, admin); w.Code != 400 ||
		!strings.Contains(w.Body.String(), `"code":"implicit"`) {
		t.Errorf("add to everyone: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id, created_at, created_by_user_id)
		values ('org', $1, 'skill', 'docx', '2026-11-01T00:00:00Z', 'admin'), ('org', $1, 'skill', '*', '2026-11-01T00:00:00Z', null)`, groupID)
	seed(t, db, `insert into data_access_rule (org_id, group_id, source, table_name, columns, row_filter, updated_at)
		values ('org', $1, 'shop', 'orders', '{id,amount}', '{"op":"eq","value":1.0,"column":"region"}', '2026-11-02T00:00:00Z')`, groupID)

	w = call(f, "GET", "/api/v1/admin/groups/"+groupID, "", admin)
	body := w.Body.String()
	for _, want := range []string{
		`"group":{"id":"` + groupID + `","slug":"fi-nance-team","name":"Fínance Team","description":"Money","kind":"custom","memberCount":1,"ruleCount":0}`,
		`"members":[{"userId":"member","email":"m@example.com","name":null,"disabled":false,"sources":["local"]}]`,
		`"grants":[{"itemType":"skill","itemId":"*","createdAt":"2026-11-01T00:00:00.000Z","createdByUserId":null},{"itemType":"skill","itemId":"docx","createdAt":"2026-11-01T00:00:00.000Z","createdByUserId":"admin"}]`,
		`"columns":["id","amount"],"rowFilter":{"op":"eq","value":1,"column":"region"},"updatedAt":"2026-11-02T00:00:00.000Z"`,
		`"idpRules":[]`,
	} {
		if w.Code != 200 || !strings.Contains(body, want) {
			t.Errorf("detail lacks %s: %d %s", want, w.Code, body)
		}
	}
	w = call(f, "GET", "/api/v1/admin/groups/"+everyoneID, "", admin)
	if !strings.Contains(w.Body.String(), `"sources":["implicit"]`) || strings.Contains(w.Body.String(), "g@example.com") {
		t.Errorf("everyone members: %s", w.Body.String())
	}
	for _, id := range []string{identityID, "not-a-uuid"} {
		if w := call(f, "GET", "/api/v1/admin/groups/"+id, "", admin); w.Code != 404 || w.Body.String() != `{"error":"group not found"}` {
			t.Errorf("unknown %s: %d %s", id, w.Code, w.Body.String())
		}
	}

	w = call(f, "PATCH", "/api/v1/admin/groups/"+groupID, `{"name":" Finance ","description":null}`, admin)
	if g := decode(t, w)["group"].(map[string]any); w.Code != 200 || g["name"] != "Finance" || g["description"] != nil || g["slug"] != "fi-nance-team" {
		t.Errorf("patch: %d %s", w.Code, w.Body.String())
	}
	w = call(f, "PATCH", "/api/v1/admin/groups/"+groupID, `{"description":" Books "}`, admin)
	if g := decode(t, w)["group"].(map[string]any); g["name"] != "Finance" || g["description"] != "Books" {
		t.Errorf("patch description: %s", w.Body.String())
	}
	w = call(f, "PATCH", "/api/v1/admin/groups/"+groupID, `{"description":5}`, admin)
	if g := decode(t, w)["group"].(map[string]any); g["description"] != "Books" {
		t.Errorf("patch keeps description: %s", w.Body.String())
	}
	for _, c := range []struct {
		id, body string
		code     int
		want     string
	}{
		{adminsID, `{"name":"Admins"}`, 400, `"code":"builtin"`},
		{adminsID, `{"name":"Administrators"}`, 200, `"name":"Administrators"`},
		{groupID, `{"name":""}`, 400, `"code":"invalid"`},
		{groupID, `null`, 400, `{"error":"invalid JSON body"}`},
		{identityID, `{}`, 404, `{"error":"group not found","code":"not_found"}`},
	} {
		if w := call(f, "PATCH", "/api/v1/admin/groups/"+c.id, c.body, admin); w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("patch %s %s: %d %s", c.id, c.body, w.Code, w.Body.String())
		}
	}

	w = call(f, "DELETE", members, `{"userId":"member"}`, admin)
	if w.Code != 200 || w.Body.String() != `{"stillMember":false}` {
		t.Errorf("remove: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/groups/"+everyoneID+"/members", `{"userId":"member"}`, admin); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "users cannot leave Everyone") {
		t.Errorf("leave everyone: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/groups/"+adminsID+"/members", `{"userId":"admin"}`, admin); w.Code != 409 ||
		!strings.Contains(w.Body.String(), `"code":"lockout"`) {
		t.Errorf("lockout: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/groups/"+adminsID, "", admin); w.Code != 400 || !strings.Contains(w.Body.String(), "built-in groups cannot be deleted") {
		t.Errorf("delete builtin: %d %s", w.Code, w.Body.String())
	}
	wk.calls = nil
	if w := call(f, "DELETE", "/api/v1/admin/groups/"+groupID, "", admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	if !slices.Equal(wk.calls, []string{"POST /admin/graphjin/group-grants {}"}) {
		t.Errorf("worker calls: %v", wk.calls)
	}
	if w := call(f, "DELETE", "/api/v1/admin/groups/"+groupID, "", admin); w.Code != 404 {
		t.Errorf("delete again: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/groups/not-a-uuid", "", admin); w.Code != 500 || !strings.Contains(w.Body.String(), "invalid input syntax for type uuid") {
		t.Errorf("delete bad id: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminIdpRules(t *testing.T) {
	f, db, _ := groupsFront(t)
	admin := sessionCookie("admin")
	const ssoID = "b0000000-0000-4000-8000-000000000001"
	seed(t, db, `insert into sso_group (id, org_id, provider, tenant_id, external_id, display_name)
		values ($1, 'org', 'scalekit', 't1', 'ext-ops', 'Ops'), ('b0000000-0000-4000-8000-000000000002', 'org', 'scalekit', 't1', 'ext-old', null)`, ssoID)
	seed(t, db, `update sso_group set active = false where external_id = 'ext-old'`)
	seed(t, db, `insert into sso_group_membership (org_id, group_id, user_id) values ('org', $1, 'member'), ('org', $1, 'admin')`, ssoID)

	w := call(f, "POST", "/api/v1/admin/groups", `{"name":"Ops"}`, admin)
	groupID := decode(t, w)["group"].(map[string]any)["id"].(string)

	w = call(f, "GET", "/api/v1/admin/idp-rules", "", admin)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"rules":[],"idpGroups":[{"id":"`+ssoID+`","provider":"scalekit","tenantId":"t1","externalId":"ext-ops","displayName":"Ops","active":true,"memberCount":2},`) ||
		!strings.Contains(w.Body.String(), `"displayName":null,"active":false,"memberCount":0}],"groups":[`) {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{}`, 400, `{"error":"ssoGroupId and userGroupId are required"}`},
		{`{"ssoGroupId":"` + identityID + `","userGroupId":"` + groupID + `"}`, 404, `{"code":"not_found","error":"IdP group not found"}`},
		{`{"ssoGroupId":"` + ssoID + `","userGroupId":"` + identityID + `"}`, 404, `{"code":"not_found","error":"group not found"}`},
	} {
		w := call(f, "POST", "/api/v1/admin/idp-rules", c.body, admin)
		var got, want any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		_ = json.Unmarshal([]byte(c.want), &want)
		if w.Code != c.code || jsCanonical(got) != jsCanonical(want) {
			t.Errorf("create %s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	rule := `{"ssoGroupId":"` + ssoID + `","userGroupId":"` + groupID + `"}`
	w = call(f, "POST", "/api/v1/admin/idp-rules", rule, admin)
	ruleID, _ := decode(t, w)["rule"].(map[string]any)["id"].(string)
	if w.Code != 201 || ruleID == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/admin/idp-rules", rule, admin); w.Code != 201 || !strings.Contains(w.Body.String(), ruleID) {
		t.Errorf("create again: %d %s", w.Code, w.Body.String())
	}
	var n int
	_ = db.QueryRow(t.Context(), `select count(*) from user_group_membership where group_id = $1 and source = $2`, groupID, "rule:"+ruleID).Scan(&n)
	if n != 2 {
		t.Errorf("rule members: %d", n)
	}
	var by *string
	_ = db.QueryRow(t.Context(), `select created_by_user_id from idp_group_rule where id = $1`, ruleID).Scan(&by)
	if by == nil || *by != "admin" {
		t.Errorf("created by: %v", by)
	}
	w = call(f, "GET", "/api/v1/admin/groups/"+groupID, "", admin)
	if !strings.Contains(w.Body.String(), `"memberCount":2,"ruleCount":1}`) ||
		!strings.Contains(w.Body.String(), `"idpRules":[{"id":"`+ruleID+`","ssoGroupId":"`+ssoID+`","idpGroupName":"Ops","provider":"scalekit","userGroupId":"`+groupID+`","userGroupName":"Ops","createdAt":"`) ||
		!strings.Contains(w.Body.String(), `"sources":["rule:`+ruleID+`"]`) {
		t.Errorf("detail: %s", w.Body.String())
	}

	if w := call(f, "DELETE", "/api/v1/admin/idp-rules/"+identityID, "", admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete unknown: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", "/api/v1/admin/idp-rules/"+ruleID, "", admin); w.Code != 200 {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	_ = db.QueryRow(t.Context(), `select count(*) from user_group_membership where group_id = $1`, groupID).Scan(&n)
	if n != 0 {
		t.Errorf("members after delete: %d", n)
	}

	adminsID := ""
	_ = db.QueryRow(t.Context(), `select id::text from user_group where org_id = 'org' and slug = 'administrators'`).Scan(&adminsID)
	w = call(f, "POST", "/api/v1/admin/idp-rules", `{"ssoGroupId":"`+ssoID+`","userGroupId":"`+adminsID+`"}`, admin)
	adminRule, _ := decode(t, w)["rule"].(map[string]any)["id"].(string)
	seed(t, db, `delete from user_group_membership where user_id = 'admin' and source = 'local'`)
	seed(t, db, `update app_user set disabled_at = now() where id = 'member'`)
	if w := call(f, "DELETE", "/api/v1/admin/idp-rules/"+adminRule, "", admin); w.Code != 409 || !strings.Contains(w.Body.String(), "last active administrator") {
		t.Errorf("lockout: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminDirectory(t *testing.T) {
	f, _, wk := groupsFront(t)
	admin := sessionCookie("admin")
	wk.reply["/admin/directory/status"] = `{"lastSyncAt": null, "count": 1.50}`
	wk.reply["/admin/directory/sync"] = `{"ok":true}`
	if w := call(f, "GET", "/api/v1/admin/directory", "", admin); w.Code != 200 || w.Body.String() != `{"lastSyncAt":null,"count":1.5}` {
		t.Errorf("status: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", "/api/v1/admin/directory", "", admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("sync: %d %s", w.Code, w.Body.String())
	}
	if !slices.Contains(wk.calls, "POST /admin/directory/sync {}") {
		t.Errorf("calls: %v", wk.calls)
	}
	delete(wk.reply, "/admin/directory/status")
	if w := call(f, "GET", "/api/v1/admin/directory", "", admin); w.Code != 404 || w.Body.String() != `{"error":"Worker returned HTTP 404"}` {
		t.Errorf("not JSON: %d %s", w.Code, w.Body.String())
	}
	wk.fail = `{"error":"directory sync is not configured"}`
	if w := call(f, "POST", "/api/v1/admin/directory", "", admin); w.Code != 502 || w.Body.String() != `{"error":"directory sync is not configured"}` {
		t.Errorf("worker error: %d %s", w.Code, w.Body.String())
	}
	f.API.Gate.WorkerURL = "http://127.0.0.1:1"
	r := httptest.NewRequest("GET", "/api/v1/admin/directory", nil)
	w := httptest.NewRecorder()
	f.API.relayWorker(w, r, "/admin/directory/status", nil)
	if w.Code != 503 || w.Body.String() != `{"error":"worker is unavailable"}` {
		t.Errorf("worker down: %d %s", w.Code, w.Body.String())
	}
}

func TestDirectorySyncJob(t *testing.T) {
	var status int
	var reply string
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	a := &API{Gate: &Gate{WorkerURL: srv.URL}}
	status, reply = 200, `{"stats":{"usersSeen":2}}`
	if err := a.DirectorySync(t.Context()); err != nil {
		t.Errorf("ok: %v", err)
	}
	if !slices.Equal(calls, []string{"POST /admin/directory/sync {}"}) {
		t.Errorf("calls: %v", calls)
	}
	status, reply = 404, `{"error":"no directory provider is configured","code":"no_provider"}`
	if err := a.DirectorySync(t.Context()); err != nil {
		t.Errorf("no provider: %v", err)
	}
	status, reply = 409, `{"error":"a directory sync is already running","code":"running"}`
	if err := a.DirectorySync(t.Context()); err == nil || err.Error() != "directory sync failed (409): a directory sync is already running" {
		t.Errorf("running: %v", err)
	}
	status, reply = 500, ``
	if err := a.DirectorySync(t.Context()); err == nil || err.Error() != "directory sync failed (500): Internal Server Error" {
		t.Errorf("no body: %v", err)
	}
	a.Gate.WorkerURL = "http://127.0.0.1:1"
	if err := a.DirectorySync(t.Context()); err == nil || !strings.Contains(err.Error(), "the worker did not answer") {
		t.Errorf("worker down: %v", err)
	}
}
