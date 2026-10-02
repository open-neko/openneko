package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestOwnedChildSharesRunBudgetAndReadScope(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Find two references',{})"}`,
		`{"javascriptCode":"const one=team.researcher({question:'Find first reference'}); const two=team.researcher({question:'Find second reference'}); final('Two references',{one,two});"}`,
		`{"javascriptCode":"final('Find first reference',{})"}`,
		`{"javascriptCode":"const found=catalog({key:'first'}); final('Found',{found});"}`,
		`{"answer":"REF-1"}`,
		`{"javascriptCode":"final('Find second reference',{})"}`,
		`{"javascriptCode":"const found=catalog({key:'second'}); final('Found',{found});"}`,
		`{"answer":"REF-2"}`,
		`{"answer":"REF-1 and REF-2"}`,
	}
	var modelCalls, reads, writes atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(modelCalls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer model.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	read := Capability{Name: "catalog", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a fixture.", InputSchema: json.RawMessage(`{"type":"object","required":["key"],"properties":{"key":{"type":"string"}},"additionalProperties":false}`), Call: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		reads.Add(1)
		return json.Marshal(map[string]json.RawMessage{"reference": raw})
	}}
	write := Capability{Name: "write", Version: "1", Origin: "fixture", Effect: "durable", Description: "Write a fixture.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		writes.Add(1)
		return json.RawMessage(`{}`), nil
	}}
	var events []Event
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "child", InputID: "input", Prompt: "Find two references", MaxOperations: 4, MaxModelCalls: 16}, client, Tools{Capabilities: []Capability{read, write}, ChildReads: []string{"catalog"}}, func(e Event) error { events = append(events, e); return nil })
	if err != nil || result.Status != "completed" || result.Answer != "REF-1 and REF-2" || reads.Load() != 2 || writes.Load() != 0 || result.Usage == nil || result.Usage.Requests != int(modelCalls.Load()) {
		t.Fatalf("result=%+v err=%v calls=%d reads=%d writes=%d events=%d", result, err, modelCalls.Load(), reads.Load(), writes.Load(), len(events))
	}
	started, finished := 0, 0
	stageCalls := map[string]int{}
	for _, event := range events {
		if event.Type == "child.started" {
			started++
		}
		if event.Type == "child.finished" {
			finished++
		}
		if event.Type == "model.stage_usage" && event.StageUsage != nil {
			stageCalls[event.Name] = event.StageUsage.Requests
		}
	}
	if started != 2 || finished != 2 {
		t.Fatalf("child lifecycle events started=%d finished=%d", started, finished)
	}
	for _, stage := range []string{"distiller", "executor", "responder"} {
		if stageCalls[stage] != 1 || stageCalls["child."+stage] != 2 {
			t.Fatalf("stage request counts=%v", stageCalls)
		}
	}
	modelCalls.Store(0)
	reads.Store(0)
	events = nil
	result, err = RunWithTools(context.Background(), Spec{Version: 1, RunID: "child-budget", InputID: "input", Prompt: "Find two references", MaxOperations: 4, MaxModelCalls: 4}, client, Tools{Capabilities: []Capability{read, write}, ChildReads: []string{"catalog"}}, func(e Event) error { events = append(events, e); return nil })
	if err != nil || result.Status != "failed" || result.Code != "model_budget_exceeded" || modelCalls.Load() != 4 || writes.Load() != 0 {
		t.Fatalf("child escaped shared budget: result=%+v err=%v calls=%d writes=%d", result, err, modelCalls.Load(), writes.Load())
	}
	modelCalls.Store(0)
	reads.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err = RunWithTools(ctx, Spec{Version: 1, RunID: "child-cancel", InputID: "input", Prompt: "Find two references", MaxOperations: 4, MaxModelCalls: 16}, client, Tools{Capabilities: []Capability{read, write}, ChildReads: []string{"catalog"}}, func(e Event) error {
		if e.Type == "model.request.started" && e.CallID == 3 {
			cancel()
		}
		return nil
	})
	if err != nil || result.Status != "cancelled" || reads.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("child survived parent cancellation: result=%+v err=%v reads=%d writes=%d", result, err, reads.Load(), writes.Load())
	}
	modelCalls.Store(0)
	result, err = RunWithTools(context.Background(), Spec{Version: 1, RunID: "child-disabled", InputID: "input", Prompt: "Find two references", MaxOperations: 4, MaxModelCalls: 16}, client, Tools{Capabilities: []Capability{read, write}}, func(Event) error { return nil })
	if err != nil || result.Status != "failed" || reads.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("disabled child was callable: result=%+v err=%v reads=%d writes=%d", result, err, reads.Load(), writes.Load())
	}
}

