package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ax "github.com/ax-llm/ax/packages/go"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/session"
)

func TestAdmittedNativeAndMCPShareDurableAxBoundary(t *testing.T) {
	ctx := context.Background()
	server := protocol.NewServer(&protocol.Implementation{Name: "fixture", Version: "1"}, nil)
	var remote atomic.Int32
	schema := json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`)
	server.AddTool(&protocol.Tool{Name: "read_record", InputSchema: schema}, func(_ context.Context, request *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		remote.Add(1)
		return &protocol.CallToolResult{Content: []protocol.Content{&protocol.TextContent{Text: "record-42"}}}, nil
	})
	server.AddTool(&protocol.Tool{Name: "unapproved", InputSchema: schema}, func(context.Context, *protocol.CallToolRequest) (*protocol.CallToolResult, error) {
		t.Fatal("unapproved MCP tool called")
		return nil, nil
	})
	clientSide, serverSide := protocol.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := protocol.NewClient(&protocol.Implementation{Name: "harness", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	allowed := []Admission{{Name: "read_record", Alias: "mcp_read_record", Version: "1", Origin: "fixture", Effect: "read", Description: "Read a record by key.", Schema: schema}}
	capabilities, err := Admit(ctx, clientSession, allowed)
	if err != nil {
		t.Fatal(err)
	}
	var native atomic.Int32
	capabilities = append([]agent.Capability{{Name: "native_status", Version: "1", Origin: "host", Effect: "read", Description: "Read native status.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), Call: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if agent.OperationID(ctx) != 1 {
			t.Fatal("native operation identity")
		}
		native.Add(1)
		return json.RawMessage(`{"status":"ok"}`), nil
	}}}, capabilities...)
	tools := agent.Tools{Capabilities: capabilities}
	if _, err := tools.Binding("unapproved"); err == nil {
		t.Fatal("discovered tool gained admission")
	}
	if _, err := (agent.Tools{Capabilities: append(capabilities, capabilities[0])}).Binding("native_status"); err == nil {
		t.Fatal("name collision accepted")
	}
	answers := []string{`{"javascriptCode":"final('Read both tools',{})"}`, `{"javascriptCode":"const a=native_status({}); const b=mcp_read_record({key:'42'}); final('Record 42',{a,b});"}`, `{"answer":"Record 42"}`}
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(modelCalls.Add(1)) - 1
		if n >= len(answers) {
			t.Errorf("unexpected model request")
			http.Error(w, "unexpected", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("choices", ax.Array(ax.Object("message", ax.Object("role", "assistant", "content", answers[n]), "finish_reason", "stop"))))
	}))
	defer model.Close()
	axClient := ax.NewOpenAICompatibleClient(ax.Object("base_url", model.URL, "api_key", "synthetic", "model", "fixture"))
	spec := agent.Spec{Version: 1, RunID: "catalog-fixture", InputID: "input", Prompt: "Read record 42"}
	root := t.TempDir()
	result, err := session.RunWithTools(ctx, root, spec, axClient, tools, func(agent.Event) error { return nil })
	if err != nil || result.Status != "completed" || native.Load() != 1 || remote.Load() != 1 {
		t.Fatalf("result=%+v err=%v native=%d remote=%d", result, err, native.Load(), remote.Load())
	}
	recovery, err := session.Inspect(root, spec)
	if err != nil || len(recovery.Operations) != 2 || recovery.Operations[0].Binding == "" || recovery.Operations[1].Binding == "" {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
	if _, err := session.ResumeWithTools(ctx, root, spec, nil, agent.Tools{}, func(agent.Event) error { return nil }); err != nil || native.Load() != 1 || remote.Load() != 1 {
		t.Fatalf("terminal replay: %v", err)
	}
	changed := append([]Admission(nil), allowed...)
	changed[0].Schema = json.RawMessage(`{"type":"object"}`)
	if _, err := Admit(ctx, clientSession, changed); err == nil {
		t.Fatal("schema change accepted")
	}
	changed = append([]Admission(nil), allowed...)
	changed[0].Effect = "durable"
	if _, err := Admit(ctx, clientSession, changed); err == nil {
		t.Fatal("unqualified MCP effect admitted")
	}
}
