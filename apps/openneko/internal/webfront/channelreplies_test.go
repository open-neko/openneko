package webfront

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

const plantedAnswer = "Revenue was 4,210 for SKU-PLANTED-77"

func inboundRunArgs(t *testing.T, f *Front, db *pgxpool.Pool, channel, update string) jobs.WorkRunArgs {
	t.Helper()
	before := len(riverQueueJobs(t, db, jobs.KindWorkRun, jobs.QueueWorkRun))
	if !f.API.processInbound(t.Context(), "org", channel, parse(t, update)) {
		t.Fatalf("%s update held", channel)
	}
	runs := riverQueueJobs(t, db, jobs.KindWorkRun, jobs.QueueWorkRun)
	if len(runs) != before+1 {
		t.Fatalf("%s update started %d runs", channel, len(runs)-before)
	}
	var args jobs.WorkRunArgs
	if err := json.Unmarshal([]byte(runs[len(runs)-1].Args), &args); err != nil {
		t.Fatal(err)
	}
	return args
}

// replyRequests queues the answer the way a finished run does and sends the
// queued job to the fake chat service.
func replyRequests(t *testing.T, f *Front, db *pgxpool.Pool, srv *fakeChannelServer, run jobs.WorkRunArgs) ([]channelRequest, error) {
	t.Helper()
	run.RunID = uuid.NewString()
	if err := f.API.queueChatReply(t.Context(), run, plantedAnswer); err != nil {
		t.Fatal(err)
	}
	queued := riverJobs(t, db, jobs.KindChannelDeliver)
	var job jobs.ChannelDeliverArgs
	if err := json.Unmarshal([]byte(queued[len(queued)-1].Args), &job); err != nil {
		t.Fatal(err)
	}
	srv.requests = nil
	err := f.API.ChannelDeliver(t.Context(), job)
	return srv.requests, err
}

func putReplies(t *testing.T, f *Front, body string) {
	t.Helper()
	if w := call(f, "PUT", "/api/v1/admin/channels/replies", body, sessionCookie("admin")); w.Code != 200 {
		t.Fatalf("%s: %d %s", body, w.Code, w.Body)
	}
}

func describe(reqs []channelRequest) string {
	var out []string
	for _, r := range reqs {
		out = append(out, r.Path+" "+r.Body)
	}
	return strings.Join(out, "\n")
}

