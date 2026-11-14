package webfront

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

const (
	hookBodyMax        = 256 * 1024
	hookMaxBatch       = 1_000
	hookWaitMaxMs      = 600_000
	hookSyncWaiters    = 5
	hookPendingNote    = "Poll resultRequest.url with the same webhook token."
	hookTokenHint      = "<same webhook token used to start the run>"
	hookMissingBatch   = "Batch run ended without publishing the required result.csv artifact."
	hookSeedKey        = "openneko_reckon_webhook"
	hookWorkflowAPIIn  = "trigger_input.json"
	hookPollInterval   = 250 * time.Millisecond
	hookDispatchHeader = "Idempotency-Replayed"
)

var hookKeyChars = func() [256]bool {
	var ok [256]bool
	for _, c := range []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:-") {
		ok[c] = true
	}
	return ok
}()

type hookError struct {
	status     int
	body       *jsObj
	retryAfter int64
}

func (e *hookError) Error() string { return fmt.Sprint(e.body.vals["error"]) }

func hookErr(status int, retryAfter int64, kv ...any) *hookError {
	body := &jsObj{}
	for i := 0; i+1 < len(kv); i += 2 {
		body.set(kv[i].(string), kv[i+1])
	}
	return &hookError{status: status, body: body, retryAfter: retryAfter}
}

func hookNotFound() *hookError { return hookErr(404, 0, "error", "not found") }

type compatWebhook struct {
	reckonID, orgID, workflowID, tokenSHA string
	params                                []string
	chunk                                 int
}

