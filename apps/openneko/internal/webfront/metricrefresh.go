package webfront

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed metricprompt.txt
var metricPromptText string

var metricSections = parseTurnSections(metricPromptText)

type metricCard struct {
	Question          string
	Role, Slug, Title string
	Why, ChartHint    *string
}

var roleSeedSearches = map[string][]string{
	"CEO": {"revenue by month", "top customers", "channel mix"},
	"CFO": {"margin by product", "accounts receivable aging", "cost trends"},
	"COO": {"order fulfillment lead time", "inventory below reorder point", "throughput"},
	"CRO": {"sales by rep", "pipeline by stage", "quota attainment"},
	"CMO": {"customer acquisition by segment", "retention cohorts", "campaign performance"},
	"CIO": {"data freshness", "integration volumes", "system activity"},
	"CPO": {"product adoption", "feature usage", "activation funnel"},
}

// discoveryPathwaysSection is buildDiscoveryPathwaysSection() over
// getDiscoveryPathways(): hub tables and query templates from insights.json,
// and seed searches for the role.
func discoveryPathwaysSection(role, intent, insightsJSON string) string {
	var entrypoints, templates []string
	if v, err := jsParse([]byte(insightsJSON)); err == nil {
		if o, ok := v.(*jsObj); ok && o != nil {
			hubs, _ := lookup(o, "hub_tables").([]any)
			for _, h := range hubs {
				name := lookup(asObj(h), "table")
				if nullish(name) {
					name = lookup(asObj(h), "name")
				}
				if nullish(name) {
					name = ""
				}
				if jsTruthy(name) {
					entrypoints = append(entrypoints, jsTemplate(name))
				}
			}
			entrypoints = entrypoints[:min(len(entrypoints), 12)]
			list, _ := lookup(o, "query_templates").([]any)
			for _, t := range list[:min(len(list), 8)] {
				title := lookup(asObj(t), "title")
				if !jsTruthy(title) {
					continue
				}
				note := lookup(asObj(t), "description")
				if nullish(note) {
					note = lookup(asObj(t), "note")
				}
				line := "- " + jsTemplate(title)
				if !nullish(note) && jsTruthy(note) {
					line += " — " + jsTemplate(note)
				}
				templates = append(templates, line)
			}
		}
	}
	var seeds []string
	if intent != "" {
		seeds = append(seeds, intent)
	}
	seeds = append(seeds, roleSeedSearches[role]...)
	seeds = seeds[:min(len(seeds), 5)]
	if len(entrypoints) == 0 && len(seeds) == 0 && len(templates) == 0 {
		return ""
	}
	lines := []string{"<discovery-pathways>"}
	if len(entrypoints) > 0 {
		lines = append(lines, "Start discovery from these hub tables (inspect them before searching wider):")
		for _, e := range entrypoints {
			lines = append(lines, "- "+e)
		}
		lines = append(lines, "")
	}
	if len(seeds) > 0 {
		lines = append(lines, "Seed searches worth running first for this role:")
		for _, s := range seeds {
			lines = append(lines, `- "`+s+`"`)
		}
		lines = append(lines, "")
	}
	if len(templates) > 0 {
		lines = append(lines, "Curated query patterns (prefer adapting one over writing from scratch):")
		lines = append(lines, templates...)
	}
	return strings.Join(append(lines, "</discovery-pathways>"), "\n")
}

func asObj(v any) *jsObj {
	o, _ := v.(*jsObj)
	return o
}

