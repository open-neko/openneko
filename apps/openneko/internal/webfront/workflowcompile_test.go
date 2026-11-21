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
	p := compileProposal{AnswerCall: "c3", AnchorCall: "c1",
		Query: `{ salesorderheader(where: { orderdate: { gte: $day } }) { sum_subtotal } }`,
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
			compileProposal{AnswerCall: "c2", AnchorCall: "c1", Query: query,
				Variables: []compiledVar{{Name: "day", Value: "2025-06-28", Base: "today", Offset: -1, Format: "date"}}},
			nil, "gives 2026-11-20"},
		"the rerun returns other data": {
			compileProposal{AnswerCall: "c2", AnchorCall: "c1", Query: query, Variables: []compiledVar{good}},
			map[string]string{`{"day":"2025-06-28"}`: `{"data":{"a":{"n":5}}}`}, "does not return the run's result"},
		"the answer call failed": {
			compileProposal{AnswerCall: "c9", Query: query}, nil, "answer_call"},
		"the query writes": {
			compileProposal{AnswerCall: "c2", Query: `mutation { a(insert: {}) { n } }`}, nil, "not a read"},
		"the variable is not in the query": {
			compileProposal{AnswerCall: "c2", AnchorCall: "c1", Query: `{ a { n } }`, Variables: []compiledVar{good}}, nil, "not in the query"},
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
