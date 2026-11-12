package webfront

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func job(t *testing.T, db *pgxpool.Pool, kind, status, progress string, errText any) {
	t.Helper()
	seed(t, db, `insert into processing_job (org_id, kind, status, trigger, trigger_payload, progress, error)
		values ('org', $1, $2, 'test', '{}', $3::jsonb, $4)`, kind, status, progress, errText)
}

func onboardingBody(t *testing.T, f *Front, header map[string]string) (int, string) {
	t.Helper()
	w := call(f, "GET", "/api/v1/onboarding/status", "", header)
	return w.Code, w.Body.String()
}

func TestOnboardingStatus(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	solo, _ := newSetupFront(t, `{"provider":null}`, db)

	check := func(name, want string) {
		t.Helper()
		if code, got := onboardingBody(t, solo, nil); code != 200 || got != want {
			t.Errorf("%s: %d %s", name, code, got)
		}
	}
	check("new install", `{"state":"needs_wizard"}`)

	job(t, db, "business_profile_build", "failed", "{}", "ax turn exceeded its 360s budget and was terminated")
	code, got := onboardingBody(t, solo, nil)
	if code != 200 || !strings.Contains(got, `"state":"failed"`) || !strings.Contains(got, "time limit") || strings.Contains(got, "ax turn") {
		t.Errorf("failed build: %d %s", code, got)
	}

	seed(t, db, `delete from processing_job`)
	job(t, db, "business_profile_build", "running", "{}", nil)
	var jobID string
	if err := db.QueryRow(t.Context(), `select id::text from processing_job`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	check("no stage message", `{"state":"processing","jobId":"`+jobID+`","currentStage":{"kind":"business_profile_build","message":null}}`)
	seed(t, db, `update processing_job set progress = '{"message":"   "}'`)
	check("blank stage message", `{"state":"processing","jobId":"`+jobID+`","currentStage":{"kind":"business_profile_build","message":null}}`)
	seed(t, db, `update processing_job set progress = '{"message":7}'`)
	check("number stage message", `{"state":"processing","jobId":"`+jobID+`","currentStage":{"kind":"business_profile_build","message":null}}`)
	seed(t, db, `update processing_job set progress = '{"message":"Reading sales tables…"}'`)
	check("stage message", `{"state":"processing","jobId":"`+jobID+`","currentStage":{"kind":"business_profile_build","message":"Reading sales tables…"}}`)

	seed(t, db, `insert into processing_job (org_id, kind, status, trigger, created_at) values ('org', 'metric_refresh', 'succeeded', 'old', now() - interval '1 hour')`)
	job(t, db, "bootstrap_metrics_build", "succeeded", "{}", nil)
	for range 6 {
		job(t, db, "metric_refresh", "succeeded", "{}", nil)
	}
	job(t, db, "metric_refresh", "running", `{"message":"Refreshing revenue"}`, nil)
	job(t, db, "metric_refresh", "failed", "{}", nil)
	check("metrics", `{"state":"processing","jobId":"`+jobID+`","currentStage":{"kind":"metric_refresh","message":"Refreshing revenue"},"metricsProgress":{"total":8,"completed":6,"failed":1}}`)

	seed(t, db, `insert into customer_profile (org_id, version, is_current, business_profile) values ('org', 1, false, 'old'), ('org', 3, true, 'We sell tea.')`)
	check("ready without a wizard row", `{"state":"ready","profileVersion":3,"seats":[],"mode":"shared","metricsProgress":{"total":8,"completed":6,"failed":1}}`)
	seed(t, db, `insert into onboarding_wizard (org_id, active_seats) values ('org', '{CEO,CFO,COO,HR}')`)
	seed(t, db, `delete from processing_job where kind = 'metric_refresh'`)
	check("ready with seats", `{"state":"ready","profileVersion":3,"seats":["CEO","CFO","COO","HR"],"mode":"shared"}`)

	sso, _ := newSetupFront(t, `{"provider":{"pluginName":"scalekit"}}`, db)
	admin := sessionCookie("admin")
	if code, got := onboardingBody(t, sso, admin); code != 200 || got != `{"state":"needs_persona"}` {
		t.Errorf("SSO without persona: %d %s", code, got)
	}
	seed(t, db, `insert into operator_profile (org_id, user_id, role_template) values ('org', '', 'Shared CEO view'), ('org', 'admin', '  ')`)
	if _, got := onboardingBody(t, sso, admin); got != `{"state":"needs_persona"}` {
		t.Errorf("blank or shared persona: %s", got)
	}
	seed(t, db, `update operator_profile set role_template = 'Head of EU wholesale operations' where user_id = 'admin'`)
	if code, got := onboardingBody(t, sso, admin); code != 200 || got != `{"state":"ready","profileVersion":3,"seats":[],"mode":"personal"}` {
		t.Errorf("SSO persona: %d %s", code, got)
	}
	if code, _ := onboardingBody(t, sso, sessionCookie("member")); code != 403 {
		t.Errorf("member: %d", code)
	}
	if code, _ := onboardingBody(t, sso, nil); code != 401 && code != 302 {
		t.Errorf("anonymous: %d", code)
	}
	if w := call(solo, "POST", "/api/v1/onboarding/status", "", nil); w.Code != 405 {
		t.Errorf("POST: %d", w.Code)
	}

	t.Setenv("DEMO", "true")
	if _, got := onboardingBody(t, solo, nil); got != `{"state":"ready","profileVersion":0,"seats":["CEO","CFO","CRO"],"mode":"shared"}` {
		t.Errorf("demo: %s", got)
	}
}

func TestOnboardingStatusDatabaseDown(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	f, _ := newSetupFront(t, `{"provider":null}`, db)
	if _, err := f.API.OrgID(t.Context()); err != nil {
		t.Fatal(err)
	}
	seed(t, db, `alter table customer_profile rename column version to version_gone`)
	code, got := onboardingBody(t, f, nil)
	if code != 503 || !strings.HasPrefix(got, `{"state":"db_error","message":"`) {
		t.Errorf("%d %s", code, got)
	}
}

func TestProfileBuildFailureMessage(t *testing.T) {
	text := func(s string) *string { return &s }
	for _, c := range []struct {
		stored *string
		want   string
	}{
		{nil, failureGeneric},
		{text(""), failureGeneric},
		{text("ax turn exceeded its 360s budget and was terminated"), failureTimeout},
		{text("request Timed Out after 30s"), failureTimeout},
		{text("hit the maximum iterations"), failureTimeout},
		{text("openneko serve is unavailable: connect ENOENT /run/openneko/serve.sock"), failureRuntime},
		{text("agent sandbox exited 1: builtin-skills not found"), failureRuntime},
		{text("Internal error: No LLM provider configured."), failureHandoff},
		{text("Gemini HTTP 429: quota"), failureProvider},
		{text("ratelimit reached"), failureProvider},
		{text("execute_graphql failed: unauthorized access to table orders"), failureData},
		{text("unauthorized\ntable"), failureGeneric},
		{text("unexpected /private/path containing sensitive context"), failureGeneric},
	} {
		if got := profileBuildFailureMessage(c.stored); got != c.want {
			t.Errorf("%v: %q", c.stored, got)
		}
	}
}
