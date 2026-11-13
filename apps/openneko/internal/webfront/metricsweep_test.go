package webfront

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestMetricRefreshDue(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	ok, pending := "ok", "pending"
	for _, c := range []struct {
		cadence, org string
		status       *string
		updated      time.Time
		want         bool
	}{
		{"hourly", "", &ok, at("2026-08-20T10:59:59Z"), true},
		{"hourly", "", &ok, at("2026-08-20T11:00:01Z"), false},
		{"daily", "", &ok, at("2026-08-20T10:59:59Z"), false},
		{"weekly", "", nil, at("2026-08-13T12:00:00Z"), true},
		{"monthly", "", &ok, at("2026-08-19T12:00:00Z"), true},
		{"hourly", "", &pending, at("2026-08-01T00:00:00Z"), false},
		{"hourly", "card", &ok, at("2026-08-19T11:00:00Z"), true},
		{"hourly", "daily", &ok, at("2026-08-19T11:00:00Z"), true},
		{"hourly", "weekly", &ok, at("2026-08-19T11:00:00Z"), false},
		{"hourly", "off", &ok, at("2026-01-01T00:00:00Z"), false},
		{"weekly", "daily", &ok, at("2026-08-19T11:00:00Z"), false},
	} {
		if got := metricRefreshDue(c.cadence, c.status, c.updated, c.org, now); got != c.want {
			t.Errorf("%s/%s at %s: got %v", c.cadence, c.org, c.updated, got)
		}
	}
}

func TestMetricSweep(t *testing.T) {
	f, db := memoryFront(t)
	seedPgBoss(t, db, f)
	ctx := context.Background()
	const due, pendingID, daily, inactive = "e0000000-0000-4000-8000-000000000001", "e0000000-0000-4000-8000-000000000002",
		"e0000000-0000-4000-8000-000000000003", "e0000000-0000-4000-8000-000000000004"
	seed(t, db, `insert into metric (id, org_id, role, slug, source, title, chart_hint, cadence, active, last_refresh_status, updated_at) values
		($1, 'org', 'CFO', 'a', 'bootstrap', 'A', 'line', 'hourly', true, 'ok', now() - interval '2 hours'),
		($2, 'org', 'CFO', 'b', 'bootstrap', 'B', 'line', 'hourly', true, 'pending', now() - interval '2 days'),
		($3, 'org', 'CFO', 'c', 'bootstrap', 'C', 'line', 'daily', true, 'ok', now() - interval '2 hours'),
		($4, 'org', 'CFO', 'd', 'bootstrap', 'D', 'line', 'hourly', false, 'ok', now() - interval '2 days')`, due, pendingID, daily, inactive)
	seed(t, db, `update work_memory set expires_at = now() - interval '1 minute' where id = $1`, memOther)
	seed(t, db, `update work_pending_memory set created_at = now() - interval '15 days' where id = $1`, pendOther)
	seed(t, db, `update work_pending_memory set created_at = now() where id = $1`, pendMine)
	seed(t, db, `insert into workflow_definition (org_id, name, description, owner_user_id) values ('org', 'My digest', 'Mine', 'member')`)

	if err := f.API.MetricSweep(ctx); err != nil {
		t.Fatal(err)
	}
	var status, jobRef, kind, trigger, payload string
	if err := db.QueryRow(ctx, `select m.last_refresh_status, m.last_refresh_job_id::text, p.kind, p.trigger, p.trigger_payload::text
		from metric m join processing_job p on p.id = m.last_refresh_job_id where m.id = $1`, due).Scan(&status, &jobRef, &kind, &trigger, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || kind != "metric_refresh" || trigger != "scheduled" || payload != `{"metricId": "`+due+`"}` {
		t.Errorf("due metric: %s %s %s %s", status, kind, trigger, payload)
	}
	got := bossJobs(t, db, f, queueMetricRefresh)
	for i := range got {
		got[i].Data = regexp.MustCompile(`"queuedAt": \d{13}, `).ReplaceAllString(got[i].Data, "")
	}
	if len(got) != 1 || got[0].Data != `{"orgId": "org", "processingJobId": "`+jobRef+`"}` || got[0].RetryLimit != 2 || got[0].RetryWait != 30 || got[0].KeepDays != 7 {
		t.Errorf("queued jobs: %+v", got)
	}
	var others int
	_ = db.QueryRow(ctx, `select count(*) from processing_job where kind = 'metric_refresh' and trigger_payload->>'metricId' <> $1`, due).Scan(&others)
	if others != 0 {
		t.Errorf("%d refreshes queued for metrics that are not due", others)
	}

	var archived bool
	var events int
	_ = db.QueryRow(ctx, `select archived_at is not null, (select count(*) from work_memory_event where memory_id = $1 and action = 'expired')
		from work_memory where id = $1`, memOther).Scan(&archived, &events)
	if !archived || events != 1 {
		t.Errorf("expired memory: archived %v, %d events", archived, events)
	}
	for id, want := range map[string]string{pendOther: "declined", pendMine: "proposed"} {
		var s string
		_ = db.QueryRow(ctx, `select status from work_pending_memory where id = $1`, id).Scan(&s)
		if s != want {
			t.Errorf("pending %s: %s", id, s)
		}
	}

	root := orgAgentRoot("org")
	for _, name := range []string{".git", "memory", "workflows"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("the sweep wrote %s", name)
		}
	}

	seed(t, db, `update spend_limit set metric_refresh = 'off' where org_id = 'org' and workflow_id is null`)
	seed(t, db, `update metric set updated_at = now() - interval '2 hours' where id = $1`, daily)
	if err := f.API.MetricSweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := bossJobs(t, db, f, queueMetricRefresh); len(got) != 1 {
		t.Errorf("second sweep queued again: %+v", got)
	}

	seed(t, db, `update spend_limit set metric_refresh = null where org_id = 'org' and workflow_id is null`)
	seed(t, db, `update metric set updated_at = now() - interval '2 days' where id = $1`, daily)
	f.API.BossSchema = "missing_boss"
	f.API.queues.Delete(queueMetricRefresh)
	if err := f.API.MetricSweep(ctx); err == nil {
		t.Errorf("the sweep passed with no queue")
	}
	_ = db.QueryRow(ctx, `select last_refresh_status from metric where id = $1`, daily).Scan(&status)
	if status != "ok" {
		t.Errorf("a failed sweep left the metric %s", status)
	}
}
