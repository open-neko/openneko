package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const KindCronSweep = "workflow_cron_sweep"

type CronSweepArgs struct{}

func (CronSweepArgs) Kind() string { return KindCronSweep }

// A sweep that outlasts its minute must not overlap the next one: two sweeps
// would read the same due watchers.
func (CronSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	}}}
}

type cronSweep struct {
	river.WorkerDefaults[CronSweepArgs]
	run func(context.Context) error
}

func (w *cronSweep) Work(ctx context.Context, _ *river.Job[CronSweepArgs]) error {
	if w.run == nil {
		return errors.New("the cron sweep has no runner")
	}
	return w.run(ctx)
}

func (w *cronSweep) NextRetry(*river.Job[CronSweepArgs]) time.Time {
	return time.Now().Add(15 * time.Second)
}

// EveryMinute fires at the start of each UTC minute, as the cron "* * * * *" did.
type EveryMinute struct{}

func (EveryMinute) Next(t time.Time) time.Time {
	return t.UTC().Truncate(time.Minute).Add(time.Minute)
}
