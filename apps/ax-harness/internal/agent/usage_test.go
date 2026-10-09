package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestModelUsageFollowsEachAdmittedCall(t *testing.T) {
	answers := []string{
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
			"usage", ax.Object("prompt_tokens", (index+1)*10, "completion_tokens", index+3, "total_tokens", (index+1)*10+index+3)))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	var events []Event
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "usage", InputID: "input", Prompt: "Answer"},
		client, Tools{}, func(e Event) error { events = append(events, e); return nil })
	if err != nil || result.Status != "completed" || result.Usage == nil || result.Usage.Requests != 3 || result.Usage.Reported != 3 ||
		result.Usage.InputTokens != 60 || result.Usage.OutputTokens != 12 || result.Usage.TotalTokens != 72 || result.Usage.Coverage != "complete" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var started, finished int
	for _, event := range events {
		switch event.Type {
		case "model.request.started":
			started++
			if event.CallID != uint64(started) {
				t.Fatalf("wrong call admission: %+v", event)
			}
		case "model.request.finished":
			finished++
			if event.CallID != uint64(finished) || event.Usage == nil || event.Usage.Reported != 1 {
				t.Fatalf("missing per-call usage: %+v", event)
			}
		}
	}
	if started != 3 || finished != 3 || calls.Load() != 3 {
		t.Fatalf("starts=%d finishes=%d model=%d", started, finished, calls.Load())
	}
}

func TestModelUsageMarksMissingProviderReport(t *testing.T) {
	answers := []string{
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
		response := ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop")))
		if index != 1 {
			response["usage"] = ax.Object("prompt_tokens", 10, "completion_tokens", 2, "total_tokens", 12)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "partial-usage", InputID: "input", Prompt: "Answer"},
		client, Tools{}, func(Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Usage == nil || result.Usage.Requests != 3 || result.Usage.Reported != 2 ||
		result.Usage.TotalTokens != 24 || result.Usage.Coverage != "partial" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestObservedTokenBudgetStopsBeforeNextModelRequest(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Do work',{})"}`, `{"javascriptCode":"final('Answer',{})"}`}
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
	var events []Event
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "token-stop", InputID: "input", Prompt: "Answer", MaxModelTokens: 11000},
		client, Tools{}, func(e Event) error { events = append(events, e); return nil })
	if err != nil || result.Status != "failed" || result.Code != "model_token_budget_exceeded" || calls.Load() != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
	if result.Usage == nil || result.Usage.TotalTokens != 10000 || result.Usage.Coverage != "complete" {
		t.Fatalf("usage=%+v", result.Usage)
	}
	var admissions int
	for _, event := range events {
		if event.Type == "model.request.started" {
			admissions++
		}
	}
	if admissions != 2 {
		t.Fatalf("admissions=%d", admissions)
	}
}

func TestMissingUsageConsumesConservativeTokenReservation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", `{"javascriptCode":"final('Do work',{})"}`), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "token-missing", InputID: "input", Prompt: "Answer", MaxModelTokens: 8000},
		client, Tools{}, func(Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Code != "model_token_budget_exceeded" || calls.Load() != 1 || result.Usage == nil || result.Usage.Coverage != "unavailable" {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestSingleModelRequestOvershootFailsTerminally(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", `{"javascriptCode":"final('Done',{})"}`), "finish_reason", "stop")),
			"usage", ax.Object("prompt_tokens", 6000, "completion_tokens", 1000, "total_tokens", 7000)))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "token-overshoot", InputID: "input", Prompt: "Answer", MaxModelTokens: 5000},
		client, Tools{}, func(Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Code != "model_token_budget_exceeded" || calls.Load() != 1 ||
		result.Usage == nil || result.Usage.TotalTokens != 7000 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestGraphJinLookupSharesModelTokenAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, runID string
		limit       int64
		lookups     int
	}{
		{name: "denied before remote dispatch", runID: "remote-denied", limit: 55_000, lookups: 0},
		{name: "remote usage stops next model call", runID: "remote-spent", limit: 74_000, lookups: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answers := []string{`{"javascriptCode":"final('Need lookup',{})"}`, `{"javascriptCode":"const data=lookup('find the row'); final('Done',{data});"}`}
			var modelCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				index := int(modelCalls.Add(1)) - 1
				if index >= len(answers) {
					http.Error(w, "unexpected model call", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop")),
					"usage", ax.Object("prompt_tokens", 4000, "completion_tokens", 1000, "total_tokens", 5000)))
			}))
			defer server.Close()
			client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
			lookups := 0
			lookup := func(context.Context, string) (json.RawMessage, error) {
				lookups++
				return json.RawMessage(`{"response":{"status":"answered","usage":{"prompt_tokens":50000,"completion_tokens":10000,"total_tokens":60000,"llm_calls":3}}}`), nil
			}
			var events []Event
			result, err := Run(context.Background(), Spec{Version: 1, RunID: tc.runID, InputID: "input", Prompt: "Find row", MaxModelTokens: tc.limit},
				client, lookup, func(e Event) error { events = append(events, e); return nil })
			if err != nil || result.Status != "failed" || result.Code != "model_token_budget_exceeded" || modelCalls.Load() != 2 || lookups != tc.lookups {
				t.Fatalf("result=%+v err=%v model=%d lookups=%d", result, err, modelCalls.Load(), lookups)
			}
			var remoteEvents int
			for _, event := range events {
				if event.RemoteUsage == nil {
					continue
				}
				remoteEvents++
				if event.Type != "tool.finished" || event.Name != "lookup" || !event.RemoteUsage.Reported ||
					event.RemoteUsage.TotalTokens != 60000 || event.RemoteUsage.ChargedTokens != 60000 || event.RemoteUsage.LLMCalls != 3 {
					t.Fatalf("remote usage event=%+v", event)
				}
			}
			if remoteEvents != tc.lookups {
				t.Fatalf("remote usage events=%d, want %d", remoteEvents, tc.lookups)
			}
		})
	}
}

