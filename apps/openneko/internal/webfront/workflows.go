package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type workflowSummary struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Goal         string     `json:"goal"`
	Enabled      bool       `json:"enabled"`
	Status       string     `json:"status"`
	Cron         *string    `json:"cron"`
	CronTimezone string     `json:"cronTimezone"`
	CronEnabled  bool       `json:"cronEnabled"`
	Steps        storedJSON `json:"steps"`
	CreatedAt    string     `json:"createdAt"`
	UpdatedAt    string     `json:"updatedAt"`
}

func (a *API) workflows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	visible, err := a.workflowVisibility(ctx, act)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := a.DB.Query(ctx, `
		select id::text, name, description, goal, enabled, status, cron, cron_timezone, cron_enabled, steps,
		  created_at, updated_at, owner_user_id
		from workflow_definition where org_id = $1 order by updated_at desc`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []workflowSummary{}
	for rows.Next() {
		var s workflowSummary
		var steps []byte
		var created, updated time.Time
		var owner string
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Goal, &s.Enabled, &s.Status, &s.Cron, &s.CronTimezone,
			&s.CronEnabled, &steps, &created, &updated, &owner); err != nil {
			a.fail(w, r, err)
			return
		}
		if !visible(s.ID, owner) {
			continue
		}
		s.Steps, s.CreatedAt, s.UpdatedAt = storedJSON(steps), jsTime(created), jsTime(updated)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"workflows": out})
}

// workflowHidden is requireWorkflow() in apps/web/src/lib/entitlements.ts: a
// workflow that does not exist is not hidden.
func (a *API) workflowHidden(ctx context.Context, act actor, workflowID string) (bool, error) {
	if !isUUID(workflowID) {
		return false, nil
	}
	var owner string
	err := a.DB.QueryRow(ctx, `select owner_user_id from workflow_definition where org_id = $1 and id = $2`, act.OrgID, workflowID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	visible, err := a.workflowVisibility(ctx, act)
	if err != nil {
		return false, err
	}
	return !visible(workflowID, owner), nil
}

// workflowRunHidden is requireWorkflowRun().
func (a *API) workflowRunHidden(ctx context.Context, act actor, runID string) (bool, error) {
	if !isUUID(runID) {
		return false, nil
	}
	var workflowID string
	err := a.DB.QueryRow(ctx, `select workflow_id::text from workflow_run where org_id = $1 and id = $2`, act.OrgID, runID).Scan(&workflowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.workflowHidden(ctx, act, workflowID)
}

type workflowDetail struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Description         string     `json:"description"`
	Goal                string     `json:"goal"`
	SystemPromptOverlay string     `json:"systemPromptOverlay"`
	Enabled             bool       `json:"enabled"`
	Status              string     `json:"status"`
	Steps               storedJSON `json:"steps"`
	Cron                *string    `json:"cron"`
	CronTimezone        string     `json:"cronTimezone"`
	CronEnabled         bool       `json:"cronEnabled"`
	DailyRunBudget      *int       `json:"dailyRunBudget"`
	RunsToday           int        `json:"runsToday"`
	MinutesSaved30d     int        `json:"minutesSaved30d"`
	CreatedByThreadID   *string    `json:"createdByThreadId"`
	CreatedByRunID      *string    `json:"createdByRunId"`
	CreatedAt           string     `json:"createdAt"`
	UpdatedAt           string     `json:"updatedAt"`
}

type workflowSubscriptionOut struct {
	ID         string     `json:"id"`
	SourceKind string     `json:"sourceKind"`
	Filter     storedJSON `json:"filter"`
	Enabled    bool       `json:"enabled"`
	DebounceMs int        `json:"debounceMs"`
}

type recentRunOut struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	TriggerKind   string     `json:"triggerKind"`
	ExecutionMode *string    `json:"executionMode"`
	QueueAttempts int        `json:"queueAttempts"`
	Progress      storedJSON `json:"progress"`
	Telemetry     storedJSON `json:"telemetry"`
	Summary       *string    `json:"summary"`
	Error         *string    `json:"error"`
	StartedAt     *string    `json:"startedAt"`
	FinishedAt    *string    `json:"finishedAt"`
	DurationMs    *int64     `json:"durationMs"`
	CreatedAt     string     `json:"createdAt"`
}

