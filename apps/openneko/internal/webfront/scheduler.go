package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

const (
	workflowSchedulerHealthID        = "cron"
	workflowSchedulerLockKey         = "openneko.workflow-scheduler.materialize"
	workflowSchedulerDispatchLease   = 90 * time.Second
	defaultWorkflowSchedulerEvery    = 30 * time.Second
	defaultWorkflowSchedulerStale    = 90 * time.Second
	defaultWorkflowSchedulerLeaseMax = 25
	maxCoalescedScanSteps            = 10000
	workflowSchedulerDedupeWindow    = 30 * 24 * time.Hour
)

type schedulerRuntimeState struct {
	initialized         bool
	configured          bool
	startedAt           time.Time
	lastAttemptAt       *time.Time
	lastSuccessAt       *time.Time
	lastErrorAt         *time.Time
	lastError           *string
	consecutiveFailures int
	materialized        int
	dispatched          int
	recovered           int
}

type SchedulerHealth struct {
	ID                  string  `json:"id"`
	Status              string  `json:"status"`
	StartedAt           string  `json:"startedAt"`
	LastAttemptAt       *string `json:"lastAttemptAt"`
	LastSuccessAt       *string `json:"lastSuccessAt"`
	LastErrorAt         *string `json:"lastErrorAt"`
	LastError           *string `json:"lastError"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	Stale               bool    `json:"stale"`
	Materialized        int     `json:"materialized"`
	Dispatched          int     `json:"dispatched"`
	Recovered           int     `json:"recovered"`
}

var cronScheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// NextWorkflowFireAt computes the next cron firing strictly after from.
func NextWorkflowFireAt(expr, tz string, from time.Time) (time.Time, error) {
	loc := time.UTC
	if trimmed := strings.TrimSpace(tz); trimmed != "" {
		loaded, err := time.LoadLocation(trimmed)
		if err != nil {
			return time.Time{}, err
		}
		loc = loaded
	}
	sched, err := cronScheduleParser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from.In(loc)).UTC(), nil
}

type advanceSchedulePlan struct {
	ScheduledFor []time.Time
	NextFireAt   time.Time
	Coalesced    int
}

func advanceWorkflowSchedule(expr, tz string, nextFireAt *time.Time, now time.Time) (advanceSchedulePlan, error) {
	if nextFireAt == nil {
		first, err := NextWorkflowFireAt(expr, tz, now)
		if err != nil {
			return advanceSchedulePlan{}, err
		}
		return advanceSchedulePlan{ScheduledFor: []time.Time{}, NextFireAt: first}, nil
	}
	if nextFireAt.After(now) {
		return advanceSchedulePlan{ScheduledFor: []time.Time{}, NextFireAt: *nextFireAt}, nil
	}
	cursor := *nextFireAt
	missed := 0
	for step := 0; step < maxCoalescedScanSteps; step++ {
		next, err := NextWorkflowFireAt(expr, tz, cursor)
		if err != nil {
			return advanceSchedulePlan{}, err
		}
		if next.After(now) {
			return advanceSchedulePlan{
				ScheduledFor: []time.Time{cursor},
				NextFireAt:   next,
				Coalesced:    missed,
			}, nil
		}
		cursor = next
		missed++
	}
	fallback, err := NextWorkflowFireAt(expr, tz, now)
	if err != nil {
		return advanceSchedulePlan{}, err
	}
	return advanceSchedulePlan{
		ScheduledFor: []time.Time{cursor},
		NextFireAt:   fallback,
		Coalesced:    missed,
	}, nil
}

// ResetWorkflowSchedulerHealth resets the in-memory scheduler state; tests use it.
func (a *API) ResetWorkflowSchedulerHealth(now time.Time) {
	a.schedMu.Lock()
	a.schedState = schedulerRuntimeState{
		initialized: true,
		configured:  true,
		startedAt:   now.UTC(),
	}
	a.schedMu.Unlock()
}

// WorkflowSchedulerHealth returns the current scheduler health snapshot.
func (a *API) WorkflowSchedulerHealth(now time.Time) SchedulerHealth {
	staleAfter := schedulerDurationEnv("OPENNEKO_WORKFLOW_SCHEDULER_STALE_MS", defaultWorkflowSchedulerStale)
	a.schedMu.Lock()
	if !a.schedState.initialized {
		a.schedState.initialized = true
		a.schedState.startedAt = now.UTC()
	}
	st := a.schedState
	a.schedMu.Unlock()

	ref := st.startedAt
	if st.lastSuccessAt != nil {
		ref = *st.lastSuccessAt
	}
	stale := now.Sub(ref) > staleAfter
	healthy := st.consecutiveFailures == 0 && !stale
	status := "degraded"
	if healthy {
		status = "ok"
	}
	return SchedulerHealth{
		ID:                  workflowSchedulerHealthID,
		Status:              status,
		StartedAt:           jsTime(st.startedAt),
		LastAttemptAt:       optJSTime(st.lastAttemptAt),
		LastSuccessAt:       optJSTime(st.lastSuccessAt),
		LastErrorAt:         optJSTime(st.lastErrorAt),
		LastError:           st.lastError,
		ConsecutiveFailures: st.consecutiveFailures,
		Stale:               stale,
		Materialized:        st.materialized,
		Dispatched:          st.dispatched,
		Recovered:           st.recovered,
	}
}

func optJSTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := jsTime(*t)
	return &s
}

func schedulerDurationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 1000 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

// StartWorkflowScheduler runs the durable cron scheduler tick loop until ctx ends.
func (a *API) StartWorkflowScheduler(ctx context.Context) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OPENNEKO_DISABLE_WORKFLOW_SCHEDULER")), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("OPENNEKO_DISABLE_WORKFLOW_SCHEDULER")), "true") {
		return
	}
	interval := schedulerDurationEnv("OPENNEKO_WORKFLOW_SCHEDULER_INTERVAL_MS", defaultWorkflowSchedulerEvery)
	a.ResetWorkflowSchedulerHealth(time.Now())
	go func() {
		a.RunWorkflowSchedulerTick(ctx, time.Now())
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				a.RunWorkflowSchedulerTick(ctx, now)
			}
		}
	}()
}

// RunWorkflowSchedulerTick materializes due workflow firings, leases pending
// firings, enqueues their workflow_run_fire jobs, and updates scheduler health.
func (a *API) RunWorkflowSchedulerTick(ctx context.Context, now time.Time) SchedulerHealth {
	now = now.UTC()
	a.schedMu.Lock()
	if !a.schedState.initialized {
		a.schedState.initialized = true
		a.schedState.startedAt = now
	}
	a.schedState.lastAttemptAt = &now
	a.schedMu.Unlock()

	materialized, coalesced, recovered, err := a.materializeDueWorkflowFirings(ctx, now)
	if err != nil {
		a.recordSchedulerTickFailure(ctx, now, err)
		return a.WorkflowSchedulerHealth(now)
	}
	dispatched, err := a.dispatchPendingWorkflowFirings(ctx, now)
	if err != nil {
		a.recordSchedulerTickFailure(ctx, now, err)
		return a.WorkflowSchedulerHealth(now)
	}

	a.schedMu.Lock()
	a.schedState.lastSuccessAt = &now
	a.schedState.lastError = nil
	a.schedState.consecutiveFailures = 0
	a.schedState.materialized += materialized
	a.schedState.dispatched += dispatched
	a.schedState.recovered += recovered
	a.schedMu.Unlock()

	_ = a.recordDBSchedulerSuccess(ctx, now, materialized, dispatched)
	if materialized > 0 || dispatched > 0 || recovered > 0 {
		log.Printf("[workflow-scheduler] materialized=%d dispatched=%d recovered=%d coalesced=%d",
			materialized, dispatched, recovered, coalesced)
	}
	return a.WorkflowSchedulerHealth(now)
}

func (a *API) recordSchedulerTickFailure(ctx context.Context, now time.Time, err error) {
	msg := err.Error()
	a.schedMu.Lock()
	a.schedState.lastErrorAt = &now
	a.schedState.lastError = &msg
	a.schedState.consecutiveFailures++
	a.schedMu.Unlock()
	_ = a.recordDBSchedulerFailure(ctx, now, msg)
	log.Printf("[workflow-scheduler] tick failed: %s", msg)
}

func (a *API) materializeDueWorkflowFirings(ctx context.Context, now time.Time) (int, int, int, error) {
	var materialized, coalesced, recovered int
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtext($1))`, workflowSchedulerLockKey).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return nil
		}
		r, err := recoverWorkflowFirings(ctx, tx, now)
		if err != nil {
			return err
		}
		recovered = r

		rows, err := tx.Query(ctx, `
			select d.id::text, d.org_id, d.cron, coalesce(nullif(d.cron_timezone, ''), 'UTC'), d.updated_at, s.next_fire_at
			from workflow_definition d
			left join workflow_schedule_state s on s.workflow_id = d.id
			where d.enabled = true
			  and d.cron_enabled = true
			  and d.cron is not null
			  and btrim(d.cron) <> ''
			order by d.id asc
			for update of d skip locked`)
		if err != nil {
			return err
		}
		type scheduleRow struct {
			id                  string
			orgID               string
			cron                string
			timezone            string
			definitionUpdatedAt time.Time
			nextFireAt          *time.Time
		}
		defs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (scheduleRow, error) {
			var s scheduleRow
			err := row.Scan(&s.id, &s.orgID, &s.cron, &s.timezone, &s.definitionUpdatedAt, &s.nextFireAt)
			return s, err
		})
		if err != nil {
			return err
		}

		for _, row := range defs {
			plan, err := advanceWorkflowSchedule(row.cron, row.timezone, row.nextFireAt, now)
			if err != nil {
				if _, uerr := tx.Exec(ctx, `
					update workflow_schedule_firing
					set status = 'cancelled', completed_at = $2, lease_until = null,
					    last_error = 'schedule is invalid before dispatch',
					    updated_at = $2
					where workflow_id = $1
					  and status in ('pending', 'dispatching', 'enqueued')`,
					row.id, now); uerr != nil {
					return uerr
				}
				if _, derr := tx.Exec(ctx, `delete from workflow_schedule_state where workflow_id = $1`, row.id); derr != nil {
					return derr
				}
				continue
			}
			coalesced += plan.Coalesced
			var lastMaterializedAt *time.Time
			for _, sf := range plan.ScheduledFor {
				sfCopy := sf
				tag, ierr := tx.Exec(ctx, `
					insert into workflow_schedule_firing (org_id, workflow_id, scheduled_for, status, available_at, created_at, updated_at)
					values ($1, $2, $3, 'pending', $4, $4, $4)
					on conflict (workflow_id, scheduled_for) do nothing`,
					row.orgID, row.id, sf, now)
				if ierr != nil {
					return ierr
				}
				if tag.RowsAffected() > 0 {
					materialized += int(tag.RowsAffected())
					lastMaterializedAt = &sfCopy
				}
			}
			if _, err := tx.Exec(ctx, `
				insert into workflow_schedule_state
				  (workflow_id, org_id, cron, cron_timezone, definition_updated_at, next_fire_at, catch_up_policy, last_materialized_at, created_at, updated_at)
				values ($1, $2, $3, $4, $5, $6, 'coalesce', $7, $8, $8)
				on conflict (workflow_id) do update
				set org_id = excluded.org_id,
				    cron = excluded.cron,
				    cron_timezone = excluded.cron_timezone,
				    definition_updated_at = excluded.definition_updated_at,
				    next_fire_at = excluded.next_fire_at,
				    last_materialized_at = coalesce(excluded.last_materialized_at, workflow_schedule_state.last_materialized_at),
				    catch_up_policy = excluded.catch_up_policy,
				    updated_at = excluded.updated_at`,
				row.id, row.orgID, row.cron, row.timezone, row.definitionUpdatedAt, plan.NextFireAt, lastMaterializedAt, now); err != nil {
				return err
			}
		}
		return nil
	})
	return materialized, coalesced, recovered, err
}

