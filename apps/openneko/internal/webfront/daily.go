package webfront

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func jsTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func jsTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := jsTime(*t)
	return &s
}

// queryLimit matches Math.min(Math.max(Number(value ?? fallback), 1), 500).
// A value that is not a number gives the fallback.
func queryLimit(r *http.Request, fallback int) int {
	raw, ok := r.URL.Query()["limit"]
	if !ok {
		return fallback
	}
	s := strings.TrimSpace(raw[0])
	n := 0.0
	if s != "" {
		var err error
		if n, err = strconv.ParseFloat(s, 64); err != nil || math.IsNaN(n) {
			return fallback
		}
	}
	return int(math.Trunc(math.Min(math.Max(n, 1), 500)))
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (a *API) observations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	consumerWorkflowID, sourceOutputID, status := q.Get("consumerWorkflowId"), q.Get("sourceOutputId"), q.Get("status")
	type observationOut struct {
		ID                 string  `json:"id"`
		SourceOutputID     *string `json:"sourceOutputId"`
		ConsumerKind       string  `json:"consumerKind"`
		ConsumerWorkflowID *string `json:"consumerWorkflowId"`
		ConsumerRunID      *string `json:"consumerRunId"`
		ConsumerUserID     *string `json:"consumerUserId"`
		SubscriptionID     *string `json:"subscriptionId"`
		Title              *string `json:"title"`
		Body               *string `json:"body"`
		Mood               *string `json:"mood"`
		Status             string  `json:"status"`
		FirstSeenAt        string  `json:"firstSeenAt"`
		LastSeenAt         string  `json:"lastSeenAt"`
		CreatedAt          string  `json:"createdAt"`
	}
	out := []observationOut{}
	if (consumerWorkflowID != "" && !isUUID(consumerWorkflowID)) || (sourceOutputID != "" && !isUUID(sourceOutputID)) {
		sendJSON(w, http.StatusOK, map[string]any{"observations": out})
		return
	}
	rows, err := a.DB.Query(ctx, `
		select id::text, source_output_id::text, consumer_kind, consumer_workflow_id::text, consumer_run_id::text,
		  consumer_user_id, subscription_id::text, title, body, mood, status, first_seen_at, last_seen_at, created_at
		from observation
		where org_id = $1
		  and ($2 = '' or consumer_workflow_id = nullif($2, '')::uuid)
		  and ($3 = '' or source_output_id = nullif($3, '')::uuid)
		  and ($4 = '' or status = $4)
		order by created_at desc
		limit $5`, act.OrgID, consumerWorkflowID, sourceOutputID, status, queryLimit(r, 50))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for rows.Next() {
		var o observationOut
		var first, last, created time.Time
		if err := rows.Scan(&o.ID, &o.SourceOutputID, &o.ConsumerKind, &o.ConsumerWorkflowID, &o.ConsumerRunID,
			&o.ConsumerUserID, &o.SubscriptionID, &o.Title, &o.Body, &o.Mood, &o.Status, &first, &last, &created); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		o.FirstSeenAt, o.LastSeenAt, o.CreatedAt = jsTime(first), jsTime(last), jsTime(created)
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	visible, err := a.workflowVisibility(ctx, act)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	owners, err := a.workflowOwners(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	shown := out[:0]
	for _, o := range out {
		var ok bool
		if o.ConsumerWorkflowID != nil {
			ok = visible(*o.ConsumerWorkflowID, owners[*o.ConsumerWorkflowID])
		} else {
			ok = o.ConsumerUserID == nil || *o.ConsumerUserID == "" || *o.ConsumerUserID == act.UserID
		}
		if ok {
			shown = append(shown, o)
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"observations": shown})
}

// actionOutcome matches actionOutcome() in apps/web/src/lib/action-outcome.ts.
func actionOutcome(status string, executionResultStatus *string) string {
	result := deref(executionResultStatus)
	switch {
	case status == "pending_approval":
		return "waiting"
	case status == "rejected":
		return "rejected"
	case status == "failed":
		return "failed"
	case result == "reconcile_required":
		return "needs_check"
	case result == "partially_applied":
		return "partial"
	case result == "failed":
		return "failed"
	case status == "executed":
		return "completed"
	}
	return "approved"
}

var riskOrder = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}

func riskRank(level *string) int {
	if n, ok := riskOrder[deref(level)]; ok {
		return n
	}
	return 99
}

const unconfirmedExecution = `exists (
  select 1 from action_execution e
  where e.action_request_id = a.id
    and e.result->>'status' in ('reconcile_required', 'partially_applied')
    and e.created_at = (select max(e2.created_at) from action_execution e2 where e2.action_request_id = a.id))`

var approvalFilters = map[string]string{
	"awaiting": `a.status = 'pending_approval'`,
	"fired":    `a.status in ('executed', 'approved') and not ` + unconfirmedExecution,
	"failed":   `(a.status = 'failed' or (a.status = 'executed' and ` + unconfirmedExecution + `))`,
	"rejected": `a.status = 'rejected'`,
	"all":      `true`,
}

type approvalWorkflow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type observationTitle struct {
	Title string `json:"title"`
}

type approvalOut struct {
	ID                     string            `json:"id"`
	WorkflowRunID          *string           `json:"workflowRunId"`
	Workflow               *approvalWorkflow `json:"workflow"`
	TriggeredByObservation *observationTitle `json:"triggeredByObservation"`
	Kind                   string            `json:"kind"`
	Target                 *string           `json:"target"`
	Payload                storedJSON        `json:"payload"`
	RiskLevel              *string           `json:"riskLevel"`
	Summary                *string           `json:"summary"`
	Scope                  string            `json:"scope"`
	Status                 string            `json:"status"`
	MinutesSaved           *int              `json:"minutesSaved"`
	ApprovedAt             *string           `json:"approvedAt"`
	ApproverKind           *string           `json:"approverKind"`
	ApproverLabel          *string           `json:"approverLabel"`
	RejectionReason        *string           `json:"rejectionReason"`
	FailureReason          *string           `json:"failureReason"`
	Outcome                string            `json:"outcome"`
	ExecutionError         *string           `json:"executionError"`
	RunAt                  string            `json:"runAt"`
	CreatedAt              string            `json:"createdAt"`

	created time.Time
}

func approverKind(approvedByUserID, policyID *string, approvedAt *time.Time) *string {
	var kind string
	switch {
	case deref(approvedByUserID) != "":
		kind = "operator"
	case deref(policyID) != "":
		kind = "policy"
	case approvedAt != nil:
		kind = "auto"
	default:
		return nil
	}
	return &kind
}

func (a *API) approvals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	visible, err := a.actionRequestVisibility(ctx, act)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if q.Get("countOnly") == "true" {
		rows, err := a.DB.Query(ctx, `
			select a.kind, a.scope, coalesce(r.workflow_id::text, '')
			from action_request a left join workflow_run r on r.id = a.workflow_run_id
			where a.org_id = $1 and a.status = 'pending_approval' and ($2 or a.kind <> 'source_config_admin')`,
			act.OrgID, act.Admin)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		count := 0
		for rows.Next() {
			var kind, scope, workflowID string
			if err := rows.Scan(&kind, &scope, &workflowID); err != nil {
				rows.Close()
				a.fail(w, r, err)
				return
			}
			if visible(kind, scope, workflowID) {
				count++
			}
		}
		if err := rows.Err(); err != nil {
			a.fail(w, r, err)
			return
		}
		sendJSON(w, http.StatusOK, map[string]int{"count": count})
		return
	}
	filter := q.Get("filter")
	if _, ok := approvalFilters[filter]; !ok {
		filter = "awaiting"
	}
	limit := 100
	if filter == "awaiting" {
		limit = 200
	}
	rows, err := a.DB.Query(ctx, `
		select a.id::text, a.workflow_run_id::text, a.kind, a.target, a.payload, a.risk_level, a.summary, a.scope,
		  a.status, a.minutes_saved, a.approved_at, a.approved_by_user_id, a.policy_id::text, a.rejection_reason,
		  a.failure_reason, a.created_at, r.started_at, r.created_at, w.id::text, w.name, o.title, p.name,
		  (select coalesce(e.result->>'status', e.status) from action_execution e
		    where e.action_request_id = a.id order by e.created_at desc limit 1),
		  (select e.error from action_execution e
		    where e.action_request_id = a.id order by e.created_at desc limit 1)
		from action_request a
		left join workflow_run r on a.workflow_run_id = r.id
		left join workflow_definition w on r.workflow_id = w.id
		left join observation o on a.triggered_by_observation_id = o.id
		left join action_policy p on a.policy_id = p.id
		where a.org_id = $1 and `+approvalFilters[filter]+` and ($2 or a.kind <> 'source_config_admin')
		order by a.created_at desc
		limit $3`, act.OrgID, act.Admin, limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var all []approvalOut
	for rows.Next() {
		var o approvalOut
		var approvedAt, runStarted, runCreated *time.Time
		var approvedBy, policyID, workflowID, workflowName, obsTitle, policyName, resultStatus *string
		if err := rows.Scan(&o.ID, &o.WorkflowRunID, &o.Kind, &o.Target, (*[]byte)(&o.Payload), &o.RiskLevel, &o.Summary, &o.Scope,
			&o.Status, &o.MinutesSaved, &approvedAt, &approvedBy, &policyID, &o.RejectionReason,
			&o.FailureReason, &o.created, &runStarted, &runCreated, &workflowID, &workflowName, &obsTitle, &policyName,
			&resultStatus, &o.ExecutionError); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		if workflowID != nil {
			o.Workflow = &approvalWorkflow{ID: *workflowID, Name: deref(workflowName)}
		}
		if deref(obsTitle) != "" {
			o.TriggeredByObservation = &observationTitle{*obsTitle}
		}
		o.ApprovedAt = jsTimePtr(approvedAt)
		o.ApproverKind = approverKind(approvedBy, policyID, approvedAt)
		o.ApproverLabel = approvedBy
		if o.ApproverLabel == nil {
			o.ApproverLabel = policyName
		}
		o.Outcome = actionOutcome(o.Status, resultStatus)
		runAt := o.created
		if runCreated != nil {
			runAt = *runCreated
		}
		if runStarted != nil {
			runAt = *runStarted
		}
		o.RunAt, o.CreatedAt = jsTime(runAt), jsTime(o.created)
		all = append(all, o)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	if filter == "awaiting" {
		slices.SortStableFunc(all, func(x, y approvalOut) int {
			if rx, ry := riskRank(x.RiskLevel), riskRank(y.RiskLevel); rx != ry {
				return rx - ry
			}
			return y.created.Truncate(time.Millisecond).Compare(x.created.Truncate(time.Millisecond))
		})
	}
	actions := []approvalOut{}
	for _, o := range all {
		if visible(o.Kind, o.Scope, workflowIDOf(o.Workflow)) {
			actions = append(actions, o)
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"actions": actions, "count": len(actions), "filter": filter})
}

func workflowIDOf(w *approvalWorkflow) string {
	if w == nil {
		return ""
	}
	return w.ID
}

var actionStatuses = []string{"draft", "pending_approval", "approved", "rejected", "expired", "executed", "failed", "cancelled"}

type actionRequestOut struct {
	ID                       string     `json:"id"`
	WorkflowRunID            *string    `json:"workflowRunId"`
	TriggeredByObservationID *string    `json:"triggeredByObservationId"`
	PolicyID                 *string    `json:"policyId"`
	Scope                    string     `json:"scope"`
	Kind                     string     `json:"kind"`
	Target                   *string    `json:"target"`
	Payload                  storedJSON `json:"payload"`
	RiskLevel                *string    `json:"riskLevel"`
	Status                   string     `json:"status"`
	Summary                  *string    `json:"summary"`
	ApprovedByUserID         *string    `json:"approvedByUserId"`
	ApprovedAt               *string    `json:"approvedAt"`
	RejectionReason          *string    `json:"rejectionReason"`
	FailureReason            *string    `json:"failureReason"`
	CreatedAt                string     `json:"createdAt"`
	UpdatedAt                string     `json:"updatedAt"`

	approvedAt *time.Time
}

const actionRequestColumns = `id::text, workflow_run_id::text, triggered_by_observation_id::text, policy_id::text,
	scope, kind, target, coalesce(payload, '{}'::jsonb), risk_level, status, summary, approved_by_user_id,
	approved_at, rejection_reason, failure_reason, created_at, updated_at`

func scanActionRequest(row pgx.Row) (actionRequestOut, error) {
	var o actionRequestOut
	var created, updated time.Time
	err := row.Scan(&o.ID, &o.WorkflowRunID, &o.TriggeredByObservationID, &o.PolicyID, &o.Scope, &o.Kind, &o.Target,
		(*[]byte)(&o.Payload), &o.RiskLevel, &o.Status, &o.Summary, &o.ApprovedByUserID, &o.approvedAt, &o.RejectionReason,
		&o.FailureReason, &created, &updated)
	o.ApprovedAt = jsTimePtr(o.approvedAt)
	o.CreatedAt, o.UpdatedAt = jsTime(created), jsTime(updated)
	return o, err
}

func runIDsOf(requests []actionRequestOut) []string {
	var ids []string
	for _, r := range requests {
		if r.WorkflowRunID != nil && !slices.Contains(ids, *r.WorkflowRunID) {
			ids = append(ids, *r.WorkflowRunID)
		}
	}
	return ids
}

func (a *API) visibleActionRequest(ctx context.Context, act actor, o actionRequestOut) (bool, error) {
	if o.Kind == "source_config_admin" && !act.Admin {
		return false, nil
	}
	visible, err := a.actionRecordVisibility(ctx, act, runIDsOf([]actionRequestOut{o}))
	if err != nil {
		return false, err
	}
	return visible(o.Kind, o.Scope, deref(o.WorkflowRunID)), nil
}

func (a *API) actionRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if !slices.Contains(actionStatuses, status) {
		status = ""
	}
	runID := q.Get("workflowRunId")
	found := []actionRequestOut{}
	if runID == "" || isUUID(runID) {
		rows, err := a.DB.Query(ctx, `select `+actionRequestColumns+` from action_request
			where org_id = $1 and ($2 = '' or status = $2) and ($3 = '' or workflow_run_id = nullif($3, '')::uuid)
			order by created_at desc limit $4`, act.OrgID, status, runID, queryLimit(r, 100))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		for rows.Next() {
			o, err := scanActionRequest(rows)
			if err != nil {
				rows.Close()
				a.fail(w, r, err)
				return
			}
			found = append(found, o)
		}
		if err := rows.Err(); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	visible, err := a.actionRecordVisibility(ctx, act, runIDsOf(found))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	shown := []actionRequestOut{}
	for _, o := range found {
		if (act.Admin || o.Kind != "source_config_admin") && visible(o.Kind, o.Scope, deref(o.WorkflowRunID)) {
			shown = append(shown, o)
		}
	}
	sendJSON(w, http.StatusOK, map[string]any{"actionRequests": shown})
}

type actionPolicyRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode"`
}

type upstreamOutputRef struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	WorkflowRunID *string `json:"workflowRunId"`
}

