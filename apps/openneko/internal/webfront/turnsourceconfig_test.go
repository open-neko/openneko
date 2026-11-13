package webfront

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

const (
	sourceAdminRun    = "b1000000-0000-4000-8000-0000000000b1"
	sourceMemberRun   = "b1000000-0000-4000-8000-0000000000b2"
	sourceNullRun     = "b1000000-0000-4000-8000-0000000000b3"
	sourceDisabledRun = "b1000000-0000-4000-8000-0000000000b4"
	sourceMain        = "c1000000-0000-4000-8000-0000000000b1"
	sourceSecond      = "c1000000-0000-4000-8000-0000000000b2"
	plantedSourcePass = "planted-source-pass-7c1e"
)

type configGraphjin struct {
	mu      sync.Mutex
	queries []string
	tokens  []string
	reply   func(query string) (int, string)
}

func (g *configGraphjin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body, _ := jsParse(raw)
	query, _ := jsGet(body, "query").(string)
	g.mu.Lock()
	g.queries = append(g.queries, query)
	g.tokens = append(g.tokens, strings.TrimPrefix(r.Header.Get("authorization"), "Bearer "))
	reply := g.reply
	g.mu.Unlock()
	status, text := reply(query)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}

func (g *configGraphjin) lastClaims(t *testing.T) string {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	parts := strings.Split(g.tokens[len(g.tokens)-1], ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestTurnSourceConfigDatabase(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	t.Setenv("OPENNEKO_GRAPHJIN_URL", "http://graphjin.internal:8089")
	gj := &configGraphjin{}
	srv := httptest.NewServer(gj)
	defer srv.Close()

	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values
		($1, 'org', $5, 'test', 'admin', 'member'), ($2, 'org', $5, 'test', 'member', 'admin'),
		($3, 'org', $5, 'test', null, 'admin'), ($4, 'org', $5, 'test', 'other', 'admin')`,
		sourceAdminRun, sourceMemberRun, sourceNullRun, sourceDisabledRun, threadMine)
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source)
		select 'org', id, 'other', 'local' from user_group where org_id = 'org' and slug = 'administrators'`)
	seed(t, db, `update app_user set disabled_at = now() where id = 'other'`)

	run := func(id string) *runTools { return a.runTools(turnBinding{OrgID: "org", RunID: id}) }
	admin := run(sourceAdminRun)
	in := sourceConfigIn{OrgID: "org", RunID: &admin.binding.RunID}
	str := func(v any, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return jsStringify(v, false)
	}

	for _, c := range []struct {
		org, run, want string
	}{
		{"org", sourceMemberRun, "GraphJin config tools are admin-only."},
		{"org", sourceNullRun, "GraphJin config tools are admin-only."},
		{"org", sourceDisabledRun, "GraphJin config tools are admin-only."},
		{"org", "b1000000-0000-4000-8000-0000000000ff", "GraphJin config run is not available."},
		{"other-org", sourceAdminRun, "GraphJin config run is not available."},
	} {
		r := a.runTools(turnBinding{OrgID: c.org, RunID: c.run})
		denied := sourceConfigIn{OrgID: c.org, RunID: &r.binding.RunID}
		if s := str(r.describeSourceGraph(ctx, denied)); s != `{"reachable":false,"denied":true,"error":"`+c.want+`"}` {
			t.Errorf("graph %s: %s", c.run, s)
		}
		if s := str(r.listSourceSecretNames(ctx, denied)); s != `{"denied":true,"error":"`+c.want+`","names":[]}` {
			t.Errorf("names %s: %s", c.run, s)
		}
		if s := str(r.describeSourceConfig(ctx, sourceConfigDescribeIn{OrgID: c.org, RunID: denied.RunID})); s != `{"denied":true,"error":"`+c.want+`"}` {
			t.Errorf("config %s: %s", c.run, s)
		}
		if s := str(r.listOpenAPISpecs(ctx, openAPIListIn{OrgID: c.org, RunID: denied.RunID})); s != `{"denied":true,"error":"`+c.want+`","assets":[]}` {
			t.Errorf("specs %s: %s", c.run, s)
		}
		if s := str(r.importOpenAPISpec(ctx, openAPIImportIn{OrgID: c.org, RunID: denied.RunID, URL: "https://api.example.com/x.yaml"})); s != `{"denied":true,"error":"`+c.want+`"}` {
			t.Errorf("import %s: %s", c.run, s)
		}
		if s := str(r.checkSource(ctx, sourceCheckIn{OrgID: c.org, RunID: denied.RunID, Answers: &jsObj{}})); s != `{"denied":true,"error":"`+c.want+`"}` {
			t.Errorf("check %s: %s", c.run, s)
		}
	}

	if s := str(admin.describeSourceGraph(ctx, in)); s != `{"reachable":false,"error":"no data source configured"}` {
		t.Errorf("graph without a source: %s", s)
	}
	if s := str(admin.describeSourceConfig(ctx, sourceConfigDescribeIn{OrgID: "org", RunID: in.RunID})); s != `{"error":"no enabled customer GraphJin data source was found"}` {
		t.Errorf("config without a source: %s", s)
	}

	seed(t, db, `insert into data_source_secret (org_id, name, description, value_enc, updated_at) values
		('org', 'erp-password', 'ERP reader', $1, '2026-11-02T00:00:00Z'),
		('org', 'old', null, $1, '2026-11-01T00:00:00Z')`, plantedSourcePass)
	if s := str(admin.listSourceSecretNames(ctx, in)); s != `{"names":[{"name":"erp-password","description":"ERP reader","updatedAt":"2026-11-02T00:00:00.000Z"},{"name":"old","description":null,"updatedAt":"2026-11-01T00:00:00.000Z"}]}` {
		t.Errorf("names: %s", s)
	}

	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, auth_mode, is_default, enabled) values
		($1, 'org', 'graphjin', 'erp', $2, 'service', true, true)`, sourceMain, srv.URL+"/api/v1/graphql")
	host := strings.TrimPrefix(strings.Split(srv.URL, ":")[1], "//")
	gj.reply = func(q string) (int, string) {
		switch {
		case strings.Contains(q, `"database"`):
			return 200, `{"data":{"gj_catalog":[{"id":"db:erp","name":"erp","summary":"postgres"}]}}`
		case strings.Contains(q, `"capability"`):
			return 200, `{"data":{"gj_catalog":null}}`
		}
		return 200, `{"data":{"gj_catalog":[{"id":"ns:a","name":"a","summary":""}]}}`
	}
	if s := str(admin.describeSourceGraph(ctx, in)); s != `{"reachable":true,"sourceName":"erp","host":"`+host+`","databases":[{"id":"db:erp","name":"erp","summary":"postgres"}],"capabilities":[],"namespaces":[{"id":"ns:a","name":"a","summary":""}]}` {
		t.Errorf("graph: %s", s)
	}
	if claims := gj.lastClaims(t); !strings.Contains(claims, `"sub":"service","role":"service"`) {
		t.Errorf("graph token: %s", claims)
	}
	gj.reply = func(string) (int, string) { return 200, `{"data":null,"errors":[{"message":"no catalog"},"second"]}` }
	if s := str(admin.describeSourceGraph(ctx, in)); s != `{"reachable":false,"error":"no catalog; second"}` {
		t.Errorf("graph errors: %s", s)
	}
	gj.reply = func(string) (int, string) { return 503, "down" }
	if s := str(admin.describeSourceGraph(ctx, in)); s != `{"reachable":false,"error":"graphjin query failed: 503 down"}` {
		t.Errorf("graph down: %s", s)
	}

	sources := `[{"name":"erp","kind":"database","type":"postgres","host":"db.internal","port":5432,"dbname":"erp","user":"reader","password":"` + plantedSourcePass +
		`","connection_string":"postgres://reader:` + plantedSourcePass + `@db.internal/erp","read_only":true,"default":true,` +
		`"access":{"read":"admin","blocked_tables":["admin_user"],"grants":[{"role":"finance","tables":[{"name":"payroll"}]}]}},` +
		`{"name":"shop","kind":"api","specs":{"orders":{"operations":{"createOrder":{"expose_mutation":true,"allowed_roles":["admin"]}}}},"capabilities":{"api.write":true}},` +
		`{"name":"docs","kind":"file","backend":"s3","bucket":"b","prefix":"p/","Access":{"Read":"owner"}}]`
	big := strings.Repeat("x", 60_000)
	gj.reply = func(string) (int, string) {
		return 200, `{"data":{"gj_config":{"catalog_revision":7,"sources":` + jsStringify(sources, false) +
			`,"roles":[{"name":"finance","match":"groups has finance","comment":"from Okta"},{"name":""}],"serv":{"api_key":"k","url":"https://u:` + plantedSourcePass + `@h"},"system":"` + big + `"}}}`
	}
	view := str(admin.describeSourceConfig(ctx, sourceConfigDescribeIn{OrgID: "org", RunID: in.RunID}))
	if strings.Contains(view, plantedSourcePass) || strings.Contains(view, `"api_key":"k"`) {
		t.Errorf("config holds a secret: %s", view)
	}
	for _, want := range []string{
		`{"source":{"id":"` + sourceMain + `","name":"erp","host":"` + host + `"},"catalogRevision":"7","config":{"sources":[{"name":"erp",`,
		`"password":"[REDACTED]","connection_string":"[REDACTED]"`,
		`"serv":{"api_key":"[REDACTED]","url":"https://[REDACTED]@h"}`,
		`"databases":null,"tables":null,"blocklist":null,"mcp":null,"serv":{"api_key":"[REDACTED]","url":"https://[REDACTED]@h"}},"summary":{"sources":[{"name":"erp","kind":"database","sentences":["A PostgreSQL source: database erp, on db.internal, port 5432, with the login reader.","OpenNeko only reads it. GraphJin blocks every change to this database.","Who may read: admins only.","Who may change or delete: nobody.","Hidden from everyone (1): admin_user.","Every other table follows the read rule.","Role finance may also read: payroll.","It is the default source."]}`,
		`{"name":"shop","kind":"api","sentences":["An API source, described by the OpenAPI documents orders.","Write calls are on, for the calls named below. Delete calls are off.","The write call orders/createOrder is open to the roles admin.","Who may read: everyone who is signed in.","Who may change or delete: nobody."]}`,
		`{"name":"docs","kind":"file","sentences":["A file source in the S3 bucket b, under p/.","Who may list and read files: everyone who is signed in, only rows where user_id is their own user.","Nobody can change or delete files through OpenNeko."]}`,
		`"roles":[{"name":"finance","sentence":"A person gets this role when groups has finance. Note: from Okta"}]},"truncated":true}`,
	} {
		if !strings.Contains(view, want) {
			t.Errorf("config: want %s\nin %s", want, view)
		}
	}
	if strings.Contains(view, `"system"`) {
		t.Errorf("config kept the field past the cap: %.200s", view)
	}
	if claims := gj.lastClaims(t); !strings.Contains(claims, `"sub":"admin","role":"admin"`) {
		t.Errorf("config token: %s", claims)
	}

	gj.reply = func(string) (int, string) { return 200, `{"data":{"gj_config":null}}` }
	if s := str(admin.describeSourceConfig(ctx, sourceConfigDescribeIn{OrgID: "org", RunID: in.RunID})); !strings.HasSuffix(s, `,"error":"GraphJin did not return its config"}`) {
		t.Errorf("config empty: %s", s)
	}
	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, auth_mode, is_default, enabled) values
		($1, 'org', 'database', 'raw', 'http://graphjin.internal:8089/api', 'service', false, true)`, sourceSecond)
	if s := str(admin.describeSourceConfig(ctx, sourceConfigDescribeIn{OrgID: "org", RunID: in.RunID})); s != `{"error":"multiple enabled data sources exist; choose a dataSourceId with list_data_sources first"}` {
		t.Errorf("config two sources: %s", s)
	}
	second := strings.ToUpper(sourceSecond)
	if s := str(admin.describeSourceConfig(ctx, sourceConfigDescribeIn{OrgID: "org", RunID: in.RunID, DataSourceID: &second})); s != `{"source":{"id":"`+sourceSecond+`","name":"raw","host":"graphjin.internal"},"error":"the selected data source is not a GraphJin server"}` {
		t.Errorf("config not graphjin: %s", s)
	}
	seed(t, db, `update data_source set kind = 'graphjin' where id = $1`, sourceSecond)
	if s := str(admin.describeSourceConfig(ctx, sourceConfigDescribeIn{OrgID: "org", RunID: in.RunID, DataSourceID: &second})); s != `{"denied":true,"source":{"id":"`+sourceSecond+`","name":"raw","host":"graphjin.internal"},"error":"refusing to read the OpenNeko internal GraphJin config"}` {
		t.Errorf("config internal: %s", s)
	}

	if s := str(admin.listOpenAPISpecs(ctx, openAPIListIn{OrgID: "org", RunID: in.RunID})); s != `{"assets":[]}` {
		t.Errorf("no specs: %s", s)
	}
	for _, title := range []string{"First", "Second"} {
		spec, err := inspectOpenAPISpec("openapi: 3.0.0\ninfo: {title: "+title+", version: '1'}\nservers: [{url: 'https://api.example.com'}]\npaths: {/a: {get: {responses: {'200': {description: ok}}}}}\n", "a.yaml", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.storeOpenAPIAsset(ctx, actor{OrgID: "org", UserID: "admin"}, "upload", "a.yaml", nil, spec); err != nil {
			t.Fatal(err)
		}
	}
	zero := 0
	if s := str(admin.listOpenAPISpecs(ctx, openAPIListIn{OrgID: "org", RunID: in.RunID, Limit: &zero})); strings.Count(s, `"id"`) != 1 || !strings.Contains(s, `"title":"Second"`) {
		t.Errorf("specs limit: %s", s)
	}
	if s := str(admin.listOpenAPISpecs(ctx, openAPIListIn{OrgID: "org", RunID: in.RunID})); strings.Count(s, `"id"`) != 2 {
		t.Errorf("specs: %s", s)
	}
	if s := str(admin.importOpenAPISpec(ctx, openAPIImportIn{OrgID: "org", RunID: in.RunID, URL: "http://api.example.com/x.yaml"})); !strings.HasPrefix(s, `{"error":"`) {
		t.Errorf("import over http: %s", s)
	}

	raw := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	u, _ := url.Parse(raw)
	dbHost, dbPort, _ := net.SplitHostPort(u.Host)
	pw, _ := u.User.Password()
	answers := func(secret string) *jsObj {
		port, _ := jsParse([]byte(dbPort))
		return jsObjOf("type", "postgres", "host", dbHost, "port", port, "dbname", "postgres", "user", u.User.Username(), "secretRef", secret)
	}
	check := str(admin.checkSource(ctx, sourceCheckIn{OrgID: "org", RunID: in.RunID, Answers: answers("missing")}))
	if !strings.Contains(check, `No stored password is named \"missing\". Save the password in the card first.`) {
		t.Errorf("check without a secret: %s", check)
	}
	check = str(admin.checkSource(ctx, sourceCheckIn{OrgID: "org", RunID: in.RunID, Answers: answers("erp-password")}))
	if strings.Contains(check, plantedSourcePass) || !strings.Contains(check, `"step":"login","status":"fail"`) {
		t.Errorf("check with a wrong password: %s", check)
	}
	seed(t, db, `update data_source_secret set value_enc = $1 where name = 'erp-password'`, pw)
	check = str(admin.checkSource(ctx, sourceCheckIn{OrgID: "org", RunID: in.RunID, Answers: answers("erp-password")}))
	if !strings.Contains(check, `"step":"login","status":"ok"`) {
		t.Errorf("check with the stored password: %s", check)
	}

	s, err := turnTools(admin.binding, admin, []string{"neko_source_config_manager"})
	if err != nil {
		t.Fatal(err)
	}
	out, rpcErr := callTool(t, s, "source_config_manager_list_source_secret_names", map[string]any{})
	if rpcErr != nil || !strings.Contains(out, `erp-password`) {
		t.Fatalf("names tool: %s %v", out, rpcErr)
	}
	out, rpcErr = callTool(t, s, "source_config_manager_check_source", map[string]any{"type": "postgres", "host": dbHost, "port": answers("").vals["port"], "dbname": "postgres", "user": u.User.Username(), "secretRef": "erp-password"})
	if rpcErr != nil || strings.Contains(out, pw) || !strings.Contains(out, `{\"step\":\"login\",\"status\":\"ok\"`) || !strings.Contains(out, `Tell the admin which step failed`) {
		t.Fatalf("check tool: %s %v", out, rpcErr)
	}
	out, rpcErr = callTool(t, s, "source_config_manager_request_source_config_change", map[string]any{"action": "add_role", "name": "x", "match": "y", "intent": "z"})
	if rpcErr != nil || !strings.Contains(out, "not served from Go yet") || !strings.Contains(out, `"isError":true`) {
		t.Fatalf("change tool: %s %v", out, rpcErr)
	}
	rows, err := db.Query(ctx, `select path from control_plane_audit where run_id = $1 order by id`, sourceAdminRun)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if got := strings.Join(paths, ","); got != "/v1/source-secrets/names,/v1/source-check" {
		t.Errorf("audit paths: %s", got)
	}

	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, enabled, config) values ('org', 'graphjin-config', 'graphjin', true, '{"sourceConfigEnabled":false}')`)
	if s := str(admin.describeSourceGraph(ctx, in)); s != `{"reachable":false,"denied":true,"error":"GraphJin config capability is disabled."}` {
		t.Errorf("graph with the capability off: %s", s)
	}
}
