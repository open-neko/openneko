package webfront

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMineAnswerFindsTheValueTheAnswerStates(t *testing.T) {
	calls := []compileCall{
		{Ref: "c1", OK: true, Result: `{"data":{"salesorderheader":{"max_orderdate":"2025-06-29T00:00:00"}}}`},
		{Ref: "c2", OK: false, Result: `{"errors":[{"message":"x"}]}`},
		{Ref: "c3", OK: true, Query: "q3", Result: `{"data":{"top":[{"productid":389,"name":"Hex Nut 2","sum_quantity":657},{"name":"Thin-Jam Hex Nut 3"}]}}`},
	}
	call, path, value, kind, ok := mineAnswer("In the Tool Crib, **Hex Nut 2** has the most units.", calls)
	if !ok || call.Query != "q3" || kind != "name" || value != "Hex Nut 2" {
		t.Fatalf("got %v %v %q %q %v", call.Ref, path, value, kind, ok)
	}
	if raw, _ := json.Marshal(path); string(raw) != `["top",0,"name"]` {
		t.Fatalf("path %s", raw)
	}
	if _, _, _, _, ok := mineAnswer("In the Tool Crib, Hex Nut 2 has the most units: 657.", calls); ok {
		t.Fatal("an answer that states two values from the results is ambiguous")
	}
	buyers := []compileCall{{Ref: "c1", OK: true, Result: `{"data":{"buyers":{"count_customerid":5901},"orders":{"count_salesorderid":6297}}}`}}
	if _, _, _, _, ok := mineAnswer("Q1 2025 had 5,901 distinct buyers across 6,297 orders.", buyers); ok {
		t.Fatal("buyers and orders in one answer must not give a test")
	}
	_, _, value, kind, ok = mineAnswer("The Tool Crib holds 657 units of its top item.", calls)
	if !ok || kind != "number" || value != "657" {
		t.Fatalf("number: %q %q %v", value, kind, ok)
	}
	if _, _, _, _, ok := mineAnswer("The newest order is from 2025.", calls); ok {
		t.Fatal("a year or a date is not a test value")
	}
	if _, _, _, _, ok := mineAnswer("Product 389 is first.", calls); ok {
		t.Fatal("an id is not a test value")
	}
}

func TestAnswerHoldsAcceptsRoundedFigures(t *testing.T) {
	cases := []struct {
		answer, kind, value string
		want                bool
	}{
		{"Revenue was $5.05M last year.", "number", "5048686.86", true},
		{"Revenue was $5,048,687.", "number", "5048686.86", true},
		{"Revenue was $4.9M.", "number", "5048686.86", false},
		{"Germany's top store is GLOBAL BIKE RETAILERS.", "name", "Global Bike Retailers", true},
		{"```neko_vitals\n{\"value\": \"657\"}\n```\nNo figure here.", "number", "657", false},
	}
	for _, c := range cases {
		if got := answerHolds(c.answer, c.kind, c.value); got != c.want {
			t.Errorf("%q %s %s: got %v", c.answer, c.kind, c.value, got)
		}
	}
}

func TestValueAtFollowsTheMinedPath(t *testing.T) {
	data, _ := compileData(`{"data":{"top":[{"product":{"name":"Hex Nut 2"}}]}}`)
	v, ok := valueAt(data, []any{"top", 0.0, "product", "name"})
	if !ok || v != "Hex Nut 2" {
		t.Fatalf("got %v %v", v, ok)
	}
	if _, ok := valueAt(data, []any{"top", 3.0, "product"}); ok {
		t.Fatal("a missing row has no value")
	}
}

func TestParsePlaybookRuleKeepsOneSafeRule(t *testing.T) {
	if got := parsePlaybookRule(`{"rule": "For revenue, sum salesorderheader.subtotal, not totaldue."}`); !strings.HasPrefix(got, "For revenue") {
		t.Fatalf("got %q", got)
	}
	for _, raw := range []string{`{"rule": ""}`, `not json`, `{"rule": "Use api_key sk-abcdefghijklmnopqrstu"}`, `{"rule": "Mail ops@example.com"}`,
		`{"rule": "` + strings.Repeat("x", playbookItemMax+1) + `"}`} {
		if got := parsePlaybookRule(raw); got != "" {
			t.Errorf("%s: kept %q", raw, got)
		}
	}
	if playbookSection(nil) != "" || !strings.Contains(playbookSection([]string{"a", "b"}), "- a\n- b") {
		t.Fatal("playbook section")
	}
}

