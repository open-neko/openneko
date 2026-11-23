package webfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const (
	compileResultMax  = 1500
	compileCallsMax   = 40
	compileRetryAfter = 7 * 24 * time.Hour
)

// compiledVar is one date of a compiled query. Its value is the start of the
// Align period of Base, moved by Offset units of Unit.
type compiledVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Base   string `json:"base"`
	Align  string `json:"align"`
	Offset int    `json:"offset"`
	Unit   string `json:"unit"`
	Format string `json:"format"`
}

type compiledQuery struct {
	AnchorQuery string
	Query       string
	Vars        []compiledVar
}

// compileCall is one execute_graphql call of a finished run.
type compileCall struct {
	Ref, Query, Result string
	OK                 bool
}

type compileDataCall func(ctx context.Context, q sandbox.DataQuery) (*jsObj, error)

var workflowCompiler = llm.Program{
	Signature:   `compileInput:string "a finished workflow run: its goal, its answer and its GraphJin calls" -> compiled:string "a JSON object"`,
	Instruction: "You turn the query that answered a workflow run into a stored query for its next runs. Reply with JSON only. No tools.",
}

const compileRules = `Return only JSON of this shape:
{"answer_calls": ["c3"], "anchor_call": "c1 or empty", "query": "one query that holds the query of each answer call, with each changing date replaced by a $variable", "variables": [{"name": "start", "value": "the literal the variable replaces", "base": "today | anchor", "align": "day | week | month | quarter | year", "offset": -1, "unit": "day | week | month | quarter | year", "format": "date | timestamp"}]}

Rules:
- answer_calls are the successful calls whose results together answer the goal. When there are several, write one query with each call's roots under its own alias, in the same order. An empty list is a result: it answers that nothing matches, and a count of zero read from it needs no other call. Return {} only when the answer needed a value that the agent worked out from many rows, such as a sum or an average of a list.
- A variable's value is the start of the align period of its base, moved by offset units. today is the date the run started. anchor is the date that anchor_call returned, such as the newest order date.
- anchor_call is the successful call that found the date the run counted back from. Leave it empty when the run counted from today.
- Replace only the dates that change from one run to the next. Keep every other part of the query as it is. Write a variable as $name and do not declare it.
- format is date for a value such as 2025-06-28 and timestamp for a value such as 2025-06-28T00:00:00.`

// workflowCompilable is true for a run that a compiled query can serve: a
// scheduled or manual run with no input, no user message and no script
// steps. A run of another trigger depends on its trigger's payload.
// OPENNEKO_WORKFLOW_COMPILE=off turns compiling off.
func workflowCompilable(o workflowTurnOptions) bool {
	if os.Getenv("OPENNEKO_WORKFLOW_COMPILE") == "off" {
		return false
	}
	if o.TriggerKind != "cron" && o.TriggerKind != "manual" || o.HasTriggerInput || jsTrim(o.UserMessage) != "" {
		return false
	}
	steps, _ := jsParse(o.Workflow.Steps)
	for _, s := range jsArray(steps) {
		if jsTruthy(jsGet(s, "script")) {
			return false
		}
	}
	return true
}

func workflowBinding(o workflowTurnOptions) turnBinding {
	return turnBinding{OrgID: o.Workflow.OrgID, ThreadID: o.ThreadID, RunID: o.RunID, Kind: "workflow",
		WorkflowRunID: o.WorkflowRunID, ObservationID: o.ObservationID}
}

func (a *API) workflowDataCall(o workflowTurnOptions, events func(ctx context.Context, event any) error) (compileDataCall, error) {
	b := workflowBinding(o)
	rt := a.runTools(b)
	rt.events = events
	return scriptDataCall(b, rt)
}

// workflowToday is the date a run started, in the workflow's time zone.
func workflowToday(tz string, at time.Time) time.Time {
	loc, err := time.LoadLocation(orDefault(tz, "UTC"))
	if err != nil {
		loc = time.UTC
	}
	t := at.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

var compileTimeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07",
	"2006-01-02 15:04:05.999999999", "2006-01-02"}

// parseCompileTime reads a date or a time as a wall-clock time in UTC.
func parseCompileTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range compileTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC), true
		}
	}
	return time.Time{}, false
}

