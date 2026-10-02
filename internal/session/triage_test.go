package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestCanaryBudgetBlocksUnjustifiedModelCall(t *testing.T) {
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t)
	defer closeServers()
	spec.HostBudgetMode = "canary"
	tools.Triage.Policy.Short.MaxModelCalls = 2
	tools.Triage.Policy.MultiStep.MaxModelCalls = 2
	root := t.TempDir()
	result, err := RunWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Code != "dynamic_budget_exceeded" || *triageCalls != 1 || *chatCalls != 1 {
		t.Fatalf("canary result=%+v err=%v triage=%d chat=%d", result, err, *triageCalls, *chatCalls)
	}
	if report, err := Inspect(root, spec); err != nil || report.Outcome != "terminal" {
		t.Fatalf("canary checkpoint=%+v err=%v", report, err)
	}
	if trace, err := ReadBudgetTrace(root, spec.RunID); err != nil || trace.Mode != "canary" {
		t.Fatalf("canary trace mode=%q err=%v", trace.Mode, err)
	}
	shadow := spec
	shadow.HostBudgetMode = ""
	if _, err := Inspect(root, shadow); err == nil {
		t.Fatal("canary checkpoint replayed as shadow mode")
	}
}

func TestCanaryBudgetExtendsAfterDurableRead(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Read the fixture',{})"}`,
		`{"javascriptCode":"const value=native_read({}); final('Done',{value});"}`,
		`{"answer":"Verified answer."}`}
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t, answers...)
	defer closeServers()
	spec.HostBudgetMode = "canary"
	tools.Triage.Policy.Short.MaxModelCalls = 3
	tools.Triage.Policy.MultiStep.MaxModelCalls = 3
	reads := 0
	tools.Capabilities = []agent.Capability{{Name: "native_read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a fixture.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads++
			return json.RawMessage(`{"value":"ok"}`), nil
		}}}
	root := t.TempDir()
	var events []agent.Event
	result, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil || result.Status != "completed" || reads != 1 || *triageCalls != 1 || *chatCalls != 3 {
		t.Fatalf("extended canary result=%+v err=%v reads=%d triage=%d chat=%d", result, err, reads, *triageCalls, *chatCalls)
	}
	extended, responder := -1, -1
	for i, e := range events {
		if e.Type == "budget.profile.extended" && e.OperationID == 1 {
			extended = i
		}
		if e.Type == "model.request.started" && e.CallID == 4 {
			responder = i
		}
	}
	if extended < 0 || responder <= extended {
		t.Fatalf("extension was not durable before responder: extension=%d responder=%d", extended, responder)
	}
	if report, err := Inspect(root, spec); err != nil || report.Outcome != "terminal" {
		t.Fatalf("extended canary checkpoint=%+v err=%v", report, err)
	}
}

func TestCanaryFallsBackToHardBudgetWhenTriageIsSkipped(t *testing.T) {
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t)
	defer closeServers()
	spec.HostBudgetMode = "canary"
	spec.MaxModelCalls = 3 // Triage refuses to consume room needed for the ordinary turn.
	root := t.TempDir()
	var skipped bool
	result, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "budget.triage.skipped" && e.Name == "call_budget" {
			skipped = true
		}
		return nil
	})
	if err != nil || result.Status != "completed" || !skipped || *triageCalls != 0 || *chatCalls != 3 {
		t.Fatalf("fallback result=%+v err=%v skipped=%v triage=%d chat=%d", result, err, skipped, *triageCalls, *chatCalls)
	}
	if report, err := Inspect(root, spec); err != nil || report.Outcome != "terminal" {
		t.Fatalf("fallback checkpoint=%+v err=%v", report, err)
	}
}

