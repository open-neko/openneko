package webfront

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	staleRunReason        = "Interrupted — the worker stopped before the run finished."
	staleQueuedReason     = "Never started — the process that accepted this run stopped before launching it. Retry to run it again."
	staleQueuedMinAge     = 120 * time.Second
	reconcileEvery        = 60 * time.Second
	knowledgeRefreshEvery = 60 * time.Second
)

// staleRunMinAge must exceed the chat turn budget: a streaming turn does not
// touch its run row, so a shorter age cancels live runs.
func staleRunMinAge() time.Duration {
	return max(660*time.Second, time.Duration(agentLimitsFor("chat").TimeoutMs)*time.Millisecond+240*time.Second)
}

// ReconcileStaleRuns cancels work runs left running or queued by a process
// that stopped, and the workflow runs that point at them.
func (a *API) ReconcileStaleRuns(ctx context.Context, now time.Time, minAge time.Duration) (int, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `update work_run set status = 'cancelled', error = $1, finished_at = $2, updated_at = $2
		where status = 'running' and updated_at <= $3 returning id::text`, staleRunReason, now, now.Add(-minAge))
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	rows, err = tx.Query(ctx, `update work_run set status = 'cancelled', error = $1, finished_at = $2, updated_at = $2
		where status = 'queued' and created_at <= $3 returning id::text`, staleQueuedReason, now, now.Add(-max(minAge, staleQueuedMinAge)))
	if err != nil {
		return 0, err
	}
	queued, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	ids = append(ids, queued...)
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `update workflow_run set status = 'cancelled', error = $1, finished_at = $2, updated_at = $2
		where work_run_id::text = any($3) and status in ('running', 'queued')`, staleRunReason, now, ids); err != nil {
		return 0, err
	}
	return len(ids), tx.Commit(ctx)
}

// StartRunReconcile runs the stale-run sweep every minute until ctx ends.
func (a *API) StartRunReconcile(ctx context.Context) {
	minAge := staleRunMinAge()
	go func() {
		tick := time.NewTicker(reconcileEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				n, err := a.ReconcileStaleRuns(ctx, now, minAge)
				if err != nil {
					log.Printf("[reconcile] stale-run sweep failed: %v", err)
				} else if n > 0 {
					log.Printf("[reconcile] cancelled %d stale run(s)", n)
				}
			}
		}
	}()
}

// RefreshKnowledgeCaches checks the catalog revision of each org with a jwt
// data source and rebuilds its knowledge pack when the revision changed.
func (a *API) RefreshKnowledgeCaches(ctx context.Context) {
	orgs, err := collectStrings(ctx, a, `select distinct org_id::text from data_source where auth_mode = 'jwt'`)
	if err != nil {
		log.Printf("[knowledge] refresh sweep failed: %v", err)
		return
	}
	for _, org := range orgs {
		if r := a.prefetchKnowledge(ctx, org, filepath.Join(orgAgentRoot(org), "knowledge"), true); !r.OK {
			log.Printf("[knowledge] refresh failed for %s: %s", org, r.Error)
		}
	}
}

func (a *API) prefetchKnowledgeAtBoot(ctx context.Context) {
	orgs, err := collectStrings(ctx, a, `select distinct org_id::text from data_source where coalesce(mcp_url, '') <> ''`)
	if err != nil {
		log.Printf("[knowledge] boot prefetch failed: %v", err)
		return
	}
	for _, org := range orgs {
		roots, err := orgWorkspace(org)
		if err != nil {
			log.Printf("[knowledge] boot prefetch failed for %s: %v", org, err)
			continue
		}
		if r := a.prefetchKnowledge(ctx, org, roots.Knowledge, false); !r.OK {
			log.Printf("[knowledge] boot prefetch failed for %s (%s); turns refresh on first run", org, r.Error)
		}
	}
}

// StartKnowledgeRefresh prefetches the knowledge packs once, then refreshes
// them every minute until ctx ends.
func (a *API) StartKnowledgeRefresh(ctx context.Context) {
	go func() {
		a.prefetchKnowledgeAtBoot(ctx)
		tick := time.NewTicker(knowledgeRefreshEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				a.RefreshKnowledgeCaches(ctx)
			}
		}
	}()
}
