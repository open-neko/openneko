package webfront

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const agentBackendID = "ax"

const interruptedRunReason = "Interrupted — the process running the agent is no longer available. Retry to run it again."

var (
	runStreamPoll      = 5 * time.Second
	runStreamLifetime  = 10 * time.Minute
	runStreamKeepalive = 30 * time.Second
	runStreamDoneGrace = 5 * time.Second
)

// hostProvision is ensureHostConfigProvisioned() in
// packages/llm/src/host-provision.ts: provisioning runs again only when the
// org's provider and data source rows change.
type hostProvision struct {
	mu      sync.Mutex
	applied map[string]string
}

func (a *API) hostConfigRevision(ctx context.Context, orgID string) (string, error) {
	var rev string
	err := a.DB.QueryRow(ctx, `select md5(
		coalesce((select jsonb_agg(jsonb_build_array(id, scope, provider, model, enabled, config, secrets, updated_at) order by scope, id)
		            from llm_provider_config where org_id = $1 and scope in ('primary', 'agent')), '[]'::jsonb)::text ||
		coalesce((select jsonb_agg(jsonb_build_array(id, graphql_url, mcp_url, auth_mode, enabled, is_default, updated_at)
		            order by is_default desc, created_at, id)
		            from data_source where org_id = $1), '[]'::jsonb)::text)`, orgID).Scan(&rev)
	return rev, err
}

func (a *API) ensureHostConfigProvisioned(ctx context.Context, orgID string) error {
	a.hostProv.mu.Lock()
	defer a.hostProv.mu.Unlock()
	rev, err := a.hostConfigRevision(ctx, orgID)
	if err != nil {
		return err
	}
	if a.hostProv.applied[orgID] == rev {
		return nil
	}
	a.provisionHostConfig(ctx, orgID)
	if a.hostProv.applied == nil {
		a.hostProv.applied = map[string]string{}
	}
	a.hostProv.applied[orgID] = rev
	return nil
}

// createWorkRun is createWorkRun() in packages/llm/src/work/store.ts: the run
// row and its spend reservation commit together.
func (a *API) createWorkRun(ctx context.Context, orgID, threadID, actorUserID, actorRole, source, workflowID string) (string, error) {
	runID, _, err := a.createWorkRunOnce(ctx, orgID, threadID, actorUserID, actorRole, source, workflowID, "", "")
	return runID, err
}

// createWorkRunOnce returns the run that an earlier request with the same
// clientRequestID made in the thread, with existing set, and makes no new one.
// A non-empty message is saved in the same transaction as the run.
func (a *API) createWorkRunOnce(ctx context.Context, orgID, threadID, actorUserID, actorRole, source, workflowID, clientRequestID, message string) (runID string, existing bool, err error) {
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		runID, existing, err = insertWorkRun(ctx, tx, orgID, threadID, actorUserID, actorRole, source, workflowID, clientRequestID, message)
		return err
	})
	return a.workRunCreated(ctx, orgID, runID, existing, actorUserID, actorRole, err)
}

const runInFlightRetrySeconds = 2

type runInFlight struct{ runID string }

func (e *runInFlight) Error() string { return "a question is still running in this thread" }

