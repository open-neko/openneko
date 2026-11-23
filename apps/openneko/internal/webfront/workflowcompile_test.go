package webfront

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

func TestCompiledVarValueCountsFromTheAlignedBase(t *testing.T) {
	today := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	anchor := time.Date(2025, 6, 29, 14, 30, 0, 0, time.UTC)
	cases := []struct {
		v    compiledVar
		want string
	}{
		{compiledVar{Base: "today", Align: "day", Offset: -1, Unit: "day", Format: "date"}, "2026-11-20"},
		{compiledVar{Base: "anchor", Align: "day", Offset: -7, Unit: "day", Format: "timestamp"}, "2025-06-22T00:00:00"},
		{compiledVar{Base: "anchor", Align: "quarter", Offset: -1, Unit: "quarter", Format: "date"}, "2025-01-01"},
		{compiledVar{Base: "today", Align: "year", Format: "date"}, "2026-01-01"},
		{compiledVar{Base: "today", Align: "week", Format: "date"}, "2026-11-16"},
		{compiledVar{Base: "anchor", Align: "month", Offset: 1, Unit: "month", Format: "date"}, "2025-07-01"},
	}
	for _, c := range cases {
		got, err := compiledVarValue(c.v, today, anchor)
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q, %v; want %q", c.v, got, err, c.want)
		}
	}
	if _, err := compiledVarValue(compiledVar{Name: "x", Base: "anchor"}, today, time.Time{}); err == nil {
		t.Error("an anchor variable with no anchor must fail")
	}
}

func TestWorkflowTodayUsesTheWorkflowTimeZone(t *testing.T) {
	at := time.Date(2026, 11, 21, 2, 0, 0, 0, time.UTC)
	if got := workflowToday("America/Los_Angeles", at); got.Day() != 20 {
		t.Fatalf("got %v", got)
	}
	if got := workflowToday("", at); got.Day() != 21 {
		t.Fatalf("got %v", got)
	}
}

// fakeCompileCall answers a query by the variables it gets.
func fakeCompileCall(answers map[string]string) (compileDataCall, *[]sandbox.DataQuery) {
	var seen []sandbox.DataQuery
	return func(_ context.Context, q sandbox.DataQuery) (*jsObj, error) {
		seen = append(seen, q)
		text, ok := answers[string(q.Variables)]
		if !ok {
			text = `{"errors":[{"message":"no data"}]}`
		}
		result, err := jsParse([]byte(`{"content":[{"type":"text","text":` + jsStringify(text, false) + `}]}`))
		if err != nil {
			return nil, err
		}
		obj, _ := result.(*jsObj)
		return obj, nil
	}, &seen
}

