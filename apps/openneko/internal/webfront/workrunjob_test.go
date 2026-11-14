package webfront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

func TestWorkRunJobChannelTurn(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	a.turnResolve = fixedBackend("http://unused")
	calls := 0
	var plainCards bool
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		calls++
		plainCards = in.CoreCards
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Sales are up."))
		_ = in.Emit(ctx, jsObjOf("type", "vitals", "items", []any{jsObjOf("label", "Revenue", "value", "$18,400", "sub", "+4%")}))
		return axRunResult{Status: "completed", FinalText: "Sales are up."}, nil
	}
	newRun := func() (string, string) {
		t.Helper()
		var runID, jobID string
		if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, status, actor_role) values ('org', $1, 'ax', 'queued', 'member') returning id::text`,
			threadMine).Scan(&runID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger) values ('org', 'work_run', 'queued', 'channel_inbound') returning id::text`).
			Scan(&jobID); err != nil {
			t.Fatal(err)
		}
		return runID, jobID
	}
	runID, jobID := newRun()
	seed(t, db, `insert into skill_usage (org_id, run_id, skill_name, content_hash, origin, source, first_event_id) values ('org', $1, 'refunds', 'h', 'builtin', 'read', 1)`, runID)
	job := jobs.WorkRunArgs{ProcessingJobID: jobID, OrgID: "org", RunID: runID, ThreadID: threadMine, Message: "How are sales?",
		Channel: "telegram", ChannelPlugin: "telegram", Recipient: json.RawMessage(`{"chatId":7}`)}
	if err := a.WorkRun(ctx, job); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !plainCards {
		t.Fatalf("%d core calls, plain cards %v", calls, plainCards)
	}
	if g := rowText(t, db, `select status from work_run where id = $1`, runID); g != "completed" {
		t.Errorf("run %s", g)
	}
	if g := rowText(t, db, `select status || ':' || (started_at is not null) || ':' || (finished_at is not null) from processing_job where id = $1`, jobID); g != "succeeded:true:true" {
		t.Errorf("processing job %s", g)
	}
	if got := riverJobs(t, db, "skill_learn"); len(got) != 1 || got[0].Args != `{"orgId": "org", "skillName": "refunds"}` {
		t.Errorf("skill learn jobs %v", got)
	}
	sends := riverJobs(t, db, "channel_deliver")
	if len(sends) != 1 || sends[0].MaxAttempts != 9 {
		t.Fatalf("deliveries %v", sends)
	}
	var send struct {
		OrgID, ChannelPlugin, Key string
		Recipient                 map[string]any
		Events                    []map[string]any
	}
	if err := json.Unmarshal([]byte(sends[0].Args), &send); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(send.Events)
	if send.OrgID != "org" || send.ChannelPlugin != "telegram" || send.Key != "reply-"+runID || send.Recipient["chatId"] != 7.0 ||
		!strings.Contains(string(event), `"kind":"converse"`) || !strings.Contains(string(event), `"text":"Sales are up."`) ||
		!strings.Contains(string(event), `"component":"MetricCard"`) || !strings.Contains(string(event), `"detail":"+4%"`) {
		t.Errorf("delivery %s", sends[0].Args)
	}

	if err := a.WorkRun(ctx, job); err != nil || calls != 1 || len(riverJobs(t, db, "channel_deliver")) != 1 {
		t.Errorf("a repeated job: %v, %d core calls", err, calls)
	}
	if g := rowText(t, db, `select status from processing_job where id = $1`, jobID); g != "succeeded" {
		t.Errorf("a repeated job changed its finished processing job to %s", g)
	}
	crashedRun, crashedJob := newRun()
	seed(t, db, `update work_run set status = 'running' where id = $1`, crashedRun)
	seed(t, db, `update processing_job set status = 'running' where id = $1`, crashedJob)
	if err := a.WorkRun(ctx, jobs.WorkRunArgs{ProcessingJobID: crashedJob, OrgID: "org", RunID: crashedRun, ThreadID: threadMine}); err != nil || calls != 1 {
		t.Errorf("a run left running: %v, %d core calls", err, calls)
	}
	if g := rowText(t, db, `select status || ': ' || error from processing_job where id = $1`, crashedJob); g != "failed: The run was already running when the job ran again." {
		t.Errorf("the processing job of a run left running: %s", g)
	}
	if err := a.WorkRun(ctx, jobs.WorkRunArgs{OrgID: "org", RunID: "00000000-0000-4000-8000-00000000dead", ThreadID: threadMine}); err != nil || calls != 1 {
		t.Errorf("a missing run: %v, %d core calls", err, calls)
	}

	webRun, webJob := newRun()
	if err := a.WorkRun(ctx, jobs.WorkRunArgs{ProcessingJobID: webJob, OrgID: "org", RunID: webRun, ThreadID: threadMine, Message: "Hi"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || plainCards || len(riverJobs(t, db, "channel_deliver")) != 1 {
		t.Errorf("a run with no channel: %d core calls, plain cards %v, %d deliveries", calls, plainCards, len(riverJobs(t, db, "channel_deliver")))
	}

	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		calls++
		return axRunResult{Status: "failed", Error: "provider down"}, nil
	}
	failedRun, failedJob := newRun()
	job.RunID, job.ProcessingJobID = failedRun, failedJob
	if err := a.WorkRun(ctx, job); err != nil {
		t.Fatal(err)
	}
	if g := rowText(t, db, `select status from work_run where id = $1`, failedRun); g != "failed" || len(riverJobs(t, db, "channel_deliver")) != 1 {
		t.Errorf("a failed run is %s with %d deliveries", g, len(riverJobs(t, db, "channel_deliver")))
	}
}

