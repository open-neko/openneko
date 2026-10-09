package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
	"github.com/open-neko/openneko/apps/ax-harness/internal/budgettriage"
)

func TestDedicatedTypesafeRouteUsesNativePathAndBrokerCredential(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/route/triage/v1/systemone" || r.Header.Get("Authorization") != "Bearer broker-alias" {
			t.Errorf("native route path or credential mismatch: path=%q", r.URL.Path)
		}
		var request ax.TypesafeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "jev-fixture" {
			t.Errorf("native request mismatch: %+v %v", request, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"workload": map[string]any{
			"type": "choice", "choice": "multi_step", "confidence": 0.8,
			"probabilities": map[string]float64{"short_answer": .05, "multi_step": .8, "artifact_pipeline": .1, "uncertain": .05}}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5}})
	}))
	defer server.Close()
	price := &agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	raw, _ := json.Marshal(routeConfig{Context: "main", Executor: "main", Responder: "main", Triage: "triage", PricingVersion: "test-v1",
		BudgetPolicy: &budgettriage.Policy{Version: "test-v1", Short: budgettriage.Limits{MaxModelCalls: 4, MaxModelTokens: 8_000, MaxCostMicros: 4_000},
			MultiStep: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 40_000, MaxCostMicros: 20_000},
			Artifact:  budgettriage.Limits{MaxModelCalls: 32, MaxModelTokens: 100_000, MaxCostMicros: 100_000}},
		Routes: []modelRoute{
			{Key: "main", Model: "fixture", URL: server.URL + "/v1", APIKeyEnv: "MAIN_KEY", Price: price},
			{Key: "triage", Model: "jev-fixture", URL: server.URL + "/route/triage", APIKeyEnv: "TRIAGE_KEY", Price: price},
		}})
	triage, err := loadTriageClient(string(raw), func(name string) string {
		if name == "TRIAGE_KEY" {
			return "broker-alias"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := triage.Client.SystemOne(context.Background(), ax.TypesafeRequest{Model: "jev-fixture", State: ax.Object("task_summary", "Find a result"),
		Questions: map[string]ax.TypesafeQuestion{"workload": {Type: "choice", Criteria: ax.Object("short_answer", "short", "multi_step", "steps", "artifact_pipeline", "artifact", "uncertain", "unknown")}}}, nil)
	if err != nil || response == nil || response.Model != "jev-fixture" || calls != 1 {
		t.Fatalf("native Typesafe route response=%+v err=%v calls=%d", response, err, calls)
	}
}

func TestHostRoutesSelectAxStagesAndPinResume(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	t.Setenv("HARNESS_CHEAP_KEY", "cheap-secret")
	t.Setenv("HARNESS_WORK_KEY", "work-secret")
	var requested []string
	serve := func(routeKey, expectedModel, expectedKey string, responses []string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+expectedKey {
				t.Errorf("wrong credential for %s", expectedModel)
			}
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Model != expectedModel {
				t.Errorf("route sent %q to %s", body.Model, expectedModel)
			}
			requested = append(requested, routeKey)
			if len(responses) == 0 {
				http.Error(w, "too many calls", 400)
				return
			}
			content := responses[0]
			responses = responses[1:]
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
		}))
	}
	cheap := serve("cheap", "fixture", "cheap-secret", []string{`{"javascriptCode":"final('Find reference', {})"}`})
	defer cheap.Close()
	work := serve("work", "fixture", "work-secret", []string{`{"javascriptCode":"final('Report reference', {reference:'REF-42'});"}`, `Answer: REF-42`})
	defer work.Close()
	config := func(responder string) string {
		b, _ := json.Marshal(routeConfig{Context: "cheap", Executor: "work", Responder: responder, Routes: []modelRoute{
			{Key: "cheap", Model: "fixture", URL: cheap.URL, APIKeyEnv: "HARNESS_CHEAP_KEY"},
			{Key: "work", Model: "fixture", URL: work.URL, APIKeyEnv: "HARNESS_WORK_KEY"},
		}})
		return string(b)
	}
	t.Setenv("HARNESS_MODEL_ROUTES", config("work"))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(request), &out)
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v output=%s", code, err, out.String())
	}
	if got := strings.Join(requested, ","); got != "cheap,work,work" {
		t.Fatalf("stage routes=%s", got)
	}
	rawEvents := out.String()
	copyOfEvents := bytes.NewBufferString(rawEvents)
	var modelEvents []string
	var modelStages []string
	var stageUsage []string
	for _, event := range events(t, copyOfEvents) {
		if event.Type == "model.request.started" {
			modelEvents = append(modelEvents, event.Origin+":"+event.Name)
			modelStages = append(modelStages, event.Stage)
		}
		if event.Type == "model.stage_usage" {
			if event.StageUsage == nil || event.StageUsage.Requests != 1 || event.StageUsage.Reported != 1 || event.StageUsage.TotalTokens != 15 || event.StageUsage.Coverage != "complete" {
				t.Fatalf("invalid stage projection: %+v", event)
			}
			stageUsage = append(stageUsage, event.Name)
		}
	}
	if got := strings.Join(modelEvents, ","); got != "cheap:fixture,work:fixture,work:fixture" {
		t.Fatalf("model receipts=%s", got)
	}
	if got := strings.Join(modelStages, ","); got != "distiller,executor,responder" {
		t.Fatalf("model stages=%q", modelStages)
	}
	if got := strings.Join(stageUsage, ","); got != "distiller,executor,responder" {
		t.Fatalf("stage usage=%s", got)
	}
	for _, secret := range []string{"cheap-secret", "work-secret"} {
		if strings.Contains(rawEvents, secret) {
			t.Fatal("credential leaked in events")
		}
	}
	var replay bytes.Buffer
	code, err = run(context.Background(), strings.NewReader(request), &replay)
	if code != 0 || err != nil || replay.String() != rawEvents || len(requested) != 3 {
		t.Fatalf("same-route replay failed: code=%d err=%v", code, err)
	}
	t.Setenv("HARNESS_MODEL_ROUTES", config("cheap"))
	var changed bytes.Buffer
	code, err = run(context.Background(), strings.NewReader(request), &changed)
	if code != 1 || err == nil || len(requested) != 3 {
		t.Fatalf("changed route replay admitted: code=%d err=%v", code, err)
	}
}