// createChatRunOnce is createWorkRunOnce for a question from the web: it
// refuses a new run while another run of the thread is queued or running.
func (a *API) createChatRunOnce(ctx context.Context, orgID, threadID, actorUserID, actorRole, clientRequestID, message string) (runID string, existing bool, err error) {
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select 1 from work_thread where id = $1 for no key update`, threadID); err != nil {
			return err
		}
		if clientRequestID != "" {
			err := tx.QueryRow(ctx, `select id::text from work_run where thread_id = $1 and client_request_id = $2`,
				threadID, clientRequestID).Scan(&runID)
			if err == nil {
				existing = true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var live string
		err := tx.QueryRow(ctx, `select id::text from work_run where thread_id = $1 and status in ('queued', 'running')
			order by created_at desc limit 1`, threadID).Scan(&live)
		if err == nil {
			return &runInFlight{runID: live}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		runID, existing, err = insertWorkRun(ctx, tx, orgID, threadID, actorUserID, actorRole, "chat", "", clientRequestID, message)
		return err
	})
	return a.workRunCreated(ctx, orgID, runID, existing, actorUserID, actorRole, err)
}

// expectedUserMismatch answers 409 when the body names a user other than the
// signed-in one, so a tab of an ended session cannot post as the next user.
func expectedUserMismatch(w http.ResponseWriter, body any, act actor) bool {
	expected, ok := fieldString(body, "expectedUserId")
	if !ok || expected == act.UserID {
		return false
	}
	sendJSON(w, http.StatusConflict, obj("error", "You are signed in as another user. Reload the page.", "code", "user_mismatch"))
	return true
}

func insertWorkRun(ctx context.Context, tx pgx.Tx, orgID, threadID, actorUserID, actorRole, source, workflowID, clientRequestID, message string) (runID string, existing bool, err error) {
	err = tx.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, status, actor_user_id, actor_role, client_request_id)
		values ($1, $2, $3, 'queued', $4, $5, $6)
		on conflict (thread_id, client_request_id) where client_request_id is not null do nothing
		returning id::text`,
		orgID, threadID, agentBackendID, nullable(actorUserID), nullable(actorRole), nullable(clientRequestID)).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `select id::text from work_run where thread_id = $1 and client_request_id = $2`,
			threadID, clientRequestID).Scan(&runID)
		return runID, true, err
	}
	if err != nil {
		return "", false, err
	}
	if err := admitRunSpend(ctx, tx, orgID, workflowID, runID, source, time.Now()); err != nil {
		return "", false, err
	}
	if message == "" {
		return runID, false, nil
	}
	return runID, false, saveUserMessage(ctx, tx, orgID, threadID, runID, message, true)
}

// workRunCreated records what follows the commit of insertWorkRun: the
// budget alert of a refused run, or the audit event of a new one.
func (a *API) workRunCreated(ctx context.Context, orgID, runID string, existing bool, actorUserID, actorRole string, err error) (string, bool, error) {
	if err != nil {
		var over *spendExceeded
		if errors.As(err, &over) {
			a.recordBudgetBlocked(context.WithoutCancel(ctx), over)
		}
		return "", false, err
	}
	if existing {
		return runID, true, nil
	}
	a.recordAudit(ctx, auditEvent{OrgID: orgID, EntityKind: "work_run", EntityID: runID, Event: "run:created",
		Payload: map[string]any{"backend": agentBackendID, "actorUserId": nullable(actorUserID), "actorRole": nullable(actorRole)}})
	return runID, false, nil
}

// suggestThreadTitle is suggestWorkThreadTitle().
func suggestThreadTitle(message string) string {
	text := strings.Join(strings.FieldsFunc(jsTrim(message), jsSpace), " ")
	if text == "" {
		return "New thread"
	}
	if utf16Len(text) > 80 {
		return utf16Head(text, 77) + "..."
	}
	return text
}

