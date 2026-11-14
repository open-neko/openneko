package webfront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/open-neko/neko/apps/openneko/internal/config"
)

const (
	wfAPIHardMaxBytes   = 1 << 20
	wfAPIHardMaxRecords = 10_000
	wfAPIPreviewBytes   = 4_096
	wfAPISerialTries    = 8
)

var (
	wfAPIActive     = []string{"pending", "dispatching", "enqueued", "running"}
	wfAPITerminal   = map[string]bool{"completed": true, "failed": true, "cancelled": true, "needs_input": true}
	wfSensitiveKey  = regexp.MustCompile(`(?:authorization|cookie|password|passwd|secret|token|api[_-]?key|private[_-]?key|credential)`)
	errWFAPIEscaped = errors.New("Workflow API file path escaped the organization workspace.")
)

type wfAPIError struct {
	status        int
	code, message string
	retryAfter    int64
}

func (e *wfAPIError) Error() string { return e.code + ": " + e.message }

func wfErr(status int, code, message string) *wfAPIError {
	return &wfAPIError{status: status, code: code, message: message}
}

func (e *wfAPIError) retry(seconds int64) *wfAPIError {
	e.retryAfter = seconds
	return e
}

var errWFCredentials = func() *wfAPIError {
	return wfErr(http.StatusUnauthorized, "invalid_credentials", "The workflow credential is invalid.")
}

func wfAPIWrite(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, jsStringify(body, false))
}

