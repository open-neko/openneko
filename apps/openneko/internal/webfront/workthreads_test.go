package webfront

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	thA       = "e0000000-0000-4000-8000-000000000001"
	thB       = "e0000000-0000-4000-8000-000000000002"
	thTheirs  = "e0000000-0000-4000-8000-000000000003"
	thRoutine = "e0000000-0000-4000-8000-000000000004"
	thChannel = "e0000000-0000-4000-8000-000000000005"
	ru1       = "e1000000-0000-4000-8000-000000000001"
	ru2       = "e1000000-0000-4000-8000-000000000002"
	ru3       = "e1000000-0000-4000-8000-000000000003"
	ruTheirs  = "e1000000-0000-4000-8000-000000000004"
	msgLoose  = "e2000000-0000-4000-8000-000000000001"
	msgU1     = "e2000000-0000-4000-8000-000000000002"
	msgA1     = "e2000000-0000-4000-8000-000000000003"
	msgU2     = "e2000000-0000-4000-8000-000000000004"
	msgU3     = "e2000000-0000-4000-8000-000000000005"
	arChat    = "e3000000-0000-4000-8000-000000000001"
	arChat2   = "e3000000-0000-4000-8000-000000000002"
	arTheirs  = "e3000000-0000-4000-8000-000000000003"
	arDraft   = "e3000000-0000-4000-8000-000000000004"
	arAdmin   = "e3000000-0000-4000-8000-000000000005"
	metricOne = "e4000000-0000-4000-8000-000000000001"
)

func threadsFront(t *testing.T) (*Front, *pgxpool.Pool) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	t.Setenv("HOME", t.TempDir())
	saved := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = saved })
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seedRiver(t, db, f)
	seed(t, db, `insert into work_thread (id, org_id, title, channel, created_by_user_id, backend_state, created_at, updated_at, last_message_at) values
		($1, 'org', '', 'web', 'member', '{"session":"s1","n":1}', '2026-11-05T08:00:00Z', '2026-11-05T08:00:00Z', '2026-11-05T09:00:00Z'),
		($2, 'org', 'Stock check', 'web', 'member', '{}', '2026-11-05T07:00:00Z', '2026-11-05T07:00:00Z', '2026-11-05T10:00:00Z'),
		($3, 'org', 'Theirs', 'web', 'other', '{}', '2026-11-05T07:00:00Z', '2026-11-05T07:00:00Z', '2026-11-05T11:00:00Z'),
		($4, 'org', 'Routine', 'web', 'member', '{}', '2026-11-05T07:00:00Z', '2026-11-05T07:00:00Z', '2026-11-05T12:00:00Z'),
		($5, 'org', 'Telegram', 'telegram', 'member', '{}', '2026-11-05T07:00:00Z', '2026-11-05T07:00:00Z', '2026-11-05T13:00:00Z')`,
		thA, thB, thTheirs, thRoutine, thChannel)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ('e1000000-0000-4000-8000-000000000009', 'org', $1, 'test')`, thRoutine)
	seed(t, db, `insert into workflow_run (org_id, workflow_id, thread_id, work_run_id, trigger_kind) values ('org', $1, $2, 'e1000000-0000-4000-8000-000000000009', 'manual')`, wfOrg, thRoutine)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, status, actor_role, analysis_minutes_saved, created_at, finished_at) values
		($1, 'org', $4, 'test', 'completed', 'member', 12, '2026-11-05T08:01:00Z', '2026-11-05T08:02:00.5Z'),
		($2, 'org', $4, 'test', 'completed', 'bogus', null, '2026-11-05T08:03:00Z', null),
		($3, 'org', $4, 'test', 'running', null, null, '2026-11-05T08:05:00Z', null),
		($5, 'org', $6, 'test', 'completed', null, null, '2026-11-05T08:05:00Z', null)`, ru1, ru2, ru3, thA, ruTheirs, thTheirs)
	seed(t, db, `update workflow_run set work_run_id = $1 where id = $2`, ru2, runHeld)
	seed(t, db, `insert into work_message (id, org_id, thread_id, run_id, role, content, created_at) values
		($1, 'org', $6, null, 'user', 'seed context', '2026-11-05T08:00:30Z'),
		($2, 'org', $6, $7, 'user', 'first question', '2026-11-05T08:00:59Z'),
		($3, 'org', $6, $7, 'assistant', 'first answer', '2026-11-05T08:01:30Z'),
		($4, 'org', $6, $8, 'user', 'second question', '2026-11-05T08:03:00Z'),
		($5, 'org', $6, $9, 'user', 'third question', '2026-11-05T08:05:00Z')`, msgLoose, msgU1, msgA1, msgU2, msgU3, thA, ru1, ru2, ru3)
	big := strings.Repeat("é", 32_005) + "😀"
	bigEvent, _ := json.Marshal(map[string]any{"type": "tool_end", "id": "t1", "result": big, "ok": true})
	objEvent, _ := json.Marshal(map[string]any{"type": "tool_end", "result": map[string]any{"rows": strings.Repeat("x", 32_000)}})
	for _, e := range []struct{ run, payload string }{
		{ru2, `{"type":"message","role":"assistant","content":"two"}`},
		{ru1, `{"type":"text","n":1.50,"big":1e21}`},
		{ru1, string(bigEvent)},
		{ru1, string(objEvent)},
		{ru3, `{"type":"tool_end","result":"small"}`},
	} {
		seed(t, db, `insert into work_run_event (org_id, thread_id, run_id, kind, payload) values ('org', $1, $2, 'x', $3)`, thA, e.run, e.payload)
	}
	return f, db
}

