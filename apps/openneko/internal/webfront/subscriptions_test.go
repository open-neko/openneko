package webfront

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func TestSubscriptionList(t *testing.T) {
	f, db := workflowsFront(t)
	seed(t, db, `update subscription set max_chain_depth_override = 3, idempotency_key_template = '{id}',
		created_at = '2026-11-01T00:00:00.5Z', updated_at = '2026-11-02T00:00:00Z' where id = $1`, subHeld)
	member := sessionCookie("member")
	want := `{"subscriptions":[{"id":"` + subHeld + `","workflowId":"` + wfHeld + `","sourceKind":"workflow_output",` +
		`"filter":{"a":{"b":2.5},"z":1},"enabled":true,"debounceMs":250,"maxConcurrentRuns":5,"maxChainDepthOverride":3,` +
		`"idempotencyKeyTemplate":"{id}","createdAt":"2026-11-01T00:00:00.500Z","updatedAt":"2026-11-02T00:00:00.000Z"}]}`
	if w := call(f, "GET", "/api/v1/workflows/"+wfHeld+"/subscriptions", "", member); w.Code != 200 || w.Body.String() != want {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ id, code, want string }{
		{wfOwned, "200", `{"subscriptions":[]}`},
		{"10000000-0000-4000-8000-0000000000ff", "200", `{"subscriptions":[]}`},
		{wfOther, "404", `{"error":"Workflow not found"}`},
		{"nope", "404", `{"error":"Workflow not found"}`},
	} {
		w := call(f, "GET", "/api/v1/workflows/"+c.id+"/subscriptions", "", member)
		if got := http.StatusText(w.Code); w.Body.String() != c.want || (c.code == "200") != (w.Code == 200) {
			t.Errorf("%s: %s %s", c.id, got, w.Body)
		}
	}
}

