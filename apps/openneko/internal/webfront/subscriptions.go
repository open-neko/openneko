package webfront

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var sourceKinds = []string{"workflow_output", "source_change", "external_event"}

const noDataSource = "no data_source configured for this org — source_change subscriptions need a configured data source"

type subscriptionOut struct {
	ID                     string     `json:"id"`
	WorkflowID             string     `json:"workflowId"`
	SourceKind             string     `json:"sourceKind"`
	Filter                 storedJSON `json:"filter"`
	Enabled                bool       `json:"enabled"`
	DebounceMs             int        `json:"debounceMs"`
	MaxConcurrentRuns      int        `json:"maxConcurrentRuns"`
	MaxChainDepthOverride  *int       `json:"maxChainDepthOverride"`
	IdempotencyKeyTemplate *string    `json:"idempotencyKeyTemplate"`
	CreatedAt              string     `json:"createdAt"`
	UpdatedAt              string     `json:"updatedAt"`
}

func (a *API) workflowSubscriptions(w http.ResponseWriter, r *http.Request) {
	act, id, ok := a.visibleWorkflow(w, r)
	if !ok {
		return
	}
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "Workflow not found")
		return
	}
	rows, err := a.DB.Query(r.Context(), `select id::text, workflow_id::text, source_kind, filter, enabled, debounce_ms, max_concurrent_runs,
		max_chain_depth_override, idempotency_key_template, created_at, updated_at
		from subscription where org_id = $1 and workflow_id = $2 order by created_at desc`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (subscriptionOut, error) {
		var s subscriptionOut
		var filter []byte
		var created, updated time.Time
		err := row.Scan(&s.ID, &s.WorkflowID, &s.SourceKind, &filter, &s.Enabled, &s.DebounceMs, &s.MaxConcurrentRuns,
			&s.MaxChainDepthOverride, &s.IdempotencyKeyTemplate, &created, &updated)
		s.Filter, s.CreatedAt, s.UpdatedAt = filter, jsTime(created), jsTime(updated)
		return s, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string][]subscriptionOut{"subscriptions": subs})
}

