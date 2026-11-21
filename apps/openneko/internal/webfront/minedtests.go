package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	mineBatch      = 200
	mineContextMax = 1500
	mineTolerance  = 0.01
)

// minedTurn is one earlier message of the thread a mined test comes from.
type minedTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// minedTest is a question that a run answered with one figure or one top
// row from a GraphJin query. The query is the oracle.
type minedTest struct {
	RunID, ThreadID, Question string
	Context                   []minedTurn
	Correction                string
	Query                     string
	ValuePath                 []any
	Value                     string
	Kind                      string
}

type mineCandidate struct {
	Path  []any
	Value any
}

var (
	answerNumber  = regexp.MustCompile(`[-+]?(?:\d{1,3}(?:,\d{3})+|\d+)(?:\.\d+)?\s*[kmbKMB]?\b`)
	correctionMsg = regexp.MustCompile(`(?i)\b(that'?s (wrong|not right|incorrect)|wrong|incorrect|not correct|should (be|use|have)|you (missed|forgot)|actually)\b|^\s*no[,.]`)
)

// mineCandidates are the scalars of a result's data: each field of an
// aggregate root and of the first row of a list root, one level deep.
func mineCandidates(data any) []mineCandidate {
	roots, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(roots))
	for k := range roots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []mineCandidate
	var fields func(path []any, row map[string]any, depth int)
	fields = func(path []any, row map[string]any, depth int) {
		names := make([]string, 0, len(row))
		for k := range row {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			p := append(append([]any{}, path...), k)
			switch v := row[k].(type) {
			case float64, string:
				if !isIDField(k) {
					out = append(out, mineCandidate{Path: p, Value: v})
				}
			case map[string]any:
				if depth == 0 {
					fields(p, v, 1)
				}
			}
		}
	}
	for _, k := range keys {
		switch v := roots[k].(type) {
		case map[string]any:
			fields([]any{k}, v, 0)
		case []any:
			if len(v) > 0 {
				if row, ok := v[0].(map[string]any); ok {
					fields([]any{k, 0.0}, row, 0)
				}
			}
		}
	}
	return out
}

// isIDField is true for a key column. An aggregate of one, such as
// count_businessentityid, is a figure.
func isIDField(name string) bool {
	name = strings.ToLower(name)
	for _, agg := range []string{"count_", "sum_", "avg_", "min_", "max_"} {
		if strings.HasPrefix(name, agg) {
			return false
		}
	}
	return strings.HasSuffix(name, "id")
}

func answerNumbers(answer string) []float64 {
	var out []float64
	for _, m := range answerNumber.FindAllString(answerFence.ReplaceAllString(answer, ""), -1) {
		raw := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(m), ",", ""))
		scale := 1.0
		switch {
		case strings.HasSuffix(raw, "k"):
			scale, raw = 1e3, strings.TrimSpace(strings.TrimSuffix(raw, "k"))
		case strings.HasSuffix(raw, "m"):
			scale, raw = 1e6, strings.TrimSpace(strings.TrimSuffix(raw, "m"))
		case strings.HasSuffix(raw, "b"):
			scale, raw = 1e9, strings.TrimSpace(strings.TrimSuffix(raw, "b"))
		}
		if n, err := strconv.ParseFloat(raw, 64); err == nil {
			out = append(out, n*scale)
		}
	}
	return out
}

// answerHolds is true when the answer states the value: a number within 1%,
// or the name in any case.
func answerHolds(answer, kind, value string) bool {
	if kind == "name" {
		return value != "" && strings.Contains(strings.ToLower(answerFence.ReplaceAllString(answer, "")), strings.ToLower(value))
	}
	want, err := strconv.ParseFloat(value, 64)
	if err != nil || want == 0 {
		return false
	}
	for _, n := range answerNumbers(answer) {
		if math.Abs(n-want) <= mineTolerance*math.Abs(want) {
			return true
		}
	}
	return false
}