func optText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// buildMetricPrompt gives the prompt of testdata/metricrefresh.json, with the
// discovery pathways. The job has no files tool, so the memory section
// leaves out the files text.
func buildMetricPrompt(c metricCard, k knowledgePack, memoryContext string) string {
	input := &jsObj{}
	if c.Question != "" {
		input.set("userQuestion", c.Question)
	}
	input.set("cardTitle", c.Title)
	input.set("cardRationale", optText(c.Why))
	input.set("cardRole", c.Role)
	input.set("cardSlug", c.Slug)
	input.set("chartHint", optText(c.ChartHint))
	prompt := strings.Join([]string{
		metricSections["metric_role"],
		readOnlyMemorySection(true, false, memoryContext),
		dataAccessSectionAs(k, true, false, "service credential"),
		metricSections["metric_time_window"],
		metricSections["metric_mood_and_chart"],
		metricSections["metric_hard_constraints"],
		metricSections["metric_output_contract"],
		"<input>\nThe card you must answer:\n\n" + jsStringifyIndent(input, "  ") + "\n</input>",
	}, "\n\n")
	if p := discoveryPathwaysSection(c.Role, c.Title, k.Insights); p != "" {
		prompt += "\n\n" + p
	}
	return prompt
}

// normalizeMetricReply is the agent loop's check and the result shape of
// runMetricAgent().
func normalizeMetricReply(text string) (*jsObj, error) {
	parsed, err := parseAgentJSON(text)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, errors.New("Cannot read properties of null (reading 'headlineMetric')")
	}
	out := asObj(parsed)
	if !jsTruthy(lookup(out, "headlineMetric")) && !jsTruthy(lookup(out, "insightText")) {
		return nil, errors.New("JSON parsed but headlineMetric and insightText are both empty")
	}
	text2 := func(key string) string {
		v := lookup(out, key)
		if nullish(v) {
			return ""
		}
		return jsValueString(v)
	}
	chart, ok := lookup(out, "chartData").([]any)
	if !ok {
		chart = []any{}
	}
	var baseline any
	if b, ok := lookup(out, "baselineValue").(float64); ok && !math.IsInf(b, 0) && !math.IsNaN(b) {
		baseline = b
	}
	tw := asObj(lookup(out, "timeWindow"))
	bound := func(key string) any {
		v := lookup(tw, key)
		if v == nil || !jsTruthy(v) {
			return nil
		}
		return jsValueString(v)
	}
	label := lookup(tw, "label")
	if nullish(label) {
		label = ""
	}
	return jsObjOf("reasoning", text2("reasoning"), "headlineMetric", text2("headlineMetric"), "headlineLabel", text2("headlineLabel"),
		"insightText", text2("insightText"), "detailText", text2("detailText"), "mood", lookup(out, "mood"),
		"chartType", lookup(out, "chartType"), "chartData", chart, "baselineValue", baseline,
		"timeWindow", jsObjOf("grain", lookup(tw, "grain"), "start", bound("start"), "end", bound("end"), "label", jsValueString(label))), nil
}

var (
	metricFailSentinels = []string{"error", "errors", "n/a", "na", "unavailable", "data unavailable", "data unavilable",
		"no data", "null", "undefined", "—", "-", "?", "tbd"}
	isoDay = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func allPunct(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !jsSpace(r) && !strings.ContainsRune("-—?_.", r) {
			return false
		}
	}
	return true
}