func (a *API) createSubscription(w http.ResponseWriter, r *http.Request) {
	act, id, ok := a.visibleWorkflow(w, r)
	if !ok {
		return
	}
	body, parsed := readBody(r)
	if parsed && body == nil {
		sendError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	kindValue, _ := field(body, "sourceKind")
	kind, _ := kindValue.(string)
	if !slices.Contains(sourceKinds, kind) {
		sendError(w, http.StatusBadRequest, "sourceKind required; one of "+strings.Join(sourceKinds, ", "))
		return
	}
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "Workflow not found")
		return
	}
	filter, _ := field(body, "filter")
	switch filter.(type) {
	case map[string]any, []any:
	default:
		filter = map[string]any{}
	}
	ctx := r.Context()

	if kind == "workflow_output" {
		matches, err := a.selfLoopOutputs(r, act.OrgID, id, filter)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if len(matches) > 0 {
			sendJSON(w, http.StatusUnprocessableEntity, struct {
				Error             string   `json:"error"`
				Code              string   `json:"code"`
				MatchingOutputIDs []string `json:"matchingOutputIds"`
			}{fmt.Sprintf("Subscription filter matches %d of this workflow's recent output(s) — this would create a self-loop. "+
				"Narrow the filter (scope, mood, kinds) so the workflow's own outputs no longer match.", len(matches)), "self_loop", matches})
			return
		}
	}

	if kind == "source_change" {
		sc, ok := parseSourceChange(filter)
		if !ok {
			sendJSON(w, http.StatusBadRequest, codedError{"source_change filter requires { table: string, primary_key: string[] } and optional where/select/version_column (identifiers only)", "invalid_filter"})
			return
		}
		if _, err := a.dataSourceURL(r, act.OrgID); errors.Is(err, pgx.ErrNoRows) {
			sendJSON(w, http.StatusUnprocessableEntity, codedError{noDataSource, "no_data_source"})
			return
		} else if err != nil {
			a.fail(w, r, err)
			return
		}
		if ack, _ := field(body, "acknowledgeMutationLoop"); !truthy(ack) {
			keyword, reason, err := a.mutationLoop(r, act.OrgID, id, sc.Table)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			template, _ := field(body, "idempotencyKeyTemplate")
			if s, _ := template.(string); keyword != "" && s == "" {
				sendJSON(w, http.StatusUnprocessableEntity, struct {
					Error           string `json:"error"`
					Code            string `json:"code"`
					MutationKeyword string `json:"mutationKeyword"`
				}{reason, "mutation_loop", keyword})
				return
			}
		}
	}

	whole := true
	number := func(name string, fallback any) any {
		if v, ok := field(body, name); ok {
			if f, ok := v.(float64); ok {
				whole = whole && f == math.Trunc(f)
				return f
			}
		}
		return fallback
	}
	debounce, concurrent, depth := number("debounceMs", 0), number("maxConcurrentRuns", 5), number("maxChainDepthOverride", nil)
	if !whole {
		sendError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	enabled := true
	if v, ok := fieldBool(body, "enabled"); ok {
		enabled = v
	}
	var template *string
	if v, _ := field(body, "idempotencyKeyTemplate"); v != nil {
		if s, ok := v.(string); ok {
			template = &s
		}
	}
	filterJSON, err := json.Marshal(filter)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var out struct {
		ID         string     `json:"id"`
		SourceKind string     `json:"sourceKind"`
		Filter     storedJSON `json:"filter"`
		Enabled    bool       `json:"enabled"`
	}
	var stored []byte
	err = a.DB.QueryRow(ctx, `insert into subscription (org_id, workflow_id, source_kind, filter, enabled, debounce_ms,
		max_concurrent_runs, max_chain_depth_override, idempotency_key_template)
		values ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9) returning id::text, source_kind, filter, enabled`,
		act.OrgID, id, kind, string(filterJSON), enabled, debounce, concurrent,
		depth, template).Scan(&out.ID, &out.SourceKind, &stored, &out.Enabled)
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == "23503" {
		sendError(w, http.StatusNotFound, "Workflow not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out.Filter = stored
	sendJSON(w, http.StatusOK, map[string]any{"subscription": out})
}

type codedError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// selfLoopOutputs is checkSubscriptionWouldLoop(): the ids of the
// workflow's last 100 outputs that the filter matches.
func (a *API) selfLoopOutputs(r *http.Request, orgID, workflowID string, filter any) ([]string, error) {
	rows, err := a.DB.Query(r.Context(), `select o.id::text, o.kind, o.scope, o.topic, o.mood
		from workflow_output o join workflow_run run on run.id = o.workflow_run_id
		where o.org_id = $1 and run.workflow_id = $2 order by o.created_at desc limit 100`, orgID, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := []string{}
	for rows.Next() {
		var id, kind string
		var scope, topic, mood *string
		if err := rows.Scan(&id, &kind, &scope, &topic, &mood); err != nil {
			return nil, err
		}
		if outputMatches(filter, kind, scope, topic, mood) {
			matches = append(matches, id)
		}
	}
	return matches, rows.Err()
}

// outputMatches is outputMatchesFilter() in packages/llm, with its
// JavaScript truthiness and includes() rules.
func outputMatches(filter any, kind string, scope, topic, mood *string) bool {
	equal := func(want any, got *string) bool {
		s, ok := want.(string)
		return ok && got != nil && *got == s
	}
	if v, _ := field(filter, "scope"); truthy(v) && !equal(v, scope) {
		return false
	}
	if v, _ := field(filter, "topic"); truthy(v) && !equal(v, topic) {
		return false
	}
	if v, _ := field(filter, "mood"); truthy(v) {
		moods, ok := v.([]any)
		if !ok {
			moods = []any{v}
		}
		if mood == nil || !slices.ContainsFunc(moods, func(m any) bool { return equal(m, mood) }) {
			return false
		}
	}
	if v, _ := field(filter, "kinds"); truthy(v) {
		switch k := v.(type) {
		case []any:
			if len(k) > 0 && !slices.Contains(k, any(kind)) {
				return false
			}
		case string:
			if !strings.Contains(k, kind) {
				return false
			}
		}
	}
	return true
}

var mutationKeywords = []string{"update", "insert", "write", "mutate", "modify", "set", "delete", "upsert", "patch", "create"}

// mutationLoop is detectMutationLoop() on the stored workflow; an empty
// keyword means no loop.
func (a *API) mutationLoop(r *http.Request, orgID, workflowID, table string) (string, string, error) {
	var goal, description string
	var steps []byte
	err := a.DB.QueryRow(r.Context(), `select coalesce(goal, ''), coalesce(description, ''), steps from workflow_definition
		where org_id = $1 and id = $2`, orgID, workflowID).Scan(&goal, &description, &steps)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	keyword, reason := mutationLoopText(table, goal, description, steps)
	return keyword, reason, nil
}

// mutationLoopText is detectMutationLoop() in packages/llm/src/workflows/cycle-detection.ts.
func mutationLoopText(table, goal, description string, steps []byte) (string, string) {
	parts := []string{goal, description}
	var list []any
	_ = json.Unmarshal(steps, &list)
	for _, s := range list {
		d, _ := field(s, "description")
		if d == nil {
			parts = append(parts, "")
		} else {
			parts = append(parts, jsStringOf(d))
		}
	}
	corpus := strings.ToLower(strings.Join(parts, "\n"))
	if !strings.Contains(corpus, strings.ToLower(table)) {
		return "", ""
	}
	for _, k := range mutationKeywords {
		if strings.Contains(corpus, k) {
			return k, fmt.Sprintf(`consumer workflow text mentions "%s" and the mutation verb "%s" — it may write back to the watched table and re-trigger itself. `+
				"Set idempotency_key_template (e.g. include {primary_key}) or pass acknowledgeMutationLoop: true to confirm.", table, k)
		}
	}
	return "", ""
}

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type sourceChange struct {
	Table         string
	PrimaryKey    []string
	Select        []string
	VersionColumn string
}

func identifiers(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(list))
	for i, e := range list {
		s, ok := e.(string)
		if !ok || !identifier.MatchString(s) {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// parseSourceChange is parseSourceChangeFilter() in packages/llm.
func parseSourceChange(filter any) (sourceChange, bool) {
	var sc sourceChange
	if _, ok := filter.(map[string]any); !ok {
		return sc, false
	}
	table, _ := field(filter, "table")
	sc.Table, _ = table.(string)
	if !identifier.MatchString(sc.Table) {
		return sc, false
	}
	pk, _ := field(filter, "primary_key")
	var ok bool
	if sc.PrimaryKey, ok = identifiers(pk); !ok || len(sc.PrimaryKey) == 0 {
		return sc, false
	}
	if where, _ := field(filter, "where"); where != nil {
		if _, ok := where.(map[string]any); !ok {
			return sc, false
		}
	}
	if sel, _ := field(filter, "select"); sel != nil {
		if sc.Select, ok = identifiers(sel); !ok {
			return sc, false
		}
	}
	if vc, _ := field(filter, "version_column"); vc != nil {
		s, ok := vc.(string)
		if !ok || !identifier.MatchString(s) {
			return sc, false
		}
		sc.VersionColumn = s
	}
	return sc, true
}

// dryRunQuery is buildSourceChangeDryRunQuery(); where is the filter's
// where object as sent, so its keys keep their order.
func dryRunQuery(sc sourceChange, where json.RawMessage, limit int) (string, error) {
	order := sc.VersionColumn
	if order == "" {
		order = sc.PrimaryKey[0]
	}
	cols := slices.Concat(sc.PrimaryKey, sc.Select)
	if sc.VersionColumn != "" {
		cols = append(cols, sc.VersionColumn)
	}
	seen := map[string]bool{}
	var projection []string
	for _, c := range cols {
		if !seen[c] {
			seen[c] = true
			projection = append(projection, "    "+c)
		}
	}
	var args []string
	if len(where) > 0 && string(where) != "null" {
		normal, err := jsJSON(where)
		if err != nil {
			return "", err
		}
		if string(normal) != "{}" {
			var buf bytes.Buffer
			dec := json.NewDecoder(bytes.NewReader(normal))
			dec.UseNumber()
			if err := graphqlLiteral(dec, &buf); err != nil {
				return "", err
			}
			args = append(args, "where: "+buf.String())
		}
	}
	args = append(args, "order_by: { "+order+": desc }", fmt.Sprintf("limit: %d", limit))
	return "query SourceChangeDryRun {\n  " + sc.Table + "(" + strings.Join(args, ", ") + ") {\n" +
		strings.Join(projection, "\n") + "\n  }\n}", nil
}

// graphqlLiteral is toGraphqlLiteral(): object keys bare, strings as JSON.
func graphqlLiteral(dec *json.Decoder, buf *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		open, end := "[", "]"
		if v == '{' {
			open, end = "{ ", " }"
		}
		buf.WriteString(open)
		for i := 0; dec.More(); i++ {
			if i > 0 {
				buf.WriteString(", ")
			}
			if v == '{' {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				buf.WriteString(k.(string) + ": ")
			}
			if err := graphqlLiteral(dec, buf); err != nil {
				return err
			}
		}
		buf.WriteString(end)
		_, err = dec.Token()
		return err
	case string:
		jsQuote(buf, v)
	case json.Number:
		buf.WriteString(v.String())
	case bool:
		fmt.Fprint(buf, v)
	default:
		buf.WriteString("null")
	}
	return nil
}

// jsQuote is JSON.stringify() for a string: unlike encoding/json, it
// leaves U+2028, U+2029 and HTML characters as they are.
func jsQuote(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, c)
			} else {
				buf.WriteRune(c)
			}
		}
	}
	buf.WriteByte('"')
}

