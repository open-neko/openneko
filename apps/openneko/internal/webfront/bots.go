package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var botCoats = map[string]bool{"snow": true, "ginger": true, "smoke": true, "ink": true, "cream": true, "cocoa": true,
	"mint": true, "lilac": true, "sky": true, "rose": true, "lemon": true, "slate": true}

var botPatterns = map[string]bool{"solid": true, "patch": true, "tabby": true}

const (
	botNameMax         = 40
	botInstructionsMax = 2000
	botRunStale        = 5 * time.Minute
	routineRunsShown   = 3
	routineRunsPage    = 10
	routineSummaryMax  = 400
)

type botSummary struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Coat          string  `json:"coat"`
	Pattern       string  `json:"pattern"`
	IsDefault     bool    `json:"isDefault"`
	ThreadID      *string `json:"threadId"`
	Instructions  string  `json:"instructions"`
	LastMessageAt *string `json:"lastMessageAt"`
	Unread        bool    `json:"unread"`
	Running       bool    `json:"running"`
	LastRunFailed bool    `json:"lastRunFailed"`
}

type botRow struct {
	id, name, coat, pattern, instructions string
	isDefault                             bool
	threadID                              *string
	seenAt                                time.Time
}

const botColumns = `b.id::text, b.name, b.coat, b.pattern, b.instructions, b.is_default, b.thread_id::text, b.seen_at`

func (b *botRow) scan(row pgx.Row, extra ...any) error {
	return row.Scan(append([]any{&b.id, &b.name, &b.coat, &b.pattern, &b.instructions, &b.isDefault, &b.threadID, &b.seenAt}, extra...)...)
}

func (b botRow) summary(lastMessageAt *time.Time, runStatus *string, runAt *time.Time) botSummary {
	s := botSummary{ID: b.id, Name: b.name, Coat: b.coat, Pattern: b.pattern, IsDefault: b.isDefault, ThreadID: b.threadID,
		Instructions: b.instructions, LastMessageAt: jsTimePtr(lastMessageAt)}
	s.Unread = lastMessageAt != nil && lastMessageAt.UnixMilli() > b.seenAt.UnixMilli()+1000
	if runStatus != nil {
		active := *runStatus == "queued" || *runStatus == "running"
		s.Running = active && runAt != nil && time.Since(*runAt) < botRunStale
		s.LastRunFailed = *runStatus == "failed"
	}
	return s
}

func (a *API) botActor(w http.ResponseWriter, r *http.Request) (actor, bool) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return act, false
	}
	if act.UserID == "" {
		sendError(w, http.StatusUnauthorized, "sign in")
		return act, false
	}
	return act, true
}

func (a *API) bots(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	list, err := a.listBots(r.Context(), act)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"bots": list})
}

func (a *API) listBots(ctx context.Context, act actor) ([]botSummary, error) {
	load := func() ([]botSummary, bool, error) {
		rows, err := a.DB.Query(ctx, `select `+botColumns+`, t.last_message_at, lr.status, lr.at
			from bot b
			left join work_thread t on t.id = b.thread_id
			left join lateral (
				select r.status, greatest(r.created_at, (select max(e.created_at) from work_run_event e where e.run_id = r.id)) as at
				from work_run r where r.thread_id = b.thread_id order by r.created_at desc limit 1
			) lr on true
			where b.org_id = $1 and b.owner_user_id = $2
			order by coalesce(t.last_message_at, b.created_at) desc, b.name asc`, act.OrgID, act.UserID)
		if err != nil {
			return nil, false, err
		}
		defer rows.Close()
		out := []botSummary{}
		hasDefault := false
		for rows.Next() {
			var b botRow
			var last, at *time.Time
			var status *string
			if err := b.scan(rows, &last, &status, &at); err != nil {
				return nil, false, err
			}
			hasDefault = hasDefault || b.isDefault
			out = append(out, b.summary(last, status, at))
		}
		return out, hasDefault, rows.Err()
	}
	list, hasDefault, err := load()
	if err != nil || hasDefault {
		return list, err
	}
	_, _ = a.createBot(ctx, act, "Neko", "snow", "patch", true)
	list, _, err = load()
	return list, err
}

var errBotExists = errors.New("bot already exists")