// validateMetricResult is validateResult() of the worker's metric_refresh
// job. The error is the TypeError that Node threw for a null chart item.
func validateMetricResult(r *jsObj) (string, error) {
	headline, _ := lookup(r, "headlineMetric").(string)
	if jsTrim(headline) == "" {
		return "empty headlineMetric", nil
	}
	norm := strings.ToLower(jsTrim(headline))
	if slices.Contains(metricFailSentinels, norm) || allPunct(norm) {
		return `agent emitted error sentinel as headlineMetric: "` + headline + `"`, nil
	}
	if !jsTruthy(lookup(r, "headlineLabel")) {
		return "empty headlineLabel", nil
	}
	if !jsTruthy(lookup(r, "insightText")) {
		return "empty insightText", nil
	}
	mood, chartType := lookup(r, "mood"), lookup(r, "chartType")
	if mood != "good" && mood != "watch" && mood != "bad" {
		return "invalid mood: " + jsTemplate(mood), nil
	}
	if c, ok := chartType.(string); !ok || !slices.Contains(bootstrapCharts, c) {
		return "invalid chartType: " + jsTemplate(chartType), nil
	}
	chart, _ := lookup(r, "chartData").([]any)
	if len(chart) == 0 {
		return "empty chartData", nil
	}
	for _, item := range chart {
		if item == nil {
			return "", errors.New("Cannot read properties of null (reading 'v')")
		}
		v, ok := lookup(asObj(item), "v").(float64)
		if !ok || math.IsNaN(v) {
			return "chartData item has non-numeric v", nil
		}
		if !jsTruthy(lookup(asObj(item), "d")) {
			return "chartData item missing d label", nil
		}
	}
	if chartType == "kpi" && len(chart) != 1 {
		return "kpi chartType requires exactly 1 chartData item, got " + strconv.Itoa(len(chart)), nil
	}
	if chartType == "kpi" && nullish(lookup(asObj(chart[0]), "t")) {
		return "kpi chartType requires a baseline (t) value", nil
	}
	tw := asObj(lookup(r, "timeWindow"))
	if tw == nil {
		return "missing timeWindow", nil
	}
	grain := lookup(tw, "grain")
	if g, ok := grain.(string); !ok || !slices.Contains([]string{"day", "week", "month", "quarter", "year", "all_time", "snapshot"}, g) {
		return "invalid timeWindow.grain: " + jsTemplate(grain), nil
	}
	if !jsTruthy(lookup(tw, "label")) {
		return "empty timeWindow.label", nil
	}
	if grain != "all_time" {
		for _, key := range []string{"start", "end"} {
			v := lookup(tw, key)
			s, ok := v.(string)
			if !ok || s == "" || !isoDay.MatchString(s) {
				return "timeWindow." + key + ` must be yyyy-mm-dd (got "` + jsTemplate(v) + `")`, nil
			}
		}
	}
	return "", nil
}

// metricUsage sums the outer usage events of the run as addUsage() did.
type metricUsage struct {
	sums     map[string]float64
	currency string
	pricing  string
	partial  bool
	reasons  []string
	seen     bool
}

var usageNumbers = []string{"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens", "totalTokens", "estimatedCostUsd", "billedCostUsd"}

func (u *metricUsage) add(usage any) {
	if u.sums == nil {
		u.sums = map[string]float64{}
	}
	for _, k := range usageNumbers {
		if n, ok := jsGet(usage, k).(float64); ok {
			u.sums[k] += n
		}
	}
	if c, ok := jsGet(usage, "currency").(string); ok && c != "" {
		u.currency = c
	}
	if p, ok := jsGet(usage, "pricingCatalogVersion").(string); ok && p != "" {
		u.pricing = p
	}
	if (u.seen && u.partial) || jsGet(usage, "coverage") != "complete" {
		u.partial = true
	}
	for _, r := range jsArray(jsGet(usage, "missingReasons")) {
		if s, ok := r.(string); ok && !slices.Contains(u.reasons, s) {
			u.reasons = append(u.reasons, s)
		}
	}
	u.seen = true
}

func (u *metricUsage) js() *jsObj {
	o := &jsObj{}
	for _, k := range usageNumbers {
		if n, ok := u.sums[k]; ok {
			o.set(k, n)
		}
	}
	if u.currency != "" {
		o.set("currency", u.currency)
	}
	if u.pricing != "" {
		o.set("pricingCatalogVersion", u.pricing)
	}
	o.set("coverage", map[bool]string{true: "partial", false: "complete"}[u.partial])
	if len(u.reasons) > 0 {
		reasons := make([]any, len(u.reasons))
		for i, r := range u.reasons {
			reasons[i] = r
		}
		o.set("missingReasons", reasons)
	}
	return o
}

