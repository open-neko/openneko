package webfront

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConcurrentJobsSettingSetsTheTurnLimitWithNoRestart(t *testing.T) {
	f, _ := modelSettingsFront(t)
	a := f.API
	t.Setenv("OPENNEKO_AGENT_CONCURRENCY", "")
	limit := func() int {
		a.slots.mu.Lock()
		defer a.slots.mu.Unlock()
		return a.slots.limit
	}
	put := func(body string) {
		t.Helper()
		if w := call(f, "PUT", "/api/v1/settings/agent", body, sessionCookie("admin")); w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	ctx := context.Background()

	if n, err := a.ApplyAgentConcurrency(ctx); err != nil || n != 3 || limit() != 3 {
		t.Fatalf("no stored setting: %d %v, limit %d, want 3", n, err, limit())
	}

	put(`{"globalCap":1}`)
	if limit() != 1 {
		t.Fatalf("after a save of 1: limit %d", limit())
	}
	first, err := a.slots.acquire(ctx, func() error { return nil }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	admitted := make(chan func(), 1)
	go func() {
		release, err := a.slots.acquire(ctx, func() error { return nil }, func() {})
		if err != nil {
			t.Error(err)
		}
		admitted <- release
	}()
	select {
	case <-admitted:
		t.Fatal("a second turn ran with a limit of 1")
	case <-time.After(50 * time.Millisecond):
	}
	put(`{"globalCap":2}`)
	select {
	case release := <-admitted:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting turn did not start after the save raised the limit")
	}

	put(`{"globalCap":5}`)
	a.slots.setLimit(0)
	if n, err := a.ApplyAgentConcurrency(ctx); err != nil || n != 5 || limit() != 5 {
		t.Fatalf("at start with 5 stored: %d %v, limit %d", n, err, limit())
	}

	t.Setenv("OPENNEKO_AGENT_CONCURRENCY", "2")
	if n, err := a.ApplyAgentConcurrency(ctx); err != nil || n != 2 || limit() != 2 {
		t.Fatalf("the env override: %d %v, limit %d", n, err, limit())
	}
	put(`{"globalCap":7}`)
	if limit() != 2 {
		t.Fatalf("a save changed the limit that OPENNEKO_AGENT_CONCURRENCY sets: %d", limit())
	}
}
