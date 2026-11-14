package webfront

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
)

func magentoTestStore(t *testing.T) (magentoPreflightInput, func(string)) {
	t.Helper()
	dsn := os.Getenv("OPENNEKO_TEST_MAGENTO_MYSQL")
	if dsn == "" {
		t.Skip("OPENNEKO_TEST_MAGENTO_MYSQL is not set (a root DSN of a MySQL 8 or MariaDB 10.6+ server)")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MultiStatements = true
	connector, _ := mysql.NewConnector(cfg)
	root := sql.OpenDB(connector)
	t.Cleanup(func() { root.Close() })
	name := fmt.Sprintf("magento_%d", time.Now().UnixNano())
	exec := func(stmt string) {
		t.Helper()
		if _, err := root.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	exec("CREATE DATABASE " + name)
	t.Cleanup(func() {
		_, _ = root.Exec("DROP DATABASE " + name)
		_, _ = root.Exec("DROP USER IF EXISTS '" + name + "'@'%'")
	})
	for _, table := range magentoRequiredTables {
		exec(fmt.Sprintf("CREATE TABLE %s.m2_%s (id INT)", name, table))
	}
	exec(fmt.Sprintf("CREATE TABLE %s.m2_admin_user (id INT)", name))
	exec(fmt.Sprintf("DROP TABLE %[1]s.m2_store; CREATE TABLE %[1]s.m2_store (store_id INT, code VARCHAR(32), website_id INT, group_id INT, name VARCHAR(64), is_active INT)", name))
	exec(fmt.Sprintf("INSERT INTO %s.m2_store VALUES (0, 'admin', 0, 0, 'Admin', 1), (1, 'default', 1, 1, 'Default', 1), (2, 'de', 1, 2, 'DE', 1), (3, 'old', 1, 3, 'Old', 0)", name))
	exec(fmt.Sprintf("CREATE TABLE %s.m2_core_config_data (scope VARCHAR(8), scope_id INT, path VARCHAR(255), value TEXT)", name))
	exec(fmt.Sprintf("DROP TABLE %[1]s.m2_cron_schedule; CREATE TABLE %[1]s.m2_cron_schedule (job_code VARCHAR(255), status VARCHAR(16), executed_at DATETIME)", name))
	exec(fmt.Sprintf("CREATE USER '%[1]s'@'%%' IDENTIFIED BY 'pw'; GRANT SELECT ON %[1]s.* TO '%[1]s'@'%%'", name))
	host, port, _ := net.SplitHostPort(cfg.Addr)
	var portNumber float64
	_, _ = fmt.Sscan(port, &portNumber)
	return magentoPreflightInput{host: host, port: portNumber, database: name, username: name, password: "pw", baseURL: "https://shop.test", storeCode: "all"}, exec
}

func magentoTestShop(status map[string]int) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body, code := "{}", 200
		if r.URL.Path == "/magento_version" {
			body = "Magento/2.4.7 (Community)"
		}
		for part, c := range status {
			if strings.Contains(r.URL.Path, part) {
				code = c
			}
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	})}
}

type magentoGraphjin struct {
	mu      sync.Mutex
	applies []string
	queries []string
}

