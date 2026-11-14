package webfront

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

// RunBuild runs one setup build or metric refresh as the worker's makeHandler() did: the
// processing_job row goes to running, then to succeeded or failed with the
// error. A failed attempt that River retries goes back to running.
func (a *API) RunBuild(ctx context.Context, kind, orgID, jobID string) error {
	run := map[string]func(context.Context, string, string) error{
		jobs.KindBusinessProfileBuild:  a.buildBusinessProfile,
		jobs.KindIndustryInsightsBuild: a.buildIndustryInsights,
		jobs.KindBootstrapMetricsBuild: a.buildBootstrapMetrics,
		jobs.KindMetricRefresh:         a.refreshMetric,
	}[kind]
	if run == nil {
		return fmt.Errorf("unknown build job %s", kind)
	}
	if kind == jobs.KindMetricRefresh && !a.MetricsRefresh {
		return a.cancelMetricRefresh(ctx, jobID)
	}
	log.Printf("[jobs] running %s job=%s org=%s", kind, jobID, orgID)
	if _, err := a.DB.Exec(ctx, `update processing_job set status = 'running', started_at = now(), updated_at = now() where id = $1`, jobID); err != nil {
		return err
	}
	err := run(ctx, jobID, orgID)
	done := context.WithoutCancel(ctx)
	if err == nil {
		_, err = a.DB.Exec(done, `update processing_job set status = 'succeeded', finished_at = now(), error = null, updated_at = now() where id = $1`, jobID)
		if err == nil {
			log.Printf("[jobs] job %s succeeded", jobID)
		}
		return err
	}
	if _, merr := a.DB.Exec(done, `update processing_job set status = 'failed', finished_at = now(), error = $2, updated_at = now() where id = $1`, jobID, err.Error()); merr != nil {
		log.Printf("[jobs] could not mark job %s failed: %v", jobID, merr)
	}
	var upstream upstreamProviderError
	if errors.As(err, &upstream) {
		log.Printf("[jobs] job %s upstream provider unavailable; no retry: %v", jobID, err)
		return fmt.Errorf("%w: %w", jobs.ErrNoRetry, err)
	}
	log.Printf("[jobs] job %s attempt failed; River may retry: %v", jobID, err)
	return err
}

const metricsOffError = "the metric refresh is off (OPENNEKO_METRICS_REFRESH)"

func (a *API) cancelMetricRefresh(ctx context.Context, jobID string) error {
	log.Printf("[jobs] metric_refresh job=%s cancelled: %s", jobID, metricsOffError)
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update processing_job set status = 'cancelled', finished_at = now(), error = $2, updated_at = now() where id = $1`,
			jobID, metricsOffError); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update metric set last_refresh_status = null, updated_at = now()
			where last_refresh_job_id::text = $1 and last_refresh_status = 'pending'`, jobID)
		return err
	})
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", jobs.ErrNoRetry, metricsOffError)
}

func (a *API) jobProgress(ctx context.Context, jobID, message string) {
	if _, err := a.DB.Exec(ctx, `update processing_job set progress = coalesce(progress, '{}'::jsonb) || jsonb_build_object('message', $2::text),
		updated_at = now() where id = $1`, jobID, message); err != nil {
		log.Printf("[jobs] could not save the progress of job %s: %v", jobID, err)
	}
}

// queueBuildJob writes the processing_job row and queues its River job in
// the caller's transaction.
func (a *API) queueBuildJob(ctx context.Context, tx pgx.Tx, kind, orgID, trigger string) (string, error) {
	return a.queueBuildJobWith(ctx, tx, kind, orgID, trigger, jobs.BuildAttempts)
}

func (a *API) queueBuildJobWith(ctx context.Context, tx pgx.Tx, kind, orgID, trigger string, attempts int) (string, error) {
	if a.Jobs == nil {
		return "", errors.New("the job queue is not ready")
	}
	var id string
	if err := tx.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger) values ($1, $2, 'queued', $3) returning id::text`,
		orgID, kind, trigger).Scan(&id); err != nil {
		return "", err
	}
	return id, a.Jobs.Build(ctx, tx, kind, orgID, id, attempts)
}
