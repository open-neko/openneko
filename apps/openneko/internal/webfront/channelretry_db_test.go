package webfront

import (
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

func TestInboundRedispatchMakesNoSecondRun(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	seed(t, db, `create sequence neko_test_done_fault`)
	seed(t, db, `create function neko_test_done_fault() returns trigger language plpgsql as $$
		begin
		  if new.status = 'done' and nextval('neko_test_done_fault') = 1 then raise exception 'planted done fault'; end if;
		  return new;
		end $$`)
	seed(t, db, `create trigger neko_test_done_fault before update on inbound_dedup for each row execute function neko_test_done_fault()`)
	runs := func() int { return count(t, db, `select count(*) from work_run`) }
	before, jobsBefore := runs(), len(riverQueueJobs(t, db, jobs.KindWorkRun, jobs.QueueWorkRun))

	update := parse(t, `{"update_id":40,"message":{"chat":{"id":7},"from":{"id":1},"text":"once"}}`)
	if a.processInbound(ctx, "org", "telegram", update) {
		t.Fatal("an update whose done mark failed was settled")
	}
	if !a.processInbound(ctx, "org", "telegram", update) {
		t.Fatal("the second dispatch did not settle the update")
	}
	if got := runs() - before; got != 1 {
		t.Errorf("a repeated dispatch of one update made %d runs", got)
	}
	if got := len(riverQueueJobs(t, db, jobs.KindWorkRun, jobs.QueueWorkRun)) - jobsBefore; got != 1 {
		t.Errorf("a repeated dispatch of one update queued %d work_run jobs", got)
	}
	if got := count(t, db, `select count(*) from work_message where content = 'once'`); got != 1 {
		t.Errorf("a repeated dispatch of one update saved %d messages", got)
	}
}

func TestInboundRunMessageAndJobCommitTogether(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	seed(t, db, `create sequence neko_test_job_fault`)
	seed(t, db, `create function neko_test_job_fault() returns trigger language plpgsql as $$
		begin
		  if new.trigger = 'channel_inbound' and nextval('neko_test_job_fault') = 1 then raise exception 'planted job fault'; end if;
		  return new;
		end $$`)
	seed(t, db, `create trigger neko_test_job_fault before insert on processing_job for each row execute function neko_test_job_fault()`)
	runs := func() int { return count(t, db, `select count(*) from work_run`) }
	before := runs()

	update := parse(t, `{"update_id":41,"message":{"chat":{"id":7},"from":{"id":1},"text":"together"}}`)
	if a.processInbound(ctx, "org", "telegram", update) {
		t.Fatal("an update whose job insert failed was settled")
	}
	if got := runs() - before; got != 0 {
		t.Errorf("a failed job insert left %d runs", got)
	}
	if got := count(t, db, `select count(*) from work_message where content = 'together'`); got != 0 {
		t.Errorf("a failed job insert left %d messages", got)
	}
	if !a.processInbound(ctx, "org", "telegram", update) {
		t.Fatal("the retry did not settle the update")
	}
	if got := runs() - before; got != 1 {
		t.Errorf("a failed attempt and its retry made %d runs", got)
	}
}

func TestInboundLostClaimIsNotDead(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	if dead, n, err := a.recordInboundFailure(ctx, "org", "telegram", "gone", "no-claim", parse(t, `{"u":2}`), "boom"); err != nil || dead || n != 0 {
		t.Fatalf("a failure with no claimed row: dead %v, attempts %d, err %v; want not dead", dead, n, err)
	}

	update := parse(t, `{"update_id":42,"message":{"chat":{"id":7},"from":{"id":1},"text":"taken"}}`)
	key := inboundUpdateKey(update)
	seed(t, db, `create function neko_test_steal_claim() returns trigger language plpgsql as $$
		begin
		  update inbound_dedup set claimed_by = 'another-process' where update_key = '`+key+`';
		  return new;
		end $$`)
	seed(t, db, `create trigger neko_test_steal_claim after insert on processing_job for each row execute function neko_test_steal_claim()`)
	if a.processInbound(ctx, "org", "telegram", update) {
		t.Error("an update whose claim another process took was settled as done or dead")
	}
	if got := rowText(t, db, `select status || ' ' || attempts from inbound_dedup where update_key = $1`, key); got != "pending 0" {
		t.Errorf("the ledger row of a lost claim is %q; want pending with no attempt counted", got)
	}
}

func TestInboundLeaseLastsWhileTheDispatchRuns(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	b := &API{Gate: a.Gate, DB: a.DB, Version: a.Version, PublicURL: a.PublicURL, SecretsDir: a.SecretsDir, Jobs: a.Jobs}
	old := inboundLease
	inboundLease = 600 * time.Millisecond
	t.Cleanup(func() { inboundLease = old })
	seed(t, db, `create function neko_test_slow_job() returns trigger language plpgsql as $$
		begin perform pg_sleep(2); return new; end $$`)
	seed(t, db, `create trigger neko_test_slow_job before insert on processing_job for each row execute function neko_test_slow_job()`)

	update := parse(t, `{"update_id":43,"message":{"chat":{"id":7},"from":{"id":1},"text":"slow"}}`)
	done := make(chan bool, 1)
	go func() { done <- a.processInbound(ctx, "org", "telegram", update) }()
	time.Sleep(1200 * time.Millisecond)
	claim, busy, _, err := b.beginInbound(ctx, "org", "telegram", inboundUpdateKey(update), update)
	if err != nil {
		t.Fatal(err)
	}
	if claim != "" || !busy {
		t.Errorf("a second process took an update after %v while the first still dispatched it", 1200*time.Millisecond)
	}
	if !<-done {
		t.Error("the slow dispatch did not settle")
	}
}

func TestInboundRetrySweepSendsAPendingUpdateAgain(t *testing.T) {
	f, db := inboundFront(t)
	a, ctx := f.API, t.Context()
	jobsOn := a.Jobs
	a.Jobs = nil
	update := parse(t, `{"type":"event_callback","event":{"type":"message","channel_type":"im","text":"acked first","user":"U7","channel":"D7"}}`)
	if a.processInbound(ctx, "org", "slack", update) {
		t.Fatal("a failed dispatch settled")
	}
	a.Jobs = jobsOn
	runs := count(t, db, `select count(*) from work_run`)
	a.retryPendingInbound(ctx)
	if got := rowText(t, db, `select status from inbound_dedup where update_key = $1`, inboundUpdateKey(update)); got != "done" {
		t.Errorf("after the sweep the update is %q; want done", got)
	}
	if got := count(t, db, `select count(*) from work_run`) - runs; got != 1 {
		t.Errorf("the sweep made %d runs; want 1", got)
	}
	a.retryPendingInbound(ctx)
	if got := count(t, db, `select count(*) from work_run`) - runs; got != 1 {
		t.Errorf("a second sweep made %d runs in all; want 1", got)
	}
}
