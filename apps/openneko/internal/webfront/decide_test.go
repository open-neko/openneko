package webfront

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func seedPgBoss(t *testing.T, db *pgxpool.Pool, f *Front) {
	t.Helper()
	ddl, err := os.ReadFile("testdata/pgboss-10.4.2.sql")
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := db.QueryRow(context.Background(), `select current_schema() || '_boss'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	seed(t, db, strings.ReplaceAll(string(ddl), "pgboss", schema))
	t.Cleanup(func() { db.Exec(context.Background(), "drop schema "+schema+" cascade") }) //nolint:errcheck
	f.API.BossSchema = schema
}

func seedRiver(t *testing.T, db *pgxpool.Pool, f *Front) {
	t.Helper()
	ctx := context.Background()
	var schema string
	if err := db.QueryRow(ctx, `select current_schema() || '_river'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), "drop schema if exists "+schema+" cascade") }) //nolint:errcheck
	if err := (&jobs.Queue{Pool: db, Schema: schema, BossSchema: schema + "_none"}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	inserter, err := jobs.NewInserter(db, schema)
	if err != nil {
		t.Fatal(err)
	}
	f.API.Jobs = inserter
}

type riverJob struct {
	Args        string
	MaxAttempts int
}

func actionJobs(t *testing.T, db *pgxpool.Pool) []riverJob {
	t.Helper()
	return riverJobs(t, db, "action_execute")
}

