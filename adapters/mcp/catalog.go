// Package mcp adapts a trusted MCP session to run-scoped harness capabilities.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-neko/harness/internal/agent"
)

// Admission is supplied by the host, never by MCP discovery or model output.
type Admission struct {
	Name, Alias, Version, Origin, Effect, Description string
	Schema                                            json.RawMessage
}

// Admit pins discovered schemas to the host allowlist. Unknown tools stay hidden.
func Admit(ctx context.Context, session *protocol.ClientSession, allowed []Admission) ([]agent.Capability, error) {
	if session == nil || len(allowed) > 64 {
		return nil, fmt.Errorf("invalid MCP admission")
	}
	discovered := map[string]*protocol.Tool{}
	cursor, seen := "", map[string]bool{}
	for page := 0; page < 32; page++ {
		result, err := session.ListTools(ctx, &protocol.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("empty MCP discovery")
		}
		for _, tool := range result.Tools {
			if tool == nil || tool.Name == "" || discovered[tool.Name] != nil || len(discovered) >= 512 {
				return nil, fmt.Errorf("invalid MCP discovery")
			}
			discovered[tool.Name] = tool
		}
		if result.NextCursor == "" {
			break
		}
		if seen[result.NextCursor] || page == 31 {
			return nil, fmt.Errorf("MCP discovery pagination failed")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
	admitted := make([]agent.Capability, 0, len(allowed))
	for _, item := range allowed {
		if item.Effect != "read" {
			return nil, fmt.Errorf("MCP effect boundary not qualified")
		}
		tool := discovered[item.Name]
		if tool == nil || !json.Valid(item.Schema) || item.Alias == "" || item.Origin == "" {
			return nil, fmt.Errorf("MCP capability unavailable")
		}
		var objectSchema map[string]any
		if json.Unmarshal(item.Schema, &objectSchema) != nil || objectSchema["type"] != "object" {
			return nil, fmt.Errorf("MCP tool requires object input schema")
		}
		actual, err := json.Marshal(tool.InputSchema)
		if err != nil || !sameJSON(actual, item.Schema) {
			return nil, fmt.Errorf("MCP schema changed")
		}
		name := item.Name
		admitted = append(admitted, agent.Capability{Name: item.Alias, Version: item.Version, Origin: item.Origin, Effect: item.Effect, Description: item.Description, InputSchema: item.Schema,
			Call: func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
				var args map[string]any
				if err := json.Unmarshal(input, &args); err != nil {
					return nil, err
				}
				result, err := session.CallTool(ctx, &protocol.CallToolParams{Name: name, Arguments: args})
				if err != nil {
					return nil, err
				}
				if result == nil {
					return nil, fmt.Errorf("empty MCP result")
				}
				texts := make([]string, 0, len(result.Content))
				for _, content := range result.Content {
					text, ok := content.(*protocol.TextContent)
					if !ok {
						return nil, fmt.Errorf("unsupported MCP content")
					}
					texts = append(texts, text.Text)
				}
				encoded, err := json.Marshal(struct {
					Content    []string `json:"content"`
					Structured any      `json:"structured_content,omitempty"`
					IsError    bool     `json:"is_error"`
				}{texts, result.StructuredContent, result.IsError})
				if err != nil || len(encoded) > 262144 {
					return nil, fmt.Errorf("MCP result exceeds limit")
				}
				return encoded, nil
			}})
	}
	return admitted, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && bytes.Equal(mustJSON(x), mustJSON(y))
}

func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }
