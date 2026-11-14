package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

const briefingCardSentinel = "::neko-briefing-card::"

type threadSummary struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	LastMessageAt string `json:"lastMessageAt"`
}

func untitled(title string) string {
	if title == "" {
		return "Untitled thread"
	}
	return title
}

func (a *API) workThreads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := a.DB.Query(ctx, `
		select id::text, title, created_at, updated_at, last_message_at from work_thread t
		where org_id = $1 and channel = 'web' and created_by_user_id = $2
		  and not exists (select 1 from workflow_run wr where wr.thread_id = t.id)
		order by last_message_at desc, created_at desc`, act.OrgID, nullable(act.UserID))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	threads, err := pgx.CollectRows(rows, scanThreadSummary)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"threads": threads})
}

func scanThreadSummary(row pgx.CollectableRow) (threadSummary, error) {
	var s threadSummary
	var created, updated, last time.Time
	if err := row.Scan(&s.ID, &s.Title, &created, &updated, &last); err != nil {
		return s, err
	}
	s.Title = untitled(s.Title)
	s.CreatedAt, s.UpdatedAt, s.LastMessageAt = jsTime(created), jsTime(updated), jsTime(last)
	return s, nil
}

func seedID(body any, name string) string {
	s, _ := fieldString(body, name)
	return s
}

func (a *API) createThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	title, _ := fieldString(body, "title")
	title = strings.TrimFunc(title, isJSSpace)
	metricID, runID, actionID := seedID(body, "seedMetricId"), seedID(body, "seedWorkflowRunId"), seedID(body, "seedActionRequestId")
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var seeds []string
	var card string
	if runID != "" {
		name, message, err := a.workflowRunContext(ctx, act.OrgID, runID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if message != "" {
			seeds = append(seeds, message)
			if title == "" {
				title = "Follow-up · " + name
			}
		}
	}
	if actionID != "" {
		label, message, err := a.actionRequestContext(ctx, act, actionID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if message != "" {
			seeds = append(seeds, message)
			if title == "" {
				title = "Follow-up · " + label
			}
		}
	}
	if metricID != "" {
		var cardTitle string
		if card, cardTitle, err = a.briefingCardSeed(ctx, act.OrgID, metricID); err != nil {
			a.fail(w, r, err)
			return
		}
		if card != "" && title == "" {
			title = cardTitle
		}
	}
	if card != "" {
		seeds = append([]string{card}, seeds...)
	}
	var out threadSummary
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		row, err := tx.Query(ctx, `insert into work_thread (org_id, title, channel, created_by_user_id, backend_state)
			values ($1, $2, 'web', $3, '{}') returning id::text, title, created_at, updated_at, last_message_at`, act.OrgID, title, nullable(act.UserID))
		if err != nil {
			return err
		}
		if out, err = pgx.CollectExactlyOneRow(row, scanThreadSummary); err != nil {
			return err
		}
		for _, m := range seeds {
			if _, err := tx.Exec(ctx, `insert into work_message (org_id, thread_id, run_id, role, content) values ($1, $2, null, 'user', $3)`,
				act.OrgID, out.ID, m); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update work_thread set updated_at = now(), last_message_at = now() where id = $1`, out.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"thread": out})
}

// enINShort is Date.toLocaleString("en-IN", { month: "short", day:
// "numeric", hour: "numeric", minute: "2-digit", hour12: true }) in the
// server's time zone.
func enINShort(t time.Time) string {
	t = t.In(time.Local)
	month := t.Format("Jan")
	if month == "Sep" {
		month = "Sept"
	}
	return fmt.Sprintf("%d %s, %s", t.Day(), month, t.Format("3:04 pm"))
}

var jsWhitespaceRun = regexp.MustCompile(`[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)

// jsSlice is s.slice(0, n) on a JavaScript string; a cut surrogate pair
// becomes U+FFFD, as Node writes it.
func jsSlice(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

func (a *API) workflowRunContext(ctx context.Context, orgID, runID string) (name, message string, err error) {
	if !isUUID(runID) {
		return "", "", nil
	}
	var status, trigger, workflowID string
	var started *time.Time
	var created time.Time
	err = a.DB.QueryRow(ctx, `select status, trigger_kind, workflow_id::text, started_at, created_at from workflow_run where org_id = $1 and id = $2`,
		orgID, runID).Scan(&status, &trigger, &workflowID, &started, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	var goal *string
	err = a.DB.QueryRow(ctx, `select name, goal from workflow_definition where org_id = $1 and id = $2`, orgID, workflowID).Scan(&name, &goal)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	when := created
	if started != nil {
		when = *started
	}
	lines := []string{"I want to follow up on a workflow run.", "", "Workflow: " + name, "Run: " + status + " · " + trigger + " · " + enINShort(when)}
	if goal != nil && *goal != "" {
		lines = append(lines, "Goal: "+*goal)
	}
	rows, err := a.DB.Query(ctx, `select title, coalesce(body, ''), coalesce(mood, ''), coalesce(scope::text, '') from workflow_output
		where workflow_run_id = $1 order by created_at desc`, runID)
	if err != nil {
		return "", "", err
	}
	outputs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Title, Body, Mood, Scope string }])
	if err != nil {
		return "", "", err
	}
	if len(outputs) > 0 {
		lines = append(lines, "", "Outputs produced:")
		for _, o := range outputs {
			line := "- " + o.Title
			if o.Mood != "" {
				line += " [" + o.Mood + "]"
			}
			if o.Scope != "" {
				line += " (scope " + o.Scope + ")"
			}
			lines = append(lines, line)
			if o.Body != "" {
				lines = append(lines, "  "+jsSlice(jsWhitespaceRun.ReplaceAllString(o.Body, " "), 400))
			}
		}
	}
	rows, err = a.DB.Query(ctx, `select kind, coalesce(target, ''), status, coalesce(summary, ''), coalesce(risk_level, '') from action_request
		where org_id = $1 and workflow_run_id = $2 order by created_at desc`, orgID, runID)
	if err != nil {
		return "", "", err
	}
	actions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Kind, Target, Status, Summary, Risk string }])
	if err != nil {
		return "", "", err
	}
	if len(actions) > 0 {
		lines = append(lines, "", "Actions proposed:")
		for _, ar := range actions {
			line := "- " + ar.Kind
			if ar.Target != "" {
				line += " → " + ar.Target
			}
			if ar.Risk != "" {
				line += " [risk " + ar.Risk + "]"
			}
			lines = append(lines, line+" ("+ar.Status+") — "+ar.Summary)
		}
	}
	lines = append(lines, "", "(I'll ask my question below.)")
	return name, strings.Join(lines, "\n"), nil
}

