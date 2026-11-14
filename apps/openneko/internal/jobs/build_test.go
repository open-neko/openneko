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
