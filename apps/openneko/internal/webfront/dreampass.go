package webfront

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

const (
	dreamSettlement    = 2 * time.Hour
	dreamThreadBatch   = 20
	dreamCallsPerPass  = 80
	dreamQueryMax      = 4000
	dreamResultMax     = 400
	dreamMessageMax    = 1500
	dreamShapeMax      = 200
	dreamFactMax       = 500
	dreamNearLessons   = 8
	dreamRunsForActive = 2
)

var dreamKinds = []string{"query", "data_fact", "schema_fact", "dead_end"}

// dreamCall is one GraphJin query of a thread, as a learning pass reads it.
type dreamCall struct {
	Ref    string
	RunID  string
	Query  string
	OK     bool
	Error  string
	Result string
}

type dreamTurn struct {
	Role, Content, RunID string
}

type dreamProposal struct {
	Kind   string
	Shape  string
	Call   string
	Text   string
	SameAs string
}

type dreamDecision struct {
	Kind     string `json:"kind"`
	Shape    string `json:"shape"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	LessonID string `json:"lessonId,omitempty"`
	RunID    string `json:"runId,omitempty"`
}

var dreamProposer = llm.Program{
	Signature:   `dreamInput:string "a settled thread, its GraphJin calls and the nearest existing lessons" -> dreamLessons:string "a JSON object with a lessons array"`,
	Instruction: "You write lessons that make the next similar question need fewer queries. Reply with JSON only. No tools.",
}

const dreamProposalShape = `{
  "lessons": [
    {
      "kind": "query | data_fact | schema_fact | dead_end",
      "question_shape": "the general question or subject, with no specific date, name or value",
      "call": "the ref of the call this lesson rests on, such as c3",
      "text": "see the rules for each kind",
      "same_as": "the id of an existing lesson that says the same thing, or empty"
    }
  ]
}`

const dreamRules = `Rules:
- query: "call" is the one successful call whose result gave the answer to the user's question. It is often not the last call. Choose a call that answers the question alone. "text" is one or two sentences on how to adapt the query: which values to change, and what the ids or names in it mean. Write no lesson when the answer needed several calls.
- data_fact: "text" states the smallest and largest value of one date or number column, or the distinct values of a column with few of them, as a successful call returned them. Name the table and the column. Never state a measure, a total or a row.
- schema_fact: "text" states one fact about tables, columns or joins that a failed call got wrong and a later successful call got right. "call" is the successful call.
- dead_end: "text" states a call pattern that fails on this source and the error it gives. "call" is the failed call.
- Write a lesson only when it would have saved a call in this thread.
- Write no lesson from a question whose answer the user later called wrong.
- When an existing lesson already says the same thing, repeat its kind and set "same_as" to its id.
- Return {"lessons": []} when the thread teaches nothing.`

// DreamSweep runs the learning pass over every settled thread that has a
// message newer than its last pass.
func (a *API) DreamSweep(ctx context.Context) error {
	return a.dreamSweep(ctx, time.Now().Add(-dreamSettlement), func(orgID, input string) (string, error) {
		res, err := a.hostCalls().Generate(ctx, orgID, "dreaming", dreamProposer, map[string]any{"dreamInput": input})
		if err != nil {
			return "", err
		}
		return jsValueString(res.Outputs["dreamLessons"]), nil
	})
}

type dreamThread struct {
	ID, OrgID string
	LastAt    time.Time
}

func (a *API) dreamSweep(ctx context.Context, settledBefore time.Time, propose func(orgID, input string) (string, error)) error {
	for {
		rows, err := a.DB.Query(ctx, `select t.id::text, t.org_id, max(m.created_at)
			from work_thread t join work_message m on m.thread_id = t.id
			group by t.id, t.org_id
			having max(m.created_at) < $1 and max(m.created_at) > coalesce(
			  (select max(p.last_message_at) from dream_pass p where p.thread_id = t.id), '-infinity')
			order by max(m.created_at) limit $2`, settledBefore, dreamThreadBatch)
		if err != nil {
			return err
		}
		var due []dreamThread
		for rows.Next() {
			var t dreamThread
			if err := rows.Scan(&t.ID, &t.OrgID, &t.LastAt); err != nil {
				rows.Close()
				return err
			}
			due = append(due, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(due) == 0 {
			return nil
		}
		for _, t := range due {
			if err := ctx.Err(); err != nil {
				return err
			}
			decisions, passErr := a.dreamThreadPass(ctx, t, propose)
			note := ""
			if passErr != nil {
				note = passErr.Error()
				log.Printf("[dreaming] org=%s thread=%s pass failed: %v", t.OrgID, t.ID, passErr)
			}
			raw, _ := json.Marshal(decisions)
			if decisions == nil {
				raw = []byte("[]")
			}
			if _, err := a.DB.Exec(ctx, `insert into dream_pass (org_id, thread_id, last_message_at, decisions, error)
				values ($1, $2::uuid, $3, $4::jsonb, nullif($5, ''))`, t.OrgID, t.ID, t.LastAt, string(raw), note); err != nil {
				return err
			}
		}
	}
}

func (a *API) dreamThreadPass(ctx context.Context, t dreamThread, propose func(orgID, input string) (string, error)) ([]dreamDecision, error) {
	turns, err := a.dreamTurns(ctx, t)
	if err != nil {
		return nil, err
	}
	calls, err := a.dreamCalls(ctx, t)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 || len(turns) == 0 {
		return nil, nil
	}
	question := ""
	for _, turn := range turns {
		if turn.Role == "user" {
			question = turn.Content
			break
		}
	}
	near, err := a.dreamLessonsFor(ctx, t.OrgID, question, dreamNearLessons, "active", "candidate")
	if err != nil {
		return nil, err
	}
	raw, err := propose(t.OrgID, dreamPassInput(turns, calls, near))
	if err != nil {
		return nil, err
	}
	var decisions []dreamDecision
	for _, p := range parseDreamProposals(raw) {
		d, err := a.dreamConsolidate(ctx, t.OrgID, p, calls, near)
		if err != nil {
			return decisions, err
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

func (a *API) dreamTurns(ctx context.Context, t dreamThread) ([]dreamTurn, error) {
	rows, err := a.DB.Query(ctx, `select role, content, coalesce(run_id::text, '') from work_message
		where thread_id = $1::uuid and org_id = $2 and role in ('user', 'assistant') order by created_at, id`, t.ID, t.OrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dreamTurn
	for rows.Next() {
		var turn dreamTurn
		if err := rows.Scan(&turn.Role, &turn.Content, &turn.RunID); err != nil {
			return nil, err
		}
		out = append(out, turn)
	}
	return out, rows.Err()
}

func (a *API) dreamCalls(ctx context.Context, t dreamThread) ([]dreamCall, error) {
	rows, err := a.DB.Query(ctx, `select run_id::text, payload::text from work_run_event
		where thread_id = $1::uuid and org_id = $2 and kind in ('tool_start', 'tool_end') order by run_id, seq`, t.ID, t.OrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []dreamEvent
	for rows.Next() {
		var e dreamEvent
		var payload string
		if err := rows.Scan(&e.RunID, &payload); err != nil {
			return nil, err
		}
		parsed, err := jsParse([]byte(payload))
		if err != nil {
			continue
		}
		e.Payload, _ = parsed.(*jsObj)
		if e.Payload != nil {
			events = append(events, e)
		}
	}
	return extractDreamCalls(events), rows.Err()
}

type dreamEvent struct {
	RunID   string
	Payload *jsObj
}

// extractDreamCalls pairs the tool_start and tool_end events of the
// execute_graphql calls of a thread, in order.
func extractDreamCalls(events []dreamEvent) []dreamCall {
	var out []dreamCall
	open := map[string]int{}
	for _, e := range events {
		id, _ := e.Payload.vals["id"].(string)
		key := e.RunID + "\x00" + id
		switch e.Payload.vals["type"] {
		case "tool_start":
			name, _ := e.Payload.vals["name"].(string)
			if !strings.HasSuffix(name, "execute_graphql") {
				continue
			}
			query := dreamQueryOf(e.Payload.vals["input"])
			if query == "" || len(query) > dreamQueryMax || len(out) >= dreamCallsPerPass {
				continue
			}
			open[key] = len(out)
			out = append(out, dreamCall{Ref: "c" + strconv.Itoa(len(out)+1), RunID: e.RunID, Query: query})
		case "tool_end":
			i, ok := open[key]
			if !ok {
				continue
			}
			delete(open, key)
			c := &out[i]
			result := e.Payload.vals["result"]
			errText, _ := e.Payload.vals["error"].(string)
			if errText == "" && isToolResultError(result) {
				errText = toolResultErrorText(result)
				if errText == "" {
					errText = "the call returned an error"
				}
			}
			if errText != "" {
				c.Error = jsSlice(errText, dreamResultMax)
				continue
			}
			c.OK = true
			c.Result = jsSlice(dreamResultText(result), dreamResultMax)
		}
	}
	return slices.DeleteFunc(out, func(c dreamCall) bool { return !c.OK && c.Error == "" })
}

func dreamQueryOf(input any) string {
	for _, v := range []any{input, jsGet(input, "arguments"), jsGet(input, "args")} {
		if q, ok := jsNonEmptyString(jsGet(v, "query")); ok {
			return strings.TrimSpace(q)
		}
	}
	return ""
}

func dreamResultText(result any) string {
	for _, entry := range jsArray(jsGet(result, "content")) {
		if text, ok := jsNonEmptyString(jsGet(entry, "text")); ok {
			return text
		}
	}
	if s, ok := result.(string); ok {
		return s
	}
	return jsStringify(result, false)
}

func dreamPassInput(turns []dreamTurn, calls []dreamCall, near []dreamLesson) string {
	var b strings.Builder
	b.WriteString("Return only JSON matching this shape:\n" + dreamProposalShape + "\n\n" + dreamRules + "\n\nThread:\n")
	written := map[string]bool{}
	writeCalls := func(runID string) {
		if runID == "" || written[runID] {
			return
		}
		written[runID] = true
		for _, c := range calls {
			if c.RunID != runID {
				continue
			}
			if c.OK {
				fmt.Fprintf(&b, "  %s ok: %s\n    result: %s\n", c.Ref, c.Query, c.Result)
			} else {
				fmt.Fprintf(&b, "  %s FAILED: %s\n    error: %s\n", c.Ref, c.Query, c.Error)
			}
		}
	}
	for _, turn := range turns {
		if turn.Role == "assistant" {
			writeCalls(turn.RunID)
		}
		fmt.Fprintf(&b, "[%s] %s\n", turn.Role, jsSlice(turn.Content, dreamMessageMax))
	}
	for _, c := range calls {
		writeCalls(c.RunID)
	}
	b.WriteString("\nExisting lessons:\n")
	if len(near) == 0 {
		b.WriteString("none\n")
	}
	for _, l := range near {
		fmt.Fprintf(&b, "- id %s · %s · %s\n%s\n", l.ID, l.Kind, l.Shape, indentLines(l.Body, "  "))
	}
	return b.String()
}

func parseDreamProposals(raw string) []dreamProposal {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil
	}
	var obj struct {
		Lessons []struct {
			Kind   string `json:"kind"`
			Shape  string `json:"question_shape"`
			Call   string `json:"call"`
			Text   string `json:"text"`
			SameAs string `json:"same_as"`
		} `json:"lessons"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &obj); err != nil {
		return nil
	}
	out := make([]dreamProposal, 0, len(obj.Lessons))
	for _, l := range obj.Lessons {
		out = append(out, dreamProposal{Kind: jsTrim(l.Kind), Shape: jsTrim(l.Shape), Call: jsTrim(l.Call),
			Text: jsTrim(l.Text), SameAs: jsTrim(l.SameAs)})
	}
	return out
}

