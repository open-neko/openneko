// Package compat exercises the pinned Ax implementation over real HTTP and Goja.
// It is a qualification suite, not the production durable execution engine.
package compat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
	axgoja "github.com/ax-llm/ax/packages/go/runtime/goja"
	"github.com/open-neko/neko/apps/agent-harness/internal/axbridge"
)

// probe records metadata only. The production journal must be durable and must
// admit an operation before dispatch; this in-memory probe proves callback placement.
type probe struct {
	mu       sync.Mutex
	events   []string
	executed int
}

func (p *probe) invoke(ctx context.Context, args map[string]ax.Value) (ax.Value, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "proposed")
	status := "completed"
	defer func() { p.events = append(p.events, status) }()
	if ctx.Err() != nil {
		status = "cancelled"
		return nil, ctx.Err()
	}
	query, ok := args["query"].(string)
	if !ok || query == "" {
		status = "invalid"
		return ax.Object("error", "invalid arguments"), nil
	}
	if query == "denied" {
		status = "denied"
		return ax.Object("error", "permission denied"), nil
	}
	p.executed++
	return ax.Object("reference", "REF-42"), nil
}
func (p *probe) check(t *testing.T, executed int, events ...string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.executed != executed || strings.Join(p.events, ",") != strings.Join(events, ",") {
		t.Fatalf("execution=%d events=%v", p.executed, p.events)
	}
}
func object(v ax.Value) map[string]ax.Value { m, _ := v.(map[string]ax.Value); return m }
func encoded(v any) string                  { b, _ := json.Marshal(v); return string(b) }

// Every request crosses Ax's HTTPTransport. Only the external model is scripted.
type requestLog struct {
	mu    sync.Mutex
	items []map[string]ax.Value
}

func (r *requestLog) snapshot() []map[string]ax.Value {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]ax.Value(nil), r.items...)
}
func modelServer(t *testing.T, messages ...map[string]ax.Value) (*ax.OpenAICompatibleClient, *requestLog) {
	t.Helper()
	requests := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.mu.Lock()
		defer requests.mu.Unlock()
		var body map[string]ax.Value
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad body", 400)
			return
		}
		requests.items = append(requests.items, body)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-test-key" {
			t.Error("unexpected method/auth")
		}
		n := len(requests.items) - 1
		if n >= len(messages) {
			t.Errorf("unexpected model request %d", n+1)
			http.Error(w, "fixture exhausted", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ax.Object("id", fmt.Sprint("response-", n), "model", "fixture", "choices", ax.Array(ax.Object("index", 0, "message", messages[n], "finish_reason", "stop"))))
	}))
	t.Cleanup(server.Close)
	return ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic-test-key", "model", "fixture")), requests
}
func answer(s string) map[string]ax.Value { return ax.Object("role", "assistant", "content", s) }
func call(id, params string) ax.Value {
	return ax.Object("id", id, "type", "function", "function", ax.Object("name", "lookup", "arguments", params))
}

func TestNativeToolBoundaryHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, params, status string
		executed             int
	}{
		{"allowed", `{"query":"reference"}`, "completed", 1},
		{"denied", `{"query":"denied"}`, "denied", 0},
		{"invalid", `{"query":7}`, "invalid", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &probe{}
			client, requests := modelServer(t, ax.Object("role", "assistant", "tool_calls", ax.Array(call("call_1", tc.params))), answer(`{"answer":"done"}`))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			gen := ax.NewAx("question:string -> answer:string", ax.Object("functions", ax.Array(axbridge.BindTool(ctx, ax.Fn("lookup"), p.invoke)), "validationRetries", 0))
			out, err := gen.Forward(ctx, client, ax.Object("question", "Find reference"), nil)
			if err != nil || object(out)["answer"] != "done" {
				t.Fatalf("output=%v error=%v", out, err)
			}
			p.check(t, tc.executed, "proposed", tc.status)
			if len(requests.snapshot()) != 2 {
				t.Fatalf("requests=%d", len(requests.snapshot()))
			}
			// Verify the provider-visible transcript, rather than only callback counts.
			count := 0
			for _, v := range requests.snapshot()[1]["messages"].([]any) {
				m := v.(map[string]any)
				if m["role"] == "tool" && m["tool_call_id"] == "call_1" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("want one matching result, got %d", count)
			}
		})
	}
}