func TestExecutorErrorEscalatesOnlyLaterApprovedModelCalls(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	var selected []string
	serve := func(alias, key string, responses []string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				t.Errorf("wrong route credential for %s", alias)
			}
			selected = append(selected, alias)
			if len(responses) == 0 {
				http.Error(w, "unexpected model call", http.StatusBadRequest)
				return
			}
			content := responses[0]
			responses = responses[1:]
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
		}))
	}
	contextServer := serve("context", "context-secret", []string{`{"javascriptCode":"final('Plan',{});"}`, `Answer: Done`})
	defer contextServer.Close()
	baseline := serve("base", "base-secret", []string{`{"javascriptCode":"throw new Error('retry this actor step');"}`})
	defer baseline.Close()
	strong := serve("strong", "strong-secret", []string{`{"javascriptCode":"final('Done',{});"}`})
	defer strong.Close()
	t.Setenv("HARNESS_CONTEXT_KEY", "context-secret")
	t.Setenv("HARNESS_BASE_KEY", "base-secret")
	t.Setenv("HARNESS_STRONG_KEY", "strong-secret")
	price := func(n int64) *agent.TokenPrice {
		return &agent.TokenPrice{InputMicrosPerMillion: n, OutputMicrosPerMillion: n}
	}
	raw, _ := json.Marshal(routeConfig{Context: "context", Executor: "base", ExecutorEscalation: "strong", ExecutorAfterErrors: 1,
		Responder: "context", PricingVersion: "test-v1", Routes: []modelRoute{
			{Key: "context", Model: "fixture-context", URL: contextServer.URL, APIKeyEnv: "HARNESS_CONTEXT_KEY", Price: price(1_000_000)},
			{Key: "base", Model: "fixture-base", URL: baseline.URL, APIKeyEnv: "HARNESS_BASE_KEY", Price: price(2_000_000)},
			{Key: "strong", Model: "fixture-strong", URL: strong.URL, APIKeyEnv: "HARNESS_STRONG_KEY", Price: price(4_000_000)},
		}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Complete the task","max_cost_micros":50000}`), &out)
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v events=%s", code, err, out.String())
	}
	if got := strings.Join(selected, ","); got != "context,base,strong,context" {
		t.Fatalf("actual route order=%s", got)
	}
	rawEvents := out.String()
	var started []string
	var failed int
	var terminal *agent.Result
	for _, event := range events(t, &out) {
		switch event.Type {
		case "model.request.started":
			started = append(started, event.Origin+":"+event.Name)
		case "executor.step.failed":
			failed++
			if event.CallID != 2 || event.Error != "actor_code_error" {
				t.Fatalf("invalid error-turn receipt: %+v", event)
			}
		case "run.finished":
			terminal = event.Result
		}
	}
	if got := strings.Join(started, ","); got != "context:fixture-context,base:fixture-base,strong:fixture-strong,context:fixture-context" {
		t.Fatalf("model admission routes=%s", got)
	}
	if failed != 1 || terminal == nil || terminal.Status != "completed" || terminal.Cost == nil || terminal.Cost.ChargedMicros != 120 {
		t.Fatalf("failed=%d terminal=%+v", failed, terminal)
	}
	var replay bytes.Buffer
	code, err = run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Complete the task","max_cost_micros":50000}`), &replay)
	if code != 0 || err != nil || replay.String() != rawEvents || len(selected) != 4 {
		t.Fatalf("terminal replay dispatched: code=%d err=%v calls=%d", code, err, len(selected))
	}
}