func TestSlackMentionAnswerPlaces(t *testing.T) {
	f, db := inboundFront(t)
	srv := newFakeChannelServer(t)
	open := `{"ok":true,"channel":{"id":"D77"}}`
	srv.reply = func(path string, _ int) string {
		if path == "/conversations.open" {
			return open
		}
		return `{"ok":true,"ts":"1700.9"}`
	}
	run := inboundRunArgs(t, f, db, "slack",
		`{"type":"event_callback","team_id":"T1","event":{"type":"app_mention","text":"<@UBOT> what sold?","user":"U1","channel":"C1","ts":"1700.1"}}`)

	inThread := func(reqs []channelRequest) bool {
		return len(reqs) == 1 && reqs[0].Path == "/chat.postMessage" &&
			strings.Contains(reqs[0].Body, `"channel":"C1"`) && strings.Contains(reqs[0].Body, `"thread_ts":"1700.1"`) &&
			strings.Contains(reqs[0].Body, plantedAnswer)
	}
	privately := func(reqs []channelRequest) bool {
		return len(reqs) == 3 && reqs[0].Path == "/conversations.open" && reqs[0].Body == `{"users":"U1"}` &&
			reqs[1].Path == "/chat.postMessage" && strings.Contains(reqs[1].Body, `"channel":"D77"`) &&
			strings.Contains(reqs[1].Body, plantedAnswer) && !strings.Contains(reqs[1].Body, "thread_ts") &&
			reqs[2].Path == "/chat.postMessage" && strings.Contains(reqs[2].Body, `"channel":"C1"`) &&
			strings.Contains(reqs[2].Body, `"thread_ts":"1700.1"`) &&
			strings.Contains(reqs[2].Body, "<@U1> I sent the answer to your direct messages.") &&
			!strings.Contains(reqs[2].Body, "4,210") && !strings.Contains(reqs[2].Body, "SKU-PLANTED")
	}

	reqs, err := replyRequests(t, f, db, srv, run)
	if err != nil || !privately(reqs) {
		t.Errorf("a new install with no listed channel: %v\n%s", err, describe(reqs))
	}
	for _, c := range []struct {
		body string
		want func([]channelRequest) bool
	}{
		{`{"channel":"slack","mode":"thread"}`, inThread},
		{`{"channel":"slack","mode":"direct","places":["C1"]}`, privately},
		{`{"channel":"slack","mode":"listed","places":["C1"]}`, inThread},
		{`{"channel":"slack","mode":"listed","places":["C9"]}`, privately},
	} {
		putReplies(t, f, c.body)
		if reqs, err := replyRequests(t, f, db, srv, run); err != nil || !c.want(reqs) {
			t.Errorf("%s: %v\n%s", c.body, err, describe(reqs))
		}
	}

	putReplies(t, f, `{"channel":"slack","mode":"direct"}`)
	for _, refusal := range []struct {
		name  string
		reply func(path string, n int) string
	}{
		{"the app may not open the chat", func(path string, _ int) string {
			if path == "/conversations.open" {
				return `{"ok":false,"error":"cannot_dm_bot"}`
			}
			return `{"ok":true,"ts":"1700.9"}`
		}},
		{"the chat refuses the message", func(path string, n int) string {
			if path == "/conversations.open" {
				return open
			}
			if n == 2 {
				return `{"ok":false,"error":"not_in_channel"}`
			}
			return `{"ok":true,"ts":"1700.9"}`
		}},
	} {
		srv.reply = refusal.reply
		reqs, err := replyRequests(t, f, db, srv, run)
		last := reqs[len(reqs)-1]
		if err != nil || !strings.Contains(last.Body, `"channel":"C1"`) ||
			!strings.Contains(last.Body, "<@U1> I could not send the answer to your direct messages. Open a direct message with the OpenNeko app, then ask again.") {
			t.Errorf("%s: %v\n%s", refusal.name, err, describe(reqs))
		}
		for _, r := range reqs {
			if strings.Contains(r.Body, `"channel":"C1"`) && (strings.Contains(r.Body, "4,210") || strings.Contains(r.Body, "SKU-PLANTED")) {
				t.Errorf("%s: the group chat got the answer: %s", refusal.name, r.Body)
			}
		}
	}

	srv.reply = func(string, int) string { return open }
	slackAPIBase = "http://127.0.0.1:1"
	reqs, err = replyRequests(t, f, db, srv, run)
	if err == nil || len(reqs) != 0 {
		t.Errorf("a network fault sent a note in place of a retry: %v %s", err, describe(reqs))
	}

	slash := `{"command":"/openneko","text":"what sold?","channel_id":"C1","user_id":"U1","team_id":"T1"}`
	if got := sharedOf(t, "slack", slackRecipient(parse(t, slash))); got != "" {
		t.Errorf("an ephemeral slash reply counts as a group chat: %s", got)
	}
	dm := `{"type":"event_callback","team_id":"T1","event":{"type":"message","channel_type":"im","text":"hi","user":"U1","channel":"D5","ts":"1"}}`
	if got := sharedOf(t, "slack", slackRecipient(parse(t, dm))); got != "" {
		t.Errorf("a direct message counts as a group chat: %s", got)
	}
}

func sharedOf(t *testing.T, channel string, recipient any) string {
	t.Helper()
	place, shared := sharedPlace(channel, recipient)
	if !shared {
		return ""
	}
	return place
}

