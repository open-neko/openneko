package webfront

import (
	"strconv"
	"strings"
	"time"
)

// workflowPromptInput holds the arguments of buildWorkflowRunnerPrompt() in
// packages/llm/src/workflows/runner-prompt.ts. Steps is the stored steps
// array, parsed with jsParse.
type workflowPromptInput struct {
	Backend             string         `json:"backend"`
	Name                string         `json:"name"`
	Description         string         `json:"description"`
	Goal                string         `json:"goal"`
	SystemPromptOverlay string         `json:"systemPromptOverlay"`
	Steps               []any          `json:"-"`
	Mode                string         `json:"mode"`
	IncludeUxMetadata   bool           `json:"includeUxMetadata"`
	MemoryContext       string         `json:"memoryContext"`
	Workspace           agentWorkspace `json:"workspace"`
	Knowledge           knowledgePack  `json:"knowledge"`
	FilesEnabled        bool           `json:"filesEnabled"`
}

func jsProp(v any, key string) any {
	if obj, ok := v.(*jsObj); ok && obj != nil {
		if val, ok := obj.vals[key]; ok {
			return val
		}
	}
	return jsUndefined{}
}

func buildWorkflowPrompt(in workflowPromptInput) string {
	lines := make([]string, len(in.Steps))
	for i, step := range in.Steps {
		lines[i] = "  " + strconv.Itoa(i+1) + ". " + jsTemplate(jsProp(step, "description"))
		if jsTruthy(jsGet(step, "script")) {
			lines[i] += " (script step: already ran; see <step_results>)"
		}
	}
	steps := strings.Join(lines, "\n")
	if steps == "" {
		steps = "  (no steps defined)"
	}
	author := ""
	if overlay := jsTrim(in.SystemPromptOverlay); overlay != "" {
		author = section("workflow_author", "overlay", overlay) + "\n\n"
	}
	goal := ""
	if g := jsTrim(in.Goal); g != "" {
		goal = "<goal>" + g + "</goal>\n"
	}
	value := ""
	if in.IncludeUxMetadata {
		value = " Then emit one time-saved block.\n\n" + section("value_estimate")
	}
	return section("workflow",
		"name", in.Name,
		"description", pick(in.Description != "", in.Description, "(none provided)"),
		"goal", goal,
		"author", author,
		"steps", steps,
		"data_access", dataAccessSection(in.Knowledge, true),
		"date_rule", section("date_rule"),
		"outputs", section("workflow_outputs"),
		"actions", section("workflow_actions"),
		"delegation", pick(in.Backend == "ax", section("ax_delegation"), ""),
		"value", value,
		"mode", pick(in.Mode == "headless", section("workflow_headless"), section("workflow_live")),
		"artifactRoot", in.Workspace.ArtifactRoot,
		"memory", memorySection(true, in.FilesEnabled, in.MemoryContext))
}

const triggerInputMaxChars = 16_000

// workflowSeedMessage is buildWorkflowSeedMessage(). payload is the run's
// trigger payload as jsParse gives it.
func workflowSeedMessage(name, triggerKind, userMessage string, payload any, now time.Time) string {
	if jsTrim(userMessage) != "" {
		return userMessage
	}
	stamp := jsTime(now)
	var start string
	switch triggerKind {
	case "cron":
		start = "[scheduled run started at " + stamp + "] Begin executing the \"" + name + "\" workflow."
	case "subscription":
		start = "[subscription-triggered run started at " + stamp + "] Begin executing the \"" + name + "\" workflow."
	case "api":
		start = "Begin executing the externally admitted \"" + name + "\" workflow with the supplied API input."
	default:
		start = "Begin executing the \"" + name + "\" workflow."
	}
	switch p := payload.(type) {
	case *jsObj:
		if p == nil || len(p.keys) == 0 {
			return start
		}
	case []any:
		if len(p) == 0 {
			return start
		}
	case string:
		if p == "" {
			return start
		}
	default:
		return start
	}
	text := jsStringifyIndent(payload, "  ")
	if utf16Len(text) > triggerInputMaxChars {
		text = jsSlice(text, triggerInputMaxChars) + "\n... (truncated)"
	}
	return start + "\n\nThis run's input is the event that triggered it. Do the work for this input.\n\n<trigger_input>\n" +
		text + "\n</trigger_input>"
}