func TestExecutorEscalationDoesNotBypassModelAdmission(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	var baselineCalls, strongCalls int
	serve := func(responses []string, calls *int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			(*calls)++
			if len(responses) == 0 {
				http.Error(w, "unexpected request", http.StatusBadRequest)
				return
			}
			content := responses[0]
			responses = responses[1:]
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		}))
	}
	contextCalls := 0
	contextServer := serve([]string{`{"javascriptCode":"final('Plan',{});"}`}, &contextCalls)
	defer contextServer.Close()
	baseServer := serve([]string{`{"javascriptCode":"throw new Error('executor failed');"}`}, &baselineCalls)
	defer baseServer.Close()
	strongServer := serve([]string{`{"javascriptCode":"final('Done',{});"}`}, &strongCalls)
	defer strongServer.Close()
	t.Setenv("HARNESS_CONTEXT_KEY", "synthetic")
	t.Setenv("HARNESS_BASE_KEY", "synthetic")
	t.Setenv("HARNESS_STRONG_KEY", "synthetic")
	raw, _ := json.Marshal(routeConfig{Context: "context", Executor: "base", ExecutorEscalation: "strong", ExecutorAfterErrors: 1,
		Responder: "context", Routes: []modelRoute{
			{Key: "context", Model: "fixture", URL: contextServer.URL, APIKeyEnv: "HARNESS_CONTEXT_KEY"},
			{Key: "base", Model: "fixture", URL: baseServer.URL, APIKeyEnv: "HARNESS_BASE_KEY"},
			{Key: "strong", Model: "fixture", URL: strongServer.URL, APIKeyEnv: "HARNESS_STRONG_KEY"},
		}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Complete","max_model_calls":2}`), &out)
	if code != 1 || err != nil || contextCalls != 1 || baselineCalls != 1 || strongCalls != 0 {
		t.Fatalf("model cap bypassed: code=%d err=%v routes=%d,%d,%d events=%s", code, err, contextCalls, baselineCalls, strongCalls, out.String())
	}
	var failed, terminal int
	for _, e := range events(t, &out) {
		if e.Type == "executor.step.failed" {
			failed++
		}
		if e.Type == "run.finished" && e.Result != nil && e.Result.Code == "model_budget_exceeded" {
			terminal++
		}
	}
	if failed != 1 || terminal != 1 {
		t.Fatalf("error and budget denial were not preserved: failed=%d terminal=%d", failed, terminal)
	}
}

func TestExecutorEscalationPolicyIsPinnedAndScoped(t *testing.T) {
	base := routeConfig{Context: "context", Executor: "base", Responder: "context", ExecutorEscalation: "strong", ExecutorAfterErrors: 1,
		Routes: []modelRoute{
			{Key: "context", Model: "fixture", URL: "https://context.example/v1", APIKeyEnv: "HARNESS_CONTEXT_KEY"},
			{Key: "base", Model: "fixture", URL: "https://base.example/v1", APIKeyEnv: "HARNESS_BASE_KEY"},
			{Key: "strong", Model: "fixture", URL: "https://strong.example/v1", APIKeyEnv: "HARNESS_STRONG_KEY"},
		}}
	digest := func(config routeConfig) (string, error) {
		raw, _ := json.Marshal(config)
		return RoutingDigest(string(raw))
	}
	first, err := digest(base)
	if err != nil || first == "" {
		t.Fatalf("valid policy: digest=%q err=%v", first, err)
	}
	changed := base
	changed.ExecutorAfterErrors = 2
	second, err := digest(changed)
	if err != nil || second == first {
		t.Fatalf("changed threshold was not pinned: digest=%q err=%v", second, err)
	}
	for _, config := range []routeConfig{
		func() routeConfig { c := base; c.ExecutorEscalation = "missing"; return c }(),
		func() routeConfig { c := base; c.ExecutorAfterErrors = 0; return c }(),
		func() routeConfig { c := base; c.Responder = "base"; return c }(),
	} {
		if _, err := digest(config); err == nil {
			t.Fatalf("unsafe route policy was accepted: %+v", config)
		}
	}
}

func TestTransientProviderFallbackChargesEachRouteWithoutReplayingTools(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	var requested []string
	primaryCalls := 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, "primary")
		primaryCalls++
		if primaryCalls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary outage"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `Answer: Done`}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer primary.Close()
	serve := func(alias, answer string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requested = append(requested, alias)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
		}))
	}
	secondary := serve("secondary", `{"javascriptCode":"final('Plan',{});"}`)
	defer secondary.Close()
	executor := serve("executor", `{"javascriptCode":"final('Done',{});"}`)
	defer executor.Close()
	t.Setenv("HARNESS_PRIMARY_KEY", "synthetic")
	t.Setenv("HARNESS_SECONDARY_KEY", "synthetic")
	t.Setenv("HARNESS_EXECUTOR_KEY", "synthetic")
	price := func(n int64) *agent.TokenPrice {
		return &agent.TokenPrice{InputMicrosPerMillion: n, OutputMicrosPerMillion: n}
	}
	raw, _ := json.Marshal(routeConfig{Context: "primary", Executor: "executor", Responder: "primary", PricingVersion: "test-v1",
		Fallbacks: []modelFallback{{From: "primary", To: "secondary"}}, Routes: []modelRoute{
			{Key: "primary", Model: "fixture", URL: primary.URL, APIKeyEnv: "HARNESS_PRIMARY_KEY", Price: price(1_000_000)},
			{Key: "secondary", Model: "fixture", URL: secondary.URL, APIKeyEnv: "HARNESS_SECONDARY_KEY", Price: price(2_000_000)},
			{Key: "executor", Model: "fixture", URL: executor.URL, APIKeyEnv: "HARNESS_EXECUTOR_KEY", Price: price(3_000_000)},
		}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Finish","max_cost_micros":50000}`), &out)
	if code != 0 || err != nil || strings.Join(requested, ",") != "primary,secondary,executor,primary" {
		t.Fatalf("fallback failed: code=%d err=%v routes=%v output=%s", code, err, requested, out.String())
	}
	rawEvents := out.String()
	var starts []string
	var stages []string
	var decisions int
	var terminal *agent.Result
	for _, e := range events(t, &out) {
		if e.Type == "model.request.started" {
			starts = append(starts, e.Origin)
			stages = append(stages, e.Stage)
		}
		if e.Type == "model.route.fallback" {
			decisions++
			if e.CallID != 1 || e.Name != "primary" || e.Origin != "secondary" || e.Error != "transient_provider_failure" {
				t.Fatalf("fallback receipt=%+v", e)
			}
		}
		if e.Type == "run.finished" {
			terminal = e.Result
		}
	}
	if strings.Join(starts, ",") != "primary,secondary,executor,primary" || decisions != 1 || terminal == nil ||
		terminal.Cost == nil || terminal.Cost.ChargedMicros != 4186 || terminal.Usage == nil || terminal.Usage.Coverage != "partial" {
		t.Fatalf("admission/cost receipts: starts=%v decisions=%d result=%+v", starts, decisions, terminal)
	}
	if got := strings.Join(stages, ","); got != "distiller,distiller,executor,responder" {
		t.Fatalf("Ax lifecycle did not attribute shared-route calls: %q", stages)
	}
	var replay bytes.Buffer
	code, err = run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Finish","max_cost_micros":50000}`), &replay)
	if code != 0 || err != nil || replay.String() != rawEvents || len(requested) != 4 {
		t.Fatalf("fallback replay dispatched: code=%d err=%v routes=%v", code, err, requested)
	}
}

func TestProviderDenialCannotFallback(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	primaryCalls, secondaryCalls := 0, 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"denied"}}`))
	}))
	defer primary.Close()
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondaryCalls++
		http.Error(w, "unexpected fallback", http.StatusInternalServerError)
	}))
	defer secondary.Close()
	t.Setenv("HARNESS_PRIMARY_KEY", "synthetic")
	t.Setenv("HARNESS_SECONDARY_KEY", "synthetic")
	raw, _ := json.Marshal(routeConfig{Context: "primary", Executor: "primary", Responder: "primary",
		Fallbacks: []modelFallback{{From: "primary", To: "secondary"}}, Routes: []modelRoute{
			{Key: "primary", Model: "fixture", URL: primary.URL, APIKeyEnv: "HARNESS_PRIMARY_KEY"},
			{Key: "secondary", Model: "fixture", URL: secondary.URL, APIKeyEnv: "HARNESS_SECONDARY_KEY"},
		}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(request), &out)
	if code != 1 || err != nil || primaryCalls != 1 || secondaryCalls != 0 || strings.Contains(out.String(), "model.route.fallback") {
		t.Fatalf("denial rerouted: code=%d err=%v primary=%d secondary=%d output=%s", code, err, primaryCalls, secondaryCalls, out.String())
	}
}

func TestProviderFallbackCannotBypassModelCallCeiling(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	primaryCalls, secondaryCalls := 0, 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer primary.Close()
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondaryCalls++
		http.Error(w, "unexpected fallback dispatch", http.StatusInternalServerError)
	}))
	defer secondary.Close()
	t.Setenv("HARNESS_PRIMARY_KEY", "synthetic")
	t.Setenv("HARNESS_SECONDARY_KEY", "synthetic")
	raw, _ := json.Marshal(routeConfig{Context: "primary", Executor: "primary", Responder: "primary",
		Fallbacks: []modelFallback{{From: "primary", To: "secondary"}}, Routes: []modelRoute{
			{Key: "primary", Model: "fixture", URL: primary.URL, APIKeyEnv: "HARNESS_PRIMARY_KEY"},
			{Key: "secondary", Model: "fixture", URL: secondary.URL, APIKeyEnv: "HARNESS_SECONDARY_KEY"},
		}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Answer","max_model_calls":1}`), &out)
	if code != 1 || err != nil || primaryCalls != 1 || secondaryCalls != 0 {
		t.Fatalf("model ceiling bypassed: code=%d err=%v primary=%d secondary=%d output=%s", code, err, primaryCalls, secondaryCalls, out.String())
	}
	var starts, fallback, denied int
	for _, event := range events(t, &out) {
		if event.Type == "model.request.started" {
			starts++
		}
		if event.Type == "model.route.fallback" {
			fallback++
		}
		if event.Type == "run.finished" && event.Result != nil && event.Result.Code == "model_budget_exceeded" {
			denied++
		}
	}
	if starts != 1 || fallback != 1 || denied != 1 {
		t.Fatalf("incorrect fallback admission receipts: starts=%d fallback=%d denied=%d", starts, fallback, denied)
	}
}