func TestTelegramGroupAnswerPlaces(t *testing.T) {
	f, db := inboundFront(t)
	srv := newFakeChannelServer(t)
	run := inboundRunArgs(t, f, db, "telegram",
		`{"update_id":31,"message":{"message_id":5,"chat":{"id":-1001555,"type":"supergroup","title":"Ops"},"from":{"id":99,"first_name":"Ada"},"text":"What sold today?"}}`)

	reqs, err := replyRequests(t, f, db, srv, run)
	if err != nil || len(reqs) != 2 ||
		!strings.Contains(reqs[0].Body, `"chat_id":"99"`) || !strings.Contains(reqs[0].Body, "SKU-PLANTED") ||
		!strings.Contains(reqs[1].Body, `"chat_id":-1001555`) || !strings.Contains(reqs[1].Body, "I sent the answer to a private chat with you.") ||
		strings.Contains(reqs[1].Body, "SKU-PLANTED") {
		t.Errorf("a group question on a new install: %v\n%s", err, describe(reqs))
	}

	srv.statusOf = func(_ string, n int) int {
		if n == 1 {
			return 403
		}
		return 200
	}
	srv.reply = func(_ string, n int) string {
		if n == 1 {
			return `{"ok":false,"error_code":403,"description":"Forbidden: bot can't initiate conversation with a user"}`
		}
		return `{"ok":true,"result":{"message_id":7}}`
	}
	reqs, err = replyRequests(t, f, db, srv, run)
	if err != nil || len(reqs) != 2 || !strings.Contains(reqs[1].Body, `"chat_id":-1001555`) ||
		!strings.Contains(reqs[1].Body, "I could not send the answer to a private chat with you. Open a chat with this bot, then ask again.") ||
		strings.Contains(reqs[1].Body, "SKU-PLANTED") {
		t.Errorf("the person never opened a chat with the bot: %v\n%s", err, describe(reqs))
	}

	srv.statusOf, srv.reply = nil, func(string, int) string { return `{"ok":true,"result":{"message_id":7}}` }
	putReplies(t, f, `{"channel":"telegram","mode":"listed","places":["-1001555"]}`)
	reqs, err = replyRequests(t, f, db, srv, run)
	if err != nil || len(reqs) != 1 || !strings.Contains(reqs[0].Body, `"chat_id":-1001555`) || !strings.Contains(reqs[0].Body, "SKU-PLANTED") {
		t.Errorf("a listed group: %v\n%s", err, describe(reqs))
	}
	putReplies(t, f, `{"channel":"telegram","mode":"direct"}`)
	private := inboundRunArgs(t, f, db, "telegram",
		`{"update_id":32,"message":{"message_id":6,"chat":{"id":99,"type":"private"},"from":{"id":99,"first_name":"Ada"},"text":"And today?"}}`)
	reqs, err = replyRequests(t, f, db, srv, private)
	if err != nil || len(reqs) != 1 || !strings.Contains(reqs[0].Body, `"chat_id":99`) || !strings.Contains(reqs[0].Body, "SKU-PLANTED") {
		t.Errorf("a private chat: %v\n%s", err, describe(reqs))
	}
}

