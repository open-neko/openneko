package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/budgettriage"
)

func triageRunFixture(t *testing.T, customAnswers ...string) (agent.Spec, *agent.RoutedClient, agent.Tools, *int, *int, func()) {
	t.Helper()
	triageCalls, chatCalls := 0, 0
	triageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		triageCalls++
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer triage-secret" {
			t.Errorf("wrong Typesafe route: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-fixture","answers":{"workload":{"type":"choice","choice":"multi_step","confidence":0.8,"probabilities":{"short_answer":0.05,"multi_step":0.8,"artifact_pipeline":0.1,"uncertain":0.05}}},"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls++
		if r.Header.Get("Authorization") != "Bearer work-secret" {
			t.Errorf("wrong work-route credential: %s", r.Header.Get("Authorization"))
		}
		answers := []string{`{"javascriptCode":"final('Answer the question',{})"}`,
			`{"javascriptCode":"final('Report the result',{})"}`, `{"answer":"Verified answer."}`}
		if len(customAnswers) > 0 {
			answers = customAnswers
		}
		if chatCalls > len(answers) {
			http.Error(w, "too many model calls", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message",
			ax.Object("role", "assistant", "content", answers[chatCalls-1]), "finish_reason", "stop")),
			"usage", ax.Object("prompt_tokens", 10, "completion_tokens", 5, "total_tokens", 15)))
	}))
	service := ax.NewOpenAICompatibleClient(ax.Object("base_url", chatServer.URL, "api_key", "work-secret", "model", "work-fixture"))
	service.Name = "work"
	router, err := ax.NewMultiServiceRouter([]ax.Value{ax.RouterServiceEntry{Key: "work", Service: service}})
	if err != nil {
		t.Fatal(err)
	}
	price := agent.TokenPrice{InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}
	client := &agent.RoutedClient{AIClient: router,
		Stages:         agent.StageModels{Context: "work", Executor: "work", Responder: "work"},
		PricingVersion: "triage-test-v1", Prices: map[string]agent.TokenPrice{"work": price, "triage": price}}
	tools := agent.Tools{Scope: "org:triage-fixture", Triage: &agent.BudgetTriage{
		Client: ax.Typesafe(ax.Object("api_key", "triage-secret", "base_url", triageServer.URL,
			"model", "jev-fixture", "retry", ax.Object("maxRetries", 0))),
		Route: "triage", Model: "jev-fixture", Policy: budgettriage.Policy{Version: "triage-test-v1",
			Short:     budgettriage.Limits{MaxModelCalls: 4, MaxModelTokens: 8_000, MaxCostMicros: 2_000},
			MultiStep: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 40_000, MaxCostMicros: 5_000},
			Artifact:  budgettriage.Limits{MaxModelCalls: 16, MaxModelTokens: 100_000, MaxCostMicros: 10_000}}}}
	spec := agent.Spec{Version: 1, RunID: "triage-run", InputID: "input", Prompt: "Investigate and answer",
		MaxModelCalls: 8, MaxModelTokens: 100_000, MaxCostMicros: 10_000,
		TriageSummary: "Investigate a reference and answer", TriageToolFamilies: "graphjin",
		TriageInputBytes: 200}
	return spec, client, tools, &triageCalls, &chatCalls, func() { triageServer.Close(); chatServer.Close() }
}