func candidateValue(v any) (value, kind string, ok bool) {
	switch v := v.(type) {
	case float64:
		if v == 0 || v == math.Trunc(v) && v >= 1900 && v <= 2100 {
			return "", "", false
		}
		return strconv.FormatFloat(v, 'f', -1, 64), "number", true
	case string:
		if len([]rune(v)) < 4 {
			return "", "", false
		}
		if _, isTime := parseCompileTime(v); isTime {
			return "", "", false
		}
		return v, "name", true
	}
	return "", "", false
}

// mineAnswer picks, from the last successful call backward, the first value
// of a call's result that the answer states.
func mineAnswer(answer string, calls []compileCall) (call compileCall, path []any, value, kind string, ok bool) {
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if !c.OK {
			continue
		}
		data, okData := compileData(c.Result)
		if !okData {
			continue
		}
		for _, cand := range mineCandidates(data) {
			v, k, okValue := candidateValue(cand.Value)
			if okValue && answerHolds(answer, k, v) {
				return c, cand.Path, v, k, true
			}
		}
	}
	return compileCall{}, nil, "", "", false
}

// valueAt reads the value at a mined path from fresh data.
func valueAt(data any, path []any) (any, bool) {
	cur := data
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur = m[s]
		case float64:
			list, ok := cur.([]any)
			if !ok || int(s) >= len(list) {
				return nil, false
			}
			cur = list[int(s)]
		default:
			return nil, false
		}
	}
	return cur, cur != nil
}

type mineRun struct {
	runID, threadID, question, answer string
	createdAt                         time.Time
}

// MineResult is what one mining pass did.
type MineResult struct {
	Mined   int            `json:"mined"`
	Skipped map[string]int `json:"skipped"`
}

