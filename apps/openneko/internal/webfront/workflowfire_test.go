package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
)

const wfFiring = "e3400000-0000-4000-8000-000000000001"

func TestWorkflowRunFireCron(t *testing.T) {
	a, db, wf := workflowTurnFront(t, `[{"id":"s1","description":"Read stock"}]`)
	ctx := context.Background()
	a.turnResolve = fixedBackend("http://unused")
	var got agentCoreInput
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		got = in
		_ = in.Emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Stock is fine."))
		return axRunResult{Status: "completed", FinalText: "Stock is fine."}, nil
	}
	seed(t, db, `insert into workflow_schedule_firing (id, org_id, workflow_id, scheduled_for, status) values ($1, 'org', $2, now(), 'enqueued')`, wfFiring, wf.ID)
	job := jobs.WorkflowRunFireArgs{OrgID: "org", WorkflowID: wf.ID, TriggerKind: "cron", ScheduleFiringID: wfFiring,
		TriggerPayload: json.RawMessage(`{"firingTime":"2030-01-01T07:00:00.000Z","scheduleFiringId":"` + wfFiring + `"}`)}
	runs := func() string { return rowText(t, db, `select count(*)::text from workflow_run`) }
	before := runs()

	if err := a.WorkflowRunFire(ctx, job, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "<mode>headless</mode>") {
		t.Error("the prompt is not in headless mode")
	}
	if !strings.HasPrefix(got.UserMessage, "[scheduled run started at ") {
		t.Errorf("seed %q", got.UserMessage)
	}
	wfRunID := rowText(t, db, `select coalesce(workflow_run_id::text, '-') from workflow_schedule_firing where id = $1`, wfFiring)
	if g := rowText(t, db, `select status || ':' || (completed_at is not null) || ':' || (lease_until is null) from workflow_schedule_firing where id = $1`, wfFiring); g != "completed:true:true" {
		t.Errorf("firing %s", g)
	}
	for sql, want := range map[string]string{
		`select trigger_kind || ':' || status || ':' || chain_depth || ':' || thread_id || ':' || summary || ':' || (telemetry_summary is not null) from workflow_run where id = $1`: "cron:completed:0:" + threadMine + ":Stock is fine.:true",
		`select trigger_payload->>'scheduleFiringId' from workflow_run where id = $1`:                                                                                                wfFiring,
		`select w.status || ':' || s.source from work_run w join workflow_run r on r.work_run_id = w.id join spend_reservation s on s.work_run_id = w.id where r.id = $1`:            "completed:cron",
	} {
		if g := rowText(t, db, sql, wfRunID); g != want {
			t.Errorf("%s\n got %q want %q", sql, g, want)
		}
	}

	after := runs()
	if err := a.WorkflowRunFire(ctx, job, 2); err != nil || runs() != after || after == before {
		t.Errorf("a second delivery: %v, runs %s then %s", err, after, runs())
	}
}

func TestWorkflowRunFireReleasesFiring(t *testing.T) {
	a, db, wf := workflowTurnFront(t, `[]`)
	ctx := context.Background()
	seed(t, db, `insert into workflow_schedule_firing (id, org_id, workflow_id, scheduled_for, status) values ($1, 'org', $2, now(), 'enqueued')`, wfFiring, wf.ID)
	job := jobs.WorkflowRunFireArgs{OrgID: "org", WorkflowID: wf.ID, TriggerKind: "cron", ScheduleFiringID: wfFiring}
	firing := func() string {
		return rowText(t, db, `select status || ':' || coalesce(workflow_run_id::text, '-') || ':' || coalesce(last_error, '-') || ':' || (available_at > now()) from workflow_schedule_firing where id = $1`, wfFiring)
	}

	a.turnResolve = func(context.Context, string) (*llm.AxBackendConfig, map[string]string, error) {
		return nil, nil, errors.New("no model key")
	}
	if err := a.WorkflowRunFire(ctx, job, 1); err == nil || err.Error() != "no model key" {
		t.Fatalf("thrown: %v", err)
	}
	if g := firing(); g != "pending:-:no model key:true" {
		t.Errorf("after a thrown run: %s", g)
	}
	if g := rowText(t, db, `select string_agg(status, ',') from workflow_run where trigger_kind = 'cron' and id <> $1`, wfTurnRun); g != "failed" {
		t.Errorf("thrown run %s", g)
	}

	a.turnResolve = fixedBackend("http://unused")
	started := make(chan struct{})
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		close(started)
		<-ctx.Done()
		return axRunResult{}, ctx.Err()
	}
	go func() {
		<-started
		a.StopTurns(ctx)
	}()
	if err := a.WorkflowRunFire(ctx, job, 2); !errors.Is(err, errTurnShutdown) {
		t.Fatalf("shutdown: %v", err)
	}
	if g := firing(); !strings.HasPrefix(g, "pending:-:"+errTurnShutdown.Error()) {
		t.Errorf("after a shutdown: %s", g)
	}
}

