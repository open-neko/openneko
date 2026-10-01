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
	"github.com/open-neko/harness/internal/budgettriage"
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
					if spec.MaxCostMicros > 0 {
						for _, event := range state.Events {
							if event.Type == "tool.started" && event.OperationID == uint64(receipt.ID) {
								finished.CostMicros = event.CostMicros // Retain reservation after ambiguous dispatch.
								break
							}
						}
					}
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
	if spec.Version != 1 || spec.OperationLimit() < 1 || spec.OperationLimit() > 32 || spec.ModelCallLimit() < 1 || spec.ModelCallLimit() > 64 || spec.MaxModelTokens < 0 || spec.MaxModelTokens > 10_000_000 || spec.MaxCostMicros < 0 || spec.MaxCostMicros > 1_000_000_000_000 || spec.TriageSummary != "" && (spec.MaxCostMicros == 0 || spec.MaxModelTokens == 0) || spec.RunID == "" || spec.InputID == "" || spec.Prompt == "" || len(spec.SkillQuery) > 8192 || !spec.ValidTriage() || !spec.ValidBudgetMode() {
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
	modelCosts := map[uint64]int64{}
	modelProviders := map[uint64]string{}
	modelFailed := map[uint64]bool{}
	lookupProposals := map[uint64]bool{}
	fallbacks := map[uint64]bool{}
	executorErrors := map[uint64]bool{}
	toolCosts := map[uint64]int64{}
	stateUpdates := map[uint64]bool{}
	stateApplied := map[uint64]bool{}
	stateFailures := map[uint64]bool{}
	var chargedCost int64
	observedUsage := agent.ModelUsage{}
	started := map[uint64]string{}
	ended := map[uint64]bool{}
	children := map[uint64]bool{}
	finishedChildren := map[uint64]bool{}
	stageSummaries := map[uint64]map[string]bool{}
	var terminalCheck *agent.Event
	finalizerEvent := false
	finalizerAdmitted := false
	finalizerCalls := 0
	var finalizerEvidenceIDs []int
	triageStarted, triageFinished, triageSkipped, triageProposed := false, false, false, false
	var triageObservation budgettriage.Observation
	var shadowProfile *budgettriage.Proposal
	var lastShadowExtensionOp uint64
	for i, e := range s.Events {
		if terminalCheck != nil && e.Type != "run.finished" && e.Type != "run.resumed" {
			return invalid()
		}
		if finalizerEvent && !finalizerAdmitted && e.Type != "run.finished" && e.Type != "run.resumed" ||
			finalizerAdmitted && (e.Type == "tool.started" || e.Type == "model.request.started" && e.Stage != "terminal_finalizer") {
			return invalid()
		}
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
			e.StateUpdate != nil && e.Type != "runtime.state.updated" ||
			e.Terminal != nil && e.Type != "terminal.checked" && e.Type != "finalizer.admitted" ||
			e.Stage != "" && e.Type != "model.request.started" && e.Type != "model.request.finished" {
			return invalid()
		}
		costEvent := e.Type == "model.request.started" || e.Type == "model.request.finished" ||
			(e.Type == "tool.started" || e.Type == "tool.finished") && e.Name == "lookup"
		if (spec.MaxCostMicros > 0 && costEvent) != (e.CostMicros != nil) ||
			e.CostMicros != nil && (*e.CostMicros < 1 || *e.CostMicros > 8_000_000_000_000_000) {
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
			if modelCalls > spec.ModelCallLimit() || e.CallID != 0 && e.CallID != uint64(modelCalls) ||
				e.Stage != "" && e.Stage != "skill_selection" && e.Stage != "terminal_finalizer" && e.Stage != "budget_triage" ||
				e.Stage == "terminal_finalizer" && (!finalizerAdmitted || finalizerCalls > 0) {
				return invalid()
			}
			if e.Stage == "budget_triage" {
				if spec.TriageSummary == "" || triageStarted || triageSkipped || modelCalls != 1 || e.Name == "" || e.Origin == "" ||
					e.CostMicros == nil || *e.CostMicros < 1 {
					return invalid()
				}
				triageStarted = true
			} else if spec.TriageSummary != "" && !triageSkipped && (!triageFinished || !triageProposed) {
				return invalid()
			}
			if e.Stage == "terminal_finalizer" {
				finalizerCalls++
			}
			modelStages[uint64(modelCalls)] = e.Stage
			modelProviders[uint64(modelCalls)] = e.Origin
			if e.CostMicros != nil {
				modelCosts[e.CallID] = *e.CostMicros
				chargedCost += *e.CostMicros
			}
		case "model.request.finished":
			if e.CallID == 0 || e.CallID > uint64(modelCalls) || modelFinished[e.CallID] || e.Stage != modelStages[e.CallID] {
				return invalid()
			}
			if e.Stage == "budget_triage" {
				var observed budgettriage.Observation
				decoder := json.NewDecoder(bytes.NewReader(e.Data))
				decoder.DisallowUnknownFields()
				if e.CostMicros == nil || len(e.Data) == 0 || len(e.Data) > 4096 || decoder.Decode(&observed) != nil || decoder.Decode(new(any)) != io.EOF ||
					!observed.Valid() || observed.RequestedModel != e.Name || observed.ChargedMicros != *e.CostMicros ||
					observed.Coverage == "complete" && (e.Usage == nil || e.Usage.InputTokens != observed.InputTokens || e.Usage.OutputTokens != observed.OutputTokens) ||
					observed.Coverage == "unavailable" && e.Usage != nil {
					return invalid()
				}
				triageFinished = true
				triageObservation = observed
			}
			modelFinished[e.CallID] = true
			modelFailed[e.CallID] = e.Error == "model_request_failed"
			if e.CostMicros != nil {
				chargedCost += *e.CostMicros - modelCosts[e.CallID]
			}
			if u := e.Usage; u != nil && (u.Requests != 0 || u.Reported != 1 || u.Coverage != "" ||
				u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 || u.ReasoningTokens < 0 ||
				u.InputTokens > 1_000_000_000_000 || u.OutputTokens > 1_000_000_000_000 || u.TotalTokens > 1_000_000_000_000 ||
				u.CacheReadTokens > 1_000_000_000_000 || u.CacheWriteTokens > 1_000_000_000_000 || u.ReasoningTokens > 1_000_000_000_000) {
				return invalid()
			}
			if e.Usage != nil {
				observedUsage.AddReported(*e.Usage)
			}
		case "executor.step.failed":
			if e.CallID == 0 || e.CallID != uint64(modelCalls) || !modelFinished[e.CallID] || executorErrors[e.CallID] || e.Error != "actor_code_error" {
				return invalid()
			}
			executorErrors[e.CallID] = true
		case "model.route.fallback":
			if e.CallID == 0 || e.CallID != uint64(modelCalls) || !modelFinished[e.CallID] || !modelFailed[e.CallID] || fallbacks[e.CallID] ||
				e.Name == "" || e.Name != modelProviders[e.CallID] || e.Origin == "" || e.Origin == e.Name || e.Error != "transient_provider_failure" {
				return invalid()
			}
			fallbacks[e.CallID] = true
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
		case "budget.triage.skipped":
			if spec.TriageSummary == "" || triageFinished || triageSkipped ||
				triageStarted && (attempt == 1 || e.Name != "interrupted") ||
				!triageStarted && e.Name != "call_budget" && e.Name != "token_budget" && e.Name != "cost_budget" {
				return invalid()
			}
			triageSkipped = true
		case "budget.profile.proposed":
			var proposal budgettriage.Proposal
			decoder := json.NewDecoder(bytes.NewReader(e.Data))
			decoder.DisallowUnknownFields()
			hard := budgettriage.Limits{MaxModelCalls: spec.ModelCallLimit(), MaxModelTokens: spec.MaxModelTokens, MaxCostMicros: spec.MaxCostMicros}
			if !triageFinished || triageSkipped || triageProposed || len(e.Data) == 0 || len(e.Data) > 1024 ||
				decoder.Decode(&proposal) != nil || decoder.Decode(new(any)) != io.EOF || !proposal.Valid(hard) ||
				proposal.Profile != triageObservation.SuggestedProfile || e.Name != proposal.Profile {
				return invalid()
			}
			triageProposed = true
			shadowProfile = &proposal
		case "budget.profile.extended":
			var extension budgettriage.Extension
			decoder := json.NewDecoder(bytes.NewReader(e.Data))
			decoder.DisallowUnknownFields()
			hard := budgettriage.Limits{MaxModelCalls: spec.ModelCallLimit(), MaxModelTokens: spec.MaxModelTokens, MaxCostMicros: spec.MaxCostMicros}
			if !triageProposed || shadowProfile == nil || e.Name == "" ||
				len(e.Data) == 0 || len(e.Data) > 1024 || decoder.Decode(&extension) != nil || decoder.Decode(new(any)) != io.EOF ||
				e.OperationID != extension.OperationID || e.CallID != extension.CallID ||
				e.Name != extension.To || !extension.Valid(*shadowProfile, hard) {
				return invalid()
			}
			if e.OperationID > 0 {
				if e.OperationID <= lastShadowExtensionOp || !ended[e.OperationID] || e.OperationID > uint64(len(s.Operations)) {
					return invalid()
				}
				op := s.Operations[e.OperationID-1]
				var resultStatus struct {
					IsError bool `json:"is_error"`
				}
				if op.Error != "" || len(op.Result) == 0 || json.Unmarshal(op.Result, &resultStatus) != nil || resultStatus.IsError {
					return invalid()
				}
				lastShadowExtensionOp = e.OperationID
			} else if e.CallID != uint64(modelCalls) || !modelFinished[e.CallID] || modelFailed[e.CallID] ||
				modelStages[e.CallID] == "budget_triage" || !lookupProposals[e.CallID] {
				return invalid()
			}
			shadowProfile = &budgettriage.Proposal{Version: extension.Version, Profile: extension.To, Limits: extension.Limits}
		case "run.resumed":
			attempt++
			if attempt > 3 || e.Attempt != attempt || len(started) != len(ended) {
				return invalid()
			}
			terminalCheck = nil
			finalizerEvent = false
			finalizerAdmitted = false
			finalizerCalls = 0
			finalizerEvidenceIDs = nil
			lookupProposals = map[uint64]bool{}
		case "tool.reused":
			if !ended[e.OperationID] || e.Name != started[e.OperationID] {
				return invalid()
			}
		case "tool.proposed":
			if !triageProposed || e.Name != "lookup" || e.CallID == 0 || e.CallID != uint64(modelCalls) ||
				e.OperationID != 0 || !modelFinished[e.CallID] || modelFailed[e.CallID] || modelStages[e.CallID] == "budget_triage" ||
				len(e.Data) != 0 {
				return invalid()
			}
			lookupProposals[e.CallID] = true
		case "tool.started":
			if e.OperationID != uint64(len(started)+1) || e.OperationID > uint64(spec.OperationLimit()) || !agent.ValidToolName(e.Name) {
				return invalid()
			}
			started[e.OperationID] = e.Name
			if e.CostMicros != nil {
				toolCosts[e.OperationID] = *e.CostMicros
				chargedCost += *e.CostMicros
			}
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
			if e.CostMicros != nil {
				chargedCost += *e.CostMicros - toolCosts[e.OperationID]
			}
			if len(e.Data) > 0 {
				if e.OperationID > uint64(len(s.Operations)) || !sameJSON(e.Data, s.Operations[e.OperationID-1].Result) {
					return invalid()
				}
			}
		case "runtime.state.updated":
			if !ended[e.OperationID] || stateUpdates[e.OperationID] || e.StateUpdate == nil || !e.StateUpdate.Valid() {
				return invalid()
			}
			stateUpdates[e.OperationID] = true
		case "runtime.state.applied":
			if !stateUpdates[e.OperationID] || stateApplied[e.OperationID] ||
				(e.Origin != "next-response" && e.Origin != "native") {
				return invalid()
			}
			stateApplied[e.OperationID] = true
		case "runtime.state.superseded":
			if !finalizerAdmitted || !stateUpdates[e.OperationID] || stateApplied[e.OperationID] ||
				e.Origin != "terminal_finalizer" {
				return invalid()
			}
			admitted := false
			for _, id := range finalizerEvidenceIDs {
				if uint64(id) == e.OperationID {
					admitted = true
					break
				}
			}
			if !admitted {
				return invalid()
			}
			stateApplied[e.OperationID] = true
		case "runtime.state.failed":
			if !ended[e.OperationID] || stateFailures[e.OperationID] || e.Error != "runtime_state_failed" {
				return invalid()
			}
			stateFailures[e.OperationID] = true
		case "finalizer.admitted":
			if finalizerEvent || e.Terminal == nil || !e.Terminal.Accepted || len(e.Terminal.EvidenceIDs) == 0 ||
				!e.Terminal.Valid(s.Operations) || len(started) != len(ended) || e.Origin == "" || len(e.Origin) > 128 || e.Error != "" {
				return invalid()
			}
			finalizerEvent = true
			finalizerAdmitted = true
			finalizerEvidenceIDs = append([]int(nil), e.Terminal.EvidenceIDs...)
		case "finalizer.denied":
			if finalizerEvent || e.Terminal != nil || e.Origin == "" || len(e.Origin) > 128 ||
				e.Error != "insufficient_evidence" && e.Error != "evidence_unavailable" && e.Error != "evidence_too_large" {
				return invalid()
			}
			finalizerEvent = true
		case "terminal.checked":
			if terminalCheck != nil || e.Terminal == nil || e.Origin == "" || len(e.Origin) > 128 ||
				len(started) != len(ended) || !e.Terminal.Valid(s.Operations) ||
				e.Terminal.Accepted && e.Error != "" ||
				!e.Terminal.Accepted && e.Error != "verification_failed" && e.Error != "verification_unavailable" && e.Error != "invalid_verification" {
				return invalid()
			}
			copy := e
			terminalCheck = &copy
		case "run.finished":
			if i != len(s.Events)-1 || s.Result == nil || e.Result == nil || !sameJSON(e.Result, s.Result) {
				return invalid()
			}
			if finalizerEvent && e.Result.Status == "completed" && (!finalizerAdmitted || finalizerCalls != 1) {
				return invalid()
			}
			if terminalCheck != nil && (terminalCheck.Terminal.Accepted != (e.Result.Status == "completed") ||
				!terminalCheck.Terminal.Accepted && (e.Result.Code != terminalCheck.Error || e.Result.Kind != "failure")) {
				return invalid()
			}
		default:
			return invalid()
		}
	}
	if len(s.Operations) > len(started) {
		return invalid()
	}
	if s.Result != nil && spec.TriageSummary != "" && !triageSkipped && (!triageFinished || !triageProposed) {
		return invalid()
	}
	if s.Result != nil {
		if (spec.MaxCostMicros > 0) != (s.Result.Cost != nil) ||
			s.Result.Cost != nil && (s.Result.Cost.PricingVersion == "" || s.Result.Cost.BudgetMicros != spec.MaxCostMicros || s.Result.Cost.ChargedMicros != chargedCost) {
			return invalid()
		}
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
	case "distiller", "executor", "responder", "child.distiller", "child.executor", "child.responder", "finalizer", "unattributed":
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