// dreamGate gives the body of the lesson that a proposal makes, or the
// reason the proposal is rejected.
func dreamGate(p dreamProposal, calls []dreamCall) (body string, call dreamCall, reason string) {
	if !slices.Contains(dreamKinds, p.Kind) {
		return "", call, "unknown kind"
	}
	if p.Shape == "" || len(p.Shape) > dreamShapeMax {
		return "", call, "no question shape, or one that is too long"
	}
	i := slices.IndexFunc(calls, func(c dreamCall) bool { return c.Ref == p.Call })
	if i < 0 {
		return "", call, "names no call of the thread"
	}
	call = calls[i]
	if len(p.Text) > dreamFactMax {
		return "", call, "text is too long"
	}
	switch p.Kind {
	case "query":
		if !call.OK {
			return "", call, "the query failed in its run"
		}
		if graphqlOperationType(call.Query) != "query" {
			return "", call, "the call is not a read"
		}
		body = call.Query
		if p.Text != "" {
			body = p.Text + "\n" + call.Query
		}
	case "dead_end":
		if call.OK {
			return "", call, "the call did not fail"
		}
		if p.Text == "" {
			return "", call, "no text"
		}
		body = p.Text
	default:
		if !call.OK {
			return "", call, "the call failed"
		}
		if p.Text == "" {
			return "", call, "no text"
		}
		body = p.Text
	}
	if learnedSecret.MatchString(body) || learnedEmail.MatchString(body) {
		return "", call, "holds a secret-shaped value or an email address"
	}
	return body, call, ""
}