func (a *API) wfAPIFailure(w http.ResponseWriter, r *http.Request, err error) int {
	var e *wfAPIError
	if errors.As(err, &e) {
		if e.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(e.retryAfter, 10))
		}
		if e.status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="workflow-api"`)
		}
		wfAPIWrite(w, e.status, obj("error", obj("code", e.code, "message", e.message)))
		return e.status
	}
	log.Printf("[workflow-api] request failed: %s %s: %v", r.Method, r.URL.Path, err)
	wfAPIWrite(w, http.StatusInternalServerError,
		obj("error", obj("code", "internal_error", "message", "The workflow API request could not be completed.")))
	return http.StatusInternalServerError
}

// startupTrace writes the startup_timing lines of
// packages/telemetry/src/startup.ts for one request.
type startupTrace struct {
	requestID, workflowRunID string
	started                  time.Time
}

func newStartupTrace(workflowRunID string) *startupTrace {
	id, _ := newUUID()
	return &startupTrace{requestID: id, workflowRunID: workflowRunID, started: time.Now()}
}

func (s *startupTrace) log(phase string, kv ...any) {
	line := obj("type", "startup_timing", "timestamp", jsTime(time.Now()), "requestId", s.requestID)
	if s.workflowRunID != "" {
		line.set("workflowRunId", s.workflowRunID)
	}
	line.set("version", firstEnv("unknown", "OPENNEKO_VERSION", "NEXT_PUBLIC_APP_VERSION"))
	line.set("commit", firstEnv("unknown", "OPENNEKO_COMMIT", "GIT_COMMIT"))
	line.set("pid", os.Getpid())
	line.set("elapsedMs", msSince(s.started))
	line.set("phase", phase)
	for i := 0; i+1 < len(kv); i += 2 {
		line.set(kv[i].(string), kv[i+1])
	}
	fmt.Fprintln(os.Stdout, jsStringify(line, false))
}

func (s *startupTrace) phase(phase string, run func() int) {
	startedAt := time.Now()
	status := run()
	s.log("workflow.http_response", "event", true, "statusCode", status)
	opID, _ := newUUID()
	s.log(phase, "durationMs", msSince(startedAt), "ok", true, "startedAt", jsTime(startedAt), "operationId", "startup:"+opID)
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// latin1 is how Node reads a raw header value: one character per byte.
func latin1(s string) string {
	runes := make([]rune, len(s))
	for i := 0; i < len(s); i++ {
		runes[i] = rune(s[i])
	}
	return string(runes)
}

// wfClientAddress is the address workflowApiFingerprint() read in Node,
// behind NodeProxy, which appended the peer to X-Forwarded-For.
func wfClientAddress(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	forwarded := peer
	if prior := r.Header.Values("X-Forwarded-For"); len(prior) > 0 {
		forwarded = strings.Join(prior, ", ") + ", " + peer
	}
	if first := strings.TrimFunc(strings.Split(latin1(forwarded), ",")[0], isJSSpace); first != "" {
		return first
	}
	if real := latin1(headerValue(r, "X-Real-Ip")); real != "" {
		return real
	}
	return "unknown"
}

func (a *API) wfKey(label string) ([]byte, error) {
	return config.SigningSecret(a.SecretsDir, "workflow-api:"+label+":v1")
}

func (a *API) wfFingerprint(r *http.Request) (string, error) {
	key, err := a.wfKey("client-fingerprint")
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(jsSlice(wfClientAddress(r), 512)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func wfBearer(header string) string {
	value := latin1(header)
	if len(value) < 7 || !strings.EqualFold(value[:6], "bearer") {
		return ""
	}
	rest := value[6:]
	trimmed := strings.TrimLeft(rest, " \t")
	if len(trimmed) == len(rest) || trimmed == "" || strings.IndexFunc(trimmed, isJSSpace) >= 0 {
		return ""
	}
	return trimmed
}

// jsParseInt is parseInt(s, 10); ok is false for NaN.
func jsParseInt(s string) (float64, bool) {
	s = strings.TrimLeftFunc(s, isJSSpace)
	sign := 1.0
	if s != "" && (s[0] == '+' || s[0] == '-') {
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
	}
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, _ := strconv.ParseFloat(s[:end], 64)
	return sign * n, true
}

// boundedEnv is boundedEnvironmentInteger().
func boundedEnv(name string, fallback, minimum, maximum int64) int64 {
	n, ok := jsParseInt(os.Getenv(name))
	if !ok || n != math.Trunc(n) || math.Abs(n) > maxSafeInt {
		return fallback
	}
	return max(minimum, min(maximum, int64(n)))
}

type wfDeployment struct {
	globalQueue, orgQueue, orgRequests, orgPolls, orgTokens, orgCost int64
}

func wfDeploymentLimits() wfDeployment {
	return wfDeployment{
		globalQueue: boundedEnv("OPENNEKO_WORKFLOW_API_GLOBAL_QUEUE_CAP", 100, 1, 1_000),
		orgQueue:    boundedEnv("OPENNEKO_WORKFLOW_API_ORG_QUEUE_CAP", 50, 1, 500),
		orgRequests: boundedEnv("OPENNEKO_WORKFLOW_API_ORG_REQUESTS_PER_MINUTE", 120, 1, 2_400),
		orgPolls:    boundedEnv("OPENNEKO_WORKFLOW_API_ORG_POLLS_PER_MINUTE", 600, 1, 4_800),
		orgTokens:   boundedEnv("OPENNEKO_WORKFLOW_API_ORG_ROLLING_TOKEN_BUDGET", 1_000_000, 1_000, 100_000_000),
		orgCost:     boundedEnv("OPENNEKO_WORKFLOW_API_ORG_ROLLING_COST_MICROS_BUDGET", 50_000_000, 1_000, 10_000_000_000),
	}
}

func wfBucket(ctx context.Context, q querier, kind, id, op string, limit int64, now time.Time) error {
	tag, err := q.Exec(ctx, `insert into workflow_api_rate_bucket (scope_kind, scope_id, operation, window_start, count, expires_at)
	   values ($1, $2, $3, date_trunc('minute', $4::timestamptz), 1, date_trunc('minute', $4::timestamptz) + interval '2 minutes')
	   on conflict (scope_kind, scope_id, operation, window_start)
	   do update set count = workflow_api_rate_bucket.count + 1 where workflow_api_rate_bucket.count < $5`,
		kind, id, op, now, limit)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return wfErr(http.StatusTooManyRequests, op+"_rate_limited", "The request rate limit has been reached.").
			retry(int64(max(1, 60-now.UTC().Second())))
	}
	return nil
}

func (a *API) wfEdgeThrottle(ctx context.Context, fingerprint string, now time.Time) error {
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if err := wfBucket(ctx, tx, "deployment", "external-workflow-api", "edge", 1_000, now); err != nil {
			return err
		}
		return wfBucket(ctx, tx, "client", fingerprint, "edge", 60, now)
	})
}

func (a *API) wfSerializable(ctx context.Context, fn func(pgx.Tx) error) error {
	for attempt := 1; ; attempt++ {
		err := pgx.BeginTxFunc(ctx, a.DB, pgx.TxOptions{IsoLevel: pgx.Serializable}, fn)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
			return err
		}
		if attempt >= wfAPISerialTries {
			return wfErr(http.StatusServiceUnavailable, "busy",
				"The workflow API is busy. Retry the request with the same Idempotency-Key.").retry(1)
		}
		pause := time.Duration(rand.Float64() * 25 * float64(int(1)<<min(attempt, 5)) * float64(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}

func wfTokenMatches(token string, verifier *string) bool {
	sum := sha256.Sum256([]byte(token))
	expected := strings.Repeat("0", 64)
	valid := verifier != nil && len(*verifier) == 64
	if valid {
		if _, err := hex.DecodeString(*verifier); err != nil {
			valid = false
		}
	}
	if valid {
		expected = *verifier
	}
	want, _ := hex.DecodeString(expected)
	matches := subtle.ConstantTimeCompare(sum[:], want) == 1
	return matches && valid
}

type wfAccess struct {
	orgID, workflowID, workflowName string
	enabled, workflowEnabled        bool
	includeTelemetry                bool
	verifier                        *string
	limits                          apiLimits
	dailyRunBudget                  *int64
	outputContract                  []byte
}

func scanWFAccess(row pgx.Row) (*wfAccess, error) {
	var v wfAccess
	dest := []any{&v.orgID, &v.workflowID, &v.enabled, &v.verifier, &v.includeTelemetry,
		&v.workflowName, &v.workflowEnabled, &v.dailyRunBudget, &v.outputContract}
	for i := range v.limits {
		dest = append(dest, &v.limits[i])
	}
	if err := row.Scan(dest...); errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &v, nil
}

const wfAccessSelect = `select access.org_id, access.workflow_id::text, access.enabled, access.token_verifier, access.include_telemetry,
	        workflow.name, workflow.enabled, workflow.daily_run_budget::bigint, workflow.output_contract, access.`

func wfAccessQuery(lock bool) string {
	q := wfAccessSelect + strings.Join(limitColumns, ", access.") + `
	   from workflow_api_access access
	   join workflow_definition workflow on workflow.id = access.workflow_id and workflow.org_id = access.org_id
	  where access.workflow_id = $1`
	if lock {
		q += ` for update of access, workflow`
	}
	return q
}

// wfVerify is verifyWorkflowApiAccessToken(); nil means a bad credential.
func (a *API) wfVerify(ctx context.Context, workflowID, token string) (*wfAccess, error) {
	if !isUUID(workflowID) {
		return nil, nil
	}
	v, err := scanWFAccess(a.DB.QueryRow(ctx, wfAccessQuery(false), workflowID))
	if err != nil || v == nil {
		return nil, err
	}
	if !wfTokenMatches(token, v.verifier) || !v.enabled {
		return nil, nil
	}
	return v, nil
}

// wfBatchContract is the compiled contract as admission stores it.
func wfBatchContract(outputContract []byte) (*jsObj, bool) {
	field, _, ok := batchColumns(outputContract)
	if !ok {
		return nil, false
	}
	parsed, err := jsParse(outputContract)
	if err != nil {
		return nil, false
	}
	root, _ := parsed.(*jsObj)
	batch, _ := root.vals["apiBatch"].(*jsObj)
	list, _ := batch.vals["columns"].([]any)
	columns := make([]any, 0, len(list))
	for _, c := range list {
		col := c.(*jsObj)
		out := obj("name", col.vals["name"], "path", col.vals["path"])
		if v, present := col.vals["default"]; present {
			out.set("default", v)
		}
		columns = append(columns, out)
	}
	return obj("version", 1, "compiled", true, "compiler", "workflow", "recordsField", field, "columns", columns), true
}

func wfScanBounds(v any, nodes *int, depth int) error {
	if depth > 10 {
		return wfErr(413, "payload_too_complex", "The JSON payload exceeds the maximum nesting depth.")
	}
	*nodes++
	if *nodes > 25_000 {
		return wfErr(413, "payload_too_complex", "The JSON payload contains too many values.")
	}
	switch t := v.(type) {
	case string:
		if len(t) > 65_536 {
			return wfErr(413, "payload_value_too_large", "A JSON string exceeds the 64 KiB value limit.")
		}
	case []any:
		if len(t) > wfAPIHardMaxRecords {
			return wfErr(413, "payload_array_too_large", "A JSON array exceeds the deployment safety limit.")
		}
		for _, item := range t {
			if err := wfScanBounds(item, nodes, depth+1); err != nil {
				return err
			}
		}
	case *jsObj:
		keys := t.ordered()
		if len(keys) > 1_000 {
			return wfErr(413, "payload_object_too_large", "A JSON object contains too many fields.")
		}
		for _, k := range keys {
			if len(k) > 256 {
				return wfErr(413, "payload_key_too_large", "A JSON field name exceeds the 256-byte limit.")
			}
			if err := wfScanBounds(t.vals[k], nodes, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func wfPreviewValue(v any, key *string, depth int) any {
	if key != nil && wfSensitiveKey.MatchString(asciiLower(*key)) {
		return "[redacted]"
	}
	if depth > 4 {
		return "[truncated]"
	}
	switch t := v.(type) {
	case string:
		if jsLength(t) > 240 {
			return jsSlice(t, 237) + "…"
		}
		return t
	case []any:
		out := []any{}
		for i, item := range t {
			if i == 5 {
				break
			}
			out = append(out, wfPreviewValue(item, nil, depth+1))
		}
		return out
	case *jsObj:
		out := &jsObj{vals: map[string]any{}}
		for i, k := range t.ordered() {
			if i == 30 {
				break
			}
			out.set(k, wfPreviewValue(t.vals[k], &k, depth+1))
		}
		return out
	}
	return v
}

// wfPreview is redactWorkflowApiInput().
func wfPreview(input *jsObj) *jsObj {
	preview := wfPreviewValue(input, nil, 0).(*jsObj)
	if len(jsStringify(preview, false)) <= wfAPIPreviewBytes {
		return preview
	}
	fields := []any{}
	for i, k := range input.ordered() {
		if i == 30 {
			break
		}
		fields = append(fields, k)
	}
	return obj("fields", fields, "note", "Input preview omitted because it exceeded the 4 KiB display limit.")
}

type wfInput struct {
	input           *jsObj
	canonical, hash string
	bytes           int
	preview         *jsObj
	records         []any
	contract        *jsObj
}

// wfValidate is validateWorkflowApiInput().
func wfValidate(value any, mode string, limits apiLimits, outputContract []byte) (*wfInput, error) {
	input, ok := value.(*jsObj)
	if !ok {
		return nil, wfErr(400, "invalid_payload", "The request body must be a JSON object.")
	}
	nodes := 0
	if err := wfScanBounds(input, &nodes, 0); err != nil {
		return nil, err
	}
	canonical := jsCanonical(input)
	if int64(len(canonical)) > limits[6] {
		return nil, wfErr(413, "request_too_large", fmt.Sprintf("The request exceeds this workflow's %d-byte limit.", limits[6]))
	}
	out := &wfInput{input: input, canonical: canonical, bytes: len(canonical)}
	if mode == "batch" {
		contract, ok := wfBatchContract(outputContract)
		if !ok {
			return nil, wfErr(422, "batch_not_ready", "This workflow does not have a compiled batch contract.")
		}
		field := contract.vals["recordsField"].(string)
		records, isArray := input.vals[field].([]any)
		if !isArray {
			return nil, wfErr(400, "invalid_batch", fmt.Sprintf("Batch input must include a '%s' array.", field))
		}
		if int64(len(records)) > limits[limBatchMax] {
			return nil, wfErr(413, "batch_record_limit", fmt.Sprintf("The batch exceeds this workflow's %d-record limit.", limits[limBatchMax]))
		}
		if len(records) == 0 {
			return nil, wfErr(400, "empty_batch", "Batch input must contain at least one record.")
		}
		for _, rec := range records {
			if _, isObject := rec.(*jsObj); !isObject {
				return nil, wfErr(400, "invalid_batch_record", "Every batch record must be a JSON object.")
			}
		}
		out.records, out.contract = records, contract
	}
	out.hash = sha256Hex(mode + "\x00" + canonical)
	out.preview = wfPreview(input)
	return out, nil
}

func wfIdempotencyKey(raw string) (string, error) {
	key := strings.TrimFunc(raw, isJSSpace)
	n := jsLength(key)
	bad := n < 8 || n > 200 || strings.IndexFunc(key, func(c rune) bool { return c <= 0x1f || c == 0x7f }) >= 0
	if bad {
		return "", wfErr(400, "invalid_idempotency_key", "Idempotency-Key must contain 8 to 200 visible characters.")
	}
	return key, nil
}

// wfWorkspacePath is resolveWorkflowApiWorkspacePath().
func wfWorkspacePath(orgID, rel string) (root, path string, err error) {
	root, err = filepath.Abs(orgAgentRoot(orgID))
	if err != nil {
		return "", "", err
	}
	path = filepath.Clean(rel)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, rel)
	}
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", "", errWFAPIEscaped
	}
	return root, path, nil
}

// wfPhysicalFile is resolveWorkflowApiPhysicalFile(): "" when the file is
// missing or a link leads out of the workspace.
func wfPhysicalFile(orgID, rel string) (string, error) {
	root, path, err := wfWorkspacePath(orgID, rel)
	if err != nil {
		return "", err
	}
	physicalRoot, errRoot := filepath.EvalSymlinks(root)
	physicalFile, errFile := filepath.EvalSymlinks(path)
	if errRoot != nil || errFile != nil {
		return "", nil
	}
	inside, err := filepath.Rel(physicalRoot, physicalFile)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) || filepath.IsAbs(inside) {
		return "", nil
	}
	return physicalFile, nil
}

func stageBatchInput(orgID, workRunID string, records []any) (string, error) {
	rel := filepath.Join("runs", workRunID, "api-batch-input.ndjson")
	_, path, err := wfWorkspacePath(orgID, rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	lines := make([]string, len(records))
	for i, rec := range records {
		lines[i] = jsStringify(rec, false) + "\n"
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(f, strings.Join(lines, "")); err != nil {
		f.Close()
		return "", err
	}
	return rel, f.Close()
}

func cleanupStaged(orgID, rel string) {
	if rel == "" {
		return
	}
	if _, path, err := wfWorkspacePath(orgID, rel); err == nil {
		_ = os.Remove(path)
	}
}

type wfAdmission struct {
	runID, status, mode, workflowID string
	replay                          bool
	expiresAt                       time.Time
}

// wfQueueAndBudgets is checkQueueAndBudgets().
func wfQueueAndBudgets(ctx context.Context, tx pgx.Tx, access *wfAccess, mode string, now time.Time) (tokens, cost int64, err error) {
	deployment := wfDeploymentLimits()
	orgTokens, orgCost := float64(deployment.orgTokens), float64(deployment.orgCost)
	var setTokens, setCost float64
	err = tx.QueryRow(ctx, `select rolling_token_budget::float8, rolling_cost_micros_budget::float8 from workflow_api_org_limits where org_id = $1`,
		access.orgID).Scan(&setTokens, &setCost)
	if err == nil {
		orgTokens, orgCost = setTokens, setCost
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, err
	}
	var global, org, workflow int64
	if err := tx.QueryRow(ctx, `select
	       count(*) filter (where status = any($1::text[])),
	       count(*) filter (where org_id = $2 and status = any($1::text[])),
	       count(*) filter (where workflow_id = $3 and status = any($1::text[]))
	     from workflow_api_admission`, wfAPIActive, access.orgID, access.workflowID).Scan(&global, &org, &workflow); err != nil {
		return 0, 0, err
	}
	if global >= deployment.globalQueue || org >= deployment.orgQueue || workflow >= access.limits[2] {
		return 0, 0, wfErr(429, "queue_full", "The workflow API queue is at capacity.").retry(15)
	}
	if access.dailyRunBudget != nil {
		var today int64
		if err := tx.QueryRow(ctx, `select count(*) from workflow_run
		     where workflow_id = $1 and created_at >= date_trunc('day', now() at time zone 'UTC') at time zone 'UTC'`,
			access.workflowID).Scan(&today); err != nil {
			return 0, 0, err
		}
		if today >= *access.dailyRunBudget {
			return 0, 0, wfErr(429, "daily_run_budget_exhausted", "The workflow's daily run budget has been exhausted.").retry(3600)
		}
	}
	if mode != "batch" {
		tokens, cost = access.limits[12], access.limits[13]
	}
	var workflowCost, usedTokens, usedCost float64
	if err := tx.QueryRow(ctx, `select
	       coalesce(sum(coalesce(actual_cost_micros, 0)) filter (
	         where workflow_id = $1 and created_at >= $3::timestamptz - ($4::bigint * interval '1 second')), 0)::float8,
	       coalesce(sum(coalesce(actual_tokens, 0)) filter (
	         where org_id = $2 and created_at >= $3::timestamptz - ($4::bigint * interval '1 second')), 0)::float8,
	       coalesce(sum(coalesce(actual_cost_micros, 0)) filter (
	         where org_id = $2 and created_at >= $3::timestamptz - ($4::bigint * interval '1 second')), 0)::float8
	     from workflow_api_admission where status <> 'expired'`,
		access.workflowID, access.orgID, now, access.limits[14]).Scan(&workflowCost, &usedTokens, &usedCost); err != nil {
		return 0, 0, err
	}
	if workflowCost >= float64(access.limits[16]) || usedTokens >= orgTokens || usedCost >= orgCost {
		return 0, 0, wfErr(429, "rolling_budget_exhausted", "The rolling workflow API usage budget has been exhausted.").
			retry(min(access.limits[14], 3_600))
	}
	return tokens, cost, nil
}

// wfAdmit is admitWorkflowApiRun().
func (a *API) wfAdmit(ctx context.Context, workflowID, token, rawKey, mode string, value any, fingerprint string, now time.Time) (*wfAdmission, error) {
	key, err := wfIdempotencyKey(rawKey)
	if err != nil {
		return nil, err
	}
	verified, err := a.wfVerify(ctx, workflowID, token)
	if err != nil {
		return nil, err
	}
	if verified == nil {
		return nil, errWFCredentials()
	}
	initial, err := wfValidate(value, mode, verified.limits, verified.outputContract)
	if err != nil {
		return nil, err
	}
	idemKey, err := a.wfKey("idempotency")
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, idemKey)
	mac.Write([]byte(workflowID + "\x00" + key))
	idempotencyHash := hex.EncodeToString(mac.Sum(nil))
	var ids [4]string
	for i := range ids {
		if ids[i], err = newUUID(); err != nil {
			return nil, err
		}
	}
	workflowRunID, workRunID, threadID, admissionID := ids[0], ids[1], ids[2], ids[3]
	staged := ""
	if mode == "batch" {
		if staged, err = stageBatchInput(verified.orgID, workRunID, initial.records); err != nil {
			return nil, err
		}
	}

	created := false
	var result *wfAdmission
	err = a.wfSerializable(ctx, func(tx pgx.Tx) error {
		created = false
		access, err := scanWFAccess(tx.QueryRow(ctx, wfAccessQuery(true), workflowID))
		if err != nil {
			return err
		}
		var verifier *string
		if access != nil {
			verifier = access.verifier
		}
		matches := wfTokenMatches(token, verifier)
		if access == nil || !access.enabled || !matches {
			return errWFCredentials()
		}
		if !access.workflowEnabled {
			return wfErr(409, "workflow_disabled", "The workflow is disabled.")
		}
		v, err := wfValidate(value, mode, access.limits, access.outputContract)
		if err != nil {
			return err
		}
		deployment := wfDeploymentLimits()
		for _, b := range []struct {
			kind, id string
			limit    int64
		}{
			{"workflow", access.workflowID, access.limits[0]},
			{"organization", access.orgID, deployment.orgRequests},
			{"client", fingerprint, max(10, access.limits[0]*2)},
		} {
			if err := wfBucket(ctx, tx, b.kind, b.id, "invoke", b.limit, now); err != nil {
				return err
			}
		}
		var prior struct {
			runID, payloadHash, mode, runStatus string
		}
		err = tx.QueryRow(ctx, `select admission.workflow_run_id::text, admission.payload_hash, admission.execution_mode, run.status
		     from workflow_api_admission admission
		     join workflow_run run on run.id = admission.workflow_run_id
		    where admission.org_id = $1 and admission.workflow_id = $2 and admission.idempotency_hash = $3`,
			access.orgID, access.workflowID, idempotencyHash).Scan(&prior.runID, &prior.payloadHash, &prior.mode, &prior.runStatus)
		if err == nil {
			if prior.payloadHash != v.hash || prior.mode != mode {
				return wfErr(409, "idempotency_conflict", "The Idempotency-Key was already used with different input.")
			}
			expires := now
			var stored *time.Time
			if err := tx.QueryRow(ctx, `select expires_at from workflow_api_admission where workflow_run_id = $1`, prior.runID).Scan(&stored); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if stored != nil {
				expires = *stored
			}
			result = &wfAdmission{runID: prior.runID, status: prior.runStatus, mode: prior.mode, replay: true,
				workflowID: access.workflowID, expiresAt: expires}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		tokens, cost, err := wfQueueAndBudgets(ctx, tx, access, mode, now)
		if err != nil {
			return err
		}
		expiresAt := now.Add(time.Duration(access.limits[17]) * time.Hour)
		progress := obj("stage", "queued")
		if mode == "batch" {
			chunks := int(math.Ceil(float64(len(v.records)) / float64(access.limits[limBatchChunk])))
			progress = obj("stage", "queued", "acceptedRows", len(v.records), "processedRows", 0, "finalRows", 0,
				"chunkCount", 0, "plannedChunks", chunks)
		}
		if _, err := tx.Exec(ctx, `insert into work_thread (id, org_id, title, channel, backend_state, created_at, updated_at, last_message_at)
		     values ($1, $2, $3, 'workflow', '{}'::jsonb, $4, $4, $4)`, threadID, access.orgID, access.workflowName, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into work_run (id, org_id, thread_id, backend, status, actor_user_id, actor_role, created_at, updated_at)
		     values ($1, $2, $3, coalesce((select config->>'backend' from llm_provider_config where org_id = $2 and scope = 'agent'), 'ax'),
		             'queued', null, 'service', $4, $4)`, workRunID, access.orgID, threadID, now); err != nil {
			return err
		}
		if err := admitRunSpend(ctx, tx, access.orgID, access.workflowID, workRunID, "api", now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into workflow_run (
		       id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, trigger_payload, execution_mode,
		       trigger_input_preview, status, progress, admitted_at, result_expires_at, created_at, updated_at
		     ) values ($1, $2, $3, $4, $5, 'api', $6::jsonb, $7, $8::jsonb, 'queued', $9::jsonb, $10, $11, $10, $10)`,
			workflowRunID, access.orgID, access.workflowID, threadID, workRunID,
			jsStringify(obj("source", "external_api", "requestBytes", v.bytes), false), mode,
			jsStringify(v.preview, false), jsStringify(progress, false), now, expiresAt); err != nil {
			return err
		}
		var payload, inputPath, contract, accepted any
		if mode == "single" {
			payload = jsStringify(v.input, false)
		} else {
			inputPath, contract, accepted = staged, jsStringify(v.contract, false), len(v.records)
		}
		if _, err := tx.Exec(ctx, `insert into workflow_api_admission (
		       id, org_id, workflow_id, workflow_run_id, idempotency_hash, payload_hash, execution_mode,
		       request_payload, input_file_path, batch_contract, request_bytes, accepted_records, status,
		       reserved_tokens, reserved_cost_micros, available_at, created_at, updated_at, expires_at
		     ) values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10::jsonb, $11, $12, 'pending', $13, $14, $15, $15, $15, $16)`,
			admissionID, access.orgID, access.workflowID, workflowRunID, idempotencyHash, v.hash, mode,
			payload, inputPath, contract, v.bytes, accepted, tokens, cost, now, expiresAt); err != nil {
			return err
		}
		if err := a.queueAPIAdmission(ctx, tx, admissionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update workflow_api_access set last_used_at = $2, updated_at = $2 where workflow_id = $1`,
			access.workflowID, now); err != nil {
			return err
		}
		created = true
		result = &wfAdmission{runID: workflowRunID, status: "queued", mode: mode, workflowID: access.workflowID, expiresAt: expiresAt}
		return nil
	})
	if err != nil {
		cleanupStaged(verified.orgID, staged)
		var over *spendExceeded
		if errors.As(err, &over) {
			a.recordBudgetBlocked(context.WithoutCancel(ctx), over)
			return nil, wfErr(429, "spend_budget_exhausted", over.message()).retry(over.retryAfter)
		}
		return nil, err
	}
	if !created {
		cleanupStaged(verified.orgID, staged)
		return result, nil
	}
	var accepted any
	if mode == "batch" {
		accepted = len(initial.records)
	}
	a.recordAudit(ctx, auditEvent{OrgID: verified.orgID, EntityKind: "workflow_run", EntityID: result.runID, Event: "api_admitted",
		Payload: map[string]any{"workflowId": workflowID, "mode": mode, "requestBytes": initial.bytes, "acceptedRecords": accepted}})
	return result, nil
}

