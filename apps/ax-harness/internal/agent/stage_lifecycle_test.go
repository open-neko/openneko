package agent

import (
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
)

func TestStageLifecyclePrefersNestedChildAndRefusesAmbiguity(t *testing.T) {
	r := &recorder{modelStages: map[string]string{"shared": "executor"}}
	event := func(kind, path string) {
		r.observeStageLifecycle(map[string]ax.Value{"type": kind, "path": path})
	}
	if stage, observed := r.activeModelStage(); observed || stage != "" {
		t.Fatalf("initial stage = %q, observed=%v", stage, observed)
	}
	event("started", "root/executor")
	if stage, observed := r.activeModelStage(); !observed || stage != "executor" {
		t.Fatalf("parent stage = %q, observed=%v", stage, observed)
	}
	event("started", "root/team.researcher/distiller")
	if stage, observed := r.activeModelStage(); !observed || stage != "child.distiller" {
		t.Fatalf("child stage = %q, observed=%v", stage, observed)
	}
	event("started", "root/team.researcher/executor")
	if stage, observed := r.activeModelStage(); !observed || stage != "" {
		t.Fatalf("overlapping child stages were attributed: %q, observed=%v", stage, observed)
	}
	event("completed", "root/team.researcher/distiller")
	event("failed", "root/team.researcher/executor")
	if stage, observed := r.activeModelStage(); !observed || stage != "executor" {
		t.Fatalf("restored parent stage = %q, observed=%v", stage, observed)
	}
	event("completed", "root/executor")
	if stage, observed := r.activeModelStage(); observed || stage != "" {
		t.Fatalf("terminal stage lingered: %q, observed=%v", stage, observed)
	}
	if stageFromPath("root/team.unknown/executor") != "" || stageFromPath("root/other") != "" {
		t.Fatal("unknown Ax path accepted")
	}
}
