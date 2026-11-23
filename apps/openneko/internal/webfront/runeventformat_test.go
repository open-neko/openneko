package webfront

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestOldTabsGetTheFirstAttemptOnly(t *testing.T) {
	f, db := signedInChatFront(t)
	member := sessionCookie("member")
	runID := insertRun(t, db, threadMine, "completed")
	old := `{"type":"message","role":"assistant","content":"Old"}`
	e1 := insertEvent(t, db, threadMine, runID, old)
	e2 := insertEvent(t, db, threadMine, runID, `{"type":"answer_reset"}`)
	e3 := insertEvent(t, db, threadMine, runID, `{"type":"card_pending"}`)
	e4 := insertEvent(t, db, threadMine, runID, `{"type":"message","role":"assistant","content":"New"}`)
	e5 := insertEvent(t, db, threadMine, runID, `{"type":"done","result":{"status":"completed"}}`)
	done := frame(e5, `{"type":"done","result":{"status":"completed"}}`)
	current := frame(e1, `{"role":"assistant","type":"message","content":"Old"}`) + frame(e2, `{"type":"answer_reset"}`) +
		frame(e3, `{"type":"card_pending"}`) + frame(e4, `{"role":"assistant","type":"message","content":"New"}`) + done
	path := "/api/v1/work/threads/" + threadMine + "/runs/" + runID + "/events"
	for _, c := range []struct{ query, want string }{
		{"?events=2", current},
		{"", frame(e1, `{"role":"assistant","type":"message","content":"Old"}`) + done},
		{"?afterId=" + strconv.FormatInt(e2, 10), done},
		{"?afterId=" + strconv.FormatInt(e1, 10), done},
	} {
		if w := call(f, "GET", path+c.query, "", member); w.Code != 200 || w.Body.String() != helloFrames(runID, threadMine)+c.want {
			t.Errorf("stream %q: %d %q", c.query, w.Code, w.Body)
		}
	}

	bundle := func(query string) []json.RawMessage {
		w := call(f, "GET", "/api/v1/work/threads/"+threadMine+query, "", member)
		var v struct {
			EventsByRun map[string][]json.RawMessage `json:"eventsByRun"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("bundle %q: %d %s", query, w.Code, w.Body)
		}
		return v.EventsByRun[runID]
	}
	if got := bundle("?events=2"); len(got) != 5 {
		t.Errorf("bundle for a current tab: %s", got)
	}
	if got := bundle(""); len(got) != 2 || string(got[0]) != `{"role":"assistant","type":"message","content":"Old"}` {
		t.Errorf("bundle for an old tab: %s", got)
	}
}

func TestOldWorkflowRunPagesGetTheFirstAttemptOnly(t *testing.T) {
	f, db := workflowsFront(t)
	member := sessionCookie("member")
	workRun := "70000000-0000-4000-8000-000000000002"
	seed(t, db, `delete from work_run_event where run_id = $1`, workRun)
	for _, p := range []string{`{"type":"message","role":"assistant","content":"Old"}`, `{"type":"answer_reset"}`,
		`{"type":"card_pending"}`, `{"type":"message","role":"assistant","content":"New"}`} {
		seed(t, db, `insert into work_run_event (org_id, thread_id, run_id, kind, payload)
			select org_id, thread_id, id, $2::jsonb->>'type', $2::jsonb from work_run where id = $1`, workRun, p)
	}
	count := func(query string) int {
		var v struct {
			Events []json.RawMessage `json:"events"`
		}
		w := call(f, "GET", "/api/v1/workflow-runs/"+runHeld+query, "", member)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("%q: %d %s", query, w.Code, w.Body)
		}
		return len(v.Events)
	}
	if n := count("?events=2"); n != 4 {
		t.Errorf("current page: %d events", n)
	}
	if n := count(""); n != 1 {
		t.Errorf("old page: %d events", n)
	}
}
