package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

type wakeCounts struct{ metric, cron, directory, action atomic.Int32 }

func wakeQueue(t *testing.T, action func(context.Context) error) (*Queue, *wakeCounts) {
	t.Helper()
	ctx := context.Background()
	pool := testpg.Migrated(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	c := &wakeCounts{}
	q.MetricSweep = func(context.Context) error { c.metric.Add(1); return nil }
	q.CronSweep = func(context.Context) error { c.cron.Add(1); return nil }
	q.DirectorySync = func(context.Context) error { c.directory.Add(1); return nil }
	q.ActionExecute = func(ctx context.Context, _, _ string) error {
		c.action.Add(1)
		if action != nil {
			return action(ctx)
		}
		return nil
	}
	return q, c
}

func queueAction(t *testing.T, q *Queue) {
	t.Helper()
	client, err := river.NewClient(riverpgxv5.New(q.Pool), &river.Config{Schema: q.Schema})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(context.Background(), ActionExecuteArgs{OrgID: "org-1", ActionRequestID: "ar-1"}, nil); err != nil {
		t.Fatal(err)
	}
}

func jobStates(t *testing.T, q *Queue) map[string]string {
	t.Helper()
	rows, err := q.Pool.Query(context.Background(), `select kind, state::text from `+q.Schema+`.river_job`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var kind, state string
		if err := rows.Scan(&kind, &state); err != nil {
			t.Fatal(err)
		}
		got[kind] += state + " "
	}
	return got
}

func TestWakeRunsQueuedAndDueJobs(t *testing.T) {
	ctx := context.Background()
	q, c := wakeQueue(t, nil)
	queueAction(t, q)
	res, err := q.Wake(ctx, 30*time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != len(cronJobs) || res.Ran != len(cronJobs)+1 || res.TimedOut {
		t.Fatalf("first wake = %+v, want %d inserted and %d ran", res, len(cronJobs), len(cronJobs)+1)
	}
	if c.metric.Load() != 1 || c.cron.Load() != 1 || c.directory.Load() != 1 || c.action.Load() != 1 {
		t.Fatalf("runs: metric %d cron %d directory %d action %d, want 1 each", c.metric.Load(), c.cron.Load(), c.directory.Load(), c.action.Load())
	}
	for kind, states := range jobStates(t, q) {
		if states != "completed " {
			t.Errorf("%s: states %q, want one completed job", kind, states)
		}
	}
	res, err = q.Wake(ctx, 30*time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted > 2 {
		t.Fatalf("second wake inserted %d jobs, want none outside a new minute", res.Inserted)
	}
	if res.Ran != res.Inserted {
		t.Fatalf("second wake = %+v, want it to run only what it inserted", res)
	}
}

func TestWakeWithJobsOnOnlyInserts(t *testing.T) {
	ctx := context.Background()
	q, c := wakeQueue(t, nil)
	queueAction(t, q)
	res, err := q.Wake(ctx, 30*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != len(cronJobs) || res.Ran != 0 {
		t.Fatalf("wake = %+v, want %d inserted and none ran", res, len(cronJobs))
	}
	if c.action.Load() != 0 || c.cron.Load() != 0 {
		t.Fatal("a wake on a web with jobs on ran a job")
	}
	if got := jobStates(t, q)[KindActionExecute]; got != "available " {
		t.Fatalf("the queued action is %q, want available", got)
	}
}

func TestWakeTimeLimit(t *testing.T) {
	ctx := context.Background()
	q, c := wakeQueue(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	queueAction(t, q)
	start := time.Now()
	res, err := q.Wake(ctx, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second+wakeStopGrace {
		t.Fatalf("wake took %s with a 1 s limit", took)
	}
	if !res.TimedOut {
		t.Fatalf("wake = %+v, want timedOut", res)
	}
	if c.action.Load() != 1 {
		t.Fatalf("the action ran %d times, want 1", c.action.Load())
	}
	if got := jobStates(t, q)[KindActionExecute]; got != "available " && got != "retryable " {
		t.Fatalf("the blocked action is %q, want it back in the queue", got)
	}
}

func TestInsertDueOncePerPeriod(t *testing.T) {
	ctx := context.Background()
	q, _ := wakeQueue(t, nil)
	finish := func() {
		if _, err := q.Pool.Exec(ctx, `update `+q.Schema+`.river_job set state = 'completed', finalized_at = now()`); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 11, 12, 10, 0, 10, 0, time.UTC)
	for _, step := range []struct {
		at   time.Time
		want int
	}{
		{t0, len(cronJobs)},
		{t0.Add(40 * time.Second), 0},
		{t0.Add(time.Minute), 2},
		{t0.Add(time.Hour), 4},
		{t0.Add(2 * time.Hour), 5},
		{t0.Add(2*time.Hour + 30*time.Minute), 2},
	} {
		n, err := q.InsertDue(ctx, step.at)
		if err != nil {
			t.Fatal(err)
		}
		if n != step.want {
			t.Fatalf("InsertDue at %s = %d, want %d", step.at.Format(time.TimeOnly), n, step.want)
		}
		finish()
	}
	if _, err := q.InsertDue(ctx, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := q.InsertDue(ctx, t0.Add(3*time.Hour+time.Minute)); err != nil || n != 0 {
		t.Fatalf("InsertDue with the last minute's sweeps unfinished = %d, %v; want 0, as their unique options hold", n, err)
	}
}

func TestInsertDueSkipsTheLeadersRun(t *testing.T) {
	ctx := context.Background()
	q, _ := wakeQueue(t, nil)
	boundary := time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	if _, err := q.Pool.Exec(ctx, `insert into `+q.Schema+`.river_job (kind, args, max_attempts, state, scheduled_at, finalized_at)
values ($1, '{}', 2, 'completed', $2, $2)`, KindOutputSweep, boundary); err != nil {
		t.Fatal(err)
	}
	n, err := q.InsertDue(ctx, boundary.Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(cronJobs)-1 {
		t.Fatalf("InsertDue = %d, want every cron job but the output sweep the leader ran", n)
	}
}

func TestCronPeriodsMatchSchedules(t *testing.T) {
	for _, c := range cronJobs {
		if got := c.schedule.Next(time.Unix(0, 0)).Sub(time.Unix(0, 0)); got != c.period {
			t.Errorf("%s: period %s, schedule step %s", c.args.Kind(), c.period, got)
		}
	}
	if len(periodicJobs()) != len(cronJobs) {
		t.Fatal("periodicJobs does not list every cron job")
	}
}
