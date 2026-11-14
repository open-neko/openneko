package jobs

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestRunnerRunsSSOSetupPoll(t *testing.T) {
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
	q.SSOSetupPoll = func(context.Context) error {
		started <- struct{}{}
		<-release
		return fmt.Errorf("planted failure")
	}
	if _, err := inserter.Insert(ctx, SSOSetupPollArgs{}, nil); err != nil {
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
		t.Fatal("the SSO setup poll did not start in 20 s")
	}
	res, err := inserter.Insert(ctx, SSOSetupPollArgs{}, nil)
	if err != nil || !res.UniqueSkippedAsDuplicate {
		t.Fatalf("a second poll was queued while the first one runs: %+v %v", res, err)
	}
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(20 * time.Second)
	for {
		var s string
		var at time.Time
		_ = pool.QueryRow(ctx, `select state::text, scheduled_at from `+q.Schema+`.river_job where kind = $1 order by id limit 1`, KindSSOSetupPoll).Scan(&s, &at)
		if s == "retryable" {
			if wait := time.Until(at); wait < 5*time.Second || wait > 16*time.Second {
				t.Fatalf("retry in %v, want 15 s", wait)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the SSO setup poll is %q after 20 s", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var jobs int
	_ = pool.QueryRow(ctx, `select count(*) from `+q.Schema+`.river_job where kind = $1`, KindSSOSetupPoll).Scan(&jobs)
	if jobs != 1 {
		t.Errorf("%d SSO setup poll jobs, want 1", jobs)
	}
}