func hookJSON(w http.ResponseWriter, status int, body *jsObj, retryAfter int64, replay bool) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
	}
	if replay {
		w.Header().Set(hookDispatchHeader, "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, jsStringify(body, false))
}

func (a *API) hookFailure(w http.ResponseWriter, r *http.Request, err error) {
	var he *hookError
	if errors.As(err, &he) {
		hookJSON(w, he.status, he.body, he.retryAfter, false)
		return
	}
	a.fail(w, r, err)
}

func (a *API) compatWebhook(ctx context.Context, reckonID string) (*compatWebhook, error) {
	var h compatWebhook
	var enabled bool
	err := a.DB.QueryRow(ctx, `select reckon_workflow_id, org_id, workflow_id::text, token_sha256, enabled, params, batch_chunk_size
	   from compat_webhook where reckon_workflow_id = $1`, reckonID).
		Scan(&h.reckonID, &h.orgID, &h.workflowID, &h.tokenSHA, &enabled, &h.params, &h.chunk)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return nil, hookNotFound()
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (a *API) authorizedWebhook(r *http.Request) (*compatWebhook, error) {
	h, err := a.compatWebhook(r.Context(), r.PathValue("workflowId"))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(headerValue(r, "X-Webhook-Token")))
	want, err := hex.DecodeString(h.tokenSHA)
	if err != nil || subtle.ConstantTimeCompare(digest[:], want) != 1 {
		return nil, hookErr(401, 0, "error", "unauthorized")
	}
	return h, nil
}

type hookLimits struct {
	queueMax, perWorkflow, reserved, rate, globalRate, resultRate, concurrency int64
}

func hookEnv(name string, fallback, ceiling int64) int64 {
	n, ok := jsNumber(os.Getenv(name))
	if os.Getenv(name) == "" || !ok || n != math.Trunc(n) || math.IsInf(n, 0) || n <= 0 {
		return fallback
	}
	return int64(math.Min(n, float64(ceiling)))
}

func readHookLimits() hookLimits {
	queueMax := hookEnv("HEADLESS_QUEUE_MAX", 250, maxSafeInt)
	return hookLimits{
		queueMax:    queueMax,
		perWorkflow: min(hookEnv("HEADLESS_QUEUE_MAX_PER_WORKFLOW", 100, maxSafeInt), queueMax),
		reserved:    min(hookEnv("HEADLESS_INTERNAL_RESERVED_SLOTS", 10, maxSafeInt), max(0, queueMax-1)),
		rate:        hookEnv("HEADLESS_WEBHOOK_RATE_PER_MINUTE", 10, maxSafeInt),
		globalRate:  hookEnv("HEADLESS_GLOBAL_WEBHOOK_RATE_PER_MINUTE", 50, maxSafeInt),
		resultRate:  hookEnv("HEADLESS_RESULT_RATE_PER_MINUTE", 10, maxSafeInt),
		concurrency: hookEnv("HEADLESS_MAX_CONCURRENCY", 5, 10),
	}
}

func minuteWindow(now time.Time) (time.Time, int64) {
	start := now.Truncate(time.Minute)
	return start, max(1, int64(math.Ceil(float64(start.Add(time.Minute).Sub(now).Milliseconds())/1000)))
}

func (a *API) hookBucket(ctx context.Context, scope, op string, limit int64, start time.Time) (bool, error) {
	var count int64
	err := a.DB.QueryRow(ctx, `insert into workflow_api_rate_bucket (scope_kind, scope_id, operation, window_start, count, expires_at)
	   values ('workflow', $1, $2, $3::timestamptz, 1, $3::timestamptz + interval '2 minutes')
	   on conflict (scope_kind, scope_id, operation, window_start)
	   do update set count = workflow_api_rate_bucket.count + 1
	   returning count`, scope, op, start).Scan(&count)
	return count <= limit, err
}

// consumeStartRate runs before the replay check, so one idempotency key
// cannot dodge the limit.
func (a *API) consumeStartRate(ctx context.Context, h *compatWebhook, now time.Time) error {
	limits := readHookLimits()
	start, retry := minuteWindow(now)
	ok, err := a.hookBucket(ctx, "reckon:global", "invoke", limits.globalRate, start)
	if err != nil {
		return err
	}
	if !ok {
		return hookErr(429, retry, "error", "global_rate")
	}
	ok, err = a.hookBucket(ctx, "reckon:"+h.reckonID, "invoke", limits.rate, start)
	if err != nil {
		return err
	}
	if !ok {
		return hookErr(429, retry, "error", "workflow_rate")
	}
	return nil
}

// decodeText is Request.text(): a leading BOM goes, and each maximal
// invalid UTF-8 subpart becomes one U+FFFD.
func decodeText(raw []byte) string {
	return utf8Text([]byte(strings.TrimPrefix(string(raw), "\xef\xbb\xbf")))
}

func invalidSubpart(p []byte) int {
	lead := p[0]
	need, lo, hi := 0, byte(0x80), byte(0xBF)
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		need = 1
	case lead == 0xE0:
		need, lo = 2, 0xA0
	case lead == 0xED:
		need, hi = 2, 0x9F
	case lead >= 0xE1 && lead <= 0xEF:
		need = 2
	case lead == 0xF0:
		need, lo = 3, 0x90
	case lead == 0xF4:
		need, hi = 3, 0x8F
	case lead >= 0xF1 && lead <= 0xF3:
		need = 3
	}
	n := 1
	for ; n <= need && n < len(p); n++ {
		if p[n] < lo || p[n] > hi {
			break
		}
		lo, hi = 0x80, 0xBF
	}
	return n
}

func (a *API) readHookBody(r *http.Request) (string, error) {
	tooLarge := hookErr(413, 0, "error", "payload_too_large", "maxBytes", float64(hookBodyMax))
	declared, ok := jsNumber(headerValue(r, "Content-Length"))
	if r.ContentLength > hookBodyMax || (ok && !math.IsInf(declared, 0) && declared > hookBodyMax) {
		return "", tooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, hookBodyMax+4))
	if err != nil {
		return "", err
	}
	if len(raw) > hookBodyMax+3 {
		return "", tooLarge
	}
	text := decodeText(raw)
	if len(text) > hookBodyMax {
		return "", tooLarge
	}
	return text, nil
}

func hookKey(raw string) (string, bool) {
	key := strings.TrimFunc(raw, isJSSpace)
	if key == "" || len(key) > 200 {
		return "", false
	}
	for i := 0; i < len(key); i++ {
		if !hookKeyChars[key[i]] {
			return "", false
		}
	}
	return key, true
}

