package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestWorkRunAndChannelDeliverOptions(t *testing.T) {
	wr := &workRun{}
	if wr.Timeout(nil) != -1 {
		t.Error("the work run has a River timeout")
	}
	if got := time.Until(wr.NextRetry(nil)); got < 29*time.Second || got > 30*time.Second {
		t.Errorf("work run retry in %v, want 30 s", got)
	}
	if o := (WorkRunArgs{}).InsertOpts(); o.MaxAttempts != 3 || o.Queue != QueueWorkRun {
		t.Errorf("work run insert options %+v", o)
	}
	cd := &channelDeliver{}
	if got := cd.Timeout(nil); got != 2*time.Minute {
		t.Errorf("delivery timeout %v", got)
	}
	job := func(attempt int) *river.Job[ChannelDeliverArgs] {
		return &river.Job[ChannelDeliverArgs]{JobRow: &rivertype.JobRow{Attempt: attempt}}
	}
	for attempt, want := range map[int]time.Duration{1: 15 * time.Second, 2: 30 * time.Second, 4: 120 * time.Second} {
		if got := time.Until(cd.NextRetry(job(attempt))); got < want-time.Second || got > want {
			t.Errorf("delivery attempt %d retries in %v, want %v", attempt, got, want)
		}
	}
	if o := (ChannelDeliverArgs{}).InsertOpts(); o.MaxAttempts != 9 || o.Queue != "" {
		t.Errorf("delivery insert options %+v", o)
	}
}

func TestWorkRunAndChannelDeliverWorkers(t *testing.T) {
	var runs []WorkRunArgs
	wr := &workRun{run: func(_ context.Context, a WorkRunArgs) error { runs = append(runs, a); return nil }}
	for _, a := range []WorkRunArgs{{RunID: "r", ThreadID: "t"}, {OrgID: "o", ThreadID: "t"}, {OrgID: "o", RunID: "r"}} {
		var cancel *rivertype.JobCancelError
		if err := wr.Work(context.Background(), &river.Job[WorkRunArgs]{JobRow: &rivertype.JobRow{}, Args: a}); !errors.As(err, &cancel) {
			t.Errorf("%+v: %v, want a cancel", a, err)
		}
	}
	if err := wr.Work(context.Background(), &river.Job[WorkRunArgs]{JobRow: &rivertype.JobRow{}, Args: WorkRunArgs{OrgID: "o", RunID: "r", ThreadID: "t"}}); err != nil || len(runs) != 1 {
		t.Errorf("a full job: %v, %d runs", err, len(runs))
	}
	cd := &channelDeliver{}
	if err := cd.Work(context.Background(), &river.Job[ChannelDeliverArgs]{JobRow: &rivertype.JobRow{}, Args: ChannelDeliverArgs{ChannelPlugin: "telegram"}}); err == nil {
		t.Error("a delivery with no runner did not fail")
	}
	var cancel *rivertype.JobCancelError
	cd.run = func(context.Context, ChannelDeliverArgs) error { return nil }
	if err := cd.Work(context.Background(), &river.Job[ChannelDeliverArgs]{JobRow: &rivertype.JobRow{}}); !errors.As(err, &cancel) {
		t.Errorf("a delivery with no channel: %v, want a cancel", err)
	}
}

