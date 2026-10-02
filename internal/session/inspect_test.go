package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/open-neko/harness/internal/agent"
)

func fixture(t *testing.T, s checkpoint) (string, string) {
	t.Helper()
	root := t.TempDir()
	sum := sha256.Sum256([]byte(s.Spec.RunID))
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".json", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	return root, path
}
func prefix() checkpoint {
	spec := agent.Spec{Version: 1, RunID: "r", InputID: "i", Prompt: "q"}
	return checkpoint{Version: 1, Spec: spec, Events: []agent.Event{
		{Version: 1, RunID: "r", InputID: "i", Sequence: 1, Type: "run.started"},
		{Version: 1, RunID: "r", InputID: "i", Sequence: 2, Type: "tool.started", Name: "lookup", OperationID: 1},
	}, Operations: []operation{{ID: 1, Instruction: "read"}}}
}

func TestInspectBoundsMetadataOnlyToolCatalogEvents(t *testing.T) {
	spec := agent.Spec{Version: 1, RunID: "catalog-metrics", InputID: "input", Prompt: "Check a row"}
	event := func(seq uint64, kind string) agent.Event {
		return agent.Event{Version: 1, RunID: spec.RunID, InputID: spec.InputID, Sequence: seq, Type: kind}
	}
	state := checkpoint{Version: 1, Spec: spec, Events: []agent.Event{
		event(1, "run.started"),
		{Version: 1, RunID: spec.RunID, InputID: spec.InputID, Sequence: 2, Type: "tool.catalog.configured", Name: "parent",
			ToolCatalog: &agent.ToolCatalogProfile{Count: 1, SchemaBytes: 20, DescriptorBytes: 75}},
		{Version: 1, RunID: spec.RunID, InputID: spec.InputID, Sequence: 3, Type: "model.request.started", CallID: 1},
		{Version: 1, RunID: spec.RunID, InputID: spec.InputID, Sequence: 4, Type: "model.request.finished", CallID: 1},
		{Version: 1, RunID: spec.RunID, InputID: spec.InputID, Sequence: 5, Type: "tool.input.rejected", Name: "catalog", Error: "invalid_input"},
	}}
	valid, _ := json.Marshal(state)
	if _, err := decodeCheckpoint(valid, spec); err != nil {
		t.Fatalf("rejected valid metadata: %v", err)
	}
	state.Events[1].ToolCatalog.DescriptorBytes = 19
	invalid, _ := json.Marshal(state)
	if _, err := decodeCheckpoint(invalid, spec); err == nil {
		t.Fatal("accepted impossible advertised byte count")
	}
	state.Events[1].ToolCatalog.DescriptorBytes = 75
	state.Events[4].Data = json.RawMessage(`{"id":"secret"}`)
	invalid, _ = json.Marshal(state)
	if _, err := decodeCheckpoint(invalid, spec); err == nil {
		t.Fatal("accepted rejected tool arguments in telemetry")
	}
}
func TestInspectSeparatesUnknownFromSavedEvidence(t *testing.T) {
	for _, finished := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "saved"}[finished], func(t *testing.T) {
			state := prefix()
			expected := "outcome_unknown"
			if finished {
				state.Operations[0].Finished = true
				state.Operations[0].Result = json.RawMessage(`{"reference":"REF-42"}`)
				expected = "interrupted"
			}
			root, path := fixture(t, state)
			before, _ := os.ReadFile(path + ".json")
			report, err := Inspect(root, state.Spec)
			if err != nil || report.Outcome != expected || len(report.Operations) != 1 {
				t.Fatalf("%+v %v", report, err)
			}
			after, _ := os.ReadFile(path + ".json")
			if string(before) != string(after) {
				t.Fatal("inspection changed state")
			}
			lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
				t.Fatal("lock")
			}
			if _, err = Inspect(root, state.Spec); err == nil {
				t.Fatal("inspected active attempt")
			}
		})
	}
}
func terminal() checkpoint {
	s := prefix()
	s.Operations[0].Finished = true
	s.Operations[0].Result = json.RawMessage(`{"reference":"REF-42"}`)
	s.Events = append(s.Events, agent.Event{Version: 1, RunID: "r", InputID: "i", Sequence: 3, Type: "tool.finished", Name: "lookup", OperationID: 1, Data: s.Operations[0].Result})
	s.Result = &agent.Result{Status: "completed", Kind: "answer", Answer: "REF-42"}
	s.Events = append(s.Events, agent.Event{Version: 1, RunID: "r", InputID: "i", Sequence: 4, Type: "run.finished", Result: s.Result})
	return s
}

