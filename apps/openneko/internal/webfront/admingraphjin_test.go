package webfront

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminGraphjinConfig(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agentic.yml")
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", file)
	f, _, db := signinFront(t, liveScalekit)
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("graphjin-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/admin/graphjin/config"
	if w := call(f, "GET", path, "", sessionCookie("member")); w.Code != 403 {
		t.Fatalf("member: %d %s", w.Code, w.Body.String())
	}
	if w := call(f, "GET", path, "", nil); w.Code == 200 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	type view struct {
		Descriptions []graphjinDescription `json:"descriptions"`
		File         graphjinFileView      `json:"file"`
	}
	read := func() (view, string) {
		t.Helper()
		w := call(f, "GET", path, "", sessionCookie("admin"))
		var v view
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("admin: %d %s", w.Code, w.Body.String())
		}
		return v, w.Body.String()
	}
	if v, _ := read(); len(v.Descriptions) != 0 || v.File.Error == nil || *v.File.Error != "the file does not exist" || v.File.State != nil {
		t.Errorf("empty: %+v", v)
	}

	if err := os.WriteFile(file, []byte("a: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, _ := read(); v.File.Error == nil || !strings.HasPrefix(*v.File.Error, "the file is not valid YAML (line ") {
		t.Errorf("bad yaml: %+v", v.File)
	}
	const pass = "s3cr3t-planted"
	if err := os.WriteFile(file, []byte("# password: "+pass+"\ndatabase:\n  host: db\n  password: "+pass+"\nurl: postgres://u:"+pass+"@h/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, graphjinStateFile), []byte(`{"state":"restarting","reason":"dial postgres://u:`+pass+`@h failed","restarts":2}`), 0o600); err != nil {
		t.Fatal(err)
	}

	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"gj_config":{"catalog_revision":3,"sources":"[{\"name\":\"erp\",\"kind\":\"database\",\"type\":\"postgres\",\"dbname\":\"erp\"}]","roles":"[]"}}}`))
	}))
	defer gj.Close()
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode) values
		('org', 'graphjin', 'erp', $1, true, true, 'jwt'),
		('org', 'graphjin', 'down', 'http://127.0.0.1:1/api/v1/graphql', true, false, 'jwt'),
		('org', 'graphjin', 'internal', 'http://127.0.0.1:8089/api/v1/graphql', true, false, 'jwt'),
		('org', 'graphjin', 'off', $1, false, false, 'jwt')`, gj.URL+"/api/v1/graphql")
	v, raw := read()
	if strings.Contains(raw, pass) {
		t.Fatalf("view holds the secret: %s", raw)
	}
	if v.File.YAML == nil || !strings.Contains(*v.File.YAML, `password: "[REDACTED]"`) || strings.Contains(*v.File.YAML, "#") {
		t.Errorf("file: %+v", v.File)
	}
	if s := v.File.State; s == nil || s.State != "restarting" || s.Restarts != 2 || s.Reason == nil || !strings.Contains(*s.Reason, "[REDACTED]@h") {
		t.Errorf("state: %+v", v.File.State)
	}
	if len(v.Descriptions) != 2 || v.Descriptions[0].Source["name"] != "down" || v.Descriptions[0].Error == nil ||
		v.Descriptions[1].Source["name"] != "erp" || v.Descriptions[1].Error != nil {
		t.Fatalf("descriptions: %s", raw)
	}
	if !strings.Contains(raw, `"summary":{"sources":[{"name":"erp","kind":"database","sentences":["A PostgreSQL source: database erp`) {
		t.Errorf("summary: %s", raw)
	}
}