func TestFallbackPolicyPinsOnlyApprovedStageRoutes(t *testing.T) {
	base := routeConfig{Context: "primary", Executor: "primary", Responder: "primary",
		Fallbacks: []modelFallback{{From: "primary", To: "spare"}}, Routes: []modelRoute{
			{Key: "primary", Model: "fixture", URL: "https://primary.example/v1", APIKeyEnv: "HARNESS_PRIMARY_KEY"},
			{Key: "spare", Model: "fixture", URL: "https://spare.example/v1", APIKeyEnv: "HARNESS_SPARE_KEY"},
			{Key: "unused", Model: "fixture", URL: "https://unused.example/v1", APIKeyEnv: "HARNESS_UNUSED_KEY"},
		}}
	digest := func(config routeConfig) (string, error) {
		raw, _ := json.Marshal(config)
		return RoutingDigest(string(raw))
	}
	first, err := digest(base)
	if err != nil || first == "" {
		t.Fatalf("valid fallback: digest=%q err=%v", first, err)
	}
	changed := base
	changed.Fallbacks = []modelFallback{{From: "primary", To: "unused"}}
	second, err := digest(changed)
	if err != nil || second == first {
		t.Fatalf("changed fallback not pinned: digest=%q err=%v", second, err)
	}
	for _, pair := range []modelFallback{
		{From: "unused", To: "spare"}, {From: "primary", To: "missing"}, {From: "primary", To: "primary"},
	} {
		invalid := base
		invalid.Fallbacks = []modelFallback{pair}
		if _, err := digest(invalid); err == nil {
			t.Fatalf("unsafe fallback accepted: %+v", pair)
		}
	}
}

