package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const spendMeterDays = 30

var spendAmountLabels = [5]string{
	"Per-run cap", "Organization hourly budget", "Organization daily budget",
	"Workflow hourly budget", "Workflow daily budget",
}

type spendAmounts struct {
	RunCapUSD         float64 `json:"runCapUsd"`
	OrgHourlyUSD      float64 `json:"orgHourlyUsd"`
	OrgDailyUSD       float64 `json:"orgDailyUsd"`
	WorkflowHourlyUSD float64 `json:"workflowHourlyUsd"`
	WorkflowDailyUSD  float64 `json:"workflowDailyUsd"`
}

type spendLimitsUSD struct {
	spendAmounts
	WarnPercent int `json:"warnPercent"`
}

type spendWindowUsage struct {
	SpentUSD float64 `json:"spentUsd"`
	HeldUSD  float64 `json:"heldUsd"`
	LimitUSD float64 `json:"limitUsd"`
	ResetsAt string  `json:"resetsAt"`
}

type workflowSpendRow struct {
	WorkflowID        string   `json:"workflowId"`
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	HourlyOverrideUSD *float64 `json:"hourlyOverrideUsd"`
	DailyOverrideUSD  *float64 `json:"dailyOverrideUsd"`
	HourlyLimitUSD    float64  `json:"hourlyLimitUsd"`
	DailyLimitUSD     float64  `json:"dailyLimitUsd"`
	SpentTodayUSD     float64  `json:"spentTodayUsd"`
}

type spendAlert struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Message   string `json:"message"`
	CreatedAt string `json:"createdAt"`
}

type spendByModelRow struct {
	Task          *string `json:"task"`
	Provider      *string `json:"provider"`
	Model         *string `json:"model"`
	Calls         int64   `json:"calls"`
	Tokens        float64 `json:"tokens"`
	SpentUSD      float64 `json:"spentUsd"`
	UnpricedCalls int64   `json:"unpricedCalls"`
}

type spendSettings struct {
	Limits        spendLimitsUSD `json:"limits"`
	MetricRefresh string         `json:"metricRefresh"`
	Ceilings      spendAmounts   `json:"ceilings"`
	Org           struct {
		Hour spendWindowUsage `json:"hour"`
		Day  spendWindowUsage `json:"day"`
	} `json:"org"`
	Workflows []workflowSpendRow `json:"workflows"`
	Alerts    []spendAlert       `json:"alerts"`
	ByModel   []spendByModelRow  `json:"byModel"`
}

type spendSettingsError string

func (e spendSettingsError) Error() string { return string(e) }

func amountsUSD(micros [5]float64) spendAmounts {
	return spendAmounts{microsToUSD(micros[0]), microsToUSD(micros[1]), microsToUSD(micros[2]), microsToUSD(micros[3]), microsToUSD(micros[4])}
}

func metricRefreshFromColumn(v *string) string {
	if v != nil && (*v == "daily" || *v == "weekly" || *v == "off") {
		return *v
	}
	return "card"
}

func parseNumeric(s string) float64 {
	n, _ := strconv.ParseFloat(s, 64)
	return n
}

