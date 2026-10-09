package agent

import (
	"context"

	ax "github.com/ax-llm/ax/packages/go"
)

// A wrapper must preserve Ax's incremental stream interface. Falling back to
// the slice-returning Stream method here would defer every responder chunk
// until the provider finishes.
func streamEvents(ctx context.Context, client ax.AIClient, request, options map[string]ax.Value) (ax.AxChatStream, error) {
	if streaming, ok := client.(ax.StreamingAIClient); ok {
		return streaming.StreamEvents(ctx, request, options)
	}
	values, err := client.Stream(ctx, request, options)
	if err != nil {
		return nil, err
	}
	return &valueStream{values: values}, nil
}

type valueStream struct {
	values []ax.Value
	index  int
}

func (s *valueStream) Next() bool {
	if s.index >= len(s.values) {
		return false
	}
	s.index++
	return true
}

func (s *valueStream) Value() ax.Value { return s.values[s.index-1] }
func (s *valueStream) Err() error      { return nil }
func (s *valueStream) Close() error    { return nil }

// The migration keeps existing non-streaming fixtures on Chat while all
// agents still run through StreamingForward. Connected providers opt in only
// after their incremental transport passes the first-content gate.
type streamingModeClient struct {
	ax.AIClient
	enabled bool
}

func (c *streamingModeClient) GetFeatures(model string) map[string]ax.Value {
	features := map[string]ax.Value{}
	if provider, ok := c.AIClient.(interface {
		GetFeatures(string) map[string]ax.Value
	}); ok {
		for key, value := range provider.GetFeatures(model) {
			features[key] = value
		}
	}
	if !c.enabled {
		features["streaming"] = false
	}
	return features
}

func (c *streamingModeClient) StreamEvents(ctx context.Context, request, options map[string]ax.Value) (ax.AxChatStream, error) {
	if !c.enabled {
		response, err := c.AIClient.Chat(ctx, request, options)
		if err != nil {
			return nil, err
		}
		return &valueStream{values: []ax.Value{response}}, nil
	}
	return streamEvents(ctx, c.AIClient, request, options)
}
