package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	"github.com/open-neko/harness/internal/agent"
)

func TestFinalizerCheckpointAndQueueRedelivery(t *testing.T) {
	answers := []string{`{"javascriptCode":"final('Investigate',{})"}`}
	for step := 0; step < 8; step++ {
		code := `console.log('Still investigating');`
		if step == 0 {
			code = `const row=read({}); console.log(row.receipt);`
		}
		encoded, _ := json.Marshal(code)
		answers = append(answers, `{"javascriptCode":`+string(encoded)+`}`)
	}
	answers = append(answers, `{"answer":"Receipt R-42 was read."}`)
	modelCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if modelCalls >= len(answers) {
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[modelCalls]), "finish_reason", "stop"))))
		modelCalls++
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
	reads, checks := 0, 0
	tools := agent.Tools{FinalizerGateVersion: "evidence-v1", FinalizerGate: func(_ context.Context, ops []agent.SavedOperation) (agent.TerminalDecision, error) {
		if len(ops) != 1 || string(ops[0].Result) != `{"receipt":"R-42"}` {
			t.Errorf("missing committed receipt: %+v", ops)
		}
		return agent.TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}, nil
	}, TerminalGateVersion: "answer-v1", TerminalGate: func(_ context.Context, candidate agent.Result, ops []agent.SavedOperation) (agent.TerminalDecision, error) {
		checks++
		if candidate.Answer != "Receipt R-42 was read." || len(ops) != 1 {
			t.Errorf("unverified answer: %+v %+v", candidate, ops)
		}
		return agent.TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}, nil
	}, Capabilities: []agent.Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read receipt.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		reads++
		return json.RawMessage(`{"receipt":"R-42"}`), nil
	}}}}
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "finalizer-replay", InputID: "input", Prompt: "Find receipt", MaxModelCalls: 12}
	result, err := RunWithTools(context.Background(), root, spec, client, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || modelCalls != 10 || reads != 1 || checks != 1 {
		t.Fatalf("result=%+v err=%v model=%d reads=%d checks=%d", result, err, modelCalls, reads, checks)
	}
	recovery, err := Inspect(root, spec)
	if err != nil || recovery.Outcome != "terminal" || len(recovery.Operations) != 1 {
		t.Fatalf("finalizer checkpoint invalid: %+v %v", recovery, err)
	}
	replayed, err := RunWithTools(context.Background(), root, spec, nil, tools, func(agent.Event) error { return nil })
	if err != nil || replayed.Answer != result.Answer || modelCalls != 10 || reads != 1 || checks != 1 {
		t.Fatalf("queue redelivery repeated work: %+v %v model=%d reads=%d checks=%d", replayed, err, modelCalls, reads, checks)
	}
}