func (a *API) actionRequestContext(ctx context.Context, act actor, id string) (label, message string, err error) {
	if !isUUID(id) {
		return "", "", nil
	}
	var kind, target, status, scope, risk, summary, approvedBy, rejection string
	var runID, policyID *string
	var approvedAt *time.Time
	var created time.Time
	var payload []byte
	err = a.DB.QueryRow(ctx, `select kind, coalesce(target, ''), status, scope, coalesce(risk_level, ''), coalesce(summary, ''),
		coalesce(approved_by_user_id, ''), coalesce(rejection_reason, ''), workflow_run_id::text, policy_id::text, approved_at, created_at, payload
		from action_request where org_id = $1 and id = $2`, act.OrgID, id).Scan(&kind, &target, &status, &scope, &risk, &summary,
		&approvedBy, &rejection, &runID, &policyID, &approvedAt, &created, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if kind == "source_config_admin" && !act.Admin {
		return "", "", nil
	}
	var workflowName, policyName string
	if runID != nil {
		err = a.DB.QueryRow(ctx, `select d.name from workflow_run r join workflow_definition d on r.workflow_id = d.id
			where r.org_id = $1 and r.id = $2`, act.OrgID, *runID).Scan(&workflowName)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
	}
	if policyID != nil {
		err = a.DB.QueryRow(ctx, `select name from action_policy where org_id = $1 and id = $2`, act.OrgID, *policyID).Scan(&policyName)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
	}
	rows, err := a.DB.Query(ctx, `select executor, status, coalesce(error, '') from action_execution
		where action_request_id = $1 order by created_at desc`, id)
	if err != nil {
		return "", "", err
	}
	executions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Executor, Status, Error string }])
	if err != nil {
		return "", "", err
	}
	label = kind
	if target != "" {
		label += " → " + target
	}
	statusLine := "Status: " + status + " · " + scope
	if risk != "" {
		statusLine += " · risk " + risk
	}
	lines := []string{"I want to follow up on an action receipt.", "", "Action: " + label, statusLine, "Proposed: " + enINShort(created)}
	if workflowName != "" {
		lines = append(lines, "From workflow: "+workflowName)
	}
	if summary != "" {
		lines = append(lines, "Summary: "+summary)
	}
	switch {
	case approvedBy != "":
		lines = append(lines, "Approved by: operator "+approvedBy)
	case policyName != "":
		lines = append(lines, `Approved by: rule "`+policyName+`"`)
	case approvedAt != nil:
		lines = append(lines, "Approved automatically.")
	}
	if rejection != "" {
		lines = append(lines, "Rejection reason: "+rejection)
	}
	if len(executions) > 0 {
		lines = append(lines, "", "Executions:")
		for _, e := range executions {
			line := "- executor=" + e.Executor + " status=" + e.Status
			if e.Error != "" {
				line += ` error="` + e.Error + `"`
			}
			lines = append(lines, line)
		}
	}
	if pretty, ok, err := prettyPayload(payload); err != nil {
		return "", "", err
	} else if ok {
		lines = append(lines, "", "Payload:", "```json", pretty, "```")
	}
	lines = append(lines, "", "(I'll ask my question below.)")
	return label, strings.Join(lines, "\n"), nil
}