func TestShadowBudgetExtensionSurvivesInterruptedToolReceipt(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Read the fixture',{})"}`,
		`{"javascriptCode":"const value=native_read({}); final('Done',{value});"}`,
		`{"javascriptCode":"final('Read the fixture',{})"}`,
		`{"javascriptCode":"const value=native_read({}); final('Done',{value});"}`,
		`{"answer":"Verified answer."}`}
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t, answers...)
	defer closeServers()
	tools.Triage.Policy.Short.MaxModelCalls = 1
	tools.Triage.Policy.MultiStep.MaxModelCalls = 2
	reads := 0
	tools.Capabilities = []agent.Capability{{Name: "native_read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a fixture.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads++
			return json.RawMessage(`{"value":"ok"}`), nil
		}}}
	root := t.TempDir()
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "tool.finished" {
			return errors.New("delivery interrupted after durable tool receipt")
		}
		return nil
	})
	if err == nil || reads != 1 || *triageCalls != 1 || *chatCalls != 2 {
		t.Fatalf("first attempt err=%v reads=%d triage=%d chat=%d", err, reads, *triageCalls, *chatCalls)
	}
	if report, inspectErr := Inspect(root, spec); inspectErr != nil || !report.CanResume {
		t.Fatalf("checkpoint report=%+v err=%v", report, inspectErr)
	}
	var events []agent.Event
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil || result.Status != "completed" || reads != 1 || *triageCalls != 1 || *chatCalls != 5 {
		t.Fatalf("resume result=%+v err=%v reads=%d triage=%d chat=%d", result, err, reads, *triageCalls, *chatCalls)
	}
	extensions := 0
	for _, e := range events {
		if e.Type != "budget.profile.extended" {
			continue
		}
		extensions++
		var extension budgettriage.Extension
		if e.OperationID != 1 || json.Unmarshal(e.Data, &extension) != nil || extension.From != "multi_step" || extension.To != "artifact" {
			t.Fatalf("invalid resumed extension: %+v %+v", e, extension)
		}
	}
	if extensions != 1 {
		t.Fatalf("expected one resumed extension, got %d", extensions)
	}
	if report, inspectErr := Inspect(root, spec); inspectErr != nil || report.Outcome != "terminal" {
		t.Fatalf("terminal report=%+v err=%v", report, inspectErr)
	}
	var replay []agent.Event
	if _, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		replay = append(replay, e)
		return nil
	}); err != nil || reads != 1 || *triageCalls != 1 || *chatCalls != 5 {
		t.Fatalf("terminal replay err=%v reads=%d triage=%d chat=%d", err, reads, *triageCalls, *chatCalls)
	}
	replayedExtensions := 0
	for _, e := range replay {
		if e.Type == "budget.profile.extended" {
			replayedExtensions++
		}
	}
	if replayedExtensions != 1 {
		t.Fatalf("terminal replay contained %d extensions", replayedExtensions)
	}
	sum := sha256.Sum256([]byte(spec.RunID))
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var saved checkpoint
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	for i := range saved.Events {
		if saved.Events[i].Type == "budget.profile.extended" {
			saved.Events[i].OperationID++
			break
		}
	}
	if err := saveCheckpoint(root, path, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, spec); err == nil {
		t.Fatal("extension bound to a different operation passed checkpoint inspection")
	}
}

