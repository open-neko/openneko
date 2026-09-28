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