type recentActionOut struct {
	ID            string  `json:"id"`
	WorkflowRunID *string `json:"workflowRunId"`
	Kind          string  `json:"kind"`
	Target        *string `json:"target"`
	Status        string  `json:"status"`
	RiskLevel     *string `json:"riskLevel"`
	Summary       *string `json:"summary"`
	ApprovedAt    *string `json:"approvedAt"`
	CreatedAt     string  `json:"createdAt"`
}

func durationMs(started, finished *time.Time) *int64 {
	if started == nil || finished == nil {
		return nil
	}
	d := finished.UnixMilli() - started.UnixMilli()
	return &d
}

const sparklineDays = 14

func (a *API) workflowDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("workflowId")
	if hidden, err := a.workflowHidden(ctx, act, id); err != nil {
		a.fail(w, r, err)
		return
	} else if hidden {
		sendError(w, http.StatusNotFound, "Workflow not found")
		return
	}
	notFound := func() { sendError(w, http.StatusNotFound, "workflow not found") }
	if !isUUID(id) {
		notFound()
		return
	}
	var d workflowDetail
	var steps []byte
	var created, updated time.Time
	err = a.DB.QueryRow(ctx, `
		select id::text, name, description, goal, system_prompt_overlay, enabled, status, steps, cron, cron_timezone,
		  cron_enabled, daily_run_budget, created_by_thread_id::text, created_by_run_id::text, created_at, updated_at
		from workflow_definition where org_id = $1 and id = $2`, act.OrgID, id).Scan(
		&d.ID, &d.Name, &d.Description, &d.Goal, &d.SystemPromptOverlay, &d.Enabled, &d.Status, &steps, &d.Cron,
		&d.CronTimezone, &d.CronEnabled, &d.DailyRunBudget, &d.CreatedByThreadID, &d.CreatedByRunID, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound()
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	d.Steps, d.CreatedAt, d.UpdatedAt = storedJSON(steps), jsTime(created), jsTime(updated)

	subs, err := a.DB.Query(ctx, `
		select id::text, source_kind, filter, enabled, debounce_ms from subscription
		where org_id = $1 and workflow_id = $2 order by created_at desc`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	subscriptions, err := pgx.CollectRows(subs, func(row pgx.CollectableRow) (workflowSubscriptionOut, error) {
		var s workflowSubscriptionOut
		var filter []byte
		err := row.Scan(&s.ID, &s.SourceKind, &filter, &s.Enabled, &s.DebounceMs)
		s.Filter = filter
		return s, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	runRows, err := a.DB.Query(ctx, `
		select id::text, status, trigger_kind, execution_mode, queue_attempts, progress, telemetry_summary, summary, error,
		  started_at, finished_at, created_at
		from workflow_run where org_id = $1 and workflow_id = $2 order by created_at desc limit 10`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	recentRuns, err := pgx.CollectRows(runRows, func(row pgx.CollectableRow) (recentRunOut, error) {
		var o recentRunOut
		var progress, telemetry []byte
		var started, finished *time.Time
		var created time.Time
		err := row.Scan(&o.ID, &o.Status, &o.TriggerKind, &o.ExecutionMode, &o.QueueAttempts, &progress, &telemetry,
			&o.Summary, &o.Error, &started, &finished, &created)
		o.Progress, o.Telemetry = progress, telemetry
		o.StartedAt, o.FinishedAt, o.DurationMs, o.CreatedAt = jsTimePtr(started), jsTimePtr(finished), durationMs(started, finished), jsTime(created)
		return o, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	actionRows, err := a.DB.Query(ctx, `
		select a.id::text, a.workflow_run_id::text, a.kind, a.target, a.status, a.risk_level, a.summary, a.approved_at, a.created_at
		from action_request a join workflow_run r on a.workflow_run_id = r.id
		where a.org_id = $1 and r.workflow_id = $2 order by a.created_at desc limit 10`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	recentActions, err := pgx.CollectRows(actionRows, func(row pgx.CollectableRow) (recentActionOut, error) {
		var o recentActionOut
		var approved *time.Time
		var created time.Time
		err := row.Scan(&o.ID, &o.WorkflowRunID, &o.Kind, &o.Target, &o.Status, &o.RiskLevel, &o.Summary, &approved, &created)
		o.ApprovedAt, o.CreatedAt = jsTimePtr(approved), jsTime(created)
		return o, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	since := today.AddDate(0, 0, -(sparklineDays - 1))
	sparkRows, err := a.DB.Query(ctx, `
		select date_trunc('day', created_at)::date::text, count(*)::int from workflow_run
		where org_id = $1 and workflow_id = $2 and created_at >= $3
		group by date_trunc('day', created_at)`, act.OrgID, id, since)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	byDay := map[string]int{}
	var day string
	var count int
	if _, err := pgx.ForEachRow(sparkRows, []any{&day, &count}, func() error {
		byDay[day] = count
		return nil
	}); err != nil {
		a.fail(w, r, err)
		return
	}
	sparkline := make([]int, sparklineDays)
	for i := range sparkline {
		sparkline[i] = byDay[since.AddDate(0, 0, i).Format("2006-01-02")]
	}

	since30d := now.Add(-30 * 24 * time.Hour)
	err = a.DB.QueryRow(ctx, `
		select
		  (select count(*)::int from workflow_run where org_id = $1 and workflow_id = $2 and created_at >= $3),
		  (select coalesce(sum(wr.analysis_minutes_saved), 0)::int from workflow_run r join work_run wr on r.work_run_id = wr.id
		    where r.org_id = $1 and r.workflow_id = $2 and wr.status = 'completed' and r.created_at >= $4)
		  + (select coalesce(sum(a.minutes_saved), 0)::int from action_request a join workflow_run r on a.workflow_run_id = r.id
		    where r.org_id = $1 and r.workflow_id = $2 and a.status = 'executed' and a.created_at >= $4)`,
		act.OrgID, id, today, since30d).Scan(&d.RunsToday, &d.MinutesSaved30d)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	sendJSON(w, http.StatusOK, struct {
		Workflow          workflowDetail            `json:"workflow"`
		Subscriptions     []workflowSubscriptionOut `json:"subscriptions"`
		RecentRuns        []recentRunOut            `json:"recentRuns"`
		RecentActions     []recentActionOut         `json:"recentActions"`
		ActivitySparkline []int                     `json:"activitySparkline"`
	}{d, subscriptions, recentRuns, recentActions, sparkline})
}

var runStatusFilters = map[string][]string{
	"active":    {"queued", "running", "needs_input", "waiting_approval"},
	"completed": {"completed"},
	"failed":    {"failed", "cancelled"},
	"all":       nil,
}

// runListLimit is parseLimit() of the Node route: parseInt(value, 10), 100
// when that is not a positive number, and at most 200.
func runListLimit(raw string) int {
	s := strings.TrimLeftFunc(raw, func(c rune) bool { return unicode.IsSpace(c) || c == '\uFEFF' })
	negative := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		negative = s[0] == '-'
		s = s[1:]
	}
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	digits := strings.TrimLeft(s[:end], "0")
	if end == 0 || negative || digits == "" {
		return 100
	}
	if len(digits) > 3 {
		return 200
	}
	n, _ := strconv.Atoi(digits)
	return min(n, 200)
}

type workflowRunRow struct {
	ID       string `json:"id"`
	Workflow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"workflow"`
	TriggerKind        string     `json:"triggerKind"`
	ExecutionMode      *string    `json:"executionMode"`
	ChainDepth         int        `json:"chainDepth"`
	Status             string     `json:"status"`
	QueueAttempts      int        `json:"queueAttempts"`
	Progress           storedJSON `json:"progress"`
	Telemetry          storedJSON `json:"telemetry"`
	Summary            *string    `json:"summary"`
	Error              *string    `json:"error"`
	StartedAt          *string    `json:"startedAt"`
	FinishedAt         *string    `json:"finishedAt"`
	DurationMs         *int64     `json:"durationMs"`
	CreatedAt          string     `json:"createdAt"`
	UpdatedAt          string     `json:"updatedAt"`
	OutputCount        int        `json:"outputCount"`
	ActionCount        int        `json:"actionCount"`
	PendingActionCount int        `json:"pendingActionCount"`
}

func (a *API) workflowRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	filter := q.Get("status")
	statuses, ok := runStatusFilters[filter]
	if !ok {
		filter = "all"
	}
	limit := runListLimit(q.Get("limit"))
	visible, err := a.workflowVisibility(ctx, act)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := a.DB.Query(ctx, `
		select r.id::text, r.workflow_id::text, d.name, d.owner_user_id, r.trigger_kind, r.execution_mode, r.chain_depth,
		  r.status, r.queue_attempts, r.progress, r.telemetry_summary, r.summary, r.error, r.started_at, r.finished_at,
		  r.created_at, r.updated_at,
		  (select count(*)::int from workflow_output o where o.workflow_run_id = r.id),
		  (select count(*)::int from action_request a where a.workflow_run_id = r.id),
		  (select count(*)::int from action_request a where a.workflow_run_id = r.id and a.status = 'pending_approval')
		from workflow_run r join workflow_definition d on r.workflow_id = d.id
		where r.org_id = $1 and ($2::text[] is null or r.status = any($2))
		order by r.created_at desc limit $3`, act.OrgID, statuses, max(limit*5, 500))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []workflowRunRow{}
	for rows.Next() {
		var o workflowRunRow
		var owner string
		var progress, telemetry []byte
		var started, finished *time.Time
		var created, updated time.Time
		if err := rows.Scan(&o.ID, &o.Workflow.ID, &o.Workflow.Name, &owner, &o.TriggerKind, &o.ExecutionMode, &o.ChainDepth,
			&o.Status, &o.QueueAttempts, &progress, &telemetry, &o.Summary, &o.Error, &started, &finished, &created, &updated,
			&o.OutputCount, &o.ActionCount, &o.PendingActionCount); err != nil {
			a.fail(w, r, err)
			return
		}
		if len(out) == limit || !visible(o.Workflow.ID, owner) {
			continue
		}
		o.Progress, o.Telemetry = progress, telemetry
		o.StartedAt, o.FinishedAt, o.DurationMs = jsTimePtr(started), jsTimePtr(finished), durationMs(started, finished)
		o.CreatedAt, o.UpdatedAt = jsTime(created), jsTime(updated)
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Runs   []workflowRunRow `json:"runs"`
		Status string           `json:"status"`
	}{out, filter})
}

type runDetailWorkflow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Goal        string `json:"goal"`
}

type runDetail struct {
	ID                  string     `json:"id"`
	WorkflowID          string     `json:"workflowId"`
	ThreadID            string     `json:"threadId"`
	WorkRunID           string     `json:"workRunId"`
	TriggerKind         string     `json:"triggerKind"`
	TriggerPayload      storedJSON `json:"triggerPayload"`
	ExecutionMode       *string    `json:"executionMode"`
	TriggerInputPreview storedJSON `json:"triggerInputPreview"`
	ChainDepth          int        `json:"chainDepth"`
	Status              string     `json:"status"`
	Telemetry           storedJSON `json:"telemetry"`
	TerminalResult      storedJSON `json:"terminalResult"`
	Progress            storedJSON `json:"progress"`
	QueueAttempts       int        `json:"queueAttempts"`
	AdmittedAt          *string    `json:"admittedAt"`
	ResultExpiresAt     *string    `json:"resultExpiresAt"`
	ArtifactURL         *string    `json:"artifactUrl"`
	Summary             *string    `json:"summary"`
	Error               *string    `json:"error"`
	StartedAt           *string    `json:"startedAt"`
	FinishedAt          *string    `json:"finishedAt"`
	CreatedAt           string     `json:"createdAt"`
	UpdatedAt           string     `json:"updatedAt"`
}

type runOutput struct {
	ID                  string     `json:"id"`
	Kind                string     `json:"kind"`
	Title               string     `json:"title"`
	Body                string     `json:"body"`
	Payload             storedJSON `json:"payload"`
	ArtifactPath        *string    `json:"artifactPath"`
	Scope               *string    `json:"scope"`
	Topic               *string    `json:"topic"`
	Mood                *string    `json:"mood"`
	TimeWindowStart     *string    `json:"timeWindowStart"`
	TimeWindowEnd       *string    `json:"timeWindowEnd"`
	FreshnessTTLSeconds *int       `json:"freshnessTtlSeconds"`
	CreatedAt           string     `json:"createdAt"`
}

type runAction struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Target          *string    `json:"target"`
	Payload         storedJSON `json:"payload"`
	Scope           string     `json:"scope"`
	RiskLevel       *string    `json:"riskLevel"`
	Status          string     `json:"status"`
	Summary         *string    `json:"summary"`
	ApprovedAt      *string    `json:"approvedAt"`
	RejectionReason *string    `json:"rejectionReason"`
	CreatedAt       string     `json:"createdAt"`
}

type runEvent struct {
	ID        int64           `json:"id"`
	Seq       int64           `json:"seq"`
	Type      json.RawMessage `json:"type"`
	Event     storedJSON      `json:"event"`
	CreatedAt string          `json:"createdAt"`
}

type upstreamRef struct {
	Output struct {
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		Scope     *string `json:"scope"`
		Mood      *string `json:"mood"`
		CreatedAt string  `json:"createdAt"`
	} `json:"output"`
	Workflow *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"workflow"`
	WorkflowRunID *string `json:"workflowRunId"`
}

type runLineage struct {
	TriggeredBySubscriptionID *string      `json:"triggeredBySubscriptionId"`
	TriggeredByOutputID       *string      `json:"triggeredByOutputId"`
	TriggeredByObservationID  *string      `json:"triggeredByObservationId"`
	Upstream                  *upstreamRef `json:"upstream"`
}

var toolEventKinds = map[string]bool{"tool_start": true, "tool_delta": true, "tool_end": true}

// eventType is `event?.type ?? "unknown"` on the stored payload; name is the
// type when it is a string.
func eventType(payload []byte) (json.RawMessage, string, error) {
	unknown := json.RawMessage(`"unknown"`)
	var obj map[string]json.RawMessage
	if json.Unmarshal(payload, &obj) != nil || obj == nil {
		return unknown, "", nil
	}
	raw, ok := obj["type"]
	if !ok || string(raw) == "null" {
		return unknown, "", nil
	}
	var name string
	_ = json.Unmarshal(raw, &name)
	v, err := jsJSON(raw)
	return v, name, err
}

func (a *API) workflowRunDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("workflowRunId")
	if hidden, err := a.workflowRunHidden(ctx, act, id); err != nil {
		a.fail(w, r, err)
		return
	} else if hidden {
		sendError(w, http.StatusNotFound, "Run not found")
		return
	}
	notFound := func() { sendError(w, http.StatusNotFound, "Workflow run not found") }
	if !isUUID(id) {
		notFound()
		return
	}
	includeTools := r.URL.Query().Get("detail") == "tools"

	var run runDetail
	var lineage runLineage
	var payload, preview, telemetry, terminal, progress []byte
	var admitted, expires, started, finished *time.Time
	var created, updated time.Time
	var artifactPath *string
	err = a.DB.QueryRow(ctx, `
		select id::text, workflow_id::text, thread_id::text, work_run_id::text, trigger_kind, trigger_payload, execution_mode,
		  trigger_input_preview, chain_depth, status, telemetry_summary, terminal_result, progress, queue_attempts,
		  admitted_at, result_expires_at, result_artifact_path, summary, error, started_at, finished_at, created_at, updated_at,
		  triggered_by_subscription_id::text, triggered_by_output_id::text, triggered_by_observation_id::text
		from workflow_run where org_id = $1 and id = $2`, act.OrgID, id).Scan(
		&run.ID, &run.WorkflowID, &run.ThreadID, &run.WorkRunID, &run.TriggerKind, &payload, &run.ExecutionMode,
		&preview, &run.ChainDepth, &run.Status, &telemetry, &terminal, &progress, &run.QueueAttempts,
		&admitted, &expires, &artifactPath, &run.Summary, &run.Error, &started, &finished, &created, &updated,
		&lineage.TriggeredBySubscriptionID, &lineage.TriggeredByOutputID, &lineage.TriggeredByObservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound()
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	run.TriggerPayload, run.TriggerInputPreview, run.Telemetry, run.TerminalResult, run.Progress = payload, preview, telemetry, terminal, progress
	run.AdmittedAt, run.ResultExpiresAt = jsTimePtr(admitted), jsTimePtr(expires)
	if artifactPath != nil && *artifactPath != "" {
		u := "/api/v1/workflow-runs/" + run.ID + "/artifact"
		run.ArtifactURL = &u
	}
	run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt = jsTimePtr(started), jsTimePtr(finished), jsTime(created), jsTime(updated)

	var workflow *runDetailWorkflow
	var wf runDetailWorkflow
	err = a.DB.QueryRow(ctx, `select id::text, name, description, goal from workflow_definition where org_id = $1 and id = $2`,
		act.OrgID, run.WorkflowID).Scan(&wf.ID, &wf.Name, &wf.Description, &wf.Goal)
	switch {
	case err == nil:
		workflow = &wf
	case !errors.Is(err, pgx.ErrNoRows):
		a.fail(w, r, err)
		return
	}

	outRows, err := a.DB.Query(ctx, `
		select id::text, kind, title, body, payload, artifact_path, scope, topic, mood, time_window_start, time_window_end,
		  freshness_ttl_seconds, created_at
		from workflow_output where workflow_run_id = $1 order by created_at desc`, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	outputs, err := pgx.CollectRows(outRows, func(row pgx.CollectableRow) (runOutput, error) {
		var o runOutput
		var p []byte
		var start, end *time.Time
		var created time.Time
		err := row.Scan(&o.ID, &o.Kind, &o.Title, &o.Body, &p, &o.ArtifactPath, &o.Scope, &o.Topic, &o.Mood, &start, &end,
			&o.FreshnessTTLSeconds, &created)
		o.Payload, o.TimeWindowStart, o.TimeWindowEnd, o.CreatedAt = p, jsTimePtr(start), jsTimePtr(end), jsTime(created)
		return o, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	actRows, err := a.DB.Query(ctx, `
		select id::text, kind, target, payload, scope, risk_level, status, summary, approved_at, rejection_reason, created_at
		from action_request where org_id = $1 and workflow_run_id = $2 order by created_at desc`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	actions, err := pgx.CollectRows(actRows, func(row pgx.CollectableRow) (runAction, error) {
		var o runAction
		var p []byte
		var approved *time.Time
		var created time.Time
		err := row.Scan(&o.ID, &o.Kind, &o.Target, &p, &o.Scope, &o.RiskLevel, &o.Status, &o.Summary, &approved,
			&o.RejectionReason, &created)
		o.Payload, o.ApprovedAt, o.CreatedAt = p, jsTimePtr(approved), jsTime(created)
		return o, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	evRows, err := a.DB.Query(ctx, `
		select id, payload, created_at from work_run_event
		where org_id = $1 and run_id = $2 and id > 0 order by id`, act.OrgID, run.WorkRunID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	events := []runEvent{}
	var evID int64
	var evPayload []byte
	var evCreated time.Time
	if _, err := pgx.ForEachRow(evRows, []any{&evID, &evPayload, &evCreated}, func() error {
		typ, name, err := eventType(evPayload)
		if err != nil || (!includeTools && toolEventKinds[name]) {
			return err
		}
		events = append(events, runEvent{evID, evID, typ, storedJSON(append([]byte(nil), evPayload...)), jsTime(evCreated)})
		return nil
	}); err != nil {
		a.fail(w, r, err)
		return
	}

	if lineage.TriggeredByOutputID != nil {
		up, err := a.upstreamOutput(ctx, act.OrgID, *lineage.TriggeredByOutputID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		lineage.Upstream = up
	}

	sendJSON(w, http.StatusOK, struct {
		Workflow *runDetailWorkflow `json:"workflow"`
		Run      runDetail          `json:"run"`
		Outputs  []runOutput        `json:"outputs"`
		Actions  []runAction        `json:"actions"`
		Events   []runEvent         `json:"events"`
		Lineage  runLineage         `json:"lineage"`
	}{workflow, run, outputs, actions, events, lineage})
}

func (a *API) upstreamOutput(ctx context.Context, orgID, outputID string) (*upstreamRef, error) {
	var up upstreamRef
	var created time.Time
	var upstreamRun *string
	err := a.DB.QueryRow(ctx, `
		select o.id::text, o.title, o.scope, o.mood, o.created_at, r.id::text
		from workflow_output o left join workflow_run r on r.id = o.workflow_run_id
		where o.org_id = $1 and o.id = $2`, orgID, outputID).Scan(
		&up.Output.ID, &up.Output.Title, &up.Output.Scope, &up.Output.Mood, &created, &upstreamRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	up.Output.CreatedAt, up.WorkflowRunID = jsTime(created), upstreamRun
	if upstreamRun == nil {
		return &up, nil
	}
	up.Workflow = &struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}{}
	err = a.DB.QueryRow(ctx, `
		select d.id::text, d.name from workflow_run r join workflow_definition d on d.id = r.workflow_id
		where r.id = $1`, *upstreamRun).Scan(&up.Workflow.ID, &up.Workflow.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		up.Workflow = nil
		err = nil
	}
	return &up, err
}

type workflowAPIError struct {
	status        int
	code, message string
}

func (e *workflowAPIError) Error() string { return e.message }

// workflowAPIFile is resolveWorkflowApiPhysicalFile() in
// packages/llm/src/workflows/api-admission.ts; "" means the file is not
// inside the org workspace.
func workflowAPIFile(orgID, relativePath string) (string, error) {
	root := filepath.Clean(orgAgentRoot(orgID))
	candidate := filepath.Join(root, relativePath)
	if filepath.IsAbs(relativePath) {
		candidate = filepath.Clean(relativePath)
	}
	if candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return "", errors.New("workflow API file path escaped the organization workspace")
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil
	}
	physicalFile, err := filepath.EvalSymlinks(candidate)
	if err != nil || !isWithin(physicalRoot, physicalFile) {
		return "", nil
	}
	return physicalFile, nil
}

func (a *API) workflowRunArtifact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("workflowRunId")
	if hidden, err := a.workflowRunHidden(ctx, act, id); err != nil {
		a.fail(w, r, err)
		return
	} else if hidden {
		sendError(w, http.StatusNotFound, "Run not found")
		return
	}
	file, size, err := a.operatorArtifact(ctx, act.OrgID, id)
	var apiErr *workflowAPIError
	if errors.As(err, &apiErr) {
		w.Header().Set("Cache-Control", "no-store, private")
		sendJSON(w, apiErr.status, map[string]any{"error": map[string]string{"code": apiErr.code, "message": apiErr.message}})
		return
	}
	if err != nil {
		log.Printf("[workflow-api-artifact] operator download failed: %v", err)
		sendJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "internal_error", "message": "Artifact download failed."}})
		return
	}
	defer file.Close()
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Content-Disposition", `attachment; filename="workflow-`+id+`.csv"`)
	h.Set("Cache-Control", "no-store, private")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, file)
	}
}

// operatorArtifact is getWorkflowApiArtifactForOperator().
func (a *API) operatorArtifact(ctx context.Context, orgID, runID string) (*os.File, int64, error) {
	notFound := &workflowAPIError{http.StatusNotFound, "run_not_found", "Workflow run not found."}
	if !isUUID(runID) {
		return nil, 0, notFound
	}
	var path *string
	var expires *time.Time
	var status string
	var maxBytes int64
	err := a.DB.QueryRow(ctx, `
		select run.result_artifact_path, run.result_expires_at, run.status, access.max_artifact_bytes
		from workflow_run run
		join workflow_api_access access on access.workflow_id = run.workflow_id and access.org_id = run.org_id
		where run.id = $1 and run.org_id = $2 and run.trigger_kind = 'api'`, runID, orgID).Scan(&path, &expires, &status, &maxBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, notFound
	}
	if err != nil {
		return nil, 0, err
	}
	if expires == nil || !expires.After(time.Now()) {
		return nil, 0, &workflowAPIError{http.StatusGone, "result_expired", "The retained API result has expired."}
	}
	if status != "completed" || path == nil || *path == "" {
		return nil, 0, &workflowAPIError{http.StatusNotFound, "artifact_not_ready", "The workflow artifact is not available."}
	}
	unavailable := &workflowAPIError{http.StatusGone, "artifact_unavailable", "The workflow artifact is unavailable."}
	physical, err := workflowAPIFile(orgID, *path)
	if err != nil {
		return nil, 0, err
	}
	if physical == "" {
		return nil, 0, unavailable
	}
	f, err := os.Open(physical)
	if err != nil {
		return nil, 0, unavailable
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		f.Close()
		return nil, 0, unavailable
	}
	return f, info.Size(), nil
}
