package webfront

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/embedding"
	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

const plantedProviderKey = "sk-planted-provider-77a1"

type fakeGateway struct {
	mu    sync.Mutex
	calls []gatewayCall
	reply map[string]any
	err   error
	fb    bool
	usage inference.Usage
}

type gatewayCall struct {
	request          inference.Request
	key, fallbackKey string
}

func (g *fakeGateway) generate(_ context.Context, request inference.Request, key, fallbackKey string) (inference.Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, gatewayCall{request, key, fallbackKey})
	if g.err != nil {
		return inference.Result{}, g.err
	}
	return inference.Result{Outputs: g.reply, Usage: g.usage, Fallback: g.fb}, nil
}

func (g *fakeGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func hostFront(t *testing.T) (*Front, *pgxpool.Pool, *fakeGateway) {
	t.Helper()
	f, _, db := signinFront(t, liveScalekit)
	g := &fakeGateway{reply: map[string]any{"reply": "READY"}}
	f.API.Gateway = g.generate
	f.API.GoogleToken = func(context.Context) (string, error) { return "adc-token", nil }
	t.Setenv("OPENNEKO_SERVE_SOCKET", "")
	t.Setenv("OPENNEKO_SERVE_URL", "")
	t.Setenv("OPENNEKO_SANDBOX_URL", "")
	t.Setenv("NEKO_EMBEDDING_URL", "")
	t.Setenv("NEKO_EMBEDDING_MODEL", "")
	servicesMu.Lock()
	clear(servicesCache)
	servicesMu.Unlock()
	return f, db, g
}

func seedPrimary(t *testing.T, db *pgxpool.Pool, provider, model, config, secrets string) {
	t.Helper()
	seed(t, db, fmt.Sprintf(`insert into llm_provider_config (org_id, scope, provider, model, enabled, config, secrets)
		values ('org', 'primary', '%s', '%s', true, '%s', '%s')`, provider, model, config, secrets))
}

func TestHostRoutesAdminOnly(t *testing.T) {
	f, _, g := hostFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/settings/provider/test", `{"scope":"primary","provider":"openai","secrets":{"apiKey":"k"}}`},
		{"GET", "/api/v1/admin/services", ""},
		{"POST", "/api/v1/onboarding/submit", `{"companyName":"Acme"}`},
	} {
		if w := call(f, c.method, c.path, c.body, sessionCookie("member")); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if g.count() != 0 {
		t.Errorf("a member reached the model: %d calls", g.count())
	}
}

func TestProviderTestPrimary(t *testing.T) {
	f, db, g := hostFront(t)
	admin := sessionCookie("admin")
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"`+plantedProviderKey+`"}`)

	if w := call(f, "POST", "/api/v1/settings/provider/test", `{}`, admin); w.Code != 400 || decode(t, w)["error"] != "missing draft scope" {
		t.Errorf("no scope: %d %s", w.Code, w.Body.String())
	}

	w := call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"primary","provider":"openai","model":"gpt-5-mini","enabled":true,"secrets":{"apiKey":"   "}}`, admin)
	got := decode(t, w)
	if w.Code != 200 || got["ok"] != true || got["provider"] != "openai" || got["model"] != "gpt-5-mini" || got["source"] != "draft" {
		t.Fatalf("test: %d %v", w.Code, got)
	}
	if strings.Contains(w.Body.String(), plantedProviderKey) {
		t.Errorf("the answer holds the key")
	}
	last := g.calls[len(g.calls)-1]
	if last.key != plantedProviderKey || last.request.Route.Provider != "openai" || last.request.Route.Model != "gpt-5-mini" || last.fallbackKey != "" {
		t.Errorf("a blank draft key must keep the stored key: %+v", last)
	}
	if last.request.Values["prompt"] != "Reply with READY only." || !strings.Contains(last.request.Signature, "reply:string") {
		t.Errorf("program %+v", last.request)
	}

	call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"primary","provider":"openai","secrets":{"apiKey":"sk-typed-0001"}}`, admin)
	if last := g.calls[len(g.calls)-1]; last.key != "sk-typed-0001" || last.request.Route.Model != "gpt-4.1-mini" {
		t.Errorf("a typed key wins and the model defaults: %+v", last)
	}

	call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"primary","provider":"ollama","model":"llama3","config":{"url":"http://localhost:11434"}}`, admin)
	if last := g.calls[len(g.calls)-1]; last.key != "none" || last.request.Route.URL != "http://host.docker.internal:11434/v1" {
		t.Errorf("a keyless provider: %+v", last)
	}

	w = call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"primary","provider":"anthropic","model":"claude"}`, admin)
	if w.Code != 500 || decode(t, w)["error"] != "anthropic requires an API key" {
		t.Errorf("a provider change does not borrow the stored key: %d %s", w.Code, w.Body.String())
	}

	g.reply = map[string]any{"reply": " "}
	w = call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"primary","provider":"openai"}`, admin)
	if w.Code != 500 || decode(t, w)["error"] != "Provider test returned an empty response" {
		t.Errorf("empty reply: %d %s", w.Code, w.Body.String())
	}

	var rows int
	if err := db.QueryRow(t.Context(), `select count(*) from spend_ledger where org_id = 'org'`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a provider test writes no spend row: %d %v", rows, err)
	}
}