// mineTests turns the org's finished chat runs into tests.
func (a *API) mineTests(ctx context.Context, orgID string) (MineResult, error) {
	res := MineResult{Skipped: map[string]int{}}
	rows, err := a.DB.Query(ctx, `select r.id::text, r.thread_id::text, r.created_at,
		  (select content from work_message m where m.run_id = r.id and m.role = 'user' order by m.created_at limit 1),
		  (select content from work_message m where m.run_id = r.id and m.role = 'assistant' order by m.created_at desc limit 1)
		from work_run r join work_thread t on t.id = r.thread_id
		where r.org_id = $1 and r.status = 'completed' and t.channel not in ('workflow', 'system')
		  and not exists (select 1 from mined_test x where x.run_id = r.id)
		  and not exists (select 1 from workflow_run w where w.work_run_id = r.id)
		order by r.created_at limit $2`, orgID, mineBatch)
	if err != nil {
		return res, err
	}
	var runs []mineRun
	for rows.Next() {
		var r mineRun
		var q, ans *string
		if err := rows.Scan(&r.runID, &r.threadID, &r.createdAt, &q, &ans); err != nil {
			rows.Close()
			return res, err
		}
		r.question, r.answer = deref(q), deref(ans)
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, r := range runs {
		reason, err := a.mineRun(ctx, orgID, r)
		if err != nil {
			return res, err
		}
		if reason == "" {
			res.Mined++
		} else {
			res.Skipped[reason]++
		}
	}
	return res, nil
}

func (a *API) threadTurns(ctx context.Context, orgID, threadID string) ([]minedTurn, []string, error) {
	rows, err := a.DB.Query(ctx, `select role, content, coalesce(run_id::text, '') from work_message
		where org_id = $1 and thread_id = $2::uuid and role in ('user', 'assistant') order by created_at, id`, orgID, threadID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var turns []minedTurn
	var runs []string
	for rows.Next() {
		var t minedTurn
		var run string
		if err := rows.Scan(&t.Role, &t.Content, &run); err != nil {
			return nil, nil, err
		}
		turns, runs = append(turns, t), append(runs, run)
	}
	return turns, runs, rows.Err()
}

// mineRun stores the test of one run, or gives the reason it has none. A
// run whose question corrects the answer before it gives its oracle to the
// question that was corrected.
func (a *API) mineRun(ctx context.Context, orgID string, r mineRun) (string, error) {
	skip := func(reason string) (string, error) {
		_, err := a.DB.Exec(ctx, `insert into mined_test (run_id, org_id, thread_id, question, query, value_path, value, value_kind, skip_reason)
			values ($1::uuid, $2, $3::uuid, '', '', '[]', '', 'none', $4) on conflict (run_id) do nothing`, r.runID, orgID, r.threadID, reason)
		return reason, err
	}
	if jsTrim(r.question) == "" || jsTrim(r.answer) == "" {
		return skip("no question or no answer")
	}
	turns, runs, err := a.threadTurns(ctx, orgID, r.threadID)
	if err != nil {
		return "", err
	}
	at := -1
	for i, run := range runs {
		if run == r.runID && turns[i].Role == "user" {
			at = i
			break
		}
	}
	if at < 0 {
		return skip("no question or no answer")
	}
	for _, t := range turns[at+1:] {
		if t.Role == "user" {
			if correctionMsg.MatchString(t.Content) {
				return skip("marked wrong")
			}
			break
		}
	}
	calls, err := a.workflowRunCalls(ctx, orgID, r.runID)
	if err != nil {
		return "", err
	}
	if len(calls) == 0 {
		return skip("no query")
	}
	call, path, value, kind, ok := mineAnswer(r.answer, calls)
	if !ok {
		return skip("no single figure or top row of a query in the answer")
	}
	question, context, correction := r.question, turns[:at], ""
	if at >= 2 && correctionMsg.MatchString(r.question) && turns[at-1].Role == "assistant" {
		for j := at - 2; j >= 0; j-- {
			if turns[j].Role == "user" {
				question, context, correction = turns[j].Content, turns[:j], r.question
				break
			}
		}
	}
	trimmed := make([]minedTurn, len(context))
	for i, t := range context {
		trimmed[i] = minedTurn{Role: t.Role, Content: jsSlice(t.Content, mineContextMax)}
	}
	ctxJSON, _ := json.Marshal(trimmed)
	pathJSON, _ := json.Marshal(path)
	_, err = a.DB.Exec(ctx, `insert into mined_test (run_id, org_id, thread_id, question, context, correction, query, value_path, value, value_kind)
		values ($1::uuid, $2, $3::uuid, $4, $5::jsonb, nullif($6, ''), $7, $8::jsonb, $9, $10) on conflict (run_id) do nothing`,
		r.runID, orgID, r.threadID, question, string(ctxJSON), correction, call.Query, string(pathJSON), value, kind)
	if err != nil {
		return "", err
	}
	return "", nil
}

func (a *API) loadMinedTests(ctx context.Context, orgID string) ([]minedTest, error) {
	rows, err := a.DB.Query(ctx, `select run_id::text, thread_id::text, question, context, coalesce(correction, ''), query, value_path, value, value_kind
		from mined_test where org_id = $1 and skip_reason is null order by created_at, run_id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []minedTest
	for rows.Next() {
		var t minedTest
		var ctxJSON, pathJSON []byte
		if err := rows.Scan(&t.RunID, &t.ThreadID, &t.Question, &ctxJSON, &t.Correction, &t.Query, &pathJSON, &t.Value, &t.Kind); err != nil {
			return nil, err
		}
		if json.Unmarshal(ctxJSON, &t.Context) != nil || json.Unmarshal(pathJSON, &t.ValuePath) != nil {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// minedReplay is one run of a mined test.
type minedReplay struct {
	Pass    bool     `json:"pass"`
	Queries int      `json:"queries"`
	Failed  int      `json:"failedQueries"`
	Oracle  string   `json:"oracle"`
	Answer  string   `json:"answer"`
	Calls   []string `json:"-"`
}

// oracleValue runs the test's query again under the service identity and
// reads the value at its path.
func (a *API) oracleValue(ctx context.Context, orgID string, t minedTest) (string, error) {
	var graphqlURL string
	var mcpURL *string
	if err := a.DB.QueryRow(ctx, `select graphql_url, mcp_url from data_source where org_id = $1 and enabled = true
		order by is_default desc, created_at limit 1`, orgID).Scan(&graphqlURL, &mcpURL); err != nil {
		return "", err
	}
	endpoint := graphqlURL
	if endpoint == "" && mcpURL != nil {
		endpoint = mcpPathSuffix.ReplaceAllString(*mcpURL, "/graphql")
	}
	token, err := a.graphjinToken(orgID, "service", 120*time.Second)
	if err != nil {
		return "", err
	}
	status, raw, err := knowledgePost(ctx, endpoint, token, t.Query)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", fmt.Errorf("the oracle query answered HTTP %d", status)
	}
	data, ok := compileData(string(raw))
	if !ok {
		return "", errors.New("the oracle query returned no data")
	}
	v, ok := valueAt(data, t.ValuePath)
	if !ok {
		return "", errors.New("the oracle query has no value at the test's path")
	}
	value, _, ok := candidateValue(v)
	if !ok {
		return "", errors.New("the oracle value is not a figure or a name")
	}
	return value, nil
}

func minedTestPrompt(k knowledgePack, playbook []string, t minedTest) string {
	var b strings.Builder
	b.WriteString(section("mined_test_role") + "\n\n")
	b.WriteString(dataAccessSection(k, true, false) + "\n\n")
	if block := playbookSection(playbook); block != "" {
		b.WriteString(block + "\n\n")
	}
	if len(t.Context) > 0 {
		b.WriteString("Earlier in this conversation:\n")
		for _, turn := range t.Context {
			fmt.Fprintf(&b, "[%s] %s\n", turn.Role, turn.Content)
		}
		b.WriteString("\n")
	}
	b.WriteString("Question: " + t.Question + "\n")
	return b.String()
}

// replayMinedTest asks the question again with the playbook and scores the
// answer against the oracle on the same data.
func (a *API) replayMinedTest(ctx context.Context, orgID string, t minedTest, playbook []string) (minedReplay, error) {
	oracle, err := a.oracleValue(ctx, orgID, t)
	if err != nil {
		return minedReplay{}, err
	}
	roots, err := orgWorkspace(orgID)
	if err != nil {
		return minedReplay{}, err
	}
	a.prefetchKnowledge(ctx, orgID, roots.Knowledge, false)
	k := readKnowledgePack(roots.Knowledge)
	if k.Mode == "agentic" {
		k.DateRanges = a.dateRangesText(ctx, orgID)
	}
	var out minedReplay
	executes := map[string]bool{}
	answer, err := a.runAgentJob(ctx, agentJob{OrgID: orgID, RunID: uuid.NewString(), Label: "mined-test", GraphjinRead: true,
		Prompt: minedTestPrompt(k, playbook, t), MaxAttempts: 1,
		OnEvent: func(_ int, event any) {
			switch jsGet(event, "type") {
			case "tool_start":
				if name, _ := jsGet(event, "name").(string); isGraphjinExecute(name) {
					out.Queries++
					id, _ := jsGet(event, "id").(string)
					executes[id] = true
					if q, ok := jsNonEmptyString(jsGet(jsGet(event, "input"), "query")); ok {
						out.Calls = append(out.Calls, q)
					}
				}
			case "tool_end":
				if id, _ := jsGet(event, "id").(string); executes[id] && isToolResultError(jsGet(event, "result")) {
					out.Failed++
				}
			}
		}})
	if err != nil {
		return minedReplay{}, err
	}
	out.Oracle, out.Answer = oracle, answer
	out.Pass = answerHolds(answer, t.Kind, oracle)
	return out, nil
}