func TestSubscriptionCreate(t *testing.T) {
	f, db := workflowsFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	post := func(who map[string]string, wf, body string) (int, string) {
		w := call(f, "POST", "/api/v1/workflows/"+wf+"/subscriptions", body, who)
		return w.Code, uuidRe.ReplaceAllString(w.Body.String(), "ID")
	}
	loop := `{"error":"Subscription filter matches 1 of this workflow's recent output(s) — this would create a self-loop. ` +
		`Narrow the filter (scope, mood, kinds) so the workflow's own outputs no longer match.","code":"self_loop","matchingOutputIds":["ID"]}`
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{}`, 400, `{"error":"sourceKind required; one of workflow_output, source_change, external_event"}`},
		{`not json`, 400, `{"error":"sourceKind required; one of workflow_output, source_change, external_event"}`},
		{`{"sourceKind":"cron"}`, 400, `{"error":"sourceKind required; one of workflow_output, source_change, external_event"}`},
		{`null`, 500, `{"error":"internal server error"}`},
		{`{"sourceKind":"workflow_output"}`, 422, loop},
		{`{"sourceKind":"workflow_output","filter":{"kinds":"a finding"}}`, 422, loop},
		{`{"sourceKind":"workflow_output","filter":{"kinds":["finding"],"scope":""}}`, 422, loop},
		{`{"sourceKind":"workflow_output","filter":{"kinds":["alert"]}}`, 200,
			`{"subscription":{"id":"ID","sourceKind":"workflow_output","filter":{"kinds":["alert"]},"enabled":true}}`},
		{`{"sourceKind":"workflow_output","filter":{"mood":["red"]}}`, 200,
			`{"subscription":{"id":"ID","sourceKind":"workflow_output","filter":{"mood":["red"]},"enabled":true}}`},
		{`{"sourceKind":"workflow_output","filter":{"scope":true}}`, 200,
			`{"subscription":{"id":"ID","sourceKind":"workflow_output","filter":{"scope":true},"enabled":true}}`},
		{`{"sourceKind":"external_event","filter":{"z":1,"a":2,"2":0,"1":0},"enabled":false,"debounceMs":30,"maxConcurrentRuns":2,` +
			`"maxChainDepthOverride":4,"idempotencyKeyTemplate":"k"}`, 200,
			`{"subscription":{"id":"ID","sourceKind":"external_event","filter":{"1":0,"2":0,"a":2,"z":1},"enabled":false}}`},
		{`{"sourceKind":"external_event","debounceMs":1.5}`, 500, `{"error":"internal server error"}`},
		{`{"sourceKind":"source_change","filter":{"table":"orders"}}`, 400,
			`{"error":"source_change filter requires { table: string, primary_key: string[] } and optional where/select/version_column (identifiers only)","code":"invalid_filter"}`},
		{`{"sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]}}`, 422,
			`{"error":"no data_source configured for this org — source_change subscriptions need a configured data source","code":"no_data_source"}`},
	} {
		if code, got := post(admin, wfOrg, c.body); code != c.code || got != c.want {
			t.Errorf("%s: %d %s", c.body, code, got)
		}
	}
	if n := queryText(t, db, `select string_agg(source_kind || ':' || filter::text || ':' || enabled || ':' || debounce_ms || ':' || max_concurrent_runs || ':' ||
		coalesce(max_chain_depth_override::text, '-') || ':' || coalesce(idempotency_key_template, '-'), ' | ' order by created_at)
		from subscription where workflow_id = $1`, wfOrg); n != `workflow_output:{"kinds": ["alert"]}:true:0:5:-:- | workflow_output:{"mood": ["red"]}:true:0:5:-:- | `+
		`workflow_output:{"scope": true}:true:0:5:-:- | external_event:{"1": 0, "2": 0, "a": 2, "z": 1}:false:30:2:4:k` {
		t.Errorf("rows: %s", n)
	}

	seed(t, db, `insert into data_source (org_id, kind, graphql_url, is_default) values ('org', 'graphjin', 'http://127.0.0.1:1/api/v1/graphql', true)`)
	seed(t, db, `update workflow_definition set goal = 'Watch ORDERS', steps = '[{"description":"Then Update the row"},{"x":1},null]' where id = $1`, wfOwned)
	mutation := `{"error":"consumer workflow text mentions \"orders\" and the mutation verb \"update\" — it may write back to the watched table and re-trigger itself. ` +
		`Set idempotency_key_template (e.g. include {primary_key}) or pass acknowledgeMutationLoop: true to confirm.","code":"mutation_loop","mutationKeyword":"update"}`
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]}}`, 422, mutation},
		{`{"sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]},"idempotencyKeyTemplate":""}`, 422, mutation},
		{`{"sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]},"acknowledgeMutationLoop":1}`, 200,
			`{"subscription":{"id":"ID","sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]},"enabled":true}}`},
		{`{"sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]},"idempotencyKeyTemplate":"{id}"}`, 200,
			`{"subscription":{"id":"ID","sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]},"enabled":true}}`},
		{`{"sourceKind":"source_change","filter":{"table":"items","primary_key":["id"]}}`, 200,
			`{"subscription":{"id":"ID","sourceKind":"source_change","filter":{"table":"items","primary_key":["id"]},"enabled":true}}`},
	} {
		if code, got := post(member, wfOwned, c.body); code != c.code || got != c.want {
			t.Errorf("%s: %d %s", c.body, code, got)
		}
	}
	for _, c := range []struct {
		who      map[string]string
		wf, want string
	}{
		{member, wfOther, `{"error":"Workflow not found"}`},
		{admin, "nope", `{"error":"Workflow not found"}`},
		{admin, "10000000-0000-4000-8000-0000000000ff", `{"error":"Workflow not found"}`},
	} {
		if code, got := post(c.who, c.wf, `{"sourceKind":"external_event"}`); code != 404 || got != c.want {
			t.Errorf("%s: %d %s", c.wf, code, got)
		}
	}
}