func recoverWorkflowFirings(ctx context.Context, tx pgx.Tx, now time.Time) (int, error) {
	staleEnqueueCutoff := now.Add(-2 * workflowSchedulerDispatchLease)
	tag1, err := tx.Exec(ctx, `
		update workflow_schedule_firing f
		set status = 'pending', lease_until = null, updated_at = $1
		where f.status = 'enqueued'
		  and f.workflow_run_id is null
		  and f.updated_at <= $2`, now, staleEnqueueCutoff)
	if err != nil {
		return 0, err
	}
	tag2, err := tx.Exec(ctx, `
		update workflow_schedule_firing f
		set status = 'pending', lease_until = null, updated_at = $1
		where f.status = 'running'
		  and f.lease_until is not null
		  and f.lease_until <= $1
		  and (
		    f.workflow_run_id is null
		    or exists (
		      select 1 from workflow_run r
		      where r.id = f.workflow_run_id
		        and r.status in ('failed', 'cancelled')
		    )
		  )`, now)
	if err != nil {
		return 0, err
	}
	return int(tag1.RowsAffected() + tag2.RowsAffected()), nil
}

type leasedWorkflowFiring struct {
	id           string
	orgID        string
	workflowID   string
	scheduledFor time.Time
	attempts     int
}

func (a *API) leasePendingWorkflowFirings(ctx context.Context, now time.Time) ([]leasedWorkflowFiring, error) {
	leaseUntil := now.Add(workflowSchedulerDispatchLease)
	rows, err := a.DB.Query(ctx, `
		with due as (
			select f.id
			from workflow_schedule_firing f
			join workflow_definition d on d.id = f.workflow_id
			where f.status = 'pending'
			  and f.scheduled_for <= $1
			  and (f.lease_until is null or f.lease_until <= $1)
			  and d.enabled = true
			  and d.cron_enabled = true
			order by f.scheduled_for asc, f.id asc
			limit $2
			for update of f skip locked
		)
		update workflow_schedule_firing f
		set lease_until = $3,
		    attempts = f.attempts + 1,
		    updated_at = $1
		from due
		where f.id = due.id
		returning f.id::text, f.org_id, f.workflow_id::text, f.scheduled_for, f.attempts`,
		now, defaultWorkflowSchedulerLeaseMax, leaseUntil)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (leasedWorkflowFiring, error) {
		var f leasedWorkflowFiring
		err := row.Scan(&f.id, &f.orgID, &f.workflowID, &f.scheduledFor, &f.attempts)
		return f, err
	})
}

