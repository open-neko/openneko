package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

type agentJobFixture struct {
	Profiler []struct {
		Mode, OrgName, CompanyNote, Tables, Insights, Namespaces, Syntax, Prompt string
	}
	ProfileValidation []struct {
		Raw   string
		Value *string
		Error *string
	}
	Bootstrap []struct {
		Seats                                              []string
		IndustryInsights, OrgName, BusinessProfile, Prompt string
	}
	BootstrapValidation []struct {
		Seats []string
		Raw   json.RawMessage
		Value json.RawMessage
		Error *string
	}
	ParseJSON []struct {
		Raw   string
		Value json.RawMessage
		Error *string
	} `json:"parseJson"`
	ExecutionErrors []struct {
		Raw         string
		Error, Name *string
	}
	Correction struct{ Base, Output, Error, Prompt string }
	Research   struct{ OrgName, Mission, UserPrompt, MissionInstructions, System string }
}

func loadAgentJobFixture(t *testing.T) agentJobFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/agentjobs.json")
	if err != nil {
		t.Fatal(err)
	}
	var f agentJobFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAgentJobsMatchNode(t *testing.T) {
	f := loadAgentJobFixture(t)
	for _, c := range f.Profiler {
		k := knowledgePack{Mode: c.Mode, Tables: c.Tables, Insights: c.Insights, Namespaces: c.Namespaces, Syntax: c.Syntax}
		if got := buildProfilerPrompt(c.OrgName, c.CompanyNote, k); got != c.Prompt {
			t.Errorf("profiler prompt (%s) differs at %d", c.Mode, firstDiff(got, c.Prompt))
		}
	}
	for _, c := range f.ProfileValidation {
		got, err := validateBusinessProfile(c.Raw)
		if c.Error != nil {
			if err == nil || err.Error() != *c.Error {
				t.Errorf("validate %q: %v", c.Raw, err)
			}
		} else if err != nil || got != *c.Value {
			t.Errorf("validate %q = %q, %v", c.Raw, got, err)
		}
	}
	for _, c := range f.Bootstrap {
		if got := buildBootstrapPrompt(c.OrgName, c.BusinessProfile, c.IndustryInsights, c.Seats); got != c.Prompt {
			t.Errorf("bootstrap prompt %v differs at %d", c.Seats, firstDiff(got, c.Prompt))
		}
	}
	for i, c := range f.BootstrapValidation {
		raw, _ := jsParse(c.Raw)
		got, err := validateBootstrapMetrics(raw, c.Seats)
		if c.Error != nil {
			if err == nil || err.Error() != *c.Error {
				t.Errorf("bootstrap validation %d: %v, want %q", i, err, *c.Error)
			}
			continue
		}
		cards := []any{}
		for _, m := range got {
			cards = append(cards, jsObjOf("role", m.Role, "slug", m.Slug, "title", m.Title, "why", m.Why, "chart_hint", m.ChartHint))
		}
		want, _ := jsParse(c.Value)
		if err != nil || jsStringify(cards, false) != jsStringify(want, false) {
			t.Errorf("bootstrap validation %d: %s, %v", i, jsStringify(cards, false), err)
		}
	}
	for _, c := range f.ParseJSON {
		got, err := parseAgentJSON(c.Raw)
		if c.Error != nil {
			if err == nil || (strings.HasPrefix(*c.Error, "agent output") && err.Error() != *c.Error) {
				t.Errorf("parse %q: %v", c.Raw, err)
			}
			continue
		}
		want, _ := jsParse(c.Value)
		if err != nil || jsStringify(got, false) != jsStringify(want, false) {
			t.Errorf("parse %q = %s, %v", c.Raw, jsStringify(got, false), err)
		}
	}
	for _, c := range f.ExecutionErrors {
		err := agentExecutionError(c.Raw)
		if c.Error == nil {
			if err != nil {
				t.Errorf("execution error for %q: %v", jsSlice(c.Raw, 60), err)
			}
			continue
		}
		var upstream upstreamProviderError
		var auth dataSourceAuthorizationError
		kind := ""
		if errors.As(err, &upstream) {
			kind = "UpstreamProviderError"
		} else if errors.As(err, &auth) {
			kind = "DataSourceAuthorizationError"
		}
		if err == nil || err.Error() != *c.Error || kind != *c.Name {
			t.Errorf("execution error for %q: %v (%s)", jsSlice(c.Raw, 60), err, kind)
		}
	}
	if got := agentCorrectionPrompt(f.Correction.Base, f.Correction.Output, f.Correction.Error); got != f.Correction.Prompt {
		t.Errorf("correction prompt differs at %d", firstDiff(got, f.Correction.Prompt))
	}
	r := f.Research
	if deepResearchUserPrompt(r.OrgName, r.Mission) != r.UserPrompt || deepResearchSystemPrompt != r.System || researchMissionWriter.Instruction != r.MissionInstructions {
		t.Error("the research prompts differ from Node")
	}
}

