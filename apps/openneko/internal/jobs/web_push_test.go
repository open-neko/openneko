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
