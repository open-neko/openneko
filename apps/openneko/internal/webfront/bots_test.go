package webfront

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestDescribeSchedule(t *testing.T) {
	for _, c := range []struct {
		cron     *string
		tz       string
		enabled  bool
		expected string
	}{
		{nil, "UTC", true, "Runs when you ask"},
		{ptr(""), "UTC", true, "Runs when you ask"},
		{ptr("*/15 * * * *"), "UTC", true, "Every 15 minutes"},
		{ptr("*/1 * * * *"), "UTC", true, "Every 1 minute"},
		{ptr("*/0 * * * *"), "UTC", true, "*/0 * * * *"},
		{ptr("0 * * * *"), "UTC", true, "Every hour"},
		{ptr("5 * * * *"), "UTC", false, "Every hour at :05 · Paused"},
		{ptr("30 9 * * *"), "UTC", true, "Every day at 9:30 AM UTC"},
		{ptr("0 0 * * 1-5"), "Asia/Kolkata", true, "Every weekday at 12:00 AM Asia/Kolkata"},
		{ptr("0 12 * * 0,6"), "UTC", true, "Every weekend at 12:00 PM UTC"},
		{ptr("15 13 * * 3"), "UTC", true, "Every Wednesday at 1:15 PM UTC"},
		{ptr("0 18 * * 1,3,5"), "Europe/Paris", false, "Every Monday, Wednesday and Friday at 6:00 PM Europe/Paris · Paused"},
		{ptr("0 18 * * 1,3"), "UTC", true, "Every Monday and Wednesday at 6:00 PM UTC"},
		{ptr("0 25 * * *"), "UTC", true, "0 25 * * *"},
		{ptr("0 9 1 * *"), "Asia/Kolkata", true, "0 9 1 * * (Asia/Kolkata)"},
		{ptr("bogus"), "UTC", true, "Bogus"},
		{ptr("0 9 * * 7"), "", true, "0 9 * * 7"},
		{ptr("  0   9 * * 2  "), "UTC", true, "Every Tuesday at 9:00 AM UTC"},
		{ptr("123 * * * *"), "UTC", true, "Every hour at :123"},
		{ptr("0 9 * * 1, 2"), "UTC", true, "0 9 * * 1, 2"},
		{ptr("ünïcode x"), "Asia/Tokyo", false, "Ünïcode x (Asia/Tokyo) · Paused"},
	} {
		if got := describeSchedule(c.cron, c.tz, c.enabled); got != c.expected {
			t.Errorf("%v %q: %q, want %q", c.cron, c.tz, got, c.expected)
		}
	}
}


func TestBotTextHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"## Stock\n**12** units left. See [the report](https://x.test).": "Stock 12 units left. See the report.",
		"```sql\nselect 1\n```\nDone  > ok | yes":                        "Done ok yes",
		"    tabs\tand spaces  ":                                         "tabs and spaces",
		strings.Repeat("a ", 400):                                        strings.Repeat("a ", 199) + "a…",
		strings.Repeat("é", 398) + " 😀😀":                                 strings.Repeat("é", 398) + "…",
	} {
		if got := plainSummary(in); got != want {
			t.Errorf("plainSummary(%.20q) = %.40q", in, got)
		}
	}
	if got := stepTexts([]byte(`[{"id":"a","description":" Fetch sales. "},{"id":"b"},{"id":"c","description":"Post in chat."},{"description":5},["x"]]`)); !reflect.DeepEqual(got, []string{"Fetch sales.", "Post in chat.", "5"}) {
		t.Errorf("stepTexts: %q", got)
	}
	if got := stepTexts([]byte(`null`)); len(got) != 0 {
		t.Errorf("stepTexts null: %q", got)
	}
	first := []string{starterJobs[0], starterJobs[1], starterJobs[2], starterJobs[3]}
	for name, want := range map[string][]string{
		"Neko":           first,
		"Customers":      first,
		"Stock watcher":  {starterJobs[2], starterJobs[0], starterJobs[1], starterJobs[3]},
		"Order desk":     {starterJobs[3], starterJobs[0], starterJobs[1], starterJobs[2]},
		"Weekly reports": {starterJobs[5], starterJobs[0], starterJobs[1], starterJobs[2]},
	} {
		if got := botStarters(name); !reflect.DeepEqual(got, want) {
			t.Errorf("starters %q: %q", name, got)
		}
	}
	if got := everyLabel(3600) + "|" + everyLabel(7200) + "|" + everyLabel(60) + "|" + everyLabel(300) + "|" + everyLabel(90); got != "hour|2 hours|minute|5 minutes|90 seconds" {
		t.Errorf("everyLabel: %s", got)
	}
}