type actionRequestDetail struct {
	ActionRequest  actionRequestOut     `json:"actionRequest"`
	Executions     []actionExecutionOut `json:"executions"`
	Workflow       *approvalWorkflow    `json:"workflow"`
	Policy         *actionPolicyRef     `json:"policy"`
	UpstreamOutput *upstreamOutputRef   `json:"upstreamOutput"`
	ApproverKind   *string              `json:"approverKind"`
	CanRetry       bool                 `json:"canRetry"`
}

type actionExecutionOut struct {
	ID                 string     `json:"id"`
	Executor           string     `json:"executor"`
	CommandOrOperation *string    `json:"commandOrOperation"`
	Payload            storedJSON `json:"payload"`
	Result             storedJSON `json:"result"`
	ExternalRef        *string    `json:"externalRef"`
	Status             string     `json:"status"`
	Error              *string    `json:"error"`
	StartedAt          *string    `json:"startedAt"`
	FinishedAt         *string    `json:"finishedAt"`
	CreatedAt          string     `json:"createdAt"`
}

func (a *API) actionRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id := r.PathValue("actionRequestId")
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	req, err := scanActionRequest(a.DB.QueryRow(ctx, `select `+actionRequestColumns+` from action_request where org_id = $1 and id = $2`, act.OrgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if ok, err := a.visibleActionRequest(ctx, act, req); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			sendError(w, http.StatusNotFound, "not found")
		}
		return
	}
	rows, err := a.DB.Query(ctx, `
		select id::text, executor, command_or_operation, payload, result, external_ref, status, error,
		  started_at, finished_at, created_at
		from action_execution where action_request_id = $1 order by created_at desc`, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	executions := []actionExecutionOut{}
	for rows.Next() {
		var e actionExecutionOut
		var payload, result []byte
		var started, finished *time.Time
		var created time.Time
		if err := rows.Scan(&e.ID, &e.Executor, &e.CommandOrOperation, &payload, &result, &e.ExternalRef, &e.Status,
			&e.Error, &started, &finished, &created); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		e.Payload, e.Result = payload, result
		e.StartedAt, e.FinishedAt, e.CreatedAt = jsTimePtr(started), jsTimePtr(finished), jsTime(created)
		executions = append(executions, e)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var canRetry bool
	var workflow *approvalWorkflow
	var policy *actionPolicyRef
	var upstream *upstreamOutputRef
	err = a.DB.QueryRow(ctx, `
		select exists (
		  select 1 from action_request a
		  join action_changeset c on c.action_request_id = a.id
		  where a.org_id = $1 and a.id = $2 and a.status = 'failed'
		    and c.org_id = a.org_id and c.status = 'failed' and c.bulk_uuid is null
		    and (select count(*) from action_changeset where action_request_id = a.id) = 1
		    and exists (select 1 from action_changeset_row where changeset_id = c.id)
		    and not exists (
		      select 1 from action_changeset_row x where x.changeset_id = c.id
		        and (x.status <> 'failed' or x.started_at is not null or x.external_ref is not null)))`,
		act.OrgID, id).Scan(&canRetry)
	if err == nil && req.WorkflowRunID != nil {
		workflow = &approvalWorkflow{}
		err = a.DB.QueryRow(ctx, `select w.id::text, w.name from workflow_run r join workflow_definition w on r.workflow_id = w.id
			where r.org_id = $1 and r.id = $2`, act.OrgID, *req.WorkflowRunID).Scan(&workflow.ID, &workflow.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			workflow, err = nil, nil
		}
	}
	if err == nil && req.PolicyID != nil {
		policy = &actionPolicyRef{}
		err = a.DB.QueryRow(ctx, `select id::text, name, mode from action_policy where org_id = $1 and id = $2`,
			act.OrgID, *req.PolicyID).Scan(&policy.ID, &policy.Name, &policy.Mode)
		if errors.Is(err, pgx.ErrNoRows) {
			policy, err = nil, nil
		}
	}
	if err == nil && req.TriggeredByObservationID != nil {
		upstream = &upstreamOutputRef{}
		err = a.DB.QueryRow(ctx, `select id::text, title, workflow_run_id::text from workflow_output where org_id = $1 and id = $2`,
			act.OrgID, *req.TriggeredByObservationID).Scan(&upstream.ID, &upstream.Title, &upstream.WorkflowRunID)
		if errors.Is(err, pgx.ErrNoRows) {
			upstream, err = nil, nil
		}
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, actionRequestDetail{
		ActionRequest:  req,
		Executions:     executions,
		Workflow:       workflow,
		Policy:         policy,
		UpstreamOutput: upstream,
		ApproverKind:   approverKind(req.ApprovedByUserID, req.PolicyID, req.approvedAt),
		CanRetry:       canRetry,
	})
}

// storedJSON is a jsonb value as Node sends it; nil is null.
type storedJSON []byte

func (s storedJSON) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	return jsJSON(s)
}

