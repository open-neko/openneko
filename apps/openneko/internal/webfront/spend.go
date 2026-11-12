package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// spendExceeded is SpendBudgetExceeded in packages/llm/src/spend/admission.ts.
type spendExceeded struct {
	budget                                string
	limitMicros, committedMicros, reserve float64
	resetsAt, windowStart                 time.Time
	retryAfter                            int64
	orgID, workflowID, source             string
}

func (e *spendExceeded) Error() string { return e.message() }

func (e *spendExceeded) message() string {
	scope := "this workflow"
	if strings.HasPrefix(e.budget, "org") {
		scope = "this organization"
	}
	window := "daily"
	if strings.HasSuffix(e.budget, "hourly") {
		window = "hourly"
	}
	return fmt.Sprintf("The %s spending limit of $%s for %s is reached. Try again after %s UTC.",
		window, jsToFixed2(microsToUSD(e.limitMicros)), scope, e.resetsAt.UTC().Format("15:04"))
}

func microsToUSD(micros float64) float64 { return math.Floor(micros+0.5) / 1_000_000 }

var spendCeilings = []struct {
	env string
	usd float64
}{
	{"OPENNEKO_SPEND_CEILING_RUN_CAP_USD", 50},
	{"OPENNEKO_SPEND_CEILING_ORG_HOURLY_USD", 500},
	{"OPENNEKO_SPEND_CEILING_ORG_DAILY_USD", 2000},
	{"OPENNEKO_SPEND_CEILING_WORKFLOW_HOURLY_USD", 100},
	{"OPENNEKO_SPEND_CEILING_WORKFLOW_DAILY_USD", 500},
}

