package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

type cancelThirdModelClient struct {
	ax.AIClient
	calls atomic.Int32
}

func (c *cancelThirdModelClient) GetFeatures(model string) map[string]ax.Value {
	if provider, ok := c.AIClient.(interface {
		GetFeatures(string) map[string]ax.Value
	}); ok {
		return provider.GetFeatures(model)
	}
	return nil
}

func (c *cancelThirdModelClient) Chat(ctx context.Context, request, options map[string]ax.Value) (ax.Value, error) {
	if c.calls.Add(1) == 3 {
		return nil, context.Canceled // Provider stream lost while the run context remains live.
	}
	return c.AIClient.Chat(ctx, request, options)
}

func TestCancelledModelAfterDurableLookupResumesWithoutRedispatch(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "cancelled-model", InputID: "accepted", Prompt: "Find REF-42"}
	var modelCalls, lookups atomic.Int32
	answers := []string{
		`{"javascriptCode":"final('Find the reference',{});"}`,
		`{"javascriptCode":"const row=lookup('read'); final('Report the reference',{row});"}`,
		`{"javascriptCode":"final('Use the saved reference',{});"}`,
		`{"javascriptCode":"const row=harnessSavedOperation(1); final('Report the reference',{row});"}`,
		`{"answer":"REF-42"}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(modelCalls.Add(1)) - 1
		if index >= len(answers) {
			t.Error("unexpected model call")
			http.Error(w, "unexpected", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	base := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	lookup := func(context.Context, string) (json.RawMessage, error) {
		lookups.Add(1)
		return json.RawMessage(`{"response":{"answer":"REF-42"}}`), nil
	}
	interrupted := &cancelThirdModelClient{AIClient: base}
	first, err := Run(context.Background(), root, spec, interrupted, lookup, func(agent.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "model stream interrupted") || lookups.Load() != 1 {
		t.Fatalf("expected resumable model interruption after one lookup: result=%+v err=%v lookups=%d model=%d attempted=%d", first, err, lookups.Load(), modelCalls.Load(), interrupted.calls.Load())
	}
	report, err := Inspect(root, spec)
	if err != nil || report.Outcome != "interrupted" || !report.CanResume || report.NextAttempt != 2 || len(report.Operations) != 1 || !report.Operations[0].Finished {
		t.Fatalf("saved lookup did not admit continuation: %+v %v", report, err)
	}
	result, err := Resume(context.Background(), root, spec, base, lookup, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Answer != "REF-42" || lookups.Load() != 1 {
		t.Fatalf("resume result=%+v err=%v lookups=%d", result, err, lookups.Load())
	}
}

func resolvedPrefix(t *testing.T) (string, agent.Spec) {
	s := prefix()
	root, _ := fixture(t, s)
	if report, err := Reconcile(root, s.Spec, []Receipt{{ID: 1, Instruction: "read", Result: json.RawMessage(`{"response":{"answer":"REF-42"}}`)}}); err != nil || !report.CanResume || report.NextAttempt != 2 {
		t.Fatalf("repaired eligibility: %+v %v", report, err)
	}
	return root, s.Spec
}

func TestResumeUsesSavedEvidenceAndContinuesOperationSequence(t *testing.T) {
	root, spec := resolvedPrefix(t)
	var calls, lookups atomic.Int32
	answers := []string{`{"javascriptCode":"final('Finish the request',{})"}`, `{"javascriptCode":"const saved=lookup('read'); const fresh=lookup('new'); final('Answer',{saved,fresh});"}`, `{"answer":"REF-42 and REF-43"}`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		body, _ := io.ReadAll(r.Body)
		if n == 0 && !strings.Contains(string(body), "REF-42") {
			t.Error("recovered evidence missing from Ax input")
		}
		if n >= len(answers) {
			http.Error(w, "unexpected request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[n]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	var events []agent.Event
	result, err := Resume(context.Background(), root, spec, client, func(ctx context.Context, instruction string) (json.RawMessage, error) {
		lookups.Add(1)
		if instruction != "new" || agent.OperationID(ctx) != 2 {
			t.Errorf("repeated or reset operation: %s %d", instruction, agent.OperationID(ctx))
		}
		return json.RawMessage(`{"response":{"answer":"REF-43"}}`), nil
	}, func(e agent.Event) error { events = append(events, e); return nil })
	if err != nil || result.Status != "completed" || result.Answer != "REF-42 and REF-43" || lookups.Load() != 1 || calls.Load() != 3 || len(result.Delegations) != 2 {
		t.Fatalf("%+v %v calls=%d lookups=%d", result, err, calls.Load(), lookups.Load())
	}
	resumed, reused := 0, 0
	for i, e := range events {
		if e.Sequence != uint64(i+1) {
			t.Fatal("event sequence reset")
		}
		if e.Type == "run.resumed" {
			resumed++
			if e.Attempt != 2 {
				t.Fatal("wrong attempt")
			}
		}
		if e.Type == "tool.reused" {
			reused++
		}
	}
	if resumed != 1 || reused != 1 {
		t.Fatalf("missing continuation events: %d %d", resumed, reused)
	}
	report, err := Inspect(root, spec)
	if err != nil || report.Outcome != "terminal" || report.CanResume || report.NextAttempt != 0 || len(report.Operations) != 2 {
		t.Fatalf("%+v %v", report, err)
	}
	replay, err := Resume(context.Background(), root, spec, nil, nil, func(agent.Event) error { return nil })
	if err != nil || replay.Answer != result.Answer || calls.Load() != 3 {
		t.Fatal("terminal continuation replayed execution")
	}
}

func TestResumeIndexesLargeEvidenceAndRetrievesItWithoutRedispatch(t *testing.T) {
	state := prefix()
	root, _ := fixture(t, state)
	large := json.RawMessage(`{"label":"REF-42","noise":"` + strings.Repeat("X", 200_000) + `"}`)
	if report, err := Reconcile(root, state.Spec, []Receipt{{ID: 1, Instruction: "read", Result: large}}); err != nil || !report.CanResume {
		t.Fatalf("reconcile: %+v %v", report, err)
	}
	answers := []string{
		`{"javascriptCode":"final('Answer',{})"}`,
		`{"javascriptCode":"const saved=harnessSavedOperation(1); final('Answer',{label:saved.result.label});"}`,
		`{"answer":"REF-42"}`,
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		body, _ := io.ReadAll(r.Body)
		if len(body) > 100_000 || strings.Contains(string(body), strings.Repeat("X", 1000)) {
			t.Errorf("saved 200 KB result leaked into model request %d (bytes=%d)", index+1, len(body))
		}
		if index == 0 && !strings.Contains(string(body), "result_ref") {
			t.Error("resume omitted the saved-result reference")
		}
		if index >= len(answers) {
			http.Error(w, "unexpected request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := Resume(context.Background(), root, state.Spec, client, func(context.Context, string) (json.RawMessage, error) {
		t.Error("saved evidence was redispatched")
		return nil, errors.New("unexpected lookup")
	}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Answer != "REF-42" || calls.Load() != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestLiveLargeObservationBecomesRunReferenceAndSurvivesResume(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "large-live", InputID: "input", Prompt: "Verify the saved receipt"}
	large := json.RawMessage(`{"label":"REF-42","noise":"` + strings.Repeat("X", 200_000) + `"}`)
	var reads atomic.Int32
	tools := agent.Tools{Capabilities: []agent.Capability{{Name: "large_read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a large receipt.",
		InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads.Add(1)
			return large, nil
		}}}}
	first, _ := proposalModel(t, `const ref=large_read({}); console.log(ref.reference);`)
	_, err := RunWithTools(context.Background(), root, spec, first, tools, func(e agent.Event) error {
		if e.Type == "tool.finished" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || reads.Load() != 1 {
		t.Fatalf("large result was not saved before interruption: err=%v reads=%d", err, reads.Load())
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume || len(report.Operations) != 1 || !report.Operations[0].Finished {
		t.Fatalf("saved observation unavailable: report=%+v err=%v", report, err)
	}
	answers := []string{
		`{"javascriptCode":"final('Verify saved receipt',{});"}`,
		`{"javascriptCode":"const saved=harnessSavedOperation(1); final('Report receipt',{label:saved.result.label});"}`,
		`{"answer":"REF-42"}`,
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		body, _ := io.ReadAll(r.Body)
		if len(body) > 100_000 || strings.Contains(string(body), strings.Repeat("X", 1000)) {
			t.Errorf("large result leaked into resumed model request %d: %d bytes", index+1, len(body))
		}
		if index >= len(answers) {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Answer != "REF-42" || reads.Load() != 1 || calls.Load() != 3 {
		t.Fatalf("result=%+v err=%v reads=%d calls=%d", result, err, reads.Load(), calls.Load())
	}
}

func TestTerminalReplayRejectsAnotherAdmissionScope(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "scoped-result", InputID: "input", Prompt: "Answer"}
	client, calls := proposalModel(t, `final('Answer',{});`)
	first := agent.Tools{Scope: "tenant-A"}
	result, err := RunWithTools(context.Background(), root, spec, client, first, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || calls.Load() != 3 {
		t.Fatalf("initial result=%+v err=%v calls=%d", result, err, calls.Load())
	}
	if _, err := RunWithTools(context.Background(), root, spec, nil, agent.Tools{Scope: "tenant-B"}, func(agent.Event) error { return nil }); err == nil {
		t.Fatal("another admission scope replayed a terminal result")
	}
	replayed, err := RunWithTools(context.Background(), root, spec, nil, first, func(agent.Event) error { return nil })
	if err != nil || replayed.Answer != result.Answer || calls.Load() != 3 {
		t.Fatalf("same-scope replay=%+v err=%v calls=%d", replayed, err, calls.Load())
	}
}

func TestResumeKeepsPersistedToolFailureIncomplete(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "failed-tool", InputID: "input", Prompt: "Save the file"}
	var writes int
	tools := agent.Tools{Capabilities: []agent.Capability{{Name: "file_write", Version: "1", Origin: "fixture", Effect: "durable", Description: "Save a file.",
		InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			writes++
			return json.RawMessage(`{"is_error":true,"error":"write_denied"}`), nil
		}}}}
	client, _ := proposalModel(t, `const receipt=file_write({}); final('Report completion',{receipt});`)
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "tool.finished" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || writes != 1 {
		t.Fatalf("failed tool was not checkpointed: err=%v writes=%d", err, writes)
	}
	client, calls := proposalModel(t, `const receipt=file_write({}); final('Report completion',{receipt});`)
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Kind != "partial" || result.Code != "incomplete_result" || writes != 1 || calls.Load() != 3 {
		t.Fatalf("result=%+v err=%v writes=%d calls=%d", result, err, writes, calls.Load())
	}
}

func TestModelUsageSurvivesResume(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "usage-resume", InputID: "input", Prompt: "Answer"}
	answers := []string{
		`{"javascriptCode":"final('Continue',{})"}`,
		`{"javascriptCode":"final('Finish',{})"}`,
		`{"javascriptCode":"final('Answer',{})"}`,
		`{"answer":"Done"}`,
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop")),
			"usage", ax.Object("prompt_tokens", 10, "completion_tokens", 2, "total_tokens", 12)))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	_, err := RunWithTools(context.Background(), root, spec, client, agent.Tools{}, func(e agent.Event) error {
		if e.Type == "model.request.finished" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("first attempt err=%v calls=%d", err, calls.Load())
	}
	result, err := ResumeWithTools(context.Background(), root, spec, client, agent.Tools{}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Usage == nil || result.Usage.Requests != 4 || result.Usage.Reported != 4 ||
		result.Usage.InputTokens != 40 || result.Usage.OutputTokens != 8 || result.Usage.TotalTokens != 48 || result.Usage.Coverage != "complete" || calls.Load() != 4 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestDurableModelChargeSurvivesInterruptedDelivery(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "cost-resume", InputID: "input", Prompt: "Answer", MaxCostMicros: 4100}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", `{"javascriptCode":"final('Answer',{})"}`), "finish_reason", "stop")),
			"usage", ax.Object("prompt_tokens", 10, "completion_tokens", 5, "total_tokens", 15)))
	}))
	defer server.Close()
	service := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	service.Name = "priced"
	router, err := ax.NewMultiServiceRouter([]ax.Value{ax.RouterServiceEntry{Key: "priced", Service: service}})
	if err != nil {
		t.Fatal(err)
	}
	client := &agent.RoutedClient{AIClient: router, Stages: agent.StageModels{Context: "priced", Executor: "priced", Responder: "priced"},
		PricingVersion: "test-v1", Prices: map[string]agent.TokenPrice{"priced": {InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}}}
	_, err = RunWithTools(context.Background(), root, spec, client, agent.Tools{}, func(e agent.Event) error {
		if e.Type == "model.request.finished" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || requests.Load() != 1 {
		t.Fatalf("interruption err=%v requests=%d", err, requests.Load())
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume {
		t.Fatalf("inspection=%+v err=%v", report, err)
	}
	result, err := ResumeWithTools(context.Background(), root, spec, client, agent.Tools{}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Code != "cost_budget_exceeded" || result.Cost == nil ||
		result.Cost.ChargedMicros != 15 || requests.Load() != 1 {
		t.Fatalf("resume result=%+v cost=%+v err=%v requests=%d", result, result.Cost, err, requests.Load())
	}
}

func TestCommittedRuntimeStateSurvivesResumeWithoutReplayingTool(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "state-resume", InputID: "input", Prompt: "Find reference"}
	answers := []string{`{"javascriptCode":"final('Read',{})"}`, `{"javascriptCode":"const row=read({}); final('Use row',{row});"}`,
		`{"javascriptCode":"final('Continue',{})"}`, `{"javascriptCode":"final('Answer',{})"}`, `{"answer":"REF-42"}`}
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		if len(requests) > len(answers) {
			http.Error(w, "unexpected call", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[len(requests)-1]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	reads, hooks := 0, 0
	tools := agent.Tools{StateHookVersion: "test-v1", AfterTool: func(_ context.Context, op agent.SavedOperation) (*agent.RuntimeStateUpdate, error) {
		hooks++
		if op.Error != "" {
			t.Fatalf("failed receipt: %+v", op)
		}
		return &agent.RuntimeStateUpdate{Target: "root/responder", State: json.RawMessage(`{"workflow_phase":"verified"}`)}, nil
	}, Capabilities: []agent.Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read fixture.",
		InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads++
			return json.RawMessage(`{"reference":"REF-42"}`), nil
		}}}}
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "runtime.state.updated" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || len(requests) != 2 || reads != 1 || hooks != 1 {
		t.Fatalf("initial err=%v requests=%d reads=%d hooks=%d", err, len(requests), reads, hooks)
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume {
		t.Fatalf("inspect=%+v err=%v", report, err)
	}
	changed := tools
	changed.StateHookVersion = "test-v2"
	if _, err := ResumeWithTools(context.Background(), root, spec, client, changed, func(agent.Event) error { return nil }); err == nil || len(requests) != 2 {
		t.Fatalf("changed state hook admitted: err=%v requests=%d", err, len(requests))
	}
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || len(requests) != 5 || reads != 1 || hooks != 1 ||
		strings.Count(requests[2], "workflow_phase") != 1 {
		t.Fatalf("resume result=%+v err=%v requests=%d reads=%d hooks=%d state=%d", result, err, len(requests), reads, hooks,
			func() int {
				if len(requests) > 2 {
					return strings.Count(requests[2], "workflow_phase")
				}
				return -1
			}())
	}
}

func TestObservedTokenReservationSurvivesResume(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "token-resume", InputID: "input", Prompt: "Read status", MaxModelTokens: 11000}
	answers := []string{`{"javascriptCode":"final('Read status',{})"}`, `{"javascriptCode":"const status=native_status({}); final('Done',{status});"}`}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected call", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop")),
			"usage", ax.Object("prompt_tokens", 4000, "completion_tokens", 1000, "total_tokens", 5000)))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	var reads int
	tools := agent.Tools{Capabilities: []agent.Capability{{Name: "native_status", Version: "1", Origin: "fixture", Effect: "read", Description: "Read status.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads++
			return json.RawMessage(`{"status":"ok"}`), nil
		}}}}
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "tool.finished" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || calls.Load() != 2 || reads != 1 {
		t.Fatalf("first attempt err=%v calls=%d reads=%d", err, calls.Load(), reads)
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume {
		t.Fatalf("recovery=%+v err=%v", report, err)
	}
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Code != "model_token_budget_exceeded" || calls.Load() != 2 || reads != 1 ||
		result.Usage == nil || result.Usage.TotalTokens != 10000 {
		t.Fatalf("result=%+v err=%v calls=%d reads=%d", result, err, calls.Load(), reads)
	}
}

func TestGraphJinTokenChargeSurvivesDurableResume(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "graphjin-token-resume", InputID: "input", Prompt: "Find row", MaxModelTokens: 74_000}
	answers := []string{`{"javascriptCode":"final('Use lookup',{})"}`, `{"javascriptCode":"const data=lookup('find the row'); final('Done',{data});"}`}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop")),
			"usage", ax.Object("prompt_tokens", 4000, "completion_tokens", 1000, "total_tokens", 5000)))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	var lookups int
	lookup := func(context.Context, string) (json.RawMessage, error) {
		lookups++
		return json.RawMessage(`{"response":{"status":"answered","usage":{"total_tokens":60000}}}`), nil
	}
	_, err := Run(context.Background(), root, spec, client, lookup, func(e agent.Event) error {
		if e.Type == "tool.finished" {
			if e.RemoteUsage == nil || !e.RemoteUsage.Reported || e.RemoteUsage.TotalTokens != 60000 {
				t.Fatalf("missing GraphJin usage receipt: %+v", e.RemoteUsage)
			}
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || calls.Load() != 2 || lookups != 1 {
		t.Fatalf("first attempt err=%v calls=%d lookups=%d", err, calls.Load(), lookups)
	}
	report, err := Inspect(root, spec)
	if err != nil || !report.CanResume {
		t.Fatalf("recovery=%+v err=%v", report, err)
	}
	var replayedUsage int
	result, err := Resume(context.Background(), root, spec, client, lookup, func(e agent.Event) error {
		if e.Type == "tool.finished" && e.RemoteUsage != nil && e.RemoteUsage.TotalTokens == 60000 {
			replayedUsage++
		}
		return nil
	})
	if err != nil || result.Status != "failed" || result.Code != "model_token_budget_exceeded" || calls.Load() != 2 || lookups != 1 || replayedUsage != 1 {
		t.Fatalf("result=%+v err=%v calls=%d lookups=%d replayed_usage=%d", result, err, calls.Load(), lookups, replayedUsage)
	}
}

func TestFailedGraphJinLookupRecordsMissingUsageWithoutForgingReceipt(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "graphjin-failed-usage", InputID: "input", Prompt: "Find row"}
	answers := []string{`{"javascriptCode":"final('Use lookup',{})"}`, `{"javascriptCode":"const data=lookup('find the row'); final('Done',{data});"}`}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected call", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop")),
			"usage", ax.Object("prompt_tokens", 4000, "completion_tokens", 1000, "total_tokens", 5000)))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	var finished agent.Event
	_, err := Run(context.Background(), root, spec, client, func(context.Context, string) (json.RawMessage, error) {
		return json.RawMessage(`{"response":{"usage":{"total_tokens":60000}}}`), errors.New("broker failed")
	}, func(e agent.Event) error {
		if e.Type == "tool.finished" {
			finished = e
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || finished.RemoteUsage == nil || finished.RemoteUsage.Reported || finished.RemoteUsage.ChargedTokens != 12*4096 || len(finished.Data) != 0 {
		t.Fatalf("err=%v remote usage=%+v data=%s", err, finished.RemoteUsage, finished.Data)
	}
	if report, err := Inspect(root, spec); err != nil || !report.CanResume {
		t.Fatalf("failed lookup checkpoint: %+v %v", report, err)
	}
}

func TestResumeRefusesUnknownAndPersistsAttemptBudgetBeforeModel(t *testing.T) {
	s := prefix()
	root, _ := fixture(t, s)
	if report, err := Inspect(root, s.Spec); err != nil || report.CanResume || report.NextAttempt != 0 {
		t.Fatalf("unknown eligibility: %+v %v", report, err)
	}
	if _, err := Resume(context.Background(), root, s.Spec, nil, nil, func(agent.Event) error { t.Fatal("unknown operations emitted"); return nil }); err == nil {
		t.Fatal("unknown outcome admitted")
	}
	root, spec := resolvedPrefix(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected model call", 400)
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	for attempt := uint64(2); attempt <= 3; attempt++ {
		if report, err := Inspect(root, spec); err != nil || !report.CanResume || report.NextAttempt != attempt {
			t.Fatalf("attempt eligibility: %+v %v", report, err)
		}
		_, err := Resume(context.Background(), root, spec, client, nil, func(e agent.Event) error {
			if e.Type == "run.resumed" && e.Attempt == attempt {
				return errors.New("delivery failure after durable admission")
			}
			return nil
		})
		if err == nil {
			t.Fatal("missing delivery error")
		}
	}
	if _, err := Resume(context.Background(), root, spec, client, nil, func(agent.Event) error { return nil }); err == nil || !strings.Contains(err.Error(), "attempt limit") {
		t.Fatalf("budget reset: %v", err)
	}
	if report, err := Inspect(root, spec); err != nil || report.CanResume || report.NextAttempt != 0 {
		t.Fatalf("exhausted eligibility: %+v %v", report, err)
	}
	if calls.Load() != 0 {
		t.Fatal("model called after admission delivery failure")
	}
}

func TestResumeCannotResetTotalLookupBudget(t *testing.T) {
	s := prefix()
	s.Events = s.Events[:1]
	s.Operations = nil
	for id := 1; id <= 4; id++ {
		result := json.RawMessage(`{"response":{"answer":"saved"}}`)
		s.Operations = append(s.Operations, operation{ID: id, Instruction: strings.Repeat("read", id), Result: result, Finished: true})
		s.Events = append(s.Events, agent.Event{Version: 1, RunID: "r", InputID: "i", Sequence: uint64(len(s.Events) + 1), Type: "tool.started", Name: "lookup", OperationID: uint64(id)})
		s.Events = append(s.Events, agent.Event{Version: 1, RunID: "r", InputID: "i", Sequence: uint64(len(s.Events) + 1), Type: "tool.finished", Name: "lookup", OperationID: uint64(id), Data: result})
	}
	root, _ := fixture(t, s)
	var calls atomic.Int32
	answers := []string{`{"javascriptCode":"final('Continue',{})"}`, `{"javascriptCode":"const result=lookup('another query'); final('Answer',{result});"}`, `{"answer":"More evidence was unavailable."}`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if n >= len(answers) {
			http.Error(w, "unexpected request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[n]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := Resume(context.Background(), root, s.Spec, client, func(context.Context, string) (json.RawMessage, error) {
		t.Fatal("operation budget reset")
		return nil, nil
	}, func(agent.Event) error { return nil })
	if err != nil || result.Kind != "partial" {
		t.Fatalf("%+v %v", result, err)
	}
	report, err := Inspect(root, s.Spec)
	if err != nil || len(report.Operations) != 4 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestInspectAcceptedRunBeforeFirstEvent(t *testing.T) {
	state := prefix()
	state.Events = nil
	state.Operations = nil
	root, _ := fixture(t, state)
	report, err := Inspect(root, state.Spec)
	if err != nil || !report.CanResume || report.NextAttempt != 1 || report.Sequence != 0 || report.Operations == nil {
		t.Fatalf("accepted checkpoint eligibility: %+v %v", report, err)
	}
}

func TestRunPreservesLookupBeyondDefaultGojaDeadline(t *testing.T) {
	var calls atomic.Int32
	answers := []string{`{"javascriptCode":"final('Find the reference',{})"}`, `{"javascriptCode":"const evidence=lookup('read'); final('Answer',{evidence});"}`, `{"answer":"REF-42"}`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		body, _ := io.ReadAll(r.Body)
		if n == 2 && !strings.Contains(string(body), "REF-42") {
			t.Error("slow lookup evidence lost")
		}
		if n >= len(answers) {
			http.Error(w, "unexpected request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[n]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := Run(context.Background(), t.TempDir(), prefix().Spec, client, func(ctx context.Context, _ string) (json.RawMessage, error) {
		select {
		case <-time.After(6 * time.Second):
			return json.RawMessage(`{"reference":"REF-42"}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || calls.Load() != 3 {
		t.Fatalf("%+v %v calls=%d", result, err, calls.Load())
	}
}
