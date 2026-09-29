package main

import (
	"context"
	"encoding/json"

	"github.com/open-neko/harness/internal/agent"
)

// A queued workflow's final answer is successful only after the host broker
// has committed at least one output. The saved broker receipt is the evidence;
// model text cannot stand in for workflow_output_emit.
func bindWorkflowOutputVerification(tools *agent.Tools, binding string) {
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
		var receipt struct {
			OK       bool   `json:"ok"`
			IsError  bool   `json:"is_error"`
			OutputID string `json:"outputId"`
			Kind     string `json:"kind"`
		}
		if json.Unmarshal(op.Result, &receipt) != nil || !receipt.OK || receipt.IsError || receipt.OutputID == "" || receipt.Kind == "" {
			return agent.TerminalDecision{Accepted: false}
		}
		ids = append(ids, op.ID)
	}
	return agent.TerminalDecision{Accepted: len(ids) > 0, EvidenceIDs: ids}
}