func (a *API) dispatchPendingWorkflowFirings(ctx context.Context, now time.Time) (int, error) {
	leased, err := a.leasePendingWorkflowFirings(ctx, now)
	if err != nil {
		return 0, err
	}
	dispatched := 0
	for _, firing := range leased {
		scheduledISO := jsTime(firing.scheduledFor.UTC())
		payload, _ := json.Marshal(map[string]any{
			"firingId":     firing.id,
			"scheduledFor": scheduledISO,
			"attempt":      firing.attempts,
		})
		dedupeKey := fmt.Sprintf("cron:%s:%s", firing.workflowID, scheduledISO)
		var jobID string
		err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			var qerr error
			jobID, qerr = a.enqueueWorkflowRunFire(ctx, tx, jobs.WorkflowRunFireArgs{
				OrgID:            firing.orgID,
				WorkflowID:       firing.workflowID,
				TriggerKind:      "cron",
				ScheduleFiringID: firing.id,
				TriggerPayload:   payload,
			}, dedupeKey, workflowSchedulerDedupeWindow)
			return qerr
		})
		if err != nil {
			_, _ = a.DB.Exec(ctx, `
				update workflow_schedule_firing
				set status = 'pending', lease_until = null, last_error = $2, updated_at = now()
				where id = $1`, firing.id, err.Error())
			log.Printf("[workflow-scheduler] dispatch failed firing=%s: %v", firing.id, err)
			continue
		}
		var jobIDPtr *string
		if jobID != "" {
			jobIDPtr = &jobID
		}
		if _, err := a.DB.Exec(ctx, `
			update workflow_schedule_firing
			set status = 'enqueued',
			    queue_job_id = coalesce($2, queue_job_id),
			    dispatched_at = coalesce(dispatched_at, now()),
			    lease_until = null,
			    last_error = null,
			    updated_at = now()
			where id = $1 and status = 'pending'`, firing.id, jobIDPtr); err != nil {
			return dispatched, err
		}
		dispatched++
	}
	return dispatched, nil
}