func TestPlaybookLearnKeepsARuleThatFixesAFailedTestAndBreaksNone(t *testing.T) {
	f, db, _ := hostFront(t)
	ctx := t.Context()
	const thread, other = "c4000000-0000-4000-8000-000000000001", "c4000000-0000-4000-8000-000000000002"
	const runA, runB, runC, runD = "c5000000-0000-4000-8000-00000000000a", "c5000000-0000-4000-8000-00000000000b",
		"c5000000-0000-4000-8000-00000000000c", "c5000000-0000-4000-8000-00000000000d"
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org'), ($2, 'org')`, thread, other)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend, status) values
		($1, 'org', $5, 'ax', 'completed'), ($2, 'org', $5, 'ax', 'completed'), ($3, 'org', $6, 'ax', 'completed'), ($4, 'org', $6, 'ax', 'completed')`,
		runA, runB, runC, runD, thread, other)
	seed(t, db, `insert into work_message (org_id, thread_id, run_id, role, content, created_at) values
		('org', $1, $2, 'user', 'what was revenue in 2024?', now() - interval '10 minutes'),
		('org', $1, $2, 'assistant', 'Revenue in 2024 was $9.9M.', now() - interval '9 minutes'),
		('org', $1, $3, 'user', 'that is wrong, revenue means subtotal', now() - interval '8 minutes'),
		('org', $1, $3, 'assistant', 'Revenue in 2024 was $8,912,345.', now() - interval '7 minutes'),
		('org', $4, $5, 'user', 'how many stores do we have?', now() - interval '6 minutes'),
		('org', $4, $5, 'assistant', 'We have 701 stores.', now() - interval '5 minutes'),
		('org', $4, $6, 'user', 'hello', now() - interval '4 minutes'),
		('org', $4, $6, 'assistant', 'Hello. Ask me about your data.', now() - interval '3 minutes')`,
		thread, runA, runB, other, runC, runD)
	call := func(run, id, query, result string) {
		seed(t, db, `insert into work_run_event (org_id, thread_id, run_id, kind, payload) values
			('org', (select thread_id from work_run where id = $1::uuid), $1::uuid, 'tool_start', jsonb_build_object('type', 'tool_start', 'id', $2::text, 'name', 'mcp_neko_graphjin_execute_graphql', 'input', jsonb_build_object('query', $3::text))),
			('org', (select thread_id from work_run where id = $1::uuid), $1::uuid, 'tool_end', jsonb_build_object('type', 'tool_end', 'id', $2::text, 'result', jsonb_build_object('content', jsonb_build_array(jsonb_build_object('type', 'text', 'text', $4::text)))))`,
			run, id, query, result)
	}
	call(runA, "a1", "{ salesorderheader { sum_totaldue } }", `{"data":{"salesorderheader":{"sum_totaldue":9912345}}}`)
	call(runB, "b1", "{ salesorderheader { sum_subtotal } }", `{"data":{"salesorderheader":{"sum_subtotal":8912345}}}`)
	call(runC, "c1", "{ store { count_businessentityid } }", `{"data":{"store":{"count_businessentityid":701}}}`)

	var replays []string
	deps := playbookDeps{
		replay: func(tc minedTest, playbook []string) (minedReplay, error) {
			replays = append(replays, tc.Question+" | "+strings.Join(playbook, ";"))
			pass := tc.Question != "what was revenue in 2024?" || len(playbook) > 0
			return minedReplay{Pass: pass, Queries: 2, Oracle: tc.Value, Answer: "answer"}, nil
		},
		propose: func(input string) (string, error) {
			if !strings.Contains(input, "that is wrong, revenue means subtotal") || strings.Contains(input, "8912345") && strings.Contains(input, "rule\": \"8912345") {
				t.Fatalf("reflector input: %s", input)
			}
			return `{"rule": "Revenue means the sum of salesorderheader.subtotal."}`, nil
		},
	}
	res, err := f.API.playbookLearn(ctx, "org", deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mine.Mined != 2 || res.Mine.Skipped["marked wrong"] != 1 || res.Mine.Skipped["no query"] != 1 {
		t.Fatalf("mine: %+v", res.Mine)
	}
	var question, correction, value string
	if err := db.QueryRow(ctx, `select question, coalesce(correction, ''), value from mined_test where run_id = $1::uuid`, runB).Scan(&question, &correction, &value); err != nil {
		t.Fatal(err)
	}
	if question != "what was revenue in 2024?" || correction != "that is wrong, revenue means subtotal" || value != "8912345" {
		t.Fatalf("corrected test: %q %q %q", question, correction, value)
	}
	if res.Replayed != 2 || res.Passed != 1 || len(res.Items) != 1 || res.Items[0].Decision != "active" {
		t.Fatalf("learn: %+v, replays %v", res, replays)
	}
	items, err := f.API.activePlaybook(ctx, "org")
	if err != nil || len(items) != 1 || !strings.Contains(f.API.playbookContext(ctx, "org"), "salesorderheader.subtotal") {
		t.Fatalf("playbook %v %v", items, err)
	}

	again, err := f.API.playbookLearn(ctx, "org", deps)
	if err != nil {
		t.Fatal(err)
	}
	if again.Mine.Mined != 0 || again.Replayed != 0 || len(again.Items) != 0 {
		t.Fatalf("a second pass redid work: %+v", again)
	}
}