func TestProviderTestResearch(t *testing.T) {
	f, db, g := hostFront(t)
	admin := sessionCookie("admin")
	var auth, body string
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		body = string(raw[:n])
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	t.Cleanup(srv.Close)
	f.API.ResearchURL = srv.URL
	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, model, enabled, config, secrets)
		values ('org', 'research', 'perplexity', 'sonar-deep-research', true, '{}', '{"apiKey":"pplx-stored-key"}')`)

	w := call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"research","provider":"perplexity","enabled":true}`, admin)
	got := decode(t, w)
	if w.Code != 200 || got["provider"] != "perplexity" || got["model"] != "sonar-deep-research" || got["source"] != "draft" {
		t.Fatalf("research: %d %v", w.Code, got)
	}
	if auth != "Bearer pplx-stored-key" || !strings.Contains(body, `"model":"sonar-deep-research"`) || !strings.Contains(body, "Reply with READY only.") {
		t.Errorf("request %q %q", auth, body)
	}

	status = 401
	w = call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"research","provider":"perplexity","enabled":true}`, admin)
	if w.Code != 500 || decode(t, w)["error"] != `perplexity 401 Unauthorized: {"error":"bad key"}` {
		t.Errorf("refused: %d %s", w.Code, w.Body.String())
	}

	w = call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"research","provider":"disabled","enabled":false}`, admin)
	if got := decode(t, w); w.Code != 200 || got["provider"] != "disabled" || got["model"] != "" {
		t.Errorf("disabled: %d %v", w.Code, got)
	}
	w = call(f, "POST", "/api/v1/settings/provider/test", `{"scope":"elsewhere","provider":"perplexity"}`, admin)
	if w.Code != 500 || decode(t, w)["error"] != "Expected research config draft, got elsewhere" {
		t.Errorf("unknown scope: %d %s", w.Code, w.Body.String())
	}
	if g.count() != 0 {
		t.Errorf("the research test called the model gateway")
	}
}

func servicesByID(t *testing.T, f *Front, query string) map[string]map[string]any {
	t.Helper()
	w := call(f, "GET", "/api/v1/admin/services"+query, "", sessionCookie("admin"))
	if w.Code != 200 {
		t.Fatalf("services: %d %s", w.Code, w.Body.String())
	}
	out := map[string]map[string]any{}
	var order []string
	for _, s := range decode(t, w)["services"].([]any) {
		m := s.(map[string]any)
		out[m["id"].(string)] = m
		order = append(order, m["id"].(string))
	}
	if strings.Join(order, ",") != "inference,embedding,sandbox,file-store" {
		t.Errorf("order %v", order)
	}
	return out
}

func TestAdminServicesOff(t *testing.T) {
	f, _, g := hostFront(t)
	got := servicesByID(t, f, "")
	for id, detail := range map[string]string{
		"inference":  "No model is set up. Agent turns and summaries stop until an admin adds one in Admin, Settings, Models.",
		"embedding":  "Search uses Postgres full-text search.",
		"sandbox":    "The terminal and the script skills are off.",
		"file-store": "File search is off: OPENNEKO_SERVE_URL is not set.",
	} {
		if s := got[id]; s["state"] != "off" || s["detail"] != detail || s["where"] != nil || s["checkedAt"] == "" {
			t.Errorf("%s: %v", id, s)
		}
	}
	if g.count() != 0 {
		t.Errorf("an off service was checked")
	}
}

