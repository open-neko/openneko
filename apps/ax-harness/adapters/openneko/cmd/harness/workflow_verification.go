package main

import (
	"context"
	"encoding/json"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
)

// A queued workflow's final answer is successful only after the host broker
// has committed at least one output. The saved broker receipt is the evidence;
// model text cannot stand in for workflow_output_emit.
func bindWorkflowOutputVerification(tools *agent.Tools, binding string) {
	tools.StateHookVersion = "openneko-workflow-output-state-v1"
	tools.AfterTool = func(_ context.Context, op agent.SavedOperation) (*agent.RuntimeStateUpdate, error) {
		if op.Name() != "workflow_output_emit" || op.Binding != binding || !op.Finished || op.Error != "" {
			return nil, nil
		}
		receipt, ok := confirmedWorkflowOutput(op.Result)
		if !ok {
			return nil, nil
		}
		state, err := json.Marshal(struct {
			OperationID int    `json:"operation_id"`
			OutputID    string `json:"output_id"`
			Kind        string `json:"kind"`
		}{OperationID: op.ID, OutputID: receipt.OutputID, Kind: receipt.Kind})
		if err != nil {
			return nil, err
		}
		return &agent.RuntimeStateUpdate{Target: "root/responder", State: state}, nil
	}
	tools.TerminalGateVersion = "openneko-workflow-output-v1"
	tools.TerminalGate = func(_ context.Context, _ agent.Result, operations []agent.SavedOperation) (agent.TerminalDecision, error) {
		return workflowOutputEvidence(operations, binding), nil
	}
	tools.FinalizerGateVersion = "openneko-workflow-output-v1"
	tools.FinalizerGate = func(_ context.Context, operations []agent.SavedOperation) (agent.TerminalDecision, error) {
		return workflowOutputEvidence(operations, binding), nil
	}
}

func workflowOutputEvidence(operations []agent.SavedOperation, binding string) agent.TerminalDecision {
	var ids []int
	for _, op := range operations {
		if op.Name() != "workflow_output_emit" {
			continue
		}
		if !op.Finished || op.Error != "" || op.Binding != binding {
			return agent.TerminalDecision{Accepted: false}
		}
		if _, ok := confirmedWorkflowOutput(op.Result); !ok {
			return agent.TerminalDecision{Accepted: false}
		}
		ids = append(ids, op.ID)
	}
	return agent.TerminalDecision{Accepted: len(ids) > 0, EvidenceIDs: ids}
}

type workflowOutputReceipt struct {
	OK       bool   `json:"ok"`
	IsError  bool   `json:"is_error"`
	OutputID string `json:"outputId"`
	Kind     string `json:"kind"`
}

func confirmedWorkflowOutput(raw json.RawMessage) (workflowOutputReceipt, bool) {
	var receipt workflowOutputReceipt
	if json.Unmarshal(raw, &receipt) != nil || !receipt.OK || receipt.IsError || receipt.OutputID == "" || receipt.Kind == "" {
		return workflowOutputReceipt{}, false
	}
	return receipt, true
}
