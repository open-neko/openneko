package agent

import ax "github.com/ax-llm/ax/packages/go"

// StageModels is host configuration, never a field supplied by a run or a tool.
// Empty models retain Ax's existing single-client behavior.
type StageModels struct {
	Context   string
	Executor  string
	Responder string
	Skill     string
}

// RoutedClient keeps route selection with the model transport. The agent reads
// only the stage profile; capability admission remains entirely in Tools.
type RoutedClient struct {
	ax.AIClient
	Stages StageModels
}

func stageOptions(stages StageModels) map[string]ax.Value {
	options := ax.Object()
	// OpenAI-compatible routes currently advertise function-mode structured
	// output; explicit stage models otherwise make this Ax build request native
	// JSON Schema, which the provider profile rejects before transport.
	if stages.Context != "" {
		options["contextOptions"] = ax.Object("model", stages.Context, "structuredOutputMode", "function")
	}
	if stages.Executor != "" {
		options["executorOptions"] = ax.Object("model", stages.Executor, "structuredOutputMode", "function")
	}
	if stages.Responder != "" {
		options["responderOptions"] = ax.Object("model", stages.Responder, "structuredOutputMode", "function")
	}
	return options
}
