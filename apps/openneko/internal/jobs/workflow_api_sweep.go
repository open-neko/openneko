package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// KindWorkflowAPISweep closes the API runs whose worker stopped, expires the
// kept results and queues the admissions that hold no job.
const KindWorkflowAPISweep = "workflow_api_sweep"

type WorkflowAPISweepArgs struct{}

func (WorkflowAPISweepArgs) Kind() string { return KindWorkflowAPISweep }

func (WorkflowAPISweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	}}}
}

type workflowAPISweep struct {
	river.WorkerDefaults[WorkflowAPISweepArgs]
	run func(context.Context) error
}

func (w *workflowAPISweep) Work(ctx context.Context, _ *river.Job[WorkflowAPISweepArgs]) error {
	if w.run == nil {
		return errors.New("the workflow API sweep has no runner")
	}
	return w.run(ctx)
}

func (w *workflowAPISweep) NextRetry(*river.Job[WorkflowAPISweepArgs]) time.Time {
	return time.Now().Add(15 * time.Second)
}