func TestVerifyCompiledKeepsAQueryThatGivesBackTheRunsResult(t *testing.T) {
	calls := []compileCall{
		{Ref: "c1", OK: true, Query: `{ salesorderheader { max_orderdate } }`, Result: `{"data":{"salesorderheader":{"max_orderdate":"2025-06-29T00:00:00"}}}`},
		{Ref: "c2", OK: false, Query: `{ bad }`, Result: `{"errors":[{"message":"x"}]}`},
		{Ref: "c3", OK: true, Query: `{ salesorderheader(where: { orderdate: { gte: "2025-06-28T00:00:00" } }) { sum_subtotal } }`,
			Result: `{"data":{"salesorderheader":{"sum_subtotal":51234.5}}}`},
	}
	p := compileProposal{AnswerCalls: []string{"c3"}, AnchorCall: "c1",
		Query:     `{ salesorderheader(where: { orderdate: { gte: $day } }) { sum_subtotal } }`,
		Variables: []compiledVar{{Name: "day", Value: "2025-06-28T00:00:00", Base: "anchor", Align: "day", Offset: -1, Unit: "day", Format: "timestamp"}}}
	call, seen := fakeCompileCall(map[string]string{`{"day":"2025-06-28T00:00:00"}`: `{"data":{"salesorderheader":{"sum_subtotal":51234.5}}}`})
	today := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	c, err := verifyCompiled(context.Background(), p, calls, today, call)
	if err != nil {
		t.Fatal(err)
	}
	if c.AnchorQuery != calls[0].Query || len(*seen) != 1 {
		t.Fatalf("compiled %+v, calls %v", c, *seen)
	}
	vars, err := compiledVarValues(c.Vars, today, time.Date(2025, 7, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(vars); string(raw) != `{"day":"2025-07-29T00:00:00"}` {
		t.Fatalf("next run variables: %s", raw)
	}
}

func TestVerifyCompiledRejectsAProposalThatDoesNotHold(t *testing.T) {
	calls := []compileCall{
		{Ref: "c1", OK: true, Query: `{ a { max_d } }`, Result: `{"data":{"a":{"max_d":"2025-06-29"}}}`},
		{Ref: "c2", OK: true, Query: `{ a(where: { d: { gte: "2025-06-28" } }) { n } }`, Result: `{"data":{"a":{"n":4}}}`},
	}
	good := compiledVar{Name: "day", Value: "2025-06-28", Base: "anchor", Align: "day", Offset: -1, Unit: "day", Format: "date"}
	query := `{ a(where: { d: { gte: $day } }) { n } }`
	today := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		p       compileProposal
		answers map[string]string
		want    string
	}{
		"the variable gives another date": {
			compileProposal{AnswerCalls: []string{"c2"}, AnchorCall: "c1", Query: query,
				Variables: []compiledVar{{Name: "day", Value: "2025-06-28", Base: "today", Offset: -1, Format: "date"}}},
			nil, "gives 2026-11-20"},
		"the rerun returns other data": {
			compileProposal{AnswerCalls: []string{"c2"}, AnchorCall: "c1", Query: query, Variables: []compiledVar{good}},
			map[string]string{`{"day":"2025-06-28"}`: `{"data":{"a":{"n":5}}}`}, "does not return the run's result"},
		"the answer call failed": {
			compileProposal{AnswerCalls: []string{"c9"}, Query: query}, nil, "answer call"},
		"the query writes": {
			compileProposal{AnswerCalls: []string{"c2"}, Query: `mutation { a(insert: {}) { n } }`}, nil, "not a read"},
		"the variable is not in the query": {
			compileProposal{AnswerCalls: []string{"c2"}, AnchorCall: "c1", Query: `{ a { n } }`, Variables: []compiledVar{good}}, nil, "not in the query"},
	}
	for name, c := range cases {
		call, _ := fakeCompileCall(c.answers)
		_, err := verifyCompiled(context.Background(), c.p, calls, today, call)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, c.want)
		}
	}
}

func TestWorkflowCompilableSkipsRunsWithInputOrScripts(t *testing.T) {
	base := workflowTurnOptions{TriggerKind: "cron", Workflow: workflowRecord{Steps: storedJSON(`[{"id":"q","description":"query"}]`)}}
	if !workflowCompilable(base) {
		t.Fatal("a plain scheduled run is compilable")
	}
	msg := base
	msg.UserMessage = "only Germany this time"
	scripted := base
	scripted.Workflow.Steps = storedJSON(`[{"id":"s","script":"run.py"}]`)
	input := base
	input.HasTriggerInput = true
	watcher := base
	watcher.TriggerKind = "subscription"
	for name, o := range map[string]workflowTurnOptions{"message": msg, "script": scripted, "input": input, "subscription": watcher} {
		if workflowCompilable(o) {
			t.Errorf("%s: compilable", name)
		}
	}
}

