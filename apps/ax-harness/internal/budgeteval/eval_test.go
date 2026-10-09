package budgeteval_test

import (
	"encoding/json"
	"testing"

	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/budgeteval"
	"github.com/open-neko/harness/internal/budgettriage"
	"github.com/open-neko/harness/internal/session"
)

func cost(n int64) *int64 { return &n }

func fixtureTrace(t *testing.T, proposed budgettriage.Proposal, withLookup, extend bool) session.BudgetTrace {
	t.Helper()
	hard := budgettriage.Limits{MaxModelCalls: 12, MaxModelTokens: 100_000, MaxCostMicros: 100_000}
	proposalData, err := json.Marshal(proposed)
	if err != nil {
		t.Fatal(err)
	}
	trace := session.BudgetTrace{RunID: "fixture", Hard: hard, Status: "completed", Events: []agent.Event{
		{Sequence: 1, Type: "model.request.started", CallID: 1, Stage: "budget_triage", CostMicros: cost(512)},
		{Sequence: 2, Type: "model.request.finished", CallID: 1, Stage: "budget_triage", DurationMS: 25,
			Usage: &agent.ModelUsage{Reported: 1, TotalTokens: 15}, CostMicros: cost(15)},
		{Sequence: 3, Type: "budget.profile.proposed", Data: proposalData},
		{Sequence: 4, Type: "model.request.started", CallID: 2, CostMicros: cost(100)},
		{Sequence: 5, Type: "model.request.finished", CallID: 2, Usage: &agent.ModelUsage{Reported: 1, TotalTokens: 15}, CostMicros: cost(15)},
	}}
	if withLookup {
		trace.Events = append(trace.Events,
			agent.Event{Sequence: 6, Type: "tool.started", Name: "lookup", OperationID: 1, CostMicros: cost(500)},
			agent.Event{Sequence: 7, Type: "tool.finished", Name: "lookup", OperationID: 1,
				RemoteUsage: &agent.RemoteUsage{Reported: true, ChargedTokens: 100}, CostMicros: cost(100)})
	}
	if extend {
		from := proposed.Profile
		to := "artifact"
		if from == "short" {
			to = "multi_step"
		}
		extension := budgettriage.Extension{Version: proposed.Version, From: from, To: to, OperationID: 1,
			Limits: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 80_000, MaxCostMicros: 80_000}, Reason: "next_call_exceeds_profile"}
		data, err := json.Marshal(extension)
		if err != nil {
			t.Fatal(err)
		}
		trace.Events = append(trace.Events, agent.Event{Sequence: uint64(len(trace.Events) + 1), Type: "budget.profile.extended", OperationID: 1, Data: data})
	}
	return trace
}

func TestHeldOutArtifactDetectsRemotePreflightFailureBeforeExtension(t *testing.T) {
	proposal := budgettriage.Proposal{Version: "fixture-v1", Profile: "multi_step",
		Limits: budgettriage.Limits{MaxModelCalls: 4, MaxModelTokens: 40_000, MaxCostMicros: 20_000}}
	trace := fixtureTrace(t, proposal, true, true)
	report, err := budgeteval.Evaluate(trace, budgeteval.Label{TaskClass: "artifact", Outcome: "verified_success", WallMS: 900})
	if err != nil || !report.ClassFalseLow || !report.PrematureBudgetFailure ||
		report.FirstBlock == nil || report.FirstBlock.Kind != "remote_tokens" || report.FirstBlock.Sequence != 6 ||
		report.FinalProfile != "artifact" || report.ActualChargedTokens != 130 || report.ActualCostMicros != 130 ||
		report.ClassifierCostMicros != 15 || report.ClassifierLatencyMS != 25 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestRemoteExtensionBeforeLookupAvoidsCounterfactualBlock(t *testing.T) {
	proposal := budgettriage.Proposal{Version: "fixture-v1", Profile: "multi_step",
		Limits: budgettriage.Limits{MaxModelCalls: 4, MaxModelTokens: 40_000, MaxCostMicros: 20_000}}
	trace := fixtureTrace(t, proposal, true, false)
	extension := budgettriage.Extension{Version: "fixture-v1", From: "multi_step", To: "artifact", CallID: 2,
		Limits: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 80_000, MaxCostMicros: 80_000}, Reason: "remote_lookup_preflight"}
	data, err := json.Marshal(extension)
	if err != nil {
		t.Fatal(err)
	}
	trace.Events = append(trace.Events[:5], append([]agent.Event{{Sequence: 6, Type: "budget.profile.extended", CallID: 2, Data: data}}, trace.Events[5:]...)...)
	trace.Events[6].Sequence = 7
	trace.Events[7].Sequence = 8
	report, err := budgeteval.Evaluate(trace, budgeteval.Label{TaskClass: "artifact", Outcome: "verified_success"})
	if err != nil || report.FirstBlock != nil || !report.ClassFalseLow || report.PrematureBudgetFailure {
		t.Fatalf("preflight report=%+v err=%v", report, err)
	}
}