func (a *API) createBot(ctx context.Context, act actor, name, coat, pattern string, isDefault bool) (botSummary, error) {
	name = jsSlice(strings.TrimFunc(name, isJSSpace), botNameMax)
	var out botSummary
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var threadID string
		var last time.Time
		if err := tx.QueryRow(ctx, `insert into work_thread (org_id, title, channel, created_by_user_id, backend_state)
			values ($1, $2, 'web', $3, '{}') returning id::text, last_message_at`, act.OrgID, name, act.UserID).Scan(&threadID, &last); err != nil {
			return err
		}
		var b botRow
		err := b.scan(tx.QueryRow(ctx, `insert into bot as b (org_id, owner_user_id, name, coat, pattern, is_default, thread_id)
			values ($1, $2, $3, $4, $5, $6, $7) on conflict do nothing returning `+botColumns,
			act.OrgID, act.UserID, name, coat, pattern, isDefault, threadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return errBotExists
		}
		if err != nil {
			return err
		}
		out = b.summary(&last, nil, nil)
		return nil
	})
	return out, err
}

func (a *API) botCreate(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	name, _ := fieldString(body, "name")
	name = strings.TrimFunc(name, isJSSpace)
	coat, _ := fieldString(body, "coat")
	pattern, _ := fieldString(body, "pattern")
	if name == "" || !botCoats[coat] || !botPatterns[pattern] {
		sendError(w, http.StatusBadRequest, "Give the bot a name, a coat and a pattern.")
		return
	}
	created, err := a.createBot(r.Context(), act, name, coat, pattern, false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusCreated, map[string]any{"bot": created})
}

