package webfront

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *API) visibleWorkflow(w http.ResponseWriter, r *http.Request) (actor, string, bool) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return act, "", false
	}
	id := r.PathValue("workflowId")
	hidden, err := a.workflowHidden(r.Context(), act, id)
	if err != nil {
		a.fail(w, r, err)
		return act, "", false
	}
	if hidden {
		sendError(w, http.StatusNotFound, "Workflow not found")
		return act, "", false
	}
	return act, id, true
}

func (a *API) workflowPatch(w http.ResponseWriter, r *http.Request) {
	act, id, ok := a.visibleWorkflow(w, r)
	if !ok {
		return
	}
	body, parsed := readBody(r)
	if parsed && body == nil {
		sendError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	values := map[string]any{}
	if v, ok := fieldBool(body, "enabled"); ok {
		values["enabled"] = v
		if v {
			values["paused_until"] = nil
		}
	}
	if v, ok := fieldBool(body, "cronEnabled"); ok {
		values["cron_enabled"] = v
	}
	if v, ok := fieldBool(body, "pauseForToday"); ok && v {
		now := time.Now().UTC()
		values["enabled"] = false
		values["paused_until"] = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	}
	sets := []string{}
	args := []any{act.OrgID, id}
	for _, column := range []string{"enabled", "cron_enabled", "paused_until"} {
		if v, ok := values[column]; ok {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
		}
	}
	if len(sets) == 0 {
		sendError(w, http.StatusBadRequest, "no recognized fields")
		return
	}
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "workflow not found")
		return
	}
	found := false
	err := pgx.BeginFunc(r.Context(), a.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `update workflow_definition set `+strings.Join(sets, ", ")+`, updated_at = now()
			where org_id = $1 and id = $2`, args...)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		found = true
		enabled, ok := values["enabled"]
		if !ok {
			return nil
		}
		_, err = tx.Exec(r.Context(), `update watcher set enabled = $3, updated_at = now()
			where org_id = $1 and workflow_id = $2 and enabled <> $3`, act.OrgID, id, enabled)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !found {
		sendError(w, http.StatusNotFound, "workflow not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func fieldBool(body any, name string) (bool, bool) {
	v, _ := field(body, name)
	b, ok := v.(bool)
	return b, ok
}

func (a *API) workflowDelete(w http.ResponseWriter, r *http.Request) {
	act, id, ok := a.visibleWorkflow(w, r)
	if !ok {
		return
	}
	if !isUUID(id) {
		sendError(w, http.StatusNotFound, "workflow not found")
		return
	}
	tag, err := a.DB.Exec(r.Context(), `delete from workflow_definition where org_id = $1 and id = $2`, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		sendError(w, http.StatusNotFound, "workflow not found")
		return
	}
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type workflowRecord struct {
	ID                  string     `json:"id"`
	OrgID               string     `json:"orgId"`
	Name                string     `json:"name"`
	Description         string     `json:"description"`
	Enabled             bool       `json:"enabled"`
	Status              string     `json:"status"`
	Goal                string     `json:"goal"`
	SystemPromptOverlay string     `json:"systemPromptOverlay"`
	Steps               storedJSON `json:"steps"`
	Cron                *string    `json:"cron"`
	CronTimezone        string     `json:"cronTimezone"`
	CronEnabled         bool       `json:"cronEnabled"`
	NetworkHosts        []string   `json:"networkHosts"`
	DailyRunBudget      *int       `json:"dailyRunBudget"`
	OutputContract      storedJSON `json:"outputContract"`
	CreatedByThreadID   *string    `json:"createdByThreadId"`
	CreatedByRunID      *string    `json:"createdByRunId"`
	OwnerUserID         string     `json:"ownerUserId"`
	OriginID            *string    `json:"originId"`
	ParentID            *string    `json:"parentId"`
	CreatedAt           string     `json:"createdAt"`
	UpdatedAt           string     `json:"updatedAt"`
}

const workflowRecordColumns = `id::text, org_id, name, description, enabled, status, goal, system_prompt_overlay, steps, cron,
	cron_timezone, cron_enabled, network_hosts, daily_run_budget, output_contract, created_by_thread_id::text,
	created_by_run_id::text, owner_user_id, origin_id::text, parent_id::text, created_at, updated_at`

func scanWorkflowRecord(row pgx.Row) (workflowRecord, error) {
	var w workflowRecord
	var steps, contract []byte
	var created, updated time.Time
	err := row.Scan(&w.ID, &w.OrgID, &w.Name, &w.Description, &w.Enabled, &w.Status, &w.Goal, &w.SystemPromptOverlay, &steps,
		&w.Cron, &w.CronTimezone, &w.CronEnabled, &w.NetworkHosts, &w.DailyRunBudget, &contract, &w.CreatedByThreadID,
		&w.CreatedByRunID, &w.OwnerUserID, &w.OriginID, &w.ParentID, &created, &updated)
	if steps == nil || string(steps) == "null" {
		steps = []byte("[]")
	}
	if string(contract) == "null" {
		contract = nil
	}
	if w.NetworkHosts == nil {
		w.NetworkHosts = []string{}
	}
	w.Steps, w.OutputContract, w.CreatedAt, w.UpdatedAt = storedJSON(steps), storedJSON(contract), jsTime(created), jsTime(updated)
	return w, err
}

func (a *API) workflowAdopt(w http.ResponseWriter, r *http.Request) {
	act, id, ok := a.visibleWorkflow(w, r)
	if !ok {
		return
	}
	if !act.Admin {
		sendError(w, http.StatusForbidden, "admin only")
		return
	}
	ctx := r.Context()
	var source workflowRecord
	var err error
	if isUUID(id) {
		source, err = scanWorkflowRecord(a.DB.QueryRow(ctx, `select `+workflowRecordColumns+`
			from workflow_definition where org_id = $1 and id = $2`, act.OrgID, id))
	} else {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && source.OwnerUserID == "") {
		sendError(w, http.StatusNotFound, "Personal workflow not found: "+id)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	saved, verb, err := a.saveTeamCopy(ctx, source)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordWorkflowChange(ctx, saved, verb)
	adopted, err := scanWorkflowRecord(a.DB.QueryRow(ctx, `update workflow_definition set parent_id = $2, updated_at = now()
		where id = $1 returning `+workflowRecordColumns, saved.ID, source.ID))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.insertConfigChange(ctx, configChange{OrgID: act.OrgID, Kind: "workflow", Ref: source.Name, Actor: act.UserID,
		Summary: "Adopted workflow for the team: " + source.Name, UserID: source.OwnerUserID, Status: "adopted"})
	sendJSON(w, http.StatusOK, map[string]any{"workflow": adopted})
}

// saveTeamCopy is saveWorkflow() in packages/llm/src/workflows/store.ts for
// the input that adoptWorkflowForTeam() gives it.
func (a *API) saveTeamCopy(ctx context.Context, s workflowRecord) (workflowRecord, string, error) {
	steps, contract := []byte(s.Steps), []byte(s.OutputContract)
	saved, err := scanWorkflowRecord(a.DB.QueryRow(ctx, `update workflow_definition set description = $3, system_prompt_overlay = $4,
		steps = $5, goal = $6, cron = $7, cron_timezone = $8, cron_enabled = $9, daily_run_budget = $10, output_contract = $11,
		updated_at = now()
		where org_id = $1 and owner_user_id = '' and name = $2 returning `+workflowRecordColumns,
		s.OrgID, s.Name, s.Description, s.SystemPromptOverlay, steps, s.Goal, s.Cron, s.CronTimezone, s.CronEnabled,
		s.DailyRunBudget, contract))
	if err == nil {
		return saved, "Updated", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return saved, "", err
	}
	var newID string
	if err := a.DB.QueryRow(ctx, `insert into workflow_definition (org_id, owner_user_id, name, description, system_prompt_overlay,
		steps, goal, cron, cron_timezone, cron_enabled, network_hosts, daily_run_budget, output_contract)
		values ($1, '', $2, $3, $4, $5, $6, $7, $8, $9, '{}', $10, $11) returning id::text`,
		s.OrgID, s.Name, s.Description, s.SystemPromptOverlay, steps, s.Goal, s.Cron, s.CronTimezone, s.CronEnabled,
		s.DailyRunBudget, contract).Scan(&newID); err != nil {
		return saved, "", err
	}
	saved, err = scanWorkflowRecord(a.DB.QueryRow(ctx, `update workflow_definition set origin_id = id where id = $1
		returning `+workflowRecordColumns, newID))
	return saved, "Added", err
}

var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

// jsStringOf is String(v) for a JSON value.
func jsStringOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		out, _ := json.Marshal(x)
		return string(out)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = jsStringOf(e)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

var limitNames = []string{
	"requestLimitPerMinute", "pollLimitPerMinute", "queueCap", "concurrencyCap", "batchMaxRecords", "batchChunkSize",
	"maxRequestBytes", "maxResultBytes", "maxArtifactBytes", "maxRuntimeSeconds", "maxModelCalls", "maxToolCalls",
	"maxTokensPerRun", "maxCostMicrosPerRun", "rollingWindowSeconds", "rollingTokenBudget", "rollingCostMicrosBudget",
	"retentionHours",
}

var limitColumns = []string{
	"request_limit_per_minute", "poll_limit_per_minute", "queue_cap", "concurrency_cap", "batch_max_records",
	"batch_chunk_size", "max_request_bytes", "max_result_bytes", "max_artifact_bytes", "max_runtime_seconds",
	"max_model_calls", "max_tool_calls", "max_tokens_per_run", "max_cost_micros_per_run", "rolling_window_seconds",
	"rolling_token_budget", "rolling_cost_micros_budget", "retention_hours",
}

var limitRanges = [][2]int64{
	{1, 600}, {1, 1200}, {1, 250}, {1, 20}, {1, 10000}, {1, 500}, {1024, 1 << 20}, {1024, 1 << 20}, {1024, 50 << 20},
	{30, 1800}, {1, 1024}, {1, 1024}, {1000, 3000000}, {1000, 100000000}, {3600, 604800}, {1000, 10000000},
	{1000, 1000000000}, {1, 720},
}

const (
	limBatchMax   = 4
	limBatchChunk = 5
	limRuntime    = 9
	limModelCalls = 10
	limToolCalls  = 11
)

type apiLimits [18]int64

func (l apiLimits) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range limitNames {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%d", name, l[i])
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// envInt is readInt() in packages/llm/src/agent-limits.ts.
func envInt(name string, fallback, min int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	f, ok := jsNumber(raw)
	if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
		return fallback
	}
	n := math.Floor(f)
	if n < float64(min) {
		return min
	}
	if n > math.MaxInt64/2 {
		return math.MaxInt64 / 2
	}
	return int64(n)
}

// defaultAPILimits is defaultWorkflowApiLimits().
func defaultAPILimits() apiLimits {
	l := apiLimits{30, 120, 25, 2, 1000, 100, 256 << 10, 256 << 10, 10 << 20, 900, 301, 150, 2000000, 5000000, 86400,
		10000000, 10000000, 168}
	clamp := func(i int, v float64) int64 {
		return max(limitRanges[i][0], min(limitRanges[i][1], int64(math.Floor(v+0.5))))
	}
	timeoutMs := envInt("OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", 15*60_000, 1000)
	l[limRuntime] = clamp(limRuntime, float64(timeoutMs)/1000)
	l[limToolCalls] = clamp(limToolCalls, float64(envInt("OPENNEKO_WORKFLOW_MAX_TOOL_CALLS", 150, 1)))
	l[limModelCalls] = clamp(limModelCalls, float64(2*l[limToolCalls]+1))
	return l
}

type apiBatchView struct {
	Available    bool     `json:"available"`
	RecordsField *string  `json:"recordsField"`
	Columns      []string `json:"columns"`
}

type apiAccessView struct {
	WorkflowID       string       `json:"workflowId"`
	Enabled          bool         `json:"enabled"`
	TokenPrefix      *string      `json:"tokenPrefix"`
	TokenCreatedAt   *string      `json:"tokenCreatedAt"`
	TokenRotatedAt   *string      `json:"tokenRotatedAt"`
	LastUsedAt       *string      `json:"lastUsedAt"`
	Limits           apiLimits    `json:"limits"`
	IncludeTelemetry bool         `json:"includeTelemetry"`
	Batch            apiBatchView `json:"batch"`
	CreatedAt        *string      `json:"createdAt"`
	UpdatedAt        *string      `json:"updatedAt"`
}

var (
	recordsFieldPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)
	pathPartPattern     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
)

type batchColumn struct {
	Name, Path string
	Default    any
	HasDefault bool
}

// batchColumns is parseCompiledWorkflowBatchContract() in
// packages/llm/src/workflows/api-contract.ts; ok is false when it gives null.
func batchColumns(outputContract []byte) (recordsField string, columns []string, ok bool) {
	recordsField, list, ok := compiledBatch(outputContract)
	for _, c := range list {
		columns = append(columns, c.Name)
	}
	return recordsField, columns, ok
}

func compiledBatch(outputContract []byte) (recordsField string, columns []batchColumn, ok bool) {
	parsed, err := jsParse(outputContract)
	if err != nil {
		return "", nil, false
	}
	raw, isObject := jsGet(parsed, "apiBatch").(*jsObj)
	if !isObject {
		return "", nil, false
	}
	if v, _ := raw.vals["version"].(float64); v != 1 || raw.vals["compiled"] != true || raw.vals["compiler"] != "workflow" {
		return "", nil, false
	}
	recordsField = "records"
	if s, isString := raw.vals["recordsField"].(string); isString && recordsFieldPattern.MatchString(s) {
		recordsField = s
	}
	list, _ := raw.vals["columns"].([]any)
	if len(list) == 0 || len(list) > 64 {
		return "", nil, false
	}
	seen := map[string]bool{}
	for _, candidate := range list {
		c, isObject := candidate.(*jsObj)
		if !isObject {
			return "", nil, false
		}
		name, nameOK := c.vals["name"].(string)
		path, pathOK := c.vals["path"].(string)
		if !nameOK || name == "" || jsLength(name) > 80 || strings.ContainsAny(name, "\r\n") || seen[name] ||
			!pathOK || path == "" || jsLength(path) > 256 {
			return "", nil, false
		}
		for _, part := range strings.Split(path, ".") {
			if !pathPartPattern.MatchString(part) || part == "__proto__" || part == "prototype" || part == "constructor" {
				return "", nil, false
			}
		}
		col := batchColumn{Name: name, Path: path}
		if fallback, present := c.vals["default"]; present {
			switch fallback.(type) {
			case nil, string, float64, bool:
			default:
				return "", nil, false
			}
			col.Default, col.HasDefault = fallback, true
		}
		seen[name] = true
		columns = append(columns, col)
	}
	return recordsField, columns, true
}

// apiAccess is getWorkflowApiAccess(); nil means no such workflow.
func apiAccess(ctx context.Context, q querier, orgID, workflowID string) (*apiAccessView, error) {
	if !isUUID(workflowID) {
		return nil, nil
	}
	var contract []byte
	err := q.QueryRow(ctx, `select output_contract from workflow_definition where org_id = $1 and id = $2`, orgID, workflowID).Scan(&contract)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := &apiAccessView{WorkflowID: workflowID, Limits: defaultAPILimits(), Batch: apiBatchView{Columns: []string{}}}
	if field, columns, ok := batchColumns(contract); ok {
		v.Batch = apiBatchView{Available: true, RecordsField: &field, Columns: columns}
	}
	var created, updated, tokenCreated, tokenRotated, lastUsed *time.Time
	var l apiLimits
	dest := []any{&v.Enabled, &v.TokenPrefix, &tokenCreated, &tokenRotated, &lastUsed, &v.IncludeTelemetry, &created, &updated}
	for i := range l {
		dest = append(dest, &l[i])
	}
	err = q.QueryRow(ctx, `select enabled, token_prefix, token_created_at, token_rotated_at, last_used_at, include_telemetry,
		created_at, updated_at, `+strings.Join(limitColumns, ", ")+`
		from workflow_api_access where org_id = $1 and workflow_id = $2`, orgID, workflowID).Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	v.Limits = l
	v.TokenCreatedAt, v.TokenRotatedAt, v.LastUsedAt = jsTimePtr(tokenCreated), jsTimePtr(tokenRotated), jsTimePtr(lastUsed)
	v.CreatedAt, v.UpdatedAt = jsTimePtr(created), jsTimePtr(updated)
	return v, nil
}

type apiAccessError struct {
	status        int
	code, message string
}

func (e *apiAccessError) Error() string { return e.message }

var errAccessWorkflowMissing = &apiAccessError{http.StatusNotFound, "workflow_not_found", "Workflow not found."}

func sendAccess(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store, private")
	sendJSON(w, status, body)
}

func (a *API) accessFailure(w http.ResponseWriter, r *http.Request, err error) {
	var e *apiAccessError
	if errors.As(err, &e) {
		sendAccess(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.message}})
		return
	}
	log.Printf("[workflow-api-admin] %s %s: request failed: %v", r.Method, r.URL.Path, err)
	sendAccess(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "internal_error", "message": "Request failed."}})
}