// wfSavedURL is withSavedPublicUrl(): only the saved setting, never the
// environment.
func (a *API) wfSavedURL(ctx context.Context, path string) (string, error) {
	orgID, err := a.OrgID(ctx)
	if err != nil {
		return "", err
	}
	var saved *string
	err = a.DB.QueryRow(ctx, `select config->>'url' from llm_provider_config where org_id = $1 and scope = 'public-url'`, orgID).Scan(&saved)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if saved != nil && *saved != "" {
		return *saved + path, nil
	}
	return path, nil
}

func wfMode(r *http.Request) (string, error) {
	values, set := r.URL.Query()["mode"]
	if !set {
		return "single", nil
	}
	switch values[0] {
	case "single":
		return "single", nil
	case "batch":
		return "batch", nil
	}
	return "", wfErr(400, "invalid_mode", "mode must be 'single' or 'batch'.")
}

// utf8Text is Buffer.toString("utf8"): each maximal invalid subpart becomes
// one U+FFFD and a BOM stays.
func utf8Text(raw []byte) string {
	if utf8.Valid(raw) {
		return string(raw)
	}
	var b strings.Builder
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r != utf8.RuneError || size > 1 {
			b.WriteRune(r)
			i += size
			continue
		}
		i += invalidSubpart(raw[i:])
		b.WriteRune(utf8.RuneError)
	}
	return b.String()
}

