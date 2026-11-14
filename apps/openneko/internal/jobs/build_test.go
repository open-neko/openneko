package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestBuildJobsRunOnRiver(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	q := testQueue(t, pool)
	b := q.BossSchema
	if _, err := pool.Exec(ctx, `
create schema `+b+`;
create table `+b+`.job (id uuid primary key default gen_random_uuid(), name text not null, data jsonb,
  state text not null, retry_limit int not null default 2, start_after timestamptz not null default now());
insert into `+b+`.job (name, data, state, retry_limit) values
  ('business_profile_build', '{"orgId":"org-1","processingJobId":"pj-boss"}', 'created', 0);`); err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var ran []string
	q.Build = func(_ context.Context, kind, orgID, jobID string) error {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, kind+" "+orgID+" "+jobID)
		switch jobID {
		case "pj-down":
			return fmt.Errorf("%w: provider down", ErrNoRetry)
		case "pj-flaky":
			return errors.New("flaky")
		}
		return nil
	}
	for _, j := range []struct{ kind, args string }{
		{KindIndustryInsightsBuild, `{"orgId":"org-1","processingJobId":"pj-ok"}`},
		{KindBootstrapMetricsBuild, `{"orgId":"org-1","processingJobId":"pj-down"}`},
		{KindIndustryInsightsBuild, `{"orgId":"org-1","processingJobId":"pj-flaky"}`},
		{KindBootstrapMetricsBuild, `{"orgId":"org-1"}`},
	} {
		if _, err := pool.Exec(ctx, `insert into `+q.Schema+`.river_job (kind, args, max_attempts) values ($1, $2, 3)`, j.kind, j.args); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	want := []string{
		"bootstrap_metrics_build {\"orgId\": \"org-1\"} cancelled 1",
		"bootstrap_metrics_build {\"orgId\": \"org-1\", \"processingJobId\": \"pj-down\"} cancelled 1",
		"business_profile_build {\"orgId\": \"org-1\", \"processingJobId\": \"pj-boss\"} completed 1",
		"industry_insights_build {\"orgId\": \"org-1\", \"processingJobId\": \"pj-flaky\"} retryable 1",
		"industry_insights_build {\"orgId\": \"org-1\", \"processingJobId\": \"pj-ok\"} completed 1",
	}
	var got []string
	for deadline := time.Now().Add(20 * time.Second); ; {
		got = nil
		rows, err := pool.Query(ctx, `select kind || ' ' || args::text || ' ' || state::text || ' ' || attempt from `+q.Schema+`.river_job where kind like '%_build' order by 1`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			got = append(got, s)
		}
		if slices.Equal(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("river jobs = %q, want %q", got, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 4 || !slices.Contains(ran, "business_profile_build org-1 pj-boss") {
		t.Errorf("ran %q", ran)
	}
}

func TestMetricRefreshRunsOnItsQueue(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	t.Setenv("OPENNEKO_METRIC_REFRESH_CONCURRENCY", "1")
	q := testQueue(t, pool)
	b := q.BossSchema
	if _, err := pool.Exec(ctx, `
create schema `+b+`;
create table `+b+`.job (id uuid primary key default gen_random_uuid(), name text not null, data jsonb,
  state text not null, retry_limit int not null default 2, start_after timestamptz not null default now());
insert into `+b+`.job (name, data, state) values
  ('metric_refresh', '{"orgId":"org-1","processingJobId":"pj-a","queuedAt":1}', 'created'),
  ('metric_refresh', '{"orgId":"org-1","processingJobId":"pj-b","queuedAt":1}', 'retry');`); err != nil {
		t.Fatal(err)
	}
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var moved string
	if err := pool.QueryRow(ctx, `select string_agg(queue || ' ' || max_attempts, ',') from `+q.Schema+`.river_job where kind = 'metric_refresh'`).Scan(&moved); err != nil {
		t.Fatal(err)
	}
	if moved != "metric_refresh 3,metric_refresh 3" {
		t.Errorf("moved jobs: %q", moved)
	}
	var mu sync.Mutex
	running, most := 0, 0
	var ran []string
	q.Build = func(_ context.Context, kind, orgID, jobID string) error {
		mu.Lock()
		running++
		most = max(most, running)
		ran = append(ran, kind+" "+jobID)
		mu.Unlock()
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	}
	runner, err := q.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Stop(ctx)
	for deadline := time.Now().Add(20 * time.Second); ; {
		var done int
		_ = pool.QueryRow(ctx, `select count(*) from `+q.Schema+`.river_job where kind = 'metric_refresh' and state = 'completed'`).Scan(&done)
		if done == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 2 refreshes completed", done)
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if most != 1 || len(ran) != 2 || !slices.Contains(ran, "metric_refresh pj-a") {
		t.Errorf("ran %q, at most %d at once", ran, most)
	}
}

func TestMetricRefreshWorkers(t *testing.T) {
	for env, want := range map[string]int{"": 2, "5": 5, "0": 2, "x": 2, " 3 ": 3} {
		t.Setenv("OPENNEKO_METRIC_REFRESH_CONCURRENCY", env)
		if got := MetricRefreshWorkers(); got != want {
			t.Errorf("%q: %d", env, got)
		}
	}
}
