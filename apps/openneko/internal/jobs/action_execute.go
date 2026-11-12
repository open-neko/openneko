package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// The pg-boss send options of the queue: retry limit 2, retry delay 30 s
// and the 15-minute expiry. enqueueActionExecute in packages/db/src/jobs.ts
// writes the same max_attempts.
const (
	KindActionExecute       = "action_execute"
	ActionExecuteAttempts   = 3
	ActionExecuteRetryDelay = 30 * time.Second
	ActionExecuteTimeout    = 15 * time.Minute
)

type ActionExecuteArgs struct {
	OrgID           string `json:"orgId"`
	ActionRequestID string `json:"actionRequestId"`
}

func (ActionExecuteArgs) Kind() string { return KindActionExecute }

func (ActionExecuteArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: ActionExecuteAttempts}
}

type actionExecute struct {
	river.WorkerDefaults[ActionExecuteArgs]
	run func(ctx context.Context, orgID, actionRequestID string) error
}

func (w *actionExecute) Work(ctx context.Context, job *river.Job[ActionExecuteArgs]) error {
	if w.run == nil {
		return errors.New("the action execution has no runner")
	}
	if job.Args.OrgID == "" || job.Args.ActionRequestID == "" {
		return river.JobCancel(errors.New("the job names no org or action request"))
	}
	return w.run(ctx, job.Args.OrgID, job.Args.ActionRequestID)
}

func (w *actionExecute) Timeout(*river.Job[ActionExecuteArgs]) time.Duration {
	return ActionExecuteTimeout
}

func (w *actionExecute) NextRetry(*river.Job[ActionExecuteArgs]) time.Time {
	return time.Now().Add(ActionExecuteRetryDelay)
}

// Inserter adds jobs in the caller's transaction. It runs no job, so a web
// started with --jobs=false has one too.
type Inserter struct {
	client *river.Client[pgx.Tx]
}

func NewInserter(pool *pgxpool.Pool, schema string) (*Inserter, error) {
	if schema == "" {
		schema = DefaultSchema
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Schema: schema, Logger: quietLogger()})
	if err != nil {
		return nil, err
	}
	return &Inserter{client: client}, nil
}

func (i *Inserter) ActionExecute(ctx context.Context, tx pgx.Tx, orgID, actionRequestID string) error {
	_, err := i.client.InsertTx(ctx, tx, ActionExecuteArgs{OrgID: orgID, ActionRequestID: actionRequestID}, nil)
	return err
}