func TestSkillSelectionUsesApprovedCheapRoute(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	t.Setenv("HARNESS_CHEAP_KEY", "cheap-secret")
	t.Setenv("HARNESS_WORK_KEY", "work-secret")
	var requested []string
	var requests []string
	serve := func(route, key string, responses []string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				t.Errorf("wrong credential on %s", route)
			}
			body, _ := io.ReadAll(r.Body)
			requested = append(requested, route)
			requests = append(requests, string(body))
			if len(responses) == 0 {
				http.Error(w, "too many calls", 400)
				return
			}
			content := responses[0]
			responses = responses[1:]
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		}))
	}
	cheap := serve("cheap", "cheap-secret", []string{`Selected: daily-lead-union`, `{"javascriptCode":"final('Find',{})"}`})
	defer cheap.Close()
	work := serve("work", "work-secret", []string{`{"javascriptCode":"final('Answer',{})"}`, `Answer: Done`})
	defer work.Close()
	raw, _ := json.Marshal(routeConfig{Context: "cheap", Executor: "work", Responder: "work", Skill: "cheap", Routes: []modelRoute{
		{Key: "cheap", Model: "fixture", URL: cheap.URL, APIKeyEnv: "HARNESS_CHEAP_KEY"},
		{Key: "work", Model: "fixture", URL: work.URL, APIKeyEnv: "HARNESS_WORK_KEY"},
	}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	input := `{"version":1,"run_id":"skill-route","input_id":"input","skill_query":"Build the daily lead artifact","prompt":"Build the daily lead artifact"}`
	tools := agent.Tools{SkillCatalog: []agent.SkillMetadata{{Name: "daily-lead-union", Description: "Create the daily lead report"}, {Name: "records", Description: "Manage records"}},
		Capabilities: []agent.Capability{{Name: "skill_read", Version: "1", Origin: "skills", Effect: "read", Description: "Read staged skill instructions.",
			InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}},
	}
	var output bytes.Buffer
	code, err := executeWithTools(context.Background(), strings.NewReader(input), &output, tools)
	if code != 0 || err != nil {
		t.Fatalf("run code=%d err=%v output=%s", code, err, output.String())
	}
	if got := strings.Join(requested, ","); got != "cheap,cheap,work,work" {
		t.Fatalf("routes=%s", got)
	}
	if !strings.Contains(strings.Join(requests[2:], "\n"), "Candidate staged skill: daily-lead-union") {
		t.Fatal("executor and responder lost selected skill hint")
	}
	var selected, skillCall bool
	decoder := json.NewDecoder(&output)
	for {
		var event agent.Event
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if event.Type == "skill.selected" && event.Name == "daily-lead-union" && event.Origin == "semantic" {
			selected = true
		}
		if event.Type == "model.request.started" && event.Stage == "skill_selection" && event.Origin == "cheap" {
			skillCall = true
		}
	}
	if !selected || !skillCall {
		t.Fatalf("missing skill route telemetry: selected=%t call=%t", selected, skillCall)
	}
	var replay bytes.Buffer
	code, err = executeWithTools(context.Background(), strings.NewReader(input), &replay, tools)
	if code != 0 || err != nil || len(requested) != 4 {
		t.Fatalf("skill route replay dispatched again: code=%d err=%v calls=%d replay=%s", code, err, len(requested), replay.String())
	}
	var changed bytes.Buffer
	code, err = executeWithTools(context.Background(), strings.NewReader(strings.Replace(input, "daily lead artifact", "different artifact", 1)), &changed, tools)
	if code != 1 || err == nil || len(requested) != 4 {
		t.Fatalf("changed skill query accepted: code=%d err=%v calls=%d", code, err, len(requested))
	}
}

func TestRejectCallerRoutingAndUnapprovedStage(t *testing.T) {
	t.Setenv("HARNESS_MODEL_ROUTES", `{"context":"small","executor":"work","responder":"work","routes":[{"key":"small","model":"fixture","url":"http://127.0.0.1:1","api_key_env":"HARNESS_CHEAP_KEY"}]}`)
	t.Setenv("HARNESS_CHEAP_KEY", "synthetic")
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(request), &out)
	if code != 2 || err == nil || out.Len() != 0 {
		t.Fatalf("unapproved route admitted: code=%d err=%v", code, err)
	}
	code, err = run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Hi","host_routing_digest":"other"}`), &out)
	if code != 2 || err == nil || out.Len() != 0 {
		t.Fatalf("caller route admitted: code=%d err=%v", code, err)
	}
}