func alignCompileTime(t time.Time, align string) (time.Time, error) {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch align {
	case "", "day":
		return day, nil
	case "week":
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)), nil
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), nil
	case "quarter":
		return time.Date(t.Year(), t.Month()-(t.Month()-1)%3, 1, 0, 0, 0, 0, time.UTC), nil
	case "year":
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, fmt.Errorf("unknown align %q", align)
}

func moveCompileTime(t time.Time, n int, unit string) (time.Time, error) {
	switch unit {
	case "", "day":
		return t.AddDate(0, 0, n), nil
	case "week":
		return t.AddDate(0, 0, 7*n), nil
	case "month":
		return t.AddDate(0, n, 0), nil
	case "quarter":
		return t.AddDate(0, 3*n, 0), nil
	case "year":
		return t.AddDate(n, 0, 0), nil
	}
	return time.Time{}, fmt.Errorf("unknown unit %q", unit)
}

// compiledVarValue is the value of v on a run that started on today, with
// the anchor that the run's anchor query returned.
func compiledVarValue(v compiledVar, today, anchor time.Time) (string, error) {
	base := today
	switch v.Base {
	case "anchor":
		if anchor.IsZero() {
			return "", errors.New("variable " + v.Name + " counts from an anchor, and there is none")
		}
		base = anchor
	case "", "today":
	default:
		return "", fmt.Errorf("unknown base %q", v.Base)
	}
	t, err := alignCompileTime(base, v.Align)
	if err != nil {
		return "", err
	}
	if t, err = moveCompileTime(t, v.Offset, v.Unit); err != nil {
		return "", err
	}
	if v.Format == "date" {
		return t.Format("2006-01-02"), nil
	}
	return t.Format("2006-01-02T15:04:05"), nil
}

func compiledVarValues(vars []compiledVar, today, anchor time.Time) (map[string]any, error) {
	out := map[string]any{}
	for _, v := range vars {
		value, err := compiledVarValue(v, today, anchor)
		if err != nil {
			return nil, err
		}
		out[v.Name] = value
	}
	return out, nil
}

// compileResultText is the text of an execute_graphql result as the run
// events or the tools give it.
func compileResultText(result any) string {
	for _, entry := range jsArray(jsGet(result, "content")) {
		if s, ok := entry.(string); ok {
			return s
		}
		if text, ok := jsNonEmptyString(jsGet(entry, "text")); ok {
			return text
		}
	}
	if s, ok := result.(string); ok {
		return s
	}
	return ""
}

// compileData is the data object of a result text, for comparison.
func compileData(text string) (any, bool) {
	var body any
	if json.Unmarshal([]byte(text), &body) != nil {
		return nil, false
	}
	if m, ok := body.(map[string]any); ok {
		if errs, _ := m["errors"].([]any); len(errs) > 0 {
			return nil, false
		}
		if data, ok := m["data"]; ok {
			return data, data != nil
		}
	}
	return body, true
}

