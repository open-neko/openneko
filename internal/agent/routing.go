package agent

import (
	"context"
	"sync/atomic"

	ax "github.com/ax-llm/ax/packages/go"
)

// StageModels is host configuration, never a field supplied by a run or a tool.
// Empty models retain Ax's existing single-client behavior.
type StageModels struct {
	Context   string
	Executor  string
	Responder string
	Skill     string
	// An optional approved route for later executor turns after actor-code
	// errors. The host chooses both aliases; the model cannot change them.
	ExecutorEscalation  string
	ExecutorAfterErrors int
}

// RoutedClient keeps route selection with the model transport. The agent reads
// only the stage profile; capability admission remains entirely in Tools.
type RoutedClient struct {
	ax.AIClient
	Stages         StageModels
	PricingVersion string
	Prices         map[string]TokenPrice
	GraphJinPrice  *TokenPrice
}

// Keep Ax's model-aware capabilities visible through the Harness wrapper.
func (r *RoutedClient) GetFeatures(model string) map[string]ax.Value {
	if features, ok := r.AIClient.(interface {
		GetFeatures(string) map[string]ax.Value
	}); ok {
		return features.GetFeatures(model)
	}
	return nil
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

// executorErrorRoute rewrites only an executor alias after a committed error
// turn. The underlying Ax router still selects the provider, and its rate
// limiter sees the actual route before the request is dispatched. It does not
// retry a request or repeat a tool effect.
type executorErrorRoute struct {
	ax.AIClient
	baseline   string
	escalation string
	after      int32
	errors     *atomic.Int32
}

func (r *executorErrorRoute) selected(model string) string {
	if model == r.baseline && r.errors.Load() >= r.after {
		return r.escalation
	}
	return model
}

func (r *executorErrorRoute) request(request map[string]ax.Value) map[string]ax.Value {
	model, _ := request["model"].(string)
	selected := r.selected(model)
	if selected == model {
		return request
	}
	copy := make(map[string]ax.Value, len(request))
	for key, value := range request {
		copy[key] = value
	}
	copy["model"] = selected
	return copy
}

func (r *executorErrorRoute) Chat(ctx context.Context, request, options map[string]ax.Value) (ax.Value, error) {
	return r.AIClient.Chat(ctx, r.request(request), options)
}

func (r *executorErrorRoute) Stream(ctx context.Context, request, options map[string]ax.Value) ([]ax.Value, error) {
	return r.AIClient.Stream(ctx, r.request(request), options)
}

// AxGen checks provider features before transport. Preserve the router's
// model-aware capabilities when decorating it.
func (r *executorErrorRoute) GetFeatures(model string) map[string]ax.Value {
	if features, ok := r.AIClient.(interface {
		GetFeatures(string) map[string]ax.Value
	}); ok {
		return features.GetFeatures(r.selected(model))
	}
	return nil
}
