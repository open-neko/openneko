package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/budgettriage"
	"github.com/open-neko/harness/internal/session"
)

func TestPairedComparisonDetectsCheaperFailedCanary(t *testing.T) {
	a, b, c, d := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	m := manifest{Version: 1, Pairs: []pair{
		{ID: "short-1", Split: "held_out", Source: "live", TaskClass: "short",
			Fixed:  runCase{Root: "/fixture", RunID: "short-fixed", CheckpointSHA256: a, Outcome: "verified_success", WallMS: 100},
			Canary: runCase{Root: "/fixture", RunID: "short-canary", CheckpointSHA256: b, Outcome: "verified_failure", WallMS: 50}},
		{ID: "artifact-1", Split: "calibration", Source: "synthetic", TaskClass: "artifact",
			Fixed:  runCase{Root: "/fixture", RunID: "artifact-fixed", CheckpointSHA256: c, Outcome: "verified_success", WallMS: 200},
			Canary: runCase{Root: "/fixture", RunID: "artifact-canary", CheckpointSHA256: d, Outcome: "verified_success", WallMS: 220}},
	}}
	encode := func() []byte {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	read := func(_, runID string) (session.BudgetTrace, error) {
		mode, status, calls, hash := "fixed", "completed", 2, a
		switch runID {
		case "short-canary":
			mode, status, calls, hash = "canary", "failed", 1, b
		case "artifact-fixed":
			hash = c
		case "artifact-canary":
			mode, hash = "canary", d
		}
		price := int64(10)
		proposal, _ := json.Marshal(budgettriage.Proposal{Version: "fixture-v1", Profile: "short",
			Limits: budgettriage.Limits{MaxModelCalls: 1, MaxModelTokens: 50000, MaxCostMicros: 1000}})
		events := []agent.Event{{Sequence: 1, Type: "budget.profile.proposed", Data: proposal}}
		for i := 0; i < calls; i++ {
			id := uint64(i + 1)
			events = append(events, agent.Event{Sequence: uint64(len(events) + 1), Type: "model.request.started", CallID: id, CostMicros: &price})
			events = append(events, agent.Event{Sequence: uint64(len(events) + 1), Type: "model.request.finished", CallID: id,
				Usage: &agent.ModelUsage{TotalTokens: 10}, CostMicros: &price})
		}
		return session.BudgetTrace{RunID: runID, CheckpointSHA256: hash, Mode: mode, Status: status,
			RoutingDigest: strings.Repeat("e", 64), CatalogFingerprint: strings.Repeat("1", 64), MaxOperations: 4,
			Hard: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 100000, MaxCostMicros: 100000}, Events: events}, nil
	}
	out, err := compareWithTrace(encode(), read)
	if err != nil || out.Summary.Pairs != 2 || out.HeldOut.CanaryRegressions != 1 ||
		out.HeldOut.RegressionsByTaskClass["short"] != 1 || out.Summary.FixedSuccess != 2 || out.Summary.CanarySuccess != 1 ||
		out.Summary.FixedCostMicros != 40 || out.Summary.CanaryCostMicros != 30 ||
		out.Summary.FixedCostPerSuccess == nil || *out.Summary.FixedCostPerSuccess != 20 ||
		out.Summary.CanaryCostPerSuccess == nil || *out.Summary.CanaryCostPerSuccess != 30 ||
		out.Pairs[0].FixedShadowFirstBlock != "model_calls" {
		t.Fatalf("output=%+v err=%v", out, err)
	}
	m.Pairs[0].Canary.CheckpointSHA256 = a
	if _, err := compareWithTrace(encode(), read); err == nil {
		t.Fatal("accepted relabelled checkpoint")
	}
	m.Pairs[0].Canary.CheckpointSHA256 = b
	m.Pairs[1].Canary.RunID = "short-fixed"
	if _, err := compareWithTrace(encode(), read); err == nil {
		t.Fatal("accepted reused run")
	}
	wrongMode := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		trace.Mode = "fixed"
		return trace, err
	}
	m.Pairs[1].Canary.RunID = "artifact-canary"
	if _, err := compareWithTrace(encode(), wrongMode); err == nil {
		t.Fatal("accepted unpaired fixed mode")
	}
	wrongRoute := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		if trace.Mode == "canary" {
			trace.RoutingDigest = strings.Repeat("f", 64)
		}
		return trace, err
	}
	if _, err := compareWithTrace(encode(), wrongRoute); err == nil {
		t.Fatal("accepted a changed model route or price profile")
	}
	wrongCatalog := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		if trace.Mode == "canary" {
			trace.CatalogFingerprint = strings.Repeat("2", 64)
		}
		return trace, err
	}
	if _, err := compareWithTrace(encode(), wrongCatalog); err == nil {
		t.Fatal("accepted changed admitted tools or terminal gate")
	}
	missingCatalog := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		trace.CatalogFingerprint = ""
		return trace, err
	}
	if _, err := compareWithTrace(encode(), missingCatalog); err == nil {
		t.Fatal("accepted checkpoints without comparable catalogs")
	}
	wrongHardLimit := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		if trace.Mode == "canary" {
			trace.Hard.MaxCostMicros++
		}
		return trace, err
	}
	if _, err := compareWithTrace(encode(), wrongHardLimit); err == nil {
		t.Fatal("accepted a changed hard admission limit")
	}
	wrongOperationLimit := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		if trace.Mode == "canary" {
			trace.MaxOperations++
		}
		return trace, err
	}
	if _, err := compareWithTrace(encode(), wrongOperationLimit); err == nil {
		t.Fatal("accepted a changed operation limit")
	}
	missingCanaryPrice := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		if trace.Mode == "canary" {
			trace.Events[len(trace.Events)-1].CostMicros = nil
		}
		return trace, err
	}
	partial, err := compareWithTrace(encode(), missingCanaryPrice)
	if err != nil || partial.Summary.IncompleteCostPairs != 2 || partial.Summary.CanaryCostPerSuccess != nil {
		t.Fatalf("missing canary price looked like a saving: %+v %v", partial.Summary, err)
	}
}
