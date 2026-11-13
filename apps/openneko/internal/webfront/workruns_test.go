package webfront

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

func TestSuggestThreadTitle(t *testing.T) {
	long := strings.Repeat("a", 76) + "😀" + "tail"
	for in, want := range map[string]string{
		"":                              "New thread",
		" \t\n":                         "New thread",
		"  What is\n\n the   revenue? ": "What is the revenue?",
		strings.Repeat("b", 80):         strings.Repeat("b", 80),
		strings.Repeat("b", 81):         strings.Repeat("b", 77) + "...",
		long:                            strings.Repeat("a", 76) + "\uFFFD...",
	} {
		if got := suggestThreadTitle(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// signedInChatFront is chatTurnFront with sign-in on, so each session acts
// as its own user.
func signedInChatFront(t *testing.T) (*Front, *pgxpool.Pool) {
	t.Helper()
	f, db, actions := chatTurnFront(t)
	signin := &setupWorker{status: `{"provider":{"pluginName":"scalekit"}}`, reply: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/status" {
			signin.ServeHTTP(w, r)
			return
		}
		actions.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.API.Gate.WorkerURL = srv.URL
	return f, db
}

func waitRun(t *testing.T, db *pgxpool.Pool, runID string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status := rowText(t, db, `select status from work_run where id = $1`, runID)
		done := rowText(t, db, `select count(*)::text from work_run_event where run_id = $1 and kind = 'done'`, runID)
		if status != "queued" && status != "running" && done != "0" {
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not end", runID)
	return ""
}

func TestStartRunRoute(t *testing.T) {
	f, db := signedInChatFront(t)
	a := f.API
	member := sessionCookie("member")
	a.turnResolve = fixedBackend("http://unused")
	var got agentCoreInput
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		got = in
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Revenue is up."))
		return axRunResult{Status: "completed", FinalText: "Revenue is up."}, nil
	}
	runs := func() string { return rowText(t, db, `select count(*)::text from work_run where org_id = 'org'`) }
	before := runs()

	for _, c := range []struct{ thread, body, who string }{
		{threadMine, `{"message":"  \n "}`, "member"},
		{threadMine, `{"message":7}`, "member"},
		{threadMine, `not json`, "member"},
	} {
		if w := call(f, "POST", "/api/v1/work/threads/"+c.thread+"/runs", c.body, sessionCookie(c.who)); w.Code != 400 || w.Body.String() != `{"error":"message is required"}` {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	for _, c := range []struct{ thread, who string }{{threadOther, "member"}, {threadMine, "other"}, {unknownUUID, "member"}, {"nope", "member"}} {
		if w := call(f, "POST", "/api/v1/work/threads/"+c.thread+"/runs", `{"message":"Hi"}`, sessionCookie(c.who)); w.Code != 404 || w.Body.String() != `{"error":"Thread not found"}` {
			t.Errorf("%s as %s: %d %s", c.thread, c.who, w.Code, w.Body)
		}
	}
	if runs() != before {
		t.Fatal("a refused request created a run")
	}

	w := call(f, "POST", "/api/v1/work/threads/"+threadMine+"/runs", `{"message":"  What's the   revenue?\n"}`, member)
	body := decode(t, w)
	runID, _ := body["runId"].(string)
	if w.Code != 200 || !isUUID(runID) || body["threadId"] != threadMine || body["backend"] != "ax" || body["actorRole"] != "member" || len(body) != 4 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if status := waitRun(t, db, runID); status != "completed" {
		t.Fatalf("status %s", status)
	}
	if !strings.Contains(got.UserMessage, "What's the   revenue?") || got.RunID != runID {
		t.Fatalf("core input %q %s", got.UserMessage, got.RunID)
	}
	for sql, want := range map[string]string{
		`select backend || ':' || actor_user_id || ':' || actor_role from work_run where id = $1`:                        "ax:member:member",
		`select source || ':' || (workflow_id is null)::text from spend_reservation where work_run_id = $1`:              "chat:true",
		`select string_agg(role || '=' || content, '|' order by created_at) from work_message where run_id = $1`:         "user=What's the   revenue?|assistant=Revenue is up.",
		`select string_agg(event, ',' order by seq) from audit_chain where entity_kind = 'work_run' and entity_id = $1`:  "run:created,run:completed",
		`select payload->>'actorRole' from audit_chain where event = 'run:created' and entity_id = $1`:                   "member",
		`select string_agg(kind, ',' order by id) from work_run_event where run_id = $1 and kind in ('message', 'done')`: "message,done",
	} {
		if g := rowText(t, db, sql, runID); g != want {
			t.Errorf("%s\n got %q want %q", sql, g, want)
		}
	}
	if g := rowText(t, db, `select title from work_thread where id = $1`, threadMine); g != "What's the revenue?" {
		t.Errorf("title %q", g)
	}

	w = call(f, "POST", "/api/v1/work/threads/"+threadMine+"/runs", `{"message":"Second"}`, member)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitRun(t, db, decode(t, w)["runId"].(string))
	if g := rowText(t, db, `select title from work_thread where id = $1`, threadMine); g != "What's the revenue?" {
		t.Errorf("a later question renamed the thread: %q", g)
	}

	seed(t, db, `update spend_limit set org_hourly_micros = 1000000 where org_id = 'org' and workflow_id is null`)
	before = runs()
	w = call(f, "POST", "/api/v1/work/threads/"+threadMine+"/runs", `{"message":"Third"}`, member)
	body = decode(t, w)
	retry, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != 429 || body["code"] != "spend_budget_exhausted" || body["budget"] != "org_hourly" || body["limitUsd"] != 1.0 ||
		body["reservationUsd"] != 5.0 || retry < 1 || body["retryAfterSeconds"] != float64(retry) ||
		!strings.Contains(body["error"].(string), "hourly spending limit of $1.00 for this organization") {
		t.Fatalf("%d %s retry %q", w.Code, w.Body, w.Header().Get("Retry-After"))
	}
	if runs() != before {
		t.Fatal("a refused run left a row")
	}
	if g := rowText(t, db, `select count(*)::text from behavior_alert where org_id = 'org' and kind = 'spend.budget_blocked'`); g != "1" {
		t.Errorf("budget alerts %s", g)
	}
}

func TestHostConfigProvisionedOnce(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, is_default) values ('org', 'graphjin', 'shop', 'http://gj.example/api/v1/graphql', true)`)
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(dir, "graphjin", "client.json")
	ensure := func() {
		t.Helper()
		if err := a.ensureHostConfigProvisioned(ctx, "org"); err != nil {
			t.Fatal(err)
		}
	}
	ensure()
	if _, err := os.Stat(client); err != nil {
		t.Fatalf("first run did not provision: %v", err)
	}
	if err := os.Remove(client); err != nil {
		t.Fatal(err)
	}
	ensure()
	if _, err := os.Stat(client); err == nil {
		t.Fatal("an unchanged config provisioned again")
	}
	seed(t, db, `update data_source set graphql_url = 'http://gj2.example/api/v1/graphql', updated_at = now() where org_id = 'org'`)
	ensure()
	if raw, err := os.ReadFile(client); err != nil || !strings.Contains(string(raw), "http://gj2.example") {
		t.Fatalf("a changed source did not provision: %s %v", raw, err)
	}
}

func insertRun(t *testing.T, db *pgxpool.Pool, threadID, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `insert into work_run (org_id, thread_id, backend, status) values ('org', $1, 'ax', $2) returning id::text`,
		threadID, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertEvent(t *testing.T, db *pgxpool.Pool, threadID, runID, payload string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(context.Background(), `insert into work_run_event (org_id, thread_id, run_id, kind, payload)
		values ('org', $1, $2, $3::jsonb->>'type', $3::jsonb) returning id`, threadID, runID, payload).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func frame(id int64, data string) string {
	return "id: " + strconv.FormatInt(id, 10) + "\ndata: " + data + "\n\n"
}

func helloFrames(runID, threadID string) string {
	return ": hello\n\ndata: {\"type\":\"hello\",\"runId\":\"" + runID + "\",\"threadId\":\"" + threadID + "\",\"backend\":\"ax\"}\n\n"
}

func TestRunEventStreamStored(t *testing.T) {
	f, db := signedInChatFront(t)
	member := sessionCookie("member")
	runID := insertRun(t, db, threadMine, "completed")
	e1 := insertEvent(t, db, threadMine, runID, `{"type":"status","message":"Working"}`)
	e2 := insertEvent(t, db, threadMine, runID, `{"type":"message","role":"assistant","content":"Done.","n":1.0}`)
	e3 := insertEvent(t, db, threadMine, runID, `{"type":"done","result":{"status":"completed"}}`)
	all := frame(e1, `{"type":"status","message":"Working"}`) + frame(e2, `{"n":1,"role":"assistant","type":"message","content":"Done."}`) +
		frame(e3, `{"type":"done","result":{"status":"completed"}}`)
	path := "/api/v1/work/threads/" + threadMine + "/runs/" + runID + "/events"
	for _, c := range []struct {
		query  string
		header map[string]string
		want   string
	}{
		{"", member, all},
		{"?afterId=" + strconv.FormatInt(e1, 10), member, all[len(frame(e1, `{"type":"status","message":"Working"}`)):]},
		{"?afterSeq=" + strconv.FormatInt(e2, 10), member, frame(e3, `{"type":"done","result":{"status":"completed"}}`)},
		{"?afterId=" + strconv.FormatInt(e3, 10), map[string]string{"Cookie": member["Cookie"], "Last-Event-Id": strconv.FormatInt(e1, 10)},
			all[len(frame(e1, `{"type":"status","message":"Working"}`)):]},
		{"?afterId=abc", member, all},
	} {
		w := call(f, "GET", path+c.query, "", c.header)
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" || w.Body.String() != helloFrames(runID, threadMine)+c.want {
			t.Errorf("%s: %d %q", c.query, w.Code, w.Body)
		}
	}
	otherRun := insertRun(t, db, threadOther, "completed")
	for _, c := range []struct{ path, who string }{
		{path, "other"},
		{"/api/v1/work/threads/" + threadOther + "/runs/" + runID + "/events", "other"},
		{"/api/v1/work/threads/" + threadMine + "/runs/" + otherRun + "/events", "member"},
		{"/api/v1/work/threads/" + threadMine + "/runs/nope/events", "member"},
	} {
		if w := call(f, "GET", c.path, "", sessionCookie(c.who)); w.Code != 404 || w.Body.String() != "Not Found" {
			t.Errorf("%s as %s: %d %s", c.path, c.who, w.Code, w.Body)
		}
	}
}

type sseReader struct {
	t    *testing.T
	body *bufio.Reader
	resp *http.Response
}

func openStream(t *testing.T, srv *httptest.Server, path, who string) *sseReader {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Cookie", sessionCookie(who)["Cookie"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return &sseReader{t: t, body: bufio.NewReader(resp.Body), resp: resp}
}

// next reads one frame, or "" at the end of the stream.
func (s *sseReader) next(within time.Duration) string {
	s.t.Helper()
	got := make(chan string, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := s.body.ReadString('\n')
			b.WriteString(line)
			if err != nil || line == "\n" {
				got <- b.String()
				return
			}
		}
	}()
	select {
	case f := <-got:
		return f
	case <-time.After(within):
		s.t.Fatalf("no frame within %s", within)
		return ""
	}
}

func TestRunEventStreamLive(t *testing.T) {
	f, db := signedInChatFront(t)
	a := f.API
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldPoll, oldIdle := runStreamPoll, runEventFlushIdle
	t.Cleanup(func() { runStreamPoll, runEventFlushIdle = oldPoll, oldIdle })
	runStreamPoll, runEventFlushIdle = time.Hour, time.Millisecond

	t.Run("a turn in this process", func(t *testing.T) {
		a.turnResolve = fixedBackend("http://unused")
		step, release := make(chan struct{}), make(chan struct{})
		a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Half"))
			close(step)
			<-release
			return axRunResult{Status: "completed", FinalText: "Half"}, nil
		}
		w := call(f, "POST", "/api/v1/work/threads/"+threadMine+"/runs", `{"message":"Go"}`, sessionCookie("member"))
		runID := decode(t, w)["runId"].(string)
		<-step
		s := openStream(t, srv, "/api/v1/work/threads/"+threadMine+"/runs/"+runID+"/events", "member")
		if h := s.next(5*time.Second) + s.next(5*time.Second); h != helloFrames(runID, threadMine) {
			t.Fatalf("hello %q", h)
		}
		seen := ""
		for !strings.Contains(seen, `"content":"Half"`) {
			seen += s.next(5 * time.Second)
		}
		close(release)
		for {
			frame := s.next(10 * time.Second)
			if frame == "" {
				break
			}
			seen += frame
		}
		if !strings.Contains(seen, `"type":"done","result":{"status":"completed"`) {
			t.Fatalf("frames %q", seen)
		}
		if strings.Count(seen, `"content":"Half"`) != 1 {
			t.Fatalf("an event was sent twice: %q", seen)
		}
	})

	t.Run("another process stores the events", func(t *testing.T) {
		runID := insertRun(t, db, threadMine, "running")
		s := openStream(t, srv, "/api/v1/work/threads/"+threadMine+"/runs/"+runID+"/events", "member")
		s.next(5 * time.Second)
		s.next(5 * time.Second)
		time.Sleep(200 * time.Millisecond)
		id := insertEvent(t, db, threadMine, runID, `{"type":"status","message":"Elsewhere"}`)
		if got := s.next(5 * time.Second); got != frame(id, `{"type":"status","message":"Elsewhere"}`) {
			t.Fatalf("the notice did not wake the stream: %q", got)
		}
		seed(t, db, `update work_run set status = 'failed' where id = $1`, runID)
		done := insertEvent(t, db, threadMine, runID, `{"type":"done","result":{"status":"failed"}}`)
		if got := s.next(5*time.Second) + s.next(5*time.Second); got != frame(done, `{"type":"done","result":{"status":"failed"}}`) {
			t.Fatalf("end %q", got)
		}
	})

	t.Run("a live event before the first read", func(t *testing.T) {
		runID := insertRun(t, db, threadMine, "completed")
		e1 := insertEvent(t, db, threadMine, runID, `{"type":"status","message":"One"}`)
		e2 := insertEvent(t, db, threadMine, runID, `{"type":"status","message":"Two"}`)
		_, cancel := context.WithCancelCause(context.Background())
		run := a.runs.register(runID, "org", threadMine, cancel)
		t.Cleanup(func() { a.runs.unregister(runID, run) })
		old := runStreamSubscribed
		t.Cleanup(func() { runStreamSubscribed = old })
		runStreamSubscribed = func(id string) {
			if id == runID {
				a.notifyRun(runID, jsObjOf("type", "status", "message", "Two"), e2)
			}
		}
		w := call(f, "GET", "/api/v1/work/threads/"+threadMine+"/runs/"+runID+"/events", "", sessionCookie("member"))
		want := helloFrames(runID, threadMine) + frame(e2, `{"type":"status","message":"Two"}`) + frame(e1, `{"type":"status","message":"One"}`)
		if w.Body.String() != want {
			t.Fatalf("%q", w.Body)
		}
	})
}

func TestCancelRunRoute(t *testing.T) {
	f, db := signedInChatFront(t)
	a := f.API
	member := sessionCookie("member")
	cancel := func(runID, who string) (int, string) {
		w := call(f, "POST", "/api/v1/work/runs/"+runID+"/cancel", "", sessionCookie(who))
		return w.Code, w.Body.String()
	}

	stranded := insertRun(t, db, threadMine, "running")
	if code, body := cancel(stranded, "member"); code != 200 || body != `{"ok":true,"recovered":true}` {
		t.Fatalf("%d %s", code, body)
	}
	for sql, want := range map[string]string{
		`select status || ':' || error || ':' || (finished_at is not null)::text from work_run where id = $1`:    "cancelled:" + interruptedRunReason + ":true",
		`select string_agg(kind || '=' || payload::text, ',' order by id) from work_run_event where run_id = $1`: `done={"type": "done", "result": {"status": "cancelled"}}`,
		`select string_agg(event, ',') from audit_chain where entity_id = $1`:                                    "run:cancelled",
	} {
		if g := rowText(t, db, sql, stranded); g != want {
			t.Errorf("%s\n got %q", sql, g)
		}
	}
	if err := a.finishWorkRun(context.Background(), stranded, "completed", nil); err != nil {
		t.Fatal(err)
	}
	if g := rowText(t, db, `select status from work_run where id = $1`, stranded); g != "cancelled" {
		t.Fatalf("a late finish revived the run: %s", g)
	}

	queued := insertRun(t, db, threadMine, "queued")
	if code, body := cancel(queued, "member"); code != 200 || body != `{"ok":true,"recovered":true}` {
		t.Fatalf("queued %d %s", code, body)
	}
	for status, want := range map[string]string{"completed": `{"ok":true,"recovered":false}`, "needs_input": `{"ok":true,"recovered":false}`} {
		id := insertRun(t, db, threadMine, status)
		if code, body := cancel(id, "member"); code != 200 || body != want {
			t.Errorf("%s: %d %s", status, code, body)
		}
		if g := rowText(t, db, `select count(*)::text from work_run_event where run_id = $1`, id); g != "0" {
			t.Errorf("%s: a finished run got events", status)
		}
	}

	a.turnResolve = fixedBackend("http://unused")
	started := make(chan struct{})
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		close(started)
		<-ctx.Done()
		return axRunResult{}, ctx.Err()
	}
	w := call(f, "POST", "/api/v1/work/threads/"+threadMine+"/runs", `{"message":"Long one"}`, member)
	live := decode(t, w)["runId"].(string)
	<-started
	if code, body := cancel(live, "member"); code != 200 || body != `{"ok":true,"recovered":false}` {
		t.Fatalf("live %d %s", code, body)
	}
	if status := waitRun(t, db, live); status != "cancelled" {
		t.Fatalf("live run %s", status)
	}

	for _, c := range []struct{ id, who string }{{stranded, "other"}, {unknownUUID, "member"}, {"nope", "member"}} {
		if code, body := cancel(c.id, c.who); code != 404 || body != `{"error":"Run not found"}` {
			t.Errorf("%s as %s: %d %s", c.id, c.who, code, body)
		}
	}
}

func TestManualRunRoute(t *testing.T) {
	front, db, wf := workflowTurnFrontHTTP(t, `[{"id":"s1","description":"Read stock"}]`)
	a := front.API
	admin := sessionCookie("admin")
	a.turnResolve = fixedBackend("http://unused")
	var got agentCoreInput
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		got = in
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Stock is fine."))
		_ = in.Emit(ctx, jsObjOf("type", "usage", "source", "outer", "usage",
			jsObjOf("totalTokens", 40.0, "billedCostUsd", 0.02, "costStatus", "actual", "coverage", "complete")))
		return axRunResult{Status: "completed", FinalText: "Stock is fine."}, nil
	}
	path := "/api/v1/workflows/" + wf.ID + "/manual-runs"
	summary := func(wfRunID string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			got := rowText(t, db, `select coalesce(telemetry_summary::text, '') from workflow_run where id = $1`, wfRunID)
			if got != "" || time.Now().After(deadline) {
				return got
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	w := call(front, "POST", path, `{"userMessage":"  Check north \n"}`, admin)
	body := decode(t, w)
	runID, _ := body["workRunId"].(string)
	wfRunID, _ := body["workflowRunId"].(string)
	if w.Code != 200 || !isUUID(runID) || !isUUID(wfRunID) || body["threadId"] != threadMine || len(body) != 3 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if status := waitRun(t, db, runID); status != "completed" {
		t.Fatalf("status %s", status)
	}
	if !strings.Contains(got.UserMessage, "Check north") || strings.Contains(got.UserMessage, "Check north \n") {
		t.Errorf("seed message %q", got.UserMessage)
	}
	for sql, want := range map[string]string{
		`select trigger_kind || ':' || trigger_payload::text || ':' || status || ':' || chain_depth from workflow_run where id = $1`:                                 `manual:{"userMessage": "Check north"}:completed:0`,
		`select coalesce(actor_user_id, '-') || ':' || actor_role || ':' || w.thread_id from work_run w join workflow_run r on r.work_run_id = w.id where r.id = $1`: "-:service:" + threadMine,
		`select source || ':' || s.workflow_id from spend_reservation s join workflow_run r on r.work_run_id = s.work_run_id where r.id = $1`:                        "trigger:" + wf.ID,
		`select summary from workflow_run where id = $1`: "Stock is fine.",
	} {
		if g := rowText(t, db, sql, wfRunID); g != want {
			t.Errorf("%s\n got %q want %q", sql, g, want)
		}
	}
	want := `{"runId": "` + runID + `", "usage": {"coverage": "complete", "costStatus": "actual", "totalTokens": 40, "billedCostUsd": 0.02, "missingReasons": []}, "schemaVersion": "openneko.harness-run-summary/v1"}`
	if g := summary(wfRunID); g != want {
		t.Errorf("telemetry summary\n got %s\nwant %s", g, want)
	}
	if g := rowText(t, db, `select (payload->'summary')::text from work_run_event where run_id = $1 and kind = 'telemetry'`, runID); g != want {
		t.Errorf("telemetry event %s", g)
	}

	w = call(front, "POST", path, `{}`, admin)
	wfRunID = decode(t, w)["workflowRunId"].(string)
	waitRun(t, db, decode(t, w)["workRunId"].(string))
	if g := rowText(t, db, `select trigger_payload::text from workflow_run where id = $1`, wfRunID); g != `{"userMessage": null}` {
		t.Errorf("payload without a message %s", g)
	}

	seed(t, db, `update workflow_definition set created_by_thread_id = null where id = $1`, wf.ID)
	w = call(front, "POST", path, `{"userMessage":""}`, admin)
	body = decode(t, w)
	threadID := body["threadId"].(string)
	waitRun(t, db, body["workRunId"].(string))
	if threadID == threadMine || rowText(t, db, `select channel || ':' || title from work_thread where id = $1`, threadID) != "workflow:Low stock" {
		t.Errorf("own thread %s", threadID)
	}
	if g := rowText(t, db, `select trigger_payload::text from workflow_run where id = $1`, body["workflowRunId"]); g != `{"userMessage": ""}` {
		t.Errorf("payload with an empty message %s", g)
	}

	a.turnResolve = func(context.Context, string) (*llm.AxBackendConfig, map[string]string, error) {
		return nil, nil, errors.New("no model key")
	}
	w = call(front, "POST", path, `{}`, admin)
	body = decode(t, w)
	runID, wfRunID = body["workRunId"].(string), body["workflowRunId"].(string)
	if status := waitRun(t, db, runID); status != "failed" {
		t.Fatalf("thrown %s", status)
	}
	if g := rowText(t, db, `select w.error || ':' || r.status || ':' || r.error from work_run w join workflow_run r on r.work_run_id = w.id where w.id = $1`, runID); g != "no model key:failed:no model key" {
		t.Errorf("thrown rows %q", g)
	}
	if !strings.Contains(summary(wfRunID), `"coverage": "unavailable"`) {
		t.Errorf("thrown run summary %s", summary(wfRunID))
	}
	if g := rowText(t, db, `select string_agg(kind, ',' order by id) from work_run_event where run_id = $1`, runID); g != "telemetry,error,done" {
		t.Errorf("thrown events %s", g)
	}
	a.turnResolve = fixedBackend("http://unused")

	seed(t, db, `update spend_limit set run_cap_micros = 1000000 where org_id = 'org' and workflow_id is null`)
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		_ = in.Emit(ctx, jsObjOf("type", "usage", "source", "outer", "usage", jsObjOf("costStatus", "estimated", "estimatedCostUsd", 2.0)))
		<-ctx.Done()
		return axRunResult{}, ctx.Err()
	}
	w = call(front, "POST", path, `{}`, admin)
	body = decode(t, w)
	if status := waitRun(t, db, body["workRunId"].(string)); status != "failed" {
		t.Fatalf("over the cap: %s", status)
	}
	msg := "The run exceeded its $1.00 spend cap ($2.00 spent)."
	if g := rowText(t, db, `select status || ':' || error from workflow_run where id = $1`, body["workflowRunId"]); g != "failed:"+msg {
		t.Errorf("over the cap: %q", g)
	}
	seed(t, db, `update spend_limit set run_cap_micros = 5000000 where org_id = 'org' and workflow_id is null`)

	runs := func() string { return rowText(t, db, `select count(*)::text from workflow_run`) }
	before := runs()
	seed(t, db, `update workflow_definition set enabled = false where id = $1`, wf.ID)
	for _, c := range []struct{ id, want string }{
		{wf.ID, `{"error":"Workflow Low stock is disabled."}`},
		{unknownUUID, `{"error":"Workflow ` + unknownUUID + ` not found for org org."}`},
		{"nope", `{"error":"Workflow nope not found for org org."}`},
	} {
		if w := call(front, "POST", "/api/v1/workflows/"+c.id+"/manual-runs", `{}`, admin); w.Code != 400 || w.Body.String() != c.want {
			t.Errorf("%s: %d %s", c.id, w.Code, w.Body)
		}
	}
	seed(t, db, `update workflow_definition set enabled = true, owner_user_id = 'other' where id = $1`, wf.ID)
	seed(t, db, `update app_user set disabled_at = now() where id = 'other'`)
	if w := call(front, "POST", path, `{}`, admin); w.Code != 400 || w.Body.String() != `{"error":"The workflow owner is no longer active"}` {
		t.Errorf("inactive owner: %d %s", w.Code, w.Body)
	}
	if runs() != before {
		t.Error("a refused manual run left a workflow run")
	}
}
