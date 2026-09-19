package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/open-neko/harness/internal/agent"
)

// Recovery is evidence, not authorization to repeat an operation. Inspect never
// contacts models/tools or rewrites a checkpoint. Operations may contain content.
type Recovery struct {
	Version    int           `json:"version"`
	RunID      string        `json:"run_id"`
	Outcome    string        `json:"outcome"`
	Operations []operation   `json:"operations"`
	Result     *agent.Result `json:"result,omitempty"`
}

// Inspect requires exact trusted input and obtains the same lock as execution.
// A busy process is not a stopped attempt; an unfinished remote operation remains
// unknown even after its local process has gone away.
func Inspect(root string, spec agent.Spec) (Recovery, error) {
	if root == "" || spec.RunID == "" {
		return Recovery{}, fmt.Errorf("invalid recovery input")
	}
	sum := sha256.Sum256([]byte(spec.RunID))
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		return Recovery{}, fmt.Errorf("recovery lock unavailable")
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return Recovery{}, fmt.Errorf("run still executing; recovery refused")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	file, err := os.Open(path + ".json")
	if err != nil {
		return Recovery{}, fmt.Errorf("checkpoint unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil {
		return Recovery{}, fmt.Errorf("checkpoint unreadable")
	}
	state, err := decodeCheckpoint(data, spec)
	if err != nil {
		return Recovery{}, err
	}
	report := Recovery{Version: 1, RunID: spec.RunID, Outcome: "interrupted", Operations: state.Operations, Result: state.Result}
	if state.Result != nil {
		report.Outcome = "terminal"
	} else {
		for _, op := range state.Operations {
			if !op.Finished {
				report.Outcome = "outcome_unknown"
				break
			}
		}
	}
	return report, nil
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// Validate the durable prefix before either recovery or ordinary replay. Partial
// tool sequences are allowed only without a terminal result.
func decodeCheckpoint(data []byte, spec agent.Spec) (checkpoint, error) {
	var s checkpoint
	invalid := func() (checkpoint, error) { return checkpoint{}, fmt.Errorf("invalid or inconsistent checkpoint") }
	if len(data) > 8<<20 || json.Unmarshal(data, &s) != nil || s.Version != 1 {
		return invalid()
	}
	if s.Spec != spec {
		return checkpoint{}, fmt.Errorf("run input conflicts with accepted input")
	}
	if spec.Version != 1 || spec.RunID == "" || spec.InputID == "" || spec.Prompt == "" {
		return invalid()
	}
	if len(s.Operations) > 4 {
		return invalid()
	}
	for i, op := range s.Operations {
		if op.ID != i+1 || len(op.Instruction) == 0 || len(op.Instruction) > 8000 || len(op.Result) > 262144 {
			return invalid()
		}
		if !op.Finished && (len(op.Result) != 0 || op.Error != "") {
			return invalid()
		}
		if op.Finished && ((len(op.Result) == 0) == (op.Error == "")) {
			return invalid()
		}
		if len(op.Result) > 0 && (!json.Valid(op.Result) || bytes.Equal(bytes.TrimSpace(op.Result), []byte("null"))) {
			return invalid()
		}
	}
	started := map[uint64]bool{}
	ended := map[uint64]bool{}
	for i, e := range s.Events {
		if e.Version != 1 || e.RunID != spec.RunID || e.InputID != spec.InputID || e.Sequence != uint64(i+1) {
			return invalid()
		}
		if i == 0 && e.Type != "run.started" {
			return invalid()
		}
		if e.Type == "run.started" && i != 0 {
			return invalid()
		}
		if e.Result != nil && e.Type != "run.finished" {
			return invalid()
		}
		switch e.Type {
		case "run.started", "span.started", "span.finished":
		case "tool.started":
			if e.OperationID != uint64(len(started)+1) || e.OperationID > 4 || e.Name != "lookup" {
				return invalid()
			}
			started[e.OperationID] = true
		case "tool.finished":
			if !started[e.OperationID] || ended[e.OperationID] || e.Name != "lookup" {
				return invalid()
			}
			ended[e.OperationID] = true
			if len(e.Data) > 0 {
				if e.OperationID > uint64(len(s.Operations)) || !sameJSON(e.Data, s.Operations[e.OperationID-1].Result) {
					return invalid()
				}
			}
		case "run.finished":
			if i != len(s.Events)-1 || s.Result == nil || e.Result == nil || !sameJSON(e.Result, s.Result) {
				return invalid()
			}
		default:
			return invalid()
		}
	}
	if len(s.Operations) > len(started) {
		return invalid()
	}
	if s.Result != nil {
		if len(s.Events) < 2 || s.Events[len(s.Events)-1].Type != "run.finished" || len(started) != len(ended) {
			return invalid()
		}
		if s.Result.Status != "completed" && s.Result.Status != "failed" && s.Result.Status != "cancelled" {
			return invalid()
		}
		if len(s.Result.Answer) > 65536 || (s.Result.Status == "completed" && s.Result.Answer == "") {
			return invalid()
		}
		for _, op := range s.Operations {
			if !op.Finished {
				return invalid()
			}
		}
	}
	return s, nil
}