func TestAdminServicesChecks(t *testing.T) {
	f, db, g := hostFront(t)
	seedPrimary(t, db, "openai-compatible", "qwen3", `{"url":"https://models.example.test/v1"}`, `{"apiKey":"`+plantedProviderKey+`"}`)

	sandboxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" || r.Header.Get("Authorization") != "Bearer sandbox-key-1" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(sandboxSrv.Close)
	serveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"file_store":"s3","file_store_ok":true,"file_store_where":"s3://bucket"}`))
	}))
	t.Cleanup(serveSrv.Close)
	t.Setenv("OPENNEKO_SANDBOX_URL", sandboxSrv.URL)
	t.Setenv("OPENNEKO_SANDBOX_KEY", "sandbox-key-1")
	t.Setenv("OPENNEKO_SERVE_URL", serveSrv.URL+"/")
	t.Setenv("NEKO_EMBEDDING_URL", "https://embed.example.test/v1/")
	t.Setenv("NEKO_EMBEDDING_MODEL", "embeddinggemma")
	t.Setenv("NEKO_EMBEDDING_DIMENSIONS", "4")
	var embedReq embedding.Request
	f.API.Embedder = func(_ context.Context, r embedding.Request, _ string) (embedding.Result, error) {
		embedReq = r
		return embedding.Result{Vectors: [][]float64{{1, 2, 3, 4}}}, nil
	}

	got := servicesByID(t, f, "")
	want := map[string][3]string{
		"inference":  {"ok", "openai-compatible · qwen3 · https://models.example.test", "The default model answered a test prompt."},
		"embedding":  {"ok", "https://embed.example.test · embeddinggemma", "The service returned a vector of 4 numbers."},
		"sandbox":    {"ok", serviceWhere(sandboxSrv.URL), "The sandbox accepted the key."},
		"file-store": {"ok", "s3://bucket", "Files are in the bucket."},
	}
	for id, w := range want {
		if s := got[id]; s["state"] != w[0] || s["where"] != w[1] || s["detail"] != w[2] {
			t.Errorf("%s: %v", id, s)
		}
	}
	if embedReq.URL != "https://embed.example.test/v1" || embedReq.Dimensions != 4 || embedReq.Input[0] != "OpenNeko status check" {
		t.Errorf("embed request %+v", embedReq)
	}
	if g.count() != 1 || g.calls[0].key != plantedProviderKey {
		t.Fatalf("gateway calls %d", g.count())
	}

	servicesByID(t, f, "")
	if g.count() != 1 {
		t.Errorf("a second look within a minute made a paid call: %d", g.count())
	}

	g.err = errors.New("401 from https://models.example.test: key " + plantedProviderKey + " refused")
	t.Setenv("OPENNEKO_SANDBOX_KEY", "wrong-key")
	got = servicesByID(t, f, "?refresh=1")
	if g.count() != 2 {
		t.Errorf("refresh=1 checks again: %d", g.count())
	}
	if s := got["inference"]; s["state"] != "failing" || s["detail"] != "401 from https://models.example.test: key [redacted] refused" {
		t.Errorf("inference failing: %v", s)
	}
	if s := got["sandbox"]; s["state"] != "failing" || s["detail"] != "the sandbox refused the key" {
		t.Errorf("sandbox failing: %v", s)
	}
}

func TestServiceWhere(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:p@host.example:8443/v1/x?key=1#f": "https://host.example:8443",
		"unix:///run/sandbox.sock":                   "unix:/run/sandbox.sock",
		"not a url":                                  "an address that is not a URL",
	} {
		if got := serviceWhere(in); got != want {
			t.Errorf("serviceWhere(%q) = %q", in, got)
		}
	}
	long := strings.Repeat("é", 400)
	if got := plainError(errors.New(long), nil); len([]rune(got)) != 300 || !strings.HasSuffix(got, "...") {
		t.Errorf("long error cut to %d", len([]rune(got)))
	}
}

func onboardingFront(t *testing.T) (*Front, *pgxpool.Pool) {
	t.Helper()
	f, db, _ := hostFront(t)
	seedPgBoss(t, db, f)
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"`+plantedProviderKey+`"}`)
	return f, db
}

const acme = `{"companyName":"AdventureWorks Cycles","companyNote":"%s","fiscalYearStartMonth":7,"activeSeats":["CEO","CFO"],"priorities":["Defend wholesale margins"]}`