func TestWorkThreadList(t *testing.T) {
	f, _ := threadsFront(t)
	w := call(f, "GET", "/api/v1/work/threads", "", sessionCookie("member"))
	want := `{"threads":[{"id":"` + thB + `","title":"Stock check","createdAt":"2026-11-05T07:00:00.000Z","updatedAt":"2026-11-05T07:00:00.000Z","lastMessageAt":"2026-11-05T10:00:00.000Z"},` +
		`{"id":"` + thA + `","title":"Untitled thread","createdAt":"2026-11-05T08:00:00.000Z","updatedAt":"2026-11-05T08:00:00.000Z","lastMessageAt":"2026-11-05T09:00:00.000Z"}]}`
	if w.Code != 200 || w.Body.String() != want {
		t.Errorf("list: %d\n%s\nwant\n%s", w.Code, w.Body, want)
	}
	if w := call(f, "GET", "/api/work/threads", "", sessionCookie("admin")); w.Body.String() != `{"threads":[]}` {
		t.Errorf("admin sees no one else's threads: %s", w.Body)
	}
}

func TestWorkThreadCreate(t *testing.T) {
	f, db := threadsFront(t)
	ctx := context.Background()
	seed(t, db, `insert into metric (id, org_id, role, slug, source, title, chart_hint, cadence, active) values ($1, 'org', 'CEO', 'rev', 'bootstrap', 'Revenue YoY', 'line', 'daily', true)`, metricOne)
	seed(t, db, `insert into metric_snapshot (metric_id, status, payload, captured_at) values
		($1, 'bad', '{"mood":"warn"}', '2026-11-01T00:00:00Z'),
		($1, 'good', '{"insightText":"Up 28.1%.","detailText":"Q3 strong.","headlineMetric":"$50.37M","chartData":[{"d":"Jan","v":4.50}]}', '2026-11-02T00:00:00Z')`, metricOne)
	seed(t, db, `update action_request set summary = 'Add a note', approved_by_user_id = 'admin' where id = $1`, arOrgRun)
	seed(t, db, `update workflow_output set body = e'Two  orders\nlate', mood = 'warn' where id = $1`, outputID)
	member := sessionCookie("member")
	messages := func(id string) []string {
		rows, err := db.Query(ctx, `select content from work_message where thread_id = $1 order by created_at, content`, id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	create := func(body string) (string, string) {
		w := call(f, "POST", "/api/v1/work/threads", body, member)
		var v struct{ Thread struct{ ID, Title string } }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("create %s: %d %s", body, w.Code, w.Body)
		}
		return v.Thread.ID, v.Thread.Title
	}

	id, title := create(`not json`)
	if title != "Untitled thread" || len(messages(id)) != 0 {
		t.Errorf("empty: %q %v", title, messages(id))
	}
	if id, title = create(`{"title":"  Hello  ","seedMetricId":"` + unknownUUID + `"}`); title != "Hello" || len(messages(id)) != 0 {
		t.Errorf("titled: %q %v", title, messages(id))
	}

	id, title = create(`{"seedMetricId":"` + metricOne + `"}`)
	card := briefingCardSentinel + `{"id":"seed-` + metricOne + `","metricId":"` + metricOne + `","source":"bootstrap","state":"ok","mood":"good",` +
		`"text":"Revenue YoY","metric":"$50.37M","label":"","detail":"Up 28.1%. Q3 strong.","chart":"line","chartData":[{"d":"Jan","v":4.5}]}`
	if got := messages(id); title != "Revenue YoY" || len(got) != 1 || got[0] != card {
		t.Errorf("card: %q\n%v\nwant\n%s", title, got, card)
	}

	id, title = create(`{"seedWorkflowRunId":"` + runOrg + `"}`)
	runText := "I want to follow up on a workflow run.\n\nWorkflow: Org flow\nRun: queued · manual · 1 Nov, 8:00 am\n\nOutputs produced:\n" +
		"- Late order [warn]\n  Two orders late\n\nActions proposed:\n- note [risk low] (executed) — \n- note [risk low] (executed) — \n" +
		"- note [risk low] (pending_approval) — Add a note\n\n(I'll ask my question below.)"
	if got := messages(id); title != "Follow-up · Org flow" || len(got) != 1 || got[0] != runText {
		t.Errorf("run seed: %q\n%q\nwant\n%q", title, got, runText)
	}

	id, title = create(`{"seedActionRequestId":"` + arFired + `"}`)
	actionText := "I want to follow up on an action receipt.\n\nAction: note\nStatus: executed · internal · risk low\nProposed: 3 Nov, 6:00 am\n" +
		"From workflow: Org flow\nApproved by: rule \"Auto small\"\n\nExecutions:\n- executor=host status=succeeded\n\nPayload:\n```json\n" +
		"{\n  \"a\": [\n    2\n  ],\n  \"b\": 1\n}\n```\n\n(I'll ask my question below.)"
	if got := messages(id); title != "Follow-up · note" || len(got) != 1 || got[0] != actionText {
		t.Errorf("action seed: %q\n%q\nwant\n%q", title, got, actionText)
	}

	if id, title = create(`{"seedActionRequestId":"` + arSource + `"}`); title != "Untitled thread" || len(messages(id)) != 0 {
		t.Errorf("admin-only action for a member: %q %v", title, messages(id))
	}

	id, title = create(`{"seedWorkflowRunId":"` + runOrg + `","seedActionRequestId":"` + arFired + `","seedMetricId":"` + metricOne + `"}`)
	if got := messages(id); title != "Follow-up · Org flow" || len(got) != 3 {
		t.Errorf("all seeds: %q %d", title, len(got))
	}
	var owner string
	if err := db.QueryRow(ctx, `select created_by_user_id from work_thread where id = $1`, id).Scan(&owner); err != nil || owner != "member" {
		t.Errorf("owner %q %v", owner, err)
	}
}

func TestWorkThreadBundle(t *testing.T) {
	f, _ := threadsFront(t)
	member := sessionCookie("member")
	get := func(q string, who map[string]string) (int, map[string]json.RawMessage) {
		w := call(f, "GET", "/api/v1/work/threads/"+thA+q, "", who)
		var v map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v
	}
	code, all := get("", member)
	if code != 200 {
		t.Fatalf("bundle: %d", code)
	}
	if want := `{"id":"` + thA + `","title":"Untitled thread","backendState":{"n":1,"session":"s1"},"createdAt":"2026-11-05T08:00:00.000Z",` +
		`"updatedAt":"2026-11-05T08:00:00.000Z","lastMessageAt":"2026-11-05T09:00:00.000Z"}`; string(all["thread"]) != want {
		t.Errorf("thread: %s", all["thread"])
	}
	if want := `[{"id":"` + ru1 + `","backend":"test","status":"completed","error":null,"createdAt":"2026-11-05T08:01:00.000Z","finishedAt":"2026-11-05T08:02:00.500Z",` +
		`"analysisMinutesSaved":12,"analysisMinutesBasis":null,"actorRole":"member","routineName":null},` +
		`{"id":"` + ru2 + `","backend":"test","status":"completed","error":null,"createdAt":"2026-11-05T08:03:00.000Z","finishedAt":null,` +
		`"analysisMinutesSaved":null,"analysisMinutesBasis":null,"actorRole":null,"routineName":"Held flow"},` +
		`{"id":"` + ru3 + `","backend":"test","status":"running","error":null,"createdAt":"2026-11-05T08:05:00.000Z","finishedAt":null,` +
		`"analysisMinutesSaved":null,"analysisMinutesBasis":null,"actorRole":null,"routineName":null}]`; string(all["runs"]) != want {
		t.Errorf("runs: %s", all["runs"])
	}
	if !strings.HasPrefix(string(all["messages"]), `[{"id":"`+msgLoose+`","runId":null,"role":"user","content":"seed context","createdAt":"2026-11-05T08:00:30.000Z"},`) {
		t.Errorf("messages: %s", all["messages"])
	}
	var events map[string][]json.RawMessage
	_ = json.Unmarshal(all["eventsByRun"], &events)
	if !strings.HasPrefix(string(all["eventsByRun"]), `{"`+ru2+`":[`) || len(events[ru1]) != 3 || len(events[ru3]) != 1 {
		t.Errorf("events: %.200s", all["eventsByRun"])
	}
	if string(events[ru1][0]) != `{"n":1.5,"big":1e+21,"type":"text"}` {
		t.Errorf("event numbers: %s", events[ru1][0])
	}
	wantBig := `{"id":"t1","ok":true,"type":"tool_end","result":"` + strings.Repeat("é", 32_000) + `\n… 7 more characters not shown"}`
	if string(events[ru1][1]) != wantBig {
		t.Errorf("capped string: %.80s … %s", events[ru1][1], events[ru1][1][len(events[ru1][1])-60:])
	}
	var obj struct{ Result string }
	if json.Unmarshal(events[ru1][2], &obj) != nil || !strings.HasSuffix(obj.Result, "\n… 11 more characters not shown") || !strings.HasPrefix(obj.Result, `{"rows":"xxx`) {
		t.Errorf("capped object: %.60s", events[ru1][2])
	}
	if _, ok := all["hasOlder"]; ok {
		t.Error("hasOlder without a page")
	}

	runIDs := func(v map[string]json.RawMessage) string {
		var runs []struct{ ID string }
		_ = json.Unmarshal(v["runs"], &runs)
		var ids []string
		for _, r := range runs {
			ids = append(ids, r.ID[len(r.ID)-1:])
		}
		return strings.Join(ids, ",")
	}
	contents := func(v map[string]json.RawMessage) string {
		var ms []struct{ Content string }
		_ = json.Unmarshal(v["messages"], &ms)
		var out []string
		for _, m := range ms {
			out = append(out, m.Content)
		}
		return strings.Join(out, "|")
	}
	for _, c := range []struct{ q, runs, older, messages, eventRuns string }{
		{"?limit=2", "2,3", "true", "second question|third question", "2,3"},
		{"?limit=2&before=" + ru2, "1", "false", "seed context|first question|first answer", "1"},
		{"?limit=1&before=" + ru2, "1", "false", "seed context|first question|first answer", "1"},
		{"?limit=1&before=" + ru3, "2", "true", "second question", "2"},
		{"?limit=0x2", "2,3", "true", "second question|third question", "2,3"},
		{"?limit=500", "1,2,3", "false", "seed context|first question|first answer|second question|third question", "1,2,3"},
		{"?limit=1&before=" + ru1, "", "false", "seed context", ""},
	} {
		code, v := get(c.q, member)
		var ev map[string]json.RawMessage
		_ = json.Unmarshal(v["eventsByRun"], &ev)
		var evRuns []string
		for _, r := range []string{ru1, ru2, ru3} {
			if _, ok := ev[r]; ok {
				evRuns = append(evRuns, r[len(r)-1:])
			}
		}
		if code != 200 || runIDs(v) != c.runs || string(v["hasOlder"]) != c.older || contents(v) != c.messages || strings.Join(evRuns, ",") != c.eventRuns {
			t.Errorf("%s: %d runs %s older %s messages %s events %v", c.q, code, runIDs(v), v["hasOlder"], contents(v), evRuns)
		}
	}
	for _, q := range []string{"?limit=1.5", "?limit=-1", "?limit=abc", "?limit=Infinity"} {
		if _, v := get(q, member); runIDs(v) != "1,2,3" || v["hasOlder"] != nil {
			t.Errorf("%s pages: %s", q, runIDs(v))
		}
	}
	for _, p := range []string{thTheirs, unknownUUID, "not-a-uuid"} {
		if w := call(f, "GET", "/api/v1/work/threads/"+p, "", member); w.Code != 404 || w.Body.String() != `{"error":"Thread not found"}` {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	if code, _ := get("", sessionCookie("admin")); code != 404 {
		t.Errorf("admin reads a member's thread: %d", code)
	}
}

func TestWorkThreadDeleteAndTruncate(t *testing.T) {
	f, db := threadsFront(t)
	ctx := context.Background()
	member := sessionCookie("member")
	count := func(q string) int {
		var n int
		if err := db.QueryRow(ctx, q, thA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	truncate := func(body string, who map[string]string) string {
		w := call(f, "POST", "/api/v1/work/threads/"+thA+"/truncate", body, who)
		return w.Body.String()
	}
	for body, want := range map[string]string{
		`{}`:                                  `{"error":"messageId is required"}`,
		`{"messageId":"  "}`:                  `{"error":"messageId is required"}`,
		`{"messageId":"x"}`:                   `{"error":"Message not found"}`,
		`{"messageId":"` + msgLoose + `"}`:    `{"error":"Message not found"}`,
		`{"messageId":"` + msgA1 + `"}`:       `{"error":"Only user messages can be truncated from"}`,
		`{"messageId":"` + unknownUUID + `"}`: `{"error":"Message not found"}`,
	} {
		if got := truncate(body, member); got != want {
			t.Errorf("truncate %s: %s", body, got)
		}
	}
	if got := truncate(`{"messageId":"`+msgU2+`"}`, sessionCookie("other")); got != `{"error":"Thread not found"}` {
		t.Errorf("other user: %s", got)
	}
	seed(t, db, `insert into work_message (org_id, thread_id, run_id, role, content) values ('org', $1, $2, 'user', 'elsewhere')`, thTheirs, ruTheirs)
	var elsewhere string
	_ = db.QueryRow(ctx, `select id::text from work_message where content = 'elsewhere'`).Scan(&elsewhere)
	if got := truncate(`{"messageId":"`+elsewhere+`"}`, member); got != `{"error":"Run not found"}` {
		t.Errorf("message of another thread: %s", got)
	}
	if got := truncate(`{"messageId":"`+msgU2+`"}`, member); got != `{"ok":true}` {
		t.Fatalf("truncate: %s", got)
	}
	if runs, msgs, events, state := count(`select count(*) from work_run where thread_id = $1`), count(`select count(*) from work_message where thread_id = $1`),
		count(`select count(*) from work_run_event where thread_id = $1`), count(`select count(*) from work_thread where id = $1 and backend_state = '{}'`); runs != 1 || msgs != 3 || events != 3 || state != 1 {
		t.Errorf("after truncate: runs %d messages %d events %d state %d", runs, msgs, events, state)
	}

	if w := call(f, "DELETE", "/api/v1/work/threads/"+thA, "", sessionCookie("admin")); w.Code != 404 {
		t.Errorf("admin delete: %d", w.Code)
	}
	if w := call(f, "DELETE", "/api/v1/work/threads/"+thA, "", member); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if n := count(`select count(*) from work_thread where id = $1`); n != 0 {
		t.Error("thread remains")
	}
	if w := call(f, "DELETE", "/api/v1/work/threads/"+thA, "", member); w.Code != 404 || w.Body.String() != `{"error":"Thread not found"}` {
		t.Errorf("delete twice: %d %s", w.Code, w.Body)
	}
}

func TestWorkRunEvents(t *testing.T) {
	f, _ := threadsFront(t)
	w := call(f, "GET", "/api/v1/work/runs/"+ru1+"/events", "", sessionCookie("member"))
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"runId":"`+ru1+`","events":[{"n":1.5,"big":1e+21,"type":"text"},{"id":"t1","ok":true,"type":"tool_end","result":"ééé`) ||
		strings.Contains(w.Body.String(), "more characters not shown") {
		t.Errorf("events: %d %.200s", w.Code, w.Body)
	}
	for _, c := range []struct {
		run string
		who map[string]string
	}{{ru1, sessionCookie("admin")}, {ruTheirs, sessionCookie("member")}, {"nope", sessionCookie("member")}} {
		if w := call(f, "GET", "/api/v1/work/runs/"+c.run+"/events", "", c.who); w.Code != 404 || w.Body.String() != `{"error":"Run not found"}` {
			t.Errorf("%s: %d %s", c.run, w.Code, w.Body)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = saved
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestWorkRunTiming(t *testing.T) {
	f, _ := threadsFront(t)
	t.Setenv("OPENNEKO_VERSION", "9.9.9")
	member := sessionCookie("member")
	path := "/api/v1/work/threads/" + thA + "/runs/" + ru1 + "/timing"
	var codes []int
	logged := captureStdout(t, func() {
		for _, c := range []struct {
			path, body string
			who        map[string]string
		}{
			{path, `{"acknowledgementMs":10,"firstOutputMs":23.5,"documentVisible":1,"acknowledgementMs":11}`, member},
			{path, `{"prompt":"private"}`, member},
			{path, `{"firstOutputMs":-1}`, member},
			{path, `{"firstOutputMs":"5"}`, member},
			{path, `{"firstOutputMs":3600001}`, member},
			{path, `{"documentVisible":2}`, member},
			{path, `{"firstOutputMs":1e400}`, member},
			{path, `{}`, member},
			{path, `[]`, member},
			{path, `null`, member},
			{path, `{"firstOutputMs":2} x`, member},
			{path, `{"firstOutputMs":2}`, sessionCookie("other")},
			{"/api/v1/work/threads/" + thB + "/runs/" + ru1 + "/timing", `{"firstOutputMs":2}`, member},
			{"/api/v1/work/threads/" + thA + "/runs/nope/timing", `{"firstOutputMs":2}`, member},
		} {
			codes = append(codes, call(f, "POST", c.path, c.body, c.who).Code)
		}
	})
	want := []int{204, 400, 400, 400, 400, 400, 400, 400, 400, 400, 400, 404, 404, 404}
	for i := range want {
		if codes[i] != want[i] {
			t.Errorf("case %d: %d, want %d", i, codes[i], want[i])
		}
	}
	lines := strings.Split(strings.TrimSpace(logged), "\n")
	if len(lines) != 1 {
		t.Fatalf("logged %d lines: %s", len(lines), logged)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &v); err != nil {
		t.Fatal(err)
	}
	if v["type"] != "startup_timing" || v["runId"] != ru1 || v["threadId"] != thA || v["phase"] != "browser.startup" || v["origin"] != "client_reported" ||
		v["acknowledgementMs"] != 11.0 || v["firstOutputMs"] != 23.5 || v["documentVisible"] != 1.0 || v["version"] != "9.9.9" || v["event"] != true {
		t.Errorf("line: %s", lines[0])
	}
	if !strings.Contains(lines[0], `"origin":"client_reported","acknowledgementMs":11,"firstOutputMs":23.5,"documentVisible":1}`) {
		t.Errorf("order: %s", lines[0])
	}
}

func TestApproveRunAction(t *testing.T) {
	f, db := threadsFront(t)
	ctx := context.Background()
	for _, a := range []struct{ id, run, kind, status string }{
		{arChat, ru1, "note", "pending_approval"},
		{arChat2, ru1, "note", "pending_approval"},
		{arTheirs, ruTheirs, "note", "pending_approval"},
		{arDraft, ru1, "note", "draft"},
		{arAdmin, ru1, "source_config_admin", "pending_approval"},
	} {
		seed(t, db, `insert into action_request (id, org_id, work_run_id, scope, kind, status, payload) values ($1, 'org', $2, 'internal', $3, $4, '{}')`,
			a.id, a.run, a.kind, a.status)
	}
	member := sessionCookie("member")
	post := func(thread, run, body string, who map[string]string) (int, string) {
		w := call(f, "POST", "/api/v1/work/threads/"+thread+"/runs/"+run+"/approve-action", body, who)
		return w.Code, w.Body.String()
	}
	for _, c := range []struct {
		thread, run, body string
		who               map[string]string
		code              int
		want              string
	}{
		{thA, ru1, `{}`, member, 400, `{"error":"actionRequestId is required"}`},
		{thA, ru1, `{"actionRequestId":"` + arChat + `","decision":"maybe"}`, member, 400, `{"error":"decision must be 'approve' or 'reject'"}`},
		{thA, ru1, `{"actionRequestId":"` + arChat + `","decision":"approve"}`, sessionCookie("other"), 404, `{"error":"Thread not found"}`},
		{thA, ru1, `{"actionRequestId":"x","decision":"approve"}`, member, 404, `{"error":"action_request not found"}`},
		{thA, ru1, `{"actionRequestId":"` + arAdmin + `","decision":"approve"}`, member, 404, `{"error":"action_request not found"}`},
		{thA, ru2, `{"actionRequestId":"` + arChat + `","decision":"approve"}`, member, 403, `{"error":"action_request does not belong to this run"}`},
		{thA, ruTheirs, `{"actionRequestId":"` + arTheirs + `","decision":"approve"}`, member, 404, `{"error":"action_request not found"}`},
		{thA, ru1, `{"actionRequestId":"` + arDraft + `","decision":"approve"}`, member, 409, `{"error":"action_request status is \"draft\"; only pending_approval can be approved or rejected"}`},
		{thA, ru1, `{"actionRequestId":"` + arChat + `","decision":"approve"}`, member, 200, `{"ok":true,"decision":"approved","action_request_id":"` + arChat + `","status":"queued_for_execution"}`},
		{thA, ru1, `{"actionRequestId":"` + arChat + `","decision":"approve"}`, member, 200, `{"ok":true,"decision":"already_approved","action_request_id":"` + arChat + `","status":"approved"}`},
		{thA, ru1, `{"actionRequestId":"` + arChat + `","decision":"reject"}`, member, 409, `{"error":"action_request status is \"approved\"; only pending_approval can be approved or rejected"}`},
		{thA, ru1, `{"actionRequestId":"` + arChat2 + `","decision":"reject","rejectionReason":"Not now"}`, member, 200, `{"ok":true,"decision":"rejected","action_request_id":"` + arChat2 + `"}`},
		{thA, ru1, `{"actionRequestId":"` + arChat2 + `","decision":"reject"}`, member, 200, `{"ok":true,"decision":"already_rejected","action_request_id":"` + arChat2 + `","status":"rejected"}`},
	} {
		if code, got := post(c.thread, c.run, c.body, c.who); code != c.code || got != c.want {
			t.Errorf("%s %s: %d %s", c.run[len(c.run)-1:], c.body, code, got)
		}
	}
	if j := actionJobs(t, db); len(j) != 1 || j[0].Args != `{"orgId": "org", "actionRequestId": "`+arChat+`"}` {
		t.Errorf("jobs: %+v", j)
	}
	var row string
	_ = db.QueryRow(ctx, `select status || ' ' || approved_by_user_id || ' ' || rejection_reason from action_request where id = $1`, arChat2).Scan(&row)
	if row != "rejected member Not now" {
		t.Errorf("rejected row: %s", row)
	}
	if got := lastAudit(t, db, arChat); got != `approved {"kind": "note", "approvedBy": "member", "soloOperator": false}` {
		t.Errorf("approve audit: %s", got)
	}
	if got := lastAudit(t, db, arChat2); got != `rejected {"kind": "note", "reason": "Not now", "rejectedBy": "member"}` {
		t.Errorf("reject audit: %s", got)
	}
	var event string
	_ = db.QueryRow(ctx, `select kind || ' ' || payload::text from work_run_event where run_id = $1 and kind = 'action_request_result'`, ru1).Scan(&event)
	if want := `action_request_result {"kind": "note", "type": "action_request_result", "status": "rejected", "rejection_reason": "Not now", "action_request_id": "` + arChat2 + `"}`; event != want {
		t.Errorf("event: %s", event)
	}
}
