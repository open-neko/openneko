package webfront

import (
	"context"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunTextJoinsAtRunEnd(t *testing.T) {
	f, db := signedInChatFront(t)
	a := f.API
	ctx := context.Background()
	runID := insertRun(t, db, threadMine, "completed")
	ev := func(p string) int64 { return insertEvent(t, db, threadMine, runID, p) }
	m1 := ev(`{"type":"message","role":"assistant","content":"Rev"}`)
	m2 := ev(`{"type":"message","role":"assistant","content":"enue"}`)
	ev(`{"type":"status","message":"Working"}`)
	ev(`{"type":"message","role":"assistant","content":" rose"}`)
	p1 := ev(`{"type":"progress","id":"x","content":"Think"}`)
	p2 := ev(`{"type":"progress","id":"x","content":"ing"}`)
	ev(`{"type":"progress","id":"y","content":"Other"}`)
	ev(`{"type":"message","role":"user","content":"Hi"}`)
	ev(`{"type":"message","role":"assistant","content":"A"}`)
	ev(`{"type":"action_request_result","ok":true}`)
	ev(`{"type":"message","role":"assistant","content":"B"}`)
	ev(`{"type":"message","role":"assistant","content":"C","version":2}`)
	want := "m2:message:Revenue:{" + strconv.FormatInt(m1, 10) + "," + strconv.FormatInt(m2, 10) + "}:{3,7}|" +
		"status::-:-|message: rose:-:-|" +
		"p2:progress:Thinking:{" + strconv.FormatInt(p1, 10) + "," + strconv.FormatInt(p2, 10) + "}:{5,8}|" +
		"progress:Other:-:-|message:Hi:-:-|message:A:-:-|action_request_result::-:-|message:B:-:-|message:C:-:-"
	rows := func() string {
		return rowText(t, db, `select string_agg(
			case id when $2 then 'm2:' when $3 then 'p2:' else '' end ||
			kind || ':' || coalesce(payload->>'content', '') || ':' || coalesce(joined_ids::text, '-') || ':' || coalesce(joined_ends::text, '-'),
			'|' order by id) from work_run_event where run_id = $1`, runID, m2, p2)
	}
	for i := 0; i < 2; i++ {
		if err := a.joinRunText(ctx, "org", runID); err != nil {
			t.Fatal(err)
		}
		if got := rows(); got != want {
			t.Fatalf("join %d:\n got %s\nwant %s", i+1, got, want)
		}
	}
}

func TestRunEventStreamResumesInsideAJoinedRow(t *testing.T) {
	f, db := signedInChatFront(t)
	member := sessionCookie("member")
	runID := insertRun(t, db, threadMine, "completed")
	m1 := insertEvent(t, db, threadMine, runID, `{"type":"message","role":"assistant","content":"Rev"}`)
	m2 := insertEvent(t, db, threadMine, runID, `{"type":"message","role":"assistant","content":"enue "}`)
	m3 := insertEvent(t, db, threadMine, runID, `{"type":"message","role":"assistant","content":"rosé."}`)
	e4 := insertEvent(t, db, threadMine, runID, `{"type":"done","result":{"status":"completed"}}`)
	if err := f.API.joinRunText(context.Background(), "org", runID); err != nil {
		t.Fatal(err)
	}
	text := func(s string) string { return frame(m3, `{"role":"assistant","type":"message","content":"`+s+`"}`) }
	done := frame(e4, `{"type":"done","result":{"status":"completed"}}`)
	path := "/api/v1/work/threads/" + threadMine + "/runs/" + runID + "/events?events=2"
	for _, c := range []struct {
		after int64
		want  string
	}{
		{0, text("Revenue rosé.") + done},
		{m1, text("enue rosé.") + done},
		{m2, text("rosé.") + done},
		{m3, done},
	} {
		q := ""
		if c.after > 0 {
			q = "&afterId=" + strconv.FormatInt(c.after, 10)
		}
		if w := call(f, "GET", path+q, "", member); w.Code != 200 || w.Body.String() != helloFrames(runID, threadMine)+c.want {
			t.Errorf("after %d: %d %q", c.after, w.Code, w.Body)
		}
	}
}

func TestRunEventStreamSkipsTheReadForALiveEvent(t *testing.T) {
	f, db := signedInChatFront(t)
	a := f.API
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldPoll, oldRead := runStreamPoll, runStreamRead
	t.Cleanup(func() { runStreamPoll, runStreamRead = oldPoll, oldRead })
	runStreamPoll = time.Hour
	runID := insertRun(t, db, threadMine, "running")
	var reads atomic.Int32
	runStreamRead = func(id string) {
		if id == runID {
			reads.Add(1)
		}
	}
	_, cancel := context.WithCancelCause(context.Background())
	run := a.runs.register(runID, "org", threadMine, cancel)
	t.Cleanup(func() { a.runs.unregister(runID, run) })
	s := openStream(t, srv, "/api/v1/work/threads/"+threadMine+"/runs/"+runID+"/events?events=2", "member")
	s.next(5 * time.Second)
	s.next(5 * time.Second)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		payload := `{"type":"status","message":"Step ` + strconv.Itoa(i) + `"}`
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := tx.QueryRow(ctx, `insert into work_run_event (org_id, thread_id, run_id, kind, payload)
			values ('org', $1, $2, 'status', $3::jsonb) returning id`, threadMine, runID, payload).Scan(&id); err != nil {
			t.Fatal(err)
		}
		a.notifyRun(runID, jsObjOf("type", "status", "message", "Step "+strconv.Itoa(i)), id)
		if got := s.next(5 * time.Second); got != frame(id, payload) {
			t.Fatalf("live %d: %q", i, got)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := reads.Load(); n != 1 {
		t.Fatalf("the stream read the stored events %d times for events that it had sent", n)
	}
	other := insertEvent(t, db, threadMine, runID, `{"type":"status","message":"Elsewhere"}`)
	if got := s.next(5 * time.Second); got != frame(other, `{"type":"status","message":"Elsewhere"}`) || reads.Load() != 2 {
		t.Fatalf("an event of another process: %q after %d reads", got, reads.Load())
	}
	seed(t, db, `select pg_notify('work_run_event', $1)`, runID)
	deadline := time.Now().Add(5 * time.Second)
	for reads.Load() != 3 {
		if time.Now().After(deadline) {
			t.Fatal("a notice with no event id did not cause a read")
		}
		time.Sleep(5 * time.Millisecond)
	}
	seed(t, db, `update work_run set status = 'failed' where id = $1`, runID)
	end := insertEvent(t, db, threadMine, runID, `{"type":"done","result":{"status":"failed"}}`)
	if got := s.next(5*time.Second) + s.next(5*time.Second); got != frame(end, `{"type":"done","result":{"status":"failed"}}`) {
		t.Fatalf("end %q", got)
	}
}