// jsJSON re-encodes stored JSON as JSON.stringify(JSON.parse(raw)) does:
// numbers as JavaScript prints them, and integer-like keys first, ascending.
func jsJSON(raw []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var buf bytes.Buffer
	if err := jsValue(dec, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func jsValue(dec *json.Decoder, buf *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '[' {
			buf.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					buf.WriteByte(',')
				}
				if err := jsValue(dec, buf); err != nil {
					return err
				}
			}
			buf.WriteByte(']')
			_, err = dec.Token()
			return err
		}
		type member struct {
			key   string
			value bytes.Buffer
		}
		var members []*member
		index := map[string]*member{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			m := &member{key: k.(string)}
			if err := jsValue(dec, &m.value); err != nil {
				return err
			}
			if old, ok := index[m.key]; ok {
				old.value = m.value
				continue
			}
			index[m.key] = m
			members = append(members, m)
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		slices.SortStableFunc(members, func(x, y *member) int {
			xi, xok := arrayIndex(x.key)
			yi, yok := arrayIndex(y.key)
			switch {
			case xok && yok:
				return cmp.Compare(xi, yi)
			case xok:
				return -1
			case yok:
				return 1
			}
			return 0
		})
		buf.WriteByte('{')
		for i, m := range members {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := jsString(buf, m.key); err != nil {
				return err
			}
			buf.WriteByte(':')
			buf.Write(m.value.Bytes())
		}
		buf.WriteByte('}')
		return nil
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil || math.IsInf(f, 0) {
			buf.WriteString("null")
			return nil
		}
		if f == 0 {
			f = 0
		}
		out, _ := json.Marshal(f)
		buf.Write(out)
		return nil
	case string:
		return jsString(buf, v)
	default:
		out, err := json.Marshal(v)
		buf.Write(out)
		return err
	}
}

func jsString(buf *bytes.Buffer, s string) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1)
	return nil
}

func arrayIndex(key string) (uint64, bool) {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < math.MaxUint32
}
