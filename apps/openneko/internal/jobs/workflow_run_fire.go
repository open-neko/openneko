package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// The pg-boss send options of the queue: retry limit 2 and retry delay 30 s,
// with backoff for cron firings. The worker ran one workflow at a time.
// enqueueWorkflowRunFire in packages/db/src/jobs.ts writes the same queue,
// max_attempts and dedupe value.
const (
	KindWorkflowRunFire       = "workflow_run_fire"
	QueueWorkflowRun          = "workflow_run"
	WorkflowRunFireAttempts   = 3
	WorkflowRunFireRetryDelay = 30 * time.Second
)

// WorkflowRunFireArgs is WorkflowRunFirePayload in packages/db/src/jobs.ts.
// Dedupe is the pg-boss singleton key with its time slot.
type WorkflowRunFireArgs struct {
	QueuedAt                  *float64        `json:"queuedAt,omitempty"`
	OrgID                     string          `json:"orgId"`
	WorkflowID                string          `json:"workflowId"`
	TriggerKind               string          `json:"triggerKind"`
	ScheduleFiringID          string          `json:"scheduleFiringId,omitempty"`
	TriggerPayload            json.RawMessage `json:"triggerPayload,omitempty"`
	UserMessage               *string         `json:"userMessage,omitempty"`
	ThreadID                  string          `json:"threadId,omitempty"`
	ParentChainDepth          int             `json:"parentChainDepth,omitempty"`
	TriggeredBySubscriptionID string          `json:"triggeredBySubscriptionId,omitempty"`
	TriggeredByOutputID       string          `json:"triggeredByOutputId,omitempty"`
	TriggeredByObservationID  string          `json:"triggeredByObservationId,omitempty"`
	Dedupe                    string          `json:"dedupe,omitempty"`
}

func (WorkflowRunFireArgs) Kind() string { return KindWorkflowRunFire }

func (WorkflowRunFireArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: WorkflowRunFireAttempts, Queue: QueueWorkflowRun}
}

type WorkflowRunFireFunc func(ctx context.Context, args WorkflowRunFireArgs, attempt int) error

type workflowRunFire struct {
	river.WorkerDefaults[WorkflowRunFireArgs]
	run WorkflowRunFireFunc
}

func (w *workflowRunFire) Work(ctx context.Context, job *river.Job[WorkflowRunFireArgs]) error {
	if w.run == nil {
		return errors.New("the workflow run has no runner")
	}
	a := job.Args
	if a.OrgID == "" || a.WorkflowID == "" {
		return river.JobCancel(errors.New("the job names no org or workflow"))
	}
	switch a.TriggerKind {
	case "manual", "cron", "subscription", "watcher":
	default:
		return river.JobCancel(errors.New("the job has trigger kind " + strconv.Quote(a.TriggerKind) + ", which this queue does not run"))
	}
	return w.run(ctx, a, job.Attempt)
}

// Timeout is off: the workflow turn has its own time limit and continuations.
func (w *workflowRunFire) Timeout(*river.Job[WorkflowRunFireArgs]) time.Duration { return -1 }

func (w *workflowRunFire) NextRetry(job *river.Job[WorkflowRunFireArgs]) time.Time {
	delay := WorkflowRunFireRetryDelay
	if job.Args.TriggerKind == "cron" {
		delay <<= max(job.Attempt-1, 0)
	}
	return time.Now().Add(delay)
}

// WorkflowRunFire queues one workflow run in the caller's transaction. With
// a dedupe key, it adds no job when a job with the key exists in the same
// window of the given length, as a pg-boss singleton did. It returns the
// job id, or "" for a duplicate.
func (i *Inserter) WorkflowRunFire(ctx context.Context, tx pgx.Tx, args WorkflowRunFireArgs, dedupeKey string, window time.Duration) (string, error) {
	if dedupeKey != "" {
		seconds := max(int64(window/time.Second), 1)
		if err := tx.QueryRow(ctx, `select $1 || '@' || (floor(extract(epoch from now()) / $2)::bigint * $2)::text`,
			dedupeKey, seconds).Scan(&args.Dedupe); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('workflow_run_fire:' || $1))`, args.Dedupe); err != nil {
			return "", err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from `+pgx.Identifier{i.schema}.Sanitize()+`.river_job
			where kind = $1 and args->>'dedupe' = $2 and state <> 'cancelled')`, KindWorkflowRunFire, args.Dedupe).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			return "", nil
		}
	}
	if args.QueuedAt == nil {
		ms := float64(time.Now().UnixMilli())
		args.QueuedAt = &ms
	}
	res, err := i.client.InsertTx(ctx, tx, args, nil)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(res.Job.ID, 10), nil
}
