package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/embedding"
	"github.com/open-neko/openneko/apps/ax-harness/sandbox"
)

const queueBusinessProfile = "business_profile_build"

func (a *API) serveSocket() string { return os.Getenv("OPENNEKO_SERVE_SOCKET") }

func (a *API) hostCalls() llm.HostCalls {
	gateway := a.Gateway
	if gateway == nil {
		gateway = llm.ServeGateway(a.serveSocket())
	}
	token := a.GoogleToken
	if token == nil {
		token = llm.GoogleToken
	}
	return llm.HostCalls{DB: a.DB, SecretsDir: a.SecretsDir, Gateway: gateway, Token: token, Spend: a.recordHostSpend}
}

// recordHostSpend is recordUsageSpend() with unpriced "zero" for a host call:
// no reservation and no workflow, so only the org budgets can warn.
func (a *API) recordHostSpend(ctx context.Context, orgID, task string, served llm.RoutedModel, usage llm.Usage) {
	micros, priced := 0.0, "unpriced"
	if usage.CostStatus != "unknown" && usage.CostUSD != nil {
		micros, priced = usdToMicros(*usage.CostUSD), "estimated"
		if usage.CostStatus == "included" {
			priced = "included"
		}
	}
	var tokens *int64
	if usage.Coverage != "unavailable" {
		tokens = &usage.Total
	}
	now := time.Now()
	if _, err := a.DB.Exec(ctx, `insert into spend_ledger (
	     org_id, reservation_id, work_run_id, workflow_id, source, provider, model,
	     cost_micros, tokens, priced, cost_source, pricing_version, created_at, task
	   ) values ($1, null, null, null, 'system', $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		orgID, served.Settings.Provider, served.Settings.Model, int64(micros), tokens, priced,
		nullable(usage.CostSource), nullable(usage.PricingVersion), now, task); err != nil {
		log.Printf("[spend] host call not recorded: %v", err)
		return
	}
	if err := a.checkSpendWarnings(ctx, orgID, "", now); err != nil {
		log.Printf("[spend] alert check failed: %v", err)
	}
}

// checkSpendWarnings is checkSpendWarnings() in packages/llm/src/spend/alerts.ts.
func (a *API) checkSpendWarnings(ctx context.Context, orgID, workflowID string, now time.Time) error {
	limits, err := spendLimits(ctx, a.DB, orgID, workflowID)
	if err != nil {
		if strings.HasPrefix(err.Error(), "No spend limits") {
			return nil
		}
		return err
	}
	warnPercent := 80.0
	if err := noRows(a.DB.QueryRow(ctx, `select coalesce(warn_percent, 80)::float8 from spend_limit where org_id = $1 and workflow_id is null limit 1`, orgID).Scan(&warnPercent)); err != nil {
		return err
	}
	var workflowName *string
	if workflowID != "" {
		if err := noRows(a.DB.QueryRow(ctx, `select name from workflow_definition where org_id = $1 and id = $2::uuid`, orgID, workflowID).Scan(&workflowName)); err != nil {
			return err
		}
	}
	hourStart := now.UTC().Truncate(time.Hour)
	dayStart := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	type budget struct {
		budget, label, workflowID string
		limit                     float64
		since, resetsAt           time.Time
		seconds                   int
	}
	budgets := []budget{
		{"org_hourly", "hourly budget for this organization", "", limits[1], hourStart, hourStart.Add(time.Hour), 3_600},
		{"org_daily", "daily budget for this organization", "", limits[2], dayStart, dayStart.Add(24 * time.Hour), 86_400},
	}
	if workflowID != "" {
		budgets = append(budgets,
			budget{"workflow_hourly", "hourly budget for the workflow", workflowID, limits[3], hourStart, hourStart.Add(time.Hour), 3_600},
			budget{"workflow_daily", "daily budget for the workflow", workflowID, limits[4], dayStart, dayStart.Add(24 * time.Hour), 86_400})
	}
	for _, b := range budgets {
		spent, held, err := spendBreakdown(ctx, a.DB, orgID, b.workflowID, b.since)
		if err != nil {
			return err
		}
		committed := spent + held
		threshold := math.Ceil(b.limit * warnPercent / 100)
		if committed < threshold {
			continue
		}
		label := b.label
		if workflowName != nil && *workflowName != "" {
			label = strings.Replace(label, "the workflow", *workflowName, 1)
		}
		limitUSD := jsToFixed2(microsToUSD(b.limit))
		message := fmt.Sprintf("Spend is at %d%% of the $%s %s ($%s so far). New runs stop at $%s. The budget resets at %s UTC.",
			int64(math.Floor(committed/b.limit*100)), limitUSD, label, jsToFixed2(microsToUSD(committed)), limitUSD, b.resetsAt.UTC().Format("15:04"))
		details := struct {
			Budget         string  `json:"budget"`
			LimitMicros    float64 `json:"limitMicros"`
			WarnPercent    float64 `json:"warnPercent"`
			Message        string  `json:"message"`
			WindowStart    string  `json:"windowStart"`
			ObservedMicros float64 `json:"observedMicros"`
			ThresholdMicro float64 `json:"thresholdMicros"`
		}{b.budget, b.limit, warnPercent, message, jsDate(b.since), committed, threshold}
		raw, err := json.Marshal(details)
		if err != nil {
			return err
		}
		subject := "org"
		if b.workflowID != "" {
			subject = "workflow:" + b.workflowID
		}
		a.raiseSpendAlert(ctx, orgID, "spend.budget_warning", subject, committed, threshold, b.seconds, details.WindowStart, raw)
	}
	return nil
}

func (a *API) settingsProviderTest(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	body := readJSONObject(r)
	if body == nil {
		sendError(w, http.StatusInternalServerError, "The request body is not a JSON object.")
		return
	}
	if scope, _ := body["scope"].(string); scope == "" {
		sendError(w, http.StatusBadRequest, "missing draft scope")
		return
	}
	draft := llm.ParseDraft(body)
	ctx := r.Context()
	var provider, model, source string
	var err error
	if draft.Scope == "primary" {
		var s llm.Settings
		if s, err = llm.PrimaryFromDraft(ctx, a.DB, a.SecretsDir, act.OrgID, draft); err == nil {
			provider, model, source = s.Provider, s.Model, s.Source
			err = a.hostCalls().TestPrimary(ctx, act.OrgID, s)
		}
	} else {
		var research llm.ResearchSettings
		if research, err = llm.ResearchFromDraft(ctx, a.DB, a.SecretsDir, act.OrgID, draft); err == nil {
			provider, model, source = research.Provider, research.Model, research.Source
			if research.Off() {
				provider, model = "disabled", ""
			} else {
				err = llm.TestResearch(ctx, http.DefaultClient, a.researchURL(), research)
			}
		}
	}
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": provider, "model": model, "source": source})
}

func (a *API) researchURL() string {
	if a.ResearchURL != "" {
		return a.ResearchURL
	}
	return llm.PerplexityURL
}

type serviceStatus struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	State     string  `json:"state"`
	Where     *string `json:"where"`
	Detail    string  `json:"detail"`
	CheckedAt string  `json:"checkedAt"`
}

type serviceSettings struct {
	url, where string
	secrets    []string
}

type serviceFailure struct {
	error
	where string
}

type externalService struct {
	id, label, whenOff string
	timeout            time.Duration
	settings           func(ctx context.Context, orgID string) (*serviceSettings, error)
	check              func(ctx context.Context, orgID string, s *serviceSettings) (detail, where string, err error)
}

// serviceWhere is serviceWhere() in packages/llm/src/external-services.ts.
func serviceWhere(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || (u.Scheme != "unix" && u.Host == "" && u.Opaque == "") {
		return "an address that is not a URL"
	}
	if u.Scheme == "unix" {
		return "unix:" + u.Path
	}
	return u.Scheme + "://" + u.Host
}

func (a *API) externalServices() []externalService {
	return []externalService{
		{
			id: "inference", label: "Inference", timeout: 30 * time.Second,
			whenOff: "No model is set up. Agent turns and summaries stop until an admin adds one in Admin, Settings, Models.",
			settings: func(ctx context.Context, orgID string) (*serviceSettings, error) {
				s, err := llm.PrimarySettings(ctx, a.DB, a.SecretsDir, orgID)
				if err != nil || s.Source == "default" || !s.Enabled {
					return nil, err
				}
				out := &serviceSettings{where: s.Provider + " · " + s.Model}
				if raw, ok := s.Config["url"].(string); ok && strings.TrimSpace(raw) != "" {
					out.url = strings.TrimSpace(raw)
					out.where += " · " + serviceWhere(out.url)
				}
				for _, v := range s.Secrets {
					out.secrets = append(out.secrets, v)
				}
				return out, nil
			},
			check: func(ctx context.Context, orgID string, _ *serviceSettings) (string, string, error) {
				s, err := llm.PrimarySettings(ctx, a.DB, a.SecretsDir, orgID)
				if err == nil {
					err = a.hostCalls().TestPrimary(ctx, orgID, s)
				}
				return "The default model answered a test prompt.", "", err
			},
		},
		{
			id: "embedding", label: "Embedding", timeout: 10 * time.Second,
			whenOff: "Search uses Postgres full-text search.",
			settings: func(context.Context, string) (*serviceSettings, error) {
				c := embeddingConfig()
				if c == nil {
					return nil, nil
				}
				out := &serviceSettings{url: c.URL, where: serviceWhere(c.URL) + " · " + c.Model}
				if c.key != "" {
					out.secrets = []string{c.key}
				}
				return out, nil
			},
			check: func(ctx context.Context, _ string, _ *serviceSettings) (string, string, error) {
				c := embeddingConfig()
				embed := a.Embedder
				if embed == nil {
					embed = llm.ServeEmbedder(a.serveSocket())
				}
				result, err := embed(ctx, embedding.Request{URL: c.URL, Model: c.Model, Dimensions: c.Dimensions, Input: []string{"OpenNeko status check"}}, c.key)
				if err != nil {
					return "", "", err
				}
				if len(result.Vectors) != 1 || !validVector(result.Vectors[0], c.Dimensions) {
					return "", "", errors.New("embedding service returned an incompatible vector")
				}
				return fmt.Sprintf("The service returned a vector of %d numbers.", len(result.Vectors[0])), "", nil
			},
		},
		{
			id: "sandbox", label: "Sandbox", timeout: 5 * time.Second,
			whenOff: "The terminal and the script skills are off.",
			settings: func(context.Context, string) (*serviceSettings, error) {
				c := sandbox.ConfigFromEnv(os.Getenv)
				if !c.Enabled() {
					return nil, nil
				}
				out := &serviceSettings{url: c.URL, where: serviceWhere(c.URL)}
				if c.Key != "" {
					out.secrets = []string{c.Key}
				}
				return out, nil
			},
			check: func(ctx context.Context, _ string, _ *serviceSettings) (string, string, error) {
				return "The sandbox accepted the key.", "", sandbox.Probe(ctx, sandbox.ConfigFromEnv(os.Getenv))
			},
		},
		{
			id: "file-store", label: "File store", timeout: 10 * time.Second,
			whenOff: "File search is off: OPENNEKO_SERVE_URL is not set.",
			settings: func(context.Context, string) (*serviceSettings, error) {
				u := strings.TrimRight(strings.TrimSpace(os.Getenv("OPENNEKO_SERVE_URL")), "/")
				if u == "" {
					return nil, nil
				}
				return &serviceSettings{url: u, where: "openneko serve"}, nil
			},
			check: checkFileStore,
		},
	}
}

func checkFileStore(ctx context.Context, _ string, s *serviceSettings) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/healthz", nil)
	if err != nil {
		return "", "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", errors.New("openneko serve did not answer")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", "", fmt.Errorf("openneko serve answered HTTP %d", res.StatusCode)
	}
	var health struct {
		FileStore      string `json:"file_store"`
		FileStoreOK    *bool  `json:"file_store_ok"`
		FileStoreError string `json:"file_store_error"`
		FileStoreWhere string `json:"file_store_where"`
	}
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		return "", "", err
	}
	where := health.FileStoreWhere
	if where == "" {
		where = health.FileStore
	}
	if where == "" {
		where = "openneko serve"
	}
	if health.FileStoreOK != nil && !*health.FileStoreOK {
		msg := health.FileStoreError
		if msg == "" {
			msg = "the file store failed its check"
		}
		return "", "", serviceFailure{errors.New(msg), where}
	}
	if health.FileStore == "s3" {
		return "Files are in the bucket.", where, nil
	}
	return "Files are in the local folder. No bucket is set.", where, nil
}

type embeddingSettings struct {
	URL, Model string
	Dimensions int
	key        string
}

// embeddingConfig is embeddingConfig() in packages/llm/src/embedding.ts.
func embeddingConfig() *embeddingSettings {
	u := strings.TrimRight(strings.TrimSpace(os.Getenv("NEKO_EMBEDDING_URL")), "/")
	model := strings.TrimSpace(os.Getenv("NEKO_EMBEDDING_MODEL"))
	if u == "" || model == "" {
		return nil
	}
	c := &embeddingSettings{URL: u, Model: model, key: strings.TrimSpace(os.Getenv("NEKO_EMBEDDING_API_KEY"))}
	if n, ok := leadingInt(strings.TrimSpace(os.Getenv("NEKO_EMBEDDING_DIMENSIONS"))); ok && n > 0 && n <= embedding.MaxDimensions {
		c.Dimensions = n
	}
	return c
}

func leadingInt(s string) (int, bool) {
	n, digits := 0, 0
	for i, c := range s {
		if i == 0 && (c == '+' || c == '-') {
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	if strings.HasPrefix(s, "-") {
		n = -n
	}
	return n, true
}

func validVector(v []float64, dimensions int) bool {
	return len(v) > 0 && len(v) <= embedding.MaxDimensions && (dimensions == 0 || len(v) == dimensions)
}

// plainError is plain(): the message with each secret of four or more
// characters redacted, cut to 300 UTF-16 units.
func plainError(err error, secrets []string) string {
	text := strings.TrimSpace(err.Error())
	if text == "" {
		text = "The check failed."
	}
	for _, s := range secrets {
		if len(utf16.Encode([]rune(s))) >= 4 {
			text = strings.ReplaceAll(text, s, "[redacted]")
		}
	}
	if units := utf16.Encode([]rune(text)); len(units) > 300 {
		text = string(utf16.Decode(units[:297])) + "..."
	}
	return text
}

func statusOf(ctx context.Context, orgID string, s externalService) serviceStatus {
	out := serviceStatus{ID: s.id, Label: s.label}
	stamp := func() string { return jsDate(time.Now()) }
	settings, err := s.settings(ctx, orgID)
	if err != nil {
		out.State, out.Detail, out.CheckedAt = "failing", plainError(err, nil), stamp()
		return out
	}
	if settings == nil {
		out.State, out.Detail, out.CheckedAt = "off", s.whenOff, stamp()
		return out
	}
	checkCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	type answer struct {
		detail, where string
		err           error
	}
	done := make(chan answer, 1)
	go func() {
		detail, where, err := s.check(checkCtx, orgID, settings)
		done <- answer{detail, where, err}
	}()
	var got answer
	select {
	case got = <-done:
	case <-checkCtx.Done():
		got = answer{err: checkCtx.Err()}
	}
	where := settings.where
	if got.err == nil {
		if got.where != "" {
			where = got.where
		}
		out.State, out.Where, out.Detail, out.CheckedAt = "ok", &where, got.detail, stamp()
		return out
	}
	var failure serviceFailure
	if errors.As(got.err, &failure) && failure.where != "" {
		where = failure.where
	}
	out.State, out.Where, out.CheckedAt = "failing", &where, stamp()
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
		out.Detail = fmt.Sprintf("No answer within %d seconds.", int(s.timeout/time.Second))
	} else {
		out.Detail = plainError(got.err, settings.secrets)
	}
	return out
}

type servicesEntry struct {
	at    time.Time
	done  chan struct{}
	value []serviceStatus
}

var (
	servicesMu    sync.Mutex
	servicesCache = map[string]*servicesEntry{}
)

const servicesCacheFor = time.Minute

// serviceStatuses keeps results for a minute per org, because the inference
// and embedding checks each make a paid call.
func (a *API) serviceStatuses(ctx context.Context, orgID string, refresh bool) []serviceStatus {
	servicesMu.Lock()
	hit := servicesCache[orgID]
	if !refresh && hit != nil && time.Since(hit.at) < servicesCacheFor {
		servicesMu.Unlock()
		<-hit.done
		return hit.value
	}
	entry := &servicesEntry{at: time.Now(), done: make(chan struct{})}
	servicesCache[orgID] = entry
	servicesMu.Unlock()
	services := a.externalServices()
	entry.value = make([]serviceStatus, len(services))
	var wg sync.WaitGroup
	for i, s := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry.value[i] = statusOf(context.WithoutCancel(ctx), orgID, s)
		}()
	}
	wg.Wait()
	close(entry.done)
	return entry.value
}

func (a *API) adminServices(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	statuses := a.serviceStatuses(r.Context(), act.OrgID, r.URL.Query().Get("refresh") == "1")
	sendJSON(w, http.StatusOK, map[string]any{"services": statuses})
}

const agentRuntimeUnavailable = "The agent runtime (openneko serve) is unavailable. Restart this OpenNeko stack, wait for `openneko status` to show openneko-serve running, then try again. If it still fails, inspect `openneko logs openneko-serve`."

// agentRuntimeReady is verifyAgentRuntimeReady(): serve answers on its
// socket and the org's model keys load.
func (a *API) agentRuntimeReady(ctx context.Context, orgID string) error {
	if socket := a.serveSocket(); socket != "" {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var d net.Dialer
		conn, err := d.DialContext(dialCtx, "unix", socket)
		if err != nil {
			if dialCtx.Err() != nil {
				return errors.New("openneko serve did not answer within 5s")
			}
			return fmt.Errorf("openneko serve is unavailable: %w", err)
		}
		_ = conn.Close()
	}
	token := a.GoogleToken
	if token == nil {
		token = llm.GoogleToken
	}
	_, err := llm.AgentModelKeys(ctx, a.DB, a.SecretsDir, orgID, token)
	return err
}

func (a *API) onboardingSubmit(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		CompanyName          any      `json:"companyName"`
		CompanyNote          *string  `json:"companyNote"`
		FiscalYearStartMonth *int16   `json:"fiscalYearStartMonth"`
		ActiveSeats          []string `json:"activeSeats"`
		Priorities           []string `json:"priorities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		sendError(w, http.StatusBadRequest, "The request body does not match the onboarding form.")
		return
	}
	name, _ := body.CompanyName.(string)
	name = strings.TrimSpace(name)
	if name == "" {
		sendError(w, http.StatusBadRequest, "companyName is required")
		return
	}
	ctx := r.Context()
	orgID := act.OrgID
	setUp, err := llm.SetupComplete(ctx, a.DB, a.SecretsDir, orgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !setUp {
		sendJSON(w, http.StatusConflict, map[string]string{
			"error": "The primary model provider is not configured. Open Agent settings, save a provider, then try again.",
			"code":  "primary_provider_unavailable",
		})
		return
	}
	if err := a.agentRuntimeReady(ctx, orgID); err != nil {
		log.Printf("[agent-runtime-readiness] openneko serve check failed: %v", err)
		sendJSON(w, http.StatusServiceUnavailable, map[string]string{"error": agentRuntimeUnavailable, "code": "agent_runtime_unavailable"})
		return
	}
	if body.ActiveSeats == nil {
		body.ActiveSeats = []string{}
	}
	if body.Priorities == nil {
		body.Priorities = []string{}
	}
	if err := a.ensureQueue(ctx, queueBusinessProfile); err != nil {
		a.fail(w, r, err)
		return
	}
	var jobID, inflight string
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, "openneko.onboarding:"+orgID); err != nil {
			return err
		}
		if err := noRows(tx.QueryRow(ctx, `select id::text from processing_job
		    where org_id = $1 and kind = 'business_profile_build' and status in ('queued', 'running') limit 1`, orgID).Scan(&inflight)); err != nil || inflight != "" {
			return err
		}
		if _, err := tx.Exec(ctx, `update organization set name = $2, updated_at = now() where id = $1`, orgID, name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from onboarding_wizard where org_id = $1`, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into onboarding_wizard (org_id, company_note, fiscal_year_start_month, active_seats, priorities, step, submitted_at)
		    values ($1, $2, $3, $4, $5, 'submitting', now())`, orgID, body.CompanyNote, body.FiscalYearStartMonth, body.ActiveSeats, body.Priorities); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger)
		    values ($1, 'business_profile_build', 'queued', 'wizard_submit') returning id::text`, orgID).Scan(&jobID)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if inflight != "" {
		sendJSON(w, http.StatusOK, map[string]any{"ok": true, "jobId": inflight})
		return
	}
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		_, err := a.enqueueWith(ctx, tx, queueBusinessProfile, map[string]string{"processingJobId": jobID, "orgId": orgID}, 0)
		return err
	})
	if err != nil {
		msg := "enqueue failed: " + err.Error()
		if _, uerr := a.DB.Exec(ctx, `update processing_job set status = 'failed', error = $2, finished_at = now(), updated_at = now() where id = $1`, jobID, msg); uerr != nil {
			log.Printf("[onboarding] could not mark job %s failed: %v", jobID, uerr)
		}
		sendError(w, http.StatusInternalServerError, msg)
		return
	}
	sendJSON(w, http.StatusOK, map[string]string{"jobId": jobID})
}

// EmbeddingModel is embeddingModel() in packages/llm/src/embedding.ts.
func (a *API) EmbeddingModel() string {
	c := embeddingConfig()
	if c == nil {
		return ""
	}
	if c.Dimensions > 0 {
		return fmt.Sprintf("%s@%d", c.Model, c.Dimensions)
	}
	return c.Model
}

// EmbedText is embedText(text, 150_000) in packages/llm/src/embedding.ts.
func (a *API) EmbedText(ctx context.Context, text string) ([]float64, error) {
	c := embeddingConfig()
	if c == nil {
		return nil, errors.New("embedding is off: NEKO_EMBEDDING_URL and NEKO_EMBEDDING_MODEL are not both set")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("embedText: text is empty")
	}
	if len(utf16.Encode([]rune(text))) > 32000 {
		return nil, errors.New("embedText: text exceeds 32000 characters")
	}
	embed := a.Embedder
	if embed == nil {
		embed = llm.ServeEmbedder(a.serveSocket())
	}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	result, err := embed(ctx, embedding.Request{URL: c.URL, Model: c.Model, Dimensions: c.Dimensions, Input: []string{text}}, c.key)
	if err != nil {
		return nil, err
	}
	if len(result.Vectors) != 1 || !validVector(result.Vectors[0], c.Dimensions) {
		return nil, errors.New("embedding service returned an incompatible vector")
	}
	return result.Vectors[0], nil
}