func (a *API) recordDBSchedulerSuccess(ctx context.Context, now time.Time, materialized, dispatched int) error {
	_, err := a.DB.Exec(ctx, `
		insert into workflow_scheduler_health
		  (id, last_started_at, last_succeeded_at, last_error, last_materialized_count, last_dispatched_count, updated_at)
		values ($1, $2, $2, null, $3, $4, $2)
		on conflict (id) do update
		set last_started_at = excluded.last_started_at,
		    last_succeeded_at = excluded.last_succeeded_at,
		    last_error = null,
		    last_materialized_count = excluded.last_materialized_count,
		    last_dispatched_count = excluded.last_dispatched_count,
		    updated_at = excluded.updated_at`,
		workflowSchedulerHealthID, now, materialized, dispatched)
	return err
}

func (a *API) recordDBSchedulerFailure(ctx context.Context, now time.Time, msg string) error {
	_, err := a.DB.Exec(ctx, `
		insert into workflow_scheduler_health
		  (id, last_started_at, last_error_at, last_error, updated_at)
		values ($1, $2, $2, $3, $2)
		on conflict (id) do update
		set last_started_at = excluded.last_started_at,
		    last_error_at = excluded.last_error_at,
		    last_error = excluded.last_error,
		    updated_at = excluded.updated_at`,
		workflowSchedulerHealthID, now, msg)
	return err
}