func hookMode(r *http.Request) (string, bool) {
	q := r.URL.Query()
	if _, set := q["mode"]; !set {
		return "single", true
	}
	switch q.Get("mode") {
	case "", "single":
		return "single", true
	case "batch":
		return "batch", true
	}
	return "", false
}

const maxSafeInt = 1<<53 - 1

// headerValue is Headers.get(): repeated values joined by ", ".
func headerValue(r *http.Request, name string) string {
	return strings.Join(r.Header.Values(name), ", ")
}

const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func ulid(ms int64) string {
	var b [26]byte
	for i := 9; i >= 0; i-- {
		b[i] = ulidAlphabet[ms%32]
		ms /= 32
	}
	var random [16]byte
	_, _ = rand.Read(random[:])
	for i, c := range random {
		b[10+i] = ulidAlphabet[c%32]
	}
	return string(b[:])
}

func hookArtifactName(mode string) string {
	if mode == "batch" {
		return "result.csv"
	}
	return "result.json"
}

// seedMessage is buildReckonSeedMessage().
func seedMessage(mode string, params *jsObj, started time.Time, chunk int) string {
	lines := []string{
		"[webhook run started at " + jsDate(started) + "] Begin executing the workflow.",
		"",
		"Trigger parameters (JSON):",
		"```json",
		jsStringify(params, true),
		"```",
		"The same parameters are saved as JSON in $RUN_DIR/" + hookWorkflowAPIIn + ". Read large inputs from that file.",
		"",
		"[output] Write your single deliverable to $ARTIFACT_DIR/" + hookArtifactName(mode) + ". It is the only file the caller is served.",
		"Keep every intermediate and row-level file under $RUN_DIR. Never print row data into the reply.",
	}
	if mode == "batch" {
		lines = append(lines, "",
			"[batch] Execute the complete workflow in chunks of at most "+groupThousands(chunk)+" records.",
			"Pass intermediate datasets between steps by file path, and publish exactly one CSV artifact.")
	}
	lines = append(lines, "", "Reply with a short receipt only: counts and the artifact name.")
	return strings.Join(lines, "\n")
}

type hookAdmission struct {
	runID, status string
	replay        bool
	eligible      bool
}

