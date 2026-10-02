package session

import (
	"testing"

	"github.com/open-neko/harness/internal/agent"
)

func TestTaskFingerprintIgnoresRunIdentityButBindsAcceptedInput(t *testing.T) {
	base := agent.Spec{Version: 1, RunID: "fixed", InputID: "fixed-input", Prompt: "Read REF-42",
		SkillQuery: "reference check", TriageSummary: "Verify a reference", TriageToolFamilies: "graphjin",
		MaxModelCalls: 16, MaxModelTokens: 100000, MaxCostMicros: 1000000}
	paired := base
	paired.RunID, paired.InputID, paired.HostBudgetMode = "canary", "canary-input", "canary"
	if taskFingerprint(base) != taskFingerprint(paired) {
		t.Fatal("run identity or budget mode changed task fingerprint")
	}
	changed := paired
	changed.Prompt = "Read REF-43"
	if taskFingerprint(base) == taskFingerprint(changed) {
		t.Fatal("changed accepted prompt reused task fingerprint")
	}
	changed = paired
	changed.TriageArtifactRequested = true
	if taskFingerprint(base) == taskFingerprint(changed) {
		t.Fatal("changed classifier signal reused task fingerprint")
	}
}
