package webfront

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

var (
	subscriptionRetryMin  = 5 * time.Second
	subscriptionHeldRetry = time.Minute
)

const (
	subscriptionRefreshEvery  = time.Minute
	subscriptionRetryMax      = time.Minute
	subscriptionFanoutWindow  = time.Minute
	subscriptionMaxChainDepth = 20
	subscriptionMaxFanout     = 32
	subscriptionDedupeWindow  = time.Hour
	subscriptionAncestorDepth = 64
	subscriptionMatchRows     = 1000
)

type liveSubscription struct {
	ID                     string
	OrgID                  string
	WorkflowID             string
	SourceKind             string
	Filter                 json.RawMessage
	MaxConcurrentRuns      int
	MaxChainDepthOverride  *int
	IdempotencyKeyTemplate *string
	UpdatedAt              time.Time
	BaselinePending        bool
}

func (a *API) enabledSubscriptions(ctx context.Context) ([]liveSubscription, error) {
	rows, err := a.DB.Query(ctx, `select id::text, org_id, workflow_id::text, source_kind, filter::text,
		max_concurrent_runs, max_chain_depth_override, idempotency_key_template, updated_at, baseline_pending
		from subscription where enabled order by created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[liveSubscription])
}

// subscriptionOperation is buildSubscriptionQuery() in packages/llm.
func subscriptionOperation(sub liveSubscription) (string, map[string]any, bool) {
	switch sub.SourceKind {
	case "workflow_output":
		var f map[string]any
		if json.Unmarshal(sub.Filter, &f) != nil {
			f = nil
		}
		return workflowOutputOperation(f, sub.OrgID)
	case "source_change":
		var f map[string]any
		var raw map[string]json.RawMessage
		if json.Unmarshal(sub.Filter, &f) != nil || json.Unmarshal(sub.Filter, &raw) != nil {
			return "", nil, false
		}
		sc, ok := parseSourceChange(f)
		if !ok {
			return "", nil, false
		}
		q, err := sourceChangeOperation("subscription SourceChangeMatch", sc, raw["where"], subscriptionMatchRows)
		if err != nil {
			return "", nil, false
		}
		return q, map[string]any{}, true
	}
	return "", nil, false
}

func workflowOutputOperation(f map[string]any, orgID string) (string, map[string]any, bool) {
	vars := map[string]any{"org_id": orgID}
	where := []string{"org_id: { eq: $org_id }"}
	if v := f["scope"]; truthy(v) {
		vars["scope"] = v
		where = append(where, "scope: { eq: $scope }")
	}
	if v := f["topic"]; truthy(v) {
		vars["topic"] = v
		where = append(where, "topic: { eq: $topic }")
	}
	switch v := f["mood"].(type) {
	case []any:
		where = append(where, "mood: { in: "+graphqlStrings(v)+" }")
	default:
		if truthy(v) {
			vars["mood"] = v
			where = append(where, "mood: { eq: $mood }")
		}
	}
	if kinds, ok := f["kinds"].([]any); ok && len(kinds) > 0 {
		where = append(where, "kind: { in: "+graphqlStrings(kinds)+" }")
	}
	return `subscription WorkflowOutputMatch {
  workflow_output(where: { ` + strings.Join(where, ", ") + ` }, order_by: { created_at: desc }, limit: 1) {
    id
    org_id
    workflow_run_id
    kind
    scope
    topic
    mood
    title
    created_at
  }
}`, vars, true
}

func graphqlStrings(list []any) string {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, v := range list {
		if i > 0 {
			buf.WriteString(", ")
		}
		jsQuote(&buf, jsStringOf(v))
	}
	buf.WriteByte(']')
	return buf.String()
}

type outputMatch struct {
	ID, OrgID, WorkflowRunID, Kind string
	Scope, Topic, Mood             *string
	Title, CreatedAt               string
}

func tableRows(payload any, table string) ([]any, bool) {
	msg, ok := payload.(*jsObj)
	if !ok {
		return nil, false
	}
	data, ok := msg.vals["data"].(*jsObj)
	if !ok {
		return nil, false
	}
	rows, ok := data.vals[table].([]any)
	return rows, ok
}

func firstRow(payload any, table string) (*jsObj, bool) {
	rows, ok := tableRows(payload, table)
	if !ok || len(rows) == 0 {
		return nil, false
	}
	row, ok := rows[0].(*jsObj)
	return row, ok
}

// parseOutputMatch is parseWorkflowOutputMatch() in packages/llm.
func parseOutputMatch(payload any) (outputMatch, bool) {
	row, ok := firstRow(payload, "workflow_output")
	if !ok {
		return outputMatch{}, false
	}
	id, _ := row.vals["id"].(string)
	if id == "" {
		return outputMatch{}, false
	}
	nullable := func(k string) *string {
		v := row.vals[k]
		if v == nil {
			return nil
		}
		s := jsStringOf(v)
		return &s
	}
	title := ""
	if v := row.vals["title"]; v != nil {
		title = jsStringOf(v)
	}
	str := func(k string) string {
		v, ok := row.vals[k]
		if !ok {
			return "undefined"
		}
		return jsStringOf(v)
	}
	return outputMatch{ID: id, OrgID: str("org_id"), WorkflowRunID: str("workflow_run_id"), Kind: str("kind"),
		Scope: nullable("scope"), Topic: nullable("topic"), Mood: nullable("mood"), Title: title,
		CreatedAt: str("created_at")}, true
}

type sourceMatch struct {
	Table        string
	PrimaryKey   *jsObj
	Snapshot     *jsObj
	VersionToken *string
	RowHash      string
}

// parseSourceMatches reads each row of a push. It reports false when the
// push holds no row list for the table.
func parseSourceMatches(payload any, sc sourceChange) ([]sourceMatch, bool) {
	rows, ok := tableRows(payload, sc.Table)
	if !ok {
		return nil, false
	}
	out := make([]sourceMatch, 0, len(rows))
	for _, r := range rows {
		if row, ok := r.(*jsObj); ok {
			if m, ok := parseSourceRow(row, sc); ok {
				out = append(out, m)
			}
		}
	}
	return out, true
}

func parseSourceRow(row *jsObj, sc sourceChange) (sourceMatch, bool) {
	pk := &jsObj{}
	for _, col := range sc.PrimaryKey {
		v, ok := row.vals[col]
		if !ok {
			return sourceMatch{}, false
		}
		pk.set(col, v)
	}
	sum := sha256.Sum256([]byte(jsStringify(row, false)))
	m := sourceMatch{Table: sc.Table, PrimaryKey: pk, Snapshot: row, RowHash: hex.EncodeToString(sum[:])[:16]}
	if sc.VersionColumn != "" {
		if v := row.vals[sc.VersionColumn]; v != nil {
			s := jsStringOf(v)
			m.VersionToken = &s
		}
	}
	return m, true
}

type observationInput struct {
	SourceOutputID *string
	Title          *string
	Body           *string
	Mood           *string
}

type matchPlan struct {
	Observation    observationInput
	TriggerPayload func(observationID string) *jsObj
	OutputID       string
	DedupeKey      string
	AuditTable     string
	Audit          func(observationID string) *jsObj
	SeenKey        string
	SeenRow        string
}

func outputMatchPlan(sub liveSubscription, m outputMatch) matchPlan {
	var title *string
	if m.Title != "" {
		title = &m.Title
	}
	key := sub.ID + ":" + m.ID + ":" + m.CreatedAt
	if sub.IdempotencyKeyTemplate != nil && *sub.IdempotencyKeyTemplate != "" {
		key = replaceFirst(*sub.IdempotencyKeyTemplate, "{subscription_id}", sub.ID)
		key = replaceFirst(key, "{source_record_id}", m.ID)
		key = replaceFirst(key, "{source_version}", m.CreatedAt)
	}
	return matchPlan{
		Observation: observationInput{SourceOutputID: &m.ID, Title: title, Mood: m.Mood},
		TriggerPayload: func(obs string) *jsObj {
			return jsObjOf("subscription_id", sub.ID, "output_id", m.ID, "observation_id", obs)
		},
		OutputID:  m.ID,
		DedupeKey: key,
	}
}

func sourceMatchPlan(sub liveSubscription, m sourceMatch, dataSourceID string) matchPlan {
	var version any
	versionText := m.RowHash
	if m.VersionToken != nil {
		version = *m.VersionToken
		versionText = *m.VersionToken
	}
	pkHash := hashPrimaryKey(m.PrimaryKey)
	key := sub.ID + ":" + pkHash + ":" + versionText
	if sub.IdempotencyKeyTemplate != nil && *sub.IdempotencyKeyTemplate != "" {
		key = replaceFirst(*sub.IdempotencyKeyTemplate, "{subscription_id}", sub.ID)
		key = replaceFirst(key, "{source_record_id}", pkHash)
		key = replaceFirst(key, "{primary_key}", pkHash)
		key = replaceFirst(key, "{source_version}", versionText)
	}
	title := m.Table + " " + summarizePrimaryKey(m.PrimaryKey)
	body := utf16Prefix(jsStringify(m.Snapshot, false), 4000)
	return matchPlan{
		Observation: observationInput{Title: &title, Body: &body},
		TriggerPayload: func(obs string) *jsObj {
			return jsObjOf("subscription_id", sub.ID, "observation_id", obs, "table", m.Table,
				"primary_key", m.PrimaryKey, "snapshot", m.Snapshot, "version_token", version)
		},
		DedupeKey:  key,
		SeenKey:    pkHash,
		SeenRow:    m.RowHash,
		AuditTable: m.Table,
		Audit: func(obs string) *jsObj {
			return jsObjOf("subscription_id", sub.ID, "observation_id", obs, "primary_key", m.PrimaryKey,
				"snapshot", m.Snapshot, "version_token", version)
		},
	}
}

// replaceFirst is String.prototype.replace() with a string pattern.
func replaceFirst(s, old, repl string) string {
	return strings.Replace(s, old, repl, 1)
}

var (
	keyCollatorMu sync.Mutex
	keyCollator   = collate.New(language.Und)
)

func hashPrimaryKey(pk *jsObj) string {
	keys := pk.ordered()
	keyCollatorMu.Lock()
	slices.SortStableFunc(keys, keyCollator.CompareString)
	keyCollatorMu.Unlock()
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + scalarText(pk.vals[k])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])[:16]
}

func summarizePrimaryKey(pk *jsObj) string {
	keys := pk.ordered()
	if len(keys) == 0 {
		return "()"
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + scalarText(pk.vals[k])
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func scalarText(v any) string {
	if v == nil {
		return ""
	}
	return jsStringOf(v)
}

type matchDecision struct {
	Dropped       string
	Held          bool
	ObservationID string
	JobID         string
}

func (a *API) subscriptionGuards(ctx context.Context, sub liveSubscription, queued int) (string, error) {
	var running int
	if err := a.DB.QueryRow(ctx, `select count(*) from workflow_run where triggered_by_subscription_id = $1 and status = 'running'`,
		sub.ID).Scan(&running); err != nil {
		return "", err
	}
	if running += queued; running >= sub.MaxConcurrentRuns {
		return fmt.Sprintf("subscription %s at max_concurrent_runs (%d/%d)", sub.ID, running, sub.MaxConcurrentRuns), nil
	}
	var budget *int
	err := a.DB.QueryRow(ctx, `select daily_run_budget from workflow_definition where org_id = $1 and id = $2::uuid`,
		sub.OrgID, sub.WorkflowID).Scan(&budget)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if budget != nil {
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		var ran int
		if err := a.DB.QueryRow(ctx, `select count(*) from workflow_run where org_id = $1 and workflow_id = $2::uuid and created_at >= $3`,
			sub.OrgID, sub.WorkflowID, today).Scan(&ran); err != nil {
			return "", err
		}
		if ran += queued; ran >= *budget {
			return fmt.Sprintf("consumer workflow %s reached daily_run_budget (%d/%d)", sub.WorkflowID, ran, *budget), nil
		}
	}
	return "", nil
}

// handleOutputMatch is handleSubscriptionMatch() in packages/llm.
func (a *API) handleOutputMatch(ctx context.Context, sub liveSubscription, m outputMatch) (matchDecision, error) {
	if m.OrgID != sub.OrgID {
		return matchDecision{Dropped: fmt.Sprintf("org mismatch (sub=%s output=%s)", sub.OrgID, m.OrgID)}, nil
	}
	maxChain := subscriptionMaxChainDepth
	if sub.MaxChainDepthOverride != nil {
		maxChain = *sub.MaxChainDepthOverride
	}
	var depth int
	err := a.DB.QueryRow(ctx, `select chain_depth from workflow_run where id = $1::uuid`, m.WorkflowRunID).Scan(&depth)
	switch {
	case err == nil:
		if depth+1 > maxChain {
			return matchDecision{Dropped: fmt.Sprintf("chain depth %d exceeds max %d", depth+1, maxChain)}, nil
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return matchDecision{}, err
	}
	var cycle bool
	if err := a.DB.QueryRow(ctx, `with recursive chain as (
		select wr.id as run_id, wr.workflow_id, wr.triggered_by_observation_id, 0 as depth
		from workflow_run wr where wr.id = $1::uuid
		union all
		select producer.id, producer.workflow_id, producer.triggered_by_observation_id, chain.depth + 1
		from chain
		join observation o on o.id = chain.triggered_by_observation_id
		join workflow_output wo on wo.id = o.source_output_id
		join workflow_run producer on producer.id = wo.workflow_run_id
		where chain.triggered_by_observation_id is not null and chain.depth < $3)
		select exists (select 1 from chain where workflow_id = $2::uuid)`,
		m.WorkflowRunID, sub.WorkflowID, subscriptionAncestorDepth).Scan(&cycle); err != nil {
		return matchDecision{}, err
	}
	if cycle {
		return matchDecision{Dropped: fmt.Sprintf("cycle detected — consumer workflow %s is already in the producing run's chain", sub.WorkflowID)}, nil
	}
	var fanout int
	if err := a.DB.QueryRow(ctx, `select count(*) from observation where source_output_id = $1::uuid and created_at >= $2`,
		m.ID, time.Now().Add(-subscriptionFanoutWindow)).Scan(&fanout); err != nil {
		return matchDecision{}, err
	}
	if fanout >= subscriptionMaxFanout {
		return matchDecision{Dropped: fmt.Sprintf("fanout cap reached (%d/%d) for output %s", fanout, subscriptionMaxFanout, m.ID)}, nil
	}
	if reason, err := a.subscriptionGuards(ctx, sub, 0); err != nil || reason != "" {
		return matchDecision{Dropped: reason}, err
	}
	return a.commitMatch(ctx, sub, outputMatchPlan(sub, m), "")
}

// handleSourceMatch is handleSourceChangeMatch() in packages/llm.
func (a *API) handleSourceMatch(ctx context.Context, sub liveSubscription, m sourceMatch, dataSourceID string, queued int) (matchDecision, error) {
	needle := jsStringify([]any{jsObjOf("table", m.Table, "primary_key", m.PrimaryKey)}, false)
	var recent bool
	if err := a.DB.QueryRow(ctx, `select exists (select 1 from workflow_run where workflow_id = $1::uuid
		and created_at >= $2 and source_writes @> $3::jsonb)`,
		sub.WorkflowID, time.Now().Add(-subscriptionFanoutWindow), needle).Scan(&recent); err != nil {
		return matchDecision{}, err
	}
	if recent {
		if _, err := a.DB.Exec(ctx, markSeenSQL, sub.ID, hashPrimaryKey(m.PrimaryKey), m.RowHash); err != nil {
			return matchDecision{}, err
		}
		return matchDecision{Dropped: fmt.Sprintf("cycle detected — workflow %s recently wrote to %s:%s",
			sub.WorkflowID, m.Table, jsStringify(m.PrimaryKey, false))}, nil
	}
	if reason, err := a.subscriptionGuards(ctx, sub, queued); err != nil || reason != "" {
		return matchDecision{Dropped: reason, Held: reason != ""}, err
	}
	return a.commitMatch(ctx, sub, sourceMatchPlan(sub, m, dataSourceID), dataSourceID)
}

const markSeenSQL = `insert into subscription_seen (subscription_id, key_hash, row_hash) values ($1::uuid, $2, $3)
	on conflict (subscription_id, key_hash) do update set row_hash = excluded.row_hash, seen_at = now()`

// handleSourceRows queues one run for each row of a push that is new or
// changed since the last push. It reports true when the run limit held a
// row back.
func (a *API) handleSourceRows(ctx context.Context, sub liveSubscription, sc sourceChange, payload any) (bool, error) {
	matches, ok := parseSourceMatches(payload, sc)
	if !ok {
		return false, nil
	}
	dsID, _, err := a.orgDataSource(ctx, sub.OrgID)
	if err != nil {
		return false, err
	}
	if sub.BaselinePending {
		if done, err := a.baselineSourceRows(ctx, sub, matches); done || err != nil {
			return false, err
		}
	}
	rows, err := a.DB.Query(ctx, `select key_hash, row_hash from subscription_seen where subscription_id = $1::uuid`, sub.ID)
	if err != nil {
		return false, err
	}
	seen := map[string]string{}
	var k, h string
	if _, err := pgx.ForEachRow(rows, []any{&k, &h}, func() error { seen[k] = h; return nil }); err != nil {
		return false, err
	}
	keys := make([]string, len(matches))
	queued, held, reason := 0, false, ""
	for i, m := range matches {
		keys[i] = hashPrimaryKey(m.PrimaryKey)
		if held || seen[keys[i]] == m.RowHash {
			continue
		}
		d, err := a.handleSourceMatch(ctx, sub, m, dsID, queued)
		switch {
		case err != nil:
			return true, err
		case d.Held:
			held, reason = true, d.Dropped
		case d.Dropped != "":
			log.Printf("[subscription-manager] dropped sub=%s %s:%s: %s", sub.ID, m.Table, jsStringify(m.PrimaryKey, false), d.Dropped)
		case d.JobID != "":
			queued++
		}
	}
	if _, err := a.DB.Exec(ctx, `delete from subscription_seen where subscription_id = $1::uuid and not (key_hash = any($2))`,
		sub.ID, keys); err != nil {
		return held, err
	}
	if len(matches) >= subscriptionMatchRows {
		log.Printf("[subscription-manager] sub=%s matches %d rows or more; the filter is too wide and later rows get no run",
			sub.ID, subscriptionMatchRows)
	}
	if queued > 0 || held {
		log.Printf("[subscription-manager] sub=%s %s rows=%d queued=%d held=%q", sub.ID, sc.Table, len(matches), queued, reason)
	}
	return held, nil
}

// baselineSourceRows records the rows of the first push as seen, with no
// run, for a subscription that is older than the seen table. It reports
// false when the baseline is already done.
func (a *API) baselineSourceRows(ctx context.Context, sub liveSubscription, matches []sourceMatch) (bool, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `update subscription set baseline_pending = false where id = $1::uuid and baseline_pending`, sub.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	for _, m := range matches {
		if _, err := tx.Exec(ctx, markSeenSQL, sub.ID, hashPrimaryKey(m.PrimaryKey), m.RowHash); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	log.Printf("[subscription-manager] sub=%s %s baseline of %d rows, no run", sub.ID, sub.SourceKind, len(matches))
	return true, nil
}

func (a *API) commitMatch(ctx context.Context, sub liveSubscription, p matchPlan, dataSourceID string) (matchDecision, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return matchDecision{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var obs string
	o := p.Observation
	if err := tx.QueryRow(ctx, `insert into observation (org_id, source_output_id, consumer_kind, consumer_workflow_id,
		subscription_id, title, body, mood) values ($1, $2::uuid, 'workflow', $3::uuid, $4::uuid, $5, $6, $7) returning id::text`,
		sub.OrgID, o.SourceOutputID, sub.WorkflowID, sub.ID, o.Title, o.Body, o.Mood).Scan(&obs); err != nil {
		return matchDecision{}, err
	}
	if p.SeenKey != "" {
		if _, err := tx.Exec(ctx, markSeenSQL, sub.ID, p.SeenKey, p.SeenRow); err != nil {
			return matchDecision{}, err
		}
	}
	if p.Audit != nil {
		if _, err := tx.Exec(ctx, `insert into source_change_log (org_id, source_id, table_name, change_kind, payload)
			values ($1, $2::uuid, $3, 'subscription_match', $4::jsonb)`,
			sub.OrgID, dataSourceID, p.AuditTable, jsStringify(p.Audit(obs), false)); err != nil {
			return matchDecision{}, err
		}
	}
	job := jobs.WorkflowRunFireArgs{OrgID: sub.OrgID, WorkflowID: sub.WorkflowID, TriggerKind: "subscription",
		TriggerPayload: json.RawMessage(jsStringify(p.TriggerPayload(obs), false)), TriggeredBySubscriptionID: sub.ID,
		TriggeredByOutputID: p.OutputID, TriggeredByObservationID: obs}
	id, err := a.enqueueWorkflowRunFire(ctx, tx, job, p.DedupeKey, subscriptionDedupeWindow)
	if err != nil {
		return matchDecision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return matchDecision{}, err
	}
	return matchDecision{ObservationID: obs, JobID: id}, nil
}

func graphqlEndpoint(u string) string {
	u = strings.TrimRight(u, "/")
	if strings.HasSuffix(u, "/api/v1/graphql") {
		return u
	}
	return u + "/api/v1/graphql"
}

func subscriptionsGraphjinURL() string {
	if u := os.Getenv("OPENNEKO_GRAPHJIN_URL"); u != "" {
		return graphqlEndpoint(u)
	}
	return graphqlEndpoint("http://127.0.0.1:8089")
}

func (a *API) orgDataSource(ctx context.Context, orgID string) (id, graphqlURL string, err error) {
	err = a.DB.QueryRow(ctx, `select id::text, graphql_url from data_source where org_id = $1
		order by is_default desc, created_at limit 1`, orgID).Scan(&id, &graphqlURL)
	if errors.Is(err, pgx.ErrNoRows) {
		err = fmt.Errorf("no data_source configured for org %s", orgID)
	}
	return id, graphqlURL, err
}

var errSubscriptionNotWired = errors.New("not wired or filter invalid")

type wsMessage struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// followSubscription holds one graphql-transport-ws subscription open and
// handles each match until ctx ends or the server ends the stream.
func (a *API) followSubscription(ctx context.Context, sub liveSubscription, acked func()) error {
	query, vars, ok := subscriptionOperation(sub)
	if !ok {
		return errSubscriptionNotWired
	}
	endpoint := subscriptionsGraphjinURL()
	var sc sourceChange
	initPayload := map[string]any{}
	if sub.SourceKind == "source_change" {
		var f map[string]any
		_ = json.Unmarshal(sub.Filter, &f)
		sc, _ = parseSourceChange(f)
		id, u, err := a.orgDataSource(ctx, sub.OrgID)
		if err != nil {
			return err
		}
		endpoint = graphqlEndpoint(u)
		owner, err := a.workflowPrincipalByID(ctx, sub.OrgID, sub.WorkflowID)
		if err != nil {
			return err
		}
		var authMode string
		if err := a.DB.QueryRow(ctx, `select auth_mode from data_source where id::text = $1`, id).Scan(&authMode); err != nil {
			return err
		}
		if authMode == "jwt" {
			token, err := a.graphjinPrincipalToken(ctx, sub.OrgID, owner, 5*time.Minute)
			if err != nil {
				return err
			}
			initPayload["Authorization"] = "Bearer " + token
		}
	}
	wsURL := endpoint
	if strings.HasPrefix(wsURL, "http") {
		wsURL = "ws" + strings.TrimPrefix(wsURL, "http")
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{"graphql-transport-ws"}})
	if err != nil {
		return fmt.Errorf("graphjin subscription socket error: %w", err)
	}
	conn.SetReadLimit(32 << 20)
	defer conn.CloseNow() //nolint:errcheck
	connCtx, stopConn := context.WithCancel(context.WithoutCancel(ctx))
	defer stopConn()
	send := func(m any) error {
		b, _ := json.Marshal(m)
		return conn.Write(connCtx, websocket.MessageText, b)
	}
	subID := uuid.NewString()
	pushes := make(chan json.RawMessage, 16)
	go a.handlePushes(connCtx, sub, sc, pushes)
	go func() {
		select {
		case <-ctx.Done():
			_ = send(map[string]any{"type": "complete", "id": subID})
			conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
		case <-connCtx.Done():
		}
	}()
	if err := send(map[string]any{"type": "connection_init", "payload": initPayload}); err != nil {
		return err
	}
	for {
		_, data, err := conn.Read(connCtx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("graphjin subscription socket error: %w", err)
		}
		var msg wsMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "connection_ack":
			if err := send(map[string]any{"type": "subscribe", "id": subID,
				"payload": map[string]any{"query": query, "variables": vars}}); err != nil {
				return err
			}
			acked()
		case "ping":
			if err := send(map[string]any{"type": "pong"}); err != nil {
				return err
			}
		case "next":
			if msg.ID == subID {
				select {
				case pushes <- msg.Payload:
				case <-connCtx.Done():
				}
			}
		case "error":
			if msg.ID == subID {
				return errors.New(subscriptionErrorText(msg.Payload))
			}
		case "complete":
			if msg.ID == subID {
				return errors.New("the server ended the subscription")
			}
		}
	}
}

func subscriptionErrorText(payload json.RawMessage) string {
	var v any
	_ = json.Unmarshal(payload, &v)
	list, ok := v.([]any)
	if !ok {
		if errs, ok := field(v, "errors"); ok {
			list, _ = errs.([]any)
		}
	}
	var parts []string
	for _, e := range list {
		if m, ok := field(e, "message"); ok {
			parts = append(parts, fmt.Sprint(m))
		} else if s, ok := e.(string); ok {
			parts = append(parts, s)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; ")
	}
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "subscription error"
}

// handlePushes handles each push in order. It handles the last push again
// after a wait when the run limit held a row back.
func (a *API) handlePushes(ctx context.Context, sub liveSubscription, sc sourceChange, pushes <-chan json.RawMessage) {
	var last json.RawMessage
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case last = <-pushes:
		case <-retry:
		}
		retry = nil
		if a.onSubscriptionNext(ctx, sub, sc, last) {
			retry = time.After(subscriptionHeldRetry)
		}
	}
}

func (a *API) onSubscriptionNext(ctx context.Context, sub liveSubscription, sc sourceChange, payload json.RawMessage) bool {
	msg, err := jsParse(payload)
	if err != nil {
		return false
	}
	if sub.SourceKind == "source_change" {
		held, err := a.handleSourceRows(ctx, sub, sc, msg)
		if err != nil && ctx.Err() == nil {
			log.Printf("[subscription-manager] error sub=%s: %v", sub.ID, err)
		}
		return held
	}
	var d matchDecision
	var what string
	switch sub.SourceKind {
	case "workflow_output":
		m, ok := parseOutputMatch(msg)
		if !ok {
			return false
		}
		what = "output=" + m.ID
		d, err = a.handleOutputMatch(ctx, sub, m)
	default:
		return false
	}
	switch {
	case err != nil:
		log.Printf("[subscription-manager] error sub=%s: %v", sub.ID, err)
	case d.Dropped != "":
		log.Printf("[subscription-manager] dropped sub=%s %s: %s", sub.ID, what, d.Dropped)
	default:
		log.Printf("[subscription-manager] enqueued sub=%s %s obs=%s", sub.ID, what, d.ObservationID)
	}
	return false
}

type openSubscription struct {
	version time.Time
	cancel  context.CancelFunc
	done    chan struct{}
}

type subscriptionManager struct {
	api  *API
	open map[string]openSubscription
}

func (m *subscriptionManager) follow(ctx context.Context, sub liveSubscription) {
	wait := subscriptionRetryMin
	for {
		err := m.api.followSubscription(ctx, sub, func() { wait = subscriptionRetryMin })
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errSubscriptionNotWired) {
			log.Printf("[subscription-manager] skipping subscription %s — source_kind=%q not wired or filter invalid", sub.ID, sub.SourceKind)
			return
		}
		log.Printf("[subscription-manager] error sub=%s: %v; retry in %s", sub.ID, err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, subscriptionRetryMax)
	}
}

func (m *subscriptionManager) refresh(ctx context.Context) error {
	subs, err := m.api.enabledSubscriptions(ctx)
	if err != nil {
		return err
	}
	want := map[string]liveSubscription{}
	for _, s := range subs {
		want[s.ID] = s
	}
	for id, o := range m.open {
		if s, ok := want[id]; !ok || !s.UpdatedAt.Equal(o.version) {
			o.cancel()
			<-o.done
			delete(m.open, id)
		}
	}
	for _, s := range subs {
		if _, ok := m.open[s.ID]; ok {
			continue
		}
		subCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		m.open[s.ID] = openSubscription{version: s.UpdatedAt, cancel: cancel, done: done}
		go func() {
			defer close(done)
			m.follow(subCtx, s)
		}()
	}
	return nil
}

func (m *subscriptionManager) stop() {
	for id, o := range m.open {
		o.cancel()
		<-o.done
		delete(m.open, id)
	}
}

// StartSubscriptionManager follows each enabled subscription on GraphJin and
// queues a workflow run for each match. It reads the subscription list each
// minute until ctx ends.
func (a *API) StartSubscriptionManager(ctx context.Context) {
	go a.runSubscriptionManager(ctx, subscriptionRefreshEvery)
}

func (a *API) runSubscriptionManager(ctx context.Context, every time.Duration) {
	m := &subscriptionManager{api: a, open: map[string]openSubscription{}}
	defer m.stop()
	if err := m.refresh(ctx); err != nil {
		log.Printf("[subscription-manager] initial connect failed: %v", err)
	} else {
		log.Printf("[subscription-manager] ready (%d active)", len(m.open))
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := m.refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[subscription-manager] error: %v", err)
			}
		}
	}
}
