package webfront

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/open-neko/neko/apps/openneko/internal/packs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gqlFormat(t *testing.T, text string) string {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Input: text})
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	var out strings.Builder
	formatter.NewFormatter(&out, formatter.WithIndent("  ")).FormatQueryDocument(doc)
	return out.String()
}

func TestPackDoctorNodeFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "nodedoctor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Bound []struct {
			Sources  []packs.Artifact
			Queries  []packs.Artifact
			Bindings map[string]string
			Bound    map[string]string
		}
		Databases []struct {
			Version string
			Type    *string
			Error   *string
		}
		Readiness []struct {
			Token            *string
			CustomersEnabled bool
			Statuses         map[string]any
			Requests         []string
			Result           struct {
				Overall string
				Domains map[string]string
			}
		}
		Details []struct {
			Reason *string
			Detail string
		}
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Bound) != 5 || len(fx.Databases) != 8 || len(fx.Readiness) != 8 || len(fx.Details) != 7 {
		t.Fatalf("fixture counts: %d %d %d %d", len(fx.Bound), len(fx.Databases), len(fx.Readiness), len(fx.Details))
	}

	for i, c := range fx.Bound {
		bundle := &packs.Bundle{Manifest: &packs.Manifest{}, Artifacts: append(slices.Clone(c.Sources), c.Queries...)}
		bundle.Manifest.Metadata.ID = "tbl"
		_, got, err := bindPackQueries(bundle, c.Bindings)
		if err != nil {
			t.Fatalf("bound %d: %v", i, err)
		}
		for _, q := range c.Queries {
			text, ok := got[q.Path]
			if !ok {
				text = q.Content.(string)
			}
			if want := gqlFormat(t, c.Bound[q.Path]); gqlFormat(t, text) != want {
				t.Errorf("bound %d %s:\n got %s\nwant %s", i, q.Path, text, want)
			}
		}
	}

	for _, c := range fx.Databases {
		typ, err := supportedMagentoDatabase(c.Version)
		if c.Error != nil {
			if err == nil || err.Error() != *c.Error {
				t.Errorf("database %q: got %q %v, want %s", c.Version, typ, err, *c.Error)
			}
		} else if err != nil || typ != *c.Type {
			t.Errorf("database %q: got %q %v, want %s", c.Version, typ, err, *c.Type)
		}
	}

	for i, c := range fx.Readiness {
		var requests []string
		var mu sync.Mutex
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			requests = append(requests, r.URL.String()+" "+r.Header.Get("Authorization"))
			mu.Unlock()
			status := 200
			for part, s := range c.Statuses {
				if strings.Contains(r.URL.String(), "/V1/"+part) {
					if s == "error" {
						return nil, errors.New("connection refused")
					}
					status = int(s.(float64))
				}
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
		}), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		in := magentoPreflightInput{baseURL: "https://shop.test/", storeCode: "de store", customersEnabled: c.CustomersEnabled}
		if c.Token != nil {
			in.integrationToken = *c.Token
		}
		overall, domains := magentoOperatorReadiness(context.Background(), in, client)
		if overall != c.Result.Overall || !reflect.DeepEqual(domains, c.Result.Domains) {
			t.Errorf("readiness %d: got %s %v, want %s %v", i, overall, domains, c.Result.Overall, c.Result.Domains)
		}
		if !slices.Equal(requests, c.Requests) && len(requests)+len(c.Requests) > 0 {
			t.Errorf("readiness %d requests:\n got %v\nwant %v", i, requests, c.Requests)
		}
	}

	for _, c := range fx.Details {
		reason := ""
		if c.Reason != nil {
			reason = *c.Reason
		}
		if got := operatorReadinessDetail(reason); got != c.Detail {
			t.Errorf("detail %q: got %q", reason, got)
		}
	}
}