func TestGraphJinUsageChargeDoesNotCountNestedEvidence(t *testing.T) {
	result := json.RawMessage(`{"response":{"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8},"data":{"usage":{"total_tokens":900000}}}}`)
	if got := lookupTokenCharge(result); got != 8 {
		t.Fatalf("charged nested usage: %d", got)
	}
	if got := lookupTokenCharge(json.RawMessage(`{"response":{"data":{"usage":{"total_tokens":900000}}}}`)); got != remoteLookupReservation {
		t.Fatalf("missing usage charge=%d", got)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"response":{"usage":{"total_tokens":-1}}}`),
		json.RawMessage(`{"response":{"usage":{"total_tokens":1000000000001}}}`),
		json.RawMessage(`{"response":{"usage":{"prompt_tokens":1000000000000,"completion_tokens":1}}}`),
	} {
		if usage := GraphJinRemoteUsage(raw); usage.Reported || usage.ChargedTokens != remoteLookupReservation {
			t.Fatalf("invalid usage escaped reservation: %+v", usage)
		}
	}
}

func TestResumedGraphJinUsageCannotResetTokenAllowance(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected model call", http.StatusBadRequest)
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	prior := Continuation{Attempt: 2, Sequence: 4, ModelCalls: 2, MaxReportedCallTokens: 5000,
		Usage: ModelUsage{Requests: 2, Reported: 2, InputTokens: 8000, OutputTokens: 2000, TotalTokens: 10000},
		Operations: []SavedOperation{{ID: 1, Instruction: "find the row", Finished: true,
			Result: json.RawMessage(`{"response":{"status":"answered","usage":{"total_tokens":60000}}}`)}},
	}
	result, err := RunAttempt(context.Background(), Spec{Version: 1, RunID: "remote-resume", InputID: "input", Prompt: "Find row", MaxModelTokens: 74_000},
		client, func(context.Context, string) (json.RawMessage, error) {
			t.Fatal("lookup redispatched")
			return nil, nil
		},
		func(Event) error { return nil }, prior)
	if err != nil || result.Status != "failed" || result.Code != "model_token_budget_exceeded" || calls.Load() != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}
