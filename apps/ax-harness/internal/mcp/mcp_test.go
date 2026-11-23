package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-neko/openneko/apps/ax-harness/internal/agent"
)

func connect(t *testing.T, server *protocol.Server) *protocol.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientSide, serverSide := protocol.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := protocol.NewClient(&protocol.Implementation{Name: "harness", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func byName(capabilities []agent.Capability) map[string]agent.Capability {
	out := make(map[string]agent.Capability, len(capabilities))
	for _, c := range capabilities {
		out[c.Name] = c
	}
	return out
}

func TestMCPResultKindsAndBoundaries(t *testing.T) {
	ctx := context.Background()
	server := protocol.NewServer(&protocol.Implementation{Name: "fixture", Version: "1"}, &protocol.ServerOptions{PageSize: 1})
	schema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	server.AddTool(&protocol.Tool{Name: "structured", InputSchema: schema}, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: "record-42"}}, StructuredContent: map[string]any{"id": "42"}}, nil
	})
	server.AddTool(&protocol.Tool{Name: "resource", InputSchema: schema}, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.ResourceLink{URI: "artifact://run/42", Name: "result.csv", MIMEType: "text/csv"}}}, nil
	})
	server.AddTool(&protocol.Tool{Name: "tool_error", InputSchema: schema}, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: "permission denied"}}, IsError: true}, nil
	})
	server.AddTool(&protocol.Tool{Name: "image", InputSchema: schema}, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.ImageContent{Data: []byte("image"), MIMEType: "image/png"}}}, nil
	})
	server.AddTool(&protocol.Tool{Name: "large", InputSchema: schema}, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: strings.Repeat("x", maxResultBytes)}}}, nil
	})
	server.AddTool(&protocol.Tool{Name: "slow", InputSchema: schema}, func(ctx context.Context, _ *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	caps, err := Capabilities(ctx, connect(t, server), "mcp_", nil)
	if err != nil || len(caps) != 6 {
		t.Fatalf("paginated discovery: caps=%d err=%v", len(caps), err)
	}
	tools := byName(caps)
	call := func(name string, callCtx context.Context) (map[string]any, error) {
		raw, err := tools["mcp_"+name].Call(callCtx, json.RawMessage(`{}`))
		if err != nil {
			return nil, err
		}
		var result map[string]any
		err = json.Unmarshal(raw, &result)
		return result, err
	}
	structured, err := call("structured", ctx)
	if err != nil || structured["is_error"] != false || structured["structured_content"].(map[string]any)["id"] != "42" {
		t.Fatalf("structured result lost: %v %v", structured, err)
	}
	resource, err := call("resource", ctx)
	if err != nil || resource["resource_links"].([]any)[0].(map[string]any)["uri"] != "artifact://run/42" {
		t.Fatalf("resource link lost: %v %v", resource, err)
	}
	toolError, err := call("tool_error", ctx)
	if err != nil || toolError["is_error"] != true || !strings.Contains(toolError["content"].([]any)[0].(string), "permission denied") {
		t.Fatalf("MCP tool error flattened: %v %v", toolError, err)
	}
	if _, err := call("image", ctx); err == nil || !strings.Contains(err.Error(), "unsupported MCP content") {
		t.Fatalf("image content was silently accepted: %v", err)
	}
	if _, err := call("large", ctx); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("large result was silently accepted: %v", err)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := call("slow", deadlineCtx); err == nil {
		t.Fatal("stalled MCP call ignored deadline")
	}
}

func TestMCPNamesEffectsAndSchemas(t *testing.T) {
	server := protocol.NewServer(&protocol.Implementation{Name: "fixture", Version: "1"}, nil)
	schema := json.RawMessage(`{"type":"object"}`)
	handler := func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{}, nil
	}
	server.AddTool(&protocol.Tool{Name: "records_find", Description: "Find records.", InputSchema: schema}, handler)
	server.AddTool(&protocol.Tool{Name: "records_save", InputSchema: schema, Annotations: &protocol.ToolAnnotations{ReadOnlyHint: false}}, handler)
	server.AddTool(&protocol.Tool{Name: "records_get", InputSchema: schema, Annotations: &protocol.ToolAnnotations{ReadOnlyHint: true}}, handler)
	server.AddTool(&protocol.Tool{Name: "interaction_ask", InputSchema: schema}, handler)
	server.AddTool(&protocol.Tool{Name: "Bad-Name", InputSchema: schema}, handler)
	server.AddTool(&protocol.Tool{Name: "huge", Description: strings.Repeat("d", 5000), InputSchema: schema}, handler)
	caps, err := Capabilities(context.Background(), connect(t, server), "mcp_neko_", map[string]string{"interaction_ask": "pause"})
	if err != nil {
		t.Fatal(err)
	}
	tools := byName(caps)
	if len(tools) != 5 || tools["mcp_neko_Bad-Name"].Name != "" {
		t.Fatalf("unexpected tools: %v", tools)
	}
	for name, effect := range map[string]string{"records_find": "read", "records_save": "durable", "records_get": "read", "interaction_ask": "pause"} {
		if got := tools["mcp_neko_"+name].Effect; got != effect {
			t.Fatalf("%s effect=%q, want %q", name, got, effect)
		}
	}
	if tools["mcp_neko_records_find"].Description != "Find records." || len(tools["mcp_neko_huge"].Description) != 1000 ||
		tools["mcp_neko_records_save"].Description != "records_save" {
		t.Fatal("descriptions not bounded or defaulted")
	}
}

type pipeStream struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (p pipeStream) Close() error {
	for _, c := range p.closers {
		_ = c.Close()
	}
	return nil
}

func TestHostCallCarriesAScriptDataAnswer(t *testing.T) {
	ctx := context.Background()
	answer := `{"data":{"a":"<\"` + strings.Repeat("x", 40<<20) + `"}}`
	server := protocol.NewServer(&protocol.Implementation{Name: "bridge", Version: "1"}, nil)
	server.AddTool(&protocol.Tool{Name: "script_data_query", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: answer}}}, nil
	})
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	session, err := server.Connect(ctx, &protocol.IOTransport{Reader: toServer, Writer: fromServer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	caps, caller, closeBridge, err := ConnectStream(ctx, pipeStream{toClient, fromClient, []io.Closer{toClient, fromClient}}, "mcp_neko_", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeBridge() })
	raw, err := caller(ctx, "script_data_query", json.RawMessage(`{"query":"{ a }"}`))
	if err != nil {
		t.Fatalf("a 40 MiB answer did not pass the host call: %v", err)
	}
	var result struct{ Content []string }
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) != 1 || result.Content[0] != answer {
		t.Fatalf("the answer changed: %v", err)
	}
	if _, err := byName(caps)["mcp_neko_script_data_query"].Call(ctx, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("a tool result for the model kept no 8 MiB cap: %v", err)
	}
}
