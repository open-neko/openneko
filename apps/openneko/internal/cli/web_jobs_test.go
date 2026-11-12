package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestWebJobsSwitch(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Pool(t)
	var schema string
	if err := pool.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{}, 4)
	q := &jobs.Queue{Pool: pool, Schema: schema + "_river", BossSchema: schema + "_boss",
		CronSweep: func(context.Context) error { ran <- struct{}{}; return nil }}
	t.Cleanup(func() { pool.Exec(context.Background(), "drop schema if exists "+q.Schema+" cascade") }) //nolint:errcheck
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into `+q.Schema+`.river_job (kind, args, max_attempts) values ($1, '{}', 2)`, jobs.KindCronSweep); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENNEKO_WEB_JOBS", "false")
	if envBool("OPENNEKO_WEB_JOBS", true) {
		t.Fatal("OPENNEKO_WEB_JOBS=false left jobs on")
	}
	var out bytes.Buffer
	if r := startJobs(ctx, false, q, &out, &out); r != nil {
		t.Fatal("--jobs=false started a River client")
	}
	if !strings.Contains(out.String(), "background jobs are off (--jobs=false)") {
		t.Errorf("output %q", out.String())
	}
	time.Sleep(3 * time.Second)
	state := func() string {
		var s string
		_ = pool.QueryRow(ctx, `select state::text from `+q.Schema+`.river_job where kind = $1`, jobs.KindCronSweep).Scan(&s)
		return s
	}
	if s := state(); s != "available" || len(ran) != 0 {
		t.Fatalf("with jobs off the queued sweep is %q and ran %d times", s, len(ran))
	}

	r := startJobs(ctx, true, q, &out, &out)
	if r == nil {
		t.Fatalf("jobs on: %s", out.String())
	}
	defer r.Stop(ctx)
	select {
	case <-ran:
	case <-time.After(20 * time.Second):
		t.Fatalf("with jobs on the sweep did not run; it is %q", state())
	}
}
