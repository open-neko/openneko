package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestWebPushWorkerAndInserter(t *testing.T) {
	if o := (WebPushArgs{}).InsertOpts(); o.MaxAttempts != 4 || o.Queue != "" {
		t.Fatalf("WebPushArgs opts = %+v", o)
	}
	w := &webPush{}
	job := func(attempt int, args WebPushArgs) *river.Job[WebPushArgs] {
		return &river.Job[WebPushArgs]{JobRow: &rivertype.JobRow{Attempt: attempt}, Args: args}
	}
	if d := w.Timeout(job(1, WebPushArgs{})); d != time.Minute {
		t.Fatalf("timeout = %v, want 1m", d)
	}
	if wait := time.Until(w.NextRetry(job(2, WebPushArgs{}))); wait < 25*time.Second || wait > 35*time.Second {
		t.Fatalf("retry 2 wait = %v, want ~30s", wait)
	}
	if err := w.Work(context.Background(), job(1, WebPushArgs{OrgID: "org", Event: "run_finished"})); err == nil {
		t.Fatal("expected error when runner is nil")
	}
	var cancel *river.JobCancelError
	w.run = func(context.Context, WebPushArgs) error { return nil }
	if err := w.Work(context.Background(), job(1, WebPushArgs{})); !errors.As(err, &cancel) {
		t.Fatalf("expected JobCancelError on empty args, got %v", err)
	}

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
	got := make(chan WebPushArgs, 1)
	q.WebPush = func(_ context.Context, args WebPushArgs) error {
		got <- args
		return nil
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)

	if err := ins.WebPush(ctx, "org-1", "approval_waiting", []string{"u-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case args := <-got:
		if args.OrgID != "org-1" || args.Event != "approval_waiting" || len(args.Users) != 1 || args.Users[0] != "u-1" {
			t.Fatalf("got %+v", args)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("web_push job did not run within 20 s")
	}
}

func TestWebPushRetriesOnlyFailedSubscriptions(t *testing.T) {
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
	q.WebPush = func(_ context.Context, args WebPushArgs) error {
		if len(args.Subscriptions) > 0 {
			return nil
		}
		return &WebPushRetryError{Subscriptions: []string{"sub-2"}, Err: errors.New("web push endpoint returned HTTP 500")}
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	if err := ins.WebPush(ctx, "org-1", "approval_waiting", []string{"u-1", "u-2"}); err != nil {
		t.Fatal(err)
	}

	type row struct {
		State       string
		Attempt     int
		MaxAttempts int
		Args        WebPushArgs
		Wait        time.Duration
	}
	var rows []row
	deadline := time.Now().Add(20 * time.Second)
	for {
		rs, err := pool.Query(ctx, `select state, attempt, max_attempts, args, scheduled_at - now() from `+q.Schema+`.river_job where kind = $1 order by id`, KindWebPush)
		if err != nil {
			t.Fatal(err)
		}
		rows = rows[:0]
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.State, &r.Attempt, &r.MaxAttempts, &r.Args, &r.Wait); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, r)
		}
		rs.Close()
		if len(rows) == 2 && rows[0].State == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs after 20 s: %+v", rows)
		}
		time.Sleep(100 * time.Millisecond)
	}
	next := rows[1]
	if next.State != "scheduled" || next.MaxAttempts != WebPushAttempts-1 {
		t.Fatalf("follow-up job = %+v", next)
	}
	if len(next.Args.Users) != 0 || len(next.Args.Subscriptions) != 1 || next.Args.Subscriptions[0] != "sub-2" || next.Args.Retry != 1 {
		t.Fatalf("follow-up args = %+v, want only sub-2", next.Args)
	}
	if next.Wait < 10*time.Second || next.Wait > 20*time.Second {
		t.Fatalf("follow-up waits %v, want ~15s", next.Wait)
	}
	w := &webPush{}
	retry := &river.Job[WebPushArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: next.Args}
	if wait := time.Until(w.NextRetry(retry)); wait < 25*time.Second || wait > 35*time.Second {
		t.Fatalf("retry of the follow-up waits %v, want ~30s", wait)
	}
}
