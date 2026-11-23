package compat

import (
	"context"
	"testing"
	"time"

	ax "github.com/ax-llm/ax/packages/go"
	axgoja "github.com/ax-llm/ax/packages/go/runtime/goja"
)

func TestActorCallbackCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	runtime := axgoja.NewRuntime(axgoja.WithCallable("waitForCancel", func(ax.Value) (ax.Value, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }))
	session, err := runtime.CreateSession(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Close waits for Execute, and Execute waits for ctx, so cancel runs first.
	defer session.Close()
	defer cancel()
	done := make(chan ax.Value, 1)
	go func() { done <- session.Execute("waitForCancel({}); final({unreachable:true})", nil) }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("callback not reached")
	}
	cancel()
	select {
	case out := <-done:
		if object(out)["is_error"] != true {
			t.Fatalf("cancelled callback returned success: %v", out)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("callback did not terminate")
	}
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
