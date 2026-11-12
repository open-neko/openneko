package jobs

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestSixHourly(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	for _, c := range []struct{ in, want time.Time }{
		{time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 12, 17, 59, 59, 0, time.UTC), time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 12, 23, 30, 0, 0, time.UTC), time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 12, 10, 15, 0, 0, ist), time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)},
	} {
		if got := (SixHourly{}).Next(c.in); !got.Equal(c.want) {
			t.Errorf("Next(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDirectorySyncTimeout(t *testing.T) {
	if got := (&directorySync{}).Timeout(nil); got != 15*time.Minute {
		t.Fatalf("timeout = %v, want 15 min", got)
	}
}

func TestRunnerRunsDirectorySync(t *testing.T) {
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
	q.DirectorySync = func(context.Context) error {
		started <- struct{}{}
		<-release
		return fmt.Errorf("planted failure")
	}
	if _, err := inserter.Insert(ctx, DirectorySyncArgs{}, nil); err != nil {
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
		t.Fatal("the directory sync did not start in 20 s")
	}
	res, err := inserter.Insert(ctx, DirectorySyncArgs{}, nil)
	if err != nil || !res.UniqueSkippedAsDuplicate {
		t.Fatalf("a second sync was queued while the first one runs: %+v %v", res, err)
	}
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(20 * time.Second)
	for {
		var s string
		var attempts int
		_ = pool.QueryRow(ctx, `select state::text, max_attempts from `+q.Schema+`.river_job where kind = $1`, KindDirectorySync).Scan(&s, &attempts)
		if s == "discarded" {
			if attempts != 1 {
				t.Fatalf("max attempts %d, want 1", attempts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the failed directory sync is %q after 20 s, want discarded", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(started) != 0 {
		t.Fatal("the failed directory sync ran again")
	}
}

func TestMigrateMovesDirectorySync(t *testing.T) {
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
  ('directory_sync', '{}', 'created', 0, now() + interval '2 hours'),
  ('directory_sync', '{}', 'completed', 0, now());
insert into `+b+`.schedule values ('directory_sync');`); err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select kind || ' ' || queue || ' ' || state || ' ' || max_attempts from `+q.Schema+`.river_job`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"directory_sync default scheduled 1"}; !slices.Equal(got, want) {
		t.Fatalf("river jobs = %q, want %q", got, want)
	}
	var left int
	if err := pool.QueryRow(ctx, `select (select count(*) from `+b+`.job where state <> 'completed') + (select count(*) from `+b+`.schedule)`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("pg-boss rows left: %d, %v", left, err)
	}
}