func (g *magentoGraphjin) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct{ Query string }
		_ = json.Unmarshal(raw, &body)
		reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gj_config": v}}) }
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case strings.Contains(body.Query, "PackGraphjinTarget"):
			reply(map[string]any{"serv": map[string]any{"production": true}})
		case strings.Contains(body.Query, "PackConfigRevision_"):
			reply(map[string]any{"catalog_revision": strings.Repeat("a", 64), "sources": `[]`, "tables": []any{}, "relationships": []any{}})
		case strings.Contains(body.Query, `mode: "preview"`):
			reply(map[string]any{"valid": true, "preview_id": "p1"})
		case strings.Contains(body.Query, `mode: "apply"`):
			g.applies = append(g.applies, body.Query)
			reply(map[string]any{"applied": true, "catalog_revision": "c2"})
		case strings.Contains(body.Query, "protect_code"):
			g.queries = append(g.queries, body.Query)
			_, _ = io.WriteString(w, `{"errors":[{"message":"column blocked"}]}`)
		case strings.Contains(body.Query, "sales_order"):
			g.queries = append(g.queries, body.Query)
			_, _ = io.WriteString(w, `{"data":{"sales_order":[]}}`)
		default:
			_, _ = io.WriteString(w, `{"errors":[{"message":"unexpected"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMagentoPackApplyRoute(t *testing.T) {
	store, exec := magentoTestStore(t)
	exec(fmt.Sprintf("INSERT INTO %s.m2_core_config_data VALUES ('default', 0, 'currency/options/base', 'EUR'), ('default', 0, 'general/locale/timezone', 'Europe/Berlin')", store.database))
	f, wk, admin := packCatalogFront(t)
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("magento-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configFile := filepath.Join(dir, "prod.yml")
	if err := os.WriteFile(configFile, []byte("sources: []\ntables: []\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", configFile)
	fakeSupervisor(t, dir)
	gj := &magentoGraphjin{}
	srv := gj.server(t)
	seed(t, f.API.DB, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt')`, srv.URL)
	old := magentoHTTPClient
	magentoHTTPClient = magentoTestShop(map[string]int{"/cmsPage/": 403})
	t.Cleanup(func() { magentoHTTPClient = old })

	body, _ := json.Marshal(map[string]any{
		"idempotencyKey": "m1",
		"inputs": map[string]any{"database.host": store.host, "database.port": store.port, "database.name": store.database,
			"magento.base_url": store.baseURL, "magento.store_code": "all"},
		"secrets": map[string]any{"database.analytics_username": store.username, "database.analytics_password": store.password,
			"magento.integration_token": "tok-1"},
	})
	w := call(f, "POST", "/api/v1/admin/packs/magento/install", string(body), admin)
	if w.Code != 200 || decode(t, w)["status"] != "installed" {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("the worker was called: %v", calls)
	}
	scalar := func(q string) string {
		t.Helper()
		var out string
		if err := f.API.DB.QueryRow(context.Background(), q).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	if got := scalar(`select config->>'magentoVersion' || ',' || (config->>'database.type') || ',' || (config->>'magento.table_prefix') || ',' ||
		(config->>'magento.base_currency') || ',' || (config->>'magento.timezone') from pack_install where pack_id = 'magento'`); got != "2.4.7,"+
		strings.Split(got, ",")[1]+",m2_,EUR,Europe/Berlin" || !strings.Contains("mysql mariadb", strings.Split(got, ",")[1]) {
		t.Errorf("config %s", got)
	}
	if got := scalar(`select string_agg(domain || '=' || enabled::text || ':' || risk_class::text, ',' order by domain) from magento_store_control`); got !=
		"catalog=true:2,content=true:2,customers=false:1,inventory=true:2,orders=true:2,promotions=true:1" {
		t.Errorf("controls %s", got)
	}
	if got := scalar(`select (caps->>'maxStoreCredit') || ',' || jsonb_array_length(scope->'stores')::text || ',' || (scope->'stores'->1->>'code')
		from magento_store_control where domain = 'catalog'`); got != "50,2,de" {
		t.Errorf("caps and scope %s", got)
	}
	if got := scalar(`select count(*)::text from magento_attribute_classification where pack_default and reviewed`); got != fmt.Sprint(len(magentoPackClassifications)) {
		t.Errorf("classifications %s", got)
	}
	if got := scalar(`select (definition_json->'execution'->'runtime'->>'storeIds') || ',' || (definition_json->'execution'->'runtime'->>'currency')
		from metric where slug = (select slug from metric limit 1)`); got != "[1, 2],EUR" {
		t.Errorf("metric runtime %s", got)
	}
	if got := scalar(`select variables_json->'storeIds'->>'value' from watcher limit 1`); got != "[1, 2]" {
		t.Errorf("watcher variables %s", got)
	}
	if got := scalar(`select string_agg(kind || '=' || readiness || ':' || coalesce(readiness_reason, ''), ',' order by kind) from pack_action_definition
		where kind in ('magento.manage_content', 'magento.manage_catalog', 'magento.manage_customers', 'magento.financial_handoff')`); got !=
		"magento.financial_handoff=ready:,magento.manage_catalog=ready:,magento.manage_content=blocked:acl_missing,magento.manage_customers=blocked:domain_disabled" {
		t.Errorf("actions %s", got)
	}
	if got := scalar(`select readiness || ',' || readiness_reason from pack_artifact where artifact_key = 'source.magento_operator'`); got != "blocked,operator:operator:acl_missing" {
		t.Errorf("operator source %s", got)
	}
	if got := scalar(`select readiness || ',' || readiness_reason from pack_artifact where artifact_kind = 'action' and target_ref like '%manage_catalog%'`); got != "ready,operator:catalog:ready" {
		t.Errorf("catalog action receipt %s", got)
	}
	if got := scalar(`select count(*)::text from pack_artifact where readiness = 'ready' and readiness_reason is null`); got == "0" {
		t.Error("no plain artifact")
	}
	raw, _ := os.ReadFile(configFile)
	var written struct {
		Sources []map[string]any
		Tables  []map[string]any
	}
	if err := yaml.Unmarshal(raw, &written); err != nil {
		t.Fatal(err)
	}
	names := map[string]map[string]any{}
	for _, s := range written.Sources {
		names[s["name"].(string)] = s
	}
	analytics, operator := names["magento_analytics"], names["magento_operator"]
	if analytics == nil || operator == nil || operator["read_only"] != false {
		t.Fatalf("sources %v", written.Sources)
	}
	if blocked := fmt.Sprint(analytics["access"].(map[string]any)["blocked_tables"]); !strings.Contains(blocked, "m2_admin_user") || strings.Contains(blocked, "m2_sales_order ") {
		t.Errorf("blocked tables %s", blocked)
	}
	var order map[string]any
	for _, table := range written.Tables {
		if table["name"] == "sales_order" {
			order = table
		}
	}
	if order == nil || order["table"] != "m2_sales_order" || fmt.Sprint(order["blocklist"]) != "[protect_code]" {
		t.Errorf("tables %v", written.Tables)
	}
	if len(gj.applies) != 1 || len(gj.queries) != 3 {
		t.Errorf("applies %d smoke %d", len(gj.applies), len(gj.queries))
	}

	w = call(f, "GET", "/api/v1/admin/packs/magento/plan", "", admin)
	for _, e := range decode(t, w)["entries"].([]any) {
		if entry := e.(map[string]any); entry["action"] != "noop" {
			t.Errorf("after install: %v", entry)
		}
	}
	reconfigure, _ := json.Marshal(map[string]any{"idempotencyKey": "m2", "inputs": map[string]any{"magento.customers_enabled": true}})
	if w := call(f, "POST", "/api/v1/admin/packs/magento/configure", string(reconfigure), admin); w.Code != 200 {
		t.Fatalf("configure: %d %s", w.Code, w.Body.String())
	}
	if got := scalar(`select enabled::text from magento_store_control where domain = 'customers'`); got != "true" {
		t.Errorf("customers after configure %s", got)
	}
	if got := scalar(`select readiness from pack_action_definition where kind = 'magento.manage_customers'`); got != "ready" {
		t.Errorf("customers action after configure %s", got)
	}
	if got := scalar(`select string_agg(operation_type || '=' || status, ',' order by created_at) from pack_operation`); got != "install=succeeded,configure=succeeded" {
		t.Errorf("operations %s", got)
	}
}

