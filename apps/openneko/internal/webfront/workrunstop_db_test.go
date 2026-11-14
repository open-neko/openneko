package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

func serviceRun(t *testing.T, db *pgxpool.Pool, wfID, status, mode string) (workRunID, workflowRunID string) {
	t.Helper()
	ctx := context.Background()
	var threadID string
	if err := db.QueryRow(ctx, `insert into work_thread (org_id, title, channel) values ('org', 'Service', 'workflow') returning id::text`).Scan(&threadID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `insert into work_run (org_id, thread_id, backend, status, actor_role) values ('org', $1, 'ax', $2, 'service')
		returning id::text`, threadID, status).Scan(&workRunID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `insert into workflow_run (org_id, workflow_id, thread_id, work_run_id, trigger_kind, status, execution_mode)
		values ('org', $1, $2, $3, 'api', $4, nullif($5, '')) returning id::text`, wfID, threadID, workRunID, status, mode).Scan(&workflowRunID); err != nil {
		t.Fatal(err)
	}
	return workRunID, workflowRunID
}

func canStop(t *testing.T, f *Front, workflowRunID, who string) any {
	t.Helper()
	w := call(f, "GET", "/api/v1/workflow-runs/"+workflowRunID, "", sessionCookie(who))
	if w.Code != 200 {
		return w.Code
	}
	return decode(t, w)["canStop"]
}

func TestAdminStopsServiceRun(t *testing.T) {
	front, db := signedInChatFront(t)
	wf := seedWorkflowTurn(t, db, `[{"id":"s1","description":"Wait"}]`)
	a := front.API
	seed(t, db, `update workflow_definition set created_by_thread_id = null where id = $1`, wf.ID)
	audit := func(runID string) string {
		return rowText(t, db, `select string_agg(event || ':' || coalesce(payload->>'stoppedBy', '-'), ',' order by seq) from audit_chain
			where entity_id = $1 and event = 'run:cancelled'`, runID)
	}

	t.Run("live", func(t *testing.T) {
		a.turnResolve = fixedBackend("http://unused")
		started := make(chan struct{})
		a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
			close(started)
			<-ctx.Done()
			return axRunResult{}, ctx.Err()
		}
		w := call(front, "POST", "/api/v1/workflows/"+wf.ID+"/manual-runs", `{}`, sessionCookie("admin"))
		body := decode(t, w)
		runID, wfRunID := body["workRunId"].(string), body["workflowRunId"].(string)
		<-started
		if g := canStop(t, front, wfRunID, "admin"); g != true {
			t.Fatalf("admin canStop %v", g)
		}
		if code, _ := cancelAs(front, runID, "member"); code != 404 {
			t.Fatalf("a member stopped a service run: %d", code)
		}
		if code, body := cancelAs(front, runID, "admin"); code != 200 || body != `{"ok":true,"recovered":false}` {
			t.Fatalf("%d %s", code, body)
		}
		if status := waitRunFor(t, db, runID, 20*time.Second); status != "cancelled" {
			t.Fatalf("work run %s", status)
		}
		if g := rowText(t, db, `select status from workflow_run where id = $1`, wfRunID); g != "cancelled" {
			t.Errorf("workflow run %s", g)
		}
		if g := audit(runID); g != "run:cancelled:admin" {
			t.Errorf("audit %q", g)
		}
		if g := canStop(t, front, wfRunID, "admin"); g != false {
			t.Errorf("canStop after the stop: %v", g)
		}
	})

	t.Run("queued", func(t *testing.T) {
		runID, _ := serviceRun(t, db, wf.ID, "queued", "single")
		if code, body := cancelAs(front, runID, "admin"); code != 200 || body != `{"ok":true,"recovered":true}` {
			t.Fatalf("%d %s", code, body)
		}
		for sql, want := range map[string]string{
			`select status || ':' || error from work_run where id = $1`:                                                  "cancelled:" + interruptedRunReason,
			`select status || ':' || error || ':' || (finished_at is not null) from workflow_run where work_run_id = $1`: "cancelled:" + interruptedRunReason + ":true",
			`select string_agg(kind, ',') from work_run_event where run_id = $1`:                                         "done",
		} {
			if g := rowText(t, db, sql, runID); g != want {
				t.Errorf("%s\n got %q", sql, g)
			}
		}
		if g := audit(runID); g != "run:cancelled:admin" {
			t.Errorf("audit %q", g)
		}
	})

	t.Run("finished", func(t *testing.T) {
		runID, wfRunID := serviceRun(t, db, wf.ID, "completed", "single")
		if g := canStop(t, front, wfRunID, "admin"); g != false {
			t.Errorf("canStop %v", g)
		}
		if code, body := cancelAs(front, runID, "admin"); code != 200 || body != `{"ok":true,"recovered":false}` {
			t.Fatalf("%d %s", code, body)
		}
		if g := rowText(t, db, `select (select status from work_run where id = $1) || ':' || (select status from workflow_run where work_run_id = $1) || ':' ||
			(select count(*) from work_run_event where run_id = $1) || ':' || (select count(*) from audit_chain where entity_id = $1::text)`, runID); g != "completed:completed:0:0" {
			t.Errorf("a finished run changed: %s", g)
		}
	})

	t.Run("member", func(t *testing.T) {
		runID, _ := serviceRun(t, db, wf.ID, "running", "single")
		if code, body := cancelAs(front, runID, "member"); code != 404 || body != `{"error":"Run not found"}` {
			t.Fatalf("%d %s", code, body)
		}
		if g := rowText(t, db, `select status from work_run where id = $1`, runID); g != "running" {
			t.Errorf("status %s", g)
		}
	})

	t.Run("running batch", func(t *testing.T) {
		runID, wfRunID := serviceRun(t, db, wf.ID, "running", "batch")
		if g := canStop(t, front, wfRunID, "admin"); g != false {
			t.Errorf("canStop %v", g)
		}
		if code, _ := cancelAs(front, runID, "admin"); code != 409 {
			t.Fatalf("%d", code)
		}
		if g := rowText(t, db, `select status from workflow_run where id = $1`, wfRunID); g != "running" {
			t.Errorf("status %s", g)
		}
	})
}