// spendLimits is loadSpendLimits(): run cap, org hourly, org daily,
// workflow hourly, workflow daily, each clamped to its ceiling.
func spendLimits(ctx context.Context, q querier, orgID, workflowID string) ([5]float64, error) {
	var out [5]float64
	rows, err := q.Query(ctx, `select workflow_id is null, run_cap_micros, org_hourly_micros, org_daily_micros,
	        workflow_hourly_micros, workflow_daily_micros
	   from spend_limit
	  where org_id = $1 and (workflow_id is null or workflow_id = $2::uuid)`, orgID, nullable(workflowID))
	if err != nil {
		return out, err
	}
	var org, override *[5]*int64
	for rows.Next() {
		var isOrg bool
		var v [5]*int64
		if err := rows.Scan(&isOrg, &v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
			rows.Close()
			return out, err
		}
		if isOrg && org == nil {
			org = &v
		} else if !isOrg && override == nil {
			override = &v
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if org == nil {
		return out, fmt.Errorf("No spend limits are configured for organization %s.", orgID)
	}
	ceilings := spendCeilingMicros()
	for i := range spendCeilings {
		ceiling := ceilings[i]
		value := org[i]
		if i >= 3 && override != nil && override[i] != nil {
			value = override[i]
		}
		n := 0.0
		if value != nil {
			n = float64(*value)
		}
		out[i] = math.Min(n, ceiling)
	}
	return out, nil
}

func spendCeilingMicros() [5]float64 {
	var out [5]float64
	for i, c := range spendCeilings {
		usd := c.usd
		if raw, ok := jsNumber(os.Getenv(c.env)); ok && !math.IsInf(raw, 0) && raw > c.usd {
			usd = raw
		}
		out[i] = usdToMicros(usd)
	}
	return out
}

func usdToMicros(usd float64) float64 { return math.Max(0, math.Ceil(usd*1_000_000)) }

// admitRunSpend checks the four budgets and reserves the run cap inside
// the caller's transaction.
func admitRunSpend(ctx context.Context, tx pgx.Tx, orgID, workflowID, workRunID, source string, now time.Time) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('openneko:spend:' || $1))`, orgID); err != nil {
		return err
	}
	limits, err := spendLimits(ctx, tx, orgID, workflowID)
	if err != nil {
		return err
	}
	hourStart := now.Truncate(time.Hour)
	dayStart := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	type check struct {
		budget          string
		limit           float64
		since, resetsAt time.Time
		workflowID      string
	}
	checks := []check{
		{"org_hourly", limits[1], hourStart, hourStart.Add(time.Hour), ""},
		{"org_daily", limits[2], dayStart, dayStart.Add(24 * time.Hour), ""},
	}
	if workflowID != "" {
		checks = append(checks,
			check{"workflow_hourly", limits[3], hourStart, hourStart.Add(time.Hour), workflowID},
			check{"workflow_daily", limits[4], dayStart, dayStart.Add(24 * time.Hour), workflowID})
	}
	reserve := limits[0]
	for _, c := range checks {
		committed, err := committedSpend(ctx, tx, orgID, c.workflowID, c.since)
		if err != nil {
			return err
		}
		if committed+reserve > c.limit {
			return &spendExceeded{budget: c.budget, limitMicros: c.limit, committedMicros: committed, reserve: reserve,
				resetsAt: c.resetsAt, windowStart: c.since, orgID: orgID, workflowID: c.workflowID, source: source,
				retryAfter: max(1, int64(math.Ceil(float64(c.resetsAt.Sub(now).Milliseconds())/1000)))}
		}
	}
	_, err = tx.Exec(ctx, `insert into spend_reservation (org_id, work_run_id, workflow_id, source, reserved_micros, created_at)
	     values ($1, $2::uuid, $3::uuid, $4, $5, $6)`, orgID, nullable(workRunID), nullable(workflowID), source, int64(reserve), now)
	return err
}

func committedSpend(ctx context.Context, tx pgx.Tx, orgID, workflowID string, since time.Time) (float64, error) {
	spent, held, err := spendBreakdown(ctx, tx, orgID, workflowID, since)
	return spent + held, err
}

func spendBreakdown(ctx context.Context, q querier, orgID, workflowID string, since time.Time) (float64, float64, error) {
	var spent, held string
	err := q.QueryRow(ctx, `with open_reservation as (
	     select r.id, r.reserved_micros
	       from spend_reservation r
	       left join work_run w on w.id = r.work_run_id
	      where r.org_id = $1
	        and ($2::uuid is null or r.workflow_id = $2::uuid)
	        and r.released_at is null
	        and (r.work_run_id is null or w.status in ('queued', 'running'))
	   ), used as (
	     select l.reservation_id, sum(l.cost_micros) as used
	       from spend_ledger l
	       join open_reservation o on o.id = l.reservation_id
	      group by l.reservation_id
	   )
	   select
	     coalesce((select sum(cost_micros) from spend_ledger
	                where org_id = $1
	                  and ($2::uuid is null or workflow_id = $2::uuid)
	                  and created_at >= $3), 0)::text as spent,
	     coalesce((select sum(greatest(o.reserved_micros - coalesce(u.used, 0), 0))
	                 from open_reservation o
	                 left join used u on u.reservation_id = o.id), 0)::text as held`,
		orgID, nullable(workflowID), since).Scan(&spent, &held)
	if err != nil {
		return 0, 0, err
	}
	s, _ := strconv.ParseFloat(spent, 64)
	h, _ := strconv.ParseFloat(held, 64)
	return s, h, nil
}

// recordBudgetBlocked is recordBudgetBlocked() and raiseSpendAlert(): one
// alert per kind, subject and window. The worker fans the event out to the
// external_event subscriptions. A failure is logged and dropped.
func (a *API) recordBudgetBlocked(ctx context.Context, e *spendExceeded) {
	subject := "org"
	if e.workflowID != "" {
		subject = "workflow:" + e.workflowID
	}
	windowSeconds := 86_400
	if strings.HasSuffix(e.budget, "hourly") {
		windowSeconds = 3_600
	}
	details := struct {
		Budget         string  `json:"budget"`
		Source         string  `json:"source"`
		Message        string  `json:"message"`
		WindowStart    string  `json:"windowStart"`
		ObservedMicros float64 `json:"observedMicros"`
		ThresholdMicro float64 `json:"thresholdMicros"`
	}{e.budget, e.source, "A run was blocked. " + e.message(), jsDate(e.windowStart), e.committedMicros, e.limitMicros}
	raw, err := json.Marshal(details)
	if err != nil {
		log.Printf("[spend] could not record blocked alert: %v", err)
		return
	}
	a.raiseSpendAlert(ctx, e.orgID, "spend.budget_blocked", subject, e.committedMicros, e.limitMicros, windowSeconds, details.WindowStart, raw)
}

// raiseSpendAlert is raiseSpendAlert(): details is a JSON object that holds
// message, windowStart, observedMicros and thresholdMicros.
func (a *API) raiseSpendAlert(ctx context.Context, orgID, kind, subject string, observed, threshold float64, windowSeconds int, windowStart string, details []byte) {
	cents := func(micros float64) float64 { return math.Min(2_147_483_647, math.Floor(micros/10_000+0.5)) }
	tag, err := a.DB.Exec(ctx, `insert into behavior_alert (org_id, kind, subject, observed, threshold, window_seconds, details)
	     values ($1, $2, $3, $4, $5, $6, $7::jsonb)
	     on conflict (org_id, kind, subject, (details->>'windowStart')) where kind like 'spend.%' do nothing`,
		orgID, kind, subject, int64(cents(observed)), int64(cents(threshold)), windowSeconds, string(details))
	if err != nil {
		log.Printf("[spend] could not record %s alert: %v", kind, err)
		return
	}
	if tag.RowsAffected() == 0 {
		return
	}
	payload := append([]byte(`{"subject":`+jsStringify(subject, false)+","), details[1:]...)
	body := fmt.Sprintf(`{"orgId":%s,"name":%s,"source":"spend-governance","payload":%s,"dedupeKey":%s}`,
		jsStringify(orgID, false), jsStringify(kind, false), payload, jsStringify(kind+":"+subject+":"+windowStart, false))
	status, reply, err := a.workerDo(ctx, http.MethodPost, "/admin/events/external", 10*time.Second, []byte(body))
	if err != nil || status != http.StatusOK {
		log.Printf("[spend] alert dispatch failed: %d %v %s", status, err, reply)
	}
}

func jsDate(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
