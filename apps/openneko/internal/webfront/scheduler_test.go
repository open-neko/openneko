package webfront

import (
	"context"
	"encoding/json"
	"errors"
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
	seed(t, db, `insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled, updated_at)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, true, '2026-08-26 06:00Z')`, wfID)

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

func schedulerTestFront(t *testing.T) (*Front, func(string, ...any)) {
	t.Helper()
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
	return f, func(sql string, args ...any) { seed(t, db, sql, args...) }
}

func schedulerFirings(t *testing.T, f *Front, wfID string) map[time.Time]string {
	t.Helper()
	rows, err := f.API.DB.Query(context.Background(), `select scheduled_for, status from workflow_schedule_firing where workflow_id = $1`, wfID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[time.Time]string{}
	for rows.Next() {
		var at time.Time
		var status string
		if err := rows.Scan(&at, &status); err != nil {
			t.Fatal(err)
		}
		out[at.UTC()] = status
	}
	return out
}

func TestSchedulerCronEditMovesTheCursor(t *testing.T) {
	f, exec := schedulerTestFront(t)
	ctx := context.Background()
	const wfID = "00000000-0000-4000-8000-000000000043"
	exec(`insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled, updated_at)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, true, '2026-08-26 06:00Z')`, wfID)
	day := func(d, h, m int) time.Time { return time.Date(2026, 8, d, h, m, 0, 0, time.UTC) }
	f.API.RunWorkflowSchedulerTick(ctx, day(26, 6, 30))
	f.API.RunWorkflowSchedulerTick(ctx, day(26, 7, 1))
	if got := schedulerFirings(t, f, wfID)[day(26, 7, 0)]; got != "enqueued" {
		t.Fatalf("07:00 firing before the edit = %q", got)
	}

	exec(`update workflow_definition set cron = '0 17 * * *', updated_at = $2 where id = $1`, wfID, day(26, 7, 1).Add(30*time.Second))
	f.API.RunWorkflowSchedulerTick(ctx, day(26, 7, 2))
	if got := schedulerFirings(t, f, wfID)[day(26, 7, 0)]; got != "cancelled" {
		t.Errorf("the open firing of the old schedule = %q, want cancelled", got)
	}
	var next time.Time
	if err := f.API.DB.QueryRow(ctx, `select next_fire_at from workflow_schedule_state where workflow_id = $1`, wfID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(day(26, 17, 0)) {
		t.Errorf("next_fire_at after the edit = %v, want 17:00", next.UTC())
	}
	snap := f.API.RunWorkflowSchedulerTick(ctx, day(27, 7, 5))
	if _, ok := schedulerFirings(t, f, wfID)[day(27, 7, 0)]; ok {
		t.Errorf("a firing at the old time after the edit: %+v", snap)
	}
}

func TestSchedulerDisabledWorkflowKeepsNoCursor(t *testing.T) {
	f, exec := schedulerTestFront(t)
	ctx := context.Background()
	const wfID = "00000000-0000-4000-8000-000000000044"
	exec(`insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled, updated_at)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, true, '2026-08-26 06:00Z')`, wfID)
	day := func(d, h, m int) time.Time { return time.Date(2026, 8, d, h, m, 0, 0, time.UTC) }
	f.API.RunWorkflowSchedulerTick(ctx, day(26, 6, 30))
	exec(`insert into workflow_schedule_firing (org_id, workflow_id, scheduled_for, status, available_at)
		values ('org', $1, $2, 'pending', $2)`, wfID, day(26, 7, 0))

	exec(`update workflow_definition set enabled = false, updated_at = $2 where id = $1`, wfID, day(26, 6, 40))
	f.API.RunWorkflowSchedulerTick(ctx, day(26, 6, 45))
	if got := schedulerFirings(t, f, wfID)[day(26, 7, 0)]; got != "cancelled" {
		t.Errorf("open firing of a disabled workflow = %q, want cancelled", got)
	}
	var n int
	if err := f.API.DB.QueryRow(ctx, `select count(*) from workflow_schedule_state where workflow_id = $1`, wfID).Scan(&n); err != nil || n != 0 {
		t.Errorf("schedule state rows for a disabled workflow = %d, %v", n, err)
	}

	exec(`update workflow_definition set enabled = true, updated_at = $2 where id = $1`, wfID, day(30, 7, 30))
	snap := f.API.RunWorkflowSchedulerTick(ctx, day(30, 8, 0))
	if snap.Materialized != 0 {
		t.Errorf("re-enable fired a stale occurrence at once: %+v %v", snap, schedulerFirings(t, f, wfID))
	}
	var next time.Time
	if err := f.API.DB.QueryRow(ctx, `select next_fire_at from workflow_schedule_state where workflow_id = $1`, wfID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(day(31, 7, 0)) {
		t.Errorf("next_fire_at after re-enable = %v, want 31 Aug 07:00", next.UTC())
	}
}

func TestSchedulerResumesAWorkflowPausedForToday(t *testing.T) {
	f, exec := schedulerTestFront(t)
	ctx := context.Background()
	const wfID = "00000000-0000-4000-8000-000000000045"
	day := func(d, h, m int) time.Time { return time.Date(2026, 8, d, h, m, 0, 0, time.UTC) }
	exec(`insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled, paused_until)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, false, $2)`, wfID, day(27, 0, 0))
	exec(`insert into watcher (org_id, workflow_id, name, query, value_path, op, enabled) values ('org', $1, 'w', 'q', 'v', 'gt', false)`, wfID)
	state := func() (enabled, paused, watcher bool) {
		t.Helper()
		if err := f.API.DB.QueryRow(ctx, `select d.enabled, d.paused_until is not null, w.enabled
			from workflow_definition d join watcher w on w.workflow_id = d.id where d.id = $1`, wfID).Scan(&enabled, &paused, &watcher); err != nil {
			t.Fatal(err)
		}
		return
	}

	f.API.RunWorkflowSchedulerTick(ctx, day(26, 8, 0))
	if enabled, paused, watcher := state(); enabled || !paused || watcher {
		t.Fatalf("before the pause ends: enabled=%v paused=%v watcher=%v", enabled, paused, watcher)
	}
	f.API.RunWorkflowSchedulerTick(ctx, day(27, 0, 1))
	if enabled, paused, watcher := state(); !enabled || paused || !watcher {
		t.Fatalf("after the pause ends: enabled=%v paused=%v watcher=%v", enabled, paused, watcher)
	}
	var next time.Time
	if err := f.API.DB.QueryRow(ctx, `select next_fire_at from workflow_schedule_state where workflow_id = $1`, wfID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(day(27, 7, 0)) {
		t.Errorf("next_fire_at after the pause = %v, want 27 Aug 07:00", next.UTC())
	}
}

func TestSchedulerKeepsCronFiringsInTheDailyRunBudget(t *testing.T) {
	f, exec := schedulerTestFront(t)
	ctx := context.Background()
	const wfID = "00000000-0000-4000-8000-000000000046"
	day := func(d, h, m int) time.Time { return time.Date(2026, 8, d, h, m, 0, 0, time.UTC) }
	exec(`insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled, daily_run_budget, updated_at)
		values ($1, 'org', 'Hourly check', '0 * * * *', 'UTC', true, true, 1, '2026-08-27 06:00Z')`, wfID)
	f.API.RunWorkflowSchedulerTick(ctx, day(27, 6, 30))
	f.API.RunWorkflowSchedulerTick(ctx, day(27, 7, 1))
	f.API.RunWorkflowSchedulerTick(ctx, day(27, 8, 1))
	got := schedulerFirings(t, f, wfID)
	if _, ok := got[day(27, 7, 0)]; !ok {
		t.Fatalf("the first firing of the day is missing: %v", got)
	}
	if _, ok := got[day(27, 8, 0)]; ok {
		t.Errorf("a second firing on a budget of one: %v", got)
	}
	f.API.RunWorkflowSchedulerTick(ctx, day(28, 7, 1))
	if _, ok := schedulerFirings(t, f, wfID)[day(28, 7, 0)]; !ok {
		t.Errorf("the budget did not reset on the next day: %v", schedulerFirings(t, f, wfID))
	}
}

func TestSchedulerWaitsForTheRetryDelayOfAFiring(t *testing.T) {
	f, exec := schedulerTestFront(t)
	ctx := context.Background()
	const wfID = "00000000-0000-4000-8000-000000000047"
	day := func(d, h, m, s int) time.Time { return time.Date(2026, 8, d, h, m, s, 0, time.UTC) }
	exec(`insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled, updated_at)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, true, '2026-08-27 06:00Z')`, wfID)
	f.API.RunWorkflowSchedulerTick(ctx, day(27, 6, 30, 0))
	exec(`insert into workflow_schedule_firing (org_id, workflow_id, scheduled_for, status, available_at)
		values ('org', $1, $2, 'pending', $3)`, wfID, day(26, 7, 0, 0), day(27, 6, 30, 15))
	if snap := f.API.RunWorkflowSchedulerTick(ctx, day(27, 6, 30, 5)); snap.Dispatched != 0 {
		t.Errorf("a firing dispatched inside its retry delay: %+v", snap)
	}
	if snap := f.API.RunWorkflowSchedulerTick(ctx, day(27, 6, 30, 20)); snap.Dispatched != 1 {
		t.Errorf("the firing after its retry delay: %+v", snap)
	}
}

func TestSchedulerEditJustBeforeTheFiringKeepsTheRun(t *testing.T) {
	f, exec := schedulerTestFront(t)
	ctx := context.Background()
	const wfID = "00000000-0000-4000-8000-000000000048"
	at := func(h, m, s int) time.Time { return time.Date(2026, 8, 26, h, m, s, 0, time.UTC) }
	exec(`insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled, updated_at)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, true, '2026-08-25 12:00Z')`, wfID)
	f.API.RunWorkflowSchedulerTick(ctx, at(8, 0, 0))

	exec(`update workflow_definition set cron = '0 9 * * *', updated_at = $2 where id = $1`, wfID, at(8, 59, 45))
	f.API.RunWorkflowSchedulerTick(ctx, at(9, 0, 10))
	if got := schedulerFirings(t, f, wfID)[at(9, 0, 0)]; got == "" {
		t.Errorf("the 09:00 run after an edit at 08:59:45 is lost: %v", schedulerFirings(t, f, wfID))
	}

	exec(`update workflow_definition set updated_at = $2 where id = $1`, wfID, at(9, 0, 30).Add(24*time.Hour-90*time.Second))
	f.API.RunWorkflowSchedulerTick(ctx, at(9, 0, 10).Add(24*time.Hour))
	if got := schedulerFirings(t, f, wfID)[at(9, 0, 0).Add(24*time.Hour)]; got == "" {
		t.Errorf("the 09:00 run after a re-apply at 08:59:00 is lost: %v", schedulerFirings(t, f, wfID))
	}
}

func TestSchedulerFailedDispatchKeepsACancelledFiring(t *testing.T) {
	f, exec := schedulerTestFront(t)
	ctx := context.Background()
	const wfID = "00000000-0000-4000-8000-000000000049"
	exec(`insert into workflow_definition (id, org_id, name, cron, cron_timezone, cron_enabled, enabled)
		values ($1, 'org', 'Morning check', '0 7 * * *', 'UTC', true, true)`, wfID)
	var firingID string
	if err := f.API.DB.QueryRow(ctx, `insert into workflow_schedule_firing (org_id, workflow_id, scheduled_for, status, last_error)
		values ('org', $1, '2026-08-26 07:00Z', 'cancelled', 'schedule disabled before dispatch') returning id::text`, wfID).Scan(&firingID); err != nil {
		t.Fatal(err)
	}
	f.API.releaseFailedWorkflowDispatch(ctx, firingID, errors.New("queue down"))
	var status, lastError string
	if err := f.API.DB.QueryRow(ctx, `select status, last_error from workflow_schedule_firing where id = $1`, firingID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || lastError != "schedule disabled before dispatch" {
		t.Errorf("a failed dispatch changed a cancelled firing to %s (%s)", status, lastError)
	}
}