func TestInspectRejectsForgedRuntimeStateApplication(t *testing.T) {
	makeState := func() checkpoint {
		s := terminal()
		update := agent.Event{Version: 1, RunID: "r", InputID: "i", Sequence: 4, Type: "runtime.state.updated", OperationID: 1,
			StateUpdate: &agent.RuntimeStateUpdate{Target: "root/responder", State: json.RawMessage(`{"reference":"REF-42"}`)}}
		applied := agent.Event{Version: 1, RunID: "r", InputID: "i", Sequence: 5, Type: "runtime.state.applied", OperationID: 1, Origin: "next-response"}
		finished := s.Events[3]
		finished.Sequence = 6
		s.Events = append(s.Events[:3], update, applied, finished)
		return s
	}
	valid := makeState()
	root, _ := fixture(t, valid)
	if _, err := Inspect(root, valid.Spec); err != nil {
		t.Fatalf("valid state acknowledgement rejected: %v", err)
	}
	for name, mutate := range map[string]func(*checkpoint){
		"wrong operation": func(s *checkpoint) { s.Events[4].OperationID = 2 },
		"unknown timing":  func(s *checkpoint) { s.Events[4].Origin = "invented" },
		"before update": func(s *checkpoint) {
			s.Events[3], s.Events[4] = s.Events[4], s.Events[3]
			s.Events[3].Sequence = 4
			s.Events[4].Sequence = 5
		},
		"duplicate": func(s *checkpoint) {
			copy := s.Events[4]
			copy.Sequence = 6
			s.Events = append(s.Events[:5], append([]agent.Event{copy}, s.Events[5:]...)...)
			s.Events[6].Sequence = 7
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := makeState()
			mutate(&s)
			root, _ := fixture(t, s)
			if _, err := Inspect(root, s.Spec); err == nil {
				t.Fatal("accepted forged Ax application acknowledgement")
			}
		})
	}
}
func TestInspectAndReplayRejectInconsistentCheckpoints(t *testing.T) {
	for name, mutate := range map[string]func(*checkpoint){
		"sequence":              func(s *checkpoint) { s.Events[1].Sequence = 9 },
		"missing result event":  func(s *checkpoint) { s.Events = s.Events[:3] },
		"unknown outcome":       func(s *checkpoint) { s.Operations[0].Finished = false; s.Operations[0].Result = nil },
		"wrong input":           func(s *checkpoint) { s.Events[1].InputID = "different" },
		"duplicate tool result": func(s *checkpoint) { s.Events[3] = s.Events[2]; s.Events[3].Sequence = 4 },
		"wrong evidence":        func(s *checkpoint) { s.Events[2].Data = json.RawMessage(`{"forged":true}`) },
		"forged remote usage": func(s *checkpoint) {
			s.Events[2].RemoteUsage = &agent.RemoteUsage{TotalTokens: 1, ChargedTokens: 1, Reported: true}
		},
		"invalid status":      func(s *checkpoint) { s.Result.Status = "invented" },
		"unsupported version": func(s *checkpoint) { s.Version = 9 },
	} {
		t.Run(name, func(t *testing.T) {
			s := terminal()
			mutate(&s)
			root, _ := fixture(t, s)
			if _, err := Inspect(root, s.Spec); err == nil {
				t.Fatal("accepted corrupt checkpoint")
			}
			if _, err := Run(context.Background(), root, s.Spec, nil, nil, func(agent.Event) error { t.Error("replayed invalid checkpoint"); return nil }); err == nil {
				t.Fatal("normal replay accepted corrupt checkpoint")
			}

		})
	}
	s := terminal()
	root, _ := fixture(t, s)
	report, err := Inspect(root, s.Spec)
	if err != nil || report.Outcome != "terminal" || report.Result.Answer != "REF-42" {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestReconcileRestoresEvidenceWithoutExecutingOrCompletingRun(t *testing.T) {
	state := prefix()
	root, path := fixture(t, state)
	receipt := Receipt{ID: 1, Instruction: "read", Result: json.RawMessage(`{"response":{"answer":"REF-42"}}`)}
	report, err := Reconcile(root, state.Spec, []Receipt{receipt})
	if err != nil || report.Outcome != "interrupted" || report.Result != nil || !report.Operations[0].Finished {
		t.Fatalf("%+v %v", report, err)
	}
	data, _ := os.ReadFile(path + ".json")
	saved, err := decodeCheckpoint(data, state.Spec)
	if err != nil || len(saved.Events) != 3 || saved.Events[2].Type != "tool.finished" {
		t.Fatalf("missing paired event: %s %v", data, err)
	}
	if usage := saved.Events[2].RemoteUsage; usage == nil || usage.Reported || usage.ChargedTokens != 12*4096 {
		t.Fatalf("missing conservative remote usage event: %+v", usage)
	}
	if _, err = Reconcile(root, state.Spec, []Receipt{receipt}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path + ".json")
	if string(data) != string(after) {
		t.Fatal("idempotent reconciliation changed checkpoint")
	}
	if _, err = Run(context.Background(), root, state.Spec, nil, nil, func(agent.Event) error { t.Fatal("unexpected replay"); return nil }); err == nil {
		t.Fatal("repair authorized automatic execution")
	}
}

func TestReconcileRejectsConflictingReceiptsWithoutChangingCheckpoint(t *testing.T) {
	valid := Receipt{ID: 1, Instruction: "read", Result: json.RawMessage(`{"answer":"REF-42"}`)}
	for name, receipts := range map[string][]Receipt{
		"missing": nil, "duplicate": {valid, valid},
		"wrong instruction": {{ID: 1, Instruction: "write", Result: valid.Result}},
		"unknown operation": {{ID: 2, Instruction: "read", Result: valid.Result}},
		"null":              {{ID: 1, Instruction: "read", Result: json.RawMessage(`null`)}},
		"invalid":           {{ID: 1, Instruction: "read", Result: json.RawMessage(`{`)}},
	} {
		t.Run(name, func(t *testing.T) {
			state := prefix()
			root, path := fixture(t, state)
			before, _ := os.ReadFile(path + ".json")
			if _, err := Reconcile(root, state.Spec, receipts); err == nil {
				t.Fatal("accepted invalid receipt")
			}
			after, _ := os.ReadFile(path + ".json")
			if string(before) != string(after) {
				t.Fatal("changed checkpoint on rejection")
			}
		})
	}
	state := prefix()
	root, path := fixture(t, state)
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err = Reconcile(root, state.Spec, []Receipt{valid}); err == nil {
		t.Fatal("repaired active execution")
	}
}

func TestReconcileCannotReplacePublishedResultOrTerminalRun(t *testing.T) {
	for _, state := range []checkpoint{terminal(), prefix()} {
		if state.Result == nil {
			state.Events = append(state.Events, agent.Event{Version: 1, RunID: "r", InputID: "i", Sequence: 3, Type: "tool.finished", Name: "lookup", OperationID: 1, Error: "lookup_failed"})
		}
		root, path := fixture(t, state)
		before, _ := os.ReadFile(path + ".json")
		if _, err := Reconcile(root, state.Spec, []Receipt{{ID: 1, Instruction: "read", Result: json.RawMessage(`{"answer":"REF-42"}`)}}); err == nil {
			t.Fatal("overwrote published outcome")
		}
		after, _ := os.ReadFile(path + ".json")
		if string(before) != string(after) {
			t.Fatal("changed published outcome")
		}
	}
}

func TestReconcileIgnoresObjectOrderWithoutRoundingIntegers(t *testing.T) {
	state := prefix()
	root, _ := fixture(t, state)
	receipt := Receipt{ID: 1, Instruction: "read", Result: json.RawMessage(`{"response":{"id":9007199254740993,"label":"REF-42"},"trace":"saved"}`)}
	if _, err := Reconcile(root, state.Spec, []Receipt{receipt}); err != nil {
		t.Fatal(err)
	}
	// JSONB may return the same object in a different key order.
	receipt.Result = json.RawMessage(`{"trace":"saved","response":{"label":"REF-42","id":9007199254740993}}`)
	if _, err := Reconcile(root, state.Spec, []Receipt{receipt}); err != nil {
		t.Fatal(err)
	}
	receipt.Result = json.RawMessage(`{"trace":"saved","response":{"label":"REF-42","id":9007199254740992}}`)
	if _, err := Reconcile(root, state.Spec, []Receipt{receipt}); err == nil {
		t.Fatal("rounded distinct integers into matching evidence")
	}
	report, err := Inspect(root, state.Spec)
	if err != nil || !report.CanResume {
		t.Fatalf("%+v %v", report, err)
	}
}