// refreshMetric is the metric_refresh job: one card, by its saved query or
// by the metric agent, written as a new snapshot.
func (a *API) refreshMetric(ctx context.Context, jobID, orgID string) error {
	a.jobProgress(ctx, jobID, "Loading card")
	var raw []byte
	if err := a.DB.QueryRow(ctx, `select trigger_payload from processing_job where id = $1 and org_id = $2`, jobID, orgID).Scan(&raw); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	parsed, _ := jsParse(raw)
	payload := asObj(parsed)
	if payload == nil {
		return fmt.Errorf("metric_refresh job %s has no trigger_payload", jobID)
	}
	field := func(key string) string {
		s, _ := lookup(payload, key).(string)
		return s
	}
	var card metricCard
	var metricID string
	switch {
	case field("metricId") != "":
		var mode string
		var definition []byte
		var hash *string
		err := a.DB.QueryRow(ctx, `select id::text, role, slug, title, why, chart_hint, execution_mode, definition_json, definition_hash
			from metric where id::text = $1 and org_id = $2`, field("metricId"), orgID).
			Scan(&metricID, &card.Role, &card.Slug, &card.Title, &card.Why, &card.ChartHint, &mode, &definition, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("metric %s not found", field("metricId"))
		}
		if err != nil {
			return err
		}
		if mode == "saved_query" {
			def, _ := jsParse(definition)
			if def == nil || hash == nil || *hash == "" {
				return fmt.Errorf("saved-query metric %s has no validated definition", field("metricId"))
			}
			return a.refreshSavedQueryMetric(ctx, jobID, orgID, metricID, card.Slug, def, *hash)
		}
		if card.Why == nil {
			card.Why = &card.Title
		}
		if card.ChartHint == nil {
			line := "line"
			card.ChartHint = &line
		}
	case field("question") != "":
		q := field("question")
		card = metricCard{Question: q, Slug: field("slug"), Role: field("role"), Title: field("title")}
		why, hint := orDefault(field("why"), q), orDefault(field("chartHint"), "bar")
		card.Why, card.ChartHint = &why, &hint
		if card.Slug == "" {
			card.Slug = "chat-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
		}
		if card.Role == "" {
			card.Role = "CEO"
		}
		if card.Title == "" {
			card.Title = utf16Head(q, 60)
		}
		err := a.DB.QueryRow(ctx, `with found as (
			  update metric set created_by_job = $5, last_refresh_status = 'pending', last_refresh_error = null, last_refresh_job_id = $5
			   where id = (select id from metric where org_id = $1 and role = $2 and slug = $3 limit 1) returning id
			), made as (
			  insert into metric (org_id, role, slug, source, title, why, chart_hint, active, created_by_job, last_refresh_status, last_refresh_job_id)
			  select $1, $2, $3, 'chat', $4, $6, $7, false, $5, 'pending', $5 where not exists (select 1 from found) returning id
			) select id::text from found union all select id::text from made`,
			orgID, card.Role, card.Slug, card.Title, jobID, why, hint).Scan(&metricID)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("metric_refresh job %s has neither metricId nor question in trigger_payload", jobID)
	}
	a.jobProgress(ctx, jobID, "Running agent")
	err := a.runMetricCard(ctx, jobID, orgID, metricID, card)
	if err != nil {
		a.markMetric(ctx, jobID, metricID, err)
	}
	return err
}

func (a *API) markMetric(ctx context.Context, jobID, metricID string, failure error) {
	ctx = context.WithoutCancel(ctx)
	var err error
	if failure == nil {
		_, err = a.DB.Exec(ctx, `update metric set last_refresh_status = 'ok', last_refresh_error = null, last_refresh_job_id = $2, updated_at = now() where id::text = $1`, metricID, jobID)
	} else {
		_, err = a.DB.Exec(ctx, `update metric set last_refresh_status = 'failed', last_refresh_error = $3, last_refresh_job_id = $2, updated_at = now() where id::text = $1`,
			metricID, jobID, utf16Prefix(failure.Error(), 500))
	}
	if err != nil {
		log.Printf("[metric_refresh] could not save the state of metric %s: %v", metricID, err)
	}
}

