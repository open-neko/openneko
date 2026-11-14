package webfront

import (
	"encoding/json"
	"testing"
)

func TestAdminUsersList(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	if w := call(f, "GET", "/api/v1/admin/users", "", sessionCookie("member")); w.Code != 403 {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/users", "", nil); w.Code == 200 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	var groupID string
	if err := db.QueryRow(t.Context(), `insert into user_group (org_id, slug, name, kind) values ('org', 'ops', 'Ops', 'custom') returning id::text`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source) values
		('org', $1, 'member', 'rule:c1000000-0000-4000-8000-0000000000a9')`, groupID)
	w := call(f, "GET", "/api/v1/admin/users", "", sessionCookie("admin"))
	var body struct {
		Users []adminUserRow `json:"users"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Users) < 2 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	byID := map[string]adminUserRow{}
	for _, u := range body.Users {
		byID[u.ID] = u
	}
	if u := byID["admin"]; u.Role != "admin" || len(u.Groups) != 1 || u.Groups[0].Name != "Administrators" || u.Groups[0].FromRule {
		t.Errorf("admin row: %+v", u)
	}
	if u := byID["member"]; u.Role != "member" || len(u.Groups) != 1 || u.Groups[0].Name != "Ops" || !u.Groups[0].FromRule {
		t.Errorf("member row: %+v", u)
	}
}