func TestExtensionBeforeNextModelCallAvoidsFalseBudgetFailure(t *testing.T) {
	proposal := budgettriage.Proposal{Version: "fixture-v1", Profile: "multi_step",
		Limits: budgettriage.Limits{MaxModelCalls: 2, MaxModelTokens: 40_000, MaxCostMicros: 20_000}}
	trace := fixtureTrace(t, proposal, false, false)
	data, err := json.Marshal(budgettriage.Extension{Version: "fixture-v1", From: "multi_step", To: "artifact", OperationID: 1,
		Limits: budgettriage.Limits{MaxModelCalls: 8, MaxModelTokens: 80_000, MaxCostMicros: 80_000}, Reason: "next_call_exceeds_profile"})
	if err != nil {
		t.Fatal(err)
	}
	trace.Events = append(trace.Events, agent.Event{Sequence: 6, Type: "budget.profile.extended", OperationID: 1, Data: data},
		agent.Event{Sequence: 7, Type: "model.request.started", CallID: 3, CostMicros: cost(100)},
		agent.Event{Sequence: 8, Type: "model.request.finished", CallID: 3, Usage: &agent.ModelUsage{Reported: 1, TotalTokens: 15}, CostMicros: cost(15)})
	report, err := budgeteval.Evaluate(trace, budgeteval.Label{TaskClass: "investigation", Outcome: "verified_success"})
	if err != nil || report.FirstBlock != nil || report.ClassFalseLow || report.ActualModelCalls != 3 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	withoutExtension := trace
	withoutExtension.Events = append([]agent.Event(nil), trace.Events[:5]...)
	withoutExtension.Events = append(withoutExtension.Events, trace.Events[6:]...)
	report, err = budgeteval.Evaluate(withoutExtension, budgeteval.Label{TaskClass: "investigation", Outcome: "verified_success"})
	if err != nil || report.FirstBlock == nil || report.FirstBlock.Kind != "model_calls" {
		t.Fatalf("missing-extension report=%+v err=%v", report, err)
	}
}

func TestUnverifiedOutcomeCannotBeCountedAsSuccess(t *testing.T) {
	proposal := budgettriage.Proposal{Version: "fixture-v1", Profile: "short",
		Limits: budgettriage.Limits{MaxModelCalls: 4, MaxModelTokens: 8_000, MaxCostMicros: 10_000}}
	trace := fixtureTrace(t, proposal, true, false)
	report, err := budgeteval.Evaluate(trace, budgeteval.Label{TaskClass: "artifact", Outcome: "unverified"})
	if err != nil || report.ClassFalseLow || report.PrematureBudgetFailure || report.FirstBlock == nil {
		t.Fatalf("unverified report=%+v err=%v", report, err)
	}
	trace.Status = "failed"
	if _, err := budgeteval.Evaluate(trace, budgeteval.Label{TaskClass: "artifact", Outcome: "verified_success"}); err == nil {
		t.Fatal("failed run accepted as independently verified success")
	}
}

func TestCanaryTraceCannotMasqueradeAsFixedControl(t *testing.T) {
	proposal := budgettriage.Proposal{Version: "fixture-v1", Profile: "short",
		Limits: budgettriage.Limits{MaxModelCalls: 4, MaxModelTokens: 8_000, MaxCostMicros: 10_000}}
	trace := fixtureTrace(t, proposal, false, false)
	trace.Mode = "canary"
	if _, err := budgeteval.Evaluate(trace, budgeteval.Label{TaskClass: "short", Outcome: "verified_success"}); err == nil {
		t.Fatal("canary trace used as a fixed-budget counterfactual")
	}
}
