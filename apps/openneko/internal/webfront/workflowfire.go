package webfront

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

const workflowFiringRunLease = 30 * time.Minute

var errWorkflowRunInterrupted = errors.New("The workflow run stopped before it finished.")

// WorkflowRunFire is the workflow_run_fire job. An error return lets River
// try again; a cron firing then goes back to the scheduler, as it did on
// pg-boss.
func (a *API) WorkflowRunFire(ctx context.Context, job jobs.WorkflowRunFireArgs, attempt int) error {
	if job.TriggerKind == "api" {
		return a.workflowAPIRunFire(ctx, job)
	}
	firing := job.ScheduleFiringID
	if firing != "" {
		claimed, err := a.claimScheduleFiring(ctx, firing, job.OrgID, job.WorkflowID)
		if err != nil {
			return err
		}
		if !claimed {
			log.Printf("[workflow-run-fire] duplicate delivery ignored firing=%s", firing)
			return nil
		}
	}
	err := a.fireWorkflowRun(ctx, job)
	bg := context.WithoutCancel(ctx)
	var over *spendExceeded
	switch {
	case err == nil:
		if firing != "" {
			_, err = a.DB.Exec(bg, `update workflow_schedule_firing set status = 'completed', lease_until = null,
				completed_at = now(), last_error = null, updated_at = now() where id = $1`, firing)
		}
		return err
	case errors.As(err, &over) && errors.Is(err, errWorkflowNotStarted):
		if firing != "" {
			if _, err := a.DB.Exec(bg, `update workflow_schedule_firing set status = 'cancelled', workflow_run_id = null,
				lease_until = null, completed_at = now(), last_error = $2, updated_at = now()
				where id = $1 and status = 'running'`, firing, over.Error()); err != nil {
				return err
			}
		}
		log.Printf("[workflow-run-fire] workflow=%s not started: %v", job.WorkflowID, over)
		return nil
	}
	if firing != "" {
		if _, rerr := a.DB.Exec(bg, `update workflow_schedule_firing set status = 'pending', workflow_run_id = null,
			lease_until = null, available_at = now() + interval '15 seconds', last_error = $2, updated_at = now()
			where id = $1 and status = 'running'`, firing, err.Error()); rerr != nil {
			log.Printf("[workflow-run-fire] could not release firing=%s: %v", firing, rerr)
		}
	}
	log.Printf("[workflow-run-fire] workflow=%s attempt %d failed; River may retry: %v", job.WorkflowID, attempt, err)
	return err
}

var errWorkflowNotStarted = errors.New("workflow not started")

type notStarted struct{ cause error }

func (e notStarted) Error() string   { return e.cause.Error() }
func (e notStarted) Unwrap() []error { return []error{e.cause, errWorkflowNotStarted} }

func (a *API) fireWorkflowRun(ctx context.Context, job jobs.WorkflowRunFireArgs) error {
	if err := a.ensureHostConfigProvisioned(ctx, job.OrgID); err != nil {
		return notStarted{err}
	}
	var payload any = jsObjOf()
	if len(job.TriggerPayload) > 0 && string(job.TriggerPayload) != "null" {
		v, err := jsParse(job.TriggerPayload)
		if err != nil {
			return notStarted{err}
		}
		payload = v
	}
	p, err := a.prepareWorkflowRun(ctx, job.OrgID, job.WorkflowID, job.TriggerKind, payload, workflowRunOrigin{
		threadID: job.ThreadID, parentChainDepth: job.ParentChainDepth, subscriptionID: job.TriggeredBySubscriptionID,
		outputID: job.TriggeredByOutputID, observationID: job.TriggeredByObservationID})
	if err != nil {
		return notStarted{err}
	}
	if job.ScheduleFiringID != "" {
		if _, err := a.DB.Exec(ctx, `update workflow_schedule_firing set workflow_run_id = $2, updated_at = now()
			where id = $1 and status = 'running'`, job.ScheduleFiringID, p.workflowRunID); err != nil {
			return err
		}
	}
	message := ""
	if job.UserMessage != nil {
		message = *job.UserMessage
	}
	o := workflowTurnOptions{Workflow: p.workflow, WorkflowRunID: p.workflowRunID, TriggerKind: job.TriggerKind,
		ThreadID: p.threadID, RunID: p.runID, ObservationID: job.TriggeredByObservationID, TriggerPayload: payload,
		UserMessage: message, Mode: "headless", IncludeUxMetadata: true, resolve: a.turnResolve, runCore: a.turnCore}
	base, cancel := context.WithCancelCause(ctx)
	run, err := a.beginWorkflowRun(base, cancel, job.OrgID, o)
	if err != nil {
		return err
	}
	res, err := run()
	if err != nil {
		return err
	}
	if res.Status == "cancelled" && !errors.Is(context.Cause(base), errRunStopped) {
		return errWorkflowRunInterrupted
	}
	return nil
}

func (a *API) claimScheduleFiring(ctx context.Context, firingID, orgID, workflowID string) (bool, error) {
	tag, err := a.DB.Exec(ctx, `update workflow_schedule_firing
		set status = 'running', lease_until = now() + $4 * interval '1 second', updated_at = now()
		where id = $1 and org_id = $2 and workflow_id = $3 and status in ('pending', 'dispatching', 'enqueued')`,
		firingID, orgID, workflowID, int64(workflowFiringRunLease/time.Second))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (a *API) enqueueWorkflowRunFire(ctx context.Context, tx pgx.Tx, job jobs.WorkflowRunFireArgs, dedupeKey string, window time.Duration) (string, error) {
	if a.Jobs == nil {
		return "", errors.New("the job queue is not ready")
	}
	return a.Jobs.WorkflowRunFire(ctx, tx, job, dedupeKey, window)
}
