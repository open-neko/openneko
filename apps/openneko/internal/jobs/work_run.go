package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// The pg-boss send options of the two queues: work_run with retry limit 2
// and retry delay 30 s, and channel_deliver with retry limit 8 and 15 s
// with backoff. The worker ran as many channel turns at once as the agent
// cap. enqueueWorkRun and enqueueChannelDeliver in packages/db/src/jobs.ts
// write the same queue and max_attempts.
const (
	KindWorkRun              = "work_run"
	QueueWorkRun             = "work_run"
	WorkRunAttempts          = 3
	WorkRunRetryDelay        = 30 * time.Second
	WorkRunWorkers           = 3
	KindChannelDeliver       = "channel_deliver"
	ChannelDeliverAttempts   = 9
	ChannelDeliverRetryDelay = 15 * time.Second
	ChannelDeliverTimeout    = 2 * time.Minute
)

// WorkRunArgs is WorkRunPayload in packages/db/src/jobs.ts.
type WorkRunArgs struct {
	QueuedAt        *float64        `json:"queuedAt,omitempty"`
	ProcessingJobID string          `json:"processingJobId"`
	OrgID           string          `json:"orgId"`
	RunID           string          `json:"runId"`
	ThreadID        string          `json:"threadId"`
	Message         string          `json:"message"`
	Channel         string          `json:"channel,omitempty"`
	ChannelPlugin   string          `json:"channelPlugin,omitempty"`
	Recipient       json.RawMessage `json:"recipient,omitempty"`
	Sender          json.RawMessage `json:"sender,omitempty"`
}

func (WorkRunArgs) Kind() string { return KindWorkRun }

func (WorkRunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: WorkRunAttempts, Queue: QueueWorkRun}
}

type workRun struct {
	river.WorkerDefaults[WorkRunArgs]
	run func(ctx context.Context, args WorkRunArgs) error
}

func (w *workRun) Work(ctx context.Context, job *river.Job[WorkRunArgs]) error {
	if w.run == nil {
		return errors.New("the work run has no runner")
	}
	a := job.Args
	if a.OrgID == "" || a.RunID == "" || a.ThreadID == "" {
		return river.JobCancel(errors.New("the job names no org, run or thread"))
	}
	return w.run(ctx, a)
}

// Timeout is off: the chat turn has its own time limit.
func (w *workRun) Timeout(*river.Job[WorkRunArgs]) time.Duration { return -1 }

func (w *workRun) NextRetry(*river.Job[WorkRunArgs]) time.Time {
	return time.Now().Add(WorkRunRetryDelay)
}

// ChannelDeliverArgs is ChannelDeliverPayload in packages/db/src/jobs.ts.
// Key is the pg-boss singleton key.
type ChannelDeliverArgs struct {
	OrgID         string          `json:"orgId"`
	ChannelPlugin string          `json:"channelPlugin"`
	Recipient     json.RawMessage `json:"recipient"`
	Events        json.RawMessage `json:"events"`
	Key           string          `json:"key,omitempty"`
}

func (ChannelDeliverArgs) Kind() string { return KindChannelDeliver }

func (ChannelDeliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: ChannelDeliverAttempts}
}

type channelDeliver struct {
	river.WorkerDefaults[ChannelDeliverArgs]
	run func(ctx context.Context, args ChannelDeliverArgs) error
}

func (w *channelDeliver) Work(ctx context.Context, job *river.Job[ChannelDeliverArgs]) error {
	if w.run == nil {
		return errors.New("the channel delivery has no runner")
	}
	if job.Args.ChannelPlugin == "" {
		return river.JobCancel(errors.New("the job names no channel"))
	}
	return w.run(ctx, job.Args)
}

func (w *channelDeliver) Timeout(*river.Job[ChannelDeliverArgs]) time.Duration {
	return ChannelDeliverTimeout
}

func (w *channelDeliver) NextRetry(job *river.Job[ChannelDeliverArgs]) time.Time {
	return time.Now().Add(ChannelDeliverRetryDelay << min(max(job.Attempt-1, 0), 10))
}

// ChannelDeliver queues one channel delivery in the caller's transaction.
// With a key, it adds no job while a job with the key is waiting, running
// or done. It returns false for a duplicate.
func (i *Inserter) ChannelDeliver(ctx context.Context, tx pgx.Tx, args ChannelDeliverArgs) (bool, error) {
	if args.Key != "" {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('channel_deliver:' || $1))`, args.Key); err != nil {
			return false, err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from `+pgx.Identifier{i.schema}.Sanitize()+`.river_job
			where kind = $1 and args->>'key' = $2 and state not in ('cancelled', 'discarded'))`, KindChannelDeliver, args.Key).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}
	_, err := i.client.InsertTx(ctx, tx, args, nil)
	return err == nil, err
}

// SkillLearn queues one skill learning pass unless one for the same org and
// skill waits, as enqueueSkillLearn in packages/db/src/jobs.ts does.
func (i *Inserter) SkillLearn(ctx context.Context, tx pgx.Tx, orgID, skillName string) error {
	args, err := json.Marshal(SkillLearnArgs{OrgID: orgID, SkillName: skillName})
	if err != nil {
		return err
	}
	schema := pgx.Identifier{i.schema}.Sanitize()
	_, err = tx.Exec(ctx, `insert into `+schema+`.river_job (kind, args, max_attempts, queue)
		select $1, $2::jsonb, $3, 'default'
		where not exists (select 1 from `+schema+`.river_job where kind = $1 and args = $2::jsonb
		  and state in ('available', 'pending', 'retryable', 'scheduled'))`, KindSkillLearn, string(args), SkillLearnAttempts)
	return err
}
