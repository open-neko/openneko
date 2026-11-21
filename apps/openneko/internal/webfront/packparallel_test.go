package webfront

import (
	"strings"
	"testing"
)

func TestParallelSearchAsksUnlessTheAdminChoseAuto(t *testing.T) {
	f, db := memoryFront(t)
	bundle := seedBundledPack(t, db, "parallel-search")
	var input map[string]any
	for _, in := range bundle.Manifest.Normalized()["inputs"].([]any) {
		if m := in.(map[string]any); m["key"] == "parallel_search.approval" {
			input = m
		}
	}
	if input == nil || input["default"] != "ask" || !strings.Contains(input["description"].(string), "leaves this install for Parallel") {
		t.Fatalf("the install choice: %v", input)
	}
	mode := func(inputs map[string]any) string {
		t.Helper()
		tx, err := db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context()) //nolint:errcheck
		p := &packApplyRun{api: f.API, orgID: "org", packID: "parallel-search"}
		if err := p.writeNative(t.Context(), tx, bundle, inputs, map[string]string{}); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := tx.QueryRow(t.Context(), `select mode from action_policy where org_id = 'org' and name = 'Parallel web search'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, c := range []struct {
		inputs map[string]any
		want   string
	}{
		{map[string]any{}, "approval_required"},
		{map[string]any{"parallel_search.approval": "ask"}, "approval_required"},
		{map[string]any{"parallel_search.approval": "never"}, "approval_required"},
		{map[string]any{"parallel_search.approval": "auto"}, "auto_approve"},
	} {
		if got := mode(c.inputs); got != c.want {
			t.Errorf("%v: %s, want %s", c.inputs, got, c.want)
		}
	}

	long, _ := jsParse([]byte(`{"objective":"` + strings.Repeat("a", 501) + `"}`))
	if _, err := guardPackBody("parallel_search.web_search", long); err == nil || !strings.Contains(err.Error(), "at most 500") {
		t.Fatalf("a long objective: %v", err)
	}
	short, _ := jsParse([]byte(`{"objective":"Competitor price changes in October 2026","search_queries":["competitor price change"]}`))
	if _, err := guardPackBody("parallel_search.web_search", short); err != nil {
		t.Fatal(err)
	}
}