// startRun is POST /api/v1/work/threads/{threadId}/runs: it saves the
// question, creates the run and starts the turn in this process.
func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, _ := readBody(r)
	raw, _ := fieldString(body, "message")
	message := jsTrim(raw)
	if message == "" {
		sendError(w, http.StatusBadRequest, "message is required")
		return
	}
	clientRequestID, _ := fieldString(body, "clientRequestId")
	if !validClientRequestID(clientRequestID) {
		sendError(w, http.StatusBadRequest, "clientRequestId must be 1 to 128 letters, digits, '-', '_' or '.'")
		return
	}
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if expectedUserMismatch(w, body, act) {
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
	if err := a.ensureHostConfigProvisioned(ctx, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	}
	packActions, err := a.packActionDescriptors(ctx, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	role := "member"
	if act.Admin {
		role = "admin"
	}
	if clientRequestID != "" {
		defer a.runStarts.lock(threadID + "\x00" + clientRequestID)()
	}
	runID, existing, err := a.createChatRunOnce(ctx, act.OrgID, threadID, act.UserID, role, clientRequestID, message)
	if err != nil {
		var live *runInFlight
		if errors.As(err, &live) {
			w.Header().Set("Retry-After", strconv.Itoa(runInFlightRetrySeconds))
			sendJSON(w, http.StatusConflict, obj("error", "A question is still running in this thread. Wait for its answer.",
				"code", "run_in_flight", "runId", live.runID, "retryAfterSeconds", float64(runInFlightRetrySeconds)))
			return
		}
		var over *spendExceeded
		if errors.As(err, &over) {
			w.Header().Set("Retry-After", strconv.FormatInt(over.retryAfter, 10))
			sendJSON(w, http.StatusTooManyRequests, obj("error", over.message(), "code", "spend_budget_exhausted", "budget", over.budget,
				"limitUsd", microsToUSD(over.limitMicros), "committedUsd", microsToUSD(over.committedMicros),
				"reservationUsd", microsToUSD(over.reserve), "retryAfterSeconds", float64(over.retryAfter)))
			return
		}
		a.fail(w, r, err)
		return
	}
	if existing {
		stored, resume, err := a.unstartedRun(ctx, runID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if !resume {
			sendJSON(w, http.StatusOK, obj("runId", runID, "threadId", threadID, "backend", agentBackendID, "actorRole", role))
			return
		}
		if stored != "" {
			message = stored
		} else if err := saveUserMessage(ctx, a.DB, act.OrgID, threadID, runID, message, true); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	o := chatTurnOptions{OrgID: act.OrgID, ThreadID: threadID, RunID: runID, Message: message, Channel: "web",
		IncludeUxMetadata: true, PackActions: packActions, resolve: a.turnResolve, runCore: a.turnCore}
	if _, err := a.startChatRun(ctx, o); err != nil && !errors.Is(err, errRunStarted) {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, obj("runId", runID, "threadId", threadID, "backend", agentBackendID, "actorRole", role))
}

func validClientRequestID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func (a *API) saveUserMessage(ctx context.Context, orgID, threadID, runID, content string) error {
	return saveUserMessage(ctx, a.DB, orgID, threadID, runID, content, false)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// saveUserMessage with nameThread also gives an untitled thread its title.
func saveUserMessage(ctx context.Context, db execer, orgID, threadID, runID, content string, nameThread bool) error {
	if _, err := db.Exec(ctx, `insert into work_message (org_id, thread_id, run_id, role, content) values ($1, $2, $3, 'user', $4)`,
		orgID, threadID, runID, content); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `update work_thread set title = case when $2 and title = '' then $3 else title end,
		updated_at = now(), last_message_at = now() where id = $1`, threadID, nameThread, suggestThreadTitle(content))
	return err
}

// unstartedRun reports a queued run that no turn in this process holds: the
// process that made it stopped before the turn started. It returns the saved
// question of the run, if any.
func (a *API) unstartedRun(ctx context.Context, runID string) (message string, resume bool, err error) {
	var status string
	if err := a.DB.QueryRow(ctx, `select status, coalesce((select content from work_message
		where run_id = r.id and role = 'user' order by created_at, id limit 1), '')
		from work_run r where id = $1`, runID).Scan(&status, &message); err != nil {
		return "", false, err
	}
	return message, status == "queued" && a.runs.get(runID) == nil, nil
}

// keyedMutex serialises the requests that share a key.
type keyedMutex struct {
	mu   sync.Mutex
	held map[string]*keyedEntry
}

type keyedEntry struct {
	mu    sync.Mutex
	users int
}

func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.held == nil {
		k.held = map[string]*keyedEntry{}
	}
	e := k.held[key]
	if e == nil {
		e = &keyedEntry{}
		k.held[key] = e
	}
	e.users++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		if e.users--; e.users == 0 {
			delete(k.held, key)
		}
		k.mu.Unlock()
	}
}

// stopTarget is a run that act may stop: a run of the actor's own thread, or
// for an org admin a service run, whose thread has no creator.
type stopTarget struct {
	status, threadID string
	runningBatch     bool
}

func (a *API) runStopTarget(ctx context.Context, act actor, runID string) (*stopTarget, error) {
	if act.UserID == "" || !isUUID(runID) {
		return nil, nil
	}
	var t stopTarget
	var owned, service bool
	err := a.DB.QueryRow(ctx, `select r.status, r.thread_id::text, coalesce(t.created_by_user_id = $3, false),
		t.created_by_user_id is null, exists (select 1 from workflow_run w where w.work_run_id = r.id
		and w.execution_mode = 'batch' and w.status = 'running')
		from work_run r join work_thread t on t.id = r.thread_id where r.org_id = $1 and r.id = $2`,
		act.OrgID, runID, act.UserID).Scan(&t.status, &t.threadID, &owned, &service, &t.runningBatch)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !owned && !(service && act.Admin) {
		return nil, nil
	}
	return &t, err
}

func (t *stopTarget) stoppable() bool {
	return (t.status == "queued" || t.status == "running") && !t.runningBatch
}

// cancelRun is POST /api/v1/work/runs/{runId}/cancel. A run that no process
// holds is still stopped in the database, so Stop never leaves it running.
func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	runID := r.PathValue("runId")
	t, err := a.runStopTarget(ctx, act, runID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if t == nil {
		sendError(w, http.StatusNotFound, "Run not found")
		return
	}
	if a.stopRun(runID, act.UserID) {
		sendJSON(w, http.StatusOK, obj("ok", true, "recovered", false))
		return
	}
	if t.runningBatch {
		sendError(w, http.StatusConflict, "A running batch API run cannot be stopped. It ends at its runtime limit.")
		return
	}
	reason := interruptedRunReason
	var orgID string
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update work_run set status = 'cancelled', error = $2, updated_at = now(), finished_at = now()
			where id = $1 and status in ('queued', 'running') returning org_id`, runID, reason).Scan(&orgID)
		if err != nil {
			return err
		}
		var wfRunID string
		err = tx.QueryRow(ctx, `update workflow_run set status = 'cancelled', error = $2, updated_at = now(), finished_at = now()
			where work_run_id = $1 and status in ('queued', 'running') returning id::text`, runID, reason).Scan(&wfRunID)
		if err := noRows(err); err != nil || wfRunID == "" {
			return err
		}
		_, err = tx.Exec(ctx, `update workflow_api_admission set status = 'cancelled', completed_at = now(), lease_until = null,
			updated_at = now() where workflow_run_id = $1 and status not in ('running', 'completed', 'failed', 'cancelled', 'expired')`, wfRunID)
		return err
	})
	recovered := err == nil
	if err := noRows(err); err != nil {
		a.fail(w, r, err)
		return
	}
	if recovered {
		a.recordAudit(ctx, auditEvent{OrgID: orgID, EntityKind: "work_run", EntityID: runID, Event: "run:cancelled",
			Payload: map[string]any{"status": "cancelled", "error": reason, "stoppedBy": act.UserID}})
		if _, err := a.appendRunEvent(ctx, orgID, t.threadID, runID, doneEvent("cancelled")); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	terminal := t.status == "completed" || t.status == "failed" || t.status == "cancelled" || t.status == "needs_input"
	sendJSON(w, http.StatusOK, obj("ok", recovered || terminal, "recovered", recovered))
}

type preparedWorkflowRun struct {
	workflow                       workflowRecord
	workflowRunID, threadID, runID string
}

// workflowRunOrigin is the part of PrepareWorkflowRunOptions that a trigger
// sets: a thread to run in and the subscription chain.
type workflowRunOrigin struct {
	threadID                                string
	parentChainDepth                        int
	subscriptionID, outputID, observationID string
}

// prepareWorkflowRun is prepareWorkflowRun(): the thread, the work run with
// its spend reservation, and the workflow run.
func (a *API) prepareWorkflowRun(ctx context.Context, orgID, workflowID, triggerKind string, payload any, from workflowRunOrigin) (preparedWorkflowRun, error) {
	var p preparedWorkflowRun
	notFound := errors.New("Workflow " + workflowID + " not found for org " + orgID + ".")
	if !isUUID(workflowID) {
		return p, notFound
	}
	wf, err := scanWorkflowRecord(a.DB.QueryRow(ctx, `select `+workflowRecordColumns+` from workflow_definition
		where org_id = $1 and id = $2`, orgID, workflowID))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, notFound
	}
	if err != nil {
		return p, err
	}
	if !wf.Enabled {
		return p, errors.New("Workflow " + wf.Name + " is disabled.")
	}
	userID, role := "", "service"
	if wf.OwnerUserID == "" {
		var needs bool
		if err := a.DB.QueryRow(ctx, `select `+workflowNeedsOwner+` from workflow_definition w where w.id = $1`, wf.ID).Scan(&needs); err != nil {
			return p, err
		}
		if needs {
			return p, errors.New("Workflow " + wf.Name + " has no owner. An admin assigns one at Admin, Settings, Workflow API before it runs.")
		}
	}
	if wf.OwnerUserID != "" {
		var active bool
		if err := a.DB.QueryRow(ctx, `select exists (select 1 from app_user where org_id = $1 and id = $2 and disabled_at is null)`,
			orgID, wf.OwnerUserID).Scan(&active); err != nil {
			return p, err
		}
		if !active {
			return p, errors.New("The workflow owner is no longer active")
		}
		if role, err = a.role(ctx, orgID, wf.OwnerUserID); err != nil {
			return p, err
		}
		userID = wf.OwnerUserID
	}
	threadID := from.threadID
	if threadID == "" && wf.CreatedByThreadID != nil {
		err := a.DB.QueryRow(ctx, `select b.thread_id::text from bot b join work_thread t on t.id = b.thread_id
			where b.org_id = $1 and b.thread_id = $2 and ($3 = '' or t.created_by_user_id = $3) limit 1`,
			orgID, *wf.CreatedByThreadID, wf.OwnerUserID).Scan(&threadID)
		if err := noRows(err); err != nil {
			return p, err
		}
	}
	if threadID == "" {
		if err := a.DB.QueryRow(ctx, `insert into work_thread (org_id, title, channel, backend_state) values ($1, $2, 'workflow', '{}')
			returning id::text`, orgID, wf.Name).Scan(&threadID); err != nil {
			return p, err
		}
	}
	source := "trigger"
	switch triggerKind {
	case "cron", "api":
		source = triggerKind
	}
	runID, err := a.createWorkRun(ctx, orgID, threadID, userID, role, source, wf.ID)
	if err != nil {
		return p, err
	}
	chainDepth := from.parentChainDepth
	if triggerKind == "subscription" {
		chainDepth++
	}
	var workflowRunID string
	if err := a.DB.QueryRow(ctx, `insert into workflow_run (org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload,
		chain_depth, triggered_by_subscription_id, triggered_by_output_id, triggered_by_observation_id, status, started_at)
		values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, 'running', now()) returning id::text`,
		orgID, wf.ID, threadID, runID, triggerKind, jsStringify(payload, false), chainDepth,
		nullable(from.subscriptionID), nullable(from.outputID), nullable(from.observationID)).Scan(&workflowRunID); err != nil {
		return p, err
	}
	return preparedWorkflowRun{workflow: wf, workflowRunID: workflowRunID, threadID: threadID, runID: runID}, nil
}

// manualRun is POST /api/v1/workflows/{workflowId}/manual-runs.
func (a *API) manualRun(w http.ResponseWriter, r *http.Request) {
	act, workflowID, ok := a.visibleWorkflow(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	body, _ := readBody(r)
	var userMessage any
	message := ""
	if s, ok := fieldString(body, "userMessage"); ok {
		message = jsTrim(s)
		userMessage = message
	}
	if err := a.ensureHostConfigProvisioned(ctx, act.OrgID); err != nil {
		a.fail(w, r, err)
		return
	}
	payload := jsObjOf("userMessage", userMessage)
	p, err := a.prepareWorkflowRun(ctx, act.OrgID, workflowID, "manual", payload, workflowRunOrigin{})
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	o := workflowTurnOptions{Workflow: p.workflow, WorkflowRunID: p.workflowRunID, TriggerKind: "manual", ThreadID: p.threadID,
		RunID: p.runID, TriggerPayload: payload, UserMessage: message, Mode: "live", IncludeUxMetadata: true,
		resolve: a.turnResolve, runCore: a.turnCore}
	if _, err := a.startWorkflowRun(ctx, act.OrgID, o); err != nil {
		a.fail(w, r, err)
		return
	}
	sendJSON(w, http.StatusOK, obj("workflowRunId", p.workflowRunID, "workRunId", p.runID, "threadId", p.threadID))
}

// startWorkflowRun is the background part of the manual-runs route: it runs
// the workflow in a goroutine. The channel closes when the run is over.
func (a *API) startWorkflowRun(ctx context.Context, orgID string, o workflowTurnOptions) (<-chan struct{}, error) {
	base, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	run, err := a.beginWorkflowRun(base, cancel, orgID, o)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = run()
	}()
	return done, nil
}

// beginWorkflowRun registers the run, stores its events and guards its
// spend. The returned function runs the workflow turn and reports a thrown
// failure on the run. It calls cancel when it ends, and so does a failure
// here.
func (a *API) beginWorkflowRun(base context.Context, cancel context.CancelCauseFunc, orgID string, o workflowTurnOptions) (func() (workflowTurnResult, error), error) {
	store := a.newRunEventStore(orgID, o.ThreadID, o.RunID)
	store.kind = "workflow"
	tel := &runTelemetry{}
	runCtx, guarded, err := a.newRunSpendGuard(base, o.RunID, tel.wrap(store.emit))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	run := a.registerRun(o.RunID, orgID, o.ThreadID, cancel)
	untrace := a.EvalTrace.register(o.RunID)
	o.Emit = guarded
	if o.wrapEmit != nil {
		o.Emit = o.wrapEmit(guarded)
	}
	return func() (workflowTurnResult, error) {
		defer a.runs.unregister(o.RunID, run)
		defer untrace()
		defer cancel(nil)
		bg := context.WithoutCancel(runCtx)
		res, err := a.runWorkflowTurn(runCtx, o)
		a.storeRunTelemetry(bg, store, tel, o.WorkflowRunID)
		if err != nil {
			log.Printf("[workflow-run] run %s threw: %v", o.WorkflowRunID, err)
			a.failThrownWorkflowRun(bg, orgID, o, store, err)
		}
		if err := store.finalize(bg); err != nil {
			log.Printf("[workflow-run] finalize failed for %s: %v", o.WorkflowRunID, err)
		}
		if err == nil && errors.Is(context.Cause(runCtx), errTurnShutdown) {
			err = errTurnShutdown
		}
		return res, err
	}, nil
}

func (a *API) failThrownWorkflowRun(ctx context.Context, orgID string, o workflowTurnOptions, store *runEventStore, cause error) {
	var status string
	if err := noRows(a.DB.QueryRow(ctx, `select status from work_run where org_id = $1 and id::text = $2`, orgID, o.RunID).Scan(&status)); err != nil {
		log.Printf("[workflow-run] cleanup failed for %s: %v", o.WorkflowRunID, err)
		return
	}
	switch status {
	case "completed", "failed", "cancelled":
		return
	}
	msg := cause.Error()
	steps := []func() error{
		func() error { return a.finishWorkRun(ctx, o.RunID, "failed", &msg) },
		func() error { return a.finishWorkflowRun(ctx, o.WorkflowRunID, "failed", nil, &msg) },
		func() error { return store.emit(ctx, jsObjOf("type", "error", "message", msg)) },
		func() error { return store.emit(ctx, doneEvent("failed")) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			log.Printf("[workflow-run] cleanup failed for %s: %v", o.WorkflowRunID, err)
			return
		}
	}
}
