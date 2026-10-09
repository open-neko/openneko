package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
	"github.com/open-neko/openneko/apps/ax-harness/internal/budgettriage"
	"github.com/open-neko/openneko/apps/ax-harness/internal/session"
)

func TestMeasuredRunSeparatesCacheTriageAndGraphJinUsage(t *testing.T) {
	hash := strings.Repeat("a", 64)
	reserveTriage, chargedTriage := int64(4), int64(6)
	reserveOuter, chargedOuter := int64(20), int64(25)
	reserveRemote, chargedRemote := int64(30), int64(40)
	trace := session.BudgetTrace{RunID: "run", CheckpointSHA256: hash, Mode: "fixed", Status: "completed",
		Events: []agent.Event{
			{Type: "model.request.started", CallID: 1, Stage: "budget_triage", CostMicros: &reserveTriage},
			{Type: "model.request.finished", CallID: 1, Stage: "budget_triage", DurationMS: 7,
				Usage: &agent.ModelUsage{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 3}, CostMicros: &chargedTriage},
			{Type: "model.request.started", CallID: 2, Stage: "executor", CostMicros: &reserveOuter},
			{Type: "model.request.finished", CallID: 2, Stage: "executor",
				Usage: &agent.ModelUsage{InputTokens: 20, OutputTokens: 5, CacheWriteTokens: 4, ReasoningTokens: 2}, CostMicros: &chargedOuter},
			{Type: "tool.started", OperationID: 1, Name: "lookup", CostMicros: &reserveRemote},
			{Type: "tool.finished", OperationID: 1, Name: "lookup", ResultBytes: 10_000,
				RemoteUsage: &agent.RemoteUsage{PromptTokens: 100, CompletionTokens: 15, Reported: true}, CostMicros: &chargedRemote},
			{Type: "observation.retrieved", OperationID: 1,
				ObservationRead: &agent.ObservationReadProfile{InstructionBytes: 12, ResultBytes: 10_000}},
		}}
	r, _, err := measuredRun(runCase{RunID: "run", CheckpointSHA256: hash, Outcome: "verified_success"}, "fixed",
		func(_, _ string) (session.BudgetTrace, error) { return trace, nil })
	if err != nil || r.ModelCalls != 2 || r.ModelInputTokens != 30 || r.ModelOutputTokens != 7 ||
		r.CacheReadTokens != 3 || r.CacheWriteTokens != 4 || r.ReasoningTokens != 2 ||
		r.GraphJinCalls != 1 || r.GraphJinPromptTokens != 100 || r.GraphJinOutputTokens != 15 ||
		r.TriageCalls != 1 || r.TriageCostMicros != 6 || r.TriageDurationMS != 7 ||
		r.ToolResultBytes != 10_000 || r.ReferencedResultBytes != 10_000 ||
		r.ObservationReadCalls != 1 || r.ObservationReadBytes != 10_012 ||
		r.ChargedMicros != 71 || r.CostCoverage != "complete" || r.UsageCoverage != "complete" {
		t.Fatalf("usage breakdown = %+v, err = %v", r, err)
	}
	if len(r.ModelRequests) != 2 || r.ModelRequests[0].CallID != 1 ||
		r.ModelRequests[0].Stage != "budget_triage" || r.ModelRequests[0].InputTokens != 10 ||
		r.ModelRequests[0].CacheReadTokens != 3 || !r.ModelRequests[0].UsageReported ||
		r.ModelRequests[1].CallID != 2 || r.ModelRequests[1].InputTokens != 20 ||
		r.ModelRequests[1].CacheWriteTokens != 4 || r.ModelRequests[1].ReasoningTokens != 2 {
		t.Fatalf("model request profiles = %+v", r.ModelRequests)
	}
}

func TestMeasuredRunMarksMissingPerCallUsage(t *testing.T) {
	hash := strings.Repeat("a", 64)
	trace := session.BudgetTrace{RunID: "run", CheckpointSHA256: hash, Mode: "fixed", Status: "completed",
		Events: []agent.Event{
			{Type: "model.request.started", CallID: 1},
			{Type: "model.request.finished", CallID: 1},
		}}
	r, _, err := measuredRun(runCase{RunID: "run", CheckpointSHA256: hash, Outcome: "verified_success"}, "fixed",
		func(_, _ string) (session.BudgetTrace, error) { return trace, nil })
	if err != nil || r.UsageCoverage != "partial" || len(r.ModelRequests) != 1 ||
		r.ModelRequests[0].UsageReported {
		t.Fatalf("missing usage looked measured: %+v, err = %v", r, err)
	}
}

