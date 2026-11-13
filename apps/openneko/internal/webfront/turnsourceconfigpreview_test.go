package webfront

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type previewGraphjin struct {
	mu       sync.Mutex
	bodies   []string
	sources  string
	revision string
	catalog  string
	preview  func(body string) string
}

func (g *previewGraphjin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	g.mu.Lock()
	g.bodies = append(g.bodies, body)
	sources, revision, catalog, preview := g.sources, g.revision, g.catalog, g.preview
	g.mu.Unlock()
	switch {
	case strings.Contains(body, "catalog_revision sources roles"):
		_, _ = w.Write([]byte(`{"data":{"gj_config":{"catalog_revision":` + revision + `,"sources":` + jsStringify(sources, false) + `,"roles":"[]"}}}`))
	case strings.Contains(body, "gj_catalog"):
		_, _ = w.Write([]byte(catalog))
	default:
		_, _ = w.Write([]byte(preview(body)))
	}
}

func (g *previewGraphjin) last() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bodies[len(g.bodies)-1]
}

func (g *previewGraphjin) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.bodies)
}

type checkSocket struct {
	mu         sync.Mutex
	candidates []string
	status     int
	reply      string
}

func TestSourceConfigPreviewDatabase(t *testing.T) {
	f, db := memoryFront(t)
	a := f.API
	ctx := context.Background()
	t.Setenv("OPENNEKO_GRAPHJIN_URL", "http://graphjin.internal:8089")
	gj := &previewGraphjin{revision: `"r7"`, catalog: `{"data":{"gj_catalog":[{"table_name":"orders"},{"table_name":"payroll"}]}}`,
		sources: `[{"name":"erp","kind":"database","type":"postgres","host":"db","password":"[REDACTED]","read_only":true,"access":{"blocked_tables":["payroll"]}},{"name":"shop","kind":"api","read_only":true}]`}
	srv := httptest.NewServer(gj)
	defer srv.Close()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "graphjin.yml")
	if err := os.WriteFile(configFile, []byte("app_name: test\nsources:\n  - name: erp\n    kind: database\n    password: ${ERP_PASSWORD}\n    read_only: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := &checkSocket{status: 200, reply: `{"valid":true,"errors":[]}`}
	socketPath := filepath.Join(dir, "check.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	check := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		id, _ := jsGet(mustParse(t, raw), "candidate").(string)
		content, err := os.ReadFile(filepath.Join(dir, graphjinCheckDir, id, "graphjin.yml"))
		if err != nil {
			t.Errorf("candidate %s: %v", id, err)
		}
		sock.mu.Lock()
		sock.candidates = append(sock.candidates, string(content))
		status, reply := sock.status, sock.reply
		sock.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	})}
	go check.Serve(ln) //nolint:errcheck
	defer check.Close()

	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values
		($1, 'org', $3, 'test', 'admin', 'member'), ($2, 'org', $3, 'test', 'member', 'admin')`,
		sourceAdminRun, sourceMemberRun, threadMine)
	admin := a.runTools(turnBinding{OrgID: "org", RunID: sourceAdminRun})
	preview := func(payload string, dataSourceID ...string) string {
		t.Helper()
		in := sourceConfigPreviewIn{OrgID: "org", RunID: &admin.binding.RunID, Payload: mustParse(t, []byte(payload)).(*jsObj)}
		if len(dataSourceID) > 0 {
			in.DataSourceID = &dataSourceID[0]
		}
		v, err := admin.previewSourceConfigChange(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		s := jsStringify(v, false)
		if strings.Contains(s, plantedSourcePass) {
			t.Errorf("the password reached the preview: %s", s)
		}
		return s
	}
	const register = `{"action":"register_source","name":"crm","kind":"database","type":"mysql","host":"crm.internal","port":3306,"dbname":"crm","user":"reader","secretRef":"crm-password","blockedTables":["salaries"]}`

	member := a.runTools(turnBinding{OrgID: "org", RunID: sourceMemberRun})
	if v, err := member.previewSourceConfigChange(ctx, sourceConfigPreviewIn{OrgID: "org", RunID: &member.binding.RunID, Payload: &jsObj{}}); err != nil ||
		jsStringify(v, false) != `{"denied":true,"error":"GraphJin config tools are admin-only."}` {
		t.Errorf("member: %s %v", jsStringify(v, false), err)
	}
	if s := preview(register); s != `{"error":"no enabled customer GraphJin data source was found"}` {
		t.Errorf("no source: %s", s)
	}
	seed(t, db, `insert into data_source (id, org_id, kind, name, graphql_url, auth_mode, is_default, enabled) values
		($1, 'org', 'graphjin', 'erp', $3, 'service', true, true),
		($2, 'org', 'graphjin', 'internal', 'http://graphjin.internal:8089/api/v1/graphql', 'service', false, true)`,
		sourceMain, sourceSecond, srv.URL+"/api/v1/graphql")
	if s := preview(register); s != `{"error":"multiple enabled data sources exist; choose a dataSourceId before previewing"}` {
		t.Errorf("two sources: %s", s)
	}
	if s := preview(register, sourceSecond); !strings.HasPrefix(s, `{"denied":true,"source":{"id":"`+sourceSecond) ||
		!strings.HasSuffix(s, `"error":"refusing to preview changes against the OpenNeko internal GraphJin"}`) {
		t.Errorf("internal GraphJin: %s", s)
	}
	seed(t, db, `update data_source set enabled = false where id = $1`, sourceSecond)
	source := `{"id":"` + sourceMain + `","name":"erp","host":"127.0.0.1"}`

	if s := preview(register); s != `{"source":`+source+`,"problems":[{"field":"secretRef","message":"No saved password is named \"crm-password\". Save the password again."}],"error":"No saved password is named \"crm-password\". Save the password again."}` {
		t.Errorf("unknown secret: %s", s)
	}
	seed(t, db, `insert into data_source_secret (org_id, name, value_enc) values ('org', 'crm-password', $1), ('org', 'blank', '')`, plantedSourcePass)
	if s := preview(strings.Replace(register, "crm-password", "blank", 1)); s != `{"source":`+source+`,"problems":[{"field":"secretRef","message":"`+passwordRequiredMessage+`"}],"error":"`+passwordRequiredMessage+`"}` {
		t.Errorf("blank secret: %s", s)
	}

	gj.preview = func(body string) string {
		return `{"data":{"gj_config":{"valid":true,"preview_id":"p1","expires_at":"2026-11-13T05:00:00Z","change_summary_json":"{\"added\":[\"crm\"],\"dsn\":\"mysql://reader:` + plantedSourcePass + `@crm\"}","findings_json":"[]","errors_json":""}}}`
	}
	s := preview(register)
	if !strings.Contains(gj.last(), plantedSourcePass) || !strings.Contains(gj.last(), `expected_catalog_revision: \"r7\"`) {
		t.Errorf("GraphJin did not get the resolved password and the revision: %s", gj.last())
	}
	for _, want := range []string{`{"source":` + source + `,"preview":{"valid":true,"patchHash":"`,
		`"catalogRevision":"r7","expiresAt":"2026-11-13T05:00:00Z","scope":null,"reloadMode":null,"changes":{"added":["crm"],"dsn":"mysql://reader:[REDACTED]@crm"},"findings":[],"errors":null,"summary":{"before":{"sources":[],"roles":[]},"after":{"sources":[{"name":"crm","kind":"database","sentences":["A MySQL source: database crm, on crm.internal, port 3306, with the login reader."`,
		`password: \"[REDACTED]\"`, `"fileCheck":{"checked":false,"reason":"OPENNEKO_GRAPHJIN_CONFIG is not set"}}}`} {
		if !strings.Contains(s, want) {
			t.Errorf("valid preview lacks %s:\n%s", want, s)
		}
	}
	if !strings.Contains(s, graphjinConfigPatchHash(mustParse(t, []byte(register)))) {
		t.Errorf("patch hash: %s", s)
	}

	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", configFile)
	t.Setenv("OPENNEKO_CONFIG_CHECK_SOCKET", socketPath)
	s = preview(register)
	if !strings.Contains(s, `"fileCheck":{"checked":true,"valid":true,"errors":[]}}}`) || len(sock.candidates) != 1 ||
		strings.Contains(sock.candidates[0], plantedSourcePass) || !strings.Contains(sock.candidates[0], "password: gjsecret://sources/crm/password") ||
		!strings.Contains(sock.candidates[0], "password: ${ERP_PASSWORD}") {
		t.Errorf("file check: %s\n%v", s, sock.candidates)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, graphjinCheckDir)); len(entries) != 0 {
		t.Errorf("the candidate stayed: %v", entries)
	}
	sock.reply = `{"valid":false,"errors":["database 'crm' (reader@crm.internal:3306/crm): connection failed: password ` + plantedSourcePass + ` authentication failed"]}`
	s = preview(register)
	if !strings.HasPrefix(s, `{"source":`+source+`,"problems":[{"field":"secretRef","message":"The database refused the login \"reader\" with this password. Check the login name and save the password again.","detail":"database 'crm' (reader@crm.internal:3306/crm): connection failed: password [REDACTED] authentication failed"}],"error":"The config file would not load in GraphJin. The database refused`) ||
		!strings.Contains(s, `"valid":false,`) || !strings.Contains(s, `"fileCheck":{"checked":true,"valid":false,"errors":["database 'crm'`) {
		t.Errorf("file check fails: %s", s)
	}
	sock.status, sock.reply = 500, "boom"
	if s := preview(register); s != `{"source":`+source+`,"error":"config check returned 500: boom"}` {
		t.Errorf("file check 500: %s", s)
	}
	t.Setenv("OPENNEKO_CONFIG_CHECK_SOCKET", "")

	gj.preview = func(string) string {
		return `{"data":{"gj_config":{"valid":false,"preview_id":null,"errors_json":"[\"database 'crm' (reader@crm.internal:3306/crm): connection failed: dial tcp: lookup crm.internal: no such host\"]","change_summary_json":"not json ` + plantedSourcePass + `"}}}`
	}
	s = preview(register)
	if !strings.Contains(s, `"changes":"not json [REDACTED]"`) || !strings.Contains(s, `"problems":[{"field":"host","message":"OpenNeko cannot find the host \"crm.internal\". Check the host name."`) ||
		!strings.Contains(s, `"valid":false`) || strings.Contains(s, "fileCheck") {
		t.Errorf("invalid preview: %s", s)
	}
	gj.preview = func(string) string { return `{"errors":[{"message":"bad password ` + plantedSourcePass + `"}]}` }
	if s := preview(register); s != `{"source":`+source+`,"error":"bad password [REDACTED]"}` {
		t.Errorf("GraphJin error: %s", s)
	}

	if s := preview(`{"action":"set_source_tables","source":"erp","blockedTables":["orders","ghost"]}`); !strings.Contains(s, `"problems":[{"field":"blockedTables","message":"The source \"erp\" has no table named \"ghost\"."}]`) {
		t.Errorf("unknown table: %s", s)
	}
	if !strings.Contains(gj.last(), `"variables":{"source":"erp"}`) {
		t.Errorf("catalog variables: %s", gj.last())
	}
	if s := preview(`{"action":"set_source_access","source":"erp","write":"authenticated"}`); s != `{"source":`+source+`,"error":"database source \"erp\" stays read-only; rejected access.write: authenticated"}` {
		t.Errorf("database write: %s", s)
	}
	if s := preview(`{"action":"set_source_access","source":"crm","read":"admin"}`); !strings.Contains(s, `No source is named \"crm\". The sources are: erp, shop.`) {
		t.Errorf("unknown source: %s", s)
	}
	gj.preview = func(body string) string {
		return `{"data":{"gj_config":{"valid":true,"preview_id":"p2","findings_json":"[]","errors_json":"null"}}}`
	}
	s = preview(`{"action":"enable_api_writes","source":"shop","spec":"orders","operation":"createOrder","allowedRoles":["admin"]}`)
	if !strings.Contains(gj.last(), `update: { mode: \"preview\", expected_catalog_revision: \"r7\", update_sources: [{ name: \"shop\", read_only: false, capabilities: $json1`) ||
		!strings.Contains(gj.last(), `"variables":{"json1":{"api.write":true}}`) || !strings.Contains(gj.last(), `mutation($json1: JSON!)`) {
		t.Errorf("API write mutation: %s", gj.last())
	}
	if !strings.Contains(s, `"valid":true`) || !strings.Contains(s, `"Write calls are on, for the calls named below. Delete calls are off."`) {
		t.Errorf("API writes: %s", s)
	}

	before := gj.count()
	if s := preview(`{"action":"register_source","name":"orders","kind":"api","specAssetId":"88888888-8888-4888-8888-888888888888"}`); s != `{"source":`+source+`,"error":"OpenAPI specification is not available"}` {
		t.Errorf("unknown spec: %s", s)
	}
	var assetID string
	if err := db.QueryRow(ctx, `insert into openapi_spec_asset (org_id, source_type, original_name, content, checksum_sha256, title, base_url, operation_count, warnings, created_at)
		values ('org', 'url', 'orders.yaml', 'x', $1, 'Orders', 'https://api.example.com', 3, '{w1}', '2026-11-13T00:00:00Z') returning id::text`, strings.Repeat("b", 64)).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	s = preview(`{"action":"register_source","name":"orders","kind":"api","specAssetId":"` + assetID + `"}`)
	if !strings.HasPrefix(s, `{"source":`+source+`,"specAsset":{"id":"`+assetID+`","originalName":"orders.yaml","sourceType":"url"`) ||
		!strings.Contains(s, `"scope":"managed_openapi","reloadMode":"staged_reload","changes":{"action":"register_source","source":"orders","kind":"api","title":"Orders","version":null,"baseUrl":"https://api.example.com","operationCount":3,"authSchemes":[],"checksumSha256":"bbbb`) ||
		!strings.Contains(s, `"findings":["w1"],"errors":null,"summary":`) || !strings.Contains(s, `specs_dir: /config/specs`) {
		t.Errorf("API source: %s", s)
	}
	t.Setenv("OPENNEKO_GRAPHJIN_FILES_DIR", "/srv/files/")
	sum := strings.Repeat("c", 64)
	s = preview(`{"action":"register_source","name":"docs","kind":"file","backend":"local","localFiles":{"sourceName":"docs","ready":true,"files":[{"name":"a.pdf","size":4,"contentType":"application/pdf","checksumSha256":"` + sum + `"}],"totalSize":4}}`)
	if !strings.Contains(s, `"scope":"managed_local_files","reloadMode":"staged_reload","changes":{"action":"register_source","source":"docs","kind":"file","backend":"local","fileCount":1,"totalSize":4,"readOnly":true,"access":"authenticated"},"findings":[],"errors":null`) ||
		!strings.Contains(s, "root: /srv/files/org_") {
		t.Errorf("local files: %s", s)
	}
	if gj.count()-before != 2 {
		t.Errorf("managed sources sent a preview mutation: %d calls", gj.count()-before)
	}
	if s := preview(`{"action":"register_source","name":"docs","kind":"file","backend":"local","localFiles":{"sourceName":"other","ready":true}}`); s != `{"source":`+source+`,"error":"register_source local file manifest must match the selected source name"}` {
		t.Errorf("wrong manifest: %s", s)
	}

	gj.revision = `""`
	if s := preview(register); s != `{"source":`+source+`,"error":"GraphJin did not return catalog_revision"}` {
		t.Errorf("no revision: %s", s)
	}
	seed(t, db, `update data_source set kind = 'rest' where id = $1`, sourceMain)
	if s := preview(register); s != `{"source":`+source+`,"error":"the selected data source is not a GraphJin server"}` {
		t.Errorf("not GraphJin: %s", s)
	}
}

func mustParse(t *testing.T, raw []byte) any {
	t.Helper()
	v, err := jsParse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