func TestMagentoPackApplyRefusesAPIMode(t *testing.T) {
	store, _ := magentoTestStore(t)
	f, _, admin := packCatalogFront(t)
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(f.API.SecretsDir, "secret-key"), []byte("magento-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gj := &fakePackGraphjin{}
	srv := gj.server(t, map[string]any{"name": "other"}, "")
	seed(t, f.API.DB, `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt')`, srv.URL)
	old := magentoHTTPClient
	magentoHTTPClient = magentoTestShop(nil)
	t.Cleanup(func() { magentoHTTPClient = old })
	body, _ := json.Marshal(map[string]any{
		"idempotencyKey": "a1",
		"inputs":         map[string]any{"database.host": store.host, "database.port": store.port, "database.name": store.database, "magento.base_url": store.baseURL},
		"secrets":        map[string]any{"database.analytics_username": store.username, "database.analytics_password": store.password},
	})
	w := call(f, "POST", "/api/v1/admin/packs/magento/install", string(body), admin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "needs the GraphJin whose config folder OpenNeko manages") {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}
	var status, compensation string
	if err := f.API.DB.QueryRow(context.Background(), `select o.status, o.compensation_status from pack_operation o where o.idempotency_key = 'a1'`).
		Scan(&status, &compensation); err != nil || status != "failed" || compensation != "succeeded" {
		t.Errorf("operation %s %s %v", status, compensation, err)
	}
	if len(gj.take()) != 0 {
		t.Error("GraphJin was changed")
	}
}

func TestMagentoGraphjinUpdateMatchesNode(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "packs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENNEKO_PACKS_DIR", abs)
	raw, err := os.ReadFile("testdata/nodemagentoupdate.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Classifications []magentoClassification
		Updates         []struct {
			Name      string
			Inputs    map[string]any
			Secrets   map[string]string
			Preflight struct {
				DatabaseType, TablePrefix               string
				AvailableAnalyticsTables, BlockedTables []string
			}
			Retired []string
			Update  map[string]any
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.Classifications, magentoPackClassifications) {
		t.Errorf("classifications differ from Node:\n got %v\nwant %v", magentoPackClassifications, fixture.Classifications)
	}
	cases := fixture.Updates
	if len(cases) != 2 {
		t.Fatalf("cases: %d", len(cases))
	}
	bundle, err := packBundle("org", "magento", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		preflight := &magentoPreflightResult{databaseType: c.Preflight.DatabaseType, tablePrefix: c.Preflight.TablePrefix,
			availableTables: c.Preflight.AvailableAnalyticsTables, blockedTables: c.Preflight.BlockedTables}
		got, err := magentoGraphjinUpdate(c.Inputs, c.Secrets, preflight, bundle, c.Retired)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		gotText, _ := packs.CanonicalJSON(plainJSON(jsValueOf(got)))
		wantText, _ := packs.CanonicalJSON(c.Update)
		if gotText != wantText {
			t.Errorf("%s:\n got %s\nwant %s", c.Name, gotText, wantText)
		}
	}
}