// prettyPayload is JSON.stringify(payload, null, 2) for a payload that
// Object.keys() finds keys in.
func prettyPayload(raw []byte) (string, bool, error) {
	if raw == nil {
		return "", false, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false, err
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			return "", false, nil
		}
	case []any:
		if len(x) == 0 {
			return "", false, nil
		}
	case string:
		if x == "" {
			return "", false, nil
		}
	default:
		return "", false, nil
	}
	js, err := jsJSON(raw)
	if err != nil {
		return "", false, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, js, "", "  "); err != nil {
		return "", false, err
	}
	return out.String(), true, nil
}

// briefingCardSeed is loadBriefingCardForSeed() of the Node route: the
// sentinel and the card as JSON, or "" when the metric is not in the org.
func (a *API) briefingCardSeed(ctx context.Context, orgID, metricID string) (message, title string, err error) {
	if !isUUID(metricID) {
		return "", "", nil
	}
	var source, chartHint *string
	err = a.DB.QueryRow(ctx, `select title, source, chart_hint from metric where id = $1 and org_id = $2`, metricID, orgID).Scan(&title, &source, &chartHint)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	var snapStatus *string
	var payload []byte
	err = a.DB.QueryRow(ctx, `select status, payload from metric_snapshot where metric_id = $1 order by captured_at desc limit 1`, metricID).Scan(&snapStatus, &payload)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	p := map[string]json.RawMessage{}
	if payload != nil {
		if js, err := jsJSON(payload); err == nil {
			_ = json.Unmarshal(js, &p)
		}
	}
	pick := func(key string, fallbacks ...any) json.RawMessage {
		if v, ok := p[key]; ok && key != "" && string(v) != "null" {
			return v
		}
		for _, f := range fallbacks {
			switch x := f.(type) {
			case *string:
				if x != nil {
					return jsonString(*x)
				}
			case string:
				return jsonString(x)
			case json.RawMessage:
				return x
			}
		}
		return nil
	}
	var detail []string
	for _, key := range []string{"insightText", "detailText"} {
		if s, ok := jsTruthyString(p[key]); ok {
			detail = append(detail, s)
		}
	}
	fields := []struct {
		key   string
		value json.RawMessage
	}{
		{"id", jsonString("seed-" + metricID)},
		{"metricId", jsonString(metricID)},
		{"source", pick("", source, "briefing")},
		{"state", jsonString("ok")},
		{"mood", pick("mood", snapStatus, "good")},
		{"text", jsonString(title)},
		{"metric", pick("headlineMetric", "")},
		{"label", pick("headlineLabel", "")},
		{"detail", jsonString(strings.Join(detail, " "))},
		{"chart", pick("chartType", chartHint, "kpi")},
		{"chartData", pick("chartData", json.RawMessage("[]"))},
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(f.key))
		b.WriteByte(':')
		b.Write(f.value)
	}
	b.WriteByte('}')
	return briefingCardSentinel + b.String(), title, nil
}

func jsonString(s string) json.RawMessage {
	var b bytes.Buffer
	_ = jsString(&b, s)
	return b.Bytes()
}

// jsTruthyString is String(v) for a truthy string, number or true.
func jsTruthyString(raw json.RawMessage) (string, bool) {
	var v any
	if raw == nil || json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, x != ""
	case float64:
		return string(raw), x != 0
	case bool:
		return "true", x
	}
	return "", false
}