const (
	botThread   = "b0000000-0000-4000-8000-000000000001"
	botOther    = "b0000000-0000-4000-8000-000000000002"
	botWatch    = "b1000000-0000-4000-8000-000000000001"
	botEvent    = "b1000000-0000-4000-8000-000000000002"
	botCron     = "b1000000-0000-4000-8000-000000000003"
	botRunsFrom = "b2000000-0000-4000-8000-000000000010"
)

func TestBotRoutes(t *testing.T) {
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	ctx := context.Background()
	member, other := sessionCookie("member"), sessionCookie("other")

	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/bots"}, {"POST", "/api/v1/bots"}, {"GET", "/api/v1/bots/" + unknownUUID},
		{"PATCH", "/api/v1/bots/" + unknownUUID}, {"DELETE", "/api/v1/bots/" + unknownUUID},
		{"GET", "/api/v1/bots/thread/" + botThread}, {"POST", "/api/v1/bots/thread/" + botThread + "/seen"},
		{"GET", "/api/v1/bots/" + unknownUUID + "/routines/" + botCron}, {"PATCH", "/api/v1/bots/" + unknownUUID + "/routines/" + botCron},
	} {
		if w := call(f, c.method, c.path, `{}`, nil); w.Code != 302 {
			t.Errorf("anonymous %s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}

	var list struct{ Bots []botSummary }
	get := func(who map[string]string) {
		w := call(f, "GET", "/api/v1/bots", "", who)
		list.Bots = nil
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
	}
	get(member)
	if len(list.Bots) != 1 || list.Bots[0].Name != "Neko" || !list.Bots[0].IsDefault || list.Bots[0].Coat != "snow" || list.Bots[0].Pattern != "patch" || list.Bots[0].ThreadID == nil {
		t.Fatalf("default bot: %+v", list.Bots)
	}
	neko := list.Bots[0]
	if get(member); len(list.Bots) != 1 {
		t.Errorf("second list made another default: %d", len(list.Bots))
	}

	for _, body := range []string{`not json`, `{"name":"  ","coat":"snow","pattern":"solid"}`, `{"name":"A","coat":"plaid","pattern":"solid"}`, `{"name":"A","coat":"snow","pattern":"spots"}`} {
		if w := call(f, "POST", "/api/v1/bots", body, member); w.Code != 400 || w.Body.String() != `{"error":"Give the bot a name, a coat and a pattern."}` {
			t.Errorf("create %s: %d %s", body, w.Code, w.Body)
		}
	}
	w := call(f, "POST", "/api/v1/bots", `{"name":"  `+strings.Repeat("x", 45)+`  ","coat":"mint","pattern":"tabby"}`, member)
	var created struct{ Bot botSummary }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Bot.Name != strings.Repeat("x", 40) || created.Bot.IsDefault ||
		created.Bot.Unread || created.Bot.LastMessageAt == nil || created.Bot.ThreadID == nil {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var title, owner, channel string
	if err := db.QueryRow(ctx, `select title, created_by_user_id, channel from work_thread where id = $1`, *created.Bot.ThreadID).Scan(&title, &owner, &channel); err != nil ||
		title != strings.Repeat("x", 40) || owner != "member" || channel != "web" {
		t.Errorf("bot thread: %q %q %q %v", title, owner, channel, err)
	}

	seed(t, db, `insert into work_thread (id, org_id, created_by_user_id, last_message_at) values ($1, 'org', 'member', now() + interval '1 hour')`, botThread)
	seed(t, db, `insert into bot (id, org_id, owner_user_id, name, coat, pattern, thread_id, seen_at, instructions)
		values ($1, 'org', 'member', 'Stock watcher', 'ink', 'solid', $2, now() - interval '1 day', '')`, botOther, botThread)
	seed(t, db, `insert into work_run (org_id, thread_id, backend, status) values ('org', $1, 'test', 'running')`, botThread)
	get(member)
	if len(list.Bots) != 3 || list.Bots[0].ID != botOther || !list.Bots[0].Unread || !list.Bots[0].Running || list.Bots[0].LastRunFailed {
		t.Fatalf("list after seed: %+v", list.Bots)
	}
	if get(other); len(list.Bots) != 1 || list.Bots[0].ID == neko.ID {
		t.Errorf("other user sees own default only: %+v", list.Bots)
	}

	if w := call(f, "GET", "/api/v1/bots/thread/"+botThread, "", member); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"starters":["Keep an eye on stock`) || !strings.Contains(w.Body.String(), `"id":"`+botOther+`"`) {
		t.Errorf("by thread: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/v1/bots/thread/" + botThread, "/api/v1/bots/thread/not-a-uuid"} {
		if w := call(f, "GET", path, "", other); w.Code != 200 || w.Body.String() != `{"bot":null,"starters":[]}` {
			t.Errorf("by thread for %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := call(f, "POST", "/api/v1/bots/thread/"+botThread+"/seen", "", other); w.Code != 404 {
		t.Errorf("seen by other: %d", w.Code)
	}
	if w := call(f, "POST", "/api/v1/bots/thread/"+botThread+"/seen", "", member); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("seen: %d %s", w.Code, w.Body)
	}

	for body, msg := range map[string]string{
		`{}`:                     "Nothing to change.",
		`not json`:               "Nothing to change.",
		`{"name":"   "}`:         "Give the bot a name.",
		`{"name":null}`:          "Give the bot a name.",
		`{"coat":"plaid"}`:       "Pick a coat from the list.",
		`{"pattern":"spots"}`:    "Pick a pattern from the list.",
		`{"instructions":5}`:     "Send the instructions as text.",
		`{"coat":"mint","x":1}x`: "Nothing to change.",
	} {
		if w := call(f, "PATCH", "/api/v1/bots/"+botOther, body, member); w.Code != 400 || w.Body.String() != `{"error":"`+msg+`"}` {
			t.Errorf("patch %s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := call(f, "PATCH", "/api/v1/bots/"+botOther, `{"coat":"mint"}`, other); w.Code != 404 {
		t.Errorf("patch other: %d", w.Code)
	}
	seed(t, db, `update bot set name_pending = true where id = $1`, botOther)
	w = call(f, "PATCH", "/api/v1/bots/"+botOther, `{"name":"  Stock desk ","coat":"mint","pattern":"tabby","instructions":"  `+strings.Repeat("i", 2100)+`"}`, member)
	var patched struct{ Bot botSummary }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &patched) != nil || patched.Bot.Name != "Stock desk" || patched.Bot.Coat != "mint" ||
		patched.Bot.Pattern != "tabby" || len(patched.Bot.Instructions) != 2000 || patched.Bot.LastMessageAt != nil || patched.Bot.Running {
		t.Errorf("patch: %d %s", w.Code, w.Body)
	}
	var pending bool
	if err := db.QueryRow(ctx, `select name_pending from bot where id = $1`, botOther).Scan(&pending); err != nil || pending {
		t.Errorf("name_pending: %v %v", pending, err)
	}
	if w := call(f, "GET", "/api/v1/bots/thread/"+botThread, "", member); !strings.Contains(w.Body.String(), `"starters":[]`) {
		t.Errorf("no starters with instructions: %s", w.Body)
	}

	seed(t, db, `insert into workflow_definition (id, org_id, name, description, goal, steps, cron, cron_timezone, cron_enabled, created_by_thread_id, created_at, paused_until) values
		($1, 'org', 'Stock watch', '', ' Watch stock. ', '[{"description":"Read stock."}]', null, 'UTC', true, $4, '2026-11-01T00:00:00Z', null),
		($2, 'org', 'On order', 'New orders', '', '[]', null, 'UTC', true, $4, '2026-11-02T00:00:00Z', null),
		($3, 'org', 'Morning', 'Daily note', '', 'null', '30 9 * * 1-5', 'Asia/Kolkata', false, $4, '2026-11-03T00:00:00Z', now() + interval '1 day')`,
		botWatch, botEvent, botCron, botThread)
	seed(t, db, `insert into watcher (org_id, workflow_id, name, query, value_path, op, cadence_seconds) values ('org', $1, 'w', 'q', 'v', 'gt', 7200)`, botWatch)
	seed(t, db, `insert into subscription (org_id, workflow_id, source_kind) values ('org', $1, 'shop.order_created')`, botEvent)
	seed(t, db, `insert into work_thread (id, org_id) values ('b3000000-0000-4000-8000-000000000001', 'org')`)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) select ('b4000000-0000-4000-8000-0000000000' || lpad(n::text, 2, '0'))::uuid, 'org', 'b3000000-0000-4000-8000-000000000001', 'test' from generate_series(1, 15) n`)
	seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, status, summary, created_at, finished_at)
		select ('b2000000-0000-4000-8000-0000000000' || lpad(n::text, 2, '0'))::uuid, 'org', $1, 'b3000000-0000-4000-8000-000000000001',
			('b4000000-0000-4000-8000-0000000000' || lpad(n::text, 2, '0'))::uuid, 'cron', case when n = 15 then 'failed' else 'completed' end,
			'**Run** ' || n, '2026-11-01T00:00:00Z'::timestamptz + n * interval '1 hour', '2026-11-01T00:30:00Z'::timestamptz + n * interval '1 hour'
		from generate_series(1, 15) n`, botCron)

	w = call(f, "GET", "/api/v1/bots/"+botOther, "", member)
	var detail struct {
		Bot      botSummary
		Routines []botRoutine
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Bot.ID != botOther || len(detail.Routines) != 3 {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	r0, r1, r2 := detail.Routines[0], detail.Routines[1], detail.Routines[2]
	if r0.Schedule != "Checks every 2 hours" || r0.Description != "Watch stock." || !reflect.DeepEqual(r0.Steps, []string{"Read stock."}) || len(r0.Runs) != 0 || r0.MoreRuns {
		t.Errorf("watch routine: %+v", r0)
	}
	if r1.Schedule != "Runs on each shop order created event" || r1.Description != "New orders" || len(r1.Steps) != 0 {
		t.Errorf("event routine: %+v", r1)
	}
	want := []botRoutineRun{
		{ID: "b2000000-0000-4000-8000-000000000015", Status: "failed", At: "2026-11-01T15:30:00.000Z", Summary: ""},
		{ID: "b2000000-0000-4000-8000-000000000014", Status: "completed", At: "2026-11-01T14:30:00.000Z", Summary: "Run 14"},
		{ID: "b2000000-0000-4000-8000-000000000013", Status: "completed", At: "2026-11-01T13:30:00.000Z", Summary: "Run 13"},
	}
	if r2.Schedule != "Every weekday at 9:30 AM Asia/Kolkata · Paused" || !reflect.DeepEqual(r2.Runs, want) || !r2.MoreRuns || r2.Enabled != true {
		t.Errorf("cron routine: %+v", r2)
	}
	if w := call(f, "GET", "/api/v1/bots/"+botOther, "", other); w.Code != 404 {
		t.Errorf("detail by other: %d", w.Code)
	}
	if w := call(f, "GET", "/api/v1/bots/not-a-uuid", "", member); w.Code != 404 {
		t.Errorf("detail bad id: %d", w.Code)
	}

	runsPath := "/api/v1/bots/" + botOther + "/routines/" + botCron
	var page botRoutineRuns
	if w := call(f, "GET", runsPath+"?before="+botRunsFrom, "", member); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil ||
		len(page.Runs) != 9 || page.More || page.Runs[0].ID != "b2000000-0000-4000-8000-000000000009" {
		t.Errorf("runs page: %d %s", w.Code, w.Body)
	}
	if w := call(f, "GET", runsPath, "", member); json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Runs) != 10 || !page.More {
		t.Errorf("first page: %s", w.Body)
	}
	for _, c := range []struct {
		path string
		who  map[string]string
	}{
		{runsPath + "?before=" + unknownUUID, member}, {runsPath + "?before=x", member}, {runsPath, other},
		{"/api/v1/bots/" + neko.ID + "/routines/" + botCron, member}, {"/api/v1/bots/" + botOther + "/routines/" + wfOrg, member},
	} {
		if w := call(f, "GET", c.path, "", c.who); w.Code != 404 {
			t.Errorf("runs %s: %d", c.path, w.Code)
		}
	}

	if w := call(f, "PATCH", runsPath, `{"enabled":"no"}`, member); w.Code != 400 || w.Body.String() != `{"error":"Send enabled as true or false."}` {
		t.Errorf("enable bad body: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PATCH", "/api/v1/bots/"+botOther+"/routines/"+wfOrg, `{"enabled":false}`, member); w.Code != 404 {
		t.Errorf("enable foreign routine: %d", w.Code)
	}
	if w := call(f, "PATCH", runsPath, `{"enabled":true}`, other); w.Code != 404 {
		t.Errorf("enable by other: %d", w.Code)
	}
	var enabled, paused bool
	if w := call(f, "PATCH", runsPath, `{"enabled":true}`, member); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("enable: %d %s", w.Code, w.Body)
	}
	if err := db.QueryRow(ctx, `select enabled, paused_until is not null from workflow_definition where id = $1`, botCron).Scan(&enabled, &paused); err != nil || !enabled || paused {
		t.Errorf("enable cleared pause: %v %v %v", enabled, paused, err)
	}
	for _, id := range []string{botWatch, botEvent} {
		if w := call(f, "PATCH", "/api/v1/bots/"+botOther+"/routines/"+id, `{"enabled":false}`, member); w.Code != 200 {
			t.Errorf("pause %s: %d", id, w.Code)
		}
	}
	var watcherOn, subOn bool
	if err := db.QueryRow(ctx, `select (select enabled from watcher where workflow_id = $1), (select enabled from subscription where workflow_id = $2)`, botWatch, botEvent).Scan(&watcherOn, &subOn); err != nil || watcherOn || subOn {
		t.Errorf("pause reached watcher and subscription: %v %v %v", watcherOn, subOn, err)
	}

	if w := call(f, "DELETE", "/api/v1/bots/"+botOther, "", other); w.Code != 404 {
		t.Errorf("delete by other: %d", w.Code)
	}
	if w := call(f, "DELETE", "/api/v1/bots/"+botOther, "", member); w.Code != 409 ||
		w.Body.String() != `{"error":"This bot is working. Stop the run or wait for it to finish, then delete the bot."}` {
		t.Errorf("delete while running: %d %s", w.Code, w.Body)
	}
	seed(t, db, `update work_run set created_at = now() - interval '10 minutes' where thread_id = $1`, botThread)
	if w := call(f, "DELETE", "/api/v1/bots/"+botOther, "", member); w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	var left int
	if err := db.QueryRow(ctx, `select (select count(*) from bot where id = $1) + (select count(*) from work_thread where id = $2)
		+ (select count(*) from workflow_definition where created_by_thread_id = $2) + (select count(*) from workflow_run where workflow_id = $3)`,
		botOther, botThread, botCron).Scan(&left); err != nil || left != 0 {
		t.Errorf("delete left %d rows: %v", left, err)
	}
	if w := call(f, "DELETE", "/api/v1/bots/"+botOther, "", member); w.Code != 404 {
		t.Errorf("delete twice: %d", w.Code)
	}
	if w := call(f, "PUT", "/api/v1/bots", "", member); w.Code != 405 || w.Header().Get("Allow") != "GET, POST" {
		t.Errorf("PUT: %d %q", w.Code, w.Header().Get("Allow"))
	}
}
