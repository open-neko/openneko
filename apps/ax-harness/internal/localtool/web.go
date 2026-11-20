package localtool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
	"github.com/open-neko/openneko/apps/ax-harness/webfetch"
)

// BridgedWeb is web_fetch for a turn whose fetches openneko web makes. call
// reaches web's tool server; web holds the allowlist and checks each URL.
func BridgedWeb(allowlist string, call func(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error)) (agent.Capability, error) {
	hosts, err := webfetch.ParseHosts(allowlist)
	if err != nil {
		return agent.Capability{}, err
	}
	return agent.Capability{Name: "web_fetch", Version: "1", Origin: "web", Effect: "read",
		Description: webfetch.Description(hosts),
		InputSchema: json.RawMessage(webfetch.InputSchema),
		Call: func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
			raw, err := call(ctx, webfetch.BridgeTool, input)
			if err != nil {
				return nil, err
			}
			var result struct {
				Content []string `json:"content"`
				IsError bool     `json:"is_error"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) == 0 {
				return nil, errors.New("web_fetch gave no answer")
			}
			if result.IsError {
				return nil, errors.New(result.Content[0])
			}
			if !json.Valid([]byte(result.Content[0])) {
				return nil, errors.New("web_fetch gave no answer")
			}
			return json.RawMessage(result.Content[0]), nil
		}}, nil
}