func (a *API) runMetricCard(ctx context.Context, jobID, orgID, metricID string, card metricCard) error {
	if err := a.ensureHostConfigProvisioned(ctx, orgID); err != nil {
		return err
	}
	var reservation string
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var err error
		reservation, err = reserveRunSpend(ctx, tx, orgID, "", "", "metric", time.Now())
		return err
	})
	var blocked *spendExceeded
	if errors.As(err, &blocked) {
		a.recordBudgetBlocked(ctx, blocked)
		return errors.New(blocked.message())
	}
	if err != nil {
		return err
	}
	var usage metricUsage
	defer func() {
		done := context.WithoutCancel(ctx)
		if usage.seen {
			if err := a.recordReservedSpend(done, orgID, reservation, jsObjOf("usage", usage.js()), time.Now()); err != nil {
				log.Printf("[metric_refresh] could not record spend for job=%s: %v", jobID, err)
			}
		}
		if _, err := a.DB.Exec(done, `update spend_reservation set released_at = now() where id::text = $1 and released_at is null`, reservation); err != nil {
			log.Printf("[metric_refresh] could not release the spend reservation of job=%s: %v", jobID, err)
		}
	}()
	result, err := a.runMetricAgent(ctx, jobID, orgID, card, &usage)
	if err != nil {
		return err
	}
	msg, verr := validateMetricResult(result)
	if verr != nil {
		return verr
	}
	if msg != "" {
		return errors.New("metric agent output invalid: " + msg)
	}
	a.jobProgress(ctx, jobID, "Saving snapshot")
	if _, err := a.DB.Exec(ctx, `insert into metric_snapshot (metric_id, status, payload) values ($1::uuid, $2, $3::jsonb)`,
		metricID, lookup(result, "mood"), jsStringify(result, false)); err != nil {
		return err
	}
	a.markMetric(ctx, jobID, metricID, nil)
	log.Printf("[metric_refresh] org=%s metric=%s mood=%s %q", orgID, card.Slug, lookup(result, "mood"), lookup(result, "headlineMetric"))
	return nil
}

func (a *API) runMetricAgent(ctx context.Context, jobID, orgID string, card metricCard, usage *metricUsage) (*jsObj, error) {
	var mcpURL *string
	if err := noRows(a.DB.QueryRow(ctx, `select mcp_url from data_source where org_id = $1 order by is_default desc, created_at limit 1`, orgID).Scan(&mcpURL)); err != nil {
		return nil, err
	}
	if mcpURL == nil || *mcpURL == "" {
		return nil, errors.New("no mcp_url for org " + orgID)
	}
	log.Printf("[metric-agent] org=%s role=%s slug=%s", orgID, card.Role, card.Slug)
	roots, err := orgWorkspace(orgID)
	if err != nil {
		return nil, err
	}
	if r := a.prefetchKnowledge(ctx, orgID, roots.Knowledge, false); r.OK {
		total := 0
		for _, f := range r.Files {
			total += f.Bytes
		}
		log.Printf("[metric-agent] org=%s slug=%s knowledge refreshed (%d files, %dB)", orgID, card.Slug, len(r.Files), total)
	} else {
		log.Printf("[metric-agent] org=%s slug=%s knowledge refresh failed (%s); proceeding with on-disk pack", orgID, card.Slug, r.Error)
	}
	memory, err := a.globalMemoryContext(ctx, orgID, 5, heldSet{All: true})
	if err != nil {
		return nil, err
	}
	started := time.Now()
	var result *jsObj
	_, err = a.runAgentJob(ctx, agentJob{OrgID: orgID, RunID: jobID, Label: "metric-agent", GraphjinRead: true, MemorySearch: true,
		Prompt: buildMetricPrompt(card, readKnowledgePack(roots.Knowledge), memory), MaxAttempts: 3,
		OnEvent: func(_ int, event any) {
			if jsGet(event, "type") == "usage" && jsGet(event, "source") == "outer" {
				usage.add(jsGet(event, "usage"))
			}
		},
		Validate: func(text string) error {
			var verr error
			result, verr = normalizeMetricReply(text)
			return verr
		}})
	if err != nil {
		return nil, err
	}
	log.Printf("[metric-agent] org=%s slug=%s done in %.0fs", orgID, card.Slug, time.Since(started).Seconds())
	return result, nil
}

