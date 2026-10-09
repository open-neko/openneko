package agent

import (
	"strings"

	ax "github.com/ax-llm/ax/packages/go"
)

// Ax's rate limiter identifies the provider and model, while run-control
// identifies a stage's lifetime. When the active path is unambiguous we can
// attach that stage to the durable request without inspecting model content.
// An overlapping sibling remains unattributed rather than being guessed from
// call order or a shared provider alias.
func (r *recorder) observeStageLifecycle(event map[string]ax.Value) {
	kind, _ := event["type"].(string)
	path, _ := event["path"].(string)
	stage := stageFromPath(path)
	if stage == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case "started":
		if r.activeStages == nil {
			r.activeStages = map[string]string{}
		}
		r.activeStages[path] = stage
	case "completed", "failed", "aborted":
		delete(r.activeStages, path)
	}
}

func stageFromPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "root" {
		return ""
	}
	stage := parts[len(parts)-1]
	if stage != "distiller" && stage != "executor" && stage != "responder" {
		return ""
	}
	if len(parts) == 2 {
		return stage
	}
	// The Harness admits one Ax child namespace, team.researcher. An unknown
	// stage tree needs its own explicit mapping before attribution is valid.
	if len(parts) == 3 && parts[1] == "team.researcher" {
		return "child." + stage
	}
	return ""
}

// Called with r.mu held. The deepest active stage owns a nested child call.
// Multiple live paths at the same depth cannot be resolved to one call.
func (r *recorder) activeModelStage() (string, bool) {
	bestDepth := -1
	bestStage := ""
	ambiguous := false
	for path, stage := range r.activeStages {
		depth := strings.Count(path, "/")
		if depth > bestDepth {
			bestDepth, bestStage, ambiguous = depth, stage, false
		} else if depth == bestDepth {
			ambiguous = true
		}
	}
	if bestDepth < 0 {
		return "", false
	}
	if ambiguous {
		return "", true
	}
	return bestStage, true
}