func firstDiff(a, b string) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

type modelLog struct {
	mu     sync.Mutex
	bodies []string
}

func (l *modelLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.bodies...)
}

func recordingModel(t *testing.T, responses []string) (*httptest.Server, *modelLog) {
	t.Helper()
	inner := fakeModel(t, responses)
	log := &modelLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.bodies = append(log.bodies, string(body))
		log.mu.Unlock()
		req, _ := http.NewRequest(r.Method, inner.URL+r.URL.Path, strings.NewReader(string(body)))
		req.Header = r.Header
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer res.Body.Close()
		w.Header().Set("content-type", res.Header.Get("content-type"))
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func buildFront(t *testing.T) (*Front, *pgxpool.Pool) {
	t.Helper()
	f, db := memoryFront(t)
	clearAxEnv(t, nil)
	t.Setenv("OPENNEKO_AGENT_RUNS_DIR", t.TempDir())
	t.Setenv("OPENNEKO_SERVE_SOCKET", startServe(t))
	seedRiver(t, db, f)
	g := &fakeGateway{reply: map[string]any{"missionCharter": "  Investigate the bicycle trade.  "}}
	f.API.Gateway = g.generate
	f.API.hostProv.applied = map[string]string{}
	seed(t, db, `update organization set name = 'Acme Cycles' where id = 'org'`)
	seed(t, db, `insert into onboarding_wizard (org_id, company_note, active_seats) values ('org', 'We sell bikes', '{CEO,CFO}')`)
	return f, db
}

func newBuildJob(t *testing.T, db *pgxpool.Pool, kind string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `insert into processing_job (org_id, kind, status, trigger) values ('org', $1, 'queued', 'test') returning id::text`, kind).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func jobState(t *testing.T, db *pgxpool.Pool, id string) string {
	t.Helper()
	return rowText(t, db, `select status || ' ' || coalesce(error, '') || ' ' || coalesce(progress->>'message', '') from processing_job where id = $1`, id)
}

func axReplies(answers ...string) []string {
	var out []string
	for _, a := range answers {
		out = append(out, `{"javascriptCode":"final('task', {})"}`, `{"javascriptCode":"final('done', {});"}`, a)
	}
	return out
}

const acmeProfile = "# Acme Cycles — Business Profile\n\n## What they are\nA bike shop."

func TestBusinessProfileBuildThroughServe(t *testing.T) {
	f, db := buildFront(t)
	a := f.API
	ctx := t.Context()
	model, calls := recordingModel(t, axReplies("I am sorry, I could not access the data.", "```markdown\n"+acmeProfile+"\n```"))
	a.turnResolve = fixedBackend(model.URL)
	tools := &fakeTools{reply: []any{}, gjTools: []any{}}
	var bindings []turnBinding
	a.jobTools = func(b turnBinding) toolBackend { bindings = append(bindings, b); return tools }

	jobID := newBuildJob(t, db, jobs.KindBusinessProfileBuild)
	if err := a.RunBuild(ctx, jobs.KindBusinessProfileBuild, "org", jobID); err == nil || !strings.Contains(err.Error(), "no mcp_url configured for org org") {
		t.Fatalf("no data source: %v", err)
	}
	if got := jobState(t, db, jobID); got != "failed no mcp_url configured for org org " {
		t.Errorf("failed row: %q", got)
	}

	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, is_default) values ('org', 'graphjin', 'shop', 'http://127.0.0.1:1/api/v1/graphql', 'http://127.0.0.1:1/api/v1/mcp', true)`)
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, model, enabled, secrets) values ('org', 'research', 'perplexity', 'sonar-deep-research', true, '{"apiKey":"pplx-test"}')`)
	if err := a.RunBuild(ctx, jobs.KindBusinessProfileBuild, "org", jobID); err != nil {
		t.Fatal(err)
	}
	if got := jobState(t, db, jobID); got != "succeeded  Saving profile" {
		t.Errorf("succeeded row: %q", got)
	}
	bodies := calls.all()
	if len(bodies) != 6 || !strings.Contains(bodies[0], "You build a short markdown business profile") || strings.Contains(bodies[2], "<previous-attempt-rejected>") ||
		!strings.Contains(bodies[3], "<previous-attempt-rejected>") || !strings.Contains(bodies[3], "profiler returned failure text") {
		t.Errorf("model calls: %d", len(bodies))
	}
	if len(bindings) != 1 || bindings[0].Kind != "agent-job" || bindings[0].RunID != jobID {
		t.Errorf("tool binding: %+v", bindings)
	}
	if got := rowText(t, db, `select version || ' ' || business_profile || ' ' || built_by_job from customer_profile where org_id = 'org' and is_current`); got != "1 "+acmeProfile+" "+jobID {
		t.Errorf("profile: %q", got)
	}
	chained := rowText(t, db, `select id::text from processing_job where kind = 'industry_insights_build' and trigger = 'chain_after_business_profile_build'`)
	if j := riverJobs(t, db, jobs.KindIndustryInsightsBuild); len(j) != 1 || j[0].Args != `{"orgId": "org", "processingJobId": "`+chained+`"}` || j[0].MaxAttempts != 3 {
		t.Errorf("chained job: %+v", j)
	}

	seed(t, db, `update llm_provider_config set enabled = false where scope = 'research'`)
	model2, _ := recordingModel(t, axReplies(acmeProfile+"\n\nv2"))
	a.turnResolve = fixedBackend(model2.URL)
	second := newBuildJob(t, db, jobs.KindBusinessProfileBuild)
	if err := a.RunBuild(ctx, jobs.KindBusinessProfileBuild, "org", second); err != nil {
		t.Fatal(err)
	}
	if got := rowText(t, db, `select string_agg(version || ':' || is_current, ',' order by version) from customer_profile where org_id = 'org'`); got != "1:false,2:true" {
		t.Errorf("versions: %q", got)
	}
	if j := riverJobs(t, db, jobs.KindIndustryInsightsBuild); len(j) != 1 {
		t.Errorf("research off still chained: %d", len(j))
	}

	down, _ := recordingModel(t, axReplies("Gemini HTTP 503 UNAVAILABLE, try later"))
	a.turnResolve = fixedBackend(down.URL)
	third := newBuildJob(t, db, jobs.KindBusinessProfileBuild)
	err := a.RunBuild(ctx, jobs.KindBusinessProfileBuild, "org", third)
	if !errors.Is(err, jobs.ErrNoRetry) {
		t.Errorf("provider outage retries: %v", err)
	}
	if got := jobState(t, db, third); !strings.HasPrefix(got, "failed Upstream provider unavailable (google-gemini): Gemini HTTP 503") {
		t.Errorf("outage row: %q", got)
	}
}

