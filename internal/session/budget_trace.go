package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/budgettriage"
)

// BudgetTrace contains only validated budget metadata. It excludes the prompt,
// tool arguments and results, Ax state, and the terminal answer.
type BudgetTrace struct {
	RunID            string              `json:"run_id"`
	CheckpointSHA256 string              `json:"checkpoint_sha256"`
	Hard             budgettriage.Limits `json:"hard"`
	Status           string              `json:"status"`
	Events           []agent.Event       `json:"events"`
}

// ReadBudgetTrace reads a stopped run under its execution lock and validates
// the checkpoint before projecting content-free events. A live run is refused
// so an evaluator cannot mistake a partial snapshot for an outcome.
func ReadBudgetTrace(root, runID string) (BudgetTrace, error) {
	if root == "" || runID == "" || len(runID) > 128 {
		return BudgetTrace{}, fmt.Errorf("invalid budget trace identity")
	}
	sum := sha256.Sum256([]byte(runID))
	path := filepath.Join(root, hex.EncodeToString(sum[:]))
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		return BudgetTrace{}, fmt.Errorf("budget trace lock unavailable")
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return BudgetTrace{}, fmt.Errorf("run still executing; budget trace refused")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	file, err := os.Open(path + ".json")
	if err != nil {
		return BudgetTrace{}, fmt.Errorf("budget checkpoint unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return BudgetTrace{}, fmt.Errorf("budget checkpoint unreadable")
	}
	var header struct {
		Spec agent.Spec `json:"spec"`
	}
	if err := json.Unmarshal(data, &header); err != nil || header.Spec.RunID != runID {
		return BudgetTrace{}, fmt.Errorf("invalid budget checkpoint identity")
	}
	state, err := decodeCheckpoint(data, header.Spec)
	if err != nil {
		return BudgetTrace{}, err
	}
	digest := sha256.Sum256(data)
	trace := BudgetTrace{RunID: runID, CheckpointSHA256: hex.EncodeToString(digest[:]), Hard: budgettriage.Limits{MaxModelCalls: header.Spec.ModelCallLimit(),
		MaxModelTokens: header.Spec.MaxModelTokens, MaxCostMicros: header.Spec.MaxCostMicros}, Status: "interrupted"}
	if state.Result != nil {
		trace.Status = state.Result.Status
	}
	for _, e := range state.Events {
		switch e.Type {
		case "model.request.started", "model.request.finished", "tool.started", "tool.finished", "budget.triage.skipped", "budget.profile.proposed", "budget.profile.extended":
		default:
			continue
		}
		clean := agent.Event{Version: e.Version, Sequence: e.Sequence, Type: e.Type,
			CallID: e.CallID, OperationID: e.OperationID, Name: e.Name, Stage: e.Stage,
			Origin: e.Origin, Effect: e.Effect, DurationMS: e.DurationMS,
			Usage: e.Usage, RemoteUsage: e.RemoteUsage, CostMicros: e.CostMicros}
		if e.Type == "budget.profile.proposed" || e.Type == "budget.profile.extended" ||
			e.Type == "model.request.finished" && e.Stage == "budget_triage" {
			clean.Data = append(clean.Data, e.Data...)
		}
		trace.Events = append(trace.Events, clean)
	}
	return trace, nil
}