func TestChildToolFailureCannotBecomeSuccessfulParentAnswer(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Delegate verification',{})"}`,
		`{"javascriptCode":"const child=team.researcher({question:'Verify reference'}); final('Use child result',{child});"}`,
		`{"javascriptCode":"final('Read the reference',{})"}`,
		`{"javascriptCode":"const found=catalog({key:'reference'}); final('Report evidence',{found});"}`,
		`{"answer":"Reference verified"}`,
		`{"answer":"The reference is verified."}`,
	}
	var calls, reads atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer model.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	read := Capability{Name: "catalog", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a reference.",
		InputSchema: json.RawMessage(`{"type":"object","required":["key"],"properties":{"key":{"type":"string"}},"additionalProperties":false}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads.Add(1)
			return json.RawMessage(`{"is_error":true,"content":["reference unavailable"]}`), nil
		}}
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "child-failed-read", InputID: "input", Prompt: "Verify the reference", MaxOperations: 4, MaxModelCalls: 12}, client,
		Tools{Capabilities: []Capability{read}, ChildReads: []string{"catalog"}}, func(Event) error { return nil })
	if err != nil || result.Status != "failed" || result.Code != "incomplete_result" || reads.Load() != 1 {
		t.Fatalf("failed child read was reported as success: result=%+v err=%v reads=%d models=%d", result, err, reads.Load(), calls.Load())
	}
}

func TestChildLargeResultReferenceStaysInChildRuntime(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Delegate read',{});"}`,
		`{"javascriptCode":"const child=team.researcher({question:'Verify receipt'}); let escaped=false; try { harnessSavedOperation(1); } catch (err) { escaped=true; } if (!escaped) throw new Error('child receipt escaped'); final('Answer',{child});"}`,
		`{"javascriptCode":"final('Read receipt',{});"}`,
		`{"javascriptCode":"const ref=catalog({}); if (!ref.reference) throw new Error('missing reference'); const saved=harnessSavedOperation(1); final('Verified',{label:saved.result.label});"}`,
		`{"answer":"REF-42"}`,
		`{"answer":"REF-42"}`,
	}
	var calls, reads atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), strings.Repeat("X", 1000)) {
			t.Error("child's large observation leaked into model context")
		}
		if index >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer model.Close()
	client := &RoutedClient{AIClient: ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture")),
		Stages: StageModels{Context: "fixture", Executor: "fixture", Responder: "fixture"}}
	read := Capability{Name: "catalog", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a large receipt.",
		InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			reads.Add(1)
			return json.Marshal(ax.Object("label", "REF-42", "noise", strings.Repeat("X", 200_000)))
		}}
	var modelStages []string
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "child-reference", InputID: "input", Prompt: "Verify receipt", MaxOperations: 4, MaxModelCalls: 8}, client,
		Tools{Capabilities: []Capability{read}, ChildReads: []string{"catalog"}}, func(event Event) error {
			if event.Type == "model.request.started" {
				modelStages = append(modelStages, event.Stage)
			}
			return nil
		})
	if err != nil || result.Status != "completed" || reads.Load() != 1 || calls.Load() != int32(len(answers)) {
		t.Fatalf("result=%+v err=%v reads=%d calls=%d", result, err, reads.Load(), calls.Load())
	}
	wantStages := []string{"distiller", "executor", "child.distiller", "child.executor", "child.responder", "responder"}
	if !reflect.DeepEqual(modelStages, wantStages) {
		t.Fatalf("model call stages = %v, want %v", modelStages, wantStages)
	}
}
