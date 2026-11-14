package jobs

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// The setup builds: each job carries the processing_job row that the
// onboarding and profile pages read. The worker's pg-boss queues sent them
// with retry limit 2 and retry delay 30 s; the onboarding submit sent the
// business profile with no retry.
const (
	KindBusinessProfileBuild  = "business_profile_build"
	KindIndustryInsightsBuild = "industry_insights_build"
	KindBootstrapMetricsBuild = "bootstrap_metrics_build"
	KindMetricRefresh         = "metric_refresh"
	QueueMetricRefresh        = "metric_refresh"
	BuildAttempts             = 3
	BuildRetryDelay           = 30 * time.Second
)

// buildTimeouts cover each job's own model limits: three profiler turns, two
// deep research calls of up to 20 minutes, one bootstrap turn, three metric
// agent turns.
var buildTimeouts = map[string]time.Duration{
	KindBusinessProfileBuild:  35 * time.Minute,
	KindIndustryInsightsBuild: 50 * time.Minute,
	KindBootstrapMetricsBuild: 15 * time.Minute,
	KindMetricRefresh:         30 * time.Minute,
}

// ErrNoRetry marks a build failure that a retry cannot fix, such as a model
// provider that is down or refuses the key.
var ErrNoRetry = errors.New("no retry")

type BuildArgs struct {
	ProcessingJobID string `json:"processingJobId"`
	OrgID           string `json:"orgId"`
}

type BusinessProfileBuildArgs BuildArgs

func (BusinessProfileBuildArgs) Kind() string { return KindBusinessProfileBuild }

type IndustryInsightsBuildArgs BuildArgs

func (IndustryInsightsBuildArgs) Kind() string { return KindIndustryInsightsBuild }

type BootstrapMetricsBuildArgs BuildArgs

func (BootstrapMetricsBuildArgs) Kind() string { return KindBootstrapMetricsBuild }

type MetricRefreshArgs BuildArgs

func (MetricRefreshArgs) Kind() string { return KindMetricRefresh }

func (MetricRefreshArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: BuildAttempts, Queue: QueueMetricRefresh}
}

type buildArgs interface {
	river.JobArgs
	BusinessProfileBuildArgs | IndustryInsightsBuildArgs | BootstrapMetricsBuildArgs | MetricRefreshArgs
}

// BuildFunc runs one build and keeps its processing_job row up to date.
type BuildFunc func(ctx context.Context, kind, orgID, processingJobID string) error

type buildWorker[T buildArgs] struct {
	river.WorkerDefaults[T]
	run BuildFunc
}

func (w *buildWorker[T]) Work(ctx context.Context, job *river.Job[T]) error {
	if w.run == nil {
		return errors.New("the setup builds have no runner")
	}
	args := BuildArgs(job.Args)
	if args.OrgID == "" || args.ProcessingJobID == "" {
		return river.JobCancel(errors.New("the job names no org or processing job"))
	}
	err := w.run(ctx, job.Kind, args.OrgID, args.ProcessingJobID)
	if errors.Is(err, ErrNoRetry) {
		return river.JobCancel(err)
	}
	return err
}

func (w *buildWorker[T]) Timeout(job *river.Job[T]) time.Duration { return buildTimeouts[job.Kind] }

func (w *buildWorker[T]) NextRetry(*river.Job[T]) time.Time { return time.Now().Add(BuildRetryDelay) }

func addBuildWorkers(workers *river.Workers, run BuildFunc) {
	river.AddWorker(workers, &buildWorker[BusinessProfileBuildArgs]{run: run})
	river.AddWorker(workers, &buildWorker[IndustryInsightsBuildArgs]{run: run})
	river.AddWorker(workers, &buildWorker[BootstrapMetricsBuildArgs]{run: run})
	river.AddWorker(workers, &buildWorker[MetricRefreshArgs]{run: run})
}

// MetricRefreshWorkers is metricRefreshConcurrency() of the worker:
// OPENNEKO_METRIC_REFRESH_CONCURRENCY, else 2. The agent slots bound all
// turns of the process.
func MetricRefreshWorkers() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("OPENNEKO_METRIC_REFRESH_CONCURRENCY"))); err == nil && n >= 1 {
		return n
	}
	return 2
}

// Build queues one setup build in the caller's transaction.
func (i *Inserter) Build(ctx context.Context, tx pgx.Tx, kind, orgID, processingJobID string, attempts int) error {
	a := BuildArgs{ProcessingJobID: processingJobID, OrgID: orgID}
	var args river.JobArgs
	switch kind {
	case KindBusinessProfileBuild:
		args = BusinessProfileBuildArgs(a)
	case KindIndustryInsightsBuild:
		args = IndustryInsightsBuildArgs(a)
	case KindBootstrapMetricsBuild:
		args = BootstrapMetricsBuildArgs(a)
	case KindMetricRefresh:
		args = MetricRefreshArgs(a)
	default:
		return errors.New("unknown build job " + kind)
	}
	opts := &river.InsertOpts{MaxAttempts: attempts}
	if kind == KindMetricRefresh {
		opts.Queue = QueueMetricRefresh
	}
	_, err := i.client.InsertTx(ctx, tx, args, opts)
	return err
}