func TestWorkflowOutputFollowsTheReplyRule(t *testing.T) {
	f, db := inboundFront(t)
	a := f.API
	const (
		wfOwned  = "b1000000-0000-4000-8000-000000000001"
		wfTeam   = "b1000000-0000-4000-8000-000000000002"
		thread   = "b1000000-0000-4000-8000-000000000003"
		workRun  = "b1000000-0000-4000-8000-000000000004"
		workRun2 = "b1000000-0000-4000-8000-000000000005"
		runOwned = "b1000000-0000-4000-8000-000000000006"
		runTeam  = "b1000000-0000-4000-8000-000000000007"
	)
	seed(t, db, `insert into workflow_definition (id, org_id, name, owner_user_id) values ($1, 'org', 'Stock', 'member')`, wfOwned)
	seed(t, db, `insert into workflow_definition (id, org_id, name, team_approved_by) values ($1, 'org', 'Team', 'admin')`, wfTeam)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'workflow', '*' from user_group where org_id = 'org' and slug = 'everyone' on conflict do nothing`)
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org')`, thread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', $3, 'test'), ($2, 'org', $3, 'test')`, workRun, workRun2, thread)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload) values
		($1, 'org', $3, $5, $6, 'manual', '{}'), ($2, 'org', $4, $5, $7, 'manual', '{}')`, runOwned, runTeam, wfOwned, wfTeam, thread, workRun, workRun2)
	seed(t, db, `insert into channel_identity (org_id, channel_plugin, workspace_id, channel_user_id, status, app_user_id)
		values ('org', 'slack', 'T1', 'U42', 'linked', 'member')`)
	seed(t, db, `insert into delivery_binding (org_id, audience, channel_plugin, recipient, enabled)
		values ('org', '*', 'slack', '{"kind":"slack","channel":"C1","thread_ts":"1700.1"}', true)`)

	srv := newFakeChannelServer(t)
	srv.reply = func(path string, _ int) string {
		if path == "/conversations.open" {
			return `{"ok":true,"channel":{"id":"D42"}}`
		}
		return `{"ok":true,"ts":"1700.9"}`
	}
	send := func(id, run string) ([]channelRequest, jobs.ChannelDeliverArgs) {
		t.Helper()
		out := jsObjOf("id", id, "workflowRunId", run, "title", "Stock low", "body", plantedAnswer, "mood", "act")
		if err := a.deliverWorkflowOutput(t.Context(), "org", out); err != nil {
			t.Fatal(err)
		}
		queued := riverJobs(t, db, jobs.KindChannelDeliver)
		var job jobs.ChannelDeliverArgs
		_ = json.Unmarshal([]byte(queued[len(queued)-1].Args), &job)
		srv.requests = nil
		if err := a.ChannelDeliver(t.Context(), job); err != nil {
			t.Fatal(err)
		}
		return srv.requests, job
	}

	reqs, job := send("out-1", runOwned)
	if job.Notice != "output" || job.PrivateTo != "U42" || len(reqs) != 3 || reqs[0].Body != `{"users":"U42"}` ||
		!strings.Contains(reqs[1].Body, "SKU-PLANTED") || !strings.Contains(reqs[2].Body, `"channel":"C1"`) ||
		!strings.Contains(reqs[2].Body, "A workflow output went to its owner privately.") || strings.Contains(reqs[2].Body, "SKU-PLANTED") {
		t.Errorf("an owned workflow: %+v\n%s", job, describe(reqs))
	}
	reqs, job = send("out-2", runTeam)
	if job.PrivateTo != "" || len(reqs) != 1 || !strings.Contains(reqs[0].Body, "A workflow output is ready in OpenNeko. Its owner can read it there.") ||
		strings.Contains(reqs[0].Body, "SKU-PLANTED") {
		t.Errorf("a workflow with no owner: %+v\n%s", job, describe(reqs))
	}
	putReplies(t, f, `{"channel":"slack","mode":"thread"}`)
	reqs, job = send("out-3", runTeam)
	if job.Notice != "" || len(reqs) != 1 || !strings.Contains(reqs[0].Body, "SKU-PLANTED") {
		t.Errorf("thread mode: %+v\n%s", job, describe(reqs))
	}
}

func TestChannelRepliesRoute(t *testing.T) {
	f, db := inboundFront(t)
	admin := sessionCookie("admin")
	w := call(f, "GET", "/api/v1/admin/channels/replies", "", admin)
	if got := strings.TrimSpace(w.Body.String()); w.Code != 200 ||
		got != `{"slack":{"mode":"listed","places":[]},"telegram":{"mode":"listed","places":[]},"keptFromUpgrade":false}` {
		t.Errorf("a new install: %d %s", w.Code, got)
	}
	put := func(b string, who map[string]string) string {
		w := call(f, "PUT", "/api/v1/admin/channels/replies", b, who)
		return fmt.Sprintf("%d %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
	for _, c := range []struct{ body, want string }{
		{`{"channel":"hud","mode":"thread"}`, `400 {"error":"channel must be slack or telegram"}`},
		{`{"channel":"slack","mode":"everywhere"}`, `400 {"error":"mode must be thread, direct or listed"}`},
		{`{"channel":"slack","mode":"listed","places":["#general"]}`, `400 {"error":"\"#general\" is not a slack chat ID"}`},
		{`{"channel":"slack","mode":"listed","places":"C1"}`, `400 {"error":"places must be a list of at most 200 chat IDs"}`},
		{`{"channel":"telegram","mode":"listed","places":["12345"]}`, `400 {"error":"\"12345\" is not a telegram chat ID"}`},
		{`{"channel":"slack","mode":"listed","places":["D123"]}`, `400 {"error":"\"D123\" is not a slack chat ID"}`},
		{`{"channel":"slack","mode":"listed","places":["C1","C1 "]}`, `200 {"slack":{"mode":"listed","places":["C1"]},"telegram":{"mode":"listed","places":[]},"keptFromUpgrade":false}`},
		{`{"channel":"telegram","mode":"listed","places":["@ops_room"]}`, `200 {"slack":{"mode":"listed","places":["C1"]},"telegram":{"mode":"listed","places":["@ops_room"]},"keptFromUpgrade":false}`},
	} {
		if got := put(c.body, admin); got != c.want {
			t.Errorf("%s: %s, want %s", c.body, got, c.want)
		}
	}
	if got := put(`{"channel":"slack","mode":"thread"}`, sessionCookie("member")); !strings.HasPrefix(got, "403") {
		t.Errorf("a member changed the rule: %s", got)
	}
	if w := call(f, "GET", "/api/v1/admin/channels/replies", "", sessionCookie("member")); w.Code != 403 {
		t.Errorf("a member read the rule: %d", w.Code)
	}
	put(`{"channel":"slack","mode":"listed","places":["C1"]}`, admin)
	var events []string
	rows, err := db.Query(t.Context(), `select event || ' ' || payload::text from audit_chain where org_id = 'org' and entity_kind = 'channel_replies' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		events = append(events, e)
	}
	if len(events) != 2 || !strings.Contains(strings.Join(events, "\n"), `"places": ["C1"], "channel": "slack", "actorUserId": "admin", "previousMode": "listed", "previousPlaces": []`) ||
		!strings.Contains(events[0]+events[1], `"places": ["@ops_room"]`) {
		t.Errorf("audit events (one per change, none for a save with no change):\n%s", strings.Join(events, "\n"))
	}
}