// spendSettingsFor is getSpendSettings() in packages/llm/src/spend/settings.ts.
func (a *API) spendSettingsFor(ctx context.Context, orgID string, now time.Time) (*spendSettings, error) {
	limits, err := spendLimits(ctx, a.DB, orgID, "")
	if err != nil {
		return nil, err
	}
	var warn *int
	var refresh *string
	if err := a.DB.QueryRow(ctx, `select warn_percent, metric_refresh from spend_limit where org_id = $1 and workflow_id is null limit 1`,
		orgID).Scan(&warn, &refresh); err != nil {
		return nil, err
	}
	out := &spendSettings{
		Limits:        spendLimitsUSD{amountsUSD(limits), 80},
		MetricRefresh: metricRefreshFromColumn(refresh),
		Ceilings:      amountsUSD(spendCeilingMicros()),
		Workflows:     []workflowSpendRow{},
		Alerts:        []spendAlert{},
		ByModel:       []spendByModelRow{},
	}
	if warn != nil {
		out.Limits.WarnPercent = *warn
	}
	hourStart := now.Truncate(time.Hour)
	u := now.UTC()
	dayStart := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	for _, win := range []struct {
		into  *spendWindowUsage
		since time.Time
		limit float64
		end   time.Time
	}{
		{&out.Org.Hour, hourStart, limits[1], hourStart.Add(time.Hour)},
		{&out.Org.Day, dayStart, limits[2], dayStart.Add(24 * time.Hour)},
	} {
		spent, held, err := spendBreakdown(ctx, a.DB, orgID, "", win.since)
		if err != nil {
			return nil, err
		}
		*win.into = spendWindowUsage{microsToUSD(spent), microsToUSD(held), microsToUSD(win.limit), jsDate(win.end)}
	}

	rows, err := a.DB.Query(ctx, `select id, kind, subject, details->>'message', created_at
	       from behavior_alert
	      where org_id = $1 and kind like 'spend.%' and acknowledged_at is null
	      order by created_at desc
	      limit 20`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var al spendAlert
		var msg *string
		var at time.Time
		if err := rows.Scan(&al.ID, &al.Kind, &al.Subject, &msg, &at); err != nil {
			rows.Close()
			return nil, err
		}
		al.Message, al.CreatedAt = al.Kind, jsDate(at)
		if msg != nil {
			al.Message = *msg
		}
		out.Alerts = append(out.Alerts, al)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = a.DB.Query(ctx, `select id, name, enabled, workflow_hourly_micros, workflow_daily_micros, spent_today::text
	   from (select w.id, w.name, w.enabled, l.workflow_hourly_micros, l.workflow_daily_micros,
	                coalesce((select sum(s.cost_micros) from spend_ledger s
	                           where s.org_id = w.org_id and s.workflow_id = w.id and s.created_at >= $2), 0) as spent_today
	           from workflow_definition w
	           left join spend_limit l on l.org_id = w.org_id and l.workflow_id = w.id
	          where w.org_id = $1) x
	  order by spent_today desc, name`,
		orgID, dayStart)
	if err != nil {
		return nil, err
	}
	override := func(v *int64) *float64 {
		if v == nil {
			return nil
		}
		usd := microsToUSD(float64(*v))
		return &usd
	}
	for rows.Next() {
		var row workflowSpendRow
		var hourly, daily *int64
		var spent string
		if err := rows.Scan(&row.WorkflowID, &row.Name, &row.Enabled, &hourly, &daily, &spent); err != nil {
			rows.Close()
			return nil, err
		}
		row.HourlyOverrideUSD, row.DailyOverrideUSD = override(hourly), override(daily)
		row.HourlyLimitUSD, row.DailyLimitUSD = microsToUSD(limits[3]), microsToUSD(limits[4])
		if row.HourlyOverrideUSD != nil {
			row.HourlyLimitUSD = *row.HourlyOverrideUSD
		}
		if row.DailyOverrideUSD != nil {
			row.DailyLimitUSD = *row.DailyOverrideUSD
		}
		row.SpentTodayUSD = microsToUSD(parseNumeric(spent))
		out.Workflows = append(out.Workflows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = a.DB.Query(ctx, `select task, provider, model, count(*), coalesce(sum(tokens), 0)::text,
	        coalesce(sum(cost_micros), 0)::text, count(*) filter (where priced in ('unpriced', 'fallback'))
	   from spend_ledger
	  where org_id = $1 and created_at >= $2
	  group by task, provider, model
	  order by coalesce(sum(cost_micros), 0) desc, count(*) desc
	  limit 100`, orgID, now.Add(-spendMeterDays*24*time.Hour))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var row spendByModelRow
		var tokens, spent string
		if err := rows.Scan(&row.Task, &row.Provider, &row.Model, &row.Calls, &tokens, &spent, &row.UnpricedCalls); err != nil {
			rows.Close()
			return nil, err
		}
		row.Tokens, row.SpentUSD = parseNumeric(tokens), microsToUSD(parseNumeric(spent))
		out.ByModel = append(out.ByModel, row)
	}
	return out, rows.Err()
}

func (a *API) sendSpendSettings(w http.ResponseWriter, r *http.Request, orgID string) {
	settings, err := a.spendSettingsFor(r.Context(), orgID, time.Now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, settings)
}

func (a *API) adminSpend(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	a.sendSpendSettings(w, r, act.OrgID)
}

// spendAmount is amount() in settings.ts: a positive dollar amount, as
// micros, at most the ceiling.
func spendAmount(v any, label string, ceiling float64) (float64, error) {
	n, ok := v.(float64)
	if !ok || math.IsInf(n, 0) || math.IsNaN(n) || n <= 0 {
		return 0, spendSettingsError(label + " must be a positive dollar amount.")
	}
	micros := usdToMicros(n)
	if micros > ceiling {
		return 0, spendSettingsError(label + " cannot exceed $" + jsToFixed2(microsToUSD(ceiling)) + ".")
	}
	return micros, nil
}

func jsonNumber(f float64) json.Number { return json.Number(jsNumberText(f)) }

func auditActor(userID string) any {
	if userID == "" {
		return nil
	}
	return userID
}

func spendAuditLimits(s *spendSettings) map[string]any {
	l := s.Limits
	return map[string]any{
		"runCapUsd": jsonNumber(l.RunCapUSD), "orgHourlyUsd": jsonNumber(l.OrgHourlyUSD), "orgDailyUsd": jsonNumber(l.OrgDailyUSD),
		"workflowHourlyUsd": jsonNumber(l.WorkflowHourlyUSD), "workflowDailyUsd": jsonNumber(l.WorkflowDailyUSD),
		"warnPercent": l.WarnPercent, "metricRefresh": s.MetricRefresh,
	}
}

// saveSpendLimits is saveSpendLimits() in settings.ts.
func (a *API) saveSpendLimits(ctx context.Context, act actor, draft map[string]any) (*spendSettings, error) {
	ceilings := spendCeilingMicros()
	keys := [5]string{"runCapUsd", "orgHourlyUsd", "orgDailyUsd", "workflowHourlyUsd", "workflowDailyUsd"}
	var next [5]float64
	for i, k := range keys {
		micros, err := spendAmount(draft[k], spendAmountLabels[i], ceilings[i])
		if err != nil {
			return nil, err
		}
		next[i] = micros
	}
	warn, ok := draft["warnPercent"].(float64)
	if !ok || warn != math.Trunc(warn) || warn < 50 || warn > 99 {
		return nil, spendSettingsError("Warning threshold must be a whole number from 50 to 99.")
	}
	switch {
	case next[1] > next[2]:
		return nil, spendSettingsError("Organization hourly budget cannot exceed the daily budget.")
	case next[3] > next[4]:
		return nil, spendSettingsError("Workflow hourly budget cannot exceed the daily budget.")
	case next[0] > next[1] || next[0] > next[3]:
		return nil, spendSettingsError("Per-run cap cannot exceed an hourly budget, or no run could start.")
	}
	refresh, present := draft["metricRefresh"]
	if present {
		if s, _ := refresh.(string); s != "card" && s != "daily" && s != "weekly" && s != "off" {
			return nil, spendSettingsError("Metric refresh must be card, daily, weekly or off.")
		}
	}
	previous, err := a.spendSettingsFor(ctx, act.OrgID, time.Now())
	if err != nil {
		return nil, err
	}
	metricRefresh := previous.MetricRefresh
	if present {
		metricRefresh = refresh.(string)
	}
	var refreshColumn any
	if metricRefresh != "card" {
		refreshColumn = metricRefresh
	}
	tag, err := a.DB.Exec(ctx, `update spend_limit
	    set run_cap_micros = $2, org_hourly_micros = $3, org_daily_micros = $4,
	        workflow_hourly_micros = $5, workflow_daily_micros = $6, warn_percent = $7,
	        metric_refresh = $9, updated_by_user_id = $8, updated_at = now()
	  where org_id = $1 and workflow_id is null`,
		act.OrgID, int64(next[0]), int64(next[1]), int64(next[2]), int64(next[3]), int64(next[4]), int(warn),
		nullable(act.UserID), refreshColumn)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, spendSettingsError("This organization has no spend limits to update.")
	}
	saved, err := a.spendSettingsFor(ctx, act.OrgID, time.Now())
	if err != nil {
		return nil, err
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "spend_limit", EntityID: act.OrgID, Event: "spend:limit_changed",
		Payload: map[string]any{"actorUserId": auditActor(act.UserID), "previous": spendAuditLimits(previous), "next": spendAuditLimits(saved)}})
	return saved, nil
}

