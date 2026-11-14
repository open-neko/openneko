package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestWorkflowRunFireDedupe(t *testing.T) {
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
	insert := func(args WorkflowRunFireArgs, key string, window time.Duration) string {
		t.Helper()
		var id string
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			id, err = ins.WorkflowRunFire(ctx, tx, args, key, window)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	args := WorkflowRunFireArgs{OrgID: "org", WorkflowID: "wf", TriggerKind: "watcher", TriggerPayload: json.RawMessage(`{"watcherId":"w1"}`)}
	first := insert(args, "watcher:w1", time.Hour)
	if first == "" {
		t.Fatal("the first fire was not queued")
	}
	if again := insert(args, "watcher:w1", time.Hour); again != "" {
		t.Errorf("a second fire in the window was queued as %s", again)
	}
	if other := insert(args, "watcher:w2", time.Hour); other == "" {
		t.Error("a fire with another key was not queued")
	}
	if plain := insert(WorkflowRunFireArgs{OrgID: "org", WorkflowID: "wf", TriggerKind: "cron"}, "", 0); plain == "" {
		t.Error("a fire with no key was not queued")
	}
	var raw, queue string
	var attempts int
	if err := pool.QueryRow(ctx, `select args::text, queue, max_attempts from `+q.Schema+`.river_job where id = $1::bigint`, first).
		Scan(&raw, &queue, &attempts); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	_ = json.Unmarshal([]byte(raw), &stored)
	dedupe, _ := stored["dedupe"].(string)
	queued, _ := stored["queuedAt"].(float64)
	if !strings.HasPrefix(dedupe, "watcher:w1@") || queued == 0 || queue != QueueWorkflowRun || attempts != WorkflowRunFireAttempts {
		t.Errorf("stored %s on %s with %d attempts", raw, queue, attempts)
	}

	if _, err := pool.Exec(ctx, `update `+q.Schema+`.river_job set state = 'cancelled', finalized_at = now() where id = $1::bigint`, first); err != nil {
		t.Fatal(err)
	}
	if again := insert(args, "watcher:w1", time.Hour); again == "" {
		t.Error("a cancelled job still held the window")
	}
}

func TestWorkflowRunFireWorker(t *testing.T) {
	var got []WorkflowRunFireArgs
	w := &workflowRunFire{run: func(_ context.Context, args WorkflowRunFireArgs, attempt int) error {
		got = append(got, args)
		if attempt != 2 {
			t.Errorf("attempt %d", attempt)
		}
		return nil
	}}
	job := func(kind string) *river.Job[WorkflowRunFireArgs] {
		return &river.Job[WorkflowRunFireArgs]{JobRow: &rivertype.JobRow{Attempt: 2},
			Args: WorkflowRunFireArgs{OrgID: "org", WorkflowID: "wf", TriggerKind: kind}}
	}
	for _, kind := range []string{"cron", "subscription", "watcher", "manual"} {
		if err := w.Work(context.Background(), job(kind)); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	for _, kind := range []string{"api", ""} {
		var cancel *rivertype.JobCancelError
		if err := w.Work(context.Background(), job(kind)); !errors.As(err, &cancel) {
			t.Errorf("%q: %v, want a cancel", kind, err)
		}
	}
	if len(got) != 4 {
		t.Errorf("ran %d jobs", len(got))
	}
	if w.Timeout(job("cron")) != -1 {
		t.Error("the job has a River timeout")
	}
	cron, plain := time.Until(w.NextRetry(job("cron"))), time.Until(w.NextRetry(job("watcher")))
	if cron < 55*time.Second || cron > 61*time.Second || plain < 25*time.Second || plain > 31*time.Second {
		t.Errorf("retry in %v for cron, %v for a watcher", cron, plain)
	}
}

func TestRunnerRunsWorkflowRunFire(t *testing.T) {
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
	ran := make(chan WorkflowRunFireArgs, 2)
	q.WorkflowRunFire = func(_ context.Context, args WorkflowRunFireArgs, _ int) error {
		ran <- args
		return nil
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := ins.WorkflowRunFire(ctx, tx, WorkflowRunFireArgs{OrgID: "org", WorkflowID: "wf", TriggerKind: "subscription",
			TriggeredBySubscriptionID: "sub", TriggerPayload: json.RawMessage(`{"subscription_id":"sub"}`)}, "sub:out", time.Hour)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runner.Stop(ctx) })
	select {
	case args := <-ran:
		if args.TriggerKind != "subscription" || args.TriggeredBySubscriptionID != "sub" || string(args.TriggerPayload) != `{"subscription_id": "sub"}` {
			t.Errorf("ran %+v (%s)", args, args.TriggerPayload)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the workflow run did not start in 20 s")
	}
}