// accessAdmin is requireAdminActor().
func (a *API) accessAdmin(w http.ResponseWriter, r *http.Request) (actor, bool) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return act, false
	}
	if !act.Admin || act.UserID == "" {
		sendError(w, http.StatusForbidden, "admin only")
		return act, false
	}
	return act, true
}

func (a *API) savedPublicURL(ctx context.Context, orgID string) (*string, error) {
	var saved *string
	err := a.DB.QueryRow(ctx, `select config->>'url' from llm_provider_config where org_id = $1 and scope = 'public-url'`, orgID).Scan(&saved)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if saved != nil && *saved != "" {
		return saved, nil
	}
	env := a.PublicURL
	if env == "" {
		env = os.Getenv("OPENNEKO_PUBLIC_URL")
	}
	if env = strings.TrimRight(strings.TrimSpace(env), "/"); env != "" {
		return &env, nil
	}
	return nil, nil
}

func (a *API) workflowAPIAccessGet(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx, id := r.Context(), r.PathValue("workflowId")
	access, err := apiAccess(ctx, a.DB, act.OrgID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if access != nil {
		var payload []byte
		err = a.DB.QueryRow(ctx, `select request_payload from workflow_api_admission
			where org_id = $1 and workflow_id = $2 and execution_mode = 'single' and request_payload is not null
			order by created_at desc limit 1`, act.OrgID, id).Scan(&payload)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.fail(w, r, err)
			return
		}
		if !strings.HasPrefix(string(payload), "{") {
			payload = nil
		}
		publicURL, err := a.savedPublicURL(ctx, act.OrgID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sendAccess(w, http.StatusOK, map[string]any{"access": access, "lastRequest": storedJSON(payload), "publicUrl": publicURL})
		return
	}
	sendAccess(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "workflow_not_found", "message": "Workflow not found."}})
}