func TestStoppedCronRunIsNotTriedAgain(t *testing.T) {
	a, db, wf := workflowTurnFront(t, `[{"id":"s1","description":"Wait"}]`)
	a.turnResolve = fixedBackend("http://unused")
	started := make(chan string, 1)
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		started <- in.RunID
		<-ctx.Done()
		return axRunResult{}, ctx.Err()
	}
	seed(t, db, `insert into workflow_schedule_firing (id, org_id, workflow_id, scheduled_for, status) values ($1, 'org', $2, now(), 'enqueued')`, wfFiring, wf.ID)
	job := jobs.WorkflowRunFireArgs{OrgID: "org", WorkflowID: wf.ID, TriggerKind: "cron", ScheduleFiringID: wfFiring,
		TriggerPayload: json.RawMessage(`{"scheduleFiringId":"` + wfFiring + `"}`)}
	go func() {
		a.stopRun(<-started, "admin")
	}()
	if err := a.WorkflowRunFire(context.Background(), job, 1); err != nil {
		t.Fatalf("a stopped run asks River to try again: %v", err)
	}
	if g := rowText(t, db, `select status from workflow_schedule_firing where id = $1`, wfFiring); g != "completed" {
		t.Errorf("firing %s", g)
	}
	if g := rowText(t, db, `select r.status || ':' || w.status from workflow_run r join work_run w on w.id = r.work_run_id
		where r.trigger_payload->>'scheduleFiringId' = $1`, wfFiring); g != "cancelled:cancelled" {
		t.Errorf("runs %s", g)
	}
}

func TestRunStopsWhenAnotherProcessCancelsIt(t *testing.T) {
	f, db := signedInChatFront(t)
	a := f.API
	old := runStopPoll
	runStopPoll = 20 * time.Millisecond
	t.Cleanup(func() { runStopPoll = old })
	runID := insertRun(t, db, threadMine, "running")
	ctx, cancel := context.WithCancelCause(context.Background())
	run := a.registerRun(runID, "org", threadMine, cancel)
	t.Cleanup(func() { a.runs.unregister(runID, run) })
	select {
	case <-ctx.Done():
		t.Fatal("the run stopped while its row is running")
	case <-time.After(100 * time.Millisecond):
	}
	seed(t, db, `update work_run set status = 'cancelled' where id = $1`, runID)
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), errRunStopped) {
			t.Fatalf("cause %v", context.Cause(ctx))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not see the cancel of another process")
	}
}
