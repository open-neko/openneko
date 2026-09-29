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
	"reflect"
	"syscall"

	"github.com/open-neko/harness/internal/agent"
)

// Recovery is evidence, not authorization to repeat an operation. Inspect never
// contacts models/tools or rewrites a checkpoint. Operations may contain content.
type Recovery struct {
	Sequence    uint64        `json:"sequence"`
	CanResume   bool          `json:"can_resume"`
	NextAttempt uint64        `json:"next_attempt,omitempty"`
	Version     int           `json:"version"`
	RunID       string        `json:"run_id"`
	Outcome     string        `json:"outcome"`
	Operations  []operation   `json:"operations"`
	Result      *agent.Result `json:"result,omitempty"`
}

// Inspect requires exact trusted input and obtains the same lock as execution.
// A busy process is not a stopped attempt; an unfinished remote operation remains
// unknown even after its local process has gone away.
func Inspect(root string, spec agent.Spec) (Recovery, error) {
	return inspect(root, spec, nil)
}

// Receipt is trusted host evidence for one previously admitted operation.
type Receipt struct {
	Tool        string          `json:"tool,omitempty"`
	Binding     string          `json:"binding,omitempty"`
	ID          int             `json:"id"`
	Instruction string          `json:"instruction"`
	Result      json.RawMessage `json:"result"`
}

// Reconcile repairs missing operation results under the execution lock. It never
// executes a model/tool, changes a terminal answer, or claims VM continuation.
func Reconcile(root string, spec agent.Spec, receipts []Receipt) (Recovery, error) {
	if len(receipts) == 0 || len(receipts) > spec.OperationLimit() || spec.OperationLimit() > 32 {
		return Recovery{}, fmt.Errorf("invalid recovery receipts")
	}
	return inspect(root, spec, receipts)
}

func inspect(root string, spec agent.Spec, receipts []Receipt) (Recovery, error) {
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
	if receipts != nil {
		if state.Result != nil {
			return Recovery{}, fmt.Errorf("terminal checkpoint cannot be reconciled")
		}
		seen := map[int]bool{}
		for _, receipt := range receipts {
			if receipt.ID < 1 || receipt.ID > len(state.Operations) || seen[receipt.ID] ||
				len(receipt.Result) == 0 || len(receipt.Result) > 262144 || !json.Valid(receipt.Result) ||
				bytes.Equal(bytes.TrimSpace(receipt.Result), []byte("null")) {
				return Recovery{}, fmt.Errorf("invalid recovery receipt")
			}
			seen[receipt.ID] = true
			op := &state.Operations[receipt.ID-1]
			if op.Name() != (agent.SavedOperation{Tool: receipt.Tool}).Name() || op.Binding != receipt.Binding || op.Instruction != receipt.Instruction || (op.Finished && (op.Error != "" || !sameJSON(op.Result, receipt.Result))) {
				return Recovery{}, fmt.Errorf("recovery receipt conflicts with operation")
			}
			op.Result, op.Finished = receipt.Result, true
			ended := false
			for _, event := range state.Events {
				if event.Type == "tool.finished" && event.OperationID == uint64(receipt.ID) {
					if event.Error != "" || !sameJSON(event.Data, receipt.Result) {
						return Recovery{}, fmt.Errorf("receipt conflicts with published tool result")
					}
					ended = true
				}
			}
			if !ended {
				finished := agent.Event{Version: 1, RunID: spec.RunID, InputID: spec.InputID,
					Sequence: uint64(len(state.Events) + 1), Type: "tool.finished", Name: op.Name(), OperationID: uint64(receipt.ID), Data: receipt.Result}
				if op.Name() == "lookup" {
					usage := agent.GraphJinRemoteUsage(receipt.Result)
					finished.RemoteUsage = &usage
				}
				state.Events = append(state.Events, finished)
			}
		}
		data, err := json.Marshal(state)
		if err != nil {
			return Recovery{}, err
		}
		if _, err = decodeCheckpoint(data, spec); err != nil {
			return Recovery{}, err
		}
		if err = saveCheckpoint(root, path, state); err != nil {
			return Recovery{}, err
		}
	}
	report := Recovery{Sequence: uint64(len(state.Events)), Version: 1, RunID: spec.RunID, Outcome: "interrupted", Operations: append([]operation{}, state.Operations...), Result: state.Result}
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
	if state.Result == nil {
		if prior, err := continuation(state); err == nil {
			report.CanResume = true
			report.NextAttempt = prior.Attempt
		}
	}
	return report, nil
}

