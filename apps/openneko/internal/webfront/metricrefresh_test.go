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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

// testdata/metricrefresh.json was written from the Node metric agent, the
// worker's validateResult() and deterministic-metric.ts at 9174293c3.
type metricFixture struct {
	Prompts []struct {
		Question       string
		Role, Slug     string
		Title          string
		Why, ChartHint *string
		Knowledge      knowledgePack
		MemoryContext  string
		Prompt         string
	}
	Now     string
	Data    json.RawMessage
	Results []struct {
		Text       string
		Result     json.RawMessage
		Error      *string
		ParseError *string
	}
	Mappings []struct {
		Name       string
		Definition json.RawMessage
		Baseline   *float64
		Out        *struct {
			Result, ValueJSON         json.RawMessage
			Value, Baseline, DeltaPct *float64
			SourceFreshnessAt         *string
		}
		Error *string
	}
	Variables []struct{ Definition, Variables json.RawMessage }
}

func parseJS(t *testing.T, raw []byte) any {
	t.Helper()
	v, err := jsParse(raw)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return v
}

func floatEq(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func TestMetricRefreshMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/metricrefresh.json")
	if err != nil {
		t.Fatal(err)
	}
	var f metricFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Prompts) != 3 || len(f.Results) != 33 || len(f.Mappings) != 38 || len(f.Variables) != 3 {
		t.Fatalf("fixture sizes: %d %d %d %d", len(f.Prompts), len(f.Results), len(f.Mappings), len(f.Variables))
	}
	for _, c := range f.Prompts {
		got := buildMetricPrompt(metricCard{Question: c.Question, Role: c.Role, Slug: c.Slug, Title: c.Title, Why: c.Why, ChartHint: c.ChartHint},
			c.Knowledge, c.MemoryContext)
		if got != c.Prompt {
			i := firstDiff(got, c.Prompt)
			t.Errorf("prompt %s differs at %d: %q", c.Slug, i, got[max(0, i-40):min(len(got), i+80)])
		}
	}
	for _, c := range f.Results {
		r, err := normalizeMetricReply(c.Text)
		var msg string
		var verr error
		if err == nil {
			msg, verr = validateMetricResult(r)
		}
		switch {
		case c.ParseError != nil:
			got := ""
			if err != nil {
				got = err.Error()
			} else if verr != nil {
				got = verr.Error()
			}
			if got != *c.ParseError {
				t.Errorf("%s: got %v %v, want thrown %q", c.Text, err, verr, *c.ParseError)
			}
		case err != nil || verr != nil:
			t.Errorf("%s: %v %v", c.Text, err, verr)
		case (c.Error == nil) != (msg == "") || (c.Error != nil && msg != *c.Error):
			t.Errorf("%s: validation %q, want %v", c.Text, msg, c.Error)
		case c.Error == nil && jsCanonical(r) != jsCanonical(parseJS(t, c.Result)):
			t.Errorf("%s: result %s, want %s", c.Text, jsCanonical(r), c.Result)
		}
	}
	now, _ := time.Parse(time.RFC3339, f.Now)
	data := parseJS(t, f.Data)
	for _, c := range f.Mappings {
		got, err := mapSavedQueryMetric(parseJS(t, c.Definition), data, c.Baseline, now)
		if c.Error != nil {
			if err == nil || err.Error() != *c.Error {
				t.Errorf("%s: %v, want %q", c.Name, err, *c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		want := c.Out
		if jsCanonical(got.Result) != jsCanonical(parseJS(t, want.Result)) {
			t.Errorf("%s: result\n%s\nwant\n%s", c.Name, jsCanonical(got.Result), jsCanonical(parseJS(t, want.Result)))
		}
		if jsCanonical(got.ValueJSON) != jsCanonical(parseJS(t, want.ValueJSON)) {
			t.Errorf("%s: value json %s, want %s", c.Name, jsCanonical(got.ValueJSON), want.ValueJSON)
		}
		if !floatEq(got.Value, want.Value) || !floatEq(got.Baseline, want.Baseline) || !floatEq(got.DeltaPct, want.DeltaPct) {
			t.Errorf("%s: numbers %v %v %v", c.Name, got.Value, got.Baseline, got.DeltaPct)
		}
		var fresh *string
		if got.SourceFreshness != nil {
			s := jsDate(*got.SourceFreshness)
			fresh = &s
		}
		if (fresh == nil) != (want.SourceFreshnessAt == nil) || (fresh != nil && *fresh != *want.SourceFreshnessAt) {
			t.Errorf("%s: freshness %v, want %v", c.Name, fresh, want.SourceFreshnessAt)
		}
	}
	for _, c := range f.Variables {
		got, err := buildSavedQueryVariables(parseJS(t, c.Definition), now)
		if err != nil || jsStringify(got, false) != jsStringify(parseJS(t, c.Variables), false) {
			t.Errorf("variables %s: %s %v", c.Definition, jsStringify(got, false), err)
		}
	}
}

func TestJSDateParse(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-14":                     "2026-11-14T00:00:00.000Z",
		"2026-11-14T08:30:00Z":           "2026-11-14T08:30:00.000Z",
		"2026-11-14T08:30:00.1234+02:00": "2026-11-14T06:30:00.123Z",
		"2026-11-14T08:30-05:30":         "2026-11-14T14:00:00.000Z",
		"2026-02-30":                     "",
		"14 Nov 2026":                    "",
		"2026-13-01T00:00:00Z":           "",
	} {
		got, ok := jsDateParse(in)
		if (want == "") == ok || (ok && jsDate(got) != want) {
			t.Errorf("%s: %v %v", in, jsDate(got), ok)
		}
	}
	local, ok := jsDateParse("2026-11-14 08:30:00")
	if !ok || local.Location() != time.Local || local.Hour() != 8 {
		t.Errorf("no offset: %v", local)
	}
}

func usageModel(t *testing.T, responses []string) (*httptest.Server, *modelLog) {
	t.Helper()
	log := &modelLog{}
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		content := responses[min(calls, len(responses)-1)]
		calls++
		log.bodies = append(log.bodies, string(body))
		mu.Unlock()
		usage := map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("content-type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": content}}}})
			end, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": usage})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\ndata: " + string(end) + "\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop"}}, "usage": usage})
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func metricJob(t *testing.T, db *pgxpool.Pool, payload string) string {
	t.Helper()
	return rowText(t, db, `insert into processing_job (org_id, kind, status, trigger, trigger_payload) values ('org', 'metric_refresh', 'queued', 'test', $1::jsonb) returning id::text`, payload)
}