func TestShadowBudgetExtensionSurvivesInterruptedToolReceipt(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Read the fixture',{})"}`,
		`{"javascriptCode":"const value=native_read({}); final('Done',{value});"}`,
		`{"javascriptCode":"final('Read the fixture',{})"}`,
		`{"javascriptCode":"const value=native_read({}); final('Done',{value});"}`,
		`{"answer":"Verified answer."}`}
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t, answers...)
	defer closeServers()
	spec.HostRoutingDigest = strings.Repeat("a", 64)
	spec.MaxOperations = 5
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
	trace, err := ReadBudgetTrace(root, spec.RunID)
	if err != nil || trace.Status != "completed" || len(trace.Events) == 0 ||
		trace.RoutingDigest != spec.HostRoutingDigest || trace.MaxOperations != spec.OperationLimit() {
		t.Fatalf("validated budget trace=%+v err=%v", trace, err)
	}
	traceJSON, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(traceJSON, []byte("Verified answer")) || bytes.Contains(traceJSON, []byte(`"value":"ok"`)) ||
		bytes.Contains(traceJSON, []byte(spec.Prompt)) {
		t.Fatal("budget trace exported an answer, tool result, or accepted prompt")
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
	if _, err := ReadBudgetTrace(root, spec.RunID); err == nil {
		t.Fatal("tampered budget checkpoint was exported")
	}
}

func TestGraphJinShadowExtensionPrecedesRemoteAdmission(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Look up the reference',{})"}`,
		`{"javascriptCode":"const value=lookup('reference'); final('Done',{value});"}`,
		`{"answer":"Verified answer."}`}
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t, answers...)
	defer closeServers()
	lookups := 0
	tools.Lookup = func(context.Context, string) (json.RawMessage, error) {
		lookups++
		return json.RawMessage(`{"response":{"answer":"REF-42"}}`), nil
	}
	remotePrice := agent.TokenPrice{InputMicrosPerMillion: 1000, OutputMicrosPerMillion: 1000}
	client.GraphJinPrice = &remotePrice
	var events []agent.Event
	result, err := RunWithTools(context.Background(), t.TempDir(), spec, client, tools, func(e agent.Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil || result.Status != "completed" || lookups != 1 || *triageCalls != 1 || *chatCalls != 3 {
		t.Fatalf("result=%+v err=%v lookups=%d triage=%d chat=%d", result, err, lookups, *triageCalls, *chatCalls)
	}
	proposalAt, extensionAt, lookupAt := -1, -1, -1
	for i, e := range events {
		if e.Type == "tool.proposed" && e.Name == "lookup" {
			proposalAt = i
		}
		if e.Type == "budget.profile.extended" {
			extensionAt = i
			var extension budgettriage.Extension
			if json.Unmarshal(e.Data, &extension) != nil || extension.Reason != "remote_lookup_preflight" ||
				extension.CallID != 3 || extension.OperationID != 0 || extension.From != "multi_step" || extension.To != "artifact" {
				t.Fatalf("invalid remote extension: %+v %+v", e, extension)
			}
		}
		if e.Type == "tool.started" && e.Name == "lookup" {
			lookupAt = i
		}
	}
	if proposalAt < 0 || extensionAt < 0 || lookupAt < 0 || proposalAt >= extensionAt || extensionAt >= lookupAt {
		t.Fatalf("remote intent and extension were not journaled before lookup: proposal=%d extension=%d lookup=%d", proposalAt, extensionAt, lookupAt)
	}
}

func TestRemotePreflightJournalFailureStopsLookupAndResumes(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Look up the reference',{})"}`,
		`{"javascriptCode":"const value=lookup('reference'); final('Done',{value});"}`,
		`{"javascriptCode":"final('Look up the reference',{})"}`,
		`{"javascriptCode":"const value=lookup('reference'); final('Done',{value});"}`,
		`{"answer":"Verified answer."}`}
	spec, client, tools, triageCalls, chatCalls, closeServers := triageRunFixture(t, answers...)
	defer closeServers()
	lookups := 0
	tools.Lookup = func(context.Context, string) (json.RawMessage, error) {
		lookups++
		return json.RawMessage(`{"response":{"answer":"REF-42"}}`), nil
	}
	remotePrice := agent.TokenPrice{InputMicrosPerMillion: 1000, OutputMicrosPerMillion: 1000}
	client.GraphJinPrice = &remotePrice
	root := t.TempDir()
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "budget.profile.extended" && e.CallID > 0 {
			return errors.New("delivery interrupted after extension checkpoint")
		}
		return nil
	})
	if err == nil || lookups != 0 || *triageCalls != 1 || *chatCalls != 2 {
		t.Fatalf("interrupted err=%v lookups=%d triage=%d chat=%d", err, lookups, *triageCalls, *chatCalls)
	}
	if report, inspectErr := Inspect(root, spec); inspectErr != nil || !report.CanResume || len(report.Operations) != 0 {
		t.Fatalf("checkpoint report=%+v err=%v", report, inspectErr)
	}
	var replay []agent.Event
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		replay = append(replay, e)
		return nil
	})
	if err != nil || result.Status != "completed" || lookups != 1 || *triageCalls != 1 || *chatCalls != 5 {
		t.Fatalf("resume result=%+v err=%v lookups=%d triage=%d chat=%d", result, err, lookups, *triageCalls, *chatCalls)
	}
	extensions := 0
	for _, e := range replay {
		if e.Type == "budget.profile.extended" {
			extensions++
		}
	}
	if extensions != 1 {
		t.Fatalf("remote extension repeated %d times", extensions)
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
		if saved.Events[i].Type == "tool.proposed" {
			saved.Events[i].CallID = 2 // A different completed model call cannot justify call 3's extension.
			break
		}
	}
	if err := saveCheckpoint(root, path, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, spec); err == nil {
		t.Fatal("remote extension without matching durable lookup intent passed inspection")
	}
}