func (a *API) workflowAPIAccessPost(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	body, parsed := readBody(r)
	if parsed && body == nil {
		a.accessFailure(w, r, errors.New("request body is null"))
		return
	}
	action, _ := field(body, "action")
	if action != "enable" && action != "rotate" {
		a.accessFailure(w, r, &apiAccessError{http.StatusBadRequest, "invalid_action", "Action must be 'enable' or 'rotate'."})
		return
	}
	result, err := a.issueAPIToken(r.Context(), act, r.PathValue("workflowId"), action.(string))
	if err != nil {
		a.accessFailure(w, r, err)
		return
	}
	status := http.StatusOK
	if action == "enable" {
		status = http.StatusCreated
	}
	sendAccess(w, status, result)
}

func (a *API) issueAPIToken(ctx context.Context, act actor, workflowID, action string) (map[string]any, error) {
	if !isUUID(workflowID) {
		return nil, errAccessWorkflowMissing
	}
	secret, fingerprint := make([]byte, 32), make([]byte, 6)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if _, err := rand.Read(fingerprint); err != nil {
		return nil, err
	}
	prefix := "onk_wf_" + hex.EncodeToString(fingerprint)
	token := prefix + "_" + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	verifier := hex.EncodeToString(sum[:])
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var locked string
		err := tx.QueryRow(ctx, `select id::text from workflow_definition where id = $1 and org_id = $2 for update`, workflowID, act.OrgID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return errAccessWorkflowMissing
		}
		if err != nil {
			return err
		}
		var enabled bool
		err = tx.QueryRow(ctx, `select enabled from workflow_api_access where org_id = $1 and workflow_id = $2`, act.OrgID, workflowID).Scan(&enabled)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if action == "enable" && enabled {
			return &apiAccessError{http.StatusConflict, "api_access_already_enabled", "API access is already enabled. Rotate the token to replace it."}
		}
		if action == "rotate" && !enabled {
			return &apiAccessError{http.StatusConflict, "api_access_disabled", "Enable API access before rotating its token."}
		}
		limits := defaultAPILimits()
		args := []any{workflowID, act.OrgID, verifier, prefix, action == "rotate"}
		placeholders := make([]string, len(limitColumns))
		for i := range limitColumns {
			args = append(args, limits[i])
			placeholders[i] = "$" + strconv.Itoa(len(args))
		}
		_, err = tx.Exec(ctx, `insert into workflow_api_access (workflow_id, org_id, enabled, token_verifier, token_prefix,
			token_created_at, token_rotated_at, updated_at, `+strings.Join(limitColumns, ", ")+`)
			values ($1, $2, true, $3, $4, now(), case when $5 then now() end, now(), `+strings.Join(placeholders, ", ")+`)
			on conflict (workflow_id) do update set org_id = excluded.org_id, enabled = true, token_verifier = excluded.token_verifier,
			  token_prefix = excluded.token_prefix, token_created_at = excluded.token_created_at,
			  token_rotated_at = excluded.token_rotated_at, updated_at = excluded.updated_at`, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	event := "enabled"
	if action == "rotate" {
		event = "token_rotated"
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "workflow_api_access", EntityID: workflowID, Event: event,
		Payload: map[string]any{"actorUserId": act.UserID, "actorRole": "admin", "tokenPrefix": prefix}})
	access, err := apiAccess(ctx, a.DB, act.OrgID, workflowID)
	if err == nil && access == nil {
		err = errors.New("workflow API access disappeared after update")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"access": access, "token": token}, nil
}