func TestSubscriptionPatchDelete(t *testing.T) {
	f, db := workflowsFront(t)
	seed(t, db, `insert into subscription (id, org_id, workflow_id, source_kind) values ('f1000000-0000-4000-8000-0000000000aa', 'org', $1, 'external_event')`, wfOther)
	hiddenSub := "f1000000-0000-4000-8000-0000000000aa"
	member := sessionCookie("member")
	enabled := func(id string) string {
		return queryText(t, db, `select coalesce((select enabled::text from subscription where id::text = $1), 'gone')`, id)
	}
	for _, c := range []struct{ id, body, want, state string }{
		{subHeld, `{"enabled":false}`, `{"ok":true}`, "false"},
		{subHeld, `{"enabled":"yes"}`, `{"ok":true}`, "false"},
		{subHeld, `not json`, `{"ok":true}`, "false"},
		{subHeld, `null`, `{"error":"internal server error"}`, "false"},
		{subHeld, `{"enabled":true}`, `{"ok":true}`, "true"},
		{hiddenSub, `{"enabled":false}`, `{"error":"not found"}`, "true"},
		{"nope", `{"enabled":false}`, `{"error":"not found"}`, "gone"},
		{"10000000-0000-4000-8000-0000000000ff", `null`, `{"error":"not found"}`, "gone"},
	} {
		w := call(f, "PATCH", "/api/v1/subscriptions/"+c.id, c.body, member)
		if w.Body.String() != c.want || enabled(c.id) != c.state {
			t.Errorf("%s %s: %d %s, %s", c.id, c.body, w.Code, w.Body, enabled(c.id))
		}
	}
	if w := call(f, "DELETE", "/api/v1/subscriptions/"+hiddenSub, "", member); w.Code != 404 || enabled(hiddenSub) != "true" {
		t.Errorf("hidden delete: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/subscriptions/"+subHeld, "", member); w.Code != 200 || w.Body.String() != `{"ok":true}` || enabled(subHeld) != "gone" {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/subscriptions/"+subHeld, "", member); w.Code != 404 || w.Body.String() != `{"error":"not found"}` {
		t.Errorf("second delete: %d %s", w.Code, w.Body)
	}
	if w := call(f, "DELETE", "/api/v1/subscriptions/"+hiddenSub, "", sessionCookie("admin")); w.Code != 200 {
		t.Errorf("admin delete: %d %s", w.Code, w.Body)
	}
}

func TestSubscriptionDryRun(t *testing.T) {
	f, db := workflowsFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	var got []string
	reply := `{"data":{"orders":[{"z":1,"id":7,"n":1.50}]}}`
	status := 200
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = append(got, r.Header.Get("Content-Type")+" "+string(raw))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer gj.Close()
	dry := func(who map[string]string, wf, body string) (int, string) {
		w := call(f, "POST", "/api/v1/workflows/"+wf+"/subscriptions/dry-run", body, who)
		return w.Code, w.Body.String()
	}
	for _, c := range []struct {
		who      map[string]string
		wf, body string
		code     int
		want     string
	}{
		{admin, "10000000-0000-4000-8000-0000000000ff", `{}`, 404, `{"error":"Workflow not found"}`},
		{admin, "nope", `{}`, 404, `{"error":"Workflow not found"}`},
		{member, wfOther, `{}`, 404, `{"error":"Workflow not found"}`},
		{member, wfHeld, `null`, 500, `{"error":"internal server error"}`},
		{member, wfHeld, `not json`, 400, `{"error":"dry-run currently supports sourceKind=\"source_change\" only","code":"unsupported_source_kind"}`},
		{member, wfHeld, `{"sourceKind":"source_change","filter":{"table":"1x","primary_key":["id"]}}`, 400,
			`{"error":"filter requires { table: string, primary_key: string[] } and optional where/select/version_column (identifiers only)","code":"invalid_filter"}`},
		{member, wfHeld, `{"sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"],"where":[1]}}`, 400,
			`{"error":"filter requires { table: string, primary_key: string[] } and optional where/select/version_column (identifiers only)","code":"invalid_filter"}`},
		{member, wfHeld, `{"sourceKind":"source_change","filter":{"table":"orders","primary_key":["id"]}}`, 422,
			`{"error":"no data_source configured for this org — source_change subscriptions need a configured data source","code":"no_data_source"}`},
	} {
		if code, body := dry(c.who, c.wf, c.body); code != c.code || body != c.want {
			t.Errorf("%s %s: %d %s", c.wf, c.body, code, body)
		}
	}
	if len(got) != 0 {
		t.Fatalf("GraphJin called: %v", got)
	}

	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, is_default, created_at) values
		('org', 'graphjin', 'old', 'http://127.0.0.1:1/old', false, '2026-01-01'), ('org', 'graphjin', 'new', $1, true, '2026-02-01')`, gj.URL)
	body := `{"sourceKind":"source_change","limit":7.9,"filter":{"table":"orders","primary_key":["id","id"],"select":["z","id"],"version_column":"updated_at",` +
		`"where":{"status":{"eq":"late  <b>"},"2":{"gt":1e21},"1":[true,null,0.5],"and":{}}}}`
	query := "query SourceChangeDryRun {\n  orders(where: { 1: [true, null, 0.5], 2: { gt: 1e+21 }, status: { eq: \"late  <b>\" }, and: {  } }, " +
		"order_by: { updated_at: desc }, limit: 7) {\n    id\n    z\n    updated_at\n  }\n}"
	code, out := dry(member, wfHeld, body)
	var parsed struct {
		CompiledQuery string `json:"compiledQuery"`
	}
	_ = json.Unmarshal([]byte(out), &parsed)
	rest := strings.Replace(out, parsed.CompiledQuery, "", 1)
	if code != 200 || parsed.CompiledQuery != query || !strings.HasSuffix(out, `,"variables":{},"rowCount":1,"rows":[{"z":1,"id":7,"n":1.5}]}`) {
		t.Errorf("dry run: %d\n%s\n%s", code, out, rest)
	}
	var sent struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "application/json ") || json.Unmarshal([]byte(strings.TrimPrefix(got[0], "application/json ")), &sent) != nil ||
		sent.Query != query || sent.Variables == nil {
		t.Errorf("GraphJin got %v", got)
	}

	simple := `{"sourceKind":"source_change","limit":51,"filter":{"table":"orders","primary_key":["id"],"where":{}}}`
	simpleQuery := `"query SourceChangeDryRun {\n  orders(order_by: { id: desc }, limit: 5) {\n    id\n  }\n}"`
	for _, c := range []struct {
		status int
		reply  string
		code   int
		want   string
	}{
		{200, `{"data":{"other":[1]}}`, 200, `{"compiledQuery":` + simpleQuery + `,"variables":{},"rowCount":0,"rows":[]}`},
		{200, `{"data":null,"errors":["bad",{"message":"no table","path":["orders",0]},{"code":7,"b":{"2":1,"1":2}},5,null,{"message":3,"path":"x"}]}`, 422,
			`{"error":"graphjin returned errors","code":"graphjin_errors","compiledQuery":` + simpleQuery + `,"graphjinErrors":[{"message":"bad"},` +
				`{"message":"no table","path":["orders",0]},{"message":"{\"code\":7,\"b\":{\"1\":2,\"2\":1}}"},{"message":"5"},{"message":"null"},` +
				`{"message":"{\"message\":3,\"path\":\"x\"}"}]}`},
		{200, `{"data":{"orders":[]},"errors":[]}`, 200, `{"compiledQuery":` + simpleQuery + `,"variables":{},"rowCount":0,"rows":[]}`},
		{503, strings.Repeat("é", 600), 502, `{"error":"graphjin query failed: 503 ` + strings.Repeat("é", 500) + `","code":"graphjin_error","compiledQuery":` + simpleQuery + `}`},
	} {
		status, reply = c.status, c.reply
		if code, out := dry(member, wfHeld, simple); code != c.code || out != c.want {
			t.Errorf("reply %d %s: %d %s", c.status, c.reply, code, out)
		}
	}
	gj.Close()
	if code, out := dry(member, wfHeld, simple); code != 502 || out != `{"error":"fetch failed","code":"graphjin_error","compiledQuery":`+simpleQuery+`}` {
		t.Errorf("closed: %d %s", code, out)
	}
}

func TestUTF16Prefix(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{{"abc", 2, "ab"}, {"a😀b", 3, "a😀"}, {"a😀b", 2, "a"}, {"ab", 5, "ab"}} {
		if got := utf16Prefix(c.in, c.n); got != c.want {
			t.Errorf("%q %d: %q", c.in, c.n, got)
		}
	}
}

func TestOutputMatches(t *testing.T) {
	scope, topic, mood := "apac_churn", "partner_renewal", "watch"
	for _, c := range []struct {
		filter string
		noMood bool
		want   bool
	}{
		{`{}`, false, true},
		{`{"scope":"apac_churn"}`, false, true},
		{`{"scope":"other"}`, false, false},
		{`{"topic":"partner_renewal"}`, false, true},
		{`{"topic":"other"}`, false, false},
		{`{"mood":"watch"}`, false, true},
		{`{"mood":["watch","act"]}`, false, true},
		{`{"mood":["good"]}`, false, false},
		{`{"mood":["watch"]}`, true, false},
		{`{"kinds":["finding","observation"]}`, false, true},
		{`{"kinds":["recommendation"]}`, false, false},
		{`{"kinds":[]}`, false, true},
		{`{"kinds":"findings"}`, false, true},
		{`{"kinds":"find"}`, false, false},
		{`{"kinds":{"length":1}}`, false, true},
		{`{"scope":1}`, false, false},
		{`{"scope":0,"topic":"","mood":null}`, false, true},
		{`[]`, false, true},
	} {
		var filter any
		if err := json.Unmarshal([]byte(c.filter), &filter); err != nil {
			t.Fatal(err)
		}
		m := &mood
		if c.noMood {
			m = nil
		}
		if got := outputMatches(filter, "finding", &scope, &topic, m); got != c.want {
			t.Errorf("%s: %v", c.filter, got)
		}
	}
}