const goodMetricReply = `{"reasoning":"sum of orders","headlineMetric":"$1.2M","headlineLabel":"Revenue TTM","insightText":"Revenue rose.","detailText":"Road bikes.",` +
	`"mood":"good","chartType":"kpi","chartData":[{"d":"TTM","v":1200000,"t":1000000}],"baselineValue":1000000,` +
	`"timeWindow":{"grain":"year","start":"2025-11-14","end":"2026-11-13","label":"TTM"}}`

func TestMetricRefreshAgentThroughServe(t *testing.T) {
	f, db := buildFront(t)
	a := f.API
	ctx := t.Context()
	bad := strings.Replace(goodMetricReply, `"headlineMetric":"$1.2M"`, `"headlineMetric":"N/A"`, 1)
	model, calls := usageModel(t, axReplies("not json at all", goodMetricReply, bad))
	a.turnResolve = fixedBackend(model.URL)
	tools := &fakeTools{reply: []any{}, gjTools: []any{}}
	var bindings []turnBinding
	a.jobTools = func(b turnBinding) toolBackend { bindings = append(bindings, b); return tools }
	seed(t, db, `insert into work_memory (org_id, scope, kind, text, pinned) values ('org', 'global', 'business_rule', 'Revenue means totaldue.', true)`)
	cardID := rowText(t, db, `insert into metric (org_id, role, slug, source, title, why, chart_hint) values ('org', 'CEO', 'revenue-ttm', 'bootstrap', 'Revenue TTM', 'Top line', 'kpi') returning id::text`)

	jobID := metricJob(t, db, `{"metricId":"`+cardID+`"}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", jobID); err == nil || err.Error() != "no mcp_url for org org" {
		t.Fatalf("no data source: %v", err)
	}
	if got := rowText(t, db, `select last_refresh_status || ' ' || last_refresh_error || ' ' || (last_refresh_job_id::text = $2)::text from metric where id = $1`, cardID, jobID); got != "failed no mcp_url for org org true" {
		t.Errorf("failed card: %q", got)
	}
	if got := rowText(t, db, `select count(*)::text from spend_reservation where org_id = 'org' and source = 'metric' and released_at is null`); got != "0" {
		t.Errorf("open reservations: %s", got)
	}

	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, is_default) values ('org', 'graphjin', 'shop', 'http://127.0.0.1:1/api/v1/graphql', 'http://127.0.0.1:1/api/v1/mcp', true)`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", jobID); err != nil {
		t.Fatal(err)
	}
	if got := jobState(t, db, jobID); got != "succeeded  Saving snapshot" {
		t.Errorf("job: %q", got)
	}
	bodies := calls.all()
	if len(bodies) != 6 || !strings.Contains(bodies[0], "You answer ONE dashboard card") || !strings.Contains(bodies[0], `\"cardSlug\": \"revenue-ttm\"`) ||
		!strings.Contains(bodies[0], "Revenue means totaldue.") || !strings.Contains(bodies[0], "service credential") ||
		!strings.Contains(bodies[3], "<previous-attempt-rejected>") || !strings.Contains(bodies[3], "no object braces found") {
		t.Errorf("model calls: %d", len(bodies))
	}
	if !strings.Contains(bodies[0], "mcp_neko_memory_search") || strings.Contains(bodies[0], "mcp_neko_memory_save") || strings.Contains(bodies[0], "mcp_neko_files_search") {
		t.Errorf("memory tools in the request")
	}
	if len(bindings) == 0 || bindings[0].Kind != "agent-job" || !bindings[0].MemoryReadOnly {
		t.Errorf("bindings: %+v", bindings)
	}
	if got := rowText(t, db, `select status || ' ' || (payload->>'headlineMetric') || ' ' || (payload->'timeWindow'->>'label') || ' ' || (value is null)::text from metric_snapshot where metric_id = $1`, cardID); got != "good $1.2M TTM true" {
		t.Errorf("snapshot: %q", got)
	}
	if got := rowText(t, db, `select last_refresh_status || ' ' || coalesce(last_refresh_error, '-') from metric where id = $1`, cardID); got != "ok -" {
		t.Errorf("card: %q", got)
	}
	if got := rowText(t, db, `select count(*) || ' ' || bool_and(l.reservation_id = r.id) || ' ' || min(l.task) || ' ' || min(l.source) || ' ' || bool_and(r.released_at is not null)
		from spend_ledger l join spend_reservation r on r.id = l.reservation_id where r.source = 'metric'`); got != "1 true turn metric true" {
		t.Errorf("spend: %q", got)
	}

	again := metricJob(t, db, `{"metricId":"`+cardID+`"}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", again); err == nil || !strings.Contains(err.Error(), `agent emitted error sentinel as headlineMetric: "N/A"`) {
		t.Fatalf("sentinel: %v", err)
	}
	if got := rowText(t, db, `select last_refresh_status from metric where id = $1`, cardID); got != "failed" {
		t.Errorf("card after sentinel: %q", got)
	}
	if got := rowText(t, db, `select count(*)::text from metric_snapshot where metric_id = $1`, cardID); got != "1" {
		t.Errorf("snapshots: %s", got)
	}
}

func TestMetricRefreshOffCancelsWithNoModelCall(t *testing.T) {
	f, db := buildFront(t)
	a := f.API
	a.MetricsRefresh = false
	model, calls := usageModel(t, axReplies(goodMetricReply))
	a.turnResolve = fixedBackend(model.URL)
	g := &fakeGateway{}
	a.Gateway = g.generate
	a.jobTools = func(turnBinding) toolBackend { t.Error("the cancelled refresh got tools"); return &fakeTools{} }
	cardID := rowText(t, db, `insert into metric (org_id, role, slug, source, title, why, chart_hint) values ('org', 'CEO', 'revenue-ttm', 'bootstrap', 'Revenue TTM', 'Top line', 'kpi') returning id::text`)
	jobID := metricJob(t, db, `{"metricId":"`+cardID+`"}`)
	seed(t, db, `update metric set last_refresh_status = 'pending', last_refresh_job_id = $2 where id = $1`, cardID, jobID)

	err := a.RunBuild(t.Context(), jobs.KindMetricRefresh, "org", jobID)
	if !errors.Is(err, jobs.ErrNoRetry) {
		t.Fatalf("RunBuild = %v, want a no-retry error", err)
	}
	if got := jobState(t, db, jobID); got != "cancelled "+metricsOffError+" " {
		t.Errorf("job: %q", got)
	}
	if got := rowText(t, db, `select coalesce(last_refresh_status, 'none') from metric where id = $1`, cardID); got != "none" {
		t.Errorf("card state: %s", got)
	}
	if len(calls.all()) != 0 || len(g.calls) != 0 {
		t.Errorf("model calls %d, gateway calls %d, want none", len(calls.all()), len(g.calls))
	}
	if got := rowText(t, db, `select count(*)::text from spend_reservation where org_id = 'org'`); got != "0" {
		t.Errorf("spend reservations: %s", got)
	}
}

func TestMetricRefreshQuestionAndLimits(t *testing.T) {
	f, db := buildFront(t)
	a := f.API
	ctx := t.Context()
	model, calls := usageModel(t, axReplies(goodMetricReply, goodMetricReply))
	a.turnResolve = fixedBackend(model.URL)
	a.jobTools = func(turnBinding) toolBackend { return &fakeTools{reply: []any{}, gjTools: []any{}} }
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, is_default) values ('org', 'graphjin', 'shop', 'http://127.0.0.1:1/api/v1/graphql', 'http://127.0.0.1:1/api/v1/mcp', true)`)

	jobID := metricJob(t, db, `{"question":"What was revenue last year?","role":"CFO","slug":"chat-revenue"}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", jobID); err != nil {
		t.Fatal(err)
	}
	if got := rowText(t, db, `select source || ' ' || active || ' ' || title || ' ' || why || ' ' || chart_hint || ' ' || last_refresh_status || ' ' || (created_by_job::text = $1)::text
		from metric where slug = 'chat-revenue'`, jobID); got != "chat false What was revenue last year? What was revenue last year? bar ok true" {
		t.Errorf("chat metric: %q", got)
	}
	if body := calls.all()[0]; !strings.Contains(body, `\"userQuestion\": \"What was revenue last year?\"`) {
		t.Errorf("no question in the prompt")
	}
	rerun := metricJob(t, db, `{"question":"What was revenue last year?","role":"CFO","slug":"chat-revenue"}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", rerun); err != nil {
		t.Fatal(err)
	}
	if got := rowText(t, db, `select count(*) || ' ' || bool_and(created_by_job::text = $1) from metric where slug = 'chat-revenue'`, rerun); got != "1 true" {
		t.Errorf("rerun: %q", got)
	}

	for payload, want := range map[string]string{
		`{}`: "has neither metricId nor question",
		`{"metricId":"00000000-0000-4000-8000-000000000000"}`: "metric 00000000-0000-4000-8000-000000000000 not found",
	} {
		if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", metricJob(t, db, payload)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", payload, err)
		}
	}

	seed(t, db, `update spend_limit set org_hourly_micros = 1 where org_id = 'org' and workflow_id is null`)
	blocked := metricJob(t, db, `{"question":"Margin?","role":"CFO","slug":"chat-margin"}`)
	err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", blocked)
	if err == nil || !strings.Contains(err.Error(), "hourly spending limit") {
		t.Fatalf("over budget: %v", err)
	}
	if got := rowText(t, db, `select last_refresh_status from metric where slug = 'chat-margin'`); got != "failed" {
		t.Errorf("blocked card: %q", got)
	}
	if got := rowText(t, db, `select count(*)::text from behavior_alert where org_id = 'org' and kind = 'spend.budget_blocked'`); got != "1" {
		t.Errorf("blocked alerts: %s", got)
	}
	if n := len(calls.all()); n != 6 {
		t.Errorf("the blocked refresh called the model: %d calls", n)
	}
}

func TestMetricRefreshSavedQuery(t *testing.T) {
	f, db := buildFront(t)
	a := f.API
	ctx := t.Context()
	var mu sync.Mutex
	var reply string
	var seen []string
	gj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("X-Role")+" "+string(body))
		out := reply
		mu.Unlock()
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(gj.Close)
	set := func(s string) { mu.Lock(); reply = s; mu.Unlock() }
	a.turnResolve = func(context.Context, string) (*llm.AxBackendConfig, map[string]string, error) {
		t.Error("a saved-query metric called the model")
		return nil, nil, nil
	}
	definition := `{"title":"Net revenue","unit":"currency","directionGood":"up","execution":{"mode":"saved_query","source":"shop","query":"net_revenue",` +
		`"document":"query net_revenue { sales { total } }","freshnessSeconds":3600,"result":{"kind":"scalar","path":"sales.total"},"runtime":{"windowDays":7}}}`
	cardID := rowText(t, db, `insert into metric (org_id, role, slug, source, title, chart_hint, execution_mode, definition_json, definition_version, definition_hash)
		values ('org', 'CEO', 'net-revenue', 'pack', 'Net revenue', 'kpi', 'saved_query', $1::jsonb, 1, 'h1') returning id::text`, definition)

	job := metricJob(t, db, `{"metricId":"`+cardID+`"}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", job); err == nil || err.Error() != "saved-query metric has no enabled GraphJin source" {
		t.Fatalf("no source: %v", err)
	}
	seed(t, db, `insert into data_source (org_id, kind, name, graphql_url, mcp_url, is_default, enabled) values ('org', 'graphjin', 'shop', $1, $1, true, true)`, gj.URL)

	set(`{"data":{"sales":{"total":1500.25,"at":"2026-11-14T08:00:00Z"}}}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", job); err != nil {
		t.Fatal(err)
	}
	set(`{"data":{"sales":{"total":3000.5}}}`)
	second := metricJob(t, db, `{"metricId":"`+cardID+`"}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", second); err != nil {
		t.Fatal(err)
	}
	if got := rowText(t, db, `select string_agg(value::text || ' ' || coalesce(baseline::text, '-') || ' ' || coalesce(round(delta_pct, 1)::text, '-') || ' ' || status || ' ' ||
		definition_hash || ' ' || coalesce(source_freshness_at::text, '-') || ' ' || (payload->>'headlineMetric') || ' ' || (payload->'timeWindow'->>'label'), '|' order by captured_at)
		from metric_snapshot where metric_id = $1`, cardID); got != "1500.25 - - watch h1 2026-11-14 08:00:00+00 $1,500.25 Last 7 days|3000.5 1500.25 100.0 good h1 - $3,000.50 Last 7 days" {
		t.Errorf("snapshots: %q", got)
	}
	if len(seen) != 2 || !strings.HasPrefix(seen[0], "/api/v1/graphql service ") || !strings.Contains(seen[0], `"query":"query net_revenue { sales { total } }"`) || !strings.Contains(seen[0], `"storeIds":[1]`) {
		t.Errorf("graphjin calls: %q", seen)
	}

	set(`{"errors":[{"message":"table sales not found"}]}`)
	failed := metricJob(t, db, `{"metricId":"`+cardID+`"}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", failed); err == nil || err.Error() != "saved-query metric failed: table sales not found" {
		t.Fatalf("graphql error: %v", err)
	}
	set(`{"data":null}`)
	if err := a.RunBuild(ctx, jobs.KindMetricRefresh, "org", failed); err == nil || err.Error() != "saved-query metric returned no data" {
		t.Fatalf("no data: %v", err)
	}
	if got := rowText(t, db, `select last_refresh_status || ' ' || last_refresh_error from metric where id = $1`, cardID); got != "failed saved-query metric returned no data" {
		t.Errorf("card: %q", got)
	}
	if got := jobState(t, db, failed); got != "failed saved-query metric returned no data Running reviewed saved query" {
		t.Errorf("job: %q", got)
	}
}