func readWFBody(r *http.Request) (any, error) {
	tooLarge := wfErr(413, "request_too_large", "The request exceeds the deployment safety limit.")
	if declared, ok := jsParseInt(headerValue(r, "Content-Length")); ok && declared > wfAPIHardMaxBytes {
		return nil, tooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, wfAPIHardMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > wfAPIHardMaxBytes {
		return nil, tooLarge
	}
	v, err := jsParse([]byte(utf8Text(raw)))
	if err != nil {
		return nil, wfErr(400, "invalid_json", "The request body is not valid JSON.")
	}
	return v, nil
}

func (a *API) workflowAPIStart(w http.ResponseWriter, r *http.Request) {
	trace := newStartupTrace("")
	trace.phase("workflow.http_submit", func() int {
		admitted, err := a.workflowAPIAdmit(r, trace)
		if err != nil {
			return a.wfAPIFailure(w, r, err)
		}
		statusURL, err := a.wfSavedURL(r.Context(), "/api/v1/workflows/"+admitted.workflowID+"/runs/"+admitted.runID)
		if err != nil {
			return a.wfAPIFailure(w, r, err)
		}
		w.Header().Set("Location", statusURL)
		retry := "1"
		if admitted.status == "queued" {
			retry = "3"
		}
		w.Header().Set("Retry-After", retry)
		wfAPIWrite(w, http.StatusAccepted, obj("runId", admitted.runID, "status", admitted.status, "mode", admitted.mode,
			"replay", admitted.replay, "statusUrl", statusURL, "expiresAt", jsTime(admitted.expiresAt)))
		return http.StatusAccepted
	})
}

func (a *API) workflowAPIAdmit(r *http.Request, trace *startupTrace) (*wfAdmission, error) {
	ctx := r.Context()
	fingerprint, err := a.wfFingerprint(r)
	if err != nil {
		return nil, err
	}
	if err := a.wfEdgeThrottle(ctx, fingerprint, time.Now()); err != nil {
		return nil, err
	}
	mode, err := wfMode(r)
	if err != nil {
		return nil, err
	}
	if strings.TrimFunc(strings.SplitN(headerValue(r, "Content-Type"), ";", 2)[0], isJSSpace) != "application/json" {
		return nil, wfErr(415, "unsupported_media_type", "Content-Type must be application/json.")
	}
	value, err := readWFBody(r)
	if err != nil {
		return nil, err
	}
	admitted, err := a.wfAdmit(ctx, r.PathValue("workflowId"), wfBearer(headerValue(r, "Authorization")),
		latin1(headerValue(r, "Idempotency-Key")), mode, value, fingerprint, time.Now().Truncate(time.Millisecond))
	if err != nil {
		return nil, err
	}
	trace.log("workflow.api_admitted", "event", true, "workflowRunId", admitted.runID, "mode", admitted.mode,
		"replay", admitted.replay, "outcome", admitted.status)
	return admitted, nil
}

// wfAuthorizePoll is authorizeWorkflowApiPoll().
func (a *API) wfAuthorizePoll(r *http.Request) (*wfAccess, error) {
	ctx := r.Context()
	fingerprint, err := a.wfFingerprint(r)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if err := a.wfEdgeThrottle(ctx, fingerprint, now); err != nil {
		return nil, err
	}
	verified, err := a.wfVerify(ctx, r.PathValue("workflowId"), wfBearer(headerValue(r, "Authorization")))
	if err != nil {
		return nil, err
	}
	if verified == nil {
		return nil, errWFCredentials()
	}
	poll := verified.limits[1]
	err = a.wfSerializable(ctx, func(tx pgx.Tx) error {
		if err := wfBucket(ctx, tx, "workflow", verified.workflowID, "poll", poll, now); err != nil {
			return err
		}
		if err := wfBucket(ctx, tx, "organization", verified.orgID, "poll", wfDeploymentLimits().orgPolls, now); err != nil {
			return err
		}
		return wfBucket(ctx, tx, "client", fingerprint, "poll", max(30, poll*2), now)
	})
	if err != nil {
		return nil, err
	}
	return verified, nil
}

func wfRunNotFound() *wfAPIError { return wfErr(404, "run_not_found", "Workflow run not found.") }
func wfExpired() *wfAPIError {
	return wfErr(410, "result_expired", "The retained API result has expired.")
}

func jsonbValue(raw []byte) (any, error) {
	if raw == nil {
		return nil, nil
	}
	return jsParse(raw)
}

func jsTimeOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return jsTime(*t)
}