func sameJSON(a, b any) bool {
	decode := func(value any) (any, error) {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var result any
		err = decoder.Decode(&result)
		return result, err
	}
	x, xerr := decode(a)
	y, yerr := decode(b)
	return xerr == nil && yerr == nil && reflect.DeepEqual(x, y)
}

// Validate the durable prefix before either recovery or ordinary replay. Partial
// tool sequences are allowed only without a terminal result.
func decodeCheckpoint(data []byte, spec agent.Spec) (checkpoint, error) {
	var s checkpoint
	invalid := func() (checkpoint, error) { return checkpoint{}, fmt.Errorf("invalid or inconsistent checkpoint") }
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if len(data) > 8<<20 || !json.Valid(data) || decoder.Decode(&s) != nil || s.Version != 1 {
		return invalid()
	}
	if s.Spec != spec {
		return checkpoint{}, fmt.Errorf("run input conflicts with accepted input")
	}
	if s.Catalog != "" {
		if len(s.Catalog) != 64 {
			return invalid()
		}
		if _, err := hex.DecodeString(s.Catalog); err != nil {
			return invalid()
		}
	}
	if s.ScopeHash != "" {
		if len(s.ScopeHash) != 64 {
			return invalid()
		}
		if _, err := hex.DecodeString(s.ScopeHash); err != nil {
			return invalid()
		}
	}
	if spec.Version != 1 || spec.OperationLimit() < 1 || spec.OperationLimit() > 32 || spec.ModelCallLimit() < 1 || spec.ModelCallLimit() > 64 || spec.MaxModelTokens < 0 || spec.MaxModelTokens > 10_000_000 || spec.RunID == "" || spec.InputID == "" || spec.Prompt == "" || len(spec.SkillQuery) > 8192 {
		return invalid()
	}
	if len(s.Operations) > spec.OperationLimit() {
		return invalid()
	}
	for i, op := range s.Operations {
		if op.ID != i+1 || !op.ValidInput() || len(op.Result) > 262144 || len(op.Error) > 128 {
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
		if op.Name() == "propose" && len(op.Result) > 0 {
			if _, err := agent.ParseProposalReceipt(op.Result); err != nil {
				return invalid()
			}
		}
	}
	attempt := uint64(1)
	modelCalls := 0
	modelFinished := map[uint64]bool{}
	modelStages := map[uint64]string{}
	observedUsage := agent.ModelUsage{}
	started := map[uint64]string{}
	ended := map[uint64]bool{}
	children := map[uint64]bool{}
	finishedChildren := map[uint64]bool{}
	stageSummaries := map[uint64]map[string]bool{}
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
		if e.Result != nil && e.Type != "run.finished" || e.Usage != nil && e.Type != "model.request.finished" ||
			e.StageUsage != nil && e.Type != "model.stage_usage" ||
			e.RemoteUsage != nil && (e.Type != "tool.finished" || e.Name != "lookup") ||
			e.Stage != "" && e.Type != "model.request.started" && e.Type != "model.request.finished" {
			return invalid()
		}
		switch e.Type {
		case "run.started", "span.started", "span.finished":
		case "child.admitted":
			if e.Name != "team.researcher" || e.SpanID != 0 {
				return invalid()
			}
		case "child.started":
			if e.Name != "team.researcher" || e.SpanID == 0 || e.ParentID == 0 || children[e.SpanID] {
				return invalid()
			}
			children[e.SpanID] = true
		case "child.finished":
			if e.Name != "team.researcher" || !children[e.SpanID] || finishedChildren[e.SpanID] {
				return invalid()
			}
			finishedChildren[e.SpanID] = true
		case "model.request.started":
			modelCalls++
			if modelCalls > spec.ModelCallLimit() || e.CallID != 0 && e.CallID != uint64(modelCalls) || e.Stage != "" && e.Stage != "skill_selection" {
				return invalid()
			}
			modelStages[uint64(modelCalls)] = e.Stage
		case "model.request.finished":
			if e.CallID == 0 || e.CallID > uint64(modelCalls) || modelFinished[e.CallID] || e.Stage != modelStages[e.CallID] {
				return invalid()
			}
			modelFinished[e.CallID] = true
			if u := e.Usage; u != nil && (u.Requests != 0 || u.Reported != 1 || u.Coverage != "" ||
				u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 || u.ReasoningTokens < 0 ||
				u.InputTokens > 1_000_000_000_000 || u.OutputTokens > 1_000_000_000_000 || u.TotalTokens > 1_000_000_000_000 ||
				u.CacheReadTokens > 1_000_000_000_000 || u.CacheWriteTokens > 1_000_000_000_000 || u.ReasoningTokens > 1_000_000_000_000) {
				return invalid()
			}
			if e.Usage != nil {
				observedUsage.AddReported(*e.Usage)
			}
		case "model.stage_usage":
			if !validStageSummary(e.Name, e.StageUsage, spec.ModelCallLimit()) {
				return invalid()
			}
			if stageSummaries[attempt] == nil {
				stageSummaries[attempt] = map[string]bool{}
			}
			if stageSummaries[attempt][e.Name] {
				return invalid()
			}
			stageSummaries[attempt][e.Name] = true
		case "skill.selected":
			if e.Origin != "exact" && e.Origin != "semantic" || e.Name != "" && !agent.ValidSkillName(e.Name) ||
				e.Origin == "exact" && (e.Name == "" || e.Error != "") ||
				e.Error != "" && e.Error != "selection_unavailable" && e.Error != "invalid_selection" {
				return invalid()
			}
		case "run.resumed":
			attempt++
			if attempt > 3 || e.Attempt != attempt || len(started) != len(ended) {
				return invalid()
			}
		case "tool.reused":
			if !ended[e.OperationID] || e.Name != started[e.OperationID] {
				return invalid()
			}
		case "tool.started":
			if e.OperationID != uint64(len(started)+1) || e.OperationID > uint64(spec.OperationLimit()) || !agent.ValidToolName(e.Name) {
				return invalid()
			}
			started[e.OperationID] = e.Name
			if e.OperationID <= uint64(len(s.Operations)) && s.Operations[e.OperationID-1].Name() != e.Name {
				return invalid()
			}
		case "tool.finished":
			if started[e.OperationID] == "" || ended[e.OperationID] || e.Name != started[e.OperationID] {
				return invalid()
			}
			if e.RemoteUsage != nil && *e.RemoteUsage != agent.GraphJinRemoteUsage(e.Data) {
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
		if u := s.Result.Usage; u != nil {
			observedUsage.Requests = modelCalls
			actual := *u
			actual.Coverage = ""
			coverage := "unavailable"
			if u.Reported > 0 {
				coverage = "partial"
			}
			if u.Requests > 0 && u.Reported == u.Requests {
				coverage = "complete"
			}
			if actual != observedUsage || u.Coverage != coverage {
				return invalid()
			}
		}
		var expectedProposals []agent.ProposalReceipt
		for _, op := range s.Operations {
			if op.Name() == "propose" && op.Error == "" {
				receipt, err := agent.ParseProposalReceipt(op.Result)
				if err != nil {
					return invalid()
				}
				expectedProposals = append(expectedProposals, receipt)
			}
		}
		if !sameJSON(expectedProposals, s.Result.Proposals) {
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

func validStageSummary(name string, usage *agent.ModelUsage, limit int) bool {
	switch name {
	case "distiller", "executor", "responder", "child.distiller", "child.executor", "child.responder", "unattributed":
	default:
		return false
	}
	if usage == nil || usage.Requests < 1 || usage.Requests > limit || usage.Reported < 0 || usage.Reported > usage.Requests {
		return false
	}
	coverage := "unavailable"
	if usage.Reported == usage.Requests {
		coverage = "complete"
	} else if usage.Reported > 0 {
		coverage = "partial"
	}
	if usage.Coverage != coverage {
		return false
	}
	for _, count := range []int64{usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens} {
		if count < 0 || count > 1_000_000_000_000 {
			return false
		}
	}
	return true
}