func TestChannelRepliesMigrationKeepsTheThreadOnUpgrade(t *testing.T) {
	db := testpg.Migrated(t)
	ddl, err := assets.MigrationsFS.ReadFile("migrations/0148_channel_reply_places.sql")
	if err != nil {
		t.Fatal(err)
	}
	a := &API{DB: db}
	ctx := t.Context()
	seed(t, db, `insert into organization (id, name) values ('upgraded', 'Before 0148')`)
	if _, err := db.Exec(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `insert into organization (id, name) values ('fresh', 'After 0148')`)
	for org, want := range map[string]string{
		"upgraded": `{"slack":{"mode":"thread","places":[]},"telegram":{"mode":"thread","places":[]},"keptFromUpgrade":true}`,
		"fresh":    `{"slack":{"mode":"listed","places":[]},"telegram":{"mode":"listed","places":[]},"keptFromUpgrade":false}`,
	} {
		rules, err := a.channelReplyRules(ctx, org)
		got, _ := json.Marshal(rules)
		if err != nil || string(got) != want {
			t.Errorf("%s: %v %s, want %s", org, err, got, want)
		}
	}
	seed(t, db, `update llm_provider_config set config = '{"slack":{"mode":"direct","places":[]}}' where org_id = 'upgraded' and scope = 'channel-replies'`)
	if _, err := db.Exec(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	if rules, _ := a.channelReplyRules(ctx, "upgraded"); rules.Slack.Mode != "direct" {
		t.Errorf("a second run of the migration changed an admin's rule: %+v", rules)
	}
}