func (a *API) workflowAPIAccessDelete(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx, id := r.Context(), r.PathValue("workflowId")
	if !isUUID(id) {
		a.accessFailure(w, r, errAccessWorkflowMissing)
		return
	}
	tag, err := a.DB.Exec(ctx, `update workflow_api_access set enabled = false, updated_at = now() where org_id = $1 and workflow_id = $2`, act.OrgID, id)
	if err != nil {
		a.accessFailure(w, r, err)
		return
	}
	if tag.RowsAffected() > 0 {
		a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "workflow_api_access", EntityID: id, Event: "disabled",
			Payload: map[string]any{"actorUserId": act.UserID, "actorRole": "admin"}})
	}
	access, err := apiAccess(ctx, a.DB, act.OrgID, id)
	if err == nil && access == nil {
		err = errAccessWorkflowMissing
	}
	if err != nil {
		a.accessFailure(w, r, err)
		return
	}
	sendAccess(w, http.StatusOK, map[string]any{"access": access})
}

// limitPatch is workflowApiLimitPatch(): keys are checked in the order
// Object.entries() gives them.
func limitPatch(raw json.RawMessage) (map[int]int64, error) {
	keys, values, ok := jsObject(raw)
	if !ok {
		return nil, &apiAccessError{http.StatusBadRequest, "invalid_limits", "Limits must be a JSON object."}
	}
	patch := map[int]int64{}
	for _, key := range keys {
		i := slices.Index(limitNames, key)
		if i < 0 {
			return nil, &apiAccessError{http.StatusBadRequest, "unknown_limit", "Unknown workflow API limit: " + key + "."}
		}
		f := math.NaN()
		if text := strings.TrimSpace(string(values[key])); text != "" && (text[0] == '-' || text[0] >= '0' && text[0] <= '9') {
			if parsed, err := strconv.ParseFloat(text, 64); err == nil || errors.Is(err, strconv.ErrRange) {
				f = parsed
			}
		}
		if f != math.Trunc(f) || math.Abs(f) > 1<<53-1 {
			return nil, &apiAccessError{http.StatusBadRequest, "invalid_limit", key + " must be an integer."}
		}
		if int64(f) < limitRanges[i][0] || int64(f) > limitRanges[i][1] {
			return nil, &apiAccessError{http.StatusBadRequest, "limit_out_of_range",
				fmt.Sprintf("%s must be between %d and %d.", key, limitRanges[i][0], limitRanges[i][1])}
		}
		patch[i] = int64(f)
	}
	chunk, hasChunk := patch[limBatchChunk]
	most, hasMost := patch[limBatchMax]
	if hasChunk && hasMost && chunk > most {
		return nil, errBatchLimits
	}
	return patch, nil
}

