package webfront

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
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

func TestAConcurrentJobsReadDoesNotOverwriteANewerOne(t *testing.T) {
	f, _ := modelSettingsFront(t)
	a := f.API
	t.Setenv("OPENNEKO_AGENT_CONCURRENCY", "")
	if w := call(f, "PUT", "/api/v1/settings/agent", `{"globalCap":5}`, sessionCookie("admin")); w.Code != 200 {
		t.Fatalf("save 5: %d %s", w.Code, w.Body.String())
	}
	saved := make(chan struct{})
	var first atomic.Bool
	afterAgentConcurrencyRead = func() {
		if first.CompareAndSwap(false, true) {
			go func() {
				defer close(saved)
				if w := call(f, "PUT", "/api/v1/settings/agent", `{"globalCap":7}`, sessionCookie("admin")); w.Code != 200 {
					t.Errorf("save 7: %d %s", w.Code, w.Body.String())
				}
			}()
			select {
			case <-saved:
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	t.Cleanup(func() { afterAgentConcurrencyRead = func() {} })
	if _, err := a.ApplyAgentConcurrency(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-saved:
	case <-time.After(10 * time.Second):
		t.Fatal("the save of 7 did not finish")
	}
	a.slots.mu.Lock()
	limit := a.slots.limit
	a.slots.mu.Unlock()
	if limit != 7 {
		t.Fatalf("limit %d after the saves of 5 and then 7, want 7", limit)
	}
}

func TestAConcurrentJobsSaveReachesAnotherWebProcess(t *testing.T) {
	f, db := modelSettingsFront(t)
	t.Setenv("OPENNEKO_AGENT_CONCURRENCY", "")
	old := agentConcurrencyRefresh
	agentConcurrencyRefresh = 20 * time.Millisecond
	t.Cleanup(func() { agentConcurrencyRefresh = old })
	other := &API{Gate: f.API.Gate, DB: db, SecretsDir: f.API.SecretsDir}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if n, err := other.ApplyAgentConcurrency(ctx); err != nil || n != 3 {
		t.Fatalf("the other process at start: %d %v", n, err)
	}
	other.StartAgentConcurrencyRefresh(ctx, t.Logf)
	if w := call(f, "PUT", "/api/v1/settings/agent", `{"globalCap":6}`, sessionCookie("admin")); w.Code != 200 {
		t.Fatalf("save 6: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		other.slots.mu.Lock()
		limit := other.slots.limit
		other.slots.mu.Unlock()
		if limit == 6 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the other process kept the limit %d after a save of 6", limit)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTheChannelTurnLimitIsStatedWhereTheSettingIs(t *testing.T) {
	root := "../../../.."
	limit := fmt.Sprintf("at most %d", jobs.WorkRunWorkers)
	for _, name := range []string{"apps/web/src/app/admin/settings/agent/AgentForm.tsx", "apps/web/src/app/admin/settings/SetupWizard.tsx", "INSTALL.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "Slack and Telegram messages run "+limit) && !strings.Contains(string(b), "Slack and Telegram messages run as `work_run` jobs, "+limit) {
			t.Errorf("%s does not say that Slack and Telegram messages run %s turns at once", name, limit)
		}
	}
}
