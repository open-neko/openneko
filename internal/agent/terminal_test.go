package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestTerminalGateRequiresCommittedReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, executor, wantStatus, wantCode string
		decision                             TerminalDecision
		gateErr                              error
		wantOperations                       int
	}{
		{"missing receipt", `final('Answer',{});`, "failed", "verification_failed", TerminalDecision{Accepted: false}, nil, 0},
		{"committed receipt", `const row=read({}); final('Answer',{row});`, "completed", "", TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}, nil, 1},
		{"invented receipt", `final('Answer',{});`, "failed", "invalid_verification", TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}, nil, 0},
		{"verifier unavailable", `const row=read({}); final('Answer',{row});`, "failed", "verification_unavailable", TerminalDecision{}, errors.New("private verifier failure"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answers := []string{`{"javascriptCode":"final('Find evidence',{})"}`, `{"javascriptCode":` + mustJSON(t, tc.executor) + `}`, `{"answer":"Completed."}`}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls >= len(answers) {
					http.Error(w, "unexpected model request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[calls]), "finish_reason", "stop"))))
				calls++
			}))
			defer server.Close()
			client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
			var events []Event
			gateCalls := 0
			tools := Tools{TerminalGateVersion: "receipt-v1", TerminalGate: func(_ context.Context, candidate Result, ops []SavedOperation) (TerminalDecision, error) {
				gateCalls++
				if candidate.Answer != "Completed." || len(ops) != tc.wantOperations {
					t.Errorf("candidate=%+v operations=%+v", candidate, ops)
				}
				if len(ops) == 1 && (!ops[0].Finished || ops[0].Error != "" || string(ops[0].Result) != `{"receipt":"R-42"}`) {
					t.Errorf("gate lacks committed receipt: %+v", ops[0])
				}
				return tc.decision, tc.gateErr
			}, Capabilities: []Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a receipt.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"receipt":"R-42"}`), nil
			}}}}
			result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: tc.name, InputID: "input", Prompt: "Find evidence"}, client, tools,
				func(e Event) error { events = append(events, e); return nil })
			if err != nil || calls != 3 || gateCalls != 1 || result.Status != tc.wantStatus || result.Code != tc.wantCode {
				t.Fatalf("result=%+v err=%v calls=%d gateCalls=%d", result, err, calls, gateCalls)
			}
			if len(events) < 2 || events[len(events)-2].Type != "terminal.checked" || events[len(events)-1].Type != "run.finished" || events[len(events)-2].Terminal == nil {
				t.Fatalf("terminal decision was not journaled before completion: %+v", events)
			}
		})
	}
}

// A pending approval does not satisfy a workflow's independent output contract.
// The same host gate must verify both successful answers and approval outcomes.
func TestTerminalGateVerifiesPendingApproval(t *testing.T) {
	for _, withOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing output", true: "committed output"}[withOutput], func(t *testing.T) {
			executor := `const approval=propose({action:'record.update',arguments:{id:'R-42'},summary:'Update R-42'}); final('Report pending approval',{approval});`
			if withOutput {
				executor = `const approval=propose({action:'record.update',arguments:{id:'R-42'},summary:'Update R-42'}); const output=read({}); final('Report pending approval and output',{approval,output});`
			}
			answers := []string{`{"javascriptCode":"final('Prepare request',{})"}`, `{"javascriptCode":` + mustJSON(t, executor) + `}`, `{"answer":"Update R-42 remains pending approval."}`}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls >= len(answers) {
					http.Error(w, "unexpected request", 400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[calls]), "finish_reason", "stop"))))
				calls++
			}))
			defer server.Close()
			client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic", "model", "fixture"))
			gateCalls := 0
			tools := Tools{Propose: func(context.Context, Proposal) (ProposalReceipt, error) {
				return ProposalReceipt{ID: "approval-1", Status: "pending_approval"}, nil
			}, TerminalGateVersion: "workflow-output-v1", TerminalGate: func(_ context.Context, result Result, ops []SavedOperation) (TerminalDecision, error) {
				gateCalls++
				if result.Kind != "approval" || len(result.Proposals) != 1 || result.Proposals[0].ID != "approval-1" {
					t.Errorf("approval lost before terminal gate: %+v", result)
				}
				if withOutput && len(ops) == 2 && ops[1].Name() == "read" && string(ops[1].Result) == `{"outputId":"output-1"}` {
					return TerminalDecision{Accepted: true, EvidenceIDs: []int{2}}, nil
				}
				return TerminalDecision{Accepted: false}, nil
			}, Capabilities: []Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read committed output.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"outputId":"output-1"}`), nil
			}}}}
			var events []Event
			result, err := RunWithTools(context.Background(), Spec{Version: 1, RunID: t.Name(), InputID: "input", Prompt: "Prepare the update"}, client, tools,
				func(e Event) error { events = append(events, e); return nil })
			if err != nil || calls != 3 || gateCalls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d gate=%d", result, err, calls, gateCalls)
			}
			if withOutput {
				if result.Status != "completed" || result.Kind != "approval" {
					t.Fatalf("verified approval rejected: %+v", result)
				}
			} else if result.Status != "failed" || result.Code != "verification_failed" || result.Kind != "failure" {
				t.Fatalf("unverified approval accepted: %+v", result)
			}
			if len(events) < 2 || events[len(events)-2].Type != "terminal.checked" || events[len(events)-1].Type != "run.finished" {
				t.Fatalf("terminal decision not journaled: %+v", events)
			}
		})
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
