package webfront

import (
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestOutputsListTheirSubjects(t *testing.T) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	seed(t, db, `insert into workflow_output (org_id, workflow_run_id, work_run_id, kind, title, mood, payload, last_seen_at)
		values ('org', $1, '70000000-0000-4000-8000-000000000002', 'finding', 'Crane slow', 'watch',
		  '{"subjects":[{"table":"quay_cranes","key":"STS-04"},{"table":"","key":"x"}],"note":"kept"}', '2026-11-20T10:00:00Z')`, runHeld)
	seed(t, db, `insert into workflow_output (org_id, workflow_run_id, work_run_id, kind, title, mood, last_seen_at)
		values ('org', $1, '70000000-0000-4000-8000-000000000004', 'finding', 'Theirs', 'act', '2026-11-21T10:00:00Z')`, runOther)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	admin, member := sessionCookie("admin"), sessionCookie("member")

	titles := func(path string, who map[string]string) []string {
		w := call(f, "GET", path, "", who)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var got []string
		for _, item := range decode(t, w)["outputs"].([]any) {
			got = append(got, item.(map[string]any)["title"].(string))
		}
		return got
	}
	if got := titles("/api/v1/outputs", member); len(got) != 1 || got[0] != "Crane slow" {
		t.Errorf("a member sees %v; want only the output of a workflow they hold", got)
	}
	if got := titles("/api/v1/outputs", admin); len(got) != 3 {
		t.Errorf("an admin sees %v; want all three", got)
	}
	if got := titles("/api/v1/outputs?mood=watch", admin); len(got) != 1 || got[0] != "Crane slow" {
		t.Errorf("mood filter: %v", got)
	}
	if got := titles("/api/v1/outputs?since=2026-11-20T12:00:00Z", admin); len(got) != 2 {
		t.Errorf("since filter: %v; want the two outputs seen after that time", got)
	}
	if w := call(f, "GET", "/api/v1/outputs?since=yesterday", "", admin); w.Code != 400 {
		t.Errorf("a since that is not a time: %d", w.Code)
	}

	first := decode(t, call(f, "GET", "/api/v1/outputs", "", member))["outputs"].([]any)[0].(map[string]any)
	subjects := first["subjects"].([]any)
	if len(subjects) != 1 || subjects[0].(map[string]any)["table"] != "quay_cranes" || subjects[0].(map[string]any)["key"] != "STS-04" {
		t.Errorf("subjects %v; want the one complete subject", subjects)
	}
}

func TestEmitOutputNamesItsSubjects(t *testing.T) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	tools := f.API.runTools(turnBinding{OrgID: "org"})
	subjectsOf := func(out any) string {
		payload, _ := jsField(out, "payload")
		return jsStringify(jsGet(payload, "subjects"), false)
	}

	given, err := tools.emitOutput(t.Context(), outputIn{OrgID: "org", WorkflowRunID: runOrg, WorkRunID: "70000000-0000-4000-8000-000000000001",
		Kind: "finding", Subjects: []outputSubject{{Table: "vessels", Key: "v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := subjectsOf(given); got != `[{"key":"v1","table":"vessels"}]` {
		t.Errorf("given subjects: %s", got)
	}

	seed(t, db, `update workflow_run set trigger_kind = 'subscription',
		trigger_payload = '{"table":"yard_blocks","primary_key":{"id":"B5"},"snapshot":{}}' where id = $1`, runHeld)
	fromRow, err := tools.emitOutput(t.Context(), outputIn{OrgID: "org", WorkflowRunID: runHeld, WorkRunID: "70000000-0000-4000-8000-000000000002", Kind: "finding"})
	if err != nil {
		t.Fatal(err)
	}
	if got := subjectsOf(fromRow); got != `[{"key":"B5","table":"yard_blocks"}]` {
		t.Errorf("subjects from the row that started the run: %s", got)
	}

	none, err := tools.emitOutput(t.Context(), outputIn{OrgID: "org", WorkflowRunID: runOther, WorkRunID: "70000000-0000-4000-8000-000000000004", Kind: "finding"})
	if err != nil {
		t.Fatal(err)
	}
	if got := subjectsOf(none); got != "undefined" && got != "null" && got != "" {
		t.Errorf("a scheduled run with no subjects: %s", got)
	}
}