// ServeHealth answers GET /health, /health/scheduler and /health/security on
// the public port. Only a signed-in admin gets the detail there; the admin
// socket always gives it.
func (f *Front) ServeHealth(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/health":
		f.API.serveHealth(w, r.URL.Path, false)
		return true
	case "/health/scheduler", "/health/security":
		f.API.serveHealth(w, r.URL.Path, f.API.signedInAdmin(r))
		return true
	}
	return false
}

func (a *API) signedInAdmin(r *http.Request) bool {
	if a == nil || a.Gate == nil || a.DB == nil || !a.Gate.Status(r.Context()).Live() {
		return false
	}
	act, err := a.currentActor(r.Context(), r)
	return err == nil && act.UserID != "" && act.Admin
}

func (a *API) schedulerHealth(now time.Time) (SchedulerHealth, bool) {
	if a == nil {
		return SchedulerHealth{}, false
	}
	a.schedMu.Lock()
	configured := a.schedState.configured
	a.schedMu.Unlock()
	if !configured {
		return SchedulerHealth{}, false
	}
	return a.WorkflowSchedulerHealth(now), true
}

func (a *API) serveHealth(w http.ResponseWriter, path string, detail bool) {
	switch path {
	case "/health":
		health, ok := a.schedulerHealth(time.Now())
		if !ok || health.Status == "ok" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(health.Status))
	case "/health/scheduler":
		health, ok := a.schedulerHealth(time.Now())
		switch {
		case !ok && detail:
			sendJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "error": "scheduler health is not configured"})
		case !ok:
			sendJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
		case detail:
			sendJSON(w, healthCode(health.Status), health)
		default:
			sendJSON(w, healthCode(health.Status), map[string]any{"status": health.Status})
		}
	case "/health/security":
		audit, hook := a.AuditHealth(), a.AuditWebhookState()
		status := "ok"
		if !audit.Healthy || hook.State == "refused" {
			status = "degraded"
		}
		if !detail {
			sendJSON(w, http.StatusOK, map[string]any{"status": status})
			return
		}
		sendJSON(w, http.StatusOK, map[string]any{"status": status, "auditLogging": audit, "auditWebhook": hook})
	}
}

func healthCode(status string) int {
	if status == "ok" {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}