func TestSkillCatalogLoadingFollowsTrustedRoute(t *testing.T) {
	if enabled, err := RouteHasSkill(""); err != nil || enabled {
		t.Fatalf("legacy route enabled skill selection: %t %v", enabled, err)
	}
	without := `{"context":"cheap","executor":"cheap","responder":"cheap","routes":[{"key":"cheap","model":"fixture","url":"https://example.invalid/v1","api_key_env":"HARNESS_CHEAP_KEY"}]}`
	if enabled, err := RouteHasSkill(without); err != nil || enabled {
		t.Fatalf("stage-only profile enabled skill selection: %t %v", enabled, err)
	}
	with := strings.Replace(without, `"responder":"cheap",`, `"responder":"cheap","skill":"cheap",`, 1)
	if enabled, err := RouteHasSkill(with); err != nil || !enabled {
		t.Fatalf("skill route unavailable: %t %v", enabled, err)
	}
}

func TestPricingProfileIsCompleteAndPinned(t *testing.T) {
	base := routeConfig{Context: "cheap", Executor: "work", Responder: "work", Routes: []modelRoute{
		{Key: "cheap", Model: "fixture", URL: "https://example.invalid/v1", APIKeyEnv: "HARNESS_CHEAP_KEY"},
		{Key: "work", Model: "fixture", URL: "https://example.invalid/v1", APIKeyEnv: "HARNESS_WORK_KEY"},
	}}
	encode := func(c routeConfig) string { b, _ := json.Marshal(c); return string(b) }
	first, err := RoutingDigest(encode(base))
	if err != nil {
		t.Fatal(err)
	}
	base.PricingVersion = "2026-09"
	if _, err := RoutingDigest(encode(base)); err == nil {
		t.Fatal("unpriced routes admitted with version")
	}
	base.Routes[0].Price = &agent.TokenPrice{InputMicrosPerMillion: 100, OutputMicrosPerMillion: 200}
	if _, err := RoutingDigest(encode(base)); err == nil {
		t.Fatal("partially priced routes admitted")
	}
	base.Routes[1].Price = &agent.TokenPrice{InputMicrosPerMillion: 300, OutputMicrosPerMillion: 400}
	base.GraphJinPrice = &agent.TokenPrice{InputMicrosPerMillion: 500, OutputMicrosPerMillion: 600}
	second, err := RoutingDigest(encode(base))
	if err != nil || first == second {
		t.Fatalf("price profile digest not pinned: %v", err)
	}
	base.Routes[1].Price.OutputMicrosPerMillion++
	third, err := RoutingDigest(encode(base))
	if err != nil || second == third {
		t.Fatalf("rate change did not change digest: %v", err)
	}
	base.PricingVersion = ""
	if _, err := RoutingDigest(encode(base)); err == nil {
		t.Fatal("unversioned prices admitted")
	}
}

