package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestCommittedStateHookSteersResponderOnce(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Read',{})"}`, `{"javascriptCode":"const row=read({}); final('Use row',{row});"}`, `{"answer":"REF-42"}`}
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
	var events []Event
	tools := Tools{StateHookVersion: "test-v1", AfterTool: func(_ context.Context, op SavedOperation) (*RuntimeStateUpdate, error) {
		if op.Name() != "read" || op.Error != "" {
			t.Fatalf("hook receipt=%+v", op)
		}
		return &RuntimeStateUpdate{Target: "root/responder", State: json.RawMessage(`{"workflow_phase":"verified"}`)}, nil
	}, Capabilities: []Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read fixture.",
		InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"reference":"REF-42"}`), nil
		}}}}
	result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: "state", InputID: "input", Prompt: "Find reference"}, client, tools,
		func(e Event) error { events = append(events, e); return nil })
	if err != nil || result.Status != "completed" || result.Answer != "REF-42" || len(requests) != 3 {
		t.Fatalf("result=%+v err=%v requests=%d", result, err, len(requests))
	}
	if strings.Contains(requests[0], "workflow_phase") || strings.Contains(requests[1], "workflow_phase") ||
		strings.Count(requests[2], "workflow_phase") != 1 {
		t.Fatalf("state not scoped to responder: %d, %d, %d", strings.Count(requests[0], "workflow_phase"), strings.Count(requests[1], "workflow_phase"), strings.Count(requests[2], "workflow_phase"))
	}
	var finished, updated int
	for i, event := range events {
		if event.Type == "tool.finished" {
			finished = i
		}
		if event.Type == "runtime.state.updated" {
			updated = i
			if event.StateUpdate == nil || !event.StateUpdate.Valid() {
				t.Fatalf("invalid update: %+v", event)
			}
		}
	}
	if updated <= finished {
		t.Fatalf("state was not journaled after result: finished=%d updated=%d", finished, updated)
	}
}