// admit is admitReckonWebhookRun().
func (a *API) admit(ctx context.Context, h *compatWebhook, mode string, params *jsObj, key string, size int, now time.Time) (*hookAdmission, error) {
	fingerprint := sha256Hex(jsCanonical(obj("executionMode", mode, "params", params)))
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "reckon-webhook:"+h.reckonID+":"+key); err != nil {
		return nil, err
	}
	var existingID, existingPrint, existingStatus string
	err = tx.QueryRow(ctx, `select compat.id, compat.request_fingerprint, run.status
	     from compat_webhook_run compat
	     join workflow_run run on run.id = compat.workflow_run_id
	    where compat.reckon_workflow_id = $1 and compat.idempotency_key = $2`, h.reckonID, key).
		Scan(&existingID, &existingPrint, &existingStatus)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		if existingPrint != fingerprint {
			return nil, hookErr(409, 0, "error", "idempotency_conflict")
		}
		status := "queued"
		if existingStatus == "running" {
			status = "running"
		}
		return &hookAdmission{runID: existingID, status: status, replay: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	var accessOn, workflowOn bool
	var retention int64
	err = tx.QueryRow(ctx, `select access.enabled, workflow.enabled, access.retention_hours
	     from workflow_api_access access
	     join workflow_definition workflow on workflow.id = access.workflow_id
	    where access.workflow_id = $1 for update of access`, h.workflowID).Scan(&accessOn, &workflowOn, &retention)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if !accessOn {
		return nil, hookErr(422, 0, "error", "batch_not_ready", "status", "api_access_disabled",
			"message", "The target workflow does not accept API runs.")
	}
	if !workflowOn {
		return nil, hookErr(409, 0, "error", "workflow_disabled")
	}

	limits := readHookLimits()
	var total, perWorkflow int64
	if err := tx.QueryRow(ctx, `select count(*)::int, count(*) filter (where compat.reckon_workflow_id = $1)::int
	     from compat_webhook_run compat
	     join workflow_run run on run.id = compat.workflow_run_id
	    where run.status in ('queued', 'running')`, h.reckonID).Scan(&total, &perWorkflow); err != nil {
		return nil, err
	}
	if total >= max(1, limits.queueMax-limits.reserved) {
		return nil, hookErr(429, 10, "error", "queue_full")
	}
	if perWorkflow >= max(1, limits.perWorkflow-1) {
		return nil, hookErr(429, 10, "error", "workflow_queue_full")
	}

	runID := ulid(now.UnixMilli())
	var ids [4]string
	for i := range ids {
		if ids[i], err = newUUID(); err != nil {
			return nil, err
		}
	}
	workflowRunID, workRunID, threadID, admissionID := ids[0], ids[1], ids[2], ids[3]
	expires := now.Add(time.Duration(retention) * time.Hour)
	message := seedMessage(mode, params, now, h.chunk)
	payload := obj(hookSeedKey, obj("mode", mode, "message", message), "params", params)

	if _, err := tx.Exec(ctx, `insert into work_thread (id, org_id, title, channel, backend_state, created_at, updated_at, last_message_at)
	     values ($1, $2, $3, 'workflow', '{}'::jsonb, $4, $4, $4)`, threadID, h.orgID, "Webhook "+h.reckonID, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into work_run (id, org_id, thread_id, backend, status, actor_user_id, actor_role, created_at, updated_at)
	     values ($1, $2, $3, coalesce((select config->>'backend' from llm_provider_config where org_id = $2 and scope = 'agent'), 'ax'),
	             'queued', null, 'service', $4, $4)`, workRunID, h.orgID, threadID, now); err != nil {
		return nil, err
	}
	if err := admitRunSpend(ctx, tx, h.orgID, h.workflowID, workRunID, "webhook", now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into workflow_run (
	       id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload,
	       execution_mode, trigger_input_preview, status, progress, admitted_at,
	       result_expires_at, created_at, updated_at
	     ) values ($1, $2, $3, $4, $5, 'api', $6::jsonb, 'single', $7::jsonb, 'queued', '{"stage":"queued"}'::jsonb, $8, $9, $8, $8)`,
		workflowRunID, h.orgID, h.workflowID, threadID, workRunID,
		jsStringify(obj("source", "reckon_webhook", "reckonWorkflowId", h.reckonID, "mode", mode), false),
		jsStringify(obj("mode", mode, "params", params), false), now, expires); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into workflow_api_admission (
	       id, org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash,
	       execution_mode, request_payload, request_bytes, status,
	       reserved_tokens, reserved_cost_micros, available_at, created_at, updated_at, expires_at
	     ) values ($1, $2, $3, $4, $5, $6, 'single', $7::jsonb, $8, 'pending', 0, 0, $9, $9, $9, $10)`,
		admissionID, h.orgID, h.workflowID, workflowRunID, sha256Hex("reckon:"+h.reckonID+":"+key), fingerprint,
		jsStringify(payload, false), size, now, expires); err != nil {
		return nil, err
	}
	if err := a.queueAPIAdmission(ctx, tx, admissionID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into compat_webhook_run (
	       id, org_id, reckon_workflow_id, workflow_run_id, work_run_id,
	       execution_mode, idempotency_key, request_fingerprint, created_at
	     ) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		runID, h.orgID, h.reckonID, workflowRunID, workRunID, mode, key, fingerprint, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &hookAdmission{runID: runID, status: "queued", eligible: total < limits.concurrency}, nil
}

func obj(kv ...any) *jsObj {
	o := &jsObj{}
	for i := 0; i+1 < len(kv); i += 2 {
		o.set(kv[i].(string), kv[i+1])
	}
	return o
}

var budgetReason = map[string]string{
	"org_hourly":      "global_hourly_budget",
	"org_daily":       "global_daily_budget",
	"workflow_hourly": "workflow_hourly_budget",
	"workflow_daily":  "workflow_daily_budget",
}

type hookRun struct {
	runID, orgID, workRunID, mode, status string
	summary, failure                      *string
	progress                              any
	expired                               bool
}

// hookRunStatus is reckonRunStatus().
func hookRunStatus(status string) string {
	switch status {
	case "queued", "running", "completed", "needs_input":
		return status
	case "cancelled":
		return "aborted"
	}
	return "error"
}

func (a *API) hookRun(ctx context.Context, reckonID, runID string) (*hookRun, error) {
	run := hookRun{runID: runID}
	var progress []byte
	var expires *time.Time
	err := a.DB.QueryRow(ctx, `select compat.org_id, compat.work_run_id::text, compat.execution_mode,
	        run.status, run.summary, run.error, run.progress, run.result_expires_at
	   from compat_webhook_run compat
	   join workflow_run run on run.id = compat.workflow_run_id
	  where compat.reckon_workflow_id = $1 and compat.id = $2`, reckonID, runID).
		Scan(&run.orgID, &run.workRunID, &run.mode, &run.status, &run.summary, &run.failure, &progress, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run.status = hookRunStatus(run.status)
	run.progress = &jsObj{}
	if progress != nil {
		if v, err := jsParse(progress); err == nil && v != nil {
			run.progress = v
		}
	}
	run.expired = expires != nil && !expires.After(time.Now())
	return &run, nil
}

func (a *API) hookPending(w http.ResponseWriter, r *http.Request, reckonID, runID, mode, status string, replay bool) {
	base := strings.TrimRight(a.publicBaseURL(r), "/")
	resultURL := "/api/hooks/" + reckonID + "/result/" + runID
	absolute := base + resultURL
	body := obj("ok", true, "runId", runID, "status", status, "mode", mode, "idempotentReplay", replay,
		"note", hookPendingNote, "resultUrl", resultURL,
		"resultRequest", obj("method", "GET", "url", absolute, "headers", obj("X-Webhook-Token", hookTokenHint),
			"curl", "curl -H 'X-Webhook-Token: "+hookTokenHint+"' '"+absolute+"'"))
	hookJSON(w, http.StatusAccepted, body, 10, replay)
}

func hookFailureBody(run *hookRun) (int, *jsObj) {
	message := hookMissingBatch
	if run.failure != nil {
		message = *run.failure
	} else if run.mode != "batch" {
		message = "Run did not produce an artifact."
	}
	phase, status := "aborted", http.StatusConflict
	switch run.status {
	case "error":
		phase, status = "failed", http.StatusBadGateway
	case "needs_input":
		phase = "needs_input"
	}
	progress := jsSpread(run.progress)
	progress.set("phase", phase)
	progress.set("message", message)
	return status, obj("error", "run_failed", "status", run.status, "runId", run.runID, "message", message, "progress", progress)
}

// serveHookRun is serveReckonRun(): the result file goes out as stored.
func (a *API) serveHookRun(w http.ResponseWriter, r *http.Request, run *hookRun, replay bool) {
	if run.status != "completed" {
		status, body := hookFailureBody(run)
		hookJSON(w, status, body, 0, replay)
		return
	}
	root := filepath.Join(orgAgentRoot(run.orgID), "runs", run.workRunID, "artifacts")
	entries, _ := os.ReadDir(root)
	names := []any{}
	var files []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			files = append(files, e.Name())
		}
	}
	slices.SortFunc(files, func(x, y string) int {
		return slices.Compare(utf16.Encode([]rune(x)), utf16.Encode([]rune(y)))
	})
	for _, n := range files {
		names = append(names, n)
	}
	if run.mode == "batch" && !slices.Contains(files, "result.csv") {
		body := obj("error", "run_failed", "status", "error", "runId", run.runID, "message", hookMissingBatch)
		progress := jsSpread(run.progress)
		progress.set("phase", "failed")
		progress.set("message", hookMissingBatch)
		body.set("progress", progress)
		hookJSON(w, http.StatusBadGateway, body, 0, replay)
		return
	}
	noArtifacts := obj("error", "no_artifacts", "status", run.status, "runId", run.runID)
	if len(files) == 0 {
		hookJSON(w, http.StatusNotFound, noArtifacts, 0, replay)
		return
	}
	chosen := ""
	var jsons []string
	for _, n := range files {
		lower := strings.ToLower(n)
		if chosen == "" && lower == "result.json" {
			chosen = n
		}
		if strings.HasSuffix(lower, ".json") {
			jsons = append(jsons, n)
		}
	}
	switch {
	case run.mode == "batch":
		chosen = "result.csv"
	case chosen != "":
	case len(jsons) == 1:
		chosen = jsons[0]
	case len(files) == 1:
		chosen = files[0]
	default:
		hookJSON(w, http.StatusConflict, obj("error", "ambiguous_artifacts", "status", run.status, "runId", run.runID, "files", names), 0, replay)
		return
	}
	f, err := os.Open(filepath.Join(root, chosen))
	if err != nil {
		hookJSON(w, http.StatusNotFound, noArtifacts, 0, replay)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		hookJSON(w, http.StatusNotFound, noArtifacts, 0, replay)
		return
	}
	disposition := "inline"
	if strings.HasSuffix(strings.ToLower(chosen), ".csv") {
		disposition = "attachment"
	}
	if replay {
		w.Header().Set(hookDispatchHeader, "true")
	}
	w.Header().Set("Content-Type", artifactType(chosen))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(chosen, `"`, "")+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
}

