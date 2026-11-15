package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const (
	KindWebPush       = "web_push"
	WebPushAttempts   = 4
	WebPushRetryDelay = 15 * time.Second
	WebPushTimeout    = time.Minute
)

var webPushTimeout = WebPushTimeout

// WebPushArgs carries the event kind and recipient user IDs for a Web Push
// notice. It holds no business data from the run.
type WebPushArgs struct {
	OrgID         string   `json:"orgId"`
	Event         string   `json:"event"`
	Users         []string `json:"users,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty"`
	Retry         int      `json:"retry,omitempty"`
}

// WebPushRetryError names the subscriptions that failed. The worker retries
// only those, so a device that took the notice does not get it again.
type WebPushRetryError struct {
	Subscriptions []string
	Err           error
}

func (e *WebPushRetryError) Error() string { return e.Err.Error() }
func (e *WebPushRetryError) Unwrap() error { return e.Err }

func (WebPushArgs) Kind() string { return KindWebPush }

func (WebPushArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: WebPushAttempts}
}

type webPush struct {
	river.WorkerDefaults[WebPushArgs]
	run func(ctx context.Context, args WebPushArgs) error
}

func (w *webPush) Work(ctx context.Context, job *river.Job[WebPushArgs]) error {
	if w.run == nil {
		return errors.New("webPush: no runner")
	}
	if job.Args.OrgID == "" || job.Args.Event == "" {
		return river.JobCancel(errors.New("web_push job missing orgId or event"))
	}
	err := w.run(ctx, job.Args)
	var failed *WebPushRetryError
	if !errors.As(err, &failed) || len(failed.Subscriptions) == 0 || job.Attempt >= job.MaxAttempts {
		return err
	}
	client, cerr := river.ClientFromContextSafely[pgx.Tx](ctx)
	if cerr != nil {
		return err
	}
	next := WebPushArgs{
		OrgID:         job.Args.OrgID,
		Event:         job.Args.Event,
		Subscriptions: failed.Subscriptions,
		Retry:         job.Args.Retry + job.Attempt,
	}
	insertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, ierr := client.Insert(insertCtx, next, &river.InsertOpts{
		MaxAttempts: job.MaxAttempts - job.Attempt,
		ScheduledAt: w.NextRetry(job),
	}); ierr != nil {
		return err
	}
	return nil
}

func (w *webPush) Timeout(*river.Job[WebPushArgs]) time.Duration {
	return webPushTimeout
}

func (w *webPush) NextRetry(job *river.Job[WebPushArgs]) time.Time {
	return time.Now().Add(WebPushRetryDelay << min(max(job.Args.Retry+job.Attempt-1, 0), 8))
}

// WebPush queues one Web Push delivery job on River.
func (i *Inserter) WebPush(ctx context.Context, orgID, event string, users []string) error {
	_, err := i.client.Insert(ctx, WebPushArgs{OrgID: orgID, Event: event, Users: users}, nil)
	return err
}

// WebPushTx queues one Web Push delivery job in the caller's transaction.
func (i *Inserter) WebPushTx(ctx context.Context, tx pgx.Tx, orgID, event string, users []string) error {
	_, err := i.client.InsertTx(ctx, tx, WebPushArgs{OrgID: orgID, Event: event, Users: users}, nil)
	return err
}