func riverJobs(t *testing.T, db *pgxpool.Pool, kind string) []riverJob {
	t.Helper()
	ctx := context.Background()
	var schema string
	if err := db.QueryRow(ctx, `select current_schema() || '_river'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, `select args::text, max_attempts from `+schema+`.river_job
		where kind = $1 and queue = 'default' and state = 'available' order by id`, kind)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[riverJob])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type queuedJob struct {
	Data                  string
	RetryLimit, RetryWait int
	KeepDays              float64
}

func bossJobs(t *testing.T, db *pgxpool.Pool, f *Front, queue string) []queuedJob {
	t.Helper()
	rows, err := db.Query(context.Background(), `select data::text, retry_limit, retry_delay,
		extract(epoch from keep_until - start_after) / 86400 from `+f.API.BossSchema+`.job where name = $1 order by created_on`, queue)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []queuedJob
	for rows.Next() {
		var j queuedJob
		if err := rows.Scan(&j.Data, &j.RetryLimit, &j.RetryWait, &j.KeepDays); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return out
}

func lastAudit(t *testing.T, db *pgxpool.Pool, id string) string {
	t.Helper()
	var event, payload string
	err := db.QueryRow(context.Background(), `select event, payload::text from audit_chain
		where entity_kind = 'action_request' and entity_id = $1 order by seq desc limit 1`, id).Scan(&event, &payload)
	if err != nil {
		return ""
	}
	return event + " " + payload
}

func TestActionRequestDecision(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedDaily(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	seedPgBoss(t, db, f)
	seedRiver(t, db, f)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	patch := func(id, body string, who map[string]string) (int, string) {
		w := call(f, "PATCH", "/api/v1/action-requests/"+id, body, who)
		return w.Code, w.Body.String()
	}
	row := func(id string) string {
		var s string
		if err := db.QueryRow(ctx, `select status || ' ' || coalesce(approved_by_user_id, '-') || ' ' || coalesce(rejection_reason, '-')
			|| ' ' || (approved_at is not null)::text from action_request where id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	for _, c := range []struct {
		id, body string
		who      map[string]string
		code     int
		want     string
	}{
		{arRunless, `{"decision":"maybe"}`, admin, 400, `{"error":"decision must be 'approve', 'reject', or 'retry'"}`},
		{arRunless, `not json`, admin, 400, `{"error":"decision must be 'approve', 'reject', or 'retry'"}`},
		{arOrgRun, `{"decision":"approve"}`, member, 404, `{"error":"not found"}`},
		{arSource, `{"decision":"approve"}`, member, 404, `{"error":"not found"}`},
		{arPack, `{"decision":"reject"}`, member, 404, `{"error":"not found"}`},
		{"not-a-uuid", `{"decision":"approve"}`, admin, 404, `{"error":"not found"}`},
		{"30000000-0000-4000-8000-0000000000ff", `{"decision":"approve"}`, admin, 404, `{"error":"not found"}`},
	} {
		if code, body := patch(c.id, c.body, c.who); code != c.code || body != c.want {
			t.Errorf("%s %s: %d %s", c.id, c.body, code, body)
		}
	}
	if s := row(arOrgRun); s != "pending_approval - - false" {
		t.Errorf("hidden request changed: %s", s)
	}

	if code, body := patch(arRunless, `{"decision":"reject","reason":"Not now"}`, admin); code != 200 ||
		body != `{"actionRequest":{"id":"`+arRunless+`","status":"rejected"}}` {
		t.Fatalf("reject: %d %s", code, body)
	}
	if s := row(arRunless); s != "rejected admin Not now true" {
		t.Errorf("rejected row: %s", s)
	}
	if a := lastAudit(t, db, arRunless); a != `rejected {"kind": "user_admin", "reason": "Not now", "rejectedBy": "admin"}` {
		t.Errorf("reject audit: %s", a)
	}
	if code, body := patch(arRunless, `{"decision":"approve"}`, admin); code != 409 ||
		body != `{"error":"invalid action_request transition: rejected → approved"}` {
		t.Errorf("second decision: %d %s", code, body)
	}
	if code, _ := patch(arOwnedRun, `{"decision":"reject","reason":7}`, member); code != 200 {
		t.Errorf("member reject: %d", code)
	}
	if s := row(arOwnedRun); s != "rejected member - true" {
		t.Errorf("reason that is not text: %s", s)
	}
	if j := actionJobs(t, db); len(j) != 0 {
		t.Errorf("a rejection queued a job: %v", j)
	}

	inserter := f.API.Jobs
	f.API.Jobs = nil
	if code, _ := patch(arHeldRun, `{"decision":"approve"}`, member); code != 500 {
		t.Errorf("approve with no job queue: %d", code)
	}
	if s := row(arHeldRun); s != "pending_approval - - false" {
		t.Errorf("an approve whose job insert failed changed the row: %s", s)
	}
	f.API.Jobs = inserter
	if code, body := patch(arHeldRun, `{"decision":"approve","reason":"ignored"}`, member); code != 200 ||
		body != `{"actionRequest":{"id":"`+arHeldRun+`","status":"approved"}}` {
		t.Fatalf("approve: %d %s", code, body)
	}
	if s := row(arHeldRun); s != "approved member - true" {
		t.Errorf("approved row: %s", s)
	}
	if a := lastAudit(t, db, arHeldRun); a != `approved {"kind": "note", "approvedBy": "member", "soloOperator": false}` {
		t.Errorf("approve audit: %s", a)
	}
	want := riverJob{`{"orgId": "org", "actionRequestId": "` + arHeldRun + `"}`, 3}
	if j := actionJobs(t, db); len(j) != 1 || j[0] != want {
		t.Errorf("approve job: %+v", j)
	}

	const group = "90000000-0000-4000-8000-000000000001"
	seed(t, db, `insert into user_group (id, org_id, slug, name) values ($1, 'org', 'buyers', 'Buyers')`, group)
	seed(t, db, `update action_policy set approver_group_id = $2 where id = $1`, policyID, group)
	seed(t, db, `insert into action_request (id, org_id, workflow_run_id, policy_id, scope, kind, status, payload)
		values ('30000000-0000-4000-8000-0000000000c0', 'org', $1, $2, 'internal', 'note', 'pending_approval', '{}')`, runHeld, policyID)
	if code, body := patch("30000000-0000-4000-8000-0000000000c0", `{"decision":"approve"}`, member); code != 403 ||
		body != `{"error":"action_request 30000000-0000-4000-8000-0000000000c0: approval needs a member of the policy's approver group"}` {
		t.Errorf("outside the approver group: %d %s", code, body)
	}
	if s := row("30000000-0000-4000-8000-0000000000c0"); s != "pending_approval - - false" {
		t.Errorf("refused decision changed the row: %s", s)
	}
	seed(t, db, `insert into user_group_membership (org_id, group_id, user_id, source) values ('org', $1, 'member', 'local')`, group)
	if code, body := patch("30000000-0000-4000-8000-0000000000c0", `{"decision":"reject"}`, member); code != 200 {
		t.Errorf("inside the approver group: %d %s", code, body)
	}

	if code, body := patch(arRetry, `{"decision":"retry"}`, member); code != 200 ||
		body != `{"actionRequest":{"id":"`+arRetry+`","status":"approved"}}` {
		t.Fatalf("retry: %d %s", code, body)
	}
	var changeset, changesetRow string
	if err := db.QueryRow(ctx, `select c.status, r.status || ' ' || (r.error is null)::text from action_changeset c
		join action_changeset_row r on r.changeset_id = c.id where c.action_request_id = $1`, arRetry).Scan(&changeset, &changesetRow); err != nil {
		t.Fatal(err)
	}
	if changeset != "approved" || changesetRow != "draft true" {
		t.Errorf("retried change set: %s, row %s", changeset, changesetRow)
	}
	if a := lastAudit(t, db, arRetry); a != `retried {"kind": "user_admin", "retriedBy": "member"}` {
		t.Errorf("retry audit: %s", a)
	}
	if j := actionJobs(t, db); len(j) != 2 || !strings.Contains(j[1].Args, arRetry) {
		t.Errorf("retry job: %+v", j)
	}
	if code, body := patch(arRetry, `{"decision":"retry"}`, member); code != 409 ||
		body != `{"error":"invalid action_request transition: approved → approved"}` {
		t.Errorf("second retry: %d %s", code, body)
	}

	seed(t, db, `update action_request set status = 'failed', failure_reason = 'boom' where id = $1`, arRetry)
	seed(t, db, `update action_changeset set status = 'failed' where action_request_id = $1`, arRetry)
	seed(t, db, `update action_changeset_row set status = 'failed', started_at = now()`)
	if code, body := patch(arRetry, `{"decision":"retry"}`, admin); code != 409 || body != `{"error":"`+unsafeRetry+`"}` {
		t.Errorf("unsafe retry: %d %s", code, body)
	}
	var failure string
	if err := db.QueryRow(ctx, `select status || ' ' || failure_reason from action_request where id = $1`, arRetry).Scan(&failure); err != nil || failure != "failed boom" {
		t.Errorf("unsafe retry changed the row: %s %v", failure, err)
	}
	if j := actionJobs(t, db); len(j) != 2 {
		t.Errorf("unsafe retry queued a job: %d", len(j))
	}
}

func TestEnsureInsights(t *testing.T) {
	ctx := context.Background()
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":{"pluginName":"magic-link"}}`, db)
	seedRiver(t, db, f)
	member := sessionCookie("member")
	ensure := func(body string) map[string]any {
		t.Helper()
		return decode(t, call(f, "POST", "/api/v1/insights/ensure", body, member))
	}
	job := func(id string) string {
		var s string
		if err := db.QueryRow(ctx, `select status || ' ' || trigger from processing_job where id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	got := ensure("")
	id, _ := got["jobId"].(string)
	if got["state"] != "queued" || job(id) != "queued processing_next_ensure" {
		t.Fatalf("no profile: %v", got)
	}
	want := riverJob{`{"orgId": "org", "processingJobId": "` + id + `"}`, 3}
	if j := riverJobs(t, db, "industry_insights_build"); len(j) != 1 || j[0] != want {
		t.Errorf("queued job: %+v", j)
	}
	if w := call(f, "POST", "/api/v1/insights/ensure", `{"force":true}`, member); w.Body.String() != `{"state":"live","jobId":"`+id+`"}` {
		t.Errorf("live: %s", w.Body.String())
	}

	seed(t, db, `update processing_job set status = 'done' where id = $1`, id)
	seed(t, db, `insert into customer_profile (org_id, version, is_current, business_profile, industry_insights)
		values ('org', 1, false, 'old', 'old insights'), ('org', 2, true, 'We sell tea.', 'Tea demand rose.')`)
	if w := call(f, "POST", "/api/insights/ensure", `{"force":"yes"}`, member); w.Body.String() != `{"state":"ready"}` {
		t.Errorf("ready: %s", w.Body.String())
	}
	got = ensure(`{"force":true}`)
	forced, _ := got["jobId"].(string)
	if got["state"] != "queued" || forced == id || job(forced) != "queued insights_regenerate" {
		t.Errorf("force: %v", got)
	}
	var insights, old string
	if err := db.QueryRow(ctx, `select coalesce((select industry_insights from customer_profile where is_current), 'null'),
		(select industry_insights from customer_profile where not is_current)`).Scan(&insights, &old); err != nil || insights != "null" || old != "old insights" {
		t.Errorf("force cleared %q, kept %q: %v", insights, old, err)
	}
	if j := riverJobs(t, db, "industry_insights_build"); len(j) != 2 {
		t.Errorf("jobs after force: %d", len(j))
	}

	seed(t, db, `insert into llm_provider_config (org_id, scope, provider, enabled) values ('org', 'research', 'disabled', true)`)
	if w := call(f, "POST", "/api/v1/insights/ensure", "", member); w.Body.String() != `{"state":"disabled"}` {
		t.Errorf("disabled: %s", w.Body.String())
	}
	if w := call(f, "GET", "/api/v1/insights/ensure", "", member); w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}
}
