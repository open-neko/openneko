package webfront

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCleanBotName(t *testing.T) {
	for in, want := range map[string]string{
		` "Stock watcher." `:                        "Stock watcher",
		`'Sam's orders'`:                            "Sam's orders",
		"Weekly wholesale customer revenue checker": "Weekly wholesale custome",
		"  ":            "",
		"*Low  stock*!": "Low stock",
	} {
		if got := cleanBotName(in); got != want {
			t.Errorf("cleanBotName(%q) = %q, want %q", in, got, want)
		}
	}
}

const (
	sweepThreadA = "c1000000-0000-4000-8000-000000000001"
	sweepThreadC = "c1000000-0000-4000-8000-000000000003"
	sweepBotA    = "c2000000-0000-4000-8000-000000000001"
	sweepBotB    = "c2000000-0000-4000-8000-000000000002"
	sweepBotC    = "c2000000-0000-4000-8000-000000000003"
	sweepBotD    = "c2000000-0000-4000-8000-000000000004"
)

func TestBotNameSweep(t *testing.T) {
	f, db, g := hostFront(t)
	ctx := t.Context()
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"k"}`)
	for _, th := range []string{sweepThreadA, sweepThreadC} {
		seed(t, db, `insert into work_thread (id, org_id, created_by_user_id, title) values ($1, 'org', 'member', 'New chat')`, th)
	}
	for i, m := range []string{"  watch stock  ", " ", strings.Repeat("x", 450), "two", "three", "four", "five", "six"} {
		seed(t, db, `insert into work_message (org_id, thread_id, role, content, created_at) values ('org', $1, 'user', $2, now() + make_interval(secs => $3))`, sweepThreadA, m, i)
	}
	seed(t, db, `insert into work_message (org_id, thread_id, role, content) values ('org', $1, 'assistant', 'a reply')`, sweepThreadA)
	seed(t, db, `insert into work_message (org_id, thread_id, role, content) values ('org', $1, 'user', 'fail me')`, sweepThreadC)
	seed(t, db, `insert into bot (id, org_id, owner_user_id, name, coat, pattern, thread_id, instructions, name_pending) values
		($1, 'org', 'member', 'New chat', 'ink', 'solid', $5, '  Watches stock.  ', true),
		($2, 'org', 'member', 'No thread', 'ink', 'solid', null, '', true),
		($3, 'org', 'member', 'Fails', 'ink', 'solid', $6, '', true),
		($4, 'org', 'member', 'Settled', 'ink', 'solid', null, '', false)`, sweepBotA, sweepBotB, sweepBotC, sweepBotD, sweepThreadA, sweepThreadC)

	g.reply = map[string]any{"name": `"Stock watcher."`}
	var inputs []botNameInput
	named, failed, err := f.API.nameBackfilledBots(ctx, func(ctx context.Context, in botNameInput) (string, error) {
		inputs = append(inputs, in)
		if in.OperatorMessages == "- fail me" {
			return "", errors.New("planted outage")
		}
		return f.API.nameBot(ctx, in)
	})
	if err != nil || named != 1 || failed != 1 {
		t.Fatalf("sweep: named %d failed %d err %v", named, failed, err)
	}
	if len(inputs) != 2 {
		t.Fatalf("model asked %d times, want 2 (a bot with no thread is not asked)", len(inputs))
	}
	in := inputs[0]
	if in.OperatorMessages == "- fail me" {
		in = inputs[1]
	}
	want := "- watch stock\n- " + strings.Repeat("x", 400) + "\n- two\n- three\n- four"
	if in.OperatorMessages != want || in.Instructions != "Watches stock." || in.ChatTitle != "New chat" {
		t.Errorf("input %+v", in)
	}
	if len(g.calls) != 1 || g.calls[0].request.Values["instructions"] != "Watches stock." || !strings.Contains(g.calls[0].request.Signature, "-> name:string") {
		t.Fatalf("gateway calls %+v", g.calls)
	}

	names := map[string]string{}
	rows, err := db.Query(ctx, `select id::text, name, name_pending from bot`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, name string
		var pending bool
		if err := rows.Scan(&id, &name, &pending); err != nil {
			t.Fatal(err)
		}
		if pending {
			t.Errorf("bot %s is still pending", id)
		}
		names[id] = name
	}
	rows.Close()
	if names[sweepBotB] != "No thread" || names[sweepBotC] != "Fails" || names[sweepBotD] != "Settled" {
		t.Errorf("names %v", names)
	}
	var title string
	if err := db.QueryRow(ctx, `select title from work_thread where id = $1`, sweepThreadA).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if names[sweepBotA] != "Stock watcher" || title != "Stock watcher" {
		t.Errorf("bot A %q, thread title %q", names[sweepBotA], title)
	}

	seed(t, db, `update bot set name_pending = true, name = 'New chat' where id = $1`, sweepBotA)
	seed(t, db, `update work_thread set title = 'New chat' where id = $1`, sweepThreadA)
	if err := f.API.BotNameSweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select name from bot where id = $1`, sweepBotA).Scan(&title); err != nil || title != "Stock watcher" {
		t.Errorf("after the sweep: %q %v", title, err)
	}
	if err := db.QueryRow(ctx, `select title from work_thread where id = $1`, sweepThreadA).Scan(&title); err != nil || title != "Stock watcher" {
		t.Errorf("thread title after the sweep: %q %v", title, err)
	}
}