func (a *API) workflowAPIRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	newStartupTrace(runID).phase("workflow.http_status", func() int {
		body, err := a.workflowAPIRunBody(r, runID)
		if err != nil {
			return a.wfAPIFailure(w, r, err)
		}
		if body.vals["retryAfterSeconds"] != nil {
			w.Header().Set("Retry-After", "3")
		}
		wfAPIWrite(w, http.StatusOK, body)
		return http.StatusOK
	})
}

func (a *API) workflowAPIRunBody(r *http.Request, runID string) (*jsObj, error) {
	ctx := r.Context()
	verified, err := a.wfAuthorizePoll(r)
	if err != nil {
		return nil, err
	}
	if !isUUID(runID) {
		return nil, wfRunNotFound()
	}
	var row struct {
		id, workflowID, status                 string
		mode, failure, errorCode, artifactPath *string
		created                                time.Time
		admitted, started, finished, expires   *time.Time
		progress, telemetry, terminal          []byte
	}
	err = a.DB.QueryRow(ctx, `select run.id::text, run.workflow_id::text, run.execution_mode, run.status,
	        run.created_at, run.admitted_at, run.started_at, run.finished_at, run.result_expires_at,
	        run.progress, run.telemetry_summary, run.terminal_result, run.result_artifact_path, run.error,
	        admission.last_error_code
	   from workflow_run run
	   join workflow_api_admission admission on admission.workflow_run_id = run.id
	  where run.id = $1 and run.workflow_id = $2 and run.org_id = $3`, runID, r.PathValue("workflowId"), verified.orgID).
		Scan(&row.id, &row.workflowID, &row.mode, &row.status, &row.created, &row.admitted, &row.started, &row.finished,
			&row.expires, &row.progress, &row.telemetry, &row.terminal, &row.artifactPath, &row.failure, &row.errorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, wfRunNotFound()
	}
	if err != nil {
		return nil, err
	}
	if row.expires == nil || !row.expires.After(time.Now()) {
		return nil, wfExpired()
	}
	if row.admitted == nil {
		return nil, errors.New("workflow run " + row.id + " has no admitted_at")
	}
	progress, err := jsonbValue(row.progress)
	if err != nil {
		return nil, err
	}
	if progress == nil {
		progress = &jsObj{}
	}
	var mode any
	if row.mode != nil {
		mode = *row.mode
	}
	body := obj("runId", row.id, "workflowId", row.workflowID, "mode", mode, "status", row.status,
		"createdAt", jsTime(row.created), "admittedAt", jsTime(*row.admitted), "startedAt", jsTimeOrNil(row.started),
		"finishedAt", jsTimeOrNil(row.finished), "expiresAt", jsTime(*row.expires), "progress", progress)
	if verified.includeTelemetry {
		telemetry, err := jsonbValue(row.telemetry)
		if err != nil {
			return nil, err
		}
		body.set("telemetry", telemetry)
	}
	terminal := wfAPITerminal[row.status]
	var result, artifact, failure, retryAfter any
	if terminal {
		if result, err = jsonbValue(row.terminal); err != nil {
			return nil, err
		}
		if row.artifactPath != nil && *row.artifactPath != "" {
			url, err := a.wfSavedURL(ctx, "/api/v1/workflows/"+row.workflowID+"/runs/"+row.id+"/artifact")
			if err != nil {
				return nil, err
			}
			artifact = obj("url", url)
		}
		if (row.failure != nil && *row.failure != "") || (row.errorCode != nil && *row.errorCode != "") {
			code, message := "workflow_failed", "Workflow execution failed."
			if row.errorCode != nil {
				code = *row.errorCode
			}
			if row.failure != nil {
				message = *row.failure
			}
			failure = obj("code", code, "message", message)
		}
	} else {
		retryAfter = 3
	}
	body.set("result", result)
	body.set("artifact", artifact)
	body.set("error", failure)
	body.set("retryAfterSeconds", retryAfter)
	return body, nil
}

