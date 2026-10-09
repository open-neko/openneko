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

func TestActorExhaustionFinalizesOnlyCommittedEvidence(t *testing.T) {
	for _, useReceipt := range []bool{false, true} {
		name := "without receipt"
		if useReceipt {
			name = "with receipt"
		}
		t.Run(name, func(t *testing.T) {
			answers := []string{`{"javascriptCode":"final('Investigate',{})"}`}
			for step := 0; step < 8; step++ {
				code := `console.log('Still investigating');`
				if step == 0 && useReceipt {
					code = `const row=read({}); console.log(row.receipt);`
				}
				answers = append(answers, `{"javascriptCode":`+mustJSON(t, code)+`}`)
			}
			if useReceipt {
				answers = append(answers, `Answer: Receipt R-42 was read.`)
			}
			var finalizerRequest string
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if calls >= len(answers) {
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if calls == 9 {
					finalizerRequest = string(body)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[calls]), "finish_reason", "stop"))))
				calls++
			}))
			defer server.Close()
			client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
			reads, finalizerChecks, terminalChecks := 0, 0, 0
			tools := Tools{StateHookVersion: "finalizer-state-v1", AfterTool: func(_ context.Context, op SavedOperation) (*RuntimeStateUpdate, error) {
				if !useReceipt {
					return nil, nil
				}
				if op.Name() != "read" || op.Error != "" {
					t.Fatalf("invalid state hook receipt: %+v", op)
				}
				return &RuntimeStateUpdate{Target: "root/responder", State: json.RawMessage(`{"receipt":"R-42"}`)}, nil
			}, FinalizerGateVersion: "evidence-v1", FinalizerGate: func(_ context.Context, ops []SavedOperation) (TerminalDecision, error) {
				finalizerChecks++
				if useReceipt && len(ops) == 1 && string(ops[0].Result) == `{"receipt":"R-42"}` {
					return TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}, nil
				}
				return TerminalDecision{Accepted: false}, nil
			}, TerminalGateVersion: "answer-v1", TerminalGate: func(_ context.Context, result Result, ops []SavedOperation) (TerminalDecision, error) {
				terminalChecks++
				if result.Answer != "Receipt R-42 was read." || len(ops) != 1 {
					t.Errorf("unverified finalizer candidate: %+v %+v", result, ops)
				}
				return TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}, nil
			}, Capabilities: []Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read receipt.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				reads++
				return json.RawMessage(`{"receipt":"R-42"}`), nil
			}}}}
			var events []Event
			result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: name, InputID: "input", Prompt: "Find receipt", MaxModelCalls: 12}, client, tools,
				func(e Event) error { events = append(events, e); return nil })
			if err != nil || finalizerChecks != 1 || reads != btoi(useReceipt) {
				t.Fatalf("result=%+v err=%v calls=%d gate=%d reads=%d", result, err, calls, finalizerChecks, reads)
			}
			if useReceipt {
				if result.Status != "completed" || result.Answer != "Receipt R-42 was read." || calls != 10 || terminalChecks != 1 ||
					!strings.Contains(finalizerRequest, "R-42") || strings.Contains(finalizerRequest, "Available JavaScript function") {
					t.Fatalf("finalizer did not use bounded evidence: result=%+v calls=%d terminal=%d request=%s", result, calls, terminalChecks, finalizerRequest)
				}
				var admitted, model, terminal, superseded, applied bool
				for _, e := range events {
					admitted = admitted || e.Type == "finalizer.admitted"
					model = model || e.Type == "model.request.started" && e.Stage == "terminal_finalizer"
					terminal = terminal || e.Type == "terminal.checked"
					superseded = superseded || e.Type == "runtime.state.superseded" && e.OperationID == 1 && e.Origin == "terminal_finalizer"
					applied = applied || e.Type == "runtime.state.applied"
				}
				if !admitted || !model || !terminal || !superseded || applied {
					t.Fatalf("missing finalizer lifecycle events: %+v", events)
				}
			} else if result.Status != "failed" || result.Code != "actor_steps_exhausted" || calls != 9 || terminalChecks != 0 {
				t.Fatalf("unjustified finalization: result=%+v calls=%d terminal=%d", result, calls, terminalChecks)
			}
		})
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