func TestCanaryRemotePreflightCanJournalTwoOrderedTiersAtOneModelReceipt(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Look up the reference',{})"}`,
		`{"javascriptCode":"const value=lookup('reference'); final('Done',{value});"}`,
		`{"answer":"Verified answer."}`}
	spec, client, tools, _, _, closeServers := triageRunFixture(t, answers...)
	defer closeServers()
	spec.HostBudgetMode = "canary"
	tools.Triage.Policy.Artifact.MaxModelTokens = 45_000 // Still below GraphJin's 49,152-token reservation.
	lookups := 0
	tools.Lookup = func(context.Context, string) (json.RawMessage, error) {
		lookups++
		return json.RawMessage(`{"response":{"answer":"REF-42"}}`), nil
	}
	remotePrice := agent.TokenPrice{InputMicrosPerMillion: 1000, OutputMicrosPerMillion: 1000}
	client.GraphJinPrice = &remotePrice
	root := t.TempDir()
	var events []agent.Event
	result, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil || result.Status != "completed" || lookups != 1 {
		t.Fatalf("result=%+v err=%v lookups=%d", result, err, lookups)
	}
	var tiers []string
	lookupAt := -1
	for i, e := range events {
		if e.Type == "budget.profile.extended" {
			var extension budgettriage.Extension
			if json.Unmarshal(e.Data, &extension) != nil || e.CallID != 3 || extension.CallID != 3 || e.OperationID != 0 {
				t.Fatalf("invalid ordered extension: %+v %+v", e, extension)
			}
			tiers = append(tiers, extension.To)
		}
		if e.Type == "tool.started" && e.Name == "lookup" {
			lookupAt = i
			if len(tiers) != 2 {
				t.Fatalf("lookup started after %d extensions", len(tiers))
			}
		}
	}
	if lookupAt < 0 || len(tiers) != 2 || tiers[0] != "artifact" || tiers[1] != "fixed" {
		t.Fatalf("ordered tiers=%v lookupAt=%d", tiers, lookupAt)
	}
	if _, err := Inspect(root, spec); err != nil {
		t.Fatalf("ordered extension checkpoint rejected: %v", err)
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
