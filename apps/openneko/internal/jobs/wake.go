package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const (
	wakePoll      = 200 * time.Millisecond
	wakeStopGrace = 5 * time.Second
)

type WakeResult struct {
	Inserted int  `json:"inserted"`
	Ran      int  `json:"ran"`
	TimedOut bool `json:"timedOut"`
}

// InsertDue adds the run of each cron job for the period that holds now,
// unless the queue already holds a run of that kind from the period. A kind's
// own unique options still apply.
func (q *Queue) InsertDue(ctx context.Context, now time.Time) (int, error) {
	client, err := river.NewClient(riverpgxv5.New(q.Pool), &river.Config{Schema: q.schema(), Logger: quietLogger(),
		Test: river.TestConfig{Time: fixedTime{now}}})
	if err != nil {
		return 0, err
	}
	tx, err := q.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('jobs-wake'))`); err != nil {
		return 0, err
	}
	var params []river.InsertManyParams
	for _, c := range q.cronJobs() {
		var ran bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from `+pgx.Identifier{q.schema()}.Sanitize()+`.river_job
  where kind = $1 and scheduled_at >= $2)`, c.args.Kind(), now.UTC().Truncate(c.period)).Scan(&ran); err != nil {
			return 0, err
		}
		if !ran {
			params = append(params, river.InsertManyParams{Args: c.args})
		}
	}
	if len(params) == 0 {
		return 0, nil
	}
	res, err := client.InsertManyTx(ctx, tx, params)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range res {
		if !r.UniqueSkippedAsDuplicate {
			n++
		}
	}
	return n, tx.Commit(ctx)
}

// fixedTime dates the inserted jobs at now, so they are available at once
// and fall in now's period; an explicit ScheduledAt would insert them as
// scheduled.
type fixedTime struct{ t time.Time }

func (f fixedTime) Now() time.Time       { return f.t }
func (f fixedTime) NowOrNil() *time.Time { return &f.t }

// Wake inserts the due cron jobs. With work set it then runs the queues on a
// client of its own until no job is ready or limit passes; a job still
// running at the limit is cancelled and runs again later.
func (q *Queue) Wake(ctx context.Context, limit time.Duration, work bool) (WakeResult, error) {
	var res WakeResult
	inserted, err := q.InsertDue(ctx, time.Now())
	if err != nil {
		return res, err
	}
	res.Inserted = inserted
	if !work {
		return res, nil
	}
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	config := q.config()
	config.ID = "wake-" + hex.EncodeToString(id)
	client, err := river.NewClient(riverpgxv5.New(q.Pool), config)
	if err != nil {
		return res, err
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		return res, err
	}
	tick := time.NewTicker(wakePoll)
	defer tick.Stop()
	var waitErr error
wait:
	for {
		select {
		case <-runCtx.Done():
			res.TimedOut = true
			break wait
		case <-ctx.Done():
			waitErr = ctx.Err()
			break wait
		case <-tick.C:
		}
		busy, err := q.busy(runCtx, config.ID)
		switch {
		case runCtx.Err() != nil:
			res.TimedOut = true
			break wait
		case err != nil:
			waitErr = err
			break wait
		case !busy:
			break wait
		}
	}
	stop, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), wakeStopGrace)
	defer cancelStop()
	if res.TimedOut {
		_ = client.StopAndCancel(stop)
	} else if err := client.Stop(stop); err != nil {
		_ = client.StopAndCancel(stop)
	}
	if err := q.Pool.QueryRow(stop, `select count(*) from `+pgx.Identifier{q.schema()}.Sanitize()+`.river_job where $1 = any(attempted_by)`,
		config.ID).Scan(&res.Ran); err != nil && waitErr == nil {
		waitErr = err
	}
	return res, waitErr
}

func (q *Queue) busy(ctx context.Context, clientID string) (bool, error) {
	var busy bool
	err := q.Pool.QueryRow(ctx, `select exists (
  select 1 from `+pgx.Identifier{q.schema()}.Sanitize()+`.river_job
  where (state = 'available' and scheduled_at <= now())
     or (state in ('scheduled', 'retryable') and scheduled_at <= now())
     or (state = 'running' and $1 = any(attempted_by)))`, clientID).Scan(&busy)
	return busy, err
}