// artifactType is reckonArtifactContentType().
func artifactType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json":
		return "application/json; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// servePostResult is serveReckonPostResult().
func (a *API) servePostResult(w http.ResponseWriter, r *http.Request, reckonID, runID string, replay bool) {
	run, err := a.hookRun(r.Context(), reckonID, runID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if run == nil {
		hookJSON(w, http.StatusNotFound, obj("error", "not found"), 0, replay)
		return
	}
	if run.status != "completed" {
		status := http.StatusBadGateway
		if run.status == "needs_input" {
			status = http.StatusConflict
		}
		var failure any
		if run.failure != nil {
			failure = *run.failure
		}
		hookJSON(w, status, obj("ok", false, "runId", run.runID, "status", run.status, "error", failure), 0, replay)
		return
	}
	a.serveHookRun(w, r, run, replay)
}

func (a *API) hookStart(w http.ResponseWriter, r *http.Request) {
	reckonID := r.PathValue("workflowId")
	h, err := a.authorizedWebhook(r)
	if err != nil {
		a.hookFailure(w, r, err)
		return
	}
	if err := a.consumeStartRate(r.Context(), h, time.Now()); err != nil {
		a.hookFailure(w, r, err)
		return
	}
	key, ok := hookKey(headerValue(r, "Idempotency-Key"))
	if !ok {
		a.hookFailure(w, r, hookErr(400, 0, "error", "invalid_idempotency_key", "message",
			"Send an Idempotency-Key header containing 1-200 letters, numbers, dots, underscores, colons, or dashes."))
		return
	}
	mode, ok := hookMode(r)
	if !ok {
		a.hookFailure(w, r, hookErr(400, 0, "error", "invalid_mode", "message", `mode must be "single" or "batch"`))
		return
	}
	text, err := a.readHookBody(r)
	if err != nil {
		a.hookFailure(w, r, err)
		return
	}
	var raw any = &jsObj{}
	if text != "" {
		if raw, err = jsParse([]byte(text)); err != nil {
			a.hookFailure(w, r, hookErr(400, 0, "error", "invalid_json"))
			return
		}
	}
	body, ok := raw.(*jsObj)
	if !ok {
		a.hookFailure(w, r, hookErr(400, 0, "error", "invalid_payload", "message", "Request JSON must be an object."))
		return
	}
	params := body
	if h.params != nil {
		params = &jsObj{}
		for _, k := range body.ordered() {
			if slices.Contains(h.params, k) {
				params.set(k, body.vals[k])
			}
		}
	}
	if mode == "batch" {
		maxRecords := min(hookMaxBatch, h.chunk)
		if largestArray(params) > maxRecords {
			a.hookFailure(w, r, hookErr(413, 0, "error", "too_many_records",
				"message", fmt.Sprintf("Batch arrays may contain at most %d records.", maxRecords), "maxRecords", float64(maxRecords)))
			return
		}
	}

	now := time.Now().Truncate(time.Millisecond)
	admitted, err := a.admit(r.Context(), h, mode, params, key, len(text), now)
	var over *spendExceeded
	if errors.As(err, &over) {
		a.recordBudgetBlocked(context.WithoutCancel(r.Context()), over)
		reason, ok := budgetReason[over.budget]
		if !ok {
			reason = "workflow_daily_budget"
		}
		err = hookErr(429, over.retryAfter, "error", reason, "limitUsd", microsToUSD(over.limitMicros),
			"committedUsd", microsToUSD(over.committedMicros), "reservationUsd", microsToUSD(over.reserve))
	}
	if err != nil {
		a.hookFailure(w, r, err)
		return
	}

	if admitted.replay {
		run, err := a.hookRun(r.Context(), reckonID, admitted.runID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if run == nil {
			hookJSON(w, http.StatusConflict, obj("error", "idempotency_run_missing"), 0, false)
			return
		}
		if run.status == "queued" || run.status == "running" {
			a.hookPending(w, r, reckonID, admitted.runID, mode, run.status, true)
			return
		}
		a.servePostResult(w, r, reckonID, admitted.runID, true)
		return
	}

	wants := r.URL.Query().Get("wait")
	if (wants != "true" && wants != "1") || !admitted.eligible || !a.takeWaiter() {
		a.hookPending(w, r, reckonID, admitted.runID, mode, "queued", false)
		return
	}
	defer a.hookWaiters.Add(-1)
	seconds, _ := jsNumber(r.URL.Query().Get("wait_timeout"))
	timeout := time.Duration(math.Min(hookWaitMaxMs, math.Max(1_000, seconds*1_000))) * time.Millisecond
	outcome, err := a.waitForRun(r.Context(), reckonID, admitted.runID, timeout)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if outcome != "terminal" {
		a.hookPending(w, r, reckonID, admitted.runID, mode, outcome, false)
		return
	}
	a.servePostResult(w, r, reckonID, admitted.runID, false)
}

func (a *API) takeWaiter() bool {
	for {
		n := a.hookWaiters.Load()
		if n >= hookSyncWaiters {
			return false
		}
		if a.hookWaiters.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (a *API) waitForRun(ctx context.Context, reckonID, runID string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, err := a.hookRun(ctx, reckonID, runID)
		if err != nil {
			return "", err
		}
		if run == nil {
			return "queued", nil
		}
		if run.status != "queued" && run.status != "running" {
			return "terminal", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(hookPollInterval):
		}
	}
	run, err := a.hookRun(ctx, reckonID, runID)
	if err != nil {
		return "", err
	}
	if run != nil && run.status == "running" {
		return "running", nil
	}
	return "queued", nil
}

func (a *API) hookResult(w http.ResponseWriter, r *http.Request) {
	reckonID, runID := r.PathValue("workflowId"), r.PathValue("runId")
	h, err := a.authorizedWebhook(r)
	if err != nil {
		a.hookFailure(w, r, err)
		return
	}
	start, retry := minuteWindow(time.Now())
	ok, err := a.hookBucket(r.Context(), "reckon:"+h.reckonID, "poll", readHookLimits().resultRate, start)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !ok {
		a.hookFailure(w, r, hookErr(429, retry, "error", "rate_limited"))
		return
	}
	run, err := a.hookRun(r.Context(), reckonID, runID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if run == nil {
		a.hookFailure(w, r, hookNotFound())
		return
	}
	if run.expired {
		hookJSON(w, http.StatusGone, obj("error", "artifact_expired", "status", run.status, "runId", runID), 0, false)
		return
	}
	if run.status == "queued" || run.status == "running" {
		hookJSON(w, http.StatusAccepted, obj("ok", true, "status", run.status, "runId", runID, "progress", run.progress), 10, false)
		return
	}
	a.serveHookRun(w, r, run, false)
}