func TestOnboardingSubmit(t *testing.T) {
	f, db := onboardingFront(t)
	admin := sessionCookie("admin")
	w := call(f, "POST", "/api/v1/onboarding/submit", fmt.Sprintf(acme, "We sell bikes"), admin)
	jobID, _ := decode(t, w)["jobId"].(string)
	if w.Code != 200 || jobID == "" {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	var name, note, seats, priorities, step string
	var fy int
	if err := db.QueryRow(t.Context(), `select o.name, w.company_note, w.fiscal_year_start_month, w.active_seats::text, w.priorities::text, w.step
		from organization o join onboarding_wizard w on w.org_id = o.id where o.id = 'org'`).Scan(&name, &note, &fy, &seats, &priorities, &step); err != nil {
		t.Fatal(err)
	}
	if name != "AdventureWorks Cycles" || note != "We sell bikes" || fy != 7 || seats != "{CEO,CFO}" || priorities != `{"Defend wholesale margins"}` || step != "submitting" {
		t.Errorf("rows %q %q %d %s %s %s", name, note, fy, seats, priorities, step)
	}
	queued := jobs(t, db, f, queueBusinessProfile)
	if len(queued) != 1 || queued[0].RetryLimit != 0 || queued[0].Data != `{"orgId": "org", "processingJobId": "`+jobID+`"}` {
		t.Errorf("queued %+v", queued)
	}

	second := call(f, "POST", "/api/v1/onboarding/submit", fmt.Sprintf(acme, "second"), admin)
	if got := decode(t, second); second.Code != 200 || got["jobId"] != jobID || got["ok"] != true {
		t.Errorf("a submit while the build is queued reports that build: %d %v", second.Code, got)
	}
	if err := db.QueryRow(t.Context(), `select company_note from onboarding_wizard where org_id = 'org'`).Scan(&note); err != nil || note != "We sell bikes" {
		t.Errorf("in-flight submit changed the wizard row: %q %v", note, err)
	}

	seed(t, db, `update processing_job set status = 'completed' where org_id = 'org'`)
	if w := call(f, "POST", "/api/v1/onboarding/submit", fmt.Sprintf(acme, "second"), admin); w.Code != 200 {
		t.Fatalf("resubmit: %d %s", w.Code, w.Body.String())
	}
	var wizards int
	if err := db.QueryRow(t.Context(), `select count(*), max(company_note) from onboarding_wizard where org_id = 'org'`).Scan(&wizards, &note); err != nil || wizards != 1 || note != "second" {
		t.Errorf("resubmit replaces the row: %d %q %v", wizards, note, err)
	}
	if n := len(jobs(t, db, f, queueBusinessProfile)); n != 2 {
		t.Errorf("jobs after resubmit: %d", n)
	}
}

func TestOnboardingSubmitRefusals(t *testing.T) {
	f, db := onboardingFront(t)
	admin := sessionCookie("admin")
	none := func(label string) {
		t.Helper()
		var jobsN, wizards int
		if err := db.QueryRow(t.Context(), `select (select count(*) from processing_job where org_id = 'org'), (select count(*) from onboarding_wizard where org_id = 'org')`).Scan(&jobsN, &wizards); err != nil || jobsN != 0 || wizards != 0 {
			t.Errorf("%s wrote rows: %d jobs, %d wizards %v", label, jobsN, wizards, err)
		}
	}
	for _, body := range []string{`{}`, `{"companyName":"   "}`, `{"companyName":7}`} {
		if w := call(f, "POST", "/api/v1/onboarding/submit", body, admin); w.Code != 400 || decode(t, w)["error"] != "companyName is required" {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	none("an empty name")

	t.Setenv("OPENNEKO_SERVE_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	w := call(f, "POST", "/api/v1/onboarding/submit", fmt.Sprintf(acme, "x"), admin)
	if got := decode(t, w); w.Code != 503 || got["code"] != "agent_runtime_unavailable" || got["error"] != agentRuntimeUnavailable {
		t.Errorf("serve down: %d %v", w.Code, got)
	}
	if strings.Contains(w.Body.String(), "absent.sock") {
		t.Errorf("the answer names a local path")
	}
	none("a submit with serve down")

	socket := filepath.Join(t.TempDir(), "serve.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	t.Setenv("OPENNEKO_SERVE_SOCKET", socket)
	seed(t, db, `update llm_provider_config set provider = 'vertex', model = 'm', config = '{"projectId":"p1","region":"global"}' where org_id = 'org'`)
	f.API.GoogleToken = func(context.Context) (string, error) { return "", errors.New("no ADC") }
	if w := call(f, "POST", "/api/v1/onboarding/submit", fmt.Sprintf(acme, "x"), admin); w.Code != 503 {
		t.Errorf("a Vertex key that does not load: %d %s", w.Code, w.Body.String())
	}
	none("a submit with no model key")

	seed(t, db, `update llm_provider_config set provider = 'openai', model = 'gpt-5', config = '{}', secrets = '{}' where org_id = 'org'`)
	w = call(f, "POST", "/api/v1/onboarding/submit", fmt.Sprintf(acme, "x"), admin)
	if got := decode(t, w); w.Code != 409 || got["code"] != "primary_provider_unavailable" {
		t.Errorf("no key: %d %v", w.Code, got)
	}
	none("a submit with no provider")
}

func TestHostGenerateSpend(t *testing.T) {
	f, db, g := hostFront(t)
	seedPrimary(t, db, "openai", "gpt-5", `{}`, `{"apiKey":"primary-key"}`)
	seed(t, db, `insert into llm_model (org_id, name, provider, model, config, secrets, enabled, input_usd_per_1m, output_usd_per_1m) values
		('org', 'main', 'openai-compatible', 'qwen3', '{"url":"https://models.example.test/v1"}', '{"apiKey":"main-key"}', true, null, null),
		('org', 'spare', 'groq', 'llama', '{}', '{"apiKey":"spare-key"}', true, 2, 10)`)
	seed(t, db, `insert into llm_task_route (org_id, task, model_id, fallback_model_id)
		select 'org', 'classify', (select id from llm_model where name = 'main'), (select id from llm_model where name = 'spare')`)
	seed(t, db, `update spend_limit set org_hourly_micros = 1000, warn_percent = 50 where org_id = 'org' and workflow_id is null`)

	g.fb = true
	g.reply = map[string]any{"answer": "42"}
	g.usage = inference.Usage{Requests: 1, Reported: 1, InputTokens: 100, OutputTokens: 50, TotalTokens: 150, Coverage: "complete"}
	result, err := f.API.hostCalls().Generate(t.Context(), "org", "classify", llm.Program{Signature: "q:string -> answer:string", Instruction: "Answer."}, map[string]any{"q": "?"})
	if err != nil {
		t.Fatal(err)
	}
	call := g.calls[0]
	if call.key != "main-key" || call.fallbackKey != "spare-key" || call.request.Route.URL != "https://models.example.test/v1" || call.request.Fallback == nil || call.request.Fallback.Provider != "groq" {
		t.Errorf("call %+v", call)
	}
	if result.Provider != "groq" || result.Model != "llama" || !result.Fallback || result.Outputs["answer"] != "42" {
		t.Errorf("result %+v", result)
	}
	var (
		provider, model, priced, source, version, task, src string
		micros, tokens                                      int64
	)
	if err := db.QueryRow(t.Context(), `select provider, model, cost_micros, tokens, priced, cost_source, pricing_version, task, source
		from spend_ledger where org_id = 'org'`).Scan(&provider, &model, &micros, &tokens, &priced, &source, &version, &task, &src); err != nil {
		t.Fatal(err)
	}
	if provider != "groq" || model != "llama" || micros != 700 || tokens != 150 || priced != "estimated" || source != "model_record" || version != "model_record" || task != "classify" || src != "system" {
		t.Errorf("ledger %s %s %d %d %s %s %s %s %s", provider, model, micros, tokens, priced, source, version, task, src)
	}
	var kind, details string
	if err := db.QueryRow(t.Context(), `select kind, details::text from behavior_alert where org_id = 'org'`).Scan(&kind, &details); err != nil ||
		kind != "spend.budget_warning" || !strings.Contains(details, "Spend is at 70% of the $0.00 hourly budget for this organization") {
		t.Errorf("warning %q %q %v", kind, details, err)
	}

	g.fb = false
	g.usage = inference.Usage{Requests: 1, Coverage: "unavailable"}
	if _, err := f.API.hostCalls().Generate(t.Context(), "org", "compact", llm.Program{Signature: "q:string -> a:string"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `select provider, priced, cost_micros, coalesce(tokens, -1) from spend_ledger where task = 'compact'`).Scan(&provider, &priced, &micros, &tokens); err != nil ||
		provider != "openai" || priced != "unpriced" || micros != 0 || tokens != -1 {
		t.Errorf("an unpriced call on the default route: %s %s %d %d %v", provider, priced, micros, tokens, err)
	}
	if last := g.calls[len(g.calls)-1]; last.key != "primary-key" || last.request.Fallback != nil {
		t.Errorf("default route %+v", last)
	}
}
