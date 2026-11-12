package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	KindDirectorySync    = "directory_sync"
	DirectorySyncTimeout = 15 * time.Minute
)

type DirectorySyncArgs struct{}

func (DirectorySyncArgs) Kind() string { return KindDirectorySync }

func (DirectorySyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	}}}
}

type directorySync struct {
	river.WorkerDefaults[DirectorySyncArgs]
	run func(context.Context) error
}

func (w *directorySync) Work(ctx context.Context, _ *river.Job[DirectorySyncArgs]) error {
	if w.run == nil {
		return errors.New("the directory sync has no runner")
	}
	return w.run(ctx)
}

func (w *directorySync) Timeout(*river.Job[DirectorySyncArgs]) time.Duration {
	return DirectorySyncTimeout
}

// SixHourly fires at 00, 06, 12 and 18 UTC, as the cron "0 */6 * * *" did.
type SixHourly struct{}

func (SixHourly) Next(t time.Time) time.Time {
	return t.UTC().Truncate(6 * time.Hour).Add(6 * time.Hour)
}