func (a *API) dreamConsolidate(ctx context.Context, orgID string, p dreamProposal, calls []dreamCall, near []dreamLesson) (dreamDecision, error) {
	d := dreamDecision{Kind: p.Kind, Shape: p.Shape}
	body, call, reason := dreamGate(p, calls)
	d.RunID = call.RunID
	if reason != "" {
		d.Decision, d.Reason = "rejected", reason
		return d, nil
	}
	state := "active"
	if p.Kind == "schema_fact" || p.Kind == "dead_end" {
		state = "candidate"
	}
	if i := slices.IndexFunc(near, func(l dreamLesson) bool { return l.ID == p.SameAs && l.Kind == p.Kind }); p.SameAs != "" && i >= 0 {
		d.LessonID, d.Decision = p.SameAs, "supported"
		replace := ""
		if p.Kind == "data_fact" && body != near[i].Body {
			replace, d.Decision = body, "replaced"
		}
		_, err := a.DB.Exec(ctx, `update dream_lesson set
			  source_run_ids = (select array(select distinct unnest(source_run_ids || $3::uuid))),
			  body = coalesce(nullif($4, ''), body),
			  state = case when state = 'candidate' and cardinality(
			    (select array(select distinct unnest(source_run_ids || $3::uuid)))) >= $5 then 'active' else state end,
			  updated_at = now()
			where org_id = $1 and id = $2::uuid and state in ('active', 'candidate')`,
			orgID, p.SameAs, call.RunID, replace, dreamRunsForActive)
		return d, err
	}
	if p.SameAs != "" {
		d.Reason = "same_as names no near lesson of this kind; written as new"
	}
	err := a.DB.QueryRow(ctx, `insert into dream_lesson (org_id, kind, question_shape, body, state, source_run_ids)
		values ($1, $2, $3, $4, $5, array[$6::uuid]) returning id::text`,
		orgID, p.Kind, p.Shape, body, state, call.RunID).Scan(&d.LessonID)
	d.Decision = "written"
	return d, err
}
