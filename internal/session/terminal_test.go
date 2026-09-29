package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/open-neko/harness/internal/agent"
)

func TestTerminalGateReplaysCommittedEvidenceAfterInterruptedDelivery(t *testing.T) {
	root := t.TempDir()
	spec := agent.Spec{Version: 1, RunID: "verified-run", InputID: "input", Prompt: "Read and report receipt"}
	readCalls, gateCalls := 0, 0
	tools := agent.Tools{TerminalGateVersion: "receipt-v1", TerminalGate: func(_ context.Context, candidate agent.Result, ops []agent.SavedOperation) (agent.TerminalDecision, error) {
		gateCalls++
		if candidate.Status != "completed" || len(ops) != 1 || ops[0].ID != 1 || !ops[0].Finished || string(ops[0].Result) != `{"receipt":"R-42"}` {
			t.Errorf("missing committed evidence: candidate=%+v operations=%+v", candidate, ops)
		}
		return agent.TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}, nil
	}, Capabilities: []agent.Capability{{Name: "read", Version: "1", Origin: "fixture", Effect: "read", Description: "Read receipt.", InputSchema: json.RawMessage(`{"type":"object"}`), Call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		readCalls++
		return json.RawMessage(`{"receipt":"R-42"}`), nil
	}}}}
	client, _ := proposalModel(t, `const receipt=read({}); final('Report receipt',{receipt});`)
	_, err := RunWithTools(context.Background(), root, spec, client, tools, func(e agent.Event) error {
		if e.Type == "terminal.checked" {
			return errors.New("delivery interrupted")
		}
		return nil
	})
	if err == nil || readCalls != 1 || gateCalls != 1 {
		t.Fatalf("expected interrupted delivery after one operation: err=%v reads=%d gates=%d", err, readCalls, gateCalls)
	}
	recovery, err := Inspect(root, spec)
	if err != nil || !recovery.CanResume || recovery.Result != nil {
		t.Fatalf("interrupted checkpoint invalid: %+v %v", recovery, err)
	}
	changed := tools
	changed.TerminalGateVersion = "receipt-v2"
	if _, err := ResumeWithTools(context.Background(), root, spec, client, changed, func(agent.Event) error { return nil }); err == nil {
		t.Fatal("resumed with changed terminal policy")
	}
	resumedClient, _ := proposalModel(t, `final('Report saved receipt',{});`)
	result, err := ResumeWithTools(context.Background(), root, spec, resumedClient, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || readCalls != 1 || gateCalls != 2 {
		t.Fatalf("resume result=%+v err=%v reads=%d gates=%d", result, err, readCalls, gateCalls)
	}
	recovery, err = Inspect(root, spec)
	if err != nil || recovery.Outcome != "terminal" || recovery.Result == nil {
		t.Fatalf("terminal checkpoint invalid: %+v %v", recovery, err)
	}
}

func TestInspectRejectsForgedTerminalEvidenceAndOutcome(t *testing.T) {
	for _, mutate := range []func(*checkpoint){
		func(s *checkpoint) { s.Events[3].Terminal.EvidenceIDs = []int{2} },
		func(s *checkpoint) { s.Result.Status = "failed"; s.Result.Code = "verification_failed" },
		func(s *checkpoint) { s.Events[3].Error = "verification_failed" },
	} {
		s := terminal()
		decision := &agent.TerminalDecision{Accepted: true, EvidenceIDs: []int{1}}
		s.Events = append(s.Events[:3], append([]agent.Event{{Version: 1, RunID: "r", InputID: "i", Sequence: 4, Type: "terminal.checked", Origin: "receipt-v1", Terminal: decision}}, s.Events[3:]...)...)
		s.Events[4].Sequence = 5
		mutate(&s)
		root, _ := fixture(t, s)
		if _, err := Inspect(root, s.Spec); err == nil {
			t.Fatal("accepted forged terminal verification")
		}
	}
}
