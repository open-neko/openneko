package webfront

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestHealthAndSecurityEndpoints(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":null}`, db)
	op := f.API.OperatorHandler()

	w := call(op, "GET", "/health", "", nil)
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("/health with no scheduler: %d %q", w.Code, w.Body.String())
	}
	w = call(op, "GET", "/health/scheduler", "", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("/health/scheduler with no scheduler: %d %s", w.Code, w.Body.String())
	}

	w = call(op, "GET", "/health/security", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/health/security: %d %s", w.Code, w.Body.String())
	}
	var sec struct {
		Status       string         `json:"status"`
		AuditLogging map[string]any `json:"auditLogging"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sec); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"healthy": true, "failureCount": float64(0), "lastFailureAt": nil, "lastError": nil}
	if sec.Status != "ok" || !reflect.DeepEqual(sec.AuditLogging, want) {
		t.Fatalf("/health/security body = %s", w.Body.String())
	}

	f.API.recordAudit(context.Background(), auditEvent{
		OrgID:      "org",
		EntityKind: "workflow",
		EntityID:   "wf-1",
		Event:      "workflow.updated",
		Payload:    map[string]any{"bad": struct{}{}},
	})

	w = call(op, "GET", "/health/security", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/health/security degraded: %d %s", w.Code, w.Body.String())
	}
	sec.AuditLogging = nil
	if err := json.Unmarshal(w.Body.Bytes(), &sec); err != nil {
		t.Fatal(err)
	}
	a := sec.AuditLogging
	if sec.Status != "degraded" || a["healthy"] != false || a["failureCount"] != float64(1) || a["lastFailureAt"] == nil || a["lastError"] == nil || len(a) != 4 {
		t.Fatalf("/health/security degraded body = %s", w.Body.String())
	}
	w = call(f, "GET", "/health", "", nil)
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("/health follows the scheduler, not the audit log: %d %q", w.Code, w.Body.String())
	}

	f.API.ResetWorkflowSchedulerHealth(time.Now().UTC().Add(-5 * time.Minute))
	w = call(f, "GET", "/health", "", nil)
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != "degraded" {
		t.Fatalf("/health with a stale scheduler: %d %q", w.Code, w.Body.String())
	}
	f.API.ResetWorkflowSchedulerHealth(time.Now().UTC())
	w = call(f, "GET", "/health", "", nil)
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("/health with a fresh scheduler: %d %q", w.Code, w.Body.String())
	}
}

func TestDurableWorkflowSchedulerAndHealthEndpoint(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	ctx := context.Background()
	var schema string
	if err := db.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	q := &jobs.Queue{Pool: db, Schema: schema + "_river", BossSchema: schema + "_boss"}
	t.Cleanup(func() { db.Exec(context.Background(), "drop schema if exists "+q.Schema+" cascade") }) //nolint:errcheck
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ins, err := jobs.NewInserter(db, q.Schema)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newSetupFront(t, `{"provider":null}`, db)
	f.API.Jobs = ins

	const wfID = "00000000-0000-4000-8000-000000000042"
	seed(t, db, `insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, true)`, wfID)

	boot := time.Date(2026, 8, 26, 6, 30, 0, 0, time.UTC)
	f.API.ResetWorkflowSchedulerHealth(boot)

	// First tick after boot initializes next_fire_at to 07:00 without firing.
	snap1 := f.API.RunWorkflowSchedulerTick(ctx, boot)
	if snap1.Status != "ok" || snap1.Materialized != 0 || snap1.Dispatched != 0 {
		t.Fatalf("boot tick: %+v", snap1)
	}
	var nextFire time.Time
	if err := db.QueryRow(ctx, `select next_fire_at from workflow_schedule_state where workflow_id = $1`, wfID).Scan(&nextFire); err != nil {
		t.Fatal(err)
	}
	if !nextFire.Equal(time.Date(2026, 8, 26, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("next_fire_at = %v, want 2026-08-26T07:00:00Z", nextFire)
	}

	// Second tick after two days of downtime coalesces into one firing and dispatches it.
	resume := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	snap2 := f.API.RunWorkflowSchedulerTick(ctx, resume)
	if snap2.Status != "ok" || snap2.Materialized != 1 || snap2.Dispatched != 1 {
		t.Fatalf("resume tick: %+v", snap2)
	}
	var status, jobID string
	var scheduledFor time.Time
	if err := db.QueryRow(ctx, `select status, coalesce(queue_job_id, ''), scheduled_for from workflow_schedule_firing where workflow_id = $1`, wfID).Scan(&status, &jobID, &scheduledFor); err != nil {
		t.Fatal(err)
	}
	if status != "enqueued" || jobID == "" || !scheduledFor.Equal(time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("firing: status=%q jobID=%q scheduledFor=%v", status, jobID, scheduledFor)
	}

	// Verify /health/scheduler reports ok when fresh and 503 when stale.
	f.API.ResetWorkflowSchedulerHealth(time.Now().UTC())
	w := call(f, "GET", "/health/scheduler", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/health/scheduler fresh: %d %s", w.Code, w.Body.String())
	}
	f.API.ResetWorkflowSchedulerHealth(time.Now().UTC().Add(-5 * time.Minute))
	w = call(f, "GET", "/health/scheduler", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/health/scheduler stale: %d %s", w.Code, w.Body.String())
	}
}

func TestHealthDetailOnlyOnTheAdminSocketAndForSignedInAdmins(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	for _, signIn := range []string{`{"provider":null}`, `{"provider":{"pluginName":"scalekit"}}`} {
		f, _ := newSetupFront(t, signIn, db)
		f.API.recordAudit(context.Background(), auditEvent{OrgID: "org", EntityKind: "workflow", EntityID: "wf-secret-id",
			Event: "workflow.updated", Payload: map[string]any{"bad": struct{}{}}})
		f.API.ResetWorkflowSchedulerHealth(time.Now().UTC())
		bare := map[string]string{"/health/security": `{"status":"degraded"}`, "/health/scheduler": `{"status":"ok"}`}
		callers := map[string]map[string]string{"no session": nil, "member": sessionCookie("member")}
		if signIn == `{"provider":null}` {
			callers["solo admin"] = nil
		}
		for who, header := range callers {
			for path, want := range bare {
				if w := call(f, "GET", path, "", header); strings.TrimSpace(w.Body.String()) != want {
					t.Errorf("%s, %s on the public port: %d %s", signIn, who, w.Code, w.Body.String())
				}
			}
		}
		for _, h := range []struct {
			who     string
			handler http.Handler
			header  map[string]string
			detail  bool
		}{
			{"admin socket", f.API.OperatorHandler(), nil, true},
			{"admin session", f, sessionCookie("admin"), signIn != `{"provider":null}`},
		} {
			sec := call(h.handler, "GET", "/health/security", "", h.header).Body.String()
			sched := call(h.handler, "GET", "/health/scheduler", "", h.header).Body.String()
			got := strings.Contains(sec, `"lastError"`) && strings.Contains(sched, `"startedAt"`)
			if got != h.detail {
				t.Errorf("%s, %s: detail = %v, want %v: %s %s", signIn, h.who, got, h.detail, sec, sched)
			}
		}
	}
}

func TestPublicHealthProbeDoesNoAuthWork(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	f.API.ResetWorkflowSchedulerHealth(time.Now().UTC())
	probed := func() bool {
		f.API.Gate.mu.Lock()
		defer f.API.Gate.mu.Unlock()
		return f.API.Gate.provider != nil
	}
	if probed() {
		t.Fatal("the gate read the sign-in status before any request")
	}
	if w := call(f, "GET", "/health", "", sessionCookie("admin")); w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("/health: %d %q", w.Code, w.Body.String())
	}
	if probed() {
		t.Fatal("/health read the sign-in status")
	}
	call(f, "GET", "/health/scheduler", "", sessionCookie("admin"))
	if !probed() {
		t.Fatal("/health/scheduler did not check for a signed-in admin")
	}
}