func (a *API) dataSourceURL(r *http.Request, orgID string) (string, error) {
	var u string
	err := a.DB.QueryRow(r.Context(), `select graphql_url from data_source where org_id = $1
		order by is_default desc, created_at limit 1`, orgID).Scan(&u)
	return u, err
}

func (a *API) subscriptionDryRun(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("workflowId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "Workflow not found")
		return
	}
	var exists bool
	if err := a.DB.QueryRow(r.Context(), `select exists (select 1 from workflow_definition where org_id = $1 and id = $2)`, act.OrgID, id).Scan(&exists); err != nil {
		a.fail(w, r, err)
		return
	}
	hidden, err := a.workflowHidden(r.Context(), act, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !exists || hidden {
		sendError(w, http.StatusNotFound, "Workflow not found")
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	var body any
	if json.Unmarshal(raw, &body) == nil && body == nil {
		sendError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if kind, _ := field(body, "sourceKind"); kind != "source_change" {
		sendJSON(w, http.StatusBadRequest, codedError{`dry-run currently supports sourceKind="source_change" only`, "unsupported_source_kind"})
		return
	}
	filter, _ := field(body, "filter")
	sc, ok := parseSourceChange(filter)
	if !ok {
		sendJSON(w, http.StatusBadRequest, codedError{"filter requires { table: string, primary_key: string[] } and optional where/select/version_column (identifiers only)", "invalid_filter"})
		return
	}
	limit := 5
	if v, _ := field(body, "limit"); v != nil {
		if f, ok := v.(float64); ok && f > 0 && f <= 50 {
			limit = int(math.Floor(f))
		}
	}
	graphqlURL, err := a.dataSourceURL(r, act.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		sendJSON(w, http.StatusUnprocessableEntity, codedError{noDataSource, "no_data_source"})
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var top struct {
		Filter struct {
			Where json.RawMessage `json:"where"`
		} `json:"filter"`
	}
	_ = json.Unmarshal(raw, &top)
	query, err := dryRunQuery(sc, top.Filter.Where, limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	data, gqlErrors, err := graphjinQuery(r, graphqlURL, query)
	if err != nil {
		sendJSON(w, http.StatusBadGateway, struct {
			Error         string `json:"error"`
			Code          string `json:"code"`
			CompiledQuery string `json:"compiledQuery"`
		}{err.Error(), "graphjin_error", query})
		return
	}
	if len(gqlErrors) > 0 {
		sendJSON(w, http.StatusUnprocessableEntity, struct {
			Error          string            `json:"error"`
			Code           string            `json:"code"`
			CompiledQuery  string            `json:"compiledQuery"`
			GraphjinErrors []json.RawMessage `json:"graphjinErrors"`
		}{"graphjin returned errors", "graphjin_errors", query, gqlErrors})
		return
	}
	var tables map[string]json.RawMessage
	var list []json.RawMessage
	if json.Unmarshal(data, &tables) == nil {
		_ = json.Unmarshal(tables[sc.Table], &list)
	}
	rows := make([]storedJSON, len(list))
	for i, row := range list {
		rows[i] = storedJSON(row)
	}
	sendJSON(w, http.StatusOK, struct {
		CompiledQuery string         `json:"compiledQuery"`
		Variables     map[string]any `json:"variables"`
		RowCount      int            `json:"rowCount"`
		Rows          []storedJSON   `json:"rows"`
	}{query, map[string]any{}, len(rows), rows})
}

var graphjinClient = &http.Client{
	Timeout: 5 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirect")
	},
}

// graphjinQuery is graphjinQuery() in packages/llm/src/graphjin/client.ts
// with no token: it gives the data and the errors in Node's shape.
func graphjinQuery(r *http.Request, endpoint, query string) (json.RawMessage, []json.RawMessage, error) {
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{}})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to parse URL from %s", endpoint)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := graphjinClient.Do(req)
	if err != nil {
		return nil, nil, errors.New("fetch failed")
	}
	defer res.Body.Close()
	text, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, nil, errors.New("fetch failed")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, nil, fmt.Errorf("graphjin query failed: %d %s", res.StatusCode, utf16Prefix(string(text), 500))
	}
	var body struct {
		Data   json.RawMessage `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(text, &body); err != nil {
		return nil, nil, fmt.Errorf("graphjin returned a body that is not JSON: %v", err)
	}
	var items []json.RawMessage
	if json.Unmarshal(body.Errors, &items) != nil {
		return body.Data, nil, nil
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		e, err := graphjinError(item)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, e)
	}
	return body.Data, out, nil
}

func graphjinError(item json.RawMessage) (json.RawMessage, error) {
	type message struct {
		Message string     `json:"message"`
		Path    storedJSON `json:"path,omitempty"`
	}
	var v any
	if err := json.Unmarshal(item, &v); err != nil {
		return nil, err
	}
	var m message
	switch x := v.(type) {
	case string:
		m.Message = x
	case map[string]any, []any:
		var fields struct {
			Message any             `json:"message"`
			Path    json.RawMessage `json:"path"`
		}
		if obj, ok := x.(map[string]any); ok {
			_ = json.Unmarshal(item, &fields)
			fields.Message = obj["message"]
		}
		if s, ok := fields.Message.(string); ok {
			m.Message = s
		} else {
			normal, err := jsJSON(item)
			if err != nil {
				return nil, err
			}
			m.Message = string(normal)
		}
		if _, ok := field(x, "path"); ok && bytes.HasPrefix(bytes.TrimSpace(fields.Path), []byte("[")) {
			m.Path = storedJSON(fields.Path)
		}
	default:
		m.Message = jsStringOf(x)
	}
	return json.Marshal(m)
}

// utf16Prefix is s.slice(0, n) in JavaScript.
func utf16Prefix(s string, n int) string {
	units := 0
	for i, c := range s {
		units += len(utf16.Encode([]rune{c}))
		if units > n {
			return s[:i]
		}
	}
	return s
}

func (a *API) ownedSubscription(w http.ResponseWriter, r *http.Request) (string, bool) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return "", false
	}
	id := r.PathValue("subscriptionId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return "", false
	}
	var workflowID string
	err = a.DB.QueryRow(r.Context(), `select workflow_id::text from subscription where org_id = $1 and id = $2`, act.OrgID, id).Scan(&workflowID)
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return "", false
	}
	if err != nil {
		a.fail(w, r, err)
		return "", false
	}
	hidden, err := a.workflowHidden(r.Context(), act, workflowID)
	if err != nil {
		a.fail(w, r, err)
		return "", false
	}
	if hidden {
		sendError(w, http.StatusNotFound, "not found")
		return "", false
	}
	return id, true
}

func (a *API) subscriptionPatch(w http.ResponseWriter, r *http.Request) {
	body, parsed := readBody(r)
	id, ok := a.ownedSubscription(w, r)
	if !ok {
		return
	}
	if parsed && body == nil {
		sendError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if v, ok := fieldBool(body, "enabled"); ok {
		if _, err := a.DB.Exec(r.Context(), `update subscription set enabled = $2, updated_at = now() where id = $1`, id, v); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) subscriptionDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := a.ownedSubscription(w, r)
	if !ok {
		return
	}
	if _, err := a.DB.Exec(r.Context(), `delete from subscription where id = $1`, id); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