func TestWorkflowRunFireSpendRefused(t *testing.T) {
	a, db, wf := workflowTurnFront(t, `[]`)
	ctx := context.Background()
	a.turnResolve = fixedBackend("http://unused")
	seed(t, db, `insert into workflow_schedule_firing (id, org_id, workflow_id, scheduled_for, status) values ($1, 'org', $2, now(), 'enqueued')`, wfFiring, wf.ID)
	seed(t, db, `update spend_limit set org_hourly_micros = 1 where org_id = 'org' and workflow_id is null`)
	before := rowText(t, db, `select count(*)::text from workflow_run`)
	if err := a.WorkflowRunFire(ctx, jobs.WorkflowRunFireArgs{OrgID: "org", WorkflowID: wf.ID, TriggerKind: "cron", ScheduleFiringID: wfFiring}, 1); err != nil {
		t.Fatalf("a refused run asks for a retry: %v", err)
	}
	if g := rowText(t, db, `select status || ':' || (last_error like '%budget%' or last_error like '%spend%') from workflow_schedule_firing where id = $1`, wfFiring); g != "cancelled:true" {
		t.Errorf("firing %s (%s)", g, rowText(t, db, `select last_error from workflow_schedule_firing where id = $1`, wfFiring))
	}
	if after := rowText(t, db, `select count(*)::text from workflow_run`); after != before {
		t.Errorf("runs %s then %s", before, after)
	}
}

func TestWorkflowRunFireSubscription(t *testing.T) {
	a, db, wf := workflowTurnFront(t, `[]`)
	ctx := context.Background()
	a.turnResolve = fixedBackend("http://unused")
	var got agentCoreInput
	a.turnCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		got = in
		return axRunResult{Status: "completed", FinalText: "Done."}, nil
	}
	const sub, output, obs = "e3400000-0000-4000-8000-000000000002", "e3400000-0000-4000-8000-000000000003", "e3400000-0000-4000-8000-000000000004"
	job := jobs.WorkflowRunFireArgs{OrgID: "org", WorkflowID: wf.ID, TriggerKind: "subscription", ThreadID: wfTurnThread, ParentChainDepth: 2,
		TriggeredBySubscriptionID: sub, TriggeredByOutputID: output, TriggeredByObservationID: obs,
		TriggerPayload: json.RawMessage(`{"subscription_id":"` + sub + `","output_id":"` + output + `","observation_id":"` + obs + `"}`)}
	if err := a.WorkflowRunFire(ctx, job, 1); err != nil {
		t.Fatal(err)
	}
	want := "subscription:completed:3:" + wfTurnThread + ":" + sub + ":" + output + ":" + obs + ":" + obs
	if g := rowText(t, db, `select trigger_kind || ':' || status || ':' || chain_depth || ':' || thread_id || ':' || triggered_by_subscription_id || ':' ||
		triggered_by_output_id || ':' || triggered_by_observation_id || ':' || (trigger_payload->>'observation_id') from workflow_run where triggered_by_subscription_id = $1`, sub); g != want {
		t.Errorf("run\n got %s\nwant %s", g, want)
	}
	if got.Workflow == nil || got.Workflow.ObservationID != obs {
		t.Errorf("the turn saw observation %+v", got.Workflow)
	}

	watcher := jobs.WorkflowRunFireArgs{OrgID: "org", WorkflowID: wf.ID, TriggerKind: "watcher",
		TriggerPayload: json.RawMessage(`{"watcherId":"w1","watcherName":"stock","value":3}`)}
	if err := a.WorkflowRunFire(ctx, watcher, 1); err != nil {
		t.Fatal(err)
	}
	if g := rowText(t, db, `select trigger_kind || ':' || status || ':' || chain_depth || ':' || (trigger_payload->>'watcherName') || ':' || thread_id
		from workflow_run where trigger_kind = 'watcher'`); g != "watcher:completed:0:stock:"+threadMine {
		t.Errorf("watcher run %s", g)
	}
	if !strings.Contains(got.UserMessage, `"watcherName": "stock"`) {
		t.Errorf("watcher seed %q", got.UserMessage)
	}

	seed(t, db, `update workflow_definition set enabled = false where id = $1`, wf.ID)
	if err := a.WorkflowRunFire(ctx, watcher, 1); err == nil || err.Error() != "Workflow Low stock is disabled." {
		t.Errorf("a disabled workflow: %v", err)
	}
}
