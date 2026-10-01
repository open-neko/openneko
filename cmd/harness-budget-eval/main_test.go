package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/budgeteval"
	"github.com/open-neko/harness/internal/budgettriage"
	"github.com/open-neko/harness/internal/session"
)

func TestBudgetManifestBindsHeldOutLabelsToFinalCheckpoints(t *testing.T) {
	digestA, digestB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	manifest := Manifest{Version: 1, Cases: []Case{
		{ID: "short-1", Root: "/fixture", RunID: "run-short", CheckpointSHA256: digestA,
			Split: "calibration", Source: "synthetic", Label: budgeteval.Label{TaskClass: "short", Outcome: "verified_success", WallMS: 100}},
		{ID: "artifact-1", Root: "/fixture", RunID: "run-artifact", CheckpointSHA256: digestB,
			Split: "held_out", Source: "live", Label: budgeteval.Label{TaskClass: "artifact", Outcome: "verified_success", WallMS: 200}},
	}}
	encode := func() []byte {
		t.Helper()
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	readTrace := func(_, runID string) (session.BudgetTrace, error) {
		profile, digest := "short", digestA
		if runID == "run-artifact" {
			profile, digest = "multi_step", digestB
		}
		proposal, _ := json.Marshal(budgettriage.Proposal{Version: "fixture-v1", Profile: profile,
			Limits: budgettriage.Limits{MaxModelCalls: 4, MaxModelTokens: 40_000, MaxCostMicros: 20_000}})
		charge := int64(15)
		return session.BudgetTrace{RunID: runID, CheckpointSHA256: digest, Status: "completed",
			Hard: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 100_000, MaxCostMicros: 100_000},
			Events: []agent.Event{
				{Sequence: 1, Type: "model.request.started", CallID: 1, Stage: "budget_triage", CostMicros: &charge},
				{Sequence: 2, Type: "model.request.finished", CallID: 1, Stage: "budget_triage", Usage: &agent.ModelUsage{TotalTokens: 15}, CostMicros: &charge},
				{Sequence: 3, Type: "budget.profile.proposed", Data: proposal},
				{Sequence: 4, Type: "model.request.started", CallID: 2, CostMicros: &charge},
				{Sequence: 5, Type: "model.request.finished", CallID: 2, Usage: &agent.ModelUsage{TotalTokens: 15}, CostMicros: &charge},
			}}, nil
	}
	output, err := evaluateWithTrace(encode(), readTrace)
	if err != nil || output.Summary.Cases != 2 || output.Summary.HeldOut != 1 ||
		output.Summary.VerifiedSuccess != 2 || output.Summary.ClassFalseLowByTaskClass["artifact"] != 1 ||
		output.HeldOut.Cases != 1 || output.HeldOut.ClassFalseLowByTaskClass["artifact"] != 1 ||
		output.Calibration.Cases != 1 || output.Calibration.ClassFalseLowByTaskClass["artifact"] != 0 ||
		output.Summary.FixedCostMicros != 30 || output.Summary.ShadowCostMicros != 60 ||
		output.Summary.FixedCostPerSuccess == nil || *output.Summary.FixedCostPerSuccess != 15 || output.Summary.CanaryReady {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	manifest.Cases[1].CheckpointSHA256 = digestA
	if _, err := evaluateWithTrace(encode(), readTrace); err == nil {
		t.Fatal("checkpoint changed after labelling")
	}
	manifest.Cases[1].CheckpointSHA256 = digestB
	manifest.Cases[1].RunID = "run-short"
	if _, err := evaluateWithTrace(encode(), readTrace); err == nil {
		t.Fatal("duplicate run was counted twice")
	}
}
