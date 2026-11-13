package webfront

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	queueMetricRefresh = "metric_refresh"
	pendingMemoryTTL   = 14 * 24 * time.Hour
)

// MetricSweep is the hourly metric_refresh_scheduled_sweep: it queues the
// metric refreshes that are due and ages out memories. Only a failure to
// queue refreshes fails the job.
func (a *API) MetricSweep(ctx context.Context) error {
	if err := a.queueDueMetrics(ctx, time.Now()); err != nil {
		return err
	}
	if archived, expired, err := a.sweepExpiredMemories(ctx, jsNow()); err != nil {
		log.Printf("[metric-sweep] memory TTL sweep failed: %v", err)
	} else if archived > 0 || expired > 0 {
		log.Printf("[metric-sweep] memory TTL sweep: archived %d, expired %d pending", archived, expired)
	}
	return nil
}

func metricCadence(cadence string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(cadence)) {
	case "hourly":
		return time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// metricRefreshDue is metricRefreshIsDue(): the organisation's setting slows
// a card, and never speeds one up.
func metricRefreshDue(cadence string, lastStatus *string, updated time.Time, orgRefresh string, now time.Time) bool {
	if (lastStatus != nil && *lastStatus == "pending") || orgRefresh == "off" {
		return false
	}
	wait := metricCadence(cadence)
	if orgRefresh == "daily" || orgRefresh == "weekly" {
		wait = max(wait, metricCadence(orgRefresh))
	}
	return !updated.Add(wait).After(now)
}

func (a *API) queueDueMetrics(ctx context.Context, now time.Time) error {
	rows, err := a.DB.Query(ctx, `select m.id::text, m.org_id, m.cadence, m.last_refresh_status, m.updated_at, coalesce(s.metric_refresh, 'card')
		from metric m left join spend_limit s on s.org_id = m.org_id and s.workflow_id is null
		where m.active`)
	if err != nil {
		return err
	}
	type card struct{ id, orgID string }
	var due []card
	for rows.Next() {
		var c card
		var cadence, orgRefresh string
		var status *string
		var updated time.Time
		if err := rows.Scan(&c.id, &c.orgID, &cadence, &status, &updated, &orgRefresh); err != nil {
			rows.Close()
			return err
		}
		if metricRefreshDue(cadence, status, updated, orgRefresh, now) {
			due = append(due, c)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	if err := a.ensureQueue(ctx, queueMetricRefresh); err != nil {
		return err
	}
	stamp := now.UTC().Truncate(time.Millisecond)
	for _, c := range due {
		err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
			var jobID string
			if err := tx.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger, trigger_payload)
				values ($1, 'metric_refresh', 'queued', 'scheduled', jsonb_build_object('metricId', $2::text)) returning id::text`,
				c.orgID, c.id).Scan(&jobID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update metric set last_refresh_status = 'pending', last_refresh_error = null,
				last_refresh_job_id = $2, updated_at = $3 where id = $1`, c.id, jobID, stamp); err != nil {
				return err
			}
			_, err := a.enqueue(ctx, tx, queueMetricRefresh, map[string]string{"processingJobId": jobID, "orgId": c.orgID})
			return err
		})
		if err != nil {
			return fmt.Errorf("queue the refresh of metric %s: %w", c.id, err)
		}
	}
	log.Printf("[metric-sweep] queued %d metric refresh job(s)", len(due))
	return nil
}

// sweepExpiredMemories is sweepExpiredWorkMemories(): it archives the
// memories past expires_at and declines the proposals left unreviewed for
// 14 days.
func (a *API) sweepExpiredMemories(ctx context.Context, now time.Time) (archived, expired int64, err error) {
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `update work_memory set archived_at = $1, updated_at = $1
			where expires_at is not null and expires_at <= now() and archived_at is null returning id::text, org_id`, now)
		if err != nil {
			return err
		}
		type gone struct{ id, orgID string }
		var list []gone
		for rows.Next() {
			var g gone
			if err := rows.Scan(&g.id, &g.orgID); err != nil {
				rows.Close()
				return err
			}
			list = append(list, g)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, g := range list {
			if err := memoryEvent(ctx, tx, g.orgID, &g.id, nil, nil, "expired", map[string]any{}); err != nil {
				return err
			}
		}
		archived = int64(len(list))
		tag, err := tx.Exec(ctx, `update work_pending_memory set status = 'declined', decision_text = 'expired (unreviewed past TTL)',
			decided_at = $1, updated_at = $1 where status = 'proposed' and created_at <= $2`, now, now.Add(-pendingMemoryTTL))
		expired = tag.RowsAffected()
		return err
	})
	return archived, expired, err
}