func (a *API) ownBot(ctx context.Context, act actor, botID string) (*botRow, error) {
	if !isUUID(botID) {
		return nil, nil
	}
	var b botRow
	err := b.scan(a.DB.QueryRow(ctx, `select `+botColumns+` from bot b where b.id = $1 and b.org_id = $2 and b.owner_user_id = $3`,
		botID, act.OrgID, act.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

type botRoutineRun struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	At      string `json:"at"`
	Summary string `json:"summary"`
}

type botRoutineRuns struct {
	Runs []botRoutineRun `json:"runs"`
	More bool            `json:"more"`
}

type botRoutine struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Schedule    string          `json:"schedule"`
	Enabled     bool            `json:"enabled"`
	Description string          `json:"description"`
	Steps       []string        `json:"steps"`
	Runs        []botRoutineRun `json:"runs"`
	MoreRuns    bool            `json:"moreRuns"`
}

func (a *API) botDetail(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	b, err := a.ownBot(ctx, act, r.PathValue("botId"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if b == nil {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	routines := []botRoutine{}
	if b.threadID != nil {
		if routines, err = a.botRoutines(ctx, act.OrgID, *b.threadID); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"bot": b.summary(nil, nil, nil), "routines": routines})
}

func (a *API) botRoutines(ctx context.Context, orgID, threadID string) ([]botRoutine, error) {
	rows, err := a.DB.Query(ctx, `select w.id::text, w.name, w.description, w.goal, w.steps, w.cron, w.cron_timezone, w.cron_enabled, w.enabled,
			(select cadence_seconds from watcher where org_id = w.org_id and workflow_id = w.id limit 1),
			(select source_kind from subscription where org_id = w.org_id and workflow_id = w.id limit 1)
		from workflow_definition w where w.org_id = $1 and w.created_by_thread_id = $2 order by w.created_at asc`, orgID, threadID)
	if err != nil {
		return nil, err
	}
	out := []botRoutine{}
	for rows.Next() {
		var rt botRoutine
		var description, goal, timezone string
		var steps []byte
		var cron, source *string
		var cronEnabled bool
		var cadence *int
		if err := rows.Scan(&rt.ID, &rt.Name, &description, &goal, &steps, &cron, &timezone, &cronEnabled, &rt.Enabled, &cadence, &source); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case cadence != nil:
			rt.Schedule = "Checks every " + everyLabel(*cadence)
		case source != nil:
			rt.Schedule = "Runs on each " + sourceSeparators.ReplaceAllString(*source, " ") + " event"
		default:
			rt.Schedule = describeSchedule(cron, timezone, cronEnabled)
		}
		if description == "" {
			description = goal
		}
		rt.Description = strings.TrimFunc(description, isJSSpace)
		rt.Steps = stepTexts(steps)
		out = append(out, rt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		page, err := a.routineRuns(ctx, orgID, out[i].ID, routineRunsShown, nil)
		if err != nil {
			return nil, err
		}
		out[i].Runs, out[i].MoreRuns = page.Runs, page.More
	}
	return out, nil
}

var sourceSeparators = regexp.MustCompile(`[_.-]+`)

func everyLabel(seconds int) string {
	switch {
	case seconds%3600 == 0 && seconds == 3600:
		return "hour"
	case seconds%3600 == 0:
		return strconv.Itoa(seconds/3600) + " hours"
	case seconds%60 == 0 && seconds == 60:
		return "minute"
	case seconds%60 == 0:
		return strconv.Itoa(seconds/60) + " minutes"
	}
	return strconv.Itoa(seconds) + " seconds"
}

func stepTexts(raw []byte) []string {
	var steps []any
	out := []string{}
	if json.Unmarshal(raw, &steps) != nil {
		return out
	}
	for _, step := range steps {
		m, ok := step.(map[string]any)
		if !ok {
			continue
		}
		if text := strings.TrimFunc(jsValueString(m["description"]), isJSSpace); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// jsValueString is String(v ?? "") for a value from JSON.
func jsValueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return jsNumberText(x)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = jsValueString(e)
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

var (
	summaryFence = regexp.MustCompile("(?s)```.*?```")
	summaryMarks = regexp.MustCompile("[#*_`>|]+")
	summaryLinks = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

func plainSummary(text string) string {
	plain := summaryFence.ReplaceAllString(text, " ")
	plain = summaryMarks.ReplaceAllString(plain, "")
	plain = summaryLinks.ReplaceAllString(plain, "$1")
	plain = strings.TrimFunc(jsWhitespaceRun.ReplaceAllString(plain, " "), isJSSpace)
	if len(utf16.Encode([]rune(plain))) > routineSummaryMax {
		return strings.TrimRightFunc(jsSlice(plain, routineSummaryMax-1), isJSSpace) + "…"
	}
	return plain
}

func (a *API) routineRuns(ctx context.Context, orgID, workflowID string, limit int, before *time.Time) (botRoutineRuns, error) {
	rows, err := a.DB.Query(ctx, `select id::text, status, coalesce(finished_at, started_at, created_at), coalesce(summary, '')
		from workflow_run where org_id = $1 and workflow_id = $2 and ($3::timestamptz is null or created_at < $3)
		order by created_at desc limit $4`, orgID, workflowID, before, limit+1)
	if err != nil {
		return botRoutineRuns{}, err
	}
	out := botRoutineRuns{Runs: []botRoutineRun{}}
	for rows.Next() {
		var run botRoutineRun
		var at time.Time
		var summary string
		if err := rows.Scan(&run.ID, &run.Status, &at, &summary); err != nil {
			rows.Close()
			return out, err
		}
		if len(out.Runs) == limit {
			out.More = true
			continue
		}
		run.At = jsTime(at)
		if run.Status == "completed" {
			run.Summary = plainSummary(summary)
		}
		out.Runs = append(out.Runs, run)
	}
	rows.Close()
	return out, rows.Err()
}

func (a *API) botPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	sets, args, msg := cleanBotPatch(body)
	if msg != "" {
		sendError(w, http.StatusBadRequest, msg)
		return
	}
	botID := r.PathValue("botId")
	if !isUUID(botID) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	args = append(args, botID, act.OrgID, act.UserID)
	n := len(args)
	var b botRow
	err := b.scan(a.DB.QueryRow(r.Context(), `update bot b set `+strings.Join(sets, ", ")+`, updated_at = now()
		where b.id = $`+strconv.Itoa(n-2)+` and b.org_id = $`+strconv.Itoa(n-1)+` and b.owner_user_id = $`+strconv.Itoa(n)+`
		returning `+botColumns, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"bot": b.summary(nil, nil, nil)})
}

func cleanBotPatch(body any) (sets []string, args []any, msg string) {
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if v, ok := field(body, "name"); ok {
		s, _ := v.(string)
		name := jsSlice(strings.TrimFunc(s, isJSSpace), botNameMax)
		if name == "" {
			return nil, nil, "Give the bot a name."
		}
		add("name", name)
		sets = append(sets, "name_pending = false")
	}
	if v, ok := field(body, "coat"); ok {
		s, _ := v.(string)
		if !botCoats[s] {
			return nil, nil, "Pick a coat from the list."
		}
		add("coat", s)
	}
	if v, ok := field(body, "pattern"); ok {
		s, _ := v.(string)
		if !botPatterns[s] {
			return nil, nil, "Pick a pattern from the list."
		}
		add("pattern", s)
	}
	if v, ok := field(body, "instructions"); ok {
		s, isText := v.(string)
		if !isText {
			return nil, nil, "Send the instructions as text."
		}
		add("instructions", jsSlice(strings.TrimFunc(s, isJSSpace), botInstructionsMax))
	}
	if len(sets) == 0 {
		return nil, nil, "Nothing to change."
	}
	return sets, args, ""
}

func (a *API) botDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	b, err := a.ownBot(ctx, act, r.PathValue("botId"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if b == nil {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if b.threadID != nil {
		var busy bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from work_run where org_id = $1 and thread_id = $2
			and status in ('queued', 'running') and created_at > now() - interval '300 seconds')`, act.OrgID, *b.threadID).Scan(&busy); err != nil {
			a.fail(w, r, err)
			return
		}
		if busy {
			sendError(w, http.StatusConflict, "This bot is working. Stop the run or wait for it to finish, then delete the bot.")
			return
		}
	}
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if b.threadID != nil {
			if _, err := tx.Exec(ctx, `delete from workflow_definition where org_id = $1 and created_by_thread_id = $2`, act.OrgID, *b.threadID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `delete from work_thread where org_id = $1 and id = $2`, act.OrgID, *b.threadID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `delete from bot where id = $1`, b.id)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) botByThread(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	threadID := r.PathValue("threadId")
	none := map[string]any{"bot": nil, "starters": []string{}}
	if !isUUID(threadID) {
		sendJSON(w, http.StatusOK, none)
		return
	}
	var b botRow
	err := b.scan(a.DB.QueryRow(r.Context(), `select `+botColumns+` from bot b where b.org_id = $1 and b.owner_user_id = $2 and b.thread_id = $3 limit 1`,
		act.OrgID, act.UserID, threadID))
	if errors.Is(err, pgx.ErrNoRows) {
		sendJSON(w, http.StatusOK, none)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	starters := []string{}
	if b.instructions == "" {
		starters = botStarters(b.name)
	}
	sendJSON(w, http.StatusOK, map[string]any{"bot": b.summary(nil, nil, nil), "starters": starters})
}

func (a *API) botSeen(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	threadID := r.PathValue("threadId")
	if !isUUID(threadID) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	tag, err := a.DB.Exec(r.Context(), `update bot set seen_at = now() where org_id = $1 and owner_user_id = $2 and thread_id = $3`,
		act.OrgID, act.UserID, threadID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) botRoutineRunsPage(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	botID, workflowID := r.PathValue("botId"), r.PathValue("workflowId")
	if !isUUID(botID) || !isUUID(workflowID) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	var owned bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from workflow_definition w join bot b on b.thread_id = w.created_by_thread_id
		where b.id = $1 and b.org_id = $2 and b.owner_user_id = $3 and w.org_id = $2 and w.id = $4)`,
		botID, act.OrgID, act.UserID, workflowID).Scan(&owned); err != nil {
		a.fail(w, r, err)
		return
	}
	if !owned {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	var before *time.Time
	if cursor := r.URL.Query().Get("before"); cursor != "" {
		var at time.Time
		err := errNoCursor
		if isUUID(cursor) {
			err = a.DB.QueryRow(ctx, `select created_at from workflow_run where org_id = $1 and workflow_id = $2 and id = $3`,
				act.OrgID, workflowID, cursor).Scan(&at)
		}
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNoCursor) {
			sendError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			a.fail(w, r, err)
			return
		}
		before = &at
	}
	page, err := a.routineRuns(ctx, act.OrgID, workflowID, routineRunsPage, before)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, page)
}

var errNoCursor = errors.New("no cursor")

func (a *API) botRoutineEnable(w http.ResponseWriter, r *http.Request) {
	act, ok := a.botActor(w, r)
	if !ok {
		return
	}
	body, _ := readBody(r)
	v, _ := field(body, "enabled")
	enabled, isBool := v.(bool)
	if !isBool {
		sendError(w, http.StatusBadRequest, "Send enabled as true or false.")
		return
	}
	ctx := r.Context()
	b, err := a.ownBot(ctx, act, r.PathValue("botId"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	workflowID := r.PathValue("workflowId")
	if b == nil || b.threadID == nil || !isUUID(workflowID) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	found := false
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update workflow_definition set enabled = $1,
			paused_until = case when $1 then null else paused_until end, updated_at = now()
			where org_id = $2 and id = $3 and created_by_thread_id = $4`, enabled, act.OrgID, workflowID, *b.threadID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		found = true
		for _, table := range []string{"watcher", "subscription"} {
			if _, err := tx.Exec(ctx, `update `+table+` set enabled = $1, updated_at = now() where org_id = $2 and workflow_id = $3`,
				enabled, act.OrgID, workflowID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !found {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var starterJobs = []string{
	"Send me a short summary each morning of what changed in the business.",
	"Watch my key numbers and tell me when one moves.",
	"Keep an eye on stock and tell me what to reorder.",
	"Flag orders that are late or stuck.",
	"Tell me when a regular customer stops buying.",
	"Draft a weekly report I can send to my team.",
}

func starterWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}

func botStarters(botName string) []string {
	hint := starterWords(botName)
	score := func(job string) int {
		n := 0
		for w := range starterWords(job) {
			if hint[strings.TrimSuffix(w, "s")] || hint[w] {
				n++
			}
		}
		return n
	}
	jobs := append([]string(nil), starterJobs...)
	sort.SliceStable(jobs, func(i, j int) bool { return score(jobs[i]) > score(jobs[j]) })
	return jobs[:4]
}

var dayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

var (
	cronDigits   = regexp.MustCompile(`^\d+$`)
	cronInterval = regexp.MustCompile(`^\*/(\d+)$`)
	cronDay      = regexp.MustCompile(`^[0-6]$`)
)

// jsInt is Number(s) when it is an integer; ok is false otherwise.
func jsInt(s string) (int, bool) {
	s = strings.TrimFunc(s, isJSSpace)
	if s == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

func describeDow(dow string) (string, bool) {
	switch dow {
	case "*":
		return "every day", true
	case "1-5":
		return "every weekday", true
	case "0,6", "6,0":
		return "every weekend", true
	}
	if d, ok := jsInt(dow); ok && d >= 0 && d <= 6 {
		return "every " + dayNames[d], true
	}
	list := strings.Split(dow, ",")
	names := make([]string, len(list))
	for i, d := range list {
		d = strings.TrimFunc(d, isJSSpace)
		if !cronDay.MatchString(d) {
			return "", false
		}
		names[i] = dayNames[d[0]-'0']
	}
	return "every " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1], true
}

func cronToEnglish(cron, timezone string) (string, bool) {
	p := strings.FieldsFunc(cron, isJSSpace)
	if len(p) != 5 {
		return "", false
	}
	minute, hour, dom, month, dow := p[0], p[1], p[2], p[3], p[4]
	tz := ""
	if timezone != "" {
		tz = " " + timezone
	}
	if strings.HasPrefix(minute, "*/") && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		m := cronInterval.FindStringSubmatch(minute)
		if m == nil {
			return "", false
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > 59 {
			return "", false
		}
		if n == 1 {
			return "every 1 minute", true
		}
		return fmt.Sprintf("every %d minutes", n), true
	}
	if hour == "*" && cronDigits.MatchString(minute) && dom == "*" && month == "*" && dow == "*" {
		n, _ := strconv.ParseFloat(minute, 64)
		if n == 0 {
			return "every hour", true
		}
		return fmt.Sprintf("every hour at :%02s", jsNumberText(n)), true
	}
	if cronDigits.MatchString(minute) && cronDigits.MatchString(hour) && dom == "*" && month == "*" {
		h, _ := strconv.ParseFloat(hour, 64)
		m, _ := strconv.ParseFloat(minute, 64)
		if h > 23 || m > 59 {
			return "", false
		}
		cadence, ok := describeDow(dow)
		if !ok {
			return "", false
		}
		period, display := "AM", int(h)
		if h >= 12 {
			period = "PM"
		}
		if display == 0 {
			display = 12
		} else if display > 12 {
			display -= 12
		}
		return fmt.Sprintf("%s at %d:%02d %s%s", cadence, display, int(m), period, tz), true
	}
	return "", false
}

// describeSchedule matches describeSchedule() in apps/web/src/lib/cron-english.ts.
func describeSchedule(cron *string, timezone string, cronEnabled bool) string {
	if cron == nil || *cron == "" {
		return "Runs when you ask"
	}
	raw, ok := cronToEnglish(*cron, timezone)
	if !ok {
		raw = *cron
		if timezone != "" && timezone != "UTC" {
			raw += " (" + timezone + ")"
		}
	}
	first, size := utf8.DecodeRuneInString(raw)
	label := raw
	if first <= 0xFFFF {
		label = string(unicode.ToUpper(first)) + raw[size:]
	}
	if !cronEnabled {
		return label + " · Paused"
	}
	return label
}
