package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func testQueue(t *testing.T, pool *pgxpool.Pool) *Queue {
	t.Helper()
	var schema string
	if err := pool.QueryRow(context.Background(), `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	q := &Queue{Pool: pool, Schema: schema + "_river", BossSchema: schema + "_boss", Logf: t.Logf}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "drop schema if exists "+q.Schema+" cascade; drop schema if exists "+q.BossSchema+" cascade") //nolint:errcheck
	})
	return q
}

func TestMigrateMovesPendingBossJobs(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	b := q.BossSchema
	if _, err := pool.Exec(ctx, `
create schema `+b+`;
create table `+b+`.job (id uuid primary key default gen_random_uuid(), name text not null, data jsonb,
  state text not null, retry_limit int not null default 2, start_after timestamptz not null default now());
create table `+b+`.schedule (name text primary key);
insert into `+b+`.job (name, data, state, retry_limit, start_after) values
  ('workflow_output_ttl_sweep', '{"a":1}', 'created', 1, now()),
  ('workflow_output_ttl_sweep', null, 'retry', 0, now() + interval '1 hour'),
  ('workflow_output_ttl_sweep', null, 'completed', 1, now()),
  ('work_run', '{"b":2}', 'created', 2, now());
insert into `+b+`.schedule values ('workflow_output_ttl_sweep'), ('work_run');`); err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select kind, args::text, max_attempts, state::text from `+q.Schema+`.river_job order by state`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var kind, args, state string
		var attempts int
		if err := rows.Scan(&kind, &args, &attempts, &state); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %s %s %d", kind, args, state, attempts))
	}
	want := []string{
		`workflow_output_ttl_sweep {"a": 1} available 2`,
		`workflow_output_ttl_sweep {} scheduled 1`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("river jobs = %q, want %q", got, want)
	}
	var left []string
	rows, err = pool.Query(ctx, `select s from (select name || ' ' || state as s from `+b+`.job union all select 'schedule ' || name from `+b+`.schedule) x order by s collate "C"`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		left = append(left, s)
	}
	if want := []string{"schedule work_run", "work_run created", "workflow_output_ttl_sweep completed"}; !slices.Equal(left, want) {
		t.Fatalf("pg-boss rows left = %q, want %q", left, want)
	}
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `select count(*) from `+q.Schema+`.river_job`).Scan(&n)
	if n != 2 {
		t.Fatalf("a second migrate left %d river jobs, want 2", n)
	}
}

func TestMigrateWithoutBoss(t *testing.T) {
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSweepsOutputs(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Migrated(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `
insert into organization (id, name) values ('org-1', 'x');
set session_replication_role = replica;
insert into workflow_output (org_id, workflow_run_id, work_run_id, kind, title, freshness_ttl_seconds, created_at) values
  ('org-1', gen_random_uuid(), gen_random_uuid(), 'finding', 'expired', 3600, now() - interval '2 hours'),
  ('org-1', gen_random_uuid(), gen_random_uuid(), 'finding', 'in grace', 3600, now() - interval '65 minutes'),
  ('org-1', gen_random_uuid(), gen_random_uuid(), 'finding', 'fresh', 3600, now()),
  ('org-1', gen_random_uuid(), gen_random_uuid(), 'finding', 'kept', null, now() - interval '30 days');
set session_replication_role = origin;`)
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into `+q.Schema+`.river_job (kind, args, max_attempts) values ($1, '{}', 2)`, KindOutputSweep); err != nil {
		t.Fatal(err)
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	deadline := time.Now().Add(20 * time.Second)
	for {
		var state string
		_ = pool.QueryRow(ctx, `select state::text from `+q.Schema+`.river_job where kind = $1 order by id limit 1`, KindOutputSweep).Scan(&state)
		if state == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sweep job is %q after 20 s", state)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var titles []string
	rows, _ := pool.Query(ctx, `select title from workflow_output order by title`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		titles = append(titles, s)
	}
	if want := []string{"fresh", "in grace", "kept"}; !slices.Equal(titles, want) {
		t.Fatalf("outputs left = %q, want %q", titles, want)
	}
}

func TestHourly(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	for _, c := range []struct{ in, want time.Time }{
		{time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 12, 9, 59, 59, 0, time.UTC), time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 12, 10, 15, 0, 0, ist), time.Date(2026, 11, 12, 5, 0, 0, 0, time.UTC)},
	} {
		if got := (Hourly{}).Next(c.in); !got.Equal(c.want) {
			t.Errorf("Next(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMovedMatchesTypeScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "db", "src", "jobs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)export const MOVED_QUEUES = \[(.*?)\] as const`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("MOVED_QUEUES not found in packages/db/src/jobs.ts")
	}
	var ts []string
	for _, q := range regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(m[1], -1) {
		ts = append(ts, string(q[1]))
	}
	queue := regexp.MustCompile(`(?s)export const QUEUE = \{(.*?)\} as const`).FindSubmatch(raw)
	for _, name := range Moved {
		if strings.Contains(string(queue[1]), `"`+name+`"`) {
			t.Errorf("%s is in QUEUE and in jobs.Moved", name)
		}
	}
	if !slices.Equal(ts, Moved) {
		t.Fatalf("MOVED_QUEUES = %q, jobs.Moved = %q", ts, Moved)
	}
}

