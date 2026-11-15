package webfront

import (
	"strings"
	"testing"
)

func TestExternalEventsDispatch(t *testing.T) {
	srcStripe := "stripe"
	srcOther := "other"
	for _, c := range []struct {
		name   string
		filter map[string]any
		event  externalEventMatch
		want   bool
	}{
		{"empty filter", map[string]any{}, externalEventMatch{Name: "invoice.paid", Source: &srcStripe}, true},
		{"matching name", map[string]any{"name": "invoice.paid"}, externalEventMatch{Name: "invoice.paid"}, true},
		{"other name", map[string]any{"name": "invoice.paid"}, externalEventMatch{Name: "customer.created"}, false},
		{"name and source", map[string]any{"name": "invoice.paid", "source": "stripe"}, externalEventMatch{Name: "invoice.paid", Source: &srcOther}, false},
		{"empty source wants no source", map[string]any{"source": ""}, externalEventMatch{Name: "invoice.paid", Source: &srcStripe}, false},
		{"empty source and no source", map[string]any{"source": ""}, externalEventMatch{Name: "invoice.paid"}, true},
		{"non-string name ignored", map[string]any{"name": 3}, externalEventMatch{Name: "invoice.paid"}, true},
		{"matching source", map[string]any{"source": "stripe"}, externalEventMatch{Name: "invoice.paid", Source: &srcStripe}, true},
		{"mismatching source", map[string]any{"source": "stripe"}, externalEventMatch{Name: "invoice.paid", Source: &srcOther}, false},
		{"nil source vs filter", map[string]any{"source": "stripe"}, externalEventMatch{Name: "invoice.paid", Source: nil}, false},
	} {
		if got := externalEventFilterMatches(c.filter, c.event); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	f, db := memoryFront(t)
	seedRiver(t, db, f)
	ctx := t.Context()

	const (
		wf1  = "d1000000-0000-4000-8000-000000000001"
		wf2  = "d1000000-0000-4000-8000-000000000002"
		sub1 = "d1000000-0000-4000-8000-000000000011"
		sub2 = "d1000000-0000-4000-8000-000000000012"
		sub3 = "d1000000-0000-4000-8000-000000000013"
	)
	seed(t, db, `insert into workflow_definition (id, org_id, name, description, daily_run_budget) values
		($1, 'org', 'Billing responder', 'x', 2),
		($2, 'org', 'Budgeted responder', 'x', 1)`, wf1, wf2)
	seed(t, db, `insert into subscription (id, org_id, workflow_id, source_kind, filter, max_concurrent_runs, idempotency_key_template) values
		($1, 'org', $4, 'external_event', '{"name":"invoice.paid","source":"stripe"}', 1, 'custom:{subscription_id}:{source_record_id}:{source_version}'),
		($2, 'org', $5, 'external_event', '{}', 2, null),
		($3, 'org', $4, 'external_event', '{"name":"customer.deleted"}', 1, null)`, sub1, sub2, sub3, wf1, wf2)

	res, err := f.API.dispatchExternalEvent(ctx, "org", externalEventMatch{
		Name:   "invoice.paid",
		Source: &srcStripe,
		Payload: obj(
			"id", "in_1",
			"amount", 4200,
		),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.Matched != 2 || res.Enqueued != 2 || len(res.Decisions) != 2 {
		t.Fatalf("first dispatch: %+v", res)
	}
	if res.Decisions[0].SubscriptionID != sub1 || res.Decisions[0].Action != "enqueued" || res.Decisions[0].ObservationID == "" || res.Decisions[0].JobID == nil {
		t.Errorf("decision 0: %+v", res.Decisions[0])
	}
	if res.Decisions[1].SubscriptionID != sub2 || res.Decisions[1].Action != "enqueued" || res.Decisions[1].ObservationID == "" || res.Decisions[1].JobID == nil {
		t.Errorf("decision 1: %+v", res.Decisions[1])
	}

	const (
		threadID = "d1000000-0000-4000-8000-000000000021"
		wrun1    = "d1000000-0000-4000-8000-000000000022"
		wrun2    = "d1000000-0000-4000-8000-000000000023"
	)
	seed(t, db, `insert into work_thread (id, org_id, title) values ($1, 'org', 't')`, threadID)
	seed(t, db, `insert into work_run (id, org_id, thread_id, backend) values ($1, 'org', $3, 'test'), ($2, 'org', $3, 'test')`, wrun1, wrun2, threadID)
	seed(t, db, `insert into workflow_run (org_id, workflow_id, thread_id, work_run_id, trigger_kind, triggered_by_subscription_id, status) values
		('org', $1, $3, $4, 'subscription', $6, 'running'),
		('org', $2, $3, $5, 'subscription', $7, 'completed')`, wf1, wf2, threadID, wrun1, wrun2, sub1, sub2)

	blocked, err := f.API.dispatchExternalEvent(ctx, "org", externalEventMatch{
		Name:      "invoice.paid",
		Source:    &srcStripe,
		Payload:   obj("id", "in_2"),
		DedupeKey: "in_2",
	})
	if err != nil {
		t.Fatalf("blocked dispatch: %v", err)
	}
	if blocked.Matched != 2 || blocked.Enqueued != 0 {
		t.Fatalf("blocked counts: %+v", blocked)
	}
	if blocked.Decisions[0].Action != "dropped" || !strings.Contains(blocked.Decisions[0].Reason, "max_concurrent_runs (1/1)") {
		t.Errorf("concurrency guard: %+v", blocked.Decisions[0])
	}
	if blocked.Decisions[1].Action != "dropped" || !strings.Contains(blocked.Decisions[1].Reason, "daily_run_budget (1/1)") {
		t.Errorf("daily budget guard: %+v", blocked.Decisions[1])
	}
}
