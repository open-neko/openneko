package webfront

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProfileBehaviorThresholds(t *testing.T) {
	for _, c := range []struct {
		profile, cp, actions string
		want                 behaviorThresholds
	}{
		{"", "", "", behaviorThresholds{200, 50, 100}},
		{"fortress", "", "", behaviorThresholds{200, 50, 100}},
		{"org", "", "", behaviorThresholds{100, 25, 50}},
		{"Hardened", "", "", behaviorThresholds{50, 12, 25}},
		{"hardened", "3", "abc", behaviorThresholds{1, 12, 25}},
		{"solo", "7.9", "-4", behaviorThresholds{7, 50, 100}},
	} {
		t.Setenv("OPENNEKO_PROFILE", c.profile)
		t.Setenv("OPENNEKO_BEHAVIOR_MAX_CP_CALLS_PER_RUN_10M", c.cp)
		t.Setenv("OPENNEKO_BEHAVIOR_MAX_ACTIONS_PER_HOUR", c.actions)
		os.Unsetenv("OPENNEKO_BEHAVIOR_MAX_MEMORY_WRITES_PER_HOUR")
		if got := profileBehaviorThresholds(); got != c.want {
			t.Errorf("%q cp=%q actions=%q: %+v, want %+v", c.profile, c.cp, c.actions, got, c.want)
		}
	}
}