func nodeInsertSQL(t *testing.T, fn string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "db", "src", "jobs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)export async function ` + fn + `\(.*?\n}\n`).Find(raw)
	if body == nil {
		t.Fatalf("%s not found in packages/db/src/jobs.ts", fn)
	}
	var out []string
	for _, m := range regexp.MustCompile("(?s)`(.*?)`").FindAllSubmatch(body, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

func TestNodeWorkRunAndDeliveryRowsRunInGo(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	workRunSQL := nodeInsertSQL(t, "enqueueWorkRun")
	if len(workRunSQL) != 1 || !strings.Contains(workRunSQL[0], "values ('work_run', $1::jsonb, 3, 'work_run')") {
		t.Fatalf("enqueueWorkRun SQL %q", workRunSQL)
	}
	deliverSQL := nodeInsertSQL(t, "enqueueChannelDeliver")
	if len(deliverSQL) != 3 || !strings.Contains(deliverSQL[2], "values ('channel_deliver', $1::jsonb, 9, 'default')") ||
		!strings.Contains(deliverSQL[0], "hashtext('channel_deliver:' || $1)") || !strings.Contains(deliverSQL[1], "state not in ('cancelled', 'discarded')") {
		t.Fatalf("enqueueChannelDeliver SQL %q", deliverSQL)
	}
	schema := func(s string) string { return strings.ReplaceAll(s, "river.", q.Schema+".") }
	if _, err := pool.Exec(ctx, schema(workRunSQL[0]), `{"processingJobId":"pj","orgId":"org","runId":"run","threadId":"th","message":"hi",
		"channel":"telegram","channelPlugin":"telegram","recipient":{"chatId":7},"sender":{"id":"u1"},"queuedAt":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, schema(deliverSQL[2]), `{"orgId":"org","channelPlugin":"slack","recipient":{"channel":"C1"},"events":[{"kind":"converse"}],"key":"output-o1-slack"}`); err != nil {
		t.Fatal(err)
	}
	runs := make(chan WorkRunArgs, 1)
	sends := make(chan ChannelDeliverArgs, 1)
	q.WorkRun = func(_ context.Context, a WorkRunArgs) error { runs <- a; return nil }
	q.ChannelDeliver = func(_ context.Context, a ChannelDeliverArgs) error { sends <- a; return nil }
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	select {
	case a := <-runs:
		if a.ProcessingJobID != "pj" || a.OrgID != "org" || a.RunID != "run" || a.ThreadID != "th" || a.Message != "hi" || a.Channel != "telegram" ||
			a.ChannelPlugin != "telegram" || string(a.Recipient) != `{"chatId": 7}` || string(a.Sender) != `{"id": "u1"}` || a.QueuedAt == nil {
			t.Errorf("work run args %+v", a)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the work run did not run")
	}
	select {
	case a := <-sends:
		if a.OrgID != "org" || a.ChannelPlugin != "slack" || string(a.Recipient) != `{"channel": "C1"}` || string(a.Events) != `[{"kind": "converse"}]` || a.Key != "output-o1-slack" {
			t.Errorf("delivery args %+v", a)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the delivery did not run")
	}
}

func TestChannelDeliverAndSkillLearnInserts(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ins, err := NewInserter(pool, q.Schema)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(key string) bool {
		t.Helper()
		var added bool
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			added, err = ins.ChannelDeliver(ctx, tx, ChannelDeliverArgs{OrgID: "org", ChannelPlugin: "telegram",
				Recipient: json.RawMessage(`{"chatId":7}`), Events: json.RawMessage(`[]`), Key: key})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return added
	}
	if !deliver("reply-r1") || deliver("reply-r1") || !deliver("reply-r2") || !deliver("") || !deliver("") {
		t.Error("a key did not hold back its second delivery, or a keyless one was held back")
	}
	if _, err := pool.Exec(ctx, `update `+q.Schema+`.river_job set state = 'completed', finalized_at = now() where args->>'key' = 'reply-r1'`); err != nil {
		t.Fatal(err)
	}
	if deliver("reply-r1") {
		t.Error("a sent reply was queued again")
	}
	if _, err := pool.Exec(ctx, `update `+q.Schema+`.river_job set state = 'discarded', finalized_at = now() where args->>'key' = 'reply-r2'`); err != nil {
		t.Fatal(err)
	}
	if !deliver("reply-r2") {
		t.Error("a reply whose delivery gave up was not queued again")
	}
	learn := func(skill string) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return ins.SkillLearn(ctx, tx, "org", skill) }); err != nil {
			t.Fatal(err)
		}
	}
	learn("refunds")
	learn("refunds")
	learn("returns")
	var n, attempts int
	if err := pool.QueryRow(ctx, `select count(*), min(max_attempts) from `+q.Schema+`.river_job where kind = $1`, KindSkillLearn).Scan(&n, &attempts); err != nil {
		t.Fatal(err)
	}
	if n != 2 || attempts != SkillLearnAttempts {
		t.Errorf("%d skill_learn jobs with %d attempts, want 2 with %d", n, attempts, SkillLearnAttempts)
	}
}