var errBatchLimits = &apiAccessError{http.StatusBadRequest, "invalid_batch_limits", "batchChunkSize cannot exceed batchMaxRecords."}

// jsObject gives the keys of a JSON object in Object.keys() order, with
// the last value of a repeated key.
func jsObject(raw json.RawMessage) ([]string, map[string]json.RawMessage, bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, nil, false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if _, err := dec.Token(); err != nil {
		return nil, nil, false
	}
	keys := []string{}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, false
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, nil, false
		}
		if k := tok.(string); !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	slices.SortStableFunc(keys, func(x, y string) int {
		xi, xok := arrayIndex(x)
		yi, yok := arrayIndex(y)
		switch {
		case xok && yok:
			return int(int64(xi) - int64(yi))
		case xok:
			return -1
		case yok:
			return 1
		}
		return 0
	})
	return keys, values, true
}

func (a *API) workflowAPIAccessPatch(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		a.accessFailure(w, r, err)
		return
	}
	var top any
	if json.Unmarshal(raw, &top) == nil && top == nil {
		a.accessFailure(w, r, errors.New("request body is null"))
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		body = nil
	}
	patch := map[int]int64{}
	if limits, ok := body["limits"]; ok {
		if patch, err = limitPatch(limits); err != nil {
			a.accessFailure(w, r, err)
			return
		}
	}
	var telemetry *bool
	if rawTelemetry, ok := body["includeTelemetry"]; ok {
		b := string(rawTelemetry) == "true"
		if !b && string(rawTelemetry) != "false" {
			a.accessFailure(w, r, &apiAccessError{http.StatusBadRequest, "invalid_telemetry", "includeTelemetry must be true or false."})
			return
		}
		telemetry = &b
	}
	if len(patch) == 0 && telemetry == nil {
		a.accessFailure(w, r, &apiAccessError{http.StatusBadRequest, "empty_limits", "At least one limit is required."})
		return
	}
	access, err := a.updateAPILimits(r.Context(), act, r.PathValue("workflowId"), patch, telemetry)
	if err != nil {
		a.accessFailure(w, r, err)
		return
	}
	sendAccess(w, http.StatusOK, map[string]any{"access": access})
}

