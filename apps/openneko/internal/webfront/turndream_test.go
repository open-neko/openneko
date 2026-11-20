package webfront

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	dreamThread = "d1000000-0000-4000-8000-000000000001"
	dreamRun    = "d2000000-0000-4000-8000-000000000001"
)

func TestDreamLessonsContextOffersTheClosestActiveLessonsOfTheOrg(t *testing.T) {
	t.Setenv("NEKO_EMBEDDING_URL", "")
	t.Setenv("NEKO_EMBEDDING_MODEL", "")
	db := testpg.Migrated(t)
	seedOrg(t, db)
	seed(t, db, `insert into organization (id, name) values ('other', 'Other')`)
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org')`, dreamThread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', $2, 'test')`, dreamRun, dreamThread)
	seed(t, db, `insert into dream_lesson (org_id, kind, question_shape, body, state) values
		('org', 'query', 'revenue by product category in a period', E'query {\n  category_revenue\n}', 'active'),
		('org', 'data_fact', 'the date range of sales orders', 'Order dates run from 2022-05-30 to 2025-06-29.', 'active'),
		('org', 'query', 'revenue by product category, old version', 'query { retired }', 'retired'),
		('other', 'query', 'revenue by product category in a period', 'query { other_org }', 'active')`)
	for n := range 6 {
		seed(t, db, `insert into dream_lesson (org_id, kind, question_shape, body) values ('org', 'schema_fact', $1, 'filler')`,
			"headcount of department "+string(rune('a'+n)))
	}
	a := &API{DB: db}
	ctx := context.Background()

	got, err := a.dreamLessonsContext(ctx, "org", dreamRun, "what was the revenue for each product category in Q3 2024")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if !strings.Contains(lines[0], "] query · revenue by product category in a period") ||
		lines[1] != "  query {" || lines[2] != "    category_revenue" {
		t.Fatalf("the closest lesson is not first, with its body indented:\n%s", got)
	}
	if n := strings.Count(got, "\n- [") + 1; n != dreamLessonsPerTurn {
		t.Fatalf("offered %d lessons:\n%s", n, got)
	}
	if strings.Contains(got, "retired") || strings.Contains(got, "other_org") {
		t.Fatalf("offered a retired lesson or a lesson of another org:\n%s", got)
	}

	var uses, used int
	if err := db.QueryRow(ctx, `select count(*) from dream_lesson_use where run_id = $1 and org_id = 'org' and outcome is null`, dreamRun).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select count(*) from dream_lesson where use_count = 1 and last_used_at is not null`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if uses != dreamLessonsPerTurn || used != dreamLessonsPerTurn {
		t.Fatalf("recorded %d uses and %d used lessons", uses, used)
	}

	if _, err := a.dreamLessonsContext(ctx, "org", dreamRun, "what was the revenue for each product category in Q3 2024"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select count(*) from dream_lesson where use_count > 1`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Fatalf("a second call for the same run counted %d lessons again", used)
	}

	if got, err := a.dreamLessonsContext(ctx, "other", dreamRun, "  "); err != nil || got != "" {
		t.Fatalf("an empty question gave %q %v", got, err)
	}
	if got := lessonsSection(""); got != "" {
		t.Fatalf("a turn with no lessons has a lessons section: %q", got)
	}
	if got := lessonsSection("- [x] query · shape"); !strings.HasPrefix(got, "<lessons>") || !strings.Contains(got, "- [x] query · shape\n</lessons>") {
		t.Fatalf("lessons section: %q", got)
	}
}

func TestTraceStepKeepsTheErrorTextOfAFailedToolResult(t *testing.T) {
	reply, _ := jsParse([]byte(`{"content":[{"type":"text","text":"{\"errors\":[{\"message\":\"Field 'totl' is not a column on the queried table; key sk-planted-1\"}]}"}]}`))
	var contents []string
	unregister := registerTraceSink("run-dream-trace", func(_, content *jsObj) {
		contents = append(contents, jsStringify(content, false))
	}, secretScrubber([]string{"sk-planted-1"}))
	args, _ := jsParse([]byte(`{"query":"query { orders { totl } }"}`))
	_, _ = traceGraphjinToolCall(traceBinding{RunID: "run-dream-trace", OrgID: "org", Kind: "work"}, "execute_graphql", args.(*jsObj),
		func() (any, error) { return reply, nil })
	unregister()
	if len(contents) != 1 || !strings.Contains(contents[0], `"error":"Field 'totl' is not a column on the queried table; key [REDACTED]"`) {
		t.Fatalf("step content: %v", contents)
	}
}

const (
	passThread = "d3000000-0000-4000-8000-000000000001"
	passRunA   = "d4000000-0000-4000-8000-000000000001"
	passRunB   = "d4000000-0000-4000-8000-000000000002"
	freshRun   = "d4000000-0000-4000-8000-000000000003"
)

func dreamToolEvents(t *testing.T, db *pgxpool.Pool, thread, run string, seq int, id, query, result string) {
	t.Helper()
	start, _ := json.Marshal(map[string]any{"type": "tool_start", "id": id, "name": "execute_graphql", "input": map[string]any{"query": query}})
	end, _ := json.Marshal(map[string]any{"type": "tool_end", "id": id, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": result}}}})
	seed(t, db, `insert into work_run_event (org_id, thread_id, run_id, seq, kind, payload) values
		('org', $1, $2, $3, 'tool_start', $4::jsonb), ('org', $1, $2, $3 + 1, 'tool_end', $5::jsonb)`, thread, run, seq, string(start), string(end))
}

func TestDreamSweepLearnsFromASettledThreadOnce(t *testing.T) {
	t.Setenv("NEKO_EMBEDDING_URL", "")
	t.Setenv("NEKO_EMBEDDING_MODEL", "")
	db := testpg.Migrated(t)
	seedOrg(t, db)
	ctx := context.Background()
	old := time.Now().Add(-3 * time.Hour)
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org'), ($2, 'org')`, passThread, dreamThread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', $3, 'test'), ($2, 'org', $3, 'test'), ($4, 'org', $5, 'test')`,
		passRunA, passRunB, passThread, freshRun, dreamThread)
	seed(t, db, `insert into work_message (org_id, thread_id, run_id, role, content, created_at) values
		('org', $1, null, 'user', 'units of Clothing sold in Q3 2023?', $3),
		('org', $1, $2, 'assistant', 'Clothing sold 6,967 units.', $3::timestamptz + interval '1 second')`, passThread, passRunA, old)
	seed(t, db, `insert into work_message (org_id, thread_id, run_id, role, content) values ('org', $1, $2, 'user', 'a fresh question')`, dreamThread, freshRun)
	dreamToolEvents(t, db, passThread, passRunA, 1, "op1", `{ productcategory { totl } }`, `{"errors":[{"message":"Field 'totl' is not a column on the queried table"}]}`)
	dreamToolEvents(t, db, passThread, passRunA, 3, "op2", `{ productcategory { productcategoryid name } }`, `{"data":{"productcategory":[{"productcategoryid":3,"name":"Clothing"}]}}`)
	dreamToolEvents(t, db, passThread, passRunA, 5, "op3", `{ salesorderdetail(where: { product: { productsubcategory: { productcategoryid: { eq: 3 } } } }) { sum_orderqty } }`, `{"data":{"salesorderdetail":[{"sum_orderqty":6967}]}}`)
	dreamToolEvents(t, db, passThread, passRunA, 7, "op4", `mutation { delete_orders { id } }`, `{"data":{}}`)
	dreamToolEvents(t, db, dreamThread, freshRun, 1, "op1", `{ unsettled { id } }`, `{"data":{}}`)

	a := &API{DB: db}
	var inputs []string
	reply := `Here you go: {"lessons":[
		{"kind":"query","question_shape":"units sold of one product category in a period","call":"c3","text":"Category ids: 3 Clothing."},
		{"kind":"data_fact","question_shape":"ids of the product categories","call":"c2","text":"productcategory.productcategoryid: 3 Clothing."},
		{"kind":"dead_end","question_shape":"columns of productcategory","call":"c1","text":"productcategory has no column totl."},
		{"kind":"query","question_shape":"a failed query","call":"c1","text":""},
		{"kind":"query","question_shape":"a write","call":"c4","text":""},
		{"kind":"query","question_shape":"a missing call","call":"c9","text":""},
		{"kind":"data_fact","question_shape":"a secret","call":"c2","text":"the api_key is sk-0123456789abcdef0123"},
		{"kind":"rumour","question_shape":"x","call":"c2","text":"y"}]}`
	propose := func(orgID, input string) (string, error) {
		inputs = append(inputs, input)
		return reply, nil
	}
	if err := a.dreamSweep(ctx, time.Now().Add(-dreamSettlement), propose); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 {
		t.Fatalf("the pass read %d threads; the unsettled thread must wait", len(inputs))
	}
	for _, want := range []string{"[user] units of Clothing sold in Q3 2023?", "c1 FAILED: { productcategory { totl } }",
		"error: Field 'totl' is not a column on the queried table", "c3 ok: { salesorderdetail", `result: {"data":{"salesorderdetail":[{"sum_orderqty":6967}]}}`, "Existing lessons:\nnone"} {
		if !strings.Contains(inputs[0], want) {
			t.Fatalf("the pass input lacks %q:\n%s", want, inputs[0])
		}
	}
	if strings.Index(inputs[0], "c3 ok") > strings.Index(inputs[0], "[assistant] Clothing sold") {
		t.Fatalf("the calls of a run do not stand before its answer:\n%s", inputs[0])
	}

	type row struct{ Kind, State, Body string }
	lessons := func() map[string]row {
		t.Helper()
		rows, err := db.Query(ctx, `select question_shape, kind, state, body from dream_lesson where org_id = 'org'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]row{}
		for rows.Next() {
			var shape string
			var r row
			if err := rows.Scan(&shape, &r.Kind, &r.State, &r.Body); err != nil {
				t.Fatal(err)
			}
			out[shape] = r
		}
		return out
	}
	got := lessons()
	query := got["units sold of one product category in a period"]
	if len(got) != 3 || query.State != "active" || !strings.HasPrefix(query.Body, "Category ids: 3 Clothing.\n{ salesorderdetail(where:") ||
		got["ids of the product categories"].State != "active" || got["columns of productcategory"].State != "candidate" {
		t.Fatalf("lessons after the pass: %+v", got)
	}
	var decisions string
	if err := db.QueryRow(ctx, `select decisions::text from dream_pass where thread_id = $1`, passThread).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"the query failed in its run", "the call is not a read", "names no call of the thread", "holds a secret-shaped value", "unknown kind"} {
		if !strings.Contains(decisions, want) {
			t.Fatalf("the pass record lacks %q: %s", want, decisions)
		}
	}

	if err := a.dreamSweep(ctx, time.Now().Add(-dreamSettlement), propose); err != nil || len(inputs) != 1 {
		t.Fatalf("a second sweep read the thread again: %d inputs, %v", len(inputs), err)
	}

	seed(t, db, `insert into work_message (org_id, thread_id, run_id, role, content, created_at) values
		('org', $1, null, 'user', 'and for Q4?', $3), ('org', $1, $2, 'assistant', 'Clothing sold 7,100 units.', $3::timestamptz + interval '1 second')`,
		passThread, passRunB, old.Add(30*time.Minute))
	dreamToolEvents(t, db, passThread, passRunB, 1, "op1", `{ productcategory { totl } }`, `{"errors":[{"message":"Field 'totl' is not a column on the queried table"}]}`)
	dreamToolEvents(t, db, passThread, passRunB, 3, "op2", `{ productcategory { productcategoryid name } }`, `{"data":{"productcategory":[{"productcategoryid":4,"name":"Accessories"}]}}`)
	var deadEnd, fact string
	if err := db.QueryRow(ctx, `select id::text from dream_lesson where kind = 'dead_end'`).Scan(&deadEnd); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select id::text from dream_lesson where kind = 'data_fact'`).Scan(&fact); err != nil {
		t.Fatal(err)
	}
	reply = `{"lessons":[
		{"kind":"dead_end","question_shape":"columns of productcategory","call":"c5","text":"productcategory has no column totl.","same_as":"` + deadEnd + `"},
		{"kind":"data_fact","question_shape":"ids of the product categories","call":"c6","text":"productcategory.productcategoryid: 3 Clothing, 4 Accessories.","same_as":"` + fact + `"}]}`
	if err := a.dreamSweep(ctx, time.Now().Add(-dreamSettlement), propose); err != nil || len(inputs) != 2 {
		t.Fatalf("the thread with a new message was not read again: %d inputs, %v", len(inputs), err)
	}
	if !strings.Contains(inputs[1], "- id "+deadEnd+" · dead_end · columns of productcategory") {
		t.Fatalf("the pass input lacks the existing candidate lesson:\n%s", inputs[1])
	}
	got = lessons()
	if len(got) != 3 || got["columns of productcategory"].State != "active" ||
		got["ids of the product categories"].Body != "productcategory.productcategoryid: 3 Clothing, 4 Accessories." {
		t.Fatalf("lessons after the second pass: %+v", got)
	}
}
