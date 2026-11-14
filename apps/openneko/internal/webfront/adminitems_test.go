package webfront

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

func TestAdminItemsAdminOnly(t *testing.T) {
	f, _, wk := groupsFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/items", ""},
		{"GET", "/api/v1/admin/items?type=skill", ""},
		{"GET", "/api/v1/admin/item-grants?itemType=skill&itemId=x", ""},
		{"POST", "/api/v1/admin/item-grants", `{}`},
		{"DELETE", "/api/v1/admin/item-grants", `{}`},
		{"GET", "/api/v1/admin/data-access", ""},
		{"POST", "/api/v1/admin/data-access", `{}`},
		{"GET", "/api/v1/admin/data-access/settings", ""},
		{"PATCH", "/api/v1/admin/data-access/settings", `{"enabled":true}`},
		{"GET", "/api/v1/admin/data-access/tables?source=shop", ""},
		{"DELETE", "/api/v1/admin/data-access/" + identityID, ""},
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

type fakeCatalog struct {
	mu      sync.Mutex
	rows    []catalogRow
	queries []string
	auth    []string
	fail    bool
}

func (c *fakeCatalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var body struct{ Query string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.queries = append(c.queries, r.URL.Path+" "+body.Query)
	c.auth = append(c.auth, r.Header.Get("Authorization"))
	if c.fail {
		_, _ = w.Write([]byte(`{"errors":[{"message":"no"}]}`))
		return
	}
	var kind, database, offset string
	if i := strings.Index(body.Query, `kind: { eq: "`); i >= 0 {
		kind = strings.SplitN(body.Query[i+13:], `"`, 2)[0]
	}
	if i := strings.Index(body.Query, `database_name: { eq: "`); i >= 0 {
		database = strings.SplitN(body.Query[i+22:], `"`, 2)[0]
	}
	if i := strings.Index(body.Query, "offset: "); i >= 0 {
		offset = strings.SplitN(body.Query[i+8:], ",", 2)[0]
	}
	page := []catalogRow{}
	if offset == "0" {
		for _, row := range c.rows {
			if kind == catalogKind(row) && (database == "" || row.DatabaseName != nil && *row.DatabaseName == database) {
				page = append(page, row)
			}
		}
	}
	out, _ := json.Marshal(map[string]any{"data": map[string]any{"gj_catalog": page}})
	_, _ = w.Write(out)
}

func catalogKind(row catalogRow) string {
	switch {
	case row.ColumnName != nil:
		return "column"
	case strings.HasPrefix(row.ID, "q:"):
		return "saved_query"
	}
	return "database"
}

func TestAdminItems(t *testing.T) {
	f, db, wk := groupsFront(t)
	admin := sessionCookie("admin")
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("items-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, err := orgWorkspace("org")
	if err != nil {
		t.Fatal(err)
	}
	for name, md := range map[string]string{"docx": "---\ndescription: Word files\n---\n", "zeta": "# no front matter\n"} {
		_ = os.MkdirAll(filepath.Join(roots.Skills, name), 0o755)
		_ = os.WriteFile(filepath.Join(roots.Skills, name, "SKILL.md"), []byte(md), 0o644)
	}
	catalog := &fakeCatalog{rows: []catalogRow{
		{ID: "d1", Name: ptr("shop"), Summary: ptr("Store")},
		{ID: "d2"},
		{ID: "q:top", Name: ptr("top_orders")},
	}}
	srv := httptest.NewServer(catalog)
	t.Cleanup(srv.Close)
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default) values
		('org', 'graphjin', 'off', 'http://127.0.0.1:1', false, true), ('org', 'graphjin', 'main', $1, true, false)`, srv.URL+"/")
	seed(t, db, `insert into workflow_definition (id, org_id, name, description, owner_user_id, updated_at) values
		('`+identityID+`', 'org', 'Org flow', 'Daily', '', '2026-11-02'),
		('aaaaaaaa-0000-4000-8000-000000000002', 'org', 'Shared', '', '', '2026-11-03'),
		('aaaaaaaa-0000-4000-8000-000000000003', 'org', 'Mine', '', 'member', '2026-11-04')`)
	seed(t, db, `insert into watcher (org_id, workflow_id, name, description, query, value_path, op) values
		('org', '`+identityID+`', 'Stock', 'Low stock', 'q', 'v', 'gt')`)
	seed(t, db, `insert into metric (id, org_id, role, slug, source, title, chart_hint, cadence, active) values
		('bbbbbbbb-0000-4000-8000-000000000001', 'org', 'CFO', 'rev', 'bootstrap', 'Revenue', 'line', 'daily', true),
		('bbbbbbbb-0000-4000-8000-000000000002', 'org', 'COO', 'old', 'bootstrap', 'Old', 'line', 'daily', false)`)
	seed(t, db, `insert into operator_profile (org_id, user_id, role_template) values ('org', '', 'CEO'), ('org', 'admin', '')`)
	seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash) values
		('org', 'shop.refund', 'Refund', '{}', 'h'), ('org', 'crm.note', 'Note', '{}', 'h')`)
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values
		('org', 'shop', '1.2', 'installed', 'h', '{}'), ('org', 'gone', '1', 'removed', 'h', '{}')`)
	seed(t, db, `insert into channel_identity (id, org_id, channel_plugin, channel_user_id, first_seen_at, created_at, updated_at)
		values ('cccccccc-0000-4000-8000-000000000001', 'org', 'telegram', 'u1', now(), now(), now())`)
	wk.reply["/admin/graphjin/api-operations"] = `{"operations":["erp:orders:list"]}`
	wk.reply["/admin/connect/providers"] = `{"providers":[{"pluginName":"slack","providerLabel":"Slack"}]}`
	if err := secrets.Write(secrets.Store{"channel.slack": {"SLACK_BOT_TOKEN": "xoxb-1"}, "channel.hud": {"HUD_CHANNEL_WORKSPACE": "pack"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}

	w := call(f, "GET", "/api/v1/admin/items", "", admin)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"types":[{"type":"skill","label":"Skill","plural":"Skills"},`) ||
		!strings.Contains(w.Body.String(), `{"type":"pack","label":"Pack","plural":"Packs"}]}`) {
		t.Errorf("types: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/items?type=library", "", admin); w.Code != 400 || w.Body.String() != `{"error":"unknown item type"}` {
		t.Errorf("unknown type: %d %s", w.Code, w.Body.String())
	}
	for typ, want := range map[string]string{
		"skill":         `{"id":"zeta","label":"zeta","detail":""}]}`,
		"workflow":      `[{"id":"aaaaaaaa-0000-4000-8000-000000000002","label":"Shared","detail":""},{"id":"` + identityID + `","label":"Org flow","detail":"Daily"}]`,
		"metric":        `[{"id":"bbbbbbbb-0000-4000-8000-000000000001","label":"Revenue","detail":"CFO"}]`,
		"dashboard":     `[{"id":"CEO","label":"CEO dashboard"},{"id":"CFO","label":"CFO dashboard"},{"id":"COO","label":"COO dashboard"}]`,
		"watcher":       `"label":"Stock","detail":"Low stock"}]`,
		"team_memory":   `[{"id":"global","label":"Global company memory"},{"id":"database:shop","label":"Memory for shop"},{"id":"database:d2","label":"Memory for d2"}]`,
		"data_source":   `[{"id":"shop","label":"shop","detail":"Store"},{"id":"d2","label":"d2"}]`,
		"saved_query":   `[{"id":"top_orders","label":"top_orders"}]`,
		"api_operation": `[{"id":"erp:orders:list","label":"orders · list","detail":"erp"}]`,
		"action":        `[{"id":"crm.note","label":"crm.note","detail":"Note"},{"id":"shop.refund","label":"shop.refund","detail":"Refund"}]`,
		"integration":   `[{"id":"slack","label":"Slack","detail":"slack"}]`,
		"channel":       `[{"id":"slack","label":"Slack","detail":"slack"},{"id":"telegram","label":"telegram","detail":"telegram"}]`,
		"pack":          `[{"id":"shop","label":"shop","detail":"v1.2"}]`,
	} {
		w := call(f, "GET", "/api/v1/admin/items?type="+typ, "", admin)
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", typ, w.Code, w.Body.String())
		}
	}

	if w := call(f, "GET", "/api/v1/admin/items?type=skill", "", admin); !strings.Contains(w.Body.String(), `{"id":"docx","label":"docx","detail":"Word files"}`) {
		t.Errorf("skill description: %s", w.Body.String())
	}

	secret, _ := config.SigningSecret(f.API.SecretsDir, "graphjin:org")
	token := strings.TrimPrefix(catalog.auth[0], "Bearer ")
	parts := strings.Split(token, ".")
	mac := hmac.New(sha256.New, []byte(base64.StdEncoding.EncodeToString(secret)))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	_ = json.Unmarshal(claims, &c)
	if len(parts) != 3 || base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] || c["role"] != "admin" || c["sub"] != "service" ||
		c["org_id"] != "org" || c["exp"].(float64)-c["iat"].(float64) != 120 {
		t.Errorf("token: %s %v", token, c)
	}
	if !slices.ContainsFunc(catalog.queries, func(q string) bool {
		return strings.HasPrefix(q, `/api/v1/graphql query AdminCatalog { gj_catalog(where: { kind: { eq: "database" } }, limit: 1000, offset: 0,`)
	}) {
		t.Errorf("queries: %v", catalog.queries)
	}

	catalog.rows = []catalogRow{
		{ID: "c1", DatabaseName: ptr("shop"), SchemaName: ptr("sales"), TableName: ptr("orders"), ColumnName: ptr("id")},
		{ID: "c2", DatabaseName: ptr("shop"), SchemaName: ptr("sales"), TableName: ptr("orders"), ColumnName: ptr("id")},
		{ID: "c3", DatabaseName: ptr("shop"), SchemaName: ptr("sales"), TableName: ptr("orders"), ColumnName: ptr("total")},
		{ID: "c4", DatabaseName: ptr("shop"), TableName: ptr("accounts"), ColumnName: ptr("name")},
		{ID: "c5", DatabaseName: ptr("shop"), TableName: ptr(""), ColumnName: ptr("x")},
		{ID: "c6", DatabaseName: ptr("crm"), TableName: ptr("leads"), ColumnName: ptr("id")},
	}
	w = call(f, "GET", "/api/v1/admin/data-access/tables?source=shop", "", admin)
	if w.Code != 200 || w.Body.String() != `{"tables":[{"schema":"","table":"accounts","columns":["name"]},{"schema":"sales","table":"orders","columns":["id","total"]}]}` {
		t.Errorf("tables: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/admin/data-access/tables", "", admin); w.Code != 400 || w.Body.String() != `{"error":"source is required"}` {
		t.Errorf("tables without source: %d %s", w.Code, w.Body.String())
	}
	catalog.fail = true
	if w := call(f, "GET", "/api/v1/admin/data-access/tables?source=shop", "", admin); w.Code != 200 || w.Body.String() != `{"tables":[]}` {
		t.Errorf("tables, GraphJin errors: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `update data_source set enabled = false where org_id = 'org'`)
	if w := call(f, "GET", "/api/v1/admin/items?type=team_memory", "", admin); w.Code != 200 || w.Body.String() != `{"items":[{"id":"global","label":"Global company memory"}]}` {
		t.Errorf("no source: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminCatalogPages(t *testing.T) {
	f, db, _ := groupsFront(t)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("items-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		offset := strings.SplitN(strings.SplitN(body.Query, "offset: ", 2)[1], ",", 2)[0]
		offsets = append(offsets, offset)
		n := 1000
		if offset == "1000" {
			n = 7
		}
		rows := make([]catalogRow, n)
		for i := range rows {
			rows[i] = catalogRow{ID: fmt.Sprintf("c%s-%d", offset, i), DatabaseName: ptr("shop"), TableName: ptr("orders"), ColumnName: ptr(fmt.Sprintf("c%s_%d", offset, i))}
		}
		out, _ := json.Marshal(map[string]any{"data": map[string]any{"gj_catalog": rows}})
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url) values ('org', 'graphjin', 'main', $1)`, srv.URL+"/api/v1/graphql")
	w := call(f, "GET", "/api/v1/admin/data-access/tables?source=shop", "", sessionCookie("admin"))
	var got struct{ Tables []sourceTable }
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || len(got.Tables) != 1 || len(got.Tables[0].Columns) != 1007 || strings.Join(offsets, ",") != "0,1000" {
		t.Errorf("pages: %d %v %d", w.Code, offsets, len(w.Body.String()))
	}
}

func TestAdminItemGrants(t *testing.T) {
	f, db, wk := groupsFront(t)
	admin := sessionCookie("admin")
	groupID := decode(t, call(f, "POST", "/api/v1/admin/groups", `{"name":"Finance"}`, admin))["group"].(map[string]any)["id"].(string)
	var adminsID, everyoneID string
	if err := db.QueryRow(t.Context(), `select (select id::text from user_group where org_id = 'org' and slug = 'administrators'),
		(select id::text from user_group where org_id = 'org' and slug = 'everyone')`).Scan(&adminsID, &everyoneID); err != nil {
		t.Fatal(err)
	}
	grants := "/api/v1/admin/item-grants"

	if w := call(f, "POST", grants, `{"groupId":"`+groupID+`","itemType":"skill","itemId":"  docx "}`, admin); w.Code != 200 || w.Body.String() != `{"created":true}` {
		t.Errorf("grant: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", grants, `{"groupId":"`+groupID+`","itemType":"skill","itemId":"docx"}`, admin); w.Code != 200 || w.Body.String() != `{"created":false}` {
		t.Errorf("grant again: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "POST", grants, `{"groupId":"`+everyoneID+`","itemType":"skill","itemId":"*"}`, admin); w.Code != 200 || w.Body.String() != `{"created":false}` {
		t.Errorf("grant all: %d %s", w.Code, w.Body.String())
	}
	if len(wk.calls) != 0 {
		t.Errorf("skill grant called the worker: %v", wk.calls)
	}
	if w := call(f, "POST", grants, `{"groupId":"`+groupID+`","itemType":"data_source","itemIds":[" shop","crm","shop"],"itemId":"ignored"}`, admin); w.Code != 200 || w.Body.String() != `{"created":2}` {
		t.Errorf("grant list: %d %s", w.Code, w.Body.String())
	}
	if len(wk.calls) != 1 || wk.calls[0] != "POST /admin/graphjin/group-grants {}" {
		t.Errorf("worker calls: %v", wk.calls)
	}
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config) values
		('90000000-0000-4000-8000-000000000001', 'org', 'office', '1', 'installed', 'h', '{}')`)
	seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash)
		values ('90000000-0000-4000-8000-000000000001', 'org', 'skill', 'docx', 'docx', 'h', 'h')`)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id) values ('org', $1, 'pack', 'office')`, groupID)

	w := call(f, "GET", grants+"?itemType=skill&itemId=docx", "", admin)
	want := `{"holders":[{"groupId":"` + adminsID + `","name":"Administrators","slug":"administrators","via":"administrators"},` +
		`{"groupId":"` + everyoneID + `","name":"Everyone","slug":"everyone","via":"all"},`
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), want) || !strings.Contains(w.Body.String(), `"via":"pack:office"`) ||
		!strings.Contains(w.Body.String(), `"slug":"finance","via":"item"`) {
		t.Errorf("holders: %d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{"?itemType=skill", "?itemType=library&itemId=x", "?itemId=x"} {
		if w := call(f, "GET", grants+q, "", admin); w.Code != 400 || w.Body.String() != `{"error":"itemType and itemId are required"}` {
			t.Errorf("holders %s: %d %s", q, w.Code, w.Body.String())
		}
	}

	var audits int
	_ = db.QueryRow(t.Context(), `select count(*) from item_grant_audit where org_id = 'org' and actor_user_id = 'admin' and action = 'grant'`).Scan(&audits)
	if audits != 3 {
		t.Errorf("grant audits: %d", audits)
	}
	if w := call(f, "DELETE", grants, `{"groupId":"`+groupID+`","itemType":"skill","itemId":"docx"}`, admin); w.Code != 200 || w.Body.String() != `{"removed":true}` {
		t.Errorf("revoke: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", grants, `{"groupId":"`+groupID+`","itemType":"skill","itemId":"docx"}`, admin); w.Code != 200 || w.Body.String() != `{"removed":false}` {
		t.Errorf("revoke again: %d %s", w.Code, w.Body.String())
	}
	_ = db.QueryRow(t.Context(), `select count(*) from item_grant_audit where org_id = 'org' and action = 'revoke' and group_name = 'Finance'`).Scan(&audits)
	if audits != 1 {
		t.Errorf("revoke audits: %d", audits)
	}

	for _, c := range []struct {
		method, body string
		code         int
		want         string
	}{
		{"POST", `{"groupId":"` + groupID + `","itemType":"skill"}`, 400, `{"error":"groupId, itemType and itemId or itemIds are required"}`},
		{"DELETE", `{"groupId":"` + groupID + `","itemType":"skill","itemIds":["a"]}`, 400, `{"error":"groupId, itemType and itemId or itemIds are required"}`},
		{"POST", `{"groupId":"` + groupID + `","itemType":"library","itemId":"a"}`, 400, `{"error":"groupId, itemType and itemId or itemIds are required"}`},
		{"POST", `[]`, 400, `{"error":"groupId, itemType and itemId or itemIds are required"}`},
		{"POST", `{"groupId":"` + groupID + `","itemType":"skill","itemId":"   "}`, 400, `{"error":"item id must be 1 to 500 characters","code":"invalid"}`},
		{"POST", `{"groupId":"` + groupID + `","itemType":"skill","itemId":"` + strings.Repeat("é", 501) + `"}`, 400, `{"error":"item id must be 1 to 500 characters","code":"invalid"}`},
		{"POST", `{"groupId":"` + groupID + `","itemType":"skill","itemIds":[]}`, 400, `{"error":"select 1 to 500 items to grant","code":"invalid"}`},
		{"POST", `{"groupId":"` + groupID + `","itemType":"skill","itemIds":["a",3]}`, 400, `{"error":"item id must be 1 to 500 characters","code":"invalid"}`},
		{"POST", `{"groupId":"` + adminsID + `","itemType":"skill","itemId":"a"}`, 400, `{"error":"Administrators hold every item; grants are not needed","code":"builtin"}`},
		{"DELETE", `{"groupId":"` + adminsID + `","itemType":"skill","itemId":"a"}`, 400, `{"error":"Administrators hold every item; grants are not needed","code":"builtin"}`},
		{"POST", `{"groupId":"` + identityID + `","itemType":"skill","itemId":"a"}`, 404, `{"error":"group not found","code":"not_found"}`},
	} {
		if w := call(f, c.method, grants, c.body, admin); w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("%s %s: %d %s", c.method, c.body, w.Code, w.Body.String())
		}
	}
	if w := call(f, "POST", grants, `{"groupId":"nope","itemType":"skill","itemId":"a"}`, admin); w.Code != 500 || !strings.Contains(w.Body.String(), "uuid") {
		t.Errorf("malformed group: %d %s", w.Code, w.Body.String())
	}
	_ = db.QueryRow(t.Context(), `select count(*) from item_grant where org_id = 'org' and item_id = 'a'`).Scan(&audits)
	if audits != 0 {
		t.Errorf("a failed grant wrote %d rows", audits)
	}
}

