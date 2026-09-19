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
