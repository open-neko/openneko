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
func TestInspectAndReplayRejectInconsistentCheckpoints(t *testing.T) {
	for name, mutate := range map[string]func(*checkpoint){
		"sequence":              func(s *checkpoint) { s.Events[1].Sequence = 9 },
		"missing result event":  func(s *checkpoint) { s.Events = s.Events[:3] },
		"unknown outcome":       func(s *checkpoint) { s.Operations[0].Finished = false; s.Operations[0].Result = nil },
		"wrong input":           func(s *checkpoint) { s.Events[1].InputID = "different" },
		"duplicate tool result": func(s *checkpoint) { s.Events[3] = s.Events[2]; s.Events[3].Sequence = 4 },
		"wrong evidence":        func(s *checkpoint) { s.Events[2].Data = json.RawMessage(`{"forged":true}`) },
		"invalid status":        func(s *checkpoint) { s.Result.Status = "invented" },
		"unsupported version":   func(s *checkpoint) { s.Version = 9 },
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