func (a *API) sendSpendResult(w http.ResponseWriter, r *http.Request, settings *spendSettings, err error) {
	var bad spendSettingsError
	switch {
	case errors.As(err, &bad):
		sendError(w, http.StatusBadRequest, string(bad))
	case err != nil:
		a.fail(w, r, err)
	default:
		sendJSON(w, http.StatusOK, settings)
	}
}

// readJSONObject is readJsonBody() in lib/groups-admin.ts: nil unless the
// body is a JSON object.
func readJSONObject(r *http.Request) map[string]any {
	body, _ := readBody(r)
	m, _ := body.(map[string]any)
	return m
}

func (a *API) adminSpendSave(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body := readJSONObject(r)
	if body == nil {
		sendError(w, http.StatusBadRequest, "A JSON body is required.")
		return
	}
	settings, err := a.saveSpendLimits(r.Context(), act, body)
	a.sendSpendResult(w, r, settings, err)
}

func (a *API) adminSpendAlertAck(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("alertId")
	if isUUID(id) {
		tag, err := a.DB.Exec(r.Context(), `update behavior_alert set acknowledged_at = now(), acknowledged_by = $3
		  where org_id = $1 and id = $2::uuid and kind like 'spend.%' and acknowledged_at is null`, act.OrgID, id, nullable(act.UserID))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if tag.RowsAffected() > 0 {
			a.sendSpendSettings(w, r, act.OrgID)
			return
		}
	}
	sendError(w, http.StatusNotFound, "Alert not found or already acknowledged.")
}

