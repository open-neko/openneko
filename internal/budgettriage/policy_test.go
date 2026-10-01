package budgettriage_test

import (
	"testing"

	"github.com/open-neko/harness/internal/budgettriage"
)

func testBudgetPolicy() budgettriage.Policy {
	return budgettriage.Policy{Version: "operator-2026-10", Short: budgettriage.Limits{MaxModelCalls: 5, MaxModelTokens: 8_000, MaxCostMicros: 2_000},
		MultiStep: budgettriage.Limits{MaxModelCalls: 16, MaxModelTokens: 40_000, MaxCostMicros: 20_000},
		Artifact:  budgettriage.Limits{MaxModelCalls: 48, MaxModelTokens: 100_000, MaxCostMicros: 100_000}}
}

func TestHostBudgetPolicyProducesOnlyCappedShadowProposals(t *testing.T) {
	policy := testBudgetPolicy()
	hard := budgettriage.Limits{MaxModelCalls: 24, MaxModelTokens: 60_000, MaxCostMicros: 30_000}
	for _, tc := range []struct {
		name string
		want budgettriage.Limits
	}{
		{"short", policy.Short},
		{"multi_step", policy.MultiStep},
		{"artifact", hard},
		{"fixed", hard},
	} {
		observed := budgettriage.Observation{Version: budgettriage.Version, RequestedModel: "jev-fixture", SuggestedProfile: tc.name,
			Reason: "classifier_unavailable", Coverage: "unavailable", ChargedMicros: 512}
		if tc.name != "fixed" {
			observed.Reason = "classified"
			observed.Coverage = "complete"
			observed.Choice = map[string]string{"short": "short_answer", "multi_step": "multi_step", "artifact": "artifact_pipeline"}[tc.name]
			observed.Probabilities = map[string]float64{"short_answer": 0, "multi_step": 0, "artifact_pipeline": 0, "uncertain": 0}
			observed.Probabilities[observed.Choice] = 1
		}
		proposal, ok := policy.Propose(observed, hard)
		if !ok || proposal.Profile != tc.name || proposal.Limits != tc.want || !proposal.Valid(hard) {
			t.Fatalf("%s: proposal=%+v ok=%t", tc.name, proposal, ok)
		}
	}
	policy.MultiStep.MaxModelCalls = 4
	if policy.Valid() {
		t.Fatal("non-monotonic policy accepted")
	}
}