func (a *API) updateAPILimits(ctx context.Context, act actor, id string, patch map[int]int64, telemetry *bool) (*apiAccessView, error) {
	current, err := apiAccess(ctx, a.DB, act.OrgID, id)
	if err == nil && current == nil {
		err = errAccessWorkflowMissing
	}
	if err != nil {
		return nil, err
	}
	merged := current.Limits
	for i, v := range patch {
		merged[i] = v
	}
	if merged[limBatchChunk] > merged[limBatchMax] {
		return nil, errBatchLimits
	}
	columns := []string{"enabled", "updated_at"}
	args := []any{id, act.OrgID, current.Enabled, time.Now()}
	fields := []string{}
	for i := range limitColumns {
		if v, ok := patch[i]; ok {
			columns = append(columns, limitColumns[i])
			args = append(args, v)
			fields = append(fields, limitNames[i])
		}
	}
	if telemetry != nil {
		columns = append(columns, "include_telemetry")
		args = append(args, *telemetry)
		fields = append(fields, "includeTelemetry")
	}
	sets := make([]string, len(columns))
	for i, c := range columns {
		sets[i] = fmt.Sprintf("%s = $%d", c, i+3)
	}
	tag, err := a.DB.Exec(ctx, `update workflow_api_access set `+strings.Join(sets, ", ")+` where workflow_id = $1 and org_id = $2`, args...)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		defaults := defaultAPILimits()
		for i := range limitColumns {
			if _, ok := patch[i]; !ok {
				columns = append(columns, limitColumns[i])
				args = append(args, defaults[i])
			}
		}
		placeholders := make([]string, len(columns))
		for i := range columns {
			placeholders[i] = "$" + strconv.Itoa(i+3)
		}
		if _, err := a.DB.Exec(ctx, `insert into workflow_api_access (workflow_id, org_id, `+strings.Join(columns, ", ")+`)
			values ($1, $2, `+strings.Join(placeholders, ", ")+`)`, args...); err != nil {
			return nil, err
		}
	}
	slices.Sort(fields)
	sorted := make([]any, len(fields))
	for i, f := range fields {
		sorted[i] = f
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "workflow_api_access", EntityID: id, Event: "limits_updated",
		Payload: map[string]any{"actorUserId": act.UserID, "actorRole": "admin", "fields": sorted}})
	access, err := apiAccess(ctx, a.DB, act.OrgID, id)
	if err == nil && access == nil {
		err = errors.New("workflow API access disappeared after update")
	}
	return access, err
}