// jsNumber is Number(s) for a query value; ok is false for NaN.
func jsNumber(s string) (float64, bool) {
	s = strings.TrimFunc(s, isJSSpace)
	if s == "" {
		return 0, true
	}
	switch strings.TrimPrefix(s, "+") {
	case "Infinity":
		return math.Inf(1), true
	}
	if s == "-Infinity" {
		return math.Inf(-1), true
	}
	if len(s) > 2 && s[0] == '0' && strings.ContainsRune("xXoObB", rune(s[1])) {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		n, err := strconv.ParseUint(s[2:], base, 64)
		return float64(n), err == nil
	}
	if !jsDecimal.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return n, true
}

var jsDecimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

const threadPageMax = 100

type threadBundle struct {
	Thread struct {
		ID            string     `json:"id"`
		Title         string     `json:"title"`
		BackendState  storedJSON `json:"backendState"`
		CreatedAt     string     `json:"createdAt"`
		UpdatedAt     string     `json:"updatedAt"`
		LastMessageAt string     `json:"lastMessageAt"`
	} `json:"thread"`
	Runs        []bundleRun     `json:"runs"`
	Messages    []bundleMessage `json:"messages"`
	EventsByRun orderedEvents   `json:"eventsByRun"`
	HasOlder    *bool           `json:"hasOlder,omitempty"`
}

type bundleRun struct {
	ID                   string  `json:"id"`
	Backend              string  `json:"backend"`
	Status               string  `json:"status"`
	Error                *string `json:"error"`
	CreatedAt            string  `json:"createdAt"`
	FinishedAt           *string `json:"finishedAt"`
	AnalysisMinutesSaved *int64  `json:"analysisMinutesSaved"`
	AnalysisMinutesBasis *string `json:"analysisMinutesBasis"`
	ActorRole            *string `json:"actorRole"`
	RoutineName          *string `json:"routineName"`
}

type bundleMessage struct {
	ID        string  `json:"id"`
	RunID     *string `json:"runId"`
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	CreatedAt string  `json:"createdAt"`
}

type orderedEvents struct {
	order []string
	byRun map[string][]json.RawMessage
}