func TestCostAdmissionStopsBeforeNextModelDispatch(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	t.Setenv("HARNESS_TEST_KEY", "synthetic")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `{"javascriptCode":"final('Answer',{})"}`}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer server.Close()
	price := &agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	raw, _ := json.Marshal(routeConfig{Context: "only", Executor: "only", Responder: "only", PricingVersion: "test-v1", Routes: []modelRoute{
		{Key: "only", Model: "fixture", URL: server.URL, APIKeyEnv: "HARNESS_TEST_KEY", Price: price},
	}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	input := `{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Answer","max_cost_micros":4100}`
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(input), &out)
	if code != 1 || err != nil || requests != 1 {
		t.Fatalf("cost gate: code=%d err=%v requests=%d output=%s", code, err, requests, out.String())
	}
	rawEvents := out.String()
	var started, finished int
	for _, event := range events(t, &out) {
		if event.Type == "model.request.started" {
			started++
			if event.CostMicros == nil || *event.CostMicros != 4096 {
				t.Fatalf("reservation: %+v", event)
			}
		}
		if event.Type == "model.request.finished" {
			finished++
			if event.CostMicros == nil || *event.CostMicros != 15 {
				t.Fatalf("observed charge: %+v", event)
			}
		}
		if event.Type == "run.finished" && (event.Result == nil || event.Result.Code != "cost_budget_exceeded" ||
			event.Result.Cost == nil || event.Result.Cost.ChargedMicros != 15 || event.Result.Cost.PricingVersion != "test-v1") {
			t.Fatalf("terminal cost: %+v", event.Result)
		}
	}
	if started != 1 || finished != 1 {
		t.Fatalf("model starts=%d finishes=%d", started, finished)
	}
	var replay bytes.Buffer
	code, err = run(context.Background(), strings.NewReader(input), &replay)
	if code != 1 || err != nil || replay.String() != rawEvents || requests != 1 {
		t.Fatalf("terminal replay changed: code=%d err=%v requests=%d", code, err, requests)
	}
}

