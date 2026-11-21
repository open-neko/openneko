package webfront

import (
	"encoding/json"
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

func TestParallelSearchUpgradeAsksTheAdminOnce(t *testing.T) {
	f, db := memoryFront(t)
	bundle := seedBundledPack(t, db, "parallel-search")
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := (&packApplyRun{api: f.API, orgID: "org", packID: "parallel-search"}).writeNative(t.Context(), tx, bundle, map[string]any{}, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	choices := func() string {
		t.Helper()
		w := call(f, "GET", "/api/v1/policies", "", sessionCookie("admin"))
		var out struct {
			PackChoices []map[string]any `json:"packChoices"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.PackChoices == nil {
			t.Fatalf("policies: %d %s", w.Code, w.Body)
		}
		ids := []string{}
		for _, c := range out.PackChoices {
			ids = append(ids, c["policyId"].(string))
		}
		return strings.Join(ids, ",")
	}
	if got := choices(); got != "" {
		t.Fatalf("a new install was asked again: %s", got)
	}

	policyID := scalar(t, db, `select id::text from action_policy where org_id = 'org' and name = 'Parallel web search'`)
	seed(t, db, `update action_policy set mode = 'auto_approve' where id = $1`, policyID)
	if got := choices(); got != policyID {
		t.Fatalf("an install from pack 0.1.0, which shipped the rule in auto, was not asked: %q", got)
	}
	choose := func(who, body string) int {
		return call(f, "POST", "/api/v1/policies/"+policyID+"/pack-choice", body, sessionCookie(who)).Code
	}
	if code := choose("member", `{"mode":"ask"}`); code != 403 {
		t.Fatalf("a member chose: %d", code)
	}
	if code := choose("admin", `{"mode":"never"}`); code != 400 {
		t.Fatalf("a mode that is not ask or auto: %d", code)
	}
	if code := choose("admin", `{"mode":"auto"}`); code != 200 {
		t.Fatalf("choose auto: %d", code)
	}
	if got := scalar(t, db, `select mode || ' ' || (select config->>'parallel_search.approval' from pack_install where pack_id = 'parallel-search')
		from action_policy where id = $1`, policyID); got != "auto_approve auto" || choices() != "" {
		t.Fatalf("after auto: %q, choices %q", got, choices())
	}
	if code := choose("admin", `{"mode":"ask"}`); code != 404 {
		t.Fatalf("a second choice: %d", code)
	}
	if got := scalar(t, db, `select count(*)::text from audit_chain where event = 'policy:pack_choice' and entity_id = $1`, policyID); got != "1" {
		t.Fatalf("audit %s", got)
	}

	seed(t, db, `update action_policy set mode = 'auto_approve' where id = $1`, policyID)
	seed(t, db, `update pack_install set config = config - 'parallel_search.approval' where pack_id = 'parallel-search'`)
	if code := choose("admin", `{"mode":"ask"}`); code != 200 {
		t.Fatalf("choose ask: %d", code)
	}
	if got := scalar(t, db, `select mode from action_policy where id = $1`, policyID); got != "approval_required" {
		t.Fatalf("after ask: %s", got)
	}

	for body, want := range map[string]string{
		`{"objective":"x","search_queries":["a","b","c","d","e","f"]}`:            "at most 5",
		`{"objective":"x","search_queries":["` + strings.Repeat("q", 201) + `"]}`: "at most 200",
		`{"objective":"x","search_queries":"customer list"}`:                      "must be a list",
	} {
		in, _ := jsParse([]byte(body))
		if _, err := guardPackBody("parallel_search.web_search", in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
	}
}