func TestBehaviorSweep(t *testing.T) {
	f, db := memoryFront(t)
	ctx := context.Background()
	worker := &setupWorker{status: `{"provider":null}`, reply: map[string]string{"/admin/events/external": `{"matched":0}`}}
	srv := httptest.NewServer(worker)
	t.Cleanup(srv.Close)
	f.API.Gate.WorkerURL = srv.URL
	const run = "f0000000-0000-4000-8000-000000000001"
	seed(t, db, `insert into control_plane_audit (org_id, run_id, path, actor_role, backend)
		select 'org', $1, '/v1/memory/search', 'service', 'ax' from generate_series(1, 9)`, run)
	seed(t, db, `insert into control_plane_audit (org_id, run_id, path, actor_role, backend, created_at)
		select 'org', gen_random_uuid(), '/v1/memory/search', 'service', 'ax', now() - interval '11 minutes' from generate_series(1, 9)`)
	seed(t, db, `insert into work_memory_event (org_id, action, payload) select 'org', 'remember', '{}' from generate_series(1, 12)`)
	limits := behaviorThresholds{ControlPlaneCallsPerRun: 5, ActionRequestsPerHour: 1000, MemoryWritesPerHour: 8}

	var writes int
	_ = db.QueryRow(ctx, `select count(*) from work_memory_event where org_id = 'org' and action = 'remember' and created_at >= now() - interval '1 hour'`).Scan(&writes)
	now := time.Now()
	got, err := f.API.behaviorSweep(ctx, "org", limits, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []behaviorAlert{{"control_plane_call_volume", run, 9, 5, 600}, {"memory_write_volume", "org", writes, 8, 3600}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("alerts %+v, want %+v", got, want)
	}
	again, err := f.API.behaviorSweep(ctx, "org", limits, now.Add(time.Minute))
	if err != nil || len(again) != 0 {
		t.Fatalf("second sweep in the window: %+v %v", again, err)
	}
	var rows int
	_ = db.QueryRow(ctx, `select count(*) from behavior_alert where org_id = 'org'`).Scan(&rows)
	if rows != 2 {
		t.Errorf("%d behavior_alert rows, want 2", rows)
	}
	worker.mu.Lock()
	calls := strings.Join(worker.calls, "\n")
	worker.mu.Unlock()
	bucket := now.UnixMilli() / 600_000
	wantCall := `POST /admin/events/external {"dedupeKey":"control_plane_call_volume:` + run + `:` + itoa(bucket) +
		`","name":"security.behavior_threshold","orgId":"org","payload":{"kind":"control_plane_call_volume","observed":9,"subject":"` + run +
		`","threshold":5,"windowSeconds":600},"source":"behavior-monitor"}`
	if !strings.Contains(calls, wantCall) || strings.Count(calls, "/admin/events/external") != 2 {
		t.Errorf("worker calls:\n%s\nwant:\n%s", calls, wantCall)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestWatcherCondition(t *testing.T) {
	data, _ := jsParse([]byte(`{"orders":[{"count":7}],"totals":{"revenue":1200},"s":"x"}`))
	for path, want := range map[string]string{"orders.0.count": "7", "totals.revenue": "1200", "orders.1.count": "", "missing.path": "",
		"orders..0.count": "7", "s.length": "", "orders.length": ""} {
		got, ok := jsonText(extractValueAtPath(data, path))
		if !ok {
			got = ""
		}
		if got != want {
			t.Errorf("path %s: %q, want %q", path, got, want)
		}
	}
	undef := any(jsUndefined{})
	for _, c := range []struct {
		op                     string
		value, threshold, last any
		want                   bool
	}{
		{"gt", 5.0, 3.0, nil, true},
		{"gte", 3.0, 3.0, nil, true},
		{"lt", 2.0, 3.0, nil, true},
		{"lte", 3.0, 3.0, nil, true},
		{"gt", "not-a-number", 3.0, nil, false},
		{"gt", "5", 3.0, nil, true},
		{"gt", []any{"5"}, 3.0, nil, true},
		{"gt", true, 0.0, nil, true},
		{"gt", undef, 0.0, nil, false},
		{"lte", nil, 0.0, nil, true},
		{"eq", "P0", "P0", nil, true},
		{"ne", "P1", "P0", nil, true},
		{"eq", undef, nil, nil, false},
		{"changed", 5.0, nil, undef, false},
		{"changed", 5.0, nil, nil, false},
		{"changed", 5.0, nil, 5.0, false},
		{"changed", 6.0, nil, 5.0, true},
		{"changed", undef, nil, 5.0, true},
	} {
		if got := watcherConditionMet(c.op, c.value, c.threshold, c.last); got != c.want {
			t.Errorf("%s %v %v last %v: %v", c.op, c.value, c.threshold, c.last, got)
		}
	}
	if !math.IsNaN(jsToNumber(&jsObj{})) || jsToNumber([]any{}) != 0 || !math.IsNaN(jsToNumber([]any{1.0, 2.0})) {
		t.Error("Number() of an object or array")
	}
}

func TestResolveWatcherVariables(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	def, _ := jsParse([]byte(`{"now":{"kind":"now"},"since":{"kind":"seconds_ago","seconds":90},"sku":{"kind":"literal","value":["a",1]}}`))
	got, err := resolveWatcherVariables(def, now)
	if err != nil || jsStringify(got, false) != `{"now":"2026-11-12T10:00:00.000Z","since":"2026-11-12T09:58:30.000Z","sku":["a",1]}` {
		t.Fatalf("%s %v", jsStringify(got, false), err)
	}
	for raw, want := range map[string]string{
		`{"x":1}`: "watcher variable x must be a descriptor",
		`{"x":{"kind":"seconds_ago","seconds":-1}}`: "watcher variable x.seconds must be non-negative",
		`{"x":{"kind":"seconds_ago"}}`:              "watcher variable x.seconds must be non-negative",
		`{"x":{"kind":"tomorrow"}}`:                 "watcher variable x uses unsupported kind tomorrow",
		`{"x":{}}`:                                  "watcher variable x uses unsupported kind undefined",
	} {
		def, _ := jsParse([]byte(raw))
		if _, err := resolveWatcherVariables(def, now); err == nil || err.Error() != want {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestGraphjinConfigPatchHash(t *testing.T) {
	v, err := yamlToJS([]byte("name: main\nType: postgres\nport: 5432\na_b: 1\nab: 2\na1: 3\nnested:\n  z: true\n  B: ~\n  b: é\n  list: [1.5, x, {k: v}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := graphjinConfigPatchHash(v); got != "4c571eab97d1469c0c65d69e87e45cac06b9879d69369f5d381bf7ee37eadaa7" {
		t.Errorf("hash %s differs from graphjinConfigPatchHash() in Node", got)
	}
	v, _ = yamlToJS([]byte("when: 2026-01-02\nhex: 0x1F\nyes: yes\nanchor: &a {x: 1}\nref: *a\n"))
	if got := jsStringify(v, false); got != `{"when":"2026-01-02","hex":31,"yes":"yes","anchor":{"x":1},"ref":{"x":1}}` {
		t.Errorf("YAML 1.2 core values: %s", got)
	}
}

type fakeGraphjin struct {
	mu      sync.Mutex
	reply   string
	status  int
	headers []http.Header
	bodies  []string
}

func (g *fakeGraphjin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	g.headers = append(g.headers, r.Header.Clone())
	g.bodies = append(g.bodies, string(body))
	if g.status != 0 {
		w.WriteHeader(g.status)
	}
	_, _ = w.Write([]byte(g.reply))
}

func (g *fakeGraphjin) set(reply string, status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reply, g.status = reply, status
}

func TestSweepWatchers(t *testing.T) {
	f, db := memoryFront(t)
	seedPgBoss(t, db, f)
	ctx := context.Background()
	gj := &fakeGraphjin{}
	srv := httptest.NewServer(gj)
	t.Cleanup(srv.Close)
	const source, workflow = "d0000000-0000-4000-8000-000000000001", "d0000000-0000-4000-8000-000000000002"
	seed(t, db, `insert into data_source (id, org_id, name, kind, graphql_url, auth_mode, is_default, enabled)
		values ($1, 'org', 'default', 'graphjin', $2, 'jwt', true, true)`, source, srv.URL)
	seed(t, db, `insert into workflow_definition (id, org_id, name, description) values ($1, 'org', 'Refund spike response', 'x')`, workflow)
	seed(t, db, `insert into watcher (org_id, workflow_id, name, query, value_path, op, threshold, cadence_seconds, debounce_seconds, severity, variables_json)
		values ('org', $1, 'refund-rate', '{ refunds_aggregate { rate } }', 'refunds_aggregate.rate', 'gt', '2', 60, 3600, 'high',
		'{"since":{"kind":"seconds_ago","seconds":60}}')`, workflow)
	var id string
	_ = db.QueryRow(ctx, `select id::text from watcher where name = 'refund-rate'`).Scan(&id)

	now := time.Now().UTC().Truncate(time.Millisecond)
	gj.set(`{"data":{"refunds_aggregate":{"rate":4.2}}}`, 0)
	fired, err := f.API.sweepWatchers(ctx, "org", now)
	if err != nil || len(fired) != 1 || fired[0].ID != id {
		t.Fatalf("first sweep: %+v %v", fired, err)
	}
	h := gj.headers[0]
	parts := strings.Split(strings.TrimPrefix(h.Get("Authorization"), "Bearer "), ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[min(1, len(parts)-1)])
	if h.Get("X-Role") != "service" || !strings.Contains(string(claims), `"sub":"service","role":"service","roles":["service"]`) {
		t.Errorf("headers %v, claims %s", h, claims)
	}
	if want := `{"query":"{ refunds_aggregate { rate } }","variables":{"since":"` + jsDate(now.Add(-time.Minute)) + `"}}`; gj.bodies[0] != want {
		t.Errorf("body %s, want %s", gj.bodies[0], want)
	}
	var data, key string
	var retry, wait int
	if err := db.QueryRow(ctx, `select data::text, singleton_key, retry_limit, retry_delay from `+f.API.BossSchema+`.job where name = 'workflow_run_fire'`).
		Scan(&data, &key, &retry, &wait); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(data), &payload)
	queued, _ := payload["queuedAt"].(float64)
	delete(payload, "queuedAt")
	encoded, _ := json.Marshal(payload)
	if string(encoded) != `{"orgId":"org","triggerKind":"watcher","triggerPayload":{"op":"gt","severity":"high","threshold":2,"value":4.2,"watcherId":"`+id+`","watcherName":"refund-rate"},"workflowId":"`+workflow+`"}` ||
		queued < float64(now.UnixMilli()) || key != "watcher:"+id || retry != 2 || wait != 30 {
		t.Errorf("queued %s key %s retry %d/%d", data, key, retry, wait)
	}
	var last string
	var checked, firedAt time.Time
	_ = db.QueryRow(ctx, `select last_value::text, last_checked_at, last_fired_at from watcher where id = $1`, id).Scan(&last, &checked, &firedAt)
	if last != "4.2" || !checked.Equal(now) || !firedAt.Equal(now) {
		t.Errorf("watcher row: %s %v %v", last, checked, firedAt)
	}

	if fired, _ := f.API.sweepWatchers(ctx, "org", now.Add(30*time.Second)); len(fired) != 0 || len(gj.bodies) != 1 {
		t.Errorf("a watcher inside its cadence ran: %d queries", len(gj.bodies))
	}
	gj.set(`{"data":{"refunds_aggregate":{"rate":5}}}`, 0)
	if fired, _ := f.API.sweepWatchers(ctx, "org", now.Add(2*time.Minute)); len(fired) != 0 || len(gj.bodies) != 2 {
		t.Errorf("inside the quiet period: fired %v, %d queries", fired, len(gj.bodies))
	}
	gj.set(`{"errors":[{"message":"table refunds not found"},"second"]}`, 0)
	if fired, _ := f.API.sweepWatchers(ctx, "org", now.Add(2*time.Hour)); len(fired) != 0 {
		t.Error("a query error fired")
	}
	var lastError *string
	_ = db.QueryRow(ctx, `select last_value::text, last_error from watcher where id = $1`, id).Scan(&last, &lastError)
	if last != "5" || lastError == nil || *lastError != "table refunds not found; second" {
		t.Errorf("after an error: last %s error %v", last, lastError)
	}
	gj.set(`{"data":{"refunds_aggregate":{"rate":5}}}`, 0)
	if fired, _ := f.API.sweepWatchers(ctx, "org", now.Add(3*time.Hour)); len(fired) != 0 {
		t.Error("an unchanged value that already held fired again")
	}
	gj.set(`{"data":{"refunds_aggregate":{"rate":9}}}`, 0)
	if fired, _ := f.API.sweepWatchers(ctx, "org", now.Add(4*time.Hour)); len(fired) != 1 {
		t.Error("a new value outside the quiet period did not fire")
	}
	gj.set(`bad gateway`, 502)
	_, _ = f.API.sweepWatchers(ctx, "org", now.Add(5*time.Hour))
	_ = db.QueryRow(ctx, `select last_error from watcher where id = $1`, id).Scan(&lastError)
	if lastError == nil || *lastError != "graphjin query failed: 502 bad gateway" {
		t.Errorf("HTTP failure: %v", lastError)
	}
	var count int
	_ = db.QueryRow(ctx, `select count(*) from `+f.API.BossSchema+`.job where name = 'workflow_run_fire'`).Scan(&count)
	if count != 1 {
		t.Errorf("%d workflow_run_fire jobs, want 1: the second fire is inside the job's one-hour singleton window", count)
	}
}

func TestSweepWatchersChangedOp(t *testing.T) {
	f, db := memoryFront(t)
	seedPgBoss(t, db, f)
	ctx := context.Background()
	gj := &fakeGraphjin{}
	srv := httptest.NewServer(gj)
	t.Cleanup(srv.Close)
	seed(t, db, `insert into data_source (org_id, name, kind, graphql_url, auth_mode, is_default, enabled) values ('org', 'default', 'graphjin', $1, 'none', true, true)`, srv.URL)
	seed(t, db, `insert into workflow_definition (org_id, name, description) values ('org', 'Stock', 'x')`)
	for op, want := range map[string]string{"changed": "125,120", "gt": "120,125,120"} {
		seed(t, db, `delete from watcher`)
		seed(t, db, `insert into watcher (org_id, workflow_id, name, query, value_path, op, threshold, cadence_seconds, debounce_seconds)
			select 'org', id, 'stock', '{ stock { total } }', 'stock.total', $1, case when $1 = 'gt' then '100'::jsonb end, 60, 0 from workflow_definition`, op)
		var seen []string
		start := time.Now()
		for i, v := range []string{"120", "125", "120", "120"} {
			gj.set(`{"data":{"stock":{"total":`+v+`}}}`, 0)
			fired, err := f.API.sweepWatchers(ctx, "org", start.Add(time.Duration(i)*2*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range fired {
				seen = append(seen, jsStringify(w.Value, false))
			}
		}
		if strings.Join(seen, ",") != want {
			t.Errorf("%s fired %v, want %s", op, seen, want)
		}
		if h := gj.headers[len(gj.headers)-1]; h.Get("Authorization") != "" {
			t.Errorf("a source without jwt got a token")
		}
	}
}

func TestPackWatcherSource(t *testing.T) {
	f, db := memoryFront(t)
	seedPgBoss(t, db, f)
	ctx := context.Background()
	gj := &fakeGraphjin{reply: `{"data":{"n":1}}`}
	srv := httptest.NewServer(gj)
	t.Cleanup(srv.Close)
	const source, other, install = "c0000000-0000-4000-8000-000000000001", "c0000000-0000-4000-8000-000000000002", "c0000000-0000-4000-8000-000000000003"
	seed(t, db, `insert into data_source (id, org_id, name, kind, graphql_url, auth_mode, is_default, enabled) values
		($1, 'org', 'pack', 'graphjin', $3, 'jwt', false, true),
		($2, 'org', 'default', 'graphjin', 'http://default.invalid', 'jwt', true, true)`, source, other, srv.URL+"/api/v1/graphql")
	seed(t, db, `insert into workflow_definition (org_id, name, description) values ('org', 'Stock', 'x')`)
	seed(t, db, `insert into watcher (org_id, workflow_id, name, query, value_path, op, threshold, cadence_seconds)
		select 'org', id, 'low-stock', '{ n }', 'n', 'gt', '5', 60 from workflow_definition`)
	sourceYAML := "name: shop\ntype: mysql\nport: 3306\n"
	srcVal, _ := yamlToJS([]byte(sourceYAML))
	table := `{"name":"orders","table":"sales_order","source":"shop"}`
	config := filepath.Join(t.TempDir(), "agentic.yml")
	write := func(body string) {
		if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("sources:\n  - name: shop\n    type: mysql\n    port: 3306\ntables:\n  - {source: shop, name: orders, table: sales_order}\n")
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", config)
	runtime := `{"_runtime":{"source":{"id":"` + source + `","graphqlUrl":"` + srv.URL + `/api/v1/graphql","authMode":"jwt"},
		"bindings":{"store":"shop"},"bindingHashes":{"store":"` + graphjinConfigPatchHash(srcVal) + `"},"tables":[` + table + `]}}`
	seed(t, db, `insert into pack_install (id, org_id, pack_id, version, status, manifest_hash, config) values ($1, 'org', 'magento', '1', 'installed', 'h', $2::jsonb)`, install, runtime)
	seed(t, db, `insert into pack_artifact (pack_install_id, org_id, artifact_kind, artifact_key, target_ref, desired_hash, last_applied_hash, metadata)
		values ($1, 'org', 'watcher', 'low', 'low', 'h', 'h', '{"locator":{"name":"low-stock"}}')`, install)

	var id string
	_ = db.QueryRow(ctx, `select id::text from watcher`).Scan(&id)
	errorAfter := func(step int) string {
		t.Helper()
		if _, err := f.API.sweepWatchers(ctx, "org", time.Now().Add(time.Duration(step)*2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		var e *string
		_ = db.QueryRow(ctx, `select last_error from watcher where id = $1`, id).Scan(&e)
		return deref(e)
	}
	if e := errorAfter(0); e != "" || len(gj.bodies) != 1 {
		t.Fatalf("pack source: error %q, %d queries to the pack source", e, len(gj.bodies))
	}
	write("sources:\n  - name: shop\n    type: mysql\n    port: 3307\ntables:\n  - {source: shop, name: orders, table: sales_order}\n")
	if e := errorAfter(1); e != "Pack source binding changed; reconfigure and revalidate the pack" {
		t.Errorf("changed binding: %q", e)
	}
	write("sources:\n  - name: shop\n    type: mysql\n    port: 3306\ntables:\n  - {source: shop, name: orders, table: sales_order_grid}\n")
	if e := errorAfter(2); e != "Pack query routing changed; restore the reviewed table mapping" {
		t.Errorf("changed table: %q", e)
	}
	write("sources:\n  - name: shop\n    type: mysql\n    port: 3306\ntables:\n  - {source: shop, name: orders, table: sales_order}\n")
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", "")
	if e := errorAfter(3); e != "Pack source bindings cannot be verified" {
		t.Errorf("no config file: %q", e)
	}
	t.Setenv("OPENNEKO_GRAPHJIN_CONFIG", config)
	seed(t, db, `update data_source set graphql_url = 'http://changed.invalid' where id = $1`, source)
	if e := errorAfter(4); e != "Pack data source changed or is unavailable; reconfigure and revalidate the pack" {
		t.Errorf("changed source: %q", e)
	}
	seed(t, db, `update data_source set graphql_url = $2 where id = $1`, source, srv.URL+"/api/v1/graphql")
	seed(t, db, `update pack_install set config = jsonb_set(config, '{_oauth}', '{"shop":{}}') where id = $1`, install)
	if e := errorAfter(5); e != "Ask an admin to upgrade the OAuth pack to confirm its account scope" {
		t.Errorf("legacy OAuth pack: %q", e)
	}
	seed(t, db, `update pack_install set config = config - '_oauth', status = 'failed' where id = $1`, install)
	if e := errorAfter(6); e != "Pack is not active" {
		t.Errorf("inactive pack: %q", e)
	}
}