func (a *API) refreshSavedQueryMetric(ctx context.Context, jobID, orgID, metricID, slug string, definition any, hash string) error {
	started := time.Now()
	err := a.runSavedQueryMetric(ctx, jobID, orgID, metricID, slug, definition, hash, started)
	a.markMetric(ctx, jobID, metricID, err)
	return err
}

func (a *API) runSavedQueryMetric(ctx context.Context, jobID, orgID, metricID, slug string, definition any, hash string, started time.Time) error {
	def, err := parseMetricDefinition(definition)
	if err != nil {
		return err
	}
	a.jobProgress(ctx, jobID, "Running reviewed saved query")
	src, err := a.packArtifactSource(ctx, orgID, "metric", metricID)
	if err != nil {
		return err
	}
	if src == nil {
		var url, auth *string
		if err := noRows(a.DB.QueryRow(ctx, `select graphql_url, auth_mode from data_source where org_id = $1 and enabled
			order by is_default desc, created_at limit 1`, orgID).Scan(&url, &auth)); err != nil {
			return err
		}
		src = &packSource{GraphqlURL: deref(url), AuthMode: deref(auth)}
	}
	if src.GraphqlURL == "" {
		return errors.New("saved-query metric has no enabled GraphJin source")
	}
	var previous *string
	if err := noRows(a.DB.QueryRow(ctx, `select value::text from metric_snapshot where metric_id::text = $1 order by captured_at desc limit 1`, metricID).Scan(&previous)); err != nil {
		return err
	}
	var baseline *float64
	if previous != nil {
		if n, err := strconv.ParseFloat(*previous, 64); err == nil && !math.IsInf(n, 0) {
			baseline = &n
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	vars, err := buildSavedQueryVariables(definition, now)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(src.GraphqlURL, "/")
	if !strings.HasSuffix(endpoint, "/api/v1/graphql") {
		endpoint += "/api/v1/graphql"
	}
	header := http.Header{"Content-Type": {"application/json"}, "X-Role": {"service"}}
	if src.AuthMode == "jwt" {
		token, err := a.graphjinToken(orgID, "service", 300*time.Second)
		if err != nil {
			return err
		}
		header.Set("Authorization", "Bearer "+token)
	}
	document := lookup(def.vals["execution"].(*jsObj), "document").(string)
	data, err := watcherGraphQL(ctx, endpoint, header, document, vars)
	var gqlErr graphqlErrors
	if errors.As(err, &gqlErr) {
		return errors.New("saved-query metric failed: " + string(gqlErr))
	}
	if err != nil {
		return err
	}
	if !jsTruthy(data) {
		return errors.New("saved-query metric returned no data")
	}
	mapped, err := mapSavedQueryMetric(definition, data, baseline, now)
	if err != nil {
		return err
	}
	msg, verr := validateMetricResult(mapped.Result)
	if verr != nil {
		return verr
	}
	if msg != "" {
		return errors.New("saved-query metric output invalid: " + msg)
	}
	a.jobProgress(ctx, jobID, "Saving deterministic snapshot")
	numeric := func(v *float64) any {
		if v == nil {
			return nil
		}
		return jsNumberText(*v)
	}
	duration := time.Since(started).Milliseconds()
	if _, err := a.DB.Exec(ctx, `insert into metric_snapshot (metric_id, value, value_json, baseline, delta_pct, status, payload,
			definition_hash, source_freshness_at, duration_ms, error_class)
		values ($1::uuid, $2::numeric, $3::jsonb, $4::numeric, $5::numeric, $6, $7::jsonb, $8, $9, $10, null)`,
		metricID, numeric(mapped.Value), jsonOrNull(mapped.ValueJSON), numeric(mapped.Baseline), numeric(mapped.DeltaPct),
		lookup(mapped.Result, "mood"), jsStringify(mapped.Result, false), hash, mapped.SourceFreshness, duration); err != nil {
		return err
	}
	log.Printf("[metric_refresh] org=%s metric=%s mode=saved_query mood=%s durationMs=%d", orgID, slug, lookup(mapped.Result, "mood"), duration)
	return nil
}

func jsonOrNull(v any) any {
	if v == nil {
		return nil
	}
	return jsStringify(v, false)
}