func TestAgentGojaTwoCallbacksHTTP(t *testing.T) {
	p := &probe{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime := axgoja.NewRuntime(axgoja.WithCallable("lookup", func(v ax.Value) (ax.Value, error) { return p.invoke(ctx, object(v)) }))
	client, requests := modelServer(t,
		answer(`{"javascriptCode":"final('Find reference', {})"}`),
		answer(`{"javascriptCode":"const first=lookup({query:'reference'}); const denied=lookup({query:'denied'}); final('Report reference', {first,denied});"}`),
		answer(`{"answer":"REF-42"}`))
	agent := ax.NewAgent("question:string -> answer:string", ax.Object("runtime", runtime, "directResponse", "off", "validationRetries", 0))
	defer agent.CloseRuntimeSession()
	out, err := agent.Forward(ctx, client, ax.Object("question", "Find reference"), nil)
	if err != nil || object(out)["answer"] != "REF-42" {
		t.Fatalf("output=%v error=%v requests=%d", out, err, len(requests.snapshot()))
	}
	p.check(t, 1, "proposed", "completed", "proposed", "denied")
	if len(requests.snapshot()) != 3 || !strings.Contains(encoded(requests.snapshot()[2]), "REF-42") || !strings.Contains(encoded(requests.snapshot()[2]), "permission denied") {
		t.Fatal("responder did not receive actual callback outcomes")
	}
}

func TestHTTPStreamingCancellation(t *testing.T) {
	observed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"s1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(observed)
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic-test-key", "model", "fixture"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.StreamEvents(ctx, ax.Object("chat_prompt", ax.Array(ax.Object("role", "user", "content", "hello"))), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Next() {
		t.Fatalf("missing first event: %v", stream.Err())
	}
	cancel()
	if stream.Next() || stream.Err() == nil {
		t.Fatal("cancelled stream did not fail")
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("HTTP upstream did not observe cancellation")
	}
}

func TestGojaSnapshotBoundary(t *testing.T) {
	runtime := axgoja.NewRuntime()
	session, err := runtime.CreateSession(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	session.Execute("counter=42; fn=function(){return 1}; final({counter})", nil)
	snapshot := session.SnapshotGlobals(nil)
	restored, err := runtime.CreateSession(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restored.PatchGlobals(snapshot, nil)
	state := object(restored.Inspect(nil))
	if fmt.Sprint(state["counter"]) != "42" {
		t.Fatalf("data lost: %v", state)
	}
	if _, exists := state["fn"]; exists {
		t.Fatal("function unexpectedly persisted; re-evaluate checkpoint contract")
	}
	capped, err := runtime.CreateSession(nil, ax.Object("runtimePolicy", ax.Object("maxSnapshotBytes", 64)))
	if err != nil {
		t.Fatal(err)
	}
	defer capped.Close()
	capped.Execute("large='x'.repeat(1000)", nil)
	if object(object(capped.SnapshotGlobals(nil))["bindings"])["__ax_snapshot_truncated"] != true {
		t.Fatal("missing truncation marker")
	}
}

func TestAgentSessionSnapshotBoundary(t *testing.T) {
	runtime := axgoja.NewRuntime()
	agent := ax.NewAgent("question:string -> answer:string", ax.Object("runtime", runtime))
	defer agent.CloseRuntimeSession()
	_, err := agent.ExecuteActorStep(runtime, "counter=42; final({answer:'saved'})", ax.Object("question", "save"), nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := agent.ExportSessionState(nil)
	restoredRuntime := axgoja.NewRuntime()
	restored := ax.NewAgent("question:string -> answer:string", ax.Object("runtime", restoredRuntime))
	defer restored.CloseRuntimeSession()
	if _, err = restored.ExecuteActorStep(restoredRuntime, "counter=0", ax.Object("question", "restore"), nil); err != nil {
		t.Fatal(err)
	}
	restored.RestoreSessionState(snapshot, nil)
	if fmt.Sprint(object(restored.InspectRuntime(nil))["counter"]) != "42" {
		t.Fatal("Agent-level restore lost runtime data")
	}
	if !strings.Contains(encoded(agent.ExportTrace()), "state_export") || !strings.Contains(encoded(restored.ExportTrace()), "state_restore") {
		t.Fatal("snapshot lifecycle not observable")
	}
	if _, ok := object(agent.ExportRuntimeState())["context_events"]; !ok {
		t.Fatal("context event state unavailable")
	}
}