func TestMissingUsageRetainsCostReservation(t *testing.T) {
	t.Setenv("HARNESS_TEST_KEY", "synthetic")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `{"javascriptCode":"final('Answer',{})"}`}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	price := &agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	raw, _ := json.Marshal(routeConfig{Context: "only", Executor: "only", Responder: "only", PricingVersion: "test-v1", Routes: []modelRoute{
		{Key: "only", Model: "fixture", URL: server.URL, APIKeyEnv: "HARNESS_TEST_KEY", Price: price},
	}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	var out bytes.Buffer
	code, err := run(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Answer","max_cost_micros":4100}`), &out)
	if code != 1 || err != nil || requests != 1 {
		t.Fatalf("missing usage: code=%d err=%v requests=%d output=%s", code, err, requests, out.String())
	}
	var terminal *agent.Result
	for _, event := range events(t, &out) {
		if event.Type == "run.finished" {
			terminal = event.Result
		}
	}
	if terminal == nil || terminal.Code != "cost_budget_exceeded" || terminal.Cost == nil || terminal.Cost.ChargedMicros != 4096 ||
		terminal.Usage == nil || terminal.Usage.Coverage != "unavailable" {
		t.Fatalf("reservation or coverage lost: %+v", terminal)
	}
}

func TestGraphJinCostReservationBlocksBrokerDispatch(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	t.Setenv("HARNESS_TEST_KEY", "synthetic")
	responses := []string{`{"javascriptCode":"final('Find the row',{})"}`, `{"javascriptCode":"const row=lookup('find row'); final('Done',{row});"}`, `Answer: Unable to finish`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(responses) == 0 {
			http.Error(w, "unexpected model call", 400)
			return
		}
		content := responses[0]
		responses = responses[1:]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer server.Close()
	price := &agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	raw, _ := json.Marshal(routeConfig{Context: "only", Executor: "only", Responder: "only", PricingVersion: "test-v1", GraphJinPrice: price,
		Routes: []modelRoute{{Key: "only", Model: "fixture", URL: server.URL, APIKeyEnv: "HARNESS_TEST_KEY", Price: price}}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	lookups := 0
	var out bytes.Buffer
	code, err := execute(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Find row","max_cost_micros":49000}`), &out,
		func(context.Context, string) (json.RawMessage, error) {
			lookups++
			return json.RawMessage(`{"response":{"answer":"row"}}`), nil
		})
	if code != 1 || err != nil || lookups != 0 {
		t.Fatalf("broker dispatch admitted: code=%d err=%v lookups=%d output=%s", code, err, lookups, out.String())
	}
	var terminal *agent.Result
	for _, event := range events(t, &out) {
		if event.Type == "run.finished" {
			terminal = event.Result
		}
	}
	if terminal == nil || terminal.Code != "cost_budget_exceeded" || terminal.Cost == nil || terminal.Cost.ChargedMicros >= 49_000 {
		t.Fatalf("cost gate result=%+v", terminal)
	}
}

func TestGraphJinReportedCostReplacesReservation(t *testing.T) {
	t.Setenv("HARNESS_STATE_DIR", t.TempDir())
	t.Setenv("HARNESS_TEST_KEY", "synthetic")
	responses := []string{`{"javascriptCode":"final('Find the row',{})"}`, `{"javascriptCode":"const row=lookup('find row'); final('Done',{row});"}`, `Answer: row`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(responses) == 0 {
			http.Error(w, "unexpected model call", 400)
			return
		}
		content := responses[0]
		responses = responses[1:]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer server.Close()
	price := &agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	raw, _ := json.Marshal(routeConfig{Context: "only", Executor: "only", Responder: "only", PricingVersion: "test-v1", GraphJinPrice: price,
		Routes: []modelRoute{{Key: "only", Model: "fixture", URL: server.URL, APIKeyEnv: "HARNESS_TEST_KEY", Price: price}}})
	t.Setenv("HARNESS_MODEL_ROUTES", string(raw))
	lookups := 0
	var out bytes.Buffer
	code, err := execute(context.Background(), strings.NewReader(`{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Find row","max_cost_micros":100000}`), &out,
		func(context.Context, string) (json.RawMessage, error) {
			lookups++
			return json.RawMessage(`{"response":{"answer":"row","usage":{"prompt_tokens":70,"completion_tokens":30,"total_tokens":100,"llm_calls":1}}}`), nil
		})
	if code != 0 || err != nil || lookups != 1 {
		t.Fatalf("lookup result: code=%d err=%v lookups=%d output=%s", code, err, lookups, out.String())
	}
	var terminal *agent.Result
	var lookupCharge int64
	for _, event := range events(t, &out) {
		if event.Type == "tool.finished" && event.Name == "lookup" && event.CostMicros != nil {
			lookupCharge = *event.CostMicros
		}
		if event.Type == "run.finished" {
			terminal = event.Result
		}
	}
	if lookupCharge != 100 || terminal == nil || terminal.Cost == nil || terminal.Cost.ChargedMicros != 145 {
		t.Fatalf("cost receipt=%d result=%+v", lookupCharge, terminal)
	}
}
