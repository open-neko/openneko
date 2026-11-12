package jobs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const KindOutputSweep = "workflow_output_ttl_sweep"

// OutputGrace keeps an expired output this long, so that subscribers still
// read fresh matches.
const OutputGrace = 10 * time.Minute

type OutputSweepArgs struct{}

func (OutputSweepArgs) Kind() string { return KindOutputSweep }

func (OutputSweepArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 2} }

type outputSweep struct {
	river.WorkerDefaults[OutputSweepArgs]
	pool *pgxpool.Pool
	logf func(string, ...any)
}

func (w *outputSweep) Work(ctx context.Context, _ *river.Job[OutputSweepArgs]) error {
	n, err := SweepOutputs(ctx, w.pool, OutputGrace)
	if err != nil {
		return err
	}
	if n > 0 {
		w.logf("[workflow-output-ttl] deleted %d stale output(s) (grace %ds)", n, int(OutputGrace.Seconds()))
	}
	return nil
}

func (w *outputSweep) NextRetry(*river.Job[OutputSweepArgs]) time.Time {
	return time.Now().Add(time.Minute)
}

// SweepOutputs deletes the workflow outputs whose freshness ended more than
// grace ago. An output with no freshness TTL stays.
func SweepOutputs(ctx context.Context, pool *pgxpool.Pool, grace time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `
delete from workflow_output
where freshness_ttl_seconds is not null
  and created_at + freshness_ttl_seconds * interval '1 second' < now() - $1 * interval '1 second'`,
		grace.Seconds())
	return tag.RowsAffected(), err
}
