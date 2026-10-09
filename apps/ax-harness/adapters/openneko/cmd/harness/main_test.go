package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/open-neko/harness/internal/agent"
)

func TestActionAdmissionNarrowsProposalBeforeBroker(t *testing.T) {
	called := 0
	broker := func(_ context.Context, proposal agent.Proposal) (agent.ProposalReceipt, error) {
		called++
		return agent.ProposalReceipt{ID: proposal.Action, Status: "pending_approval"}, nil
	}
	for _, raw := range []string{`[]`, `["a","a"]`, `[""]`, `{"kind":"a"}`} {
		if _, err := admitActions(raw, broker); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	tool, err := admitActions(`["pack.update"]`, broker)
	if err != nil {
		t.Fatal(err)
	}
	denied, err := tool(context.Background(), agent.Proposal{Action: "other"})
	if err != nil || denied.Status != "denied" || called != 0 {
		t.Fatalf("denied=%+v err=%v called=%d", denied, err, called)
	}
	approved, err := tool(context.Background(), agent.Proposal{Action: "pack.update"})
	if err != nil || approved.Status != "pending_approval" || called != 1 {
		t.Fatalf("approved=%+v err=%v called=%d", approved, err, called)
	}
}

func TestWorkflowOutputVerificationRequiresBoundBrokerReceipt(t *testing.T) {
	binding := "bound-capability"
	valid := agent.SavedOperation{ID: 1, Tool: "workflow_output_emit", Binding: binding, Finished: true,
		Result: json.RawMessage(`{"ok":true,"outputId":"output-1","kind":"finding"}`)}
	for _, tc := range []struct {
		name string
		ops  []agent.SavedOperation
		want bool
	}{
		{"missing", nil, false},
		{"broker receipt", []agent.SavedOperation{valid}, true},
		{"wrong binding", []agent.SavedOperation{{ID: 1, Tool: valid.Tool, Binding: "other", Finished: true, Result: valid.Result}}, false},
		{"missing output id", []agent.SavedOperation{{ID: 1, Tool: valid.Tool, Binding: binding, Finished: true, Result: json.RawMessage(`{"ok":true,"kind":"finding"}`)}}, false},
		{"failed broker result", []agent.SavedOperation{{ID: 1, Tool: valid.Tool, Binding: binding, Finished: true, Result: json.RawMessage(`{"ok":true,"is_error":true,"outputId":"output-1","kind":"finding"}`)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := workflowOutputEvidence(tc.ops, binding)
			if decision.Accepted != tc.want || tc.want && (len(decision.EvidenceIDs) != 1 || decision.EvidenceIDs[0] != 1) {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
	tools := agent.Tools{}
	bindWorkflowOutputVerification(&tools, binding)
	if tools.TerminalGate == nil || tools.FinalizerGate == nil || tools.TerminalGateVersion == "" || tools.FinalizerGateVersion == "" {
		t.Fatal("workflow verification was not installed")
	}
	decision, err := tools.TerminalGate(context.Background(), agent.Result{Status: "completed", Kind: "answer", Answer: "Done"}, []agent.SavedOperation{valid})
	if err != nil || !decision.Accepted {
		t.Fatalf("terminal gate rejected bound receipt: %+v %v", decision, err)
	}
}
