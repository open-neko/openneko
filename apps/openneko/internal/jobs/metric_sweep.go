package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
)

const KindMetricSweep = "metric_refresh_scheduled_sweep"

type MetricSweepArgs struct{}

func (MetricSweepArgs) Kind() string { return KindMetricSweep }

func (MetricSweepArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 3} }

type metricSweep struct {
	river.WorkerDefaults[MetricSweepArgs]
	run func(context.Context) error
}

func (w *metricSweep) Work(ctx context.Context, _ *river.Job[MetricSweepArgs]) error {
	if w.run == nil {
		return errors.New("the metric sweep has no runner")
	}
	return w.run(ctx)
}

func (w *metricSweep) NextRetry(*river.Job[MetricSweepArgs]) time.Time {
	return time.Now().Add(30 * time.Second)
}