func TestMagentoGrantsAndPrefix(t *testing.T) {
	for grant, want := range map[string]string{
		"GRANT USAGE ON *.* TO `ro`@`%`":                                "",
		"GRANT SELECT, SHOW VIEW ON `shop`.* TO `ro`@`%`":               "",
		"GRANT SELECT, INSERT ON `shop`.* TO `ro`@`%`":                  "analytics account is not read-only (unexpected grants: INSERT)",
		"GRANT ALL PRIVILEGES ON `shop`.* TO `ro`@`%`":                  "analytics account is not read-only (unexpected grants: ALL PRIVILEGES)",
		"GRANT SELECT ON `shop`.* TO `ro`@`%` WITH GRANT OPTION":        "analytics account must not have GRANT OPTION",
		"GRANT `role_a`@`%` TO `ro`@`%`":                                "",
		"GRANT SELECT, LOCK TABLES, PROCESS ON *.* TO `ro`@`localhost`": "analytics account is not read-only (unexpected grants: LOCK TABLES, PROCESS)",
	} {
		err := checkMagentoGrants([]string{grant})
		if got := fmt.Sprint(err); want == "" && err != nil || want != "" && got != want {
			t.Errorf("%s: got %v, want %q", grant, err, want)
		}
	}
	full := func(prefix string) []string {
		var out []string
		for _, table := range magentoRequiredTables {
			out = append(out, prefix+table)
		}
		return out
	}
	for _, c := range []struct {
		tables     []string
		configured string
		want, err  string
	}{
		{full(""), "", "", ""},
		{full("m2_"), "", "m2_", ""},
		{full("m2_"), "m2_", "m2_", ""},
		{append(full("a_"), full("b_")...), "", "", "multiple Magento table prefixes detected: a_, b_"},
		{append(full(""), full("b_")...), "", "", "multiple Magento table prefixes detected: <empty>, b_"},
		{append(full(""), full("b_")...), "b_", "b_", ""},
		{full("m2_")[1:], "", "", "Magento analytics tables are missing or the table prefix is incorrect"},
		{full("m2_"), "m2-", "", "magento.table_prefix may contain letters, numbers, and underscores only"},
	} {
		got, err := detectMagentoTablePrefix(c.tables, c.configured)
		if got != c.want || fmt.Sprint(err) != cmpOr(c.err, "<nil>") {
			t.Errorf("prefix %q of %d tables: got %q %v, want %q %s", c.configured, len(c.tables), got, err, c.want, c.err)
		}
	}
}

func TestReadMagentoVersion(t *testing.T) {
	var gotHost string
	reply := map[string]func(w http.ResponseWriter, r *http.Request){}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		reply[strings.Split(r.Host, ":")[0]](w, r)
	}))
	t.Cleanup(srv.Close)
	dialer := &net.Dialer{}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, srv.Listener.Addr().String())
	}}}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	base := "http://shop.test:" + port + "/"

	for body, want := range map[string]string{"Magento/2.4 (Community)": "2.4.x", "Magento/2.4.7-p3 (Community)": "2.4.7-p3", "Magento/2.3 (Community)": ""} {
		reply["shop.test"] = func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }
		got, err := readMagentoVersion(context.Background(), base, client)
		if want == "" && (err == nil || err.Error() != "Magento version endpoint did not report a supported 2.4.x version") || want != "" && got != want {
			t.Errorf("%s: got %q %v", body, got, err)
		}
	}
	reply["shop.test"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }
	if _, err := readMagentoVersion(context.Background(), base, client); err == nil || err.Error() != "Magento version preflight failed: HTTP 503" {
		t.Errorf("503: %v", err)
	}

	reply["shop.test"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "localhost:8080" {
			_, _ = io.WriteString(w, "Magento/2.4.6 (Community)")
			return
		}
		http.Redirect(w, r, "http://localhost:8080/magento_version", http.StatusFound)
	}
	reply["localhost"] = func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "Magento/2.4.6 (Community)") }
	if got, err := readMagentoVersion(context.Background(), base, client); err != nil || got != "2.4.6" || gotHost != "localhost:8080" {
		t.Errorf("loopback redirect: %q %v host %s", got, err, gotHost)
	}

	reply["shop.test"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://www.shop.test:"+port+"/magento_version", http.StatusMovedPermanently)
	}
	reply["www.shop.test"] = func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "Magento/2.4.8") }
	if got, err := readMagentoVersion(context.Background(), base, client); err != nil || got != "2.4.8" {
		t.Errorf("redirect: %q %v", got, err)
	}
}

