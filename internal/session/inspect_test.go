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
