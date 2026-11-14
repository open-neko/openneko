package webfront

import (
	"context"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestReconcileStaleRuns(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	const thread = "61000000-0000-4000-8000-000000000001"
	seed(t, db, `insert into work_thread (id, org_id) values ($1, 'org')`, thread)
	seed(t, db, `insert into workflow_definition (id, org_id, name, owner_user_id) values ('62000000-0000-4000-8000-000000000001', 'org', 'Flow', '')`)
	runs := []struct {
		id, status string
		age        time.Duration
		wfStatus   string
	}{
		{"63000000-0000-4000-8000-000000000001", "running", 20 * time.Minute, "running"},
		{"63000000-0000-4000-8000-000000000002", "running", 5 * time.Minute, "running"},
		{"63000000-0000-4000-8000-000000000003", "queued", 15 * time.Minute, "queued"},
		{"63000000-0000-4000-8000-000000000004", "queued", time.Minute, "queued"},
		{"63000000-0000-4000-8000-000000000005", "completed", time.Hour, "completed"},
	}
	for i, r := range runs {
		at := now.Add(-r.age)
		seed(t, db, `insert into work_run (id, org_id, thread_id, backend, status, created_at, updated_at) values ($1, 'org', $2, 'test', $3, $4, $4)`,
			r.id, thread, r.status, at)
		seed(t, db, `insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, status)
			values ($1, 'org', '62000000-0000-4000-8000-000000000001', $2, $3, 'manual', $4)`,
			"64000000-0000-4000-8000-00000000000"+string(rune('1'+i)), thread, r.id, r.wfStatus)
	}
	a := &API{DB: db}
	n, err := a.ReconcileStaleRuns(ctx, now, 11*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("cancelled = %d, want 2", n)
	}
	want := []struct{ status, reason string }{
		{"cancelled", staleRunReason}, {"running", ""}, {"cancelled", staleQueuedReason}, {"queued", ""}, {"completed", ""},
	}
	for i, r := range runs {
		var status, wfStatus string
		var reason, wfReason *string
		if err := db.QueryRow(ctx, `select w.status, w.error, f.status, f.error from work_run w join workflow_run f on f.work_run_id = w.id where w.id = $1`, r.id).
			Scan(&status, &reason, &wfStatus, &wfReason); err != nil {
			t.Fatal(err)
		}
		got := ""
		if reason != nil {
			got = *reason
		}
		if status != want[i].status || got != want[i].reason {
			t.Fatalf("run %d: %s %q, want %s %q", i, status, got, want[i].status, want[i].reason)
		}
		wantWF := r.wfStatus
		if want[i].status == "cancelled" {
			wantWF = "cancelled"
			if wfReason == nil || *wfReason != staleRunReason {
				t.Fatalf("workflow run %d error = %v", i, wfReason)
			}
		}
		if wfStatus != wantWF {
			t.Fatalf("workflow run %d: %s, want %s", i, wfStatus, wantWF)
		}
	}
	if n, err := a.ReconcileStaleRuns(ctx, now, 11*time.Minute); err != nil || n != 0 {
		t.Fatalf("second sweep = %d, %v", n, err)
	}
	if got := staleRunMinAge(); got != 13*time.Minute {
		t.Fatalf("staleRunMinAge = %v", got)
	}
}
