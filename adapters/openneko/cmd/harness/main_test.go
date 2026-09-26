package main

import (
	"context"
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