func TestRunnerRunsMetricSweep(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 4)
	fail := true
	q.MetricSweep = func(context.Context) error {
		calls <- struct{}{}
		if fail {
			fail = false
			return fmt.Errorf("planted failure")
		}
		return nil
	}
	if _, err := pool.Exec(ctx, `insert into `+q.Schema+`.river_job (kind, args, max_attempts) values ($1, '{}', 3)`, KindMetricSweep); err != nil {
		t.Fatal(err)
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	state := func() (string, time.Time) {
		var s string
		var at time.Time
		_ = pool.QueryRow(ctx, `select state::text, scheduled_at from `+q.Schema+`.river_job where kind = $1`, KindMetricSweep).Scan(&s, &at)
		return s, at
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		s, at := state()
		if s == "retryable" {
			if wait := time.Until(at); wait < 20*time.Second || wait > 31*time.Second {
				t.Fatalf("retry in %v, want 30 s", wait)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the metric sweep is %q after 20 s", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := pool.Exec(ctx, `update `+q.Schema+`.river_job set state = 'available', scheduled_at = now() where kind = $1`, KindMetricSweep); err != nil {
		t.Fatal(err)
	}
	for {
		if s, _ := state(); s == "completed" {
			break
		}
		if time.Now().After(deadline.Add(10 * time.Second)) {
			t.Fatal("the metric sweep did not complete after the retry")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(calls) != 2 {
		t.Fatalf("the sweep ran %d times, want 2", len(calls))
	}
}

func TestEveryMinute(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	for _, c := range []struct{ in, want time.Time }{
		{time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 9, 1, 0, 0, time.UTC)},
		{time.Date(2026, 11, 12, 9, 59, 59, 0, time.UTC), time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 12, 10, 15, 30, 0, ist), time.Date(2026, 11, 12, 4, 46, 0, 0, time.UTC)},
	} {
		if got := (EveryMinute{}).Next(c.in); !got.Equal(c.want) {
			t.Errorf("Next(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRunnerRunsCronSweep(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	inserter, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Schema: q.Schema, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	q.CronSweep = func(context.Context) error {
		started <- struct{}{}
		<-release
		return fmt.Errorf("planted failure")
	}
	if _, err := inserter.Insert(ctx, CronSweepArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runner.Stop(ctx) })
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("the cron sweep did not start in 20 s")
	}
	res, err := inserter.Insert(ctx, CronSweepArgs{}, nil)
	if err != nil || !res.UniqueSkippedAsDuplicate {
		t.Fatalf("a second sweep was queued while the first one runs: %+v %v", res, err)
	}
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(20 * time.Second)
	for {
		var s string
		var at time.Time
		_ = pool.QueryRow(ctx, `select state::text, scheduled_at from `+q.Schema+`.river_job where kind = $1 order by id limit 1`, KindCronSweep).Scan(&s, &at)
		if s == "retryable" {
			if wait := time.Until(at); wait < 5*time.Second || wait > 16*time.Second {
				t.Fatalf("retry in %v, want 15 s", wait)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cron sweep is %q after 20 s", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var jobs int
	_ = pool.QueryRow(ctx, `select count(*) from `+q.Schema+`.river_job where kind = $1`, KindCronSweep).Scan(&jobs)
	if jobs != 1 {
		t.Errorf("%d cron sweep jobs, want 1", jobs)
	}
}