// saveWorkflowSpendOverride is saveWorkflowSpendOverride() in settings.ts.
func (a *API) saveWorkflowSpendOverride(ctx context.Context, act actor, workflowID string, hourlyUSD, dailyUSD any) (*spendSettings, error) {
	ceilings := spendCeilingMicros()
	var exists bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from workflow_definition where org_id = $1 and id = $2::uuid)`,
		act.OrgID, workflowID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, spendSettingsError("Workflow not found.")
	}
	var hourly, daily *int64
	for _, v := range []struct {
		in    any
		label string
		ceil  float64
		out   **int64
	}{{hourlyUSD, spendAmountLabels[3], ceilings[3], &hourly}, {dailyUSD, spendAmountLabels[4], ceilings[4], &daily}} {
		if v.in == nil {
			continue
		}
		micros, err := spendAmount(v.in, v.label, v.ceil)
		if err != nil {
			return nil, err
		}
		n := int64(micros)
		*v.out = &n
	}
	if hourly != nil && daily != nil && *hourly > *daily {
		return nil, spendSettingsError("Workflow hourly budget cannot exceed the daily budget.")
	}
	var err error
	if hourly == nil && daily == nil {
		_, err = a.DB.Exec(ctx, `delete from spend_limit where org_id = $1 and workflow_id = $2::uuid`, act.OrgID, workflowID)
	} else {
		_, err = a.DB.Exec(ctx, `insert into spend_limit (org_id, workflow_id, workflow_hourly_micros, workflow_daily_micros, updated_by_user_id)
		   values ($1, $2::uuid, $3, $4, $5)
		   on conflict (org_id, workflow_id) where workflow_id is not null
		   do update set workflow_hourly_micros = excluded.workflow_hourly_micros,
		                 workflow_daily_micros = excluded.workflow_daily_micros,
		                 updated_by_user_id = excluded.updated_by_user_id,
		                 updated_at = now()`,
			act.OrgID, workflowID, hourly, daily, nullable(act.UserID))
	}
	if err != nil {
		return nil, err
	}
	usd := func(v *int64) any {
		if v == nil {
			return nil
		}
		return jsonNumber(microsToUSD(float64(*v)))
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "spend_limit", EntityID: workflowID, Event: "spend:limit_changed",
		Payload: map[string]any{"actorUserId": auditActor(act.UserID), "workflowId": workflowID,
			"next": map[string]any{"hourlyUsd": usd(hourly), "dailyUsd": usd(daily)}}})
	return a.spendSettingsFor(ctx, act.OrgID, time.Now())
}

func (a *API) adminWorkflowSpend(w http.ResponseWriter, r *http.Request) {
	var hourly, daily any
	if r.Method == http.MethodPut {
		body := readJSONObject(r)
		hourly, daily = body["hourlyUsd"], body["dailyUsd"]
	}
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("workflowId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "Workflow not found.")
		return
	}
	settings, err := a.saveWorkflowSpendOverride(r.Context(), act, id, hourly, daily)
	a.sendSpendResult(w, r, settings, err)
}

type workflowAPILimits struct {
	RollingTokenBudget      *float64 `json:"rollingTokenBudget"`
	RollingCostMicrosBudget *float64 `json:"rollingCostMicrosBudget"`
}

// envBudget is Number(process.env[name] ?? fallback); NaN and Infinity
// print as null.
func envBudget(name string, fallback float64) *float64 {
	n := fallback
	if raw, set := os.LookupEnv(name); set {
		var ok bool
		if n, ok = jsNumber(raw); !ok {
			return nil
		}
	}
	if math.IsInf(n, 0) {
		return nil
	}
	return &n
}

func (a *API) readWorkflowAPILimits(ctx context.Context, orgID string) (workflowAPILimits, error) {
	var tokens, cost float64
	err := a.DB.QueryRow(ctx, `select rolling_token_budget::float8, rolling_cost_micros_budget::float8
	   from workflow_api_org_limits where org_id = $1`, orgID).Scan(&tokens, &cost)
	if err == nil {
		return workflowAPILimits{&tokens, &cost}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return workflowAPILimits{}, err
	}
	return workflowAPILimits{
		envBudget("OPENNEKO_WORKFLOW_API_ORG_ROLLING_TOKEN_BUDGET", 1_000_000),
		envBudget("OPENNEKO_WORKFLOW_API_ORG_ROLLING_COST_MICROS_BUDGET", 50_000_000),
	}, nil
}

func (a *API) sendWorkflowAPILimits(w http.ResponseWriter, r *http.Request, orgID string) {
	limits, err := a.readWorkflowAPILimits(r.Context(), orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]workflowAPILimits{"limits": limits})
}

func (a *API) adminWorkflowAPILimits(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	a.sendWorkflowAPILimits(w, r, act.OrgID)
}

func safeIntegerIn(v any, low, high float64) bool {
	n, ok := v.(float64)
	return ok && n == math.Trunc(n) && math.Abs(n) <= 1<<53-1 && n >= low && n <= high
}

func (a *API) adminWorkflowAPILimitsSave(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	tokens, _ := field(body, "rollingTokenBudget")
	cost, _ := field(body, "rollingCostMicrosBudget")
	if !safeIntegerIn(tokens, 1_000, 100_000_000) || !safeIntegerIn(cost, 1_000, 10_000_000_000) {
		sendError(w, http.StatusBadRequest, "Enter a token budget from 1,000 to 100,000,000 and a spend budget from $0.001 to $10,000.")
		return
	}
	if _, err := a.DB.Exec(r.Context(), `insert into workflow_api_org_limits (org_id, rolling_token_budget, rolling_cost_micros_budget)
	   values ($1, $2, $3)
	   on conflict (org_id) do update set
	     rolling_token_budget = excluded.rolling_token_budget,
	     rolling_cost_micros_budget = excluded.rolling_cost_micros_budget,
	     updated_at = now()`, act.OrgID, int64(tokens.(float64)), int64(cost.(float64))); err != nil {
		a.fail(w, r, err)
		return
	}
	a.sendWorkflowAPILimits(w, r, act.OrgID)
}
