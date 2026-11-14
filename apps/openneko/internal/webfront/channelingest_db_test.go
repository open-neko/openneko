package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const (
	arPending  = "3f1c2b9a-0d4e-4c55-8a71-2b3c4d5e6f70"
	arRejectMe = "3f1c2b9a-0d4e-4c55-8a71-2b3c4d5e6f71"
	arGated    = "3f1c2b9a-0d4e-4c55-8a71-2b3c4d5e6f72"
)

func inboundFront(t *testing.T) (*Front, *pgxpool.Pool) {
	t.Helper()
	f, db := memoryFront(t)
	seedRiver(t, db, f)
	if err := secrets.Write(secrets.Store{
		"channel.telegram": {"TELEGRAM_BOT_TOKEN": "123:abc", "TELEGRAM_WEBHOOK_SECRET": "tg-secret"},
		"channel.slack":    {"SLACK_BOT_TOKEN": "xoxb-1", "SLACK_APP_TOKEN": "xapp-1"},
	}, f.API.SecretsDir); err != nil {
		t.Fatal(err)
	}
	return f, db
}

func count(t *testing.T, db *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func parse(t *testing.T, raw string) any {
	t.Helper()
	v, err := jsParse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestInboundLedger(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	claim := func(key string) string {
		proceed, dead, err := a.beginInbound(ctx, "org", "telegram", key)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(proceed, dead)
	}
	if got := claim("k1"); got != "true false" {
		t.Errorf("first claim %s", got)
	}
	if got := claim("k1"); got != "true false" {
		t.Errorf("a pending update proceeds again: %s", got)
	}
	if err := a.markInboundDone(ctx, "org", "telegram", "k1"); err != nil {
		t.Fatal(err)
	}
	if got := claim("k1"); got != "false false" {
		t.Errorf("a done update: %s", got)
	}
	claim("k2")
	for i := 1; i < inboundMaxAttempts; i++ {
		if dead, n, err := a.recordInboundFailure(ctx, "org", "telegram", "k2", parse(t, `{"u":1}`), "boom"); err != nil || dead || n != i {
			t.Fatalf("failure %d: %v %d %v", i, dead, n, err)
		}
	}
	if dead, n, err := a.recordInboundFailure(ctx, "org", "telegram", "k2", parse(t, `{"u":1}`), "last"); err != nil || !dead || n != inboundMaxAttempts {
		t.Fatalf("the cap: %v %d %v", dead, n, err)
	}
	if got := rowText(t, db, `select status || ' ' || last_error || ' ' || payload::text from inbound_dedup where update_key = 'k2'`); got != `dead last {"u": 1}` {
		t.Errorf("dead row %q", got)
	}
	if got := claim("k2"); got != "false true" {
		t.Errorf("a dead update: %s", got)
	}
	seed(t, db, `update inbound_dedup set created_at = now() - interval '8 days'`)
	claim("k3")
	if n, err := a.pruneInboundDedup(ctx, time.Now()); err != nil || n != 1 {
		t.Errorf("prune: %d %v", n, err)
	}
	if got := count(t, db, `select count(*) from inbound_dedup`); got != 2 {
		t.Errorf("rows after prune %d", got)
	}
	if c, err := a.loadPollCursor(ctx, "org", "telegram"); err != nil || c != "" {
		t.Errorf("no cursor: %q %v", c, err)
	}
	for _, c := range []string{"11", "12"} {
		if err := a.savePollCursor(ctx, "org", "telegram", c); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := a.loadPollCursor(ctx, "org", "telegram"); c != "12" {
		t.Errorf("cursor %q", c)
	}
}

func TestChannelActor(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	resolve := func(sender string) string {
		var s *jsObj
		if sender != "" {
			s = parse(t, sender).(*jsObj)
		}
		who, err := a.resolveChannelActor(ctx, "org", "telegram", s)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s %s %v", who.UserID, who.role(), who.Blocked)
	}
	if got := resolve(""); got != " member false" || count(t, db, `select count(*) from channel_identity`) != 0 {
		t.Errorf("no sender: %s", got)
	}
	if got := resolve(`{"id":"99","displayName":"Ada"}`); got != " member false" {
		t.Errorf("unknown sender: %s", got)
	}
	if got := rowText(t, db, `select status || ' ' || display_name || ' ' || workspace_id || '.' from channel_identity where channel_user_id = '99'`); got != "unverified Ada ." {
		t.Errorf("identity row %q", got)
	}
	resolve(`{"id":"99","displayName":"Ada L"}`)
	if got := rowText(t, db, `select display_name from channel_identity where channel_user_id = '99'`); got != "Ada L" {
		t.Errorf("name not refreshed: %q", got)
	}
	if got := resolve(`{"id":"98","email":"M@Example.com"}`); got != "member member false" {
		t.Errorf("email match: %s", got)
	}
	if got := rowText(t, db, `select status || ' ' || app_user_id from channel_identity where channel_user_id = '98'`); got != "linked member" {
		t.Errorf("auto-linked row %q", got)
	}
	seed(t, db, `insert into channel_identity (org_id, channel_plugin, workspace_id, channel_user_id, status, app_user_id)
		values ('org', 'telegram', '', 'boss', 'linked', 'admin'), ('org', 'telegram', '', 'gone', 'linked', 'other'),
		       ('org', 'telegram', '', 'bad', 'blocked', null)`)
	if got := resolve(`{"id":"boss"}`); got != "admin admin false" {
		t.Errorf("linked admin: %s", got)
	}
	seed(t, db, `update app_user set disabled_at = now() where id = 'other'`)
	if got := resolve(`{"id":"gone"}`); got != " member false" {
		t.Errorf("disabled linked user: %s", got)
	}
	if got := resolve(`{"id":"97","email":"o@example.com"}`); got != " member false" {
		t.Errorf("a disabled user was linked by email: %s", got)
	}
	if got := resolve(`{"id":"bad"}`); got != " member true" {
		t.Errorf("blocked: %s", got)
	}
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type in ('channel', 'pack')`)
	if got := resolve(`{"id":"98"}`); got != " member true" {
		t.Errorf("a linked member without the channel: %s", got)
	}
	if got := resolve(`{"id":"boss"}`); got != "admin admin false" {
		t.Errorf("an admin holds every channel: %s", got)
	}
}

func TestInboundOrg(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	seed(t, db, `insert into organization (id, name) values ('org2', 'Two')`)
	if org, err := a.resolveInboundOrg(ctx, "slack", "", "org"); err != nil || org != "org" || count(t, db, `select count(*) from channel_workspace`) != 0 {
		t.Errorf("no workspace: %s %v", org, err)
	}
	if org, err := a.resolveInboundOrg(ctx, "slack", "T1", "org"); err != nil || org != "org" ||
		rowText(t, db, `select org_id from channel_workspace where workspace_id = 'T1'`) != "org" {
		t.Errorf("first contact: %s %v", org, err)
	}
	seed(t, db, `insert into channel_workspace (org_id, channel_plugin, workspace_id) values ('org2', 'slack', 'T2')`)
	if org, err := a.resolveInboundOrg(ctx, "slack", "T2", "org"); err != nil || org != "org2" {
		t.Errorf("mapped workspace: %s %v", org, err)
	}
}

func TestChannelDispatch(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values
		($1, 'org', 'internal', 'note', 'pending_approval', '{}'), ($2, 'org', 'internal', 'note', 'pending_approval', '{}')`, arPending, arRejectMe)
	tg := func(update string) bool { return a.processInbound(ctx, "org", "telegram", parse(t, update)) }
	seed(t, db, `insert into app_passkey (org_id, user_id, credential_id, public_key, name)
		select org_id, id, 'cred-' || id, 'key', 'Phone' from app_user where org_id = 'org'`)

	if !tg(`{"update_id":1,"callback_query":{"data":"approve:` + arPending + `","from":{"id":98},"message":{"chat":{"id":4242}}}}`) {
		t.Fatal("approve held")
	}
	if got := rowText(t, db, `select status from action_request where id = $1`, arPending); got != "approved" {
		t.Errorf("approve: %s", got)
	}
	if got := lastAudit(t, db, arPending); !strings.Contains(got, `"method": "telegram"`) {
		t.Errorf("telegram approval audit: %s", got)
	}
	if j := riverJobs(t, db, jobs.KindActionExecute); len(j) != 1 || !strings.Contains(j[0].Args, arPending) {
		t.Errorf("execute job %+v", j)
	}
	if got := rowText(t, db, `select recipient::text from delivery_binding where channel_plugin = 'telegram'`); got != `{"kind": "telegram", "chatId": 4242}` {
		t.Errorf("binding %q", got)
	}
	tg(`{"update_id":2,"callback_query":{"data":"approve:` + arPending + `","from":{"id":98},"message":{"chat":{"id":4242}}}}`)
	if j := riverJobs(t, db, jobs.KindActionExecute); len(j) != 1 {
		t.Errorf("a repeated approve queued again: %+v", j)
	}
	tg(`{"update_id":3,"callback_query":{"data":"reject:` + arRejectMe + `","from":{"id":98},"message":{"chat":{"id":5}}}}`)
	if got := rowText(t, db, `select status from action_request where id = $1`, arRejectMe); got != "rejected" {
		t.Errorf("reject: %s", got)
	}
	if got := count(t, db, `select count(*) from delivery_binding where channel_plugin = 'telegram'`); got != 1 {
		t.Errorf("a second sender bound again: %d", got)
	}
	if !tg(`{"update_id":4,"callback_query":{"data":"approve:not-a-request","from":{"id":98}}}`) ||
		!tg(`{"update_id":5,"callback_query":{"data":"approve:3f1c2b9a-0d4e-4c55-8a71-000000000000","from":{"id":98}}}`) {
		t.Error("an unknown request held the cursor")
	}
	const group = "90000000-0000-4000-8000-000000000002"
	seed(t, db, `insert into user_group (id, org_id, slug, name) values ($1, 'org', 'buyers', 'Buyers')`, group)
	seed(t, db, `insert into action_policy (id, org_id, name, applies_to_kinds, applies_to_scopes, mode, priority, enabled, approver_group_id)
		values ('90000000-0000-4000-8000-000000000003', 'org', 'gated', '{note}', '{}', 'approval_required', 1, true, $1)`, group)
	seed(t, db, `insert into action_request (id, org_id, policy_id, scope, kind, status, payload)
		values ($1, 'org', '90000000-0000-4000-8000-000000000003', 'internal', 'note', 'pending_approval', '{}')`, arGated)
	if !tg(`{"update_id":6,"callback_query":{"data":"approve:` + arGated + `","from":{"id":98}}}`) {
		t.Error("a refused approval held the cursor")
	}
	if got := rowText(t, db, `select status from action_request where id = $1`, arGated); got != "pending_approval" {
		t.Errorf("an anonymous sender approved a gated request: %s", got)
	}

	if !tg(`{"update_id":7,"message":{"chat":{"id":4242},"from":{"id":99,"first_name":"Ada"},"text":"What sold today?"}}`) {
		t.Fatal("utterance held")
	}
	thread := channelThreadID("org", "telegram", "4242:4242")
	if got := rowText(t, db, `select title || ' ' || channel from work_thread where id = $1`, thread); got != "Telegram 4242 telegram" {
		t.Errorf("thread %q", got)
	}
	if got := rowText(t, db, `select string_agg(role || ':' || content, ',') from work_message where thread_id = $1`, thread); got != "user:What sold today?" {
		t.Errorf("messages %q", got)
	}
	if got := rowText(t, db, `select trigger || ' ' || trigger_payload::text from processing_job where kind = 'work_run'`); got != `channel_inbound {"sender": {"id": "99", "displayName": "Ada"}, "message": "What sold today?"}` {
		t.Errorf("processing job %q", got)
	}
	runs := riverQueueJobs(t, db, jobs.KindWorkRun, jobs.QueueWorkRun)
	if len(runs) != 1 {
		t.Fatalf("work_run jobs %+v", runs)
	}
	var args jobs.WorkRunArgs
	_ = json.Unmarshal([]byte(runs[0].Args), &args)
	if args.ThreadID != thread || args.Channel != "telegram" || args.ChannelPlugin != "telegram" || args.Message != "What sold today?" ||
		string(args.Recipient) != `{"kind": "telegram", "chatId": 4242}` || args.QueuedAt == nil || args.ProcessingJobID == "" ||
		rowText(t, db, `select status || ' ' || coalesce(actor_role, '') from work_run where id = $1`, args.RunID) != "queued member" {
		t.Errorf("work_run args %s", runs[0].Args)
	}
	tg(`{"update_id":8,"message":{"chat":{"id":4242},"from":{"id":99},"text":"/report weekly"}}`)
	tg(`{"update_id":9,"callback_query":{"data":"select:pick:opt-2","from":{"id":99},"message":{"chat":{"id":4242}}}}`)
	if got := rowText(t, db, `select string_agg(content, '|' order by created_at) from work_message where role = 'user'`); got != "What sold today?|/report weekly|opt-2" {
		t.Errorf("invoke and select %q", got)
	}

	slack := func(payload string) bool { return a.processInbound(ctx, "org", "slack", parse(t, payload)) }
	before := count(t, db, `select count(*) from work_run`)
	slack(`{"type":"event_callback","event":{"type":"message","channel_type":"channel","text":"more","user":"U6","channel":"C5","thread_ts":"1700.6"}}`)
	if count(t, db, `select count(*) from work_run`) != before {
		t.Error("a thread reply with no thread started a run")
	}
	seed(t, db, `insert into work_thread (id, org_id, channel) values ($1, 'org', 'slack')`, channelThreadID("org", "slack", "C5:1700.6"))
	slack(`{"type":"event_callback","event":{"type":"message","channel_type":"channel","text":"more again","user":"U6","channel":"C5","thread_ts":"1700.6"}}`)
	if count(t, db, `select count(*) from work_run`) != before+1 {
		t.Error("a reply in a known thread started no run")
	}

	seed(t, db, `update spend_limit set org_hourly_micros = 1000000 where org_id = 'org' and workflow_id is null`)
	before = count(t, db, `select count(*) from work_run`)
	if !tg(`{"update_id":10,"message":{"chat":{"id":4242},"from":{"id":99},"text":"Over budget?"}}`) {
		t.Error("a full budget held the cursor")
	}
	replies := riverJobs(t, db, jobs.KindChannelDeliver)
	if count(t, db, `select count(*) from work_run`) != before || len(replies) != 1 ||
		!strings.Contains(replies[0].Args, `"kind": "converse"`) || !strings.Contains(replies[0].Args, "hourly spending limit") ||
		!strings.Contains(replies[0].Args, `"key": "reply-spend-`+thread) {
		t.Errorf("budget reply %+v", replies)
	}
}

func TestInboundFailureHoldsThenDeadLetters(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	jobsOn := a.Jobs
	a.Jobs = nil
	update := parse(t, `{"update_id":1,"message":{"chat":{"id":7},"text":"hi"}}`)
	if a.processInbound(ctx, "org", "telegram", update) {
		t.Fatal("a failed dispatch did not hold")
	}
	if got := rowText(t, db, `select status || ' ' || attempts || ' ' || last_error from inbound_dedup`); got != "pending 1 the job queue is not ready" {
		t.Errorf("ledger %q", got)
	}
	seed(t, db, `update inbound_dedup set attempts = $1`, inboundMaxAttempts-1)
	if !a.processInbound(ctx, "org", "telegram", update) {
		t.Error("the last attempt did not settle")
	}
	a.Jobs = jobsOn
	if !a.processInbound(ctx, "org", "telegram", update) || count(t, db, `select count(*) from work_message where content = 'hi'`) != 2 {
		t.Error("a dead update ran again, or the failed runs left no message")
	}
}

func TestChannelWebhook(t *testing.T) {
	f, db := inboundFront(t)
	post := func(path, body string, headers map[string]string) string {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		return fmt.Sprintf("%d %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
	good := map[string]string{"X-Telegram-Bot-Api-Secret-Token": "tg-secret"}
	update := `{"update_id":5,"message":{"chat":{"id":7},"from":{"id":1},"text":"hello"}}`
	runs := func() int { return count(t, db, `select count(*) from work_run`) }
	before := runs()
	if got := post("/api/v1/channels/telegram/inbound", update, good); got != `200 {"dispatched":1,"ok":true}` {
		t.Errorf("signed: %s", got)
	}
	if got := post("/api/v1/channels/telegram/inbound", update, good); got != `200 {"dispatched":1,"ok":true}` ||
		runs() != before+1 {
		t.Errorf("a repeated update: %s", got)
	}
	if got := post("/api/v1/channels/"+url.PathEscape("@open-neko/channel-telegram")+"/inbound", `{"update_id":6,"message":{"chat":{"id":7},"text":"again"}}`, good); got != `200 {"dispatched":1,"ok":true}` {
		t.Errorf("a package name: %s", got)
	}
	for name, c := range map[string]struct{ path, body string }{
		"wrong secret":  {"/api/v1/channels/telegram/inbound", update},
		"unknown":       {"/api/v1/channels/fax/inbound", update},
		"not JSON":      {"/api/v1/channels/telegram/inbound", "{"},
		"a channel off": {"/api/v1/channels/hud/inbound", `{"kind":"utterance","text":"hi"}`},
	} {
		h := good
		if name == "wrong secret" {
			h = map[string]string{"X-Telegram-Bot-Api-Secret-Token": "nope"}
		}
		if got := post(c.path, c.body, h); got != `200 {"dispatched":0,"ok":false}` {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := post("/api/v1/channels/telegram/inbound", strings.Repeat(" ", maxInboundBody+1), good); got != `400 {"error":"could not read request body"}` {
		t.Errorf("too large: %s", got)
	}
	if runs() != before+2 {
		t.Error("a refused webhook started a run")
	}
}

func TestTelegramPoll(t *testing.T) {
	srv := newFakeChannelServer(t)
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	batch := `{"ok":true,"result":[{"update_id":41,"message":{"chat":{"id":7},"text":"one"}},{"update_id":42,"message":{"chat":{"id":7},"text":"two"}}]}`
	srv.reply = func(string, int) string { return batch }
	cursor := ""
	if held, err := a.pollOnce(ctx, "org", "telegram", &cursor); err != nil || held || cursor != "43" {
		t.Fatalf("poll: %v %v %q", held, err, cursor)
	}
	if srv.requests[0].Path != "/bot123:abc/getUpdates" || srv.requests[0].Body != `{"timeout":0}` {
		t.Errorf("request %+v", srv.requests[0])
	}
	if c, _ := a.loadPollCursor(ctx, "org", "telegram"); c != "43" || count(t, db, `select count(*) from work_message where content in ('one', 'two')`) != 2 {
		t.Errorf("saved cursor %q", c)
	}
	srv.reply = func(string, int) string {
		return `{"ok":true,"result":[{"update_id":43,"message":{"chat":{"id":7},"text":"three"}}]}`
	}
	jobsOn := a.Jobs
	a.Jobs = nil
	if held, err := a.pollOnce(ctx, "org", "telegram", &cursor); err != nil || !held || cursor != "43" {
		t.Errorf("a failed update: %v %v %q", held, err, cursor)
	}
	if srv.requests[1].Body != `{"timeout":0,"offset":43}` {
		t.Errorf("offset %s", srv.requests[1].Body)
	}
	a.Jobs = jobsOn
	if held, err := a.pollOnce(ctx, "org", "telegram", &cursor); err != nil || held || cursor != "44" {
		t.Errorf("the retry: %v %v %q", held, err, cursor)
	}
	srv.status, srv.reply = 401, func(string, int) string { return `{"ok":false,"description":"Unauthorized"}` }
	if _, err := a.pollOnce(ctx, "org", "telegram", &cursor); err == nil || err.Error() != "Telegram getUpdates returned ok=false (Unauthorized)" {
		t.Errorf("refused: %v", err)
	}
	telegramAPIBase = "http://127.0.0.1:1"
	if _, err := a.pollOnce(ctx, "org", "telegram", &cursor); err == nil || strings.Contains(err.Error(), "123:abc") {
		t.Errorf("down: %v", err)
	}
}

type fakeSlackSocket struct {
	mu     sync.Mutex
	opens  int
	acks   []string
	frames [][]string
}

func newFakeSlackSocket(t *testing.T, frames ...[]string) *fakeSlackSocket {
	s := &fakeSlackSocket{frames: frames}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apps.connections.open" {
			s.mu.Lock()
			s.opens++
			n := s.opens
			s.mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer xapp-1" {
				_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":true,"url":"ws%s/socket/%d"}`, strings.TrimPrefix(srv.URL, "http"), n)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow() //nolint:errcheck
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/socket/%d", &n)
		if n > len(s.frames) {
			<-r.Context().Done()
			return
		}
		for _, frame := range s.frames[n-1] {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
				return
			}
			if strings.Contains(frame, "envelope_id") {
				_, ack, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				s.mu.Lock()
				s.acks = append(s.acks, string(ack))
				s.mu.Unlock()
			}
		}
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(srv.Close)
	old := slackAPIBase
	slackAPIBase = srv.URL
	t.Cleanup(func() { slackAPIBase = old })
	return s
}

func TestSlackSocketMode(t *testing.T) {
	f, db := inboundFront(t)
	a := f.API
	dm := `{"type":"event_callback","team_id":"T1","event":{"type":"message","channel_type":"im","text":"hello","user":"U5","channel":"D1"}}`
	sock := newFakeSlackSocket(t,
		[]string{`{"type":"hello"}`, `not json`, `{"type":"events_api","envelope_id":"e1","payload":` + dm + `}`,
			`{"type":"events_api","envelope_id":"e2"}`, `{"type":"disconnect"}`},
		[]string{`{"type":"slash_commands","envelope_id":"e3","payload":{"command":"/openneko","text":"status","channel_id":"C1","user_id":"U1","team_id":"T1"}}`},
	)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { a.slackSocketLoop(ctx, "org", "slack"); close(done) }()
	deadline := time.Now().Add(20 * time.Second)
	for count(t, db, `select count(*) from work_message where role = 'user'`) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	sock.mu.Lock()
	opens, acks := sock.opens, strings.Join(sock.acks, ",")
	sock.mu.Unlock()
	if opens < 2 || acks != `{"envelope_id":"e1"},{"envelope_id":"e2"},{"envelope_id":"e3"}` {
		t.Errorf("opens %d acks %v", opens, acks)
	}
	if got := rowText(t, db, `select string_agg(content, '|' order by created_at) from work_message where role = 'user'`); got != "hello|/status" {
		t.Errorf("messages %q", got)
	}
	if got := rowText(t, db, `select org_id from channel_workspace where workspace_id = 'T1'`); got != "org" {
		t.Errorf("workspace %q", got)
	}

	if err := secrets.Write(secrets.Store{"channel.slack": {"SLACK_BOT_TOKEN": "xoxb-1"}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { a.slackSocketLoop(t.Context(), "org", "slack"); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Error("the loop ran with no app token")
	}
	sock.mu.Lock()
	defer sock.mu.Unlock()
	if sock.opens != opens {
		t.Error("connected with no app token")
	}
}

func TestInboundLoopsFollowSettings(t *testing.T) {
	srv := newFakeChannelServer(t)
	srv.reply = func(string, int) string { return `{"ok":true,"result":[]}` }
	f, _ := inboundFront(t)
	a := f.API
	a.PublicURL = ""
	t.Setenv("OPENNEKO_PUBLIC_URL", "")
	if err := secrets.Write(secrets.Store{"channel.telegram": {"TELEGRAM_BOT_TOKEN": "123:abc"}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := &channelLoops{ctx: ctx, running: map[string]*inboundLoop{}, noted: map[string]bool{}, wake: make(chan struct{}, 1)}
	running := func() string {
		l.mu.Lock()
		defer l.mu.Unlock()
		var ids []string
		for _, ch := range builtInChannels {
			if l.running[ch.ID] != nil {
				ids = append(ids, ch.ID)
			}
		}
		return strings.Join(ids, ",")
	}
	a.refreshInbound(l)
	if got := running(); got != "telegram" {
		t.Errorf("on: %q", got)
	}
	first := l.running["telegram"]
	a.refreshInbound(l)
	if l.running["telegram"] != first {
		t.Error("an unchanged channel restarted")
	}
	if err := a.setChannelSettings("telegram", map[string]any{"TELEGRAM_WEBHOOK_SECRET": "s"}); err != nil {
		t.Fatal(err)
	}
	a.refreshInbound(l)
	if l.running["telegram"] == first || running() != "telegram" {
		t.Error("a changed setting did not restart the loop")
	}
	if err := a.setChannelState("hud", "on"); err != nil {
		t.Fatal(err)
	}
	a.refreshInbound(l)
	if got := running(); got != "telegram" || !l.noted["hud"] {
		t.Errorf("the HUD takes webhooks only: %q", got)
	}
	a.PublicURL = "https://neko.example"
	if err := a.setChannelState("telegram", "off"); err != nil {
		t.Fatal(err)
	}
	if err := a.setChannelSettings("telegram", map[string]any{"TELEGRAM_BOT_TOKEN": "9:z"}); err != nil {
		t.Fatal(err)
	}
	a.refreshInbound(l)
	if got := running(); got != "" {
		t.Errorf("a public URL still polls: %q", got)
	}
}

func TestChannelSettingsRoutes(t *testing.T) {
	f, _ := inboundFront(t)
	a := f.API
	admin := sessionCookie("admin")
	if err := secrets.Write(secrets.Store{"channel.hud": {"HUD_CHANNEL_WORKSPACE": "plant"}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	w := call(f, "GET", "/api/v1/admin/channels/settings", "", admin)
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(body, `{"channels":[{"id":"slack","providerLabel":"Slack","directions":["outbound","inbound"],"ingress":"socket","enabled":false,"fields":[{"key":"SLACK_BOT_TOKEN","required":true,"secret":true,"description":"xoxb-`) ||
		!strings.Contains(body, `"missing":["SLACK_BOT_TOKEN"]`) ||
		!strings.Contains(body, `{"key":"HUD_CHANNEL_WORKSPACE","required":false,"secret":false,"description":"Stable identifier that scopes inbound senders and recipients, for example the HUD's pack id. Default: demo.","set":true,"value":"plant"}],"missing":[]}`) ||
		strings.Contains(body, "HUD_CHANNEL_SECRET") {
		t.Fatalf("get: %d %s", w.Code, body)
	}
	post := func(b string) string {
		w := call(f, "POST", "/api/v1/admin/channels/settings", b, admin)
		return fmt.Sprintf("%d %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
	for b, want := range map[string]string{
		`{"channel":"slack","values":{"SLACK_BOT_TOKEN":"xoxb-9","SLACK_APP_TOKEN":"xapp-9"}}`: `200 {"ok":true}`,
		`{"channel":"Slack","values":{"A":"b"}}`:                                               `400 {"error":"channel must be a channel id"}`,
		`{"channel":"slack","values":{}}`:                                                      `400 {"error":"values must map at least one setting to a string or null"}`,
		`{"channel":"slack","values":{"SLACK_BOT_TOKEN":""}}`:                                  `400 {"error":"each value must be null or a string of 1 to 8192 characters"}`,
		`{"channel":"slack","values":{"SLACK_BOT_TOKEN":"x"},"state":"on"}`:                    `400 {"error":"send values or state, not both"}`,
		`{"channel":"slack","state":"maybe"}`:                                                  `400 {"error":"state must be on or off"}`,
		`{"channel":"slack","values":{"OTHER":"x"}}`:                                           `400 {"error":"slack does not declare OTHER"}`,
		`{"channel":"hud","values":{"HUD_CHANNEL_SECRET":"x"}}`:                                `400 {"error":"hud does not declare HUD_CHANNEL_SECRET"}`,
		`{"channel":"fax","state":"on"}`:                                                       `400 {"error":"unknown channel fax"}`,
		`{"channel":"telegram","state":"on"}`:                                                  `400 {"error":"telegram needs TELEGRAM_BOT_TOKEN before it can turn on"}`,
	} {
		if got := post(b); got != want {
			t.Errorf("%s: %s, want %s", b, got, want)
		}
	}
	store, _ := secrets.Read(a.SecretsDir)
	if store["channel.slack"]["SLACK_APP_TOKEN"] != "xapp-9" {
		t.Errorf("slack section %v", store["channel.slack"])
	}
	if got := post(`{"channel":"hud","state":"on"}`); got != `200 {"ok":true}` {
		t.Fatal(got)
	}
	store, _ = secrets.Read(a.SecretsDir)
	minted := store["channel.hud"]["HUD_CHANNEL_SECRET"]
	if len(minted) != 64 || store["channel.hud"]["HUD_CHANNEL_WORKSPACE"] != "plant" {
		t.Errorf("hud on %v", store["channel.hud"])
	}
	post(`{"channel":"hud","state":"on"}`)
	if store, _ = secrets.Read(a.SecretsDir); store["channel.hud"]["HUD_CHANNEL_SECRET"] != minted {
		t.Error("a second on minted again")
	}
	if got := post(`{"channel":"slack","values":{"SLACK_BOT_TOKEN":null}}`); got != `200 {"ok":true}` {
		t.Fatal(got)
	}
	if got := post(`{"channel":"hud","state":"off"}`); got != `200 {"ok":true}` {
		t.Fatal(got)
	}
	store, _ = secrets.Read(a.SecretsDir)
	if _, ok := store["channel.hud"]; ok || store["channel.slack"]["SLACK_BOT_TOKEN"] != "" || store["channel.slack"]["SLACK_APP_TOKEN"] != "xapp-9" {
		t.Errorf("after off and clear: %v", store)
	}
	if w := call(f, "GET", "/api/v1/admin/channels/settings", "", sessionCookie("member")); w.Code != 403 {
		t.Errorf("member: %d", w.Code)
	}
}

func TestMigrateLegacyChannelSecrets(t *testing.T) {
	a := &API{SecretsDir: t.TempDir()}
	if moved, err := a.MigrateLegacyChannelSecrets(); err != nil || moved != nil {
		t.Errorf("nothing to move: %v %v", moved, err)
	}
	if err := secrets.Write(secrets.Store{
		"@open-neko/plugin-slack":     {"SLACK_BOT_TOKEN": "old", "SLACK_APP_TOKEN": "xapp-old", "UNRELATED": "x"},
		"@open-neko/channel-telegram": {"TELEGRAM_BOT_TOKEN": "1:a"},
		"channel.slack":               {"SLACK_BOT_TOKEN": "new"},
		"other":                       {"K": "v"},
	}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	moved, err := a.MigrateLegacyChannelSecrets()
	if err != nil || strings.Join(moved, ",") != "@open-neko/channel-telegram,@open-neko/plugin-slack" {
		t.Fatalf("moved %v %v", moved, err)
	}
	store, _ := secrets.Read(a.SecretsDir)
	if len(store) != 3 || store["channel.slack"]["SLACK_BOT_TOKEN"] != "new" || store["channel.slack"]["SLACK_APP_TOKEN"] != "xapp-old" ||
		store["channel.slack"]["UNRELATED"] != "" || store["channel.telegram"]["TELEGRAM_BOT_TOKEN"] != "1:a" || store["other"]["K"] != "v" {
		t.Errorf("store %v", store)
	}
}

func TestInboundLoopsCreateNoOrg(t *testing.T) {
	db := testpg.Migrated(t)
	a := &API{DB: db, SecretsDir: t.TempDir()}
	if err := secrets.Write(secrets.Store{"channel.telegram": {"TELEGRAM_BOT_TOKEN": "123:abc"}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	l := &channelLoops{ctx: t.Context(), running: map[string]*inboundLoop{}, noted: map[string]bool{}, wake: make(chan struct{}, 1)}
	a.refreshInbound(l)
	if n := count(t, db, `select count(*) from organization`); n != 0 || len(l.running) != 0 {
		t.Errorf("an empty install: %d orgs, %d loops", n, len(l.running))
	}
	seed(t, db, `insert into organization (id, name) values ('org', 'Org')`)
	a.refreshInbound(l)
	if len(l.running) != 1 {
		t.Errorf("loops after the org exists: %d", len(l.running))
	}
	l.running["telegram"].cancel()
}