func (o orderedEvents) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, run := range o.order {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(run))
		b.WriteString(":[")
		for j, e := range o.byRun[run] {
			if j > 0 {
				b.WriteByte(',')
			}
			b.Write(e)
		}
		b.WriteByte(']')
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (a *API) workThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("threadId")
	if ok, err := a.workThreadOwned(ctx, act, id); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "Thread not found")
		}
		return
	}
	limit, page := 0, false
	if n, ok := jsNumber(r.URL.Query().Get("limit")); ok && n >= 1 && n == math.Trunc(n) && !math.IsInf(n, 0) {
		limit, page = int(math.Min(n, threadPageMax)), true
	}
	bundle, found, err := a.threadBundle(ctx, act.OrgID, id, page, limit, r.URL.Query().Get("before"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !found {
		sendError(w, http.StatusNotFound, "Thread not found")
		return
	}
	sendJSON(w, http.StatusOK, bundle)
}

const runColumns = `id::text, backend, status, error, created_at, finished_at, analysis_minutes_saved, analysis_minutes_basis, actor_role`

func scanBundleRun(row pgx.CollectableRow) (bundleRun, error) {
	var out bundleRun
	var created time.Time
	var finished *time.Time
	if err := row.Scan(&out.ID, &out.Backend, &out.Status, &out.Error, &created, &finished,
		&out.AnalysisMinutesSaved, &out.AnalysisMinutesBasis, &out.ActorRole); err != nil {
		return out, err
	}
	out.CreatedAt, out.FinishedAt = jsTime(created), jsTimePtr(finished)
	if out.ActorRole != nil && *out.ActorRole != "admin" && *out.ActorRole != "member" && *out.ActorRole != "service" {
		out.ActorRole = nil
	}
	return out, nil
}

func (a *API) runStartedAt(ctx context.Context, orgID, runID string) (*time.Time, error) {
	if !isUUID(runID) {
		return nil, nil
	}
	var at time.Time
	err := a.DB.QueryRow(ctx, `select least(r.created_at, coalesce((select min(m.created_at) from work_message m where m.run_id = r.id), r.created_at))
		from work_run r where r.org_id = $1 and r.id = $2`, orgID, runID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// threadBundle is getWorkThreadBundle() in packages/llm/src/work/store.ts.
func (a *API) threadBundle(ctx context.Context, orgID, threadID string, page bool, limit int, before string) (*threadBundle, bool, error) {
	var b threadBundle
	var created, updated, last time.Time
	err := a.DB.QueryRow(ctx, `select id::text, title, backend_state, created_at, updated_at, last_message_at from work_thread where org_id = $1 and id = $2`,
		orgID, threadID).Scan(&b.Thread.ID, &b.Thread.Title, (*[]byte)(&b.Thread.BackendState), &created, &updated, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	b.Thread.Title = untitled(b.Thread.Title)
	b.Thread.CreatedAt, b.Thread.UpdatedAt, b.Thread.LastMessageAt = jsTime(created), jsTime(updated), jsTime(last)
	if b.Thread.BackendState == nil {
		b.Thread.BackendState = storedJSON("{}")
	}

	messageSQL := `select id::text, run_id::text, role, content, created_at from work_message where org_id = $1 and thread_id = $2`
	messageArgs := []any{orgID, threadID}
	eventFilter := ""
	var runIDs []string
	if !page {
		rows, err := a.DB.Query(ctx, `select `+runColumns+` from work_run where org_id = $1 and thread_id = $2 order by created_at`, orgID, threadID)
		if err != nil {
			return nil, false, err
		}
		if b.Runs, err = pgx.CollectRows(rows, scanBundleRun); err != nil {
			return nil, false, err
		}
	} else {
		var upper *time.Time
		if before != "" {
			if upper, err = a.runStartedAt(ctx, orgID, before); err != nil {
				return nil, false, err
			}
		}
		rows, err := a.DB.Query(ctx, `select `+runColumns+` from work_run where org_id = $1 and thread_id = $2
			and ($3::timestamptz is null or created_at < $3) order by created_at desc limit $4`, orgID, threadID, upper, limit+1)
		if err != nil {
			return nil, false, err
		}
		newest, err := pgx.CollectRows(rows, scanBundleRun)
		if err != nil {
			return nil, false, err
		}
		hasOlder := len(newest) > limit
		if hasOlder {
			newest = newest[:limit]
		}
		b.Runs = make([]bundleRun, len(newest))
		for i, run := range newest {
			b.Runs[len(newest)-1-i] = run
		}
		b.HasOlder = &hasOlder
		var lower *time.Time
		if hasOlder && len(b.Runs) > 0 {
			if lower, err = a.runStartedAt(ctx, orgID, b.Runs[0].ID); err != nil {
				return nil, false, err
			}
		}
		for _, run := range b.Runs {
			runIDs = append(runIDs, run.ID)
		}
		messageSQL += ` and (run_id = any($3::uuid[]) or (run_id is null and ($4::timestamptz is null or created_at >= $4)
			and ($5::timestamptz is null or created_at < $5)))`
		messageArgs = append(messageArgs, runIDs, lower, upper)
		eventFilter = ` and run_id = any($3::uuid[])`
	}
	if b.Runs == nil {
		b.Runs = []bundleRun{}
	}

	rows, err := a.DB.Query(ctx, messageSQL+` order by created_at`, messageArgs...)
	if err != nil {
		return nil, false, err
	}
	b.Messages, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (bundleMessage, error) {
		var m bundleMessage
		var at time.Time
		err := row.Scan(&m.ID, &m.RunID, &m.Role, &m.Content, &at)
		m.CreatedAt = jsTime(at)
		return m, err
	})
	if err != nil {
		return nil, false, err
	}
	if b.Messages == nil {
		b.Messages = []bundleMessage{}
	}

	b.EventsByRun.byRun = map[string][]json.RawMessage{}
	if !page || len(runIDs) > 0 {
		args := []any{orgID, threadID}
		if page {
			args = append(args, runIDs)
		}
		rows, err := a.DB.Query(ctx, `select run_id::text, payload from work_run_event where org_id = $1 and thread_id = $2`+eventFilter+` order by id`, args...)
		if err != nil {
			return nil, false, err
		}
		var runID string
		var payload []byte
		_, err = pgx.ForEachRow(rows, []any{&runID, &payload}, func() error {
			e, err := cappedToolResult(payload)
			if err != nil {
				return err
			}
			if _, ok := b.EventsByRun.byRun[runID]; !ok {
				b.EventsByRun.order = append(b.EventsByRun.order, runID)
			}
			b.EventsByRun.byRun[runID] = append(b.EventsByRun.byRun[runID], e)
			return nil
		})
		if err != nil {
			return nil, false, err
		}
	}

	if len(b.Runs) > 0 {
		ids := make([]string, len(b.Runs))
		for i, run := range b.Runs {
			ids[i] = run.ID
		}
		rows, err := a.DB.Query(ctx, `select wr.work_run_id::text, d.name from workflow_run wr join workflow_definition d on d.id = wr.workflow_id
			where wr.org_id = $1 and wr.work_run_id = any($2::uuid[])`, orgID, ids)
		if err != nil {
			return nil, false, err
		}
		routine := map[string]string{}
		var runID, name string
		if _, err := pgx.ForEachRow(rows, []any{&runID, &name}, func() error { routine[runID] = name; return nil }); err != nil {
			return nil, false, err
		}
		for i := range b.Runs {
			if n, ok := routine[b.Runs[i].ID]; ok {
				b.Runs[i].RoutineName = &n
			}
		}
	}
	return &b, true, nil
}

const maxThreadToolResult = 32_000

// cappedToolResult is cappedToolResult() in packages/llm/src/work/store.ts:
// a tool_end result over 32,000 characters is cut, in place.
func cappedToolResult(payload []byte) (json.RawMessage, error) {
	js, err := jsJSON(payload)
	if err != nil {
		return nil, err
	}
	var head struct {
		Type   string          `json:"type"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(js, &head) != nil || head.Type != "tool_end" || head.Result == nil {
		return js, nil
	}
	text := string(head.Result)
	var s string
	if json.Unmarshal(head.Result, &s) == nil {
		text = s
	}
	units := utf16.Encode([]rune(text))
	if len(units) <= maxThreadToolResult {
		return js, nil
	}
	cut := string(utf16.Decode(units[:maxThreadToolResult])) + "\n… " + groupThousands(len(units)-maxThreadToolResult) + " more characters not shown"
	return replaceMember(js, "result", jsonString(cut))
}

func groupThousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// replaceMember sets one member of a JSON object and keeps the member order.
func replaceMember(obj json.RawMessage, key string, value json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i := 0; dec.More(); i++ {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if k == key {
			raw = value
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(k.(string)))
		b.WriteByte(':')
		b.Write(raw)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (a *API) deleteThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("threadId")
	if ok, err := a.workThreadOwned(ctx, act, id); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "Thread not found")
		}
		return
	}
	tag, err := a.DB.Exec(ctx, `delete from work_thread where org_id = $1 and id = $2`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		sendError(w, http.StatusNotFound, "Thread not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) truncateThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	messageID, _ := fieldString(body, "messageId")
	messageID = strings.TrimFunc(messageID, isJSSpace)
	if messageID == "" {
		sendError(w, http.StatusBadRequest, "messageId is required")
		return
	}
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	threadID := r.PathValue("threadId")
	if ok, err := a.workThreadOwned(ctx, act, threadID); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "Thread not found")
		}
		return
	}
	var runID *string
	var role string
	if isUUID(messageID) {
		err = a.DB.QueryRow(ctx, `select run_id::text, role from work_message where id = $1`, messageID).Scan(&runID, &role)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.fail(w, r, err)
			return
		}
	}
	if runID == nil {
		sendError(w, http.StatusNotFound, "Message not found")
		return
	}
	if role != "user" {
		sendError(w, http.StatusBadRequest, "Only user messages can be truncated from")
		return
	}
	found := false
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var at time.Time
		err := tx.QueryRow(ctx, `select created_at from work_run where org_id = $1 and thread_id = $2 and id = $3`, act.OrgID, threadID, *runID).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		for _, q := range []string{
			`delete from work_message where org_id = $1 and thread_id = $2 and created_at >= $3`,
			`delete from work_run where org_id = $1 and thread_id = $2 and created_at >= $3`,
		} {
			if _, err := tx.Exec(ctx, q, act.OrgID, threadID, at); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `update work_thread set backend_state = '{}', updated_at = now(), last_message_at = now() where id = $1`, threadID)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !found {
		sendError(w, http.StatusNotFound, "Run not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) workRunEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	runID := r.PathValue("runId")
	if ok, err := a.workRunOwned(ctx, act, runID); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "Run not found")
		}
		return
	}
	rows, err := a.DB.Query(ctx, `select payload from work_run_event where org_id = $1 and run_id = $2 order by id`, act.OrgID, runID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	events, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]storedJSON, len(events))
	for i, e := range events {
		out[i] = e
	}
	sendJSON(w, http.StatusOK, struct {
		RunID  string       `json:"runId"`
		Events []storedJSON `json:"events"`
	}{runID, out})
}

// runInThread is getAuthorizedWorkThread() plus getWorkRun() with the run's
// thread checked.
func (a *API) runInThread(ctx context.Context, act actor, threadID, runID string) (bool, error) {
	if ok, err := a.workThreadOwned(ctx, act, threadID); err != nil || !ok {
		return false, err
	}
	if !isUUID(runID) {
		return false, nil
	}
	var found bool
	err := a.DB.QueryRow(ctx, `select exists (select 1 from work_run where org_id = $1 and id = $2 and thread_id = $3)`, act.OrgID, runID, threadID).Scan(&found)
	return found, err
}

var timingLimits = map[string]float64{
	"acknowledgementMs": 3_600_000, "streamOpenMs": 3_600_000, "firstEventMs": 3_600_000, "firstOutputMs": 3_600_000,
	"paintOpportunityMs": 3_600_000, "doneMs": 3_600_000, "documentVisible": 1,
}

// runTiming logs the browser's startup timings as the startup_timing line
// of packages/telemetry/src/startup.ts. Only the known keys, as bounded
// numbers, are accepted.
func (a *API) runTiming(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	threadID, runID := r.PathValue("threadId"), r.PathValue("runId")
	if ok, err := a.runInThread(ctx, act, threadID, runID); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	values, ok := timingValues(r)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var b bytes.Buffer
	member := func(k string, v json.RawMessage) {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.Write(jsonString(k))
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('{')
	member("type", jsonString("startup_timing"))
	member("timestamp", jsonString(jsTime(time.Now())))
	member("runId", jsonString(runID))
	member("threadId", jsonString(threadID))
	member("version", jsonString(firstEnv("unknown", "OPENNEKO_VERSION", "NEXT_PUBLIC_APP_VERSION")))
	member("commit", jsonString(firstEnv("unknown", "OPENNEKO_COMMIT", "GIT_COMMIT")))
	member("pid", json.RawMessage(strconv.Itoa(os.Getpid())))
	member("elapsedMs", json.RawMessage(strconv.FormatFloat(float64(time.Since(started).Microseconds())/1000, 'f', -1, 64)))
	member("phase", jsonString("browser.startup"))
	member("event", json.RawMessage("true"))
	member("origin", jsonString("client_reported"))
	for _, v := range values {
		member(v.key, v.value)
	}
	b.WriteByte('}')
	fmt.Fprintln(os.Stdout, b.String())
	w.WriteHeader(http.StatusNoContent)
}

func firstEnv(fallback string, names ...string) string {
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok {
			return v
		}
	}
	return fallback
}

type timingValue struct {
	key   string
	value json.RawMessage
}

func timingValues(r *http.Request) ([]timingValue, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var values []timingValue
	index := map[string]int{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, false
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		key := k.(string)
		limit, known := timingLimits[key]
		n, isNumber := v.(json.Number)
		if !known || !isNumber {
			return nil, false
		}
		f, err := n.Float64()
		if err != nil || math.IsInf(f, 0) || f < 0 || f > limit {
			return nil, false
		}
		js, err := jsJSON([]byte(n.String()))
		if err != nil {
			return nil, false
		}
		if i, ok := index[key]; ok {
			values[i].value = js
			continue
		}
		index[key] = len(values)
		values = append(values, timingValue{key, js})
	}
	if _, err := dec.Token(); err != nil || dec.More() {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false
	}
	return values, len(values) > 0
}

func (a *API) approveRunAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	id, _ := fieldString(body, "actionRequestId")
	decision, _ := field(body, "decision")
	var reason *string
	if s, ok := fieldString(body, "rejectionReason"); ok {
		reason = &s
	}
	if id == "" {
		sendError(w, http.StatusBadRequest, "actionRequestId is required")
		return
	}
	if decision != "approve" && decision != "reject" {
		sendError(w, http.StatusBadRequest, "decision must be 'approve' or 'reject'")
		return
	}
	rawPasskey, hasPasskey := field(body, "passkey")
	if hasPasskey && rawPasskey != nil && decision != "approve" {
		sendError(w, http.StatusBadRequest, "passkey assertion is only valid for approve")
		return
	}
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	threadID, runID := r.PathValue("threadId"), r.PathValue("runId")
	if ok, err := a.workThreadOwned(ctx, act, threadID); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "Thread not found")
		}
		return
	}
	notFound := func() { sendError(w, http.StatusNotFound, "action_request not found") }
	if !isUUID(id) {
		notFound()
		return
	}
	var kind, status string
	var workRunID *string
	err = a.DB.QueryRow(ctx, `select kind, status, work_run_id::text from action_request where org_id = $1 and id = $2`, act.OrgID, id).Scan(&kind, &status, &workRunID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind == "source_config_admin" && !act.Admin) {
		notFound()
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if workRunID == nil || *workRunID != runID {
		sendError(w, http.StatusForbidden, "action_request does not belong to this run")
		return
	}
	if inThread, err := a.runInThread(ctx, act, threadID, runID); err != nil || !inThread {
		if err != nil {
			a.fail(w, r, err)
		} else {
			notFound()
		}
		return
	}
	if status != "pending_approval" {
		if decision == "approve" && (status == "approved" || status == "executed" || status == "failed") ||
			decision == "reject" && status == "rejected" {
			already := map[bool]string{true: "already_approved", false: "already_rejected"}[decision == "approve"]
			sendJSON(w, http.StatusOK, approveReply{OK: true, Decision: already, ID: id, Status: status})
			return
		}
		sendError(w, http.StatusConflict, `action_request status is "`+status+`"; only pending_approval can be approved or rejected`)
		return
	}
	next := map[bool]string{true: "approved", false: "rejected"}[decision == "approve"]
	var refused *decisionError
	var assertion *passkeyAssertion
	if next == "approved" && rawPasskey != nil {
		if assertion, err = a.takePasskeyAssertion(ctx, act, "approve", &id, rawPasskey); err != nil {
			if errors.As(err, &refused) {
				sendDecisionError(w, refused)
			} else {
				a.fail(w, r, err)
			}
			return
		}
	}
	var pk *verifiedPasskey
	method := approvalMethod(r)
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var current string
		var policyID *string
		if err := tx.QueryRow(ctx, `select status, policy_id::text from action_request where org_id = $1 and id = $2 for update`,
			act.OrgID, id).Scan(&current, &policyID); err != nil {
			return err
		}
		if err := mayDecide(ctx, tx, act, id, policyID); err != nil {
			return err
		}
		if current != "draft" && current != "pending_approval" {
			return transitionError(current, next)
		}
		if next == "approved" {
			var err error
			if pk, err = checkApprovalPasskey(ctx, tx, a.passkeyRP(r), act, method, assertion); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update action_request set status = $3, approved_by_user_id = $4, approved_at = now(),
			rejection_reason = case when $3 = 'rejected' then $5 else rejection_reason end, updated_at = now()
			where org_id = $1 and id = $2`, act.OrgID, id, next, nullable(act.UserID), reason); err != nil {
			return err
		}
		if next == "approved" {
			return a.enqueueActionExecute(ctx, tx, act.OrgID, id)
		}
		event := map[string]any{"type": "action_request_result", "action_request_id": id, "kind": kind, "status": "rejected"}
		if reason != nil && *reason != "" {
			event["rejection_reason"] = *reason
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into work_run_event (org_id, thread_id, run_id, kind, payload) values ($1, $2, $3, 'action_request_result', $4)`,
			act.OrgID, threadID, runID, payload)
		return err
	})
	if errors.As(err, &refused) {
		sendDecisionError(w, refused)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	event := auditEvent{OrgID: act.OrgID, EntityKind: "action_request", EntityID: id, Event: next}
	if next == "approved" {
		event.Payload = approvedPayload(act, kind, method, pk)
		a.recordAudit(ctx, event)
		sendJSON(w, http.StatusOK, approveReply{OK: true, Decision: "approved", ID: id, Status: "queued_for_execution"})
		return
	}
	var why any
	if reason != nil {
		why = *reason
	}
	event.Payload = map[string]any{"rejectedBy": nullable(act.UserID), "reason": why, "kind": kind}
	a.recordAudit(ctx, event)
	sendJSON(w, http.StatusOK, approveReply{OK: true, Decision: "rejected", ID: id})
}

type approveReply struct {
	OK       bool   `json:"ok"`
	Decision string `json:"decision"`
	ID       string `json:"action_request_id"`
	Status   string `json:"status,omitempty"`
}