func TestTriageCallIsChargedAndReplayedFromCheckpoint(t *testing.T) {
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t)
	defer closeServers()
	root := t.TempDir()
	var events []agent.Event
	result, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil || result.Status != "completed" || *triageCalls != 1 || *chatCalls != 3 ||
		result.Cost == nil || result.Cost.ChargedMicros != 557 || result.Usage == nil ||
		result.Usage.Requests != 4 || result.Usage.Reported != 4 {
		t.Fatalf("result=%+v err=%v triage=%d chat=%d", result, err, *triageCalls, *chatCalls)
	}
	if len(events) < 3 || events[0].Type != "run.started" || events[1].Type != "model.request.started" ||
		events[1].Stage != "budget_triage" || events[1].CostMicros == nil || *events[1].CostMicros != 512 ||
		events[2].Type != "model.request.finished" || events[2].Stage != "budget_triage" {
		t.Fatalf("triage did not lead the durable event stream: %+v", events[:3])
	}
	var proposed budgettriage.Proposal
	if events[3].Type != "budget.profile.proposed" || json.Unmarshal(events[3].Data, &proposed) != nil ||
		proposed.Version != "triage-test-v1" || proposed.Profile != "multi_step" ||
		proposed.Limits.MaxModelCalls != 8 || proposed.Limits.MaxCostMicros != 5_000 {
		t.Fatalf("invalid pinned shadow proposal: %+v %+v", events[3], proposed)
	}
	var replay []agent.Event
	result, err = RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		replay = append(replay, e)
		return nil
	})
	if err != nil || result.Status != "completed" || *triageCalls != 1 || *chatCalls != 3 || len(replay) != len(events) {
		t.Fatalf("replay result=%+v err=%v triage=%d chat=%d", result, err, *triageCalls, *chatCalls)
	}
	if _, err := Inspect(root, spec); err != nil {
		t.Fatalf("triage checkpoint rejected: %v", err)
	}
	changed := spec
	changed.TriageSummary = "A different approved summary"
	if _, err := Inspect(root, changed); err == nil {
		t.Fatal("a changed approved triage summary replayed")
	}
	sum := sha256.Sum256([]byte(spec.RunID))
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var saved checkpoint
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Events[3].Data = json.RawMessage(`{"version":"triage-test-v1","profile":"multi_step","limits":{"max_model_calls":9,"max_model_tokens":40000,"max_cost_micros":10001}}`)
	if err := saveCheckpoint(root, path, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, spec); err == nil {
		t.Fatal("shadow proposal above the hard cost cap passed checkpoint inspection")
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Events[2].CostMicros = nil
	if err := saveCheckpoint(root, path, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, spec); err == nil {
		t.Fatal("classifier settlement without a cost passed checkpoint inspection")
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Events[2].Data = json.RawMessage(`{"version":"budget-triage-v1","requested_model":"jev-fixture","suggested_profile":"short","reason":"classified","coverage":"complete","charged_micros":512,"latency_ms":1,"input_tokens":10,"output_tokens":5,"choice":"short_answer","probabilities":{"short_answer":0.2,"multi_step":0.8,"artifact_pipeline":0,"uncertain":0}}`)
	if err := saveCheckpoint(root, path, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, spec); err == nil {
		t.Fatal("forged classifier distribution passed checkpoint inspection")
	}
}

func TestInterruptedTriageReservationIsNotRedispatched(t *testing.T) {
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t)
	defer closeServers()
	root := t.TempDir()
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "model.request.started" && e.Stage == "budget_triage" {
			return errors.New("delivery interrupted after checkpoint")
		}
		return nil
	})
	if err == nil || *triageCalls != 0 || *chatCalls != 0 {
		t.Fatalf("interruption err=%v triage=%d chat=%d", err, *triageCalls, *chatCalls)
	}
	if report, inspectErr := Inspect(root, spec); inspectErr != nil || !report.CanResume {
		t.Fatalf("checkpoint report=%+v err=%v", report, inspectErr)
	}
	changedTools := tools
	changedTriage := *tools.Triage
	changedTriage.Policy.Version = "changed-policy-v1"
	changedTools.Triage = &changedTriage
	if _, err := ResumeWithTools(context.Background(), root, spec, client, changedTools, func(agent.Event) error { return nil }); err == nil {
		t.Fatal("changed budget policy resumed an interrupted run")
	}
	var replay []agent.Event
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		replay = append(replay, e)
		return nil
	})
	if err != nil || result.Status != "completed" || *triageCalls != 0 || *chatCalls != 3 ||
		result.Cost == nil || result.Cost.ChargedMicros != 557 || result.Usage == nil ||
		result.Usage.Requests != 4 || result.Usage.Reported != 3 || result.Usage.Coverage != "partial" {
		t.Fatalf("resume result=%+v err=%v triage=%d chat=%d", result, err, *triageCalls, *chatCalls)
	}
	seenSkip := false
	for _, e := range replay {
		seenSkip = seenSkip || e.Type == "budget.triage.skipped" && e.Name == "interrupted"
	}
	if !seenSkip {
		t.Fatal("interrupted classifier was not durably skipped")
	}
}

func TestSettledTriageWithoutProposalResumesWithoutRedispatch(t *testing.T) {
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t)
	defer closeServers()
	root := t.TempDir()
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "model.request.finished" && e.Stage == "budget_triage" {
			return errors.New("delivery interrupted after classifier settlement")
		}
		return nil
	})
	if err == nil || *triageCalls != 1 || *chatCalls != 0 {
		t.Fatalf("interruption err=%v triage=%d chat=%d", err, *triageCalls, *chatCalls)
	}
	if report, inspectErr := Inspect(root, spec); inspectErr != nil || !report.CanResume {
		t.Fatalf("checkpoint report=%+v err=%v", report, inspectErr)
	}
	var replay []agent.Event
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		replay = append(replay, e)
		return nil
	})
	if err != nil || result.Status != "completed" || *triageCalls != 1 || *chatCalls != 3 {
		t.Fatalf("resume result=%+v err=%v triage=%d chat=%d", result, err, *triageCalls, *chatCalls)
	}
	proposals := 0
	for _, e := range replay {
		if e.Type == "budget.profile.proposed" {
			proposals++
		}
	}
	if proposals != 1 {
		t.Fatalf("expected one resumed proposal, got %d", proposals)
	}
}