func TestAdminDataAccess(t *testing.T) {
	f, db, wk := groupsFront(t)
	admin := sessionCookie("admin")
	groupID := decode(t, call(f, "POST", "/api/v1/admin/groups", `{"name":"Sales"}`, admin))["group"].(map[string]any)["id"].(string)
	var adminsID string
	_ = db.QueryRow(t.Context(), `select id::text from user_group where org_id = 'org' and slug = 'administrators'`).Scan(&adminsID)
	rules := "/api/v1/admin/data-access"

	w := call(f, "POST", rules, `{"groupId":"`+groupID+`","source":"shop","tableSchema":"sales","tableName":"orders","columns":[" id","amount","id",""],
		"rowFilter":{"and":[{"column":"region","op":"eq","value":"emea","extra":1},{"column":"team","op":"in","value":{"var":"user_groups"}}]}}`, admin)
	rule, _ := decode(t, w)["rule"].(map[string]any)
	columns, _ := json.Marshal(rule["columns"])
	filter, _ := json.Marshal(rule["rowFilter"])
	if w.Code != 201 || rule["groupSlug"] != "sales" || rule["tableSchema"] != "sales" || string(columns) != `["id","amount"]` ||
		string(filter) != `{"and":[{"column":"region","op":"eq","value":"emea"},{"column":"team","op":"in","value":{"var":"user_groups"}}]}` {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	ruleID := rule["id"].(string)
	w = call(f, "POST", rules, `{"groupId":"`+groupID+`","source":"shop","tableSchema":"sales","tableName":"orders","columns":["id"],"rowFilter":null}`, admin)
	if again, _ := decode(t, w)["rule"].(map[string]any); w.Code != 201 || again["id"] != ruleID || again["rowFilter"] != nil {
		t.Errorf("upsert: %d %s", w.Code, w.Body.String())
	}
	var creator string
	_ = db.QueryRow(t.Context(), `select created_by_user_id from data_access_rule where id = $1`, ruleID).Scan(&creator)
	if creator != "admin" {
		t.Errorf("creator: %q", creator)
	}
	w = call(f, "POST", rules, `{"groupId":"`+groupID+`","source":"crm","tableName":"leads","columns":["id"]}`, admin)
	if w.Code != 201 || decode(t, w)["rule"].(map[string]any)["tableSchema"] != "" {
		t.Errorf("no schema: %d %s", w.Code, w.Body.String())
	}

	w = call(f, "GET", rules+"?groupId="+groupID, "", admin)
	listed := decode(t, w)
	if w.Code != 200 || listed["enabled"] != false || len(listed["rules"].([]any)) != 2 {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", rules, "", admin); w.Code != 200 || len(decode(t, w)["rules"].([]any)) != 2 {
		t.Errorf("list all: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", rules+"?groupId=", "", admin); w.Code != 500 {
		t.Errorf("list with empty group: %d %s", w.Code, w.Body.String())
	}

	required := `{"error":"groupId, source, tableName and columns are required"}`
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"groupId":"g","source":"shop","tableName":"orders"}`, 400, required},
		{`{"groupId":"g","source":"shop","tableName":"orders","columns":["id",1]}`, 400, required},
		{`nope`, 400, required},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"column":"id","op":"like","value":1}}`, 400,
			`{"error":"filter.op must be one of eq, neq, gt, gte, lt, lte, in, nin, is_null"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"or":[]}}`, 400, `{"error":"filter.or needs at least one condition"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"and":[{"column":"id","op":"eq","value":1}],"x":1}}`, 400,
			`{"error":"filter.and must be the only key"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"and":[{"column":"1d","op":"eq","value":1}]}}`, 400,
			`{"error":"filter.and[0].column is not a valid column name"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"column":"id","op":"is_null","value":1}}`, 400,
			`{"error":"filter.value must be true or false for is_null"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"column":"id","op":"eq","value":{"var":"user_groups"}}}`, 400,
			`{"error":"filter: $user_groups needs in or nin"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"column":"id","op":"in","value":{"var":"user_id"}}}`, 400,
			`{"error":"filter: $user_id needs a single-value operator"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"column":"id","op":"eq","value":{"var":"me"}}}`, 400,
			`{"error":"filter.value.var must be one of user_id, user_groups, account_id"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"column":"id","op":"nin","value":[]}}`, 400,
			`{"error":"filter.value must be a non-empty list of values for nin"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":{"column":"id","op":"eq","value":null}}`, 400,
			`{"error":"filter.value must be a string, number or boolean"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":["id"],"rowFilter":"x"}`, 400, `{"error":"filter must be an object"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t","columns":[" "]}`, 400, `{"error":"choose at least one column","code":"invalid"}`},
		{`{"groupId":"` + groupID + `","source":"s","tableName":"t;drop","columns":["id"]}`, 400, `{"error":"invalid table name","code":"invalid"}`},
		{`{"groupId":"` + adminsID + `","source":"s","tableName":"t","columns":["id"]}`, 400,
			`{"error":"Administrators read every table; data access rules are not needed","code":"builtin"}`},
		{`{"groupId":"` + identityID + `","source":"s","tableName":"t","columns":["id"]}`, 404, `{"error":"group not found","code":"not_found"}`},
	} {
		if w := call(f, "POST", rules, c.body, admin); w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}
	deep := `{"column":"id","op":"eq","value":1}`
	for range 7 {
		deep = `{"and":[` + deep + `]}`
	}
	if w := call(f, "POST", rules, `{"groupId":"`+groupID+`","source":"s","tableName":"t","columns":["id"],"rowFilter":`+deep+`}`, admin); w.Body.String() != `{"error":"row filter is nested too deeply"}` {
		t.Errorf("deep: %d %s", w.Code, w.Body.String())
	}
	many := strings.TrimSuffix(strings.Repeat(`{"column":"id","op":"eq","value":1},`, 100), ",")
	if w := call(f, "POST", rules, `{"groupId":"`+groupID+`","source":"s","tableName":"t","columns":["id"],"rowFilter":{"or":[`+many+`]}}`, admin); w.Body.String() != `{"error":"row filter has too many conditions"}` {
		t.Errorf("many: %d %s", w.Code, w.Body.String())
	}

	applies := 0
	for _, c := range wk.calls {
		if c == "POST /admin/graphjin/group-grants {}" {
			applies++
		}
	}
	if applies != 3 {
		t.Errorf("grant applies after creates: %d %v", applies, wk.calls)
	}
	if w := call(f, "DELETE", rules+"/"+ruleID, "", admin); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "DELETE", rules+"/"+ruleID, "", admin); w.Code != 404 || w.Body.String() != `{"error":"rule not found"}` {
		t.Errorf("delete again: %d %s", w.Code, w.Body.String())
	}
	if len(wk.calls) != 4 {
		t.Errorf("worker calls after delete: %v", wk.calls)
	}

	settings := rules + "/settings"
	if w := call(f, "GET", settings, "", admin); w.Code != 200 || w.Body.String() != `{"enabled":false}` {
		t.Errorf("settings: %d %s", w.Code, w.Body.String())
	}
	seed(t, db, `insert into data_access_settings (org_id, group_grants_enabled) values ('org', true)`)
	if w := call(f, "GET", settings, "", admin); w.Code != 200 || w.Body.String() != `{"enabled":true}` {
		t.Errorf("settings on: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "PATCH", settings, `{"enabled":"yes"}`, admin); w.Code != 400 || w.Body.String() != `{"error":"enabled must be true or false"}` {
		t.Errorf("settings bad: %d %s", w.Code, w.Body.String())
	}
	wk.reply["/admin/graphjin/group-grants/disable"] = `{"ok":true,"enabled":false}`
	if w := call(f, "PATCH", settings, `{"enabled":false}`, admin); w.Code != 200 || w.Body.String() != `{"ok":true,"enabled":false}` ||
		wk.calls[len(wk.calls)-1] != `POST /admin/graphjin/group-grants/disable {"actorUserId":"admin"}` {
		t.Errorf("settings off: %d %s %v", w.Code, w.Body.String(), wk.calls)
	}
	if w := call(f, "PATCH", settings, `{"enabled":true}`, admin); w.Code != 404 || wk.calls[len(wk.calls)-1] != `POST /admin/graphjin/group-grants/enable {"actorUserId":"admin"}` {
		t.Errorf("settings on: %d %s", w.Code, w.Body.String())
	}
}
