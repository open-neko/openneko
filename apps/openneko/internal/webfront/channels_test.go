package webfront

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type channelRequest struct {
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

type fakeChannelServer struct {
	requests []channelRequest
	status   int
	reply    func(path string, n int) string
}

func newFakeChannelServer(t *testing.T) *fakeChannelServer {
	f := &fakeChannelServer{status: 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := channelRequest{Path: r.URL.Path, Body: string(body)}
		if id := r.Header.Get("x-openneko-hud-delivery-id"); id != "" {
			req.Path = strings.TrimPrefix(r.URL.Path, "")
			req.Headers = map[string]string{"x-openneko-hud-delivery-id": id, "x-openneko-hud-signature": r.Header.Get("x-openneko-hud-signature")}
		}
		f.requests = append(f.requests, req)
		w.WriteHeader(f.status)
		if f.reply != nil {
			_, _ = w.Write([]byte(f.reply(r.URL.Path, len(f.requests))))
		}
	}))
	t.Cleanup(srv.Close)
	oldSlack, oldTelegram, oldHUD, oldNow, oldID := slackAPIBase, telegramAPIBase, hudChannelURL, hudNow, hudRandomID
	slackAPIBase, telegramAPIBase, hudChannelURL = srv.URL, srv.URL, srv.URL
	hudNow = func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC) }
	hudRandomID = func() string { return "delivery-1" }
	t.Cleanup(func() {
		slackAPIBase, telegramAPIBase, hudChannelURL, hudNow, hudRandomID = oldSlack, oldTelegram, oldHUD, oldNow, oldID
	})
	f.reply = func(path string, n int) string {
		switch {
		case strings.HasPrefix(path, "/bot"):
			return `{"ok":true,"result":{"message_id":` + strconv.Itoa(100+n) + `}}`
		case strings.HasPrefix(path, "/chat."):
			return `{"ok":true,"ts":"1700.9"}`
		}
		return "ok"
	}
	return f
}

func TestChannelDeliveryMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/channeldeliver.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Events map[string]json.RawMessage `json:"events"`
		Cases  []struct {
			Name      string            `json:"name"`
			Channel   string            `json:"channel"`
			Recipient json.RawMessage   `json:"recipient"`
			Env       map[string]string `json:"env"`
			Events    string            `json:"events"`
			Requests  []channelRequest  `json:"requests"`
			Result    struct {
				Delivered bool   `json:"delivered"`
				Ref       string `json:"ref"`
			} `json:"result"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		srv := newFakeChannelServer(t)
		recipient, _ := jsParse(c.Recipient)
		events, _ := jsParse(fixture.Events[c.Events])
		result, err := findBuiltInChannel(c.Channel).Deliver(t.Context(), recipient, jsArray(events), c.Env)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if result.Delivered != c.Result.Delivered || result.Ref != c.Result.Ref {
			t.Errorf("%s: result %+v, want %+v", c.Name, result, c.Result)
		}
		if len(srv.requests) != len(c.Requests) {
			t.Errorf("%s: %d requests, want %d", c.Name, len(srv.requests), len(c.Requests))
			continue
		}
		for i, want := range c.Requests {
			got := srv.requests[i]
			if c.Channel == "hud" {
				want.Path = "/api/deliver"
			}
			if got.Path != want.Path || got.Body != want.Body {
				t.Errorf("%s request %d:\n got %s %s\nwant %s %s", c.Name, i, got.Path, got.Body, want.Path, want.Body)
			}
			for k, v := range want.Headers {
				if got.Headers[k] != v {
					t.Errorf("%s request %d header %s: %q, want %q", c.Name, i, k, got.Headers[k], v)
				}
			}
		}
	}
}

func TestChannelDeliverJob(t *testing.T) {
	srv := newFakeChannelServer(t)
	a := &API{SecretsDir: t.TempDir()}
	job := jobs.ChannelDeliverArgs{OrgID: "org", ChannelPlugin: "telegram", Recipient: json.RawMessage(`{"chatId":7}`),
		Events: json.RawMessage(`[{"kind":"converse","text":"hi"}]`), Key: "reply-r1"}
	if err := a.ChannelDeliver(t.Context(), job); err == nil || err.Error() != "channel-delivery: telegram failed: channel telegram is not turned on: set its required settings in Admin, Settings, Channels" {
		t.Errorf("channel off: %v", err)
	}
	if err := secrets.Write(secrets.Store{"channel.telegram": {"TELEGRAM_BOT_TOKEN": "123:abc"}, "channel.slack": {"SLACK_BOT_TOKEN": "xoxb-1"}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	if err := a.ChannelDeliver(t.Context(), job); err != nil {
		t.Errorf("delivered: %v", err)
	}
	if len(srv.requests) != 1 || srv.requests[0].Path != "/bot123:abc/sendMessage" || srv.requests[0].Body != `{"chat_id":7,"text":"hi","parse_mode":"HTML"}` {
		t.Errorf("requests: %+v", srv.requests)
	}
	job.Events = json.RawMessage(`[]`)
	if err := a.ChannelDeliver(t.Context(), job); err == nil || err.Error() != "channel-delivery: telegram reported delivered=false" {
		t.Errorf("nothing to send: %v", err)
	}
	job.Events = json.RawMessage(`[{"kind":"converse","text":"hi"}]`)
	srv.status, srv.reply = 400, func(string, int) string { return `{"ok":false,"description":"Bad Request: chat not found"}` }
	if err := a.ChannelDeliver(t.Context(), job); err == nil || err.Error() != "channel-delivery: telegram failed: Telegram sendMessage returned ok=false (Bad Request: chat not found)" {
		t.Errorf("refused: %v", err)
	}
	srv.status, srv.reply = 200, func(string, int) string { return `{"ok":true,"ts":"1.2"}` }
	srv.requests = nil
	job.ChannelPlugin, job.Recipient = "@open-neko/plugin-slack", json.RawMessage(`{"channel":"D1"}`)
	if err := a.ChannelDeliver(t.Context(), job); err != nil || len(srv.requests) != 1 || srv.requests[0].Path != "/chat.postMessage" {
		t.Errorf("a package name: %v %+v", err, srv.requests)
	}
	job.ChannelPlugin = "fax"
	if err := a.ChannelDeliver(t.Context(), job); err == nil || err.Error() != "channel-delivery: fax failed: unknown channel fax" {
		t.Errorf("unknown: %v", err)
	}
	telegramAPIBase = "http://127.0.0.1:1"
	job.ChannelPlugin, job.Recipient = "telegram", json.RawMessage(`{"chatId":7}`)
	if err := a.ChannelDeliver(t.Context(), job); err == nil || !strings.Contains(err.Error(), "Telegram sendMessage network error") || strings.Contains(err.Error(), "123:abc") {
		t.Errorf("down: %v", err)
	}
}

func TestWorkflowOutputGoesToBoundChannels(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := t.Context()
	const (
		wfShared  = "a1000000-0000-4000-8000-000000000001"
		wfPrivate = "a1000000-0000-4000-8000-000000000002"
		watcherID = "a1000000-0000-4000-8000-000000000003"
		thread    = "a1000000-0000-4000-8000-000000000004"
		workRun   = "a1000000-0000-4000-8000-000000000005"
		runShared = "a1000000-0000-4000-8000-000000000006"
		runOther  = "a1000000-0000-4000-8000-000000000007"
		runWatch  = "a1000000-0000-4000-8000-000000000008"
		workRun2  = "a1000000-0000-4000-8000-000000000009"
		workRun3  = "a1000000-0000-4000-8000-00000000000a"
	)
	seed(t, db, `delete from item_grant where org_id = 'org' and item_type in ('workflow', 'watcher', 'pack')`)
	seed(t, db, `insert into workflow_definition (id, org_id, name) values ($1, 'org', 'Shared'), ($2, 'org', 'Private')`, wfShared, wfPrivate)
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'workflow', $1 from user_group where org_id = 'org' and slug = 'everyone'`, wfShared)
	seed(t, db, `insert into user_group (org_id, slug, name) values ('org', 'buyers', 'Buyers')`)
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org')`, thread)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', $2, 'test'), ($3, 'org', $2, 'test'), ($4, 'org', $2, 'test')`,
		workRun, thread, workRun2, workRun3)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload) values
		($1, 'org', $4, $5, $6, 'manual', '{}'), ($2, 'org', $7, $5, $9, 'manual', '{}'),
		($3, 'org', $4, $5, $10, 'watcher', jsonb_build_object('watcherId', $8::text))`,
		runShared, runOther, runWatch, wfShared, thread, workRun, wfPrivate, watcherID, workRun2, workRun3)
	seed(t, db, `insert into delivery_binding (org_id, audience, channel_plugin, recipient, enabled) values
		('org', '*', 'telegram', '{"chatId":7}', true),
		('org', 'buyers', 'slack', '{"channel":"D1"}', true),
		('org', 'administrators', 'hud', '{}', true),
		('org', 'nobody', 'hud', '{}', true),
		('org', '*', '@open-neko/plugin-slack', '{"channel":"OLD"}', false)`)
	output := func(id, run string) *jsObj {
		return jsObjOf("id", id, "workflowRunId", run, "title", "Stock low", "body", "Three SKUs.", "mood", nil)
	}
	keys := func() []string {
		var out []string
		for _, j := range riverJobs(t, db, "channel_deliver") {
			var args jobs.ChannelDeliverArgs
			_ = json.Unmarshal([]byte(j.Args), &args)
			out = append(out, args.Key+" "+args.ChannelPlugin+" "+string(args.Events))
		}
		return out
	}
	if err := a.deliverWorkflowOutput(ctx, "org", output("out-1", runShared)); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(keys(), "\n")
	if !strings.Contains(got, "output-out-1-telegram telegram") || !strings.Contains(got, "output-out-1-slack slack") ||
		!strings.Contains(got, "output-out-1-hud hud") || strings.Count(got, "output-out-1") != 3 ||
		!strings.Contains(got, `[{"id": "out-1", "body": "Three SKUs.", "kind": "inform", "mood": "watch", "title": "Stock low"}]`) {
		t.Errorf("shared workflow: %s", got)
	}
	if err := a.deliverWorkflowOutput(ctx, "org", output("out-1", runShared)); err != nil || len(keys()) != 3 {
		t.Errorf("a repeat adds no job: %v %v", err, keys())
	}
	if err := a.deliverWorkflowOutput(ctx, "org", output("out-2", runOther)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys(), "\n"); strings.Count(got, "output-out-2") != 1 || !strings.Contains(got, "output-out-2-hud hud") {
		t.Errorf("a workflow only admins hold: %s", got)
	}
	if err := a.deliverWorkflowOutput(ctx, "org", output("out-3", runWatch)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys(), "\n"); strings.Count(got, "output-out-3") != 1 {
		t.Errorf("a watcher the audience does not hold: %s", got)
	}
	seed(t, db, `insert into item_grant (org_id, group_id, item_type, item_id)
		select 'org', id, 'watcher', $1 from user_group where org_id = 'org' and slug = 'everyone'`, watcherID)
	if err := a.deliverWorkflowOutput(ctx, "org", output("out-4", runWatch)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keys(), "\n"); strings.Count(got, "output-out-4") != 3 {
		t.Errorf("a held watcher: %s", got)
	}
}