func TestIndustryInsightsBuild(t *testing.T) {
	f, db := buildFront(t)
	a := f.API
	ctx := t.Context()
	jobID := newBuildJob(t, db, jobs.KindIndustryInsightsBuild)
	if err := a.RunBuild(ctx, jobs.KindIndustryInsightsBuild, "org", jobID); err == nil || err.Error() != "no current customer_profile for org org — run business_profile_build first" {
		t.Errorf("no profile: %v", err)
	}
	seed(t, db, `insert into customer_profile (org_id, version, is_current, business_profile, company_note) values ('org', 1, true, 'We sell bikes.', 'Note')`)

	if err := a.RunBuild(ctx, jobs.KindIndustryInsightsBuild, "org", jobID); err != nil {
		t.Fatal(err)
	}
	if got := rowText(t, db, `select coalesce(industry_insights, 'null') || ' ' || coalesce(industry_insights_research_task, 'null') from customer_profile`); got != "null null" {
		t.Errorf("research off wrote %q", got)
	}

	var mu sync.Mutex
	var requests []map[string]any
	var auth []string
	perplexity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requests = append(requests, body)
		auth = append(auth, r.Header.Get("Authorization"))
		n := len(requests)
		mu.Unlock()
		if n == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices":   []any{map[string]any{"message": map[string]any{"content": "<think>plan</think>\n# Acme Cycles — Industry Insights\n- Bikes [1]"}}},
			"citations": []string{"https://example.test/a"},
		})
	}))
	t.Cleanup(perplexity.Close)
	a.ResearchURL = perplexity.URL
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, model, enabled, secrets) values ('org', 'research', 'perplexity', 'sonar-deep-research', true, '{"apiKey":"pplx-test"}')`)
	if err := a.RunBuild(ctx, jobs.KindIndustryInsightsBuild, "org", jobID); err != nil {
		t.Fatal(err)
	}
	if got := rowText(t, db, `select industry_insights || '|' || industry_insights_research_task from customer_profile`); got != "# Acme Cycles — Industry Insights\n- Bikes [1]\n\n[1]: https://example.test/a|Investigate the bicycle trade." {
		t.Errorf("insights: %q", got)
	}
	if got := jobState(t, db, jobID); got != "succeeded  Saving industry insights" {
		t.Errorf("row: %q", got)
	}
	if len(requests) != 2 || auth[1] != "Bearer pplx-test" || requests[1]["model"] != "sonar-deep-research" || requests[1]["reasoning_effort"] != "medium" {
		t.Errorf("perplexity requests: %d %v", len(requests), auth)
	}
	msgs, _ := requests[1]["messages"].([]any)
	if len(msgs) != 2 || jsGet(jsFromGo(msgs[1]), "content") != deepResearchUserPrompt("Acme Cycles", "Investigate the bicycle trade.") {
		t.Errorf("messages: %v", msgs)
	}

	perplexity.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	})
	err := a.RunBuild(ctx, jobs.KindIndustryInsightsBuild, "org", jobID)
	if err == nil || err.Error() != "perplexity 401 Unauthorized: bad key\n" {
		t.Errorf("401: %v", err)
	}
}

func TestBootstrapMetricsBuild(t *testing.T) {
	f, db := buildFront(t)
	a := f.API
	ctx := t.Context()
	seed(t, db, `insert into customer_profile (org_id, version, is_current, business_profile, industry_insights) values ('org', 1, true, 'We sell bikes.', 'Bikes grow.')`)
	seed(t, db, `insert into metric (org_id, role, slug, source, title) values ('org', 'CFO', 'cash', 'bootstrap', 'Cash')`)
	cards := []string{}
	for _, s := range []string{"road-mix", "shop-count", "repeat-rate", "cash"} {
		cards = append(cards, `{"role":"CEO","slug":"`+s+`","title":"T `+s+`","why":"Because.","chart_hint":"bar"}`)
	}
	model, calls := recordingModel(t, axReplies("```json\n{\"metrics\":["+strings.Join(cards, ",")+"]}\n```"))
	a.turnResolve = fixedBackend(model.URL)
	a.jobTools = func(turnBinding) toolBackend { t.Error("the bootstrap job got tools"); return &fakeTools{} }

	jobID := newBuildJob(t, db, jobs.KindBootstrapMetricsBuild)
	if err := a.RunBuild(ctx, jobs.KindBootstrapMetricsBuild, "org", jobID); err != nil {
		t.Fatal(err)
	}
	bodies := calls.all()
	if len(bodies) != 3 || !strings.Contains(bodies[0], "exactly 4 starter dashboard cards: 4 per role for CEO.") {
		t.Errorf("model calls: %v", len(bodies))
	}
	if got := rowText(t, db, `select string_agg(role || '/' || slug || '/' || coalesce(last_refresh_status, 'none') || '/' || coalesce(created_by_job::text, ''), ',' order by role, slug) from metric where org_id = 'org'`); got != "CEO/cash/pending/"+jobID+",CEO/repeat-rate/pending/"+jobID+",CEO/road-mix/pending/"+jobID+",CEO/shop-count/pending/"+jobID+",CFO/cash/none/" {
		t.Errorf("metrics: %q", got)
	}
	if n := rowText(t, db, `select count(*)::text from processing_job where kind = 'metric_refresh' and trigger = 'chain_after_bootstrap_metrics_build' and trigger_payload ? 'metricId'`); n != "4" {
		t.Errorf("refresh rows: %s", n)
	}
	if j := riverQueueJobs(t, db, jobs.KindMetricRefresh, jobs.QueueMetricRefresh); len(j) != 4 || j[0].MaxAttempts != 3 {
		t.Errorf("refresh jobs: %+v", j)
	}

	again := newBuildJob(t, db, jobs.KindBootstrapMetricsBuild)
	if err := a.RunBuild(ctx, jobs.KindBootstrapMetricsBuild, "org", again); err != nil {
		t.Fatal(err)
	}
	if len(calls.all()) != 3 || jobState(t, db, again) != "succeeded  Bootstrap metrics already present" {
		t.Errorf("seeded seats called the model: %d %q", len(calls.all()), jobState(t, db, again))
	}
}
