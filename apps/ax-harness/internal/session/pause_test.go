package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

func TestClarificationPausesActorAndSurvivesResume(t *testing.T) {
	answers := []string{
		`{"javascriptCode":"final('Ask for the missing detail',{})"}`,
		`{"javascriptCode":"const q=ask({questions:[{question:'Which day?'}]}); final('Wait for the operator',{q});"}`,
	}
	var modelCalls, toolCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(modelCalls.Add(1)) - 1
		if index >= len(answers) {
			http.Error(w, "unexpected model call", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[index]), "finish_reason", "stop"))))
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	tools := agent.Tools{Capabilities: []agent.Capability{{
		Name: "ask", Version: "1", Origin: "fixture", Effect: "pause", Description: "Ask the operator and end the turn.",
		InputSchema: json.RawMessage(`{"type":"object","required":["questions"],"properties":{"questions":{"type":"array"}},"additionalProperties":false}`),
		Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			toolCalls.Add(1)
			return json.RawMessage(`{"is_error":false,"content":["needs_input"]}`), nil
		},
	}}}
	spec := agent.Spec{Version: 1, RunID: "pause", InputID: "input", Prompt: "Ask me for the day"}
	root := t.TempDir()
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(event agent.Event) error {
		if event.Type == "run.finished" {
			return errors.New("terminal delivery interrupted")
		}
		return nil
	})
	if err == nil || modelCalls.Load() != 2 || toolCalls.Load() != 1 {
		t.Fatalf("first attempt err=%v model=%d tool=%d", err, modelCalls.Load(), toolCalls.Load())
	}
	result, err := ResumeWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || result.Kind != "clarification" || modelCalls.Load() != 2 || toolCalls.Load() != 1 {
		t.Fatalf("resumed result=%+v err=%v model=%d tool=%d", result, err, modelCalls.Load(), toolCalls.Load())
	}
}