func TestChatReplyEvent(t *testing.T) {
	surface := []any{jsObjOf("version", "v0.9", "createSurface", jsObjOf("surfaceId", "s1", "catalogId", "x"))}
	text := func(e *jsObj) string {
		if e == nil {
			return "nil"
		}
		return jsStringify(e, false)
	}
	cases := []struct {
		name, body string
		events     []any
		want       string
	}{
		{"surface", "hi", []any{jsObjOf("type", "surface", "messages", surface)},
			`{"kind":"converse","id":"run-1","role":"assistant","text":"hi","enrichment":{"surfaces":[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"x"}}]}}`},
		{"plain text", "just text", []any{jsObjOf("type", "status", "message", "x")},
			`{"kind":"converse","id":"run-1","role":"assistant","text":"just text"}`},
		{"surface only", "", []any{jsObjOf("type", "surface", "messages", surface)},
			`{"kind":"converse","id":"run-1","role":"assistant","text":"","enrichment":{"surfaces":[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"x"}}]}}`},
		{"empty", "   ", nil, "nil"},
		{"vitals", "", []any{jsObjOf("type", "vitals", "items", []any{jsObjOf("label", "Orders", "value", "49,310")})},
			`{"kind":"converse","id":"run-1","role":"assistant","text":"","enrichment":{"surfaces":[` +
				`{"version":"v1.0","createSurface":{"surfaceId":"reply","catalogId":"urn:app:catalog:answer:v1"}},` +
				`{"version":"v1.0","updateComponents":{"surfaceId":"reply","components":[{"id":"root","component":"Answer","children":["vital-0"]},` +
				`{"id":"vital-0","component":"MetricCard","metric":"49,310","label":"Orders"}]}}]}}`},
		{"surface wins over vitals", "ok", []any{jsObjOf("type", "vitals", "items", []any{jsObjOf("label", "Orders", "value", "1")}),
			jsObjOf("type", "surface", "messages", surface)},
			`{"kind":"converse","id":"run-1","role":"assistant","text":"ok","enrichment":{"surfaces":[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"x"}}]}}`},
	}
	for _, c := range cases {
		if got := text(chatReplyEvent("run-1", c.body, c.events)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	body := jsStringify(replySurface("Up 4%.", []any{jsObjOf("label", "Revenue", "value", "$18,400", "sub", "+4%")}), false)
	if !strings.Contains(body, `"children":["answer-body","vital-0"]`) || !strings.Contains(body, `{"id":"answer-body","component":"Markdown","text":"Up 4%."}`) ||
		!strings.Contains(body, `{"id":"vital-0","component":"MetricCard","metric":"$18,400","label":"Revenue","detail":"+4%"}`) {
		t.Errorf("reply surface %s", body)
	}
}

func TestChannelDeliverJob(t *testing.T) {
	var status int
	var reply string
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.EscapedPath()+" "+string(body))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	a := &API{Gate: &Gate{WorkerURL: srv.URL}}
	job := jobs.ChannelDeliverArgs{OrgID: "org", ChannelPlugin: "telegram", Recipient: json.RawMessage(`{"chatId":7}`),
		Events: json.RawMessage(`[{"kind":"converse","text":"hi"}]`), Key: "reply-r1"}
	status, reply = 200, `{"delivered":true,"ref":"299"}`
	if err := a.ChannelDeliver(t.Context(), job); err != nil {
		t.Errorf("delivered: %v", err)
	}
	if !slices.Equal(calls, []string{`POST /admin/channels/telegram/deliver {"events":[{"kind":"converse","text":"hi"}],"recipient":{"chatId":7}}`}) {
		t.Errorf("calls: %v", calls)
	}
	status, reply = 200, `{"delivered":false}`
	if err := a.ChannelDeliver(t.Context(), job); err == nil || err.Error() != "channel-delivery: telegram reported delivered=false" {
		t.Errorf("not delivered: %v", err)
	}
	status, reply = 500, `{"error":"channel telegram is not turned on"}`
	if err := a.ChannelDeliver(t.Context(), job); err == nil || err.Error() != "channel-delivery: telegram failed (500): channel telegram is not turned on" {
		t.Errorf("channel off: %v", err)
	}
	calls = nil
	status, reply = 200, `{"delivered":true}`
	job.ChannelPlugin = "@open-neko/channel-slack"
	if err := a.ChannelDeliver(t.Context(), job); err != nil || len(calls) != 1 || !strings.HasPrefix(calls[0], "POST /admin/channels/@open-neko%2Fchannel-slack/deliver ") {
		t.Errorf("a package name: %v %v", err, calls)
	}
	a.Gate.WorkerURL = "http://127.0.0.1:1"
	if err := a.ChannelDeliver(t.Context(), job); err == nil || !strings.Contains(err.Error(), "the worker did not answer") {
		t.Errorf("worker down: %v", err)
	}
}