// compileAnchor is the first date or time in the data of a result.
func compileAnchor(text string) (time.Time, bool) {
	data, ok := compileData(text)
	if !ok {
		return time.Time{}, false
	}
	var found time.Time
	var walk func(v any) bool
	walk = func(v any) bool {
		switch v := v.(type) {
		case string:
			if t, ok := parseCompileTime(v); ok {
				found = t
				return true
			}
		case map[string]any:
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	walk(data)
	return found, !found.IsZero()
}

func (a *API) workflowRunCalls(ctx context.Context, orgID, runID string) ([]compileCall, error) {
	rows, err := a.DB.Query(ctx, `select payload::text from work_run_event
		where org_id = $1 and run_id = $2::uuid and kind in ('tool_start', 'tool_end') order by id`, orgID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compileCall
	open := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		payload, err := jsParse([]byte(raw))
		if err != nil {
			continue
		}
		id, _ := jsGet(payload, "id").(string)
		switch jsGet(payload, "type") {
		case "tool_start":
			name, _ := jsGet(payload, "name").(string)
			if !strings.HasSuffix(name, "execute_graphql") || len(out) >= compileCallsMax {
				continue
			}
			query := ""
			for _, v := range []any{jsGet(payload, "input"), jsGet(jsGet(payload, "input"), "arguments")} {
				if q, ok := jsNonEmptyString(jsGet(v, "query")); ok {
					query = strings.TrimSpace(q)
					break
				}
			}
			if query == "" {
				continue
			}
			open[id] = len(out)
			out = append(out, compileCall{Ref: "c" + strconv.Itoa(len(out)+1), Query: query})
		case "tool_end":
			i, ok := open[id]
			if !ok {
				continue
			}
			delete(open, id)
			result := jsGet(payload, "result")
			errText, _ := jsGet(payload, "error").(string)
			text := compileResultText(result)
			_, hasData := compileData(text)
			out[i].Result = text
			out[i].OK = errText == "" && !isToolResultError(result) && hasData
		}
	}
	return out, rows.Err()
}

func compileInput(o workflowTurnOptions, answer string, calls []compileCall) string {
	var b strings.Builder
	b.WriteString(compileRules + "\n\nGoal:\n" + o.Workflow.Goal + "\n\nCalls:\n")
	for _, c := range calls {
		status := "FAILED"
		if c.OK {
			status = "ok"
		}
		fmt.Fprintf(&b, "%s %s: %s\n  result: %s\n", c.Ref, status, c.Query, jsSlice(c.Result, compileResultMax))
	}
	b.WriteString("\nAnswer:\n" + jsSlice(answer, 4000) + "\n")
	return b.String()
}

type compileProposal struct {
	AnswerCalls []string      `json:"answer_calls"`
	AnswerCall  string        `json:"answer_call"`
	AnchorCall  string        `json:"anchor_call"`
	Query       string        `json:"query"`
	Variables   []compiledVar `json:"variables"`
}

func parseCompileProposal(raw string) (compileProposal, bool) {
	var p compileProposal
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start || json.Unmarshal([]byte(raw[start:end+1]), &p) != nil {
		return p, false
	}
	if len(p.AnswerCalls) == 0 && jsTrim(p.AnswerCall) != "" {
		p.AnswerCalls = []string{p.AnswerCall}
	}
	return p, len(p.AnswerCalls) > 0 && jsTrim(p.Query) != ""
}

// verifyCompiled checks a proposal against the run it came from: each
// variable gives back the literal it replaced on the run's date, and the
// query with those values returns the data the answering call returned.
func verifyCompiled(ctx context.Context, p compileProposal, calls []compileCall, today time.Time, call compileDataCall) (compiledQuery, error) {
	find := func(ref string) (compileCall, bool) {
		for _, c := range calls {
			if c.Ref == ref && c.OK {
				return c, true
			}
		}
		return compileCall{}, false
	}
	var answers []compileCall
	for _, ref := range p.AnswerCalls {
		c, ok := find(ref)
		if !ok {
			return compiledQuery{}, errors.New("answer call " + ref + " names no successful call")
		}
		answers = append(answers, c)
	}
	if graphqlOperationType(p.Query) != "query" {
		return compiledQuery{}, errors.New("the compiled query is not a read")
	}
	var anchor time.Time
	out := compiledQuery{Query: p.Query, Vars: p.Variables}
	if ref := jsTrim(p.AnchorCall); ref != "" {
		c, ok := find(ref)
		if !ok {
			return compiledQuery{}, errors.New("anchor_call names no successful call")
		}
		if anchor, ok = compileAnchor(c.Result); !ok {
			return compiledQuery{}, errors.New("anchor_call returned no date")
		}
		out.AnchorQuery = c.Query
	}
	for _, v := range p.Variables {
		if v.Name == "" || !strings.Contains(p.Query, "$"+v.Name) {
			return compiledQuery{}, errors.New("variable " + v.Name + " is not in the query")
		}
		value, err := compiledVarValue(v, today, anchor)
		if err != nil {
			return compiledQuery{}, err
		}
		got, ok1 := parseCompileTime(value)
		want, ok2 := parseCompileTime(v.Value)
		if !ok1 || !ok2 || !got.Equal(want) {
			return compiledQuery{}, fmt.Errorf("variable %s gives %s on the run's date, not %s", v.Name, value, v.Value)
		}
	}
	vars, _ := compiledVarValues(p.Variables, today, anchor)
	text, err := runCompiled(ctx, call, p.Query, vars)
	if err != nil {
		return compiledQuery{}, err
	}
	got, _ := compileData(text)
	if !holdsEveryRoot(got, answers) {
		return compiledQuery{}, errors.New("the compiled query does not return the run's result")
	}
	return out, nil
}

// holdsEveryRoot is true when each root of each answer call's data comes
// back, under any alias, in the compiled query's data.
func holdsEveryRoot(got any, answers []compileCall) bool {
	roots, ok := got.(map[string]any)
	if !ok {
		return false
	}
	for _, c := range answers {
		want, ok := compileData(c.Result)
		if !ok {
			return false
		}
		wantRoots, ok := want.(map[string]any)
		if !ok {
			return false
		}
		for _, w := range wantRoots {
			found := false
			for _, g := range roots {
				if reflect.DeepEqual(g, w) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func runCompiled(ctx context.Context, call compileDataCall, query string, vars map[string]any) (string, error) {
	q := sandbox.DataQuery{Query: query}
	if len(vars) > 0 {
		raw, err := json.Marshal(vars)
		if err != nil {
			return "", err
		}
		q.Variables = raw
	}
	result, err := call(ctx, q)
	if err != nil {
		return "", err
	}
	text := compileResultText(result)
	if isToolResultError(result) {
		return "", errors.New("the query failed: " + jsSlice(text, 300))
	}
	if _, ok := compileData(text); !ok {
		return "", errors.New("the query returned no data")
	}
	return text, nil
}

func workflowGoalHash(wf workflowRecord) string {
	sum := sha256.Sum256([]byte(wf.Goal + "\x00" + string(wf.Steps)))
	return hex.EncodeToString(sum[:])
}

// loadCompiledQuery gives the workflow's compiled query. A query compiled
// for another goal is dropped.
func (a *API) loadCompiledQuery(ctx context.Context, wf workflowRecord) (*compiledQuery, error) {
	var c compiledQuery
	var anchor *string
	var vars []byte
	var goal string
	err := a.DB.QueryRow(ctx, `select anchor_query, query, variables, goal_hash from workflow_compiled_query where org_id = $1 and workflow_id = $2::uuid`,
		wf.OrgID, wf.ID).Scan(&anchor, &c.Query, &vars, &goal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if goal != workflowGoalHash(wf) {
		_, err := a.DB.Exec(ctx, `delete from workflow_compiled_query where org_id = $1 and workflow_id = $2::uuid`, wf.OrgID, wf.ID)
		return nil, err
	}
	c.AnchorQuery = deref(anchor)
	if err := json.Unmarshal(vars, &c.Vars); err != nil {
		return nil, err
	}
	return &c, nil
}

// compiledWorkflowContext runs the workflow's compiled query for this run
// and gives the prompt block with its result. A compiled query that fails
// is dropped, and the run goes on as if there were none.
func (a *API) compiledWorkflowContext(ctx context.Context, o workflowTurnOptions, now time.Time, call compileDataCall) (string, error) {
	c, err := a.loadCompiledQuery(ctx, o.Workflow)
	if err != nil || c == nil {
		return "", err
	}
	text, err := func() (string, error) {
		var anchor time.Time
		if c.AnchorQuery != "" {
			result, err := runCompiled(ctx, call, c.AnchorQuery, nil)
			if err != nil {
				return "", err
			}
			var ok bool
			if anchor, ok = compileAnchor(result); !ok {
				return "", errors.New("the anchor query returned no date")
			}
		}
		vars, err := compiledVarValues(c.Vars, workflowToday(o.Workflow.CronTimezone, now), anchor)
		if err != nil {
			return "", err
		}
		result, err := runCompiled(ctx, call, c.Query, vars)
		if err != nil {
			return "", err
		}
		shown, _ := json.Marshal(vars)
		return section("workflow_compiled", "query", c.Query, "variables", string(shown),
			"result", jsSlice(result, 20_000)), nil
	}()
	if err != nil {
		log.Printf("[workflow-compile] org=%s workflow=%s dropped: %v", o.Workflow.OrgID, o.Workflow.ID, err)
		_, derr := a.DB.Exec(ctx, `delete from workflow_compiled_query where org_id = $1 and workflow_id = $2::uuid`, o.Workflow.OrgID, o.Workflow.ID)
		return "", derr
	}
	_, err = a.DB.Exec(ctx, `update workflow_compiled_query set uses = uses + 1, last_used_at = now()
		where org_id = $1 and workflow_id = $2::uuid`, o.Workflow.OrgID, o.Workflow.ID)
	return text, err
}

// compileWorkflowRun turns a finished run into the workflow's compiled query
// when the workflow has none.
func (a *API) compileWorkflowRun(ctx context.Context, o workflowTurnOptions, answer string,
	propose func(input string) (string, error), call compileDataCall) error {
	existing, err := a.loadCompiledQuery(ctx, o.Workflow)
	if err != nil || existing != nil {
		return err
	}
	var waiting bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from workflow_compile_declined where org_id = $1 and workflow_id = $2::uuid
		and goal_hash = $3 and declined_at > now() - $4 * interval '1 second')`,
		o.Workflow.OrgID, o.Workflow.ID, workflowGoalHash(o.Workflow), int(compileRetryAfter/time.Second)).Scan(&waiting); err != nil || waiting {
		return err
	}
	decline := func(reason string) error {
		_, err := a.DB.Exec(ctx, `insert into workflow_compile_declined (workflow_id, org_id, goal_hash, reason) values ($1::uuid, $2, $3, $4)
			on conflict (workflow_id) do update set goal_hash = excluded.goal_hash, reason = excluded.reason, declined_at = now()`,
			o.Workflow.ID, o.Workflow.OrgID, workflowGoalHash(o.Workflow), jsSlice(reason, 600))
		return err
	}
	var startedAt time.Time
	if err := a.DB.QueryRow(ctx, `select created_at from work_run where org_id = $1 and id = $2::uuid`,
		o.Workflow.OrgID, o.RunID).Scan(&startedAt); err != nil {
		return err
	}
	calls, err := a.workflowRunCalls(ctx, o.Workflow.OrgID, o.RunID)
	if err != nil || len(calls) == 0 {
		return err
	}
	raw, err := propose(compileInput(o, answer, calls))
	if err != nil {
		return err
	}
	p, ok := parseCompileProposal(raw)
	if !ok {
		log.Printf("[workflow-compile] org=%s workflow=%s not compiled: the model step proposed no query: %s", o.Workflow.OrgID, o.Workflow.ID, jsSlice(raw, 600))
		return decline("the model step proposed no query")
	}
	c, err := verifyCompiled(ctx, p, calls, workflowToday(o.Workflow.CronTimezone, startedAt), call)
	if err != nil {
		log.Printf("[workflow-compile] org=%s workflow=%s not compiled: %v", o.Workflow.OrgID, o.Workflow.ID, err)
		return decline(err.Error())
	}
	vars, _ := json.Marshal(c.Vars)
	_, err = a.DB.Exec(ctx, `insert into workflow_compiled_query (workflow_id, org_id, anchor_query, query, variables, source_run_id, goal_hash)
		values ($1::uuid, $2, nullif($3, ''), $4, $5::jsonb, $6::uuid, $7)
		on conflict (workflow_id) do nothing`, o.Workflow.ID, o.Workflow.OrgID, c.AnchorQuery, c.Query, string(vars), o.RunID, workflowGoalHash(o.Workflow))
	if err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, `delete from workflow_compile_declined where org_id = $1 and workflow_id = $2::uuid`, o.Workflow.OrgID, o.Workflow.ID)
	return err
}

func (a *API) compileProposer(ctx context.Context, orgID string) func(input string) (string, error) {
	return func(input string) (string, error) {
		out, err := a.hostCalls().Generate(ctx, orgID, "workflow_compile", workflowCompiler, map[string]any{"compileInput": input})
		if err != nil {
			return "", err
		}
		return jsValueString(out.Outputs["compiled"]), nil
	}
}