func TestPackDoctorRoute(t *testing.T) {
	f, wk, admin := reviewFront(t)
	db := f.API.DB
	if err := secrets.Write(secrets.Store{"pack.service-health": {"SERVICE_API_TOKEN": "t"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	doctor := func(pack string) (int, string) {
		t.Helper()
		w := call(f, "GET", "/api/v1/admin/packs/"+pack+"/doctor", "", admin)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if code, body := doctor("service-health"); code != 200 || body != `{"packId":"service-health","status":"blocked","checks":[{"id":"installation","status":"blocked","detail":"pack is not installed"}]}` {
		t.Errorf("not installed: %d %s", code, body)
	}

	var mu sync.Mutex
	var queries []string
	healthy := "1"
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		queries = append(queries, r.Header.Get("X-Role")+" "+string(raw))
		mu.Unlock()
		if healthy == "error" {
			_, _ = io.WriteString(w, `{"errors":[{"message":"no such operation"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"service_health_get_health_summary":{"healthy":`+healthy+`}}}`)
	}))
	t.Cleanup(gj.Close)
	var sourceID string
	if err := db.QueryRow(context.Background(), `insert into data_source (org_id, kind, name, graphql_url, enabled, is_default, auth_mode)
		values ('org', 'graphjin', 'GJ', $1, true, true, 'jwt') returning id::text`, gj.URL).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	config := `{"service.base_url":"https://h.test","service.timezone":"UTC","_runtime":{"source":{"id":"` + sourceID + `","graphqlUrl":"` + gj.URL + `","authMode":"jwt"},"bindings":{},"bindingHashes":{},"readiness":["reporting"],"tables":[]}}`
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values ('org', 'service-health', '0.1.0', 'installed', 'h', '`+config+`')`)

	if code, body := doctor("service-health"); code != 200 || body != `{"packId":"service-health","status":"ready","checks":[{"id":"queries","status":"ready","detail":"Pack queries and response mappings passed."}]}` {
		t.Errorf("ready: %d %s", code, body)
	}
	if len(queries) != 1 || !strings.HasPrefix(queries[0], `service {"query":"query ServiceHealth {`) || !strings.HasSuffix(queries[0], `"variables":{}}`) {
		t.Errorf("one query for the metric, the watcher and the saved query: %q", queries)
	}
	blocked := `{"packId":"service-health","status":"blocked","checks":[{"id":"queries","status":"blocked","detail":"Pack configuration or query preflight failed."}]}`
	for _, h := range []string{"error", "null"} {
		healthy = h
		if code, body := doctor("service-health"); code != 200 || body != blocked {
			t.Errorf("healthy %s: %d %s", h, code, body)
		}
	}
	healthy = "1"
	if err := secrets.Write(secrets.Store{}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	if code, body := doctor("service-health"); code != 200 || body != blocked {
		t.Errorf("missing secret: %d %s", code, body)
	}

	if err := secrets.Write(secrets.Store{"pack.service-health": {"SERVICE_API_TOKEN": "t"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `update data_source set graphql_url = 'http://changed.test' where id::text = '`+sourceID+`'`)
	if code, body := doctor("service-health"); code != 200 || body != blocked {
		t.Errorf("changed source: %d %s", code, body)
	}

	if err := os.CopyFS(filepath.Join(os.Getenv("OPENNEKO_PACKS_DIR"), "magento"), os.DirFS(filepath.Join("..", "..", "..", "..", "packs", "magento"))); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Write(secrets.Store{"pack.magento": {"DATABASE_ANALYTICS_USERNAME": "ro", "DATABASE_ANALYTICS_PASSWORD": "pw"}}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	port := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	magentoConfig := fmt.Sprintf(`{"magento.base_url":"https://shop.test","database.host":"127.0.0.1","database.port":%d,"database.name":"shop"}`, port)
	seed(t, db, `insert into pack_install (org_id, pack_id, version, status, manifest_hash, config) values ('org', 'magento', '2.0.0', 'installed', 'h', '`+magentoConfig+`')`)
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", "")
	code, body := doctor("magento")
	var got struct {
		PackID, Status string
		Checks         []struct{ ID, Status, Detail string }
	}
	_ = json.Unmarshal([]byte(body), &got)
	if code != 200 || got.Status != "blocked" || len(got.Checks) != 3 ||
		got.Checks[0].ID != "analytics" || got.Checks[0].Status != "blocked" || !strings.Contains(got.Checks[0].Detail, "connection refused") ||
		got.Checks[1] != struct{ ID, Status, Detail string }{"graphjin", "blocked", "customer GraphJin endpoint/config volume is unavailable"} ||
		got.Checks[2] != struct{ ID, Status, Detail string }{"changes", "optional", operatorReadinessDetail("")} {
		t.Errorf("magento without a database: %d %s", code, body)
	}
	if calls := wk.take(); len(calls) != 0 {
		t.Errorf("worker calls: %v", calls)
	}
	if w := call(f, "GET", "/api/v1/admin/packs/magento/doctor", "", sessionCookie("member")); w.Code != 403 {
		t.Errorf("member: %d", w.Code)
	}
}

func TestMagentoPreflightDatabase(t *testing.T) {
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
	name := fmt.Sprintf("doctor_%d", time.Now().UnixNano())
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
	exec(fmt.Sprintf("DROP TABLE %[1]s.m2_store; CREATE TABLE %[1]s.m2_store (store_id INT, code VARCHAR(32), website_id INT, group_id INT, name VARCHAR(64), is_active INT)", name))
	exec(fmt.Sprintf("INSERT INTO %s.m2_store VALUES (0, 'admin', 0, 0, 'Admin', 1), (1, 'default', 1, 1, 'Default', 1), (2, 'de', 1, 2, 'DE', 1), (3, 'old', 1, 3, 'Old', 0)", name))
	exec(fmt.Sprintf("CREATE TABLE %s.m2_core_config_data (scope VARCHAR(8), scope_id INT, path VARCHAR(255), value TEXT)", name))
	exec(fmt.Sprintf("DROP TABLE %[1]s.m2_cron_schedule; CREATE TABLE %[1]s.m2_cron_schedule (job_code VARCHAR(255), status VARCHAR(16), executed_at DATETIME)", name))
	exec(fmt.Sprintf("CREATE USER '%[1]s'@'%%' IDENTIFIED BY 'pw'; GRANT SELECT ON %[1]s.* TO '%[1]s'@'%%'", name))

	host, port, _ := net.SplitHostPort(cfg.Addr)
	var portNumber float64
	_, _ = fmt.Sscan(port, &portNumber)
	shop := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.URL.Path == "/magento_version" {
			body = "Magento/2.4.7 (Community)"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	})}
	in := magentoPreflightInput{host: host, port: portNumber, database: name, username: name, password: "pw", baseURL: "https://shop.test", storeCode: "all"}

	got, err := runMagentoPreflight(context.Background(), in, shop)
	if err != nil {
		t.Fatal(err)
	}
	if got.tablePrefix != "m2_" || !slices.Equal(got.storeIDs, []int64{1, 2}) || got.magentoVersion != "2.4.7" ||
		got.operatorReadiness != "integration_token_missing" || got.bulkConsumerReadiness != "bulk_consumers_not_running" ||
		got.databaseType != "mysql" && got.databaseType != "mariadb" {
		t.Errorf("preflight: %+v", got)
	}
	exec(fmt.Sprintf("INSERT INTO %s.m2_cron_schedule VALUES ('consumers_runner', 'success', UTC_TIMESTAMP() - INTERVAL 5 MINUTE)", name))
	in.storeCode, in.integrationToken = "de", "t"
	if got, err = runMagentoPreflight(context.Background(), in, shop); err != nil || !slices.Equal(got.storeIDs, []int64{2}) ||
		got.bulkConsumerReadiness != "ready" || got.operatorReadiness != "ready" {
		t.Errorf("store de: %+v %v", got, err)
	}
	in.storeCode = "old"
	if _, err := runMagentoPreflight(context.Background(), in, shop); err == nil || err.Error() != `Magento store code "old" is missing or inactive` {
		t.Errorf("inactive store: %v", err)
	}
	exec(fmt.Sprintf("GRANT INSERT ON %[1]s.* TO '%[1]s'@'%%'", name))
	in.storeCode = "all"
	if _, err := runMagentoPreflight(context.Background(), in, shop); err == nil || err.Error() != "analytics account is not read-only (unexpected grants: INSERT)" {
		t.Errorf("write grant: %v", err)
	}
}
