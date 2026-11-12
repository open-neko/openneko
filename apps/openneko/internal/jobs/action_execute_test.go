package jobs

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestActionExecuteOptions(t *testing.T) {
	w := &actionExecute{}
	if got := w.Timeout(nil); got != 15*time.Minute {
		t.Errorf("timeout = %v, want 15 min", got)
	}
	if got := time.Until(w.NextRetry(nil)); got < 29*time.Second || got > 30*time.Second {
		t.Errorf("retry in %v, want 30 s", got)
	}
	if got := (ActionExecuteArgs{}).InsertOpts().MaxAttempts; got != 3 {
		t.Errorf("max attempts = %d, want 3", got)
	}
}

func TestRunnerRunsActionExecute(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	inserter, err := NewInserter(pool, q.Schema)
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan [2]string, 4)
	q.ActionExecute = func(_ context.Context, orgID, id string) error {
		ran <- [2]string{orgID, id}
		if id == "ar-fail" {
			return errors.New("planted failure")
		}
		return nil
	}
	insert := func(args ActionExecuteArgs) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			_, err := inserter.client.InsertTx(ctx, tx, args, nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"ar-ok", "ar-fail"} {
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return inserter.ActionExecute(ctx, tx, "org", id) }); err != nil {
			t.Fatal(err)
		}
	}
	insert(ActionExecuteArgs{OrgID: "org"})
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runner.Stop(ctx) })

	want := map[string]string{"ar-ok": "completed 1", "ar-fail": "retryable 1", "": "cancelled 1"}
	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, err := pool.Query(ctx, `select coalesce(args->>'actionRequestId', ''), state::text || ' ' || attempt,
			extract(epoch from scheduled_at - coalesce(attempted_at, now())) from `+q.Schema+`.river_job where kind = $1`, KindActionExecute)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		var retryIn float64
		for rows.Next() {
			var id, state string
			var wait float64
			if err := rows.Scan(&id, &state, &wait); err != nil {
				t.Fatal(err)
			}
			got[id] = state
			if id == "ar-fail" {
				retryIn = wait
			}
		}
		if len(got) == len(want) && got["ar-ok"] == want["ar-ok"] && got["ar-fail"] == want["ar-fail"] && got[""] == want[""] {
			if retryIn < 25 || retryIn > 35 {
				t.Errorf("the failed action retries in %.1f s, want 30 s", retryIn)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs after 20 s: %v, want %v", got, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var calls [][2]string
	for len(ran) > 0 {
		calls = append(calls, <-ran)
	}
	slices.SortFunc(calls, func(a, b [2]string) int { return len(a[1]) - len(b[1]) })
	if want := [][2]string{{"org", "ar-ok"}, {"org", "ar-fail"}}; !slices.Equal(calls, want) {
		t.Errorf("runner calls = %v, want %v", calls, want)
	}
}

func TestInserterJoinsTheTransaction(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	inserter, err := NewInserter(pool, q.Schema)
	if err != nil {
		t.Fatal(err)
	}
	planted := errors.New("rolled back")
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := inserter.ActionExecute(ctx, tx, "org", "ar-1"); err != nil {
			return err
		}
		return planted
	}); !errors.Is(err, planted) {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from `+q.Schema+`.river_job`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("jobs after a rollback: %d, %v", n, err)
	}
}

func TestMigrateMovesActionExecute(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	b := q.BossSchema
	if _, err := pool.Exec(ctx, `
create schema `+b+`;
create table `+b+`.job (id uuid primary key default gen_random_uuid(), name text not null, data jsonb,
  state text not null, retry_limit int not null default 2, start_after timestamptz not null default now());
insert into `+b+`.job (name, data, state, retry_limit, start_after) values
  ('action_execute', '{"orgId":"org","actionRequestId":"ar-1"}', 'created', 2, now()),
  ('action_execute', '{"orgId":"org","actionRequestId":"ar-2"}', 'retry', 2, now() + interval '30 seconds'),
  ('action_execute', '{"orgId":"org","actionRequestId":"ar-3"}', 'completed', 2, now());`); err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select args->>'actionRequestId' || ' ' || queue || ' ' || state || ' ' || max_attempts
		from `+q.Schema+`.river_job where kind = 'action_execute' order by 1`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ar-1 default available 3", "ar-2 default scheduled 3"}; !slices.Equal(got, want) {
		t.Fatalf("river jobs = %q, want %q", got, want)
	}
}