func TestCompileAnchorFindsTheFirstDate(t *testing.T) {
	got, ok := compileAnchor(`{"data":{"salesorderheader":[{"orderdate":"2026-11-13 09:03:52.557158"}]}}`)
	if !ok || got.Format("2006-01-02") != "2026-11-13" {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := compileAnchor(`{"data":{"n":4}}`); ok {
		t.Fatal("no date, no anchor")
	}
}

func TestCompiledWorkflowContextRunsTheStoredQueryAndDropsItOnFailure(t *testing.T) {
	f, db, _ := hostFront(t)
	ctx := t.Context()
	const wfID, runID = "c6000000-0000-4000-8000-000000000001", "c6000000-0000-4000-8000-000000000002"
	seed(t, db, `insert into workflow_definition (id, org_id, name, goal) values ($1, 'org', 'Daily revenue', 'sum yesterday')`, wfID)
	o := workflowTurnOptions{TriggerKind: "cron", Workflow: workflowRecord{ID: wfID, OrgID: "org", Goal: "sum yesterday", CronTimezone: "UTC"}}
	vars, _ := json.Marshal([]compiledVar{{Name: "day", Base: "anchor", Align: "day", Offset: -1, Unit: "day", Format: "date"}})
	seed(t, db, `insert into workflow_compiled_query (workflow_id, org_id, anchor_query, query, variables, source_run_id, goal_hash)
		values ($1, 'org', '{ a { max_d } }', '{ a(where: { d: { eq: $day } }) { n } }', $2::jsonb, $3, $4)`, wfID, string(vars), runID, workflowGoalHash(o.Workflow))
	call := func(_ context.Context, q sandbox.DataQuery) (*jsObj, error) {
		text := `{"data":{"a":{"max_d":"2025-06-29"}}}`
		if strings.Contains(q.Query, "$day") {
			if string(q.Variables) != `{"day":"2025-06-28"}` {
				t.Fatalf("variables %s", q.Variables)
			}
			text = `{"data":{"a":{"n":42}}}`
		}
		r, _ := jsParse([]byte(`{"content":[{"type":"text","text":` + jsStringify(text, false) + `}]}`))
		return r.(*jsObj), nil
	}
	block, err := f.API.compiledWorkflowContext(ctx, o, time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC), call)
	if err != nil || !strings.Contains(block, `{"data":{"a":{"n":42}}}`) || !strings.Contains(block, `{"day":"2025-06-28"}`) {
		t.Fatalf("block %q, %v", block, err)
	}
	var uses int
	if err := db.QueryRow(ctx, `select uses from workflow_compiled_query where workflow_id = $1`, wfID).Scan(&uses); err != nil || uses != 1 {
		t.Fatalf("uses %d %v", uses, err)
	}
	failing := func(context.Context, sandbox.DataQuery) (*jsObj, error) {
		r, _ := jsParse([]byte(`{"isError":true,"content":[{"type":"text","text":"{\"errors\":[{\"message\":\"gone\"}]}"}]}`))
		return r.(*jsObj), nil
	}
	if block, err := f.API.compiledWorkflowContext(ctx, o, time.Now(), failing); err != nil || block != "" {
		t.Fatalf("a failing query must give no block: %q %v", block, err)
	}
	var left int
	if err := db.QueryRow(ctx, `select count(*) from workflow_compiled_query where workflow_id = $1`, wfID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("a failing compiled query must be dropped: %d %v", left, err)
	}
	seed(t, db, `insert into workflow_compiled_query (workflow_id, org_id, query, source_run_id, goal_hash) values ($1, 'org', '{ a { n } }', $2, 'old goal')`, wfID, runID)
	if c, err := f.API.loadCompiledQuery(ctx, o.Workflow); err != nil || c != nil {
		t.Fatalf("a query for another goal must be dropped: %v %v", c, err)
	}
}

func TestVerifyCompiledJoinsSeveralAnswerCallsUnderAliases(t *testing.T) {
	calls := []compileCall{
		{Ref: "c1", OK: true, Result: `{"data":{"a":{"max_d":"2025-06-29"}}}`},
		{Ref: "c2", OK: true, Result: `{"data":{"salesorderheader":{"count_salesorderid":0}}}`},
		{Ref: "c3", OK: true, Result: `{"data":{"salesorderheader":[]}}`},
	}
	p := compileProposal{AnswerCalls: []string{"c2", "c3"}, AnchorCall: "c1",
		Query:     `{ n: salesorderheader(where: { d: { lt: $cut } }) { count_salesorderid } oldest: salesorderheader(where: { d: { lt: $cut } }, limit: 3) { salesorderid } }`,
		Variables: []compiledVar{{Name: "cut", Value: "2025-06-24", Base: "anchor", Offset: -5, Format: "date"}}}
	call, _ := fakeCompileCall(map[string]string{`{"cut":"2025-06-24"}`: `{"data":{"n":{"count_salesorderid":0},"oldest":[]}}`})
	if _, err := verifyCompiled(context.Background(), p, calls, time.Now(), call); err != nil {
		t.Fatal(err)
	}
	call, _ = fakeCompileCall(map[string]string{`{"cut":"2025-06-24"}`: `{"data":{"n":{"count_salesorderid":0}}}`})
	if _, err := verifyCompiled(context.Background(), p, calls, time.Now(), call); err == nil {
		t.Fatal("a compiled query that drops one answer call's data must fail")
	}
	if p, ok := parseCompileProposal(`{"answer_call":"c2","query":"{ a { n } }"}`); !ok || len(p.AnswerCalls) != 1 {
		t.Fatal("a single answer_call is still read")
	}
}