func TestPairedComparisonDetectsCheaperFailedCanary(t *testing.T) {
	a, b, c, d := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	graphjin := graphJinEnvironment{Provider: "google-gemini", Model: "approved-strong-model", Reasoning: "high",
		EvalFingerprint: strings.Repeat("d", 64), DataSnapshotSHA256: strings.Repeat("e", 64)}
	m := manifest{Version: 1, Pairs: []pair{
		{ID: "short-1", Split: "held_out", Source: "live", TaskClass: "short",
			Fixed:  runCase{Root: "/fixture", RunID: "short-fixed", CheckpointSHA256: a, Outcome: "verified_success", WallMS: 100},
			Canary: runCase{Root: "/fixture", RunID: "short-canary", CheckpointSHA256: b, Outcome: "verified_failure", WallMS: 50}},
		{ID: "artifact-1", Split: "calibration", Source: "live", TaskClass: "artifact",
			Fixed:  runCase{Root: "/fixture", RunID: "artifact-fixed", CheckpointSHA256: c, Outcome: "verified_success", WallMS: 200, GraphJin: &graphjin},
			Canary: runCase{Root: "/fixture", RunID: "artifact-canary", CheckpointSHA256: d, Outcome: "verified_success", WallMS: 220, GraphJin: &graphjin}},
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
		events := []agent.Event{{Sequence: 1, Type: "budget.profile.proposed", Data: proposal},
			{Sequence: 2, Type: "tool.catalog.configured", Name: "parent",
				ToolCatalog: &agent.ToolCatalogProfile{Count: 1, SchemaBytes: 27, DescriptorBytes: 120}}}
		if runID == "short-canary" {
			events = append(events, agent.Event{Type: "tool.input.rejected", Name: "catalog", Error: "invalid_input"})
		}
		for i := 0; i < calls; i++ {
			id := uint64(i + 1)
			events = append(events, agent.Event{Sequence: uint64(len(events) + 1), Type: "model.request.started", CallID: id, CostMicros: &price})
			events = append(events, agent.Event{Sequence: uint64(len(events) + 1), Type: "model.request.finished", CallID: id,
				Usage: &agent.ModelUsage{TotalTokens: 10}, CostMicros: &price})
		}
		if strings.HasPrefix(runID, "artifact-") {
			zero := int64(0)
			events = append(events, agent.Event{Sequence: uint64(len(events) + 1), Type: "tool.started", OperationID: 1,
				Name: "lookup", CostMicros: &zero})
			events = append(events, agent.Event{Sequence: uint64(len(events) + 1), Type: "tool.finished", OperationID: 1,
				Name: "lookup", CostMicros: &zero, RemoteUsage: &agent.RemoteUsage{Reported: true}})
		}
		return session.BudgetTrace{RunID: runID, CheckpointSHA256: hash, Mode: mode, Status: status,
			TaskFingerprint: strings.Repeat("0", 64), RoutingDigest: strings.Repeat("e", 64),
			CatalogFingerprint: strings.Repeat("1", 64), MaxOperations: 4,
			Hard: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 100000, MaxCostMicros: 100000}, Events: events}, nil
	}
	out, err := compareWithTrace(encode(), read)
	if err != nil || out.Summary.Pairs != 2 || out.HeldOut.CanaryRegressions != 1 ||
		out.HeldOut.RegressionsByTaskClass["short"] != 1 || out.Summary.FixedSuccess != 2 || out.Summary.CanarySuccess != 1 ||
		out.Summary.FixedCostMicros != 40 || out.Summary.CanaryCostMicros != 30 ||
		out.Summary.FixedCostPerSuccess == nil || *out.Summary.FixedCostPerSuccess != 20 ||
		out.Summary.CanaryCostPerSuccess == nil || *out.Summary.CanaryCostPerSuccess != 30 ||
		out.Pairs[0].FixedShadowFirstBlock != "model_calls" ||
		out.Pairs[0].Fixed.ParentSchemaBytes != 27 || out.Pairs[0].Fixed.ParentDescriptorBytes != 120 ||
		out.Pairs[0].Canary.InvalidToolInputs != 1 || out.Pairs[1].GraphJin == nil ||
		*out.Pairs[1].GraphJin != graphjin {
		t.Fatalf("output=%+v err=%v", out, err)
	}
	m.Pairs[0].Source = "synthetic"
	synthetic, err := compareWithTrace(encode(), read)
	if err != nil || synthetic.HeldOut.Pairs != 0 || synthetic.SyntheticHeldOut.Pairs != 1 ||
		synthetic.SyntheticHeldOut.CanaryRegressions != 1 {
		t.Fatalf("synthetic held-out runs counted as live evidence: %+v err=%v", synthetic, err)
	}
	m.Pairs[0].Source = "live"
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
	wrongTask := func(root, runID string) (session.BudgetTrace, error) {
		trace, err := read(root, runID)
		if trace.Mode == "canary" {
			trace.TaskFingerprint = strings.Repeat("9", 64)
		}
		return trace, err
	}
	if _, err := compareWithTrace(encode(), wrongTask); err == nil {
		t.Fatal("accepted different model task input")
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
	m.Pairs[1].Canary.GraphJin = nil
	if _, err := compareWithTrace(encode(), read); err == nil {
		t.Fatal("accepted live GraphJin pair without server and data attestation")
	}
	missingFingerprint := graphjin
	missingFingerprint.EvalFingerprint = ""
	m.Pairs[1].Canary.GraphJin = &missingFingerprint
	if _, err := compareWithTrace(encode(), read); err == nil {
		t.Fatal("accepted live GraphJin pair without server evaluation fingerprint")
	}
	changedGraphJin := graphjin
	changedGraphJin.Reasoning = "low"
	m.Pairs[1].Canary.GraphJin = &changedGraphJin
	if _, err := compareWithTrace(encode(), read); err == nil {
		t.Fatal("accepted live GraphJin pair with different server reasoning")
	}
	changedGraphJin = graphjin
	changedGraphJin.EvalFingerprint = strings.Repeat("c", 64)
	m.Pairs[1].Canary.GraphJin = &changedGraphJin
	if _, err := compareWithTrace(encode(), read); err == nil {
		t.Fatal("accepted live GraphJin pair with different server evaluation fingerprint")
	}
	changedGraphJin = graphjin
	changedGraphJin.DataSnapshotSHA256 = strings.Repeat("f", 64)
	m.Pairs[1].Canary.GraphJin = &changedGraphJin
	if _, err := compareWithTrace(encode(), read); err == nil {
		t.Fatal("accepted live GraphJin pair with different data snapshot")
	}
	m.Pairs[1].Canary.GraphJin = &graphjin
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