func (a *API) workflowAPIArtifact(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	trace := newStartupTrace(runID)
	trace.phase("workflow.http_artifact", func() int {
		f, size, err := a.workflowAPIArtifactFile(r, runID)
		if err != nil {
			return a.wfAPIFailure(w, r, err)
		}
		defer f.Close()
		h := w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		h.Set("Content-Disposition", `attachment; filename="workflow-`+runID+`.csv"`)
		h.Set("Cache-Control", "no-store, private")
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return http.StatusOK
		}
		started := time.Now()
		n, err := io.Copy(w, f)
		outcome := "completed"
		if err != nil {
			outcome = "cancelled"
		}
		trace.log("workflow.artifact_stream", "event", true, "outcome", outcome, "durationMs", msSince(started), "bytesRead", n)
		return http.StatusOK
	})
}

func (a *API) workflowAPIArtifactFile(r *http.Request, runID string) (*os.File, int64, error) {
	verified, err := a.wfAuthorizePoll(r)
	if err != nil {
		return nil, 0, err
	}
	if !isUUID(runID) {
		return nil, 0, wfRunNotFound()
	}
	var rel *string
	var expires *time.Time
	var status string
	err = a.DB.QueryRow(r.Context(), `select result_artifact_path, result_expires_at, status from workflow_run
	  where id = $1 and workflow_id = $2 and org_id = $3`, runID, r.PathValue("workflowId"), verified.orgID).Scan(&rel, &expires, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, wfRunNotFound()
	}
	if err != nil {
		return nil, 0, err
	}
	if expires == nil || !expires.After(time.Now()) {
		return nil, 0, wfExpired()
	}
	if status != "completed" || rel == nil || *rel == "" {
		return nil, 0, wfErr(404, "artifact_not_ready", "The workflow artifact is not available.")
	}
	path, err := wfPhysicalFile(verified.orgID, *rel)
	if err != nil {
		return nil, 0, err
	}
	unavailable := wfErr(410, "artifact_unavailable", "The workflow artifact is unavailable.")
	if path == "" {
		return nil, 0, unavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, unavailable
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > verified.limits[8] {
		f.Close()
		return nil, 0, unavailable
	}
	return f, info.Size(), nil
}
