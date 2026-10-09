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
	"github.com/open-neko/openneko/apps/ax-harness/internal/axbridge"
)

type traceProbe struct {
	mu    sync.Mutex
	spans []*spanProbe
}
type spanProbe struct {
	owner  *traceProbe
	name   string
	parent ax.AxSpan
	ended  int
}

func (p *traceProbe) StartSpan(s ax.AxSpanStart) ax.AxSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	span := &spanProbe{owner: p, name: s.Name, parent: s.Parent}
	p.spans = append(p.spans, span)
	return span
}
func (*spanProbe) SetAttributes(map[string]ax.Value)    {}
func (*spanProbe) AddEvent(string, map[string]ax.Value) {}
func (*spanProbe) RecordException(error)                {}
func (*spanProbe) SetStatus(string, string)             {}
func (s *spanProbe) End()                               { s.owner.mu.Lock(); defer s.owner.mu.Unlock(); s.ended++ }

func TestRunScopedTelemetryHTTP(t *testing.T) {
	trace := &traceProbe{}
	var mu sync.Mutex
	usages := 0
	ax.SetUsageObserver(func(ax.AxUsageEvent) { mu.Lock(); defer mu.Unlock(); usages++ })
	defer ax.SetUsageObserver(nil)
	p := &probe{}
	client, _ := modelServer(t, ax.Object("role", "assistant", "tool_calls", ax.Array(call("call_1", `{"query":"reference"}`))), answer(`Answer: done`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gen := ax.NewAx("question:string -> answer:string", ax.Object("functions", ax.Array(axbridge.BindTool(ctx, ax.Fn("lookup"), p.invoke))))
	_, err := gen.ForwardWithHooks(ctx, client, ax.Object("question", "Find reference"), nil, ax.AxRuntimeHooks{Tracer: trace})
	if err != nil {
		t.Fatal(err)
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	names := []string{}
	for _, s := range trace.spans {
		names = append(names, s.name)
		if s.ended != 1 {
			t.Fatalf("span %s ended %d times", s.name, s.ended)
		}
		if s != trace.spans[0] && s.parent == nil {
			t.Fatalf("orphan span %s", s.name)
		}
	}
	if len(names) < 4 || !strings.Contains(strings.Join(names, ","), "ax_gen_tool") {
		t.Fatalf("missing model/tool telemetry: %v", names)
	}
	mu.Lock()
	defer mu.Unlock()
	if usages != 0 {
		t.Fatalf("missing upstream usage produced %d usage events", usages)
	}
	t.Logf("correlated spans=%v; absent provider usage yields no accounting event", names)
}

func TestActorCallbackCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	runtime := axgoja.NewRuntime(axgoja.WithCallable("waitForCancel", func(ax.Value) (ax.Value, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }))
	session, err := runtime.CreateSession(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	done := make(chan ax.Value, 1)
	go func() { done <- session.Execute("waitForCancel({}); final({unreachable:true})", nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback not reached")
	}
	cancel()
	select {
	case out := <-done:
		if object(out)["is_error"] != true {
			t.Fatalf("cancelled callback returned success: %v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not terminate")
	}
}

func TestNativeCancellationTranscriptHTTP(t *testing.T) {
	client, _ := modelServer(t, ax.Object("role", "assistant", "tool_calls", ax.Array(call("call_cancel", `{}`))))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := axbridge.BindTool(ctx, ax.Fn("lookup"), func(callCtx context.Context, _ map[string]ax.Value) (ax.Value, error) {
		cancel()
		<-callCtx.Done()
		return nil, callCtx.Err()
	})
	gen := ax.NewAx("question:string -> answer:string", ax.Object("functions", ax.Array(tool)))
	_, err := gen.Forward(ctx, client, ax.Object("question", "cancel"), ax.Object("control", ax.RunControl()))
	if err == nil {
		t.Fatal("cancelled generation reported success")
	}
	// Assert exactly one error result for the committed call in retained Ax memory.
	count := 0
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			// Ax 25 stores a tool result as [call, result, ok, result_text].
			if result, ok := x["results"].([]any); ok && x["role"] == "function" && len(result) == 4 {
				if call, ok := result[0].(map[string]any); ok && call["id"] == "call_cancel" {
					count++
					if result[2] != false {
						t.Error("cancellation result was not an error")
					}
				}
			}
			for _, child := range x {
				visit(child)
			}
		case []any:
			for _, child := range x {
				visit(child)
			}
		}
	}
	var memory any
	if e := json.Unmarshal([]byte(encoded(gen.Memory)), &memory); e != nil {
		t.Fatal(e)
	}
	visit(memory)
	if count != 1 {
		t.Fatalf("want one terminal cancellation result, got %d", count)
	}
}

func TestPartialToolStreamNeverDispatches(t *testing.T) {
	p := &probe{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"s1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"partial\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"query\\\":\"}}]}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := ax.NewOpenAICompatibleClient(ax.Object("base_url", server.URL, "api_key", "synthetic-test-key", "model", "fixture"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	gen := ax.NewAx("question:string -> answer:string", ax.Object("functions", ax.Array(axbridge.BindTool(ctx, ax.Fn("lookup"), p.invoke))))
	_, err := gen.Forward(ctx, client, ax.Object("question", "partial"), ax.Object("stream", true, "control", ax.RunControl()))
	if err == nil {
		t.Fatal("partial interrupted stream reported success")
	}
	p.check(t, 0)
}

func TestBoundToolRejectsClosedRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &probe{}
	tool := axbridge.BindTool(ctx, ax.Fn("lookup"), p.invoke)
	cancel()
	if _, err := tool.Handler(ax.Object("query", "late")); err == nil {
		t.Fatal("closed run admitted a callback")
	}
	p.check(t, 0)
}

func TestGojaCPUWorkIsBounded(t *testing.T) {
	runtime := axgoja.NewRuntime()
	session, err := runtime.CreateSession(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	out := object(session.Execute("while(true) {}", ax.Object("timeoutMs", 20)))
	if out["error_category"] != "timeout" {
		t.Fatalf("CPU loop was not bounded: %v", out)
	}
}
