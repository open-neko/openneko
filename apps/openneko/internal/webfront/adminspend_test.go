package webfront

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestAdminSpendAdminOnly(t *testing.T) {
	f, _, _ := signinFront(t, liveScalekit)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/spend", ""},
		{"PUT", "/api/v1/admin/spend", `{}`},
		{"DELETE", "/api/v1/admin/spend/alerts/00000000-0000-0000-0000-000000000000", ""},
		{"PUT", "/api/v1/admin/spend/workflows/00000000-0000-0000-0000-000000000000", `{"hourlyUsd":1}`},
		{"DELETE", "/api/v1/admin/spend/workflows/00000000-0000-0000-0000-000000000000", ""},
		{"GET", "/api/v1/admin/workflow-api-limits", ""},
		{"PUT", "/api/v1/admin/workflow-api-limits", `{"rollingTokenBudget":5000,"rollingCostMicrosBudget":5000}`},
	} {
		if w := call(f, c.method, c.path, c.body, sessionCookie("member")); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
			t.Errorf("%s %s as a member: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
		if w := call(f, c.method, c.path, c.body, nil); w.Code == 200 {
			t.Errorf("%s %s as anonymous: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

func TestAdminSpendLimits(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")
	seed(t, db, `insert into workflow_definition (id, org_id, name) values ('11111111-1111-1111-1111-111111111111', 'org', 'Daily report')`)
	seed(t, db, `insert into spend_ledger (org_id, workflow_id, source, cost_micros, tokens, priced, provider, model, task)
		values ('org', '11111111-1111-1111-1111-111111111111', 'cron', 750000, 1200, 'billed', 'gemini', 'gemini-9', 'turn')`)

	got := decode(t, call(f, "GET", "/api/v1/admin/spend", "", admin))
	limits, _ := got["limits"].(map[string]any)
	ceilings, _ := got["ceilings"].(map[string]any)
	if limits["runCapUsd"] != 5.0 || limits["orgDailyUsd"] != 500.0 || limits["warnPercent"] != 80.0 || got["metricRefresh"] != "card" ||
		ceilings["orgDailyUsd"] != 2000.0 {
		t.Fatalf("defaults %v", got)
	}
	day := got["org"].(map[string]any)["day"].(map[string]any)
	if day["spentUsd"] != 0.75 || day["heldUsd"] != 0.0 || !strings.HasSuffix(day["resetsAt"].(string), "T00:00:00.000Z") {
		t.Errorf("day %v", day)
	}
	wfs := got["workflows"].([]any)
	if len(wfs) != 1 || wfs[0].(map[string]any)["spentTodayUsd"] != 0.75 || wfs[0].(map[string]any)["hourlyOverrideUsd"] != nil {
		t.Errorf("workflows %v", wfs)
	}
	meter := got["byModel"].([]any)
	if len(meter) != 1 || meter[0].(map[string]any)["tokens"] != 1200.0 || meter[0].(map[string]any)["task"] != "turn" {
		t.Errorf("byModel %v", meter)
	}
	if alerts := got["alerts"].([]any); len(alerts) != 0 {
		t.Errorf("alerts %v", alerts)
	}

	valid := `"runCapUsd":4,"orgHourlyUsd":150,"orgDailyUsd":400,"workflowHourlyUsd":40,"workflowDailyUsd":120,"warnPercent":75`
	for _, c := range []struct{ body, want string }{
		{`[]`, "A JSON body is required."},
		{`{` + strings.Replace(valid, `"orgDailyUsd":400`, `"orgDailyUsd":2500`, 1) + `}`, "Organization daily budget cannot exceed $2000.00."},
		{`{` + strings.Replace(valid, `"runCapUsd":4`, `"runCapUsd":0`, 1) + `}`, "Per-run cap must be a positive dollar amount."},
		{`{` + strings.Replace(valid, `"runCapUsd":4`, `"runCapUsd":"4"`, 1) + `}`, "Per-run cap must be a positive dollar amount."},
		{`{` + strings.Replace(valid, `"orgHourlyUsd":150`, `"orgHourlyUsd":450`, 1) + `}`, "Organization hourly budget cannot exceed the daily budget."},
		{`{` + strings.Replace(valid, `"runCapUsd":4`, `"runCapUsd":45`, 1) + `}`, "Per-run cap cannot exceed an hourly budget, or no run could start."},
		{`{` + strings.Replace(valid, `"warnPercent":75`, `"warnPercent":75.5`, 1) + `}`, "Warning threshold must be a whole number from 50 to 99."},
		{`{` + valid + `,"metricRefresh":"hourly"}`, "Metric refresh must be card, daily, weekly or off."},
		{`{` + valid + `,"metricRefresh":null}`, "Metric refresh must be card, daily, weekly or off."},
	} {
		w := call(f, "PUT", "/api/v1/admin/spend", c.body, admin)
		if w.Code != 400 || decode(t, w)["error"] != c.want {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body.String())
		}
	}

	got = decode(t, call(f, "PUT", "/api/v1/admin/spend", `{`+valid+`,"metricRefresh":"weekly"}`, admin))
	limits = got["limits"].(map[string]any)
	if limits["runCapUsd"] != 4.0 || limits["orgDailyUsd"] != 400.0 || limits["warnPercent"] != 75.0 || got["metricRefresh"] != "weekly" {
		t.Fatalf("saved %v", got)
	}
	if got = decode(t, call(f, "PUT", "/api/v1/admin/spend", `{`+valid+`}`, admin)); got["metricRefresh"] != "weekly" {
		t.Errorf("a save without metricRefresh changed it: %v", got["metricRefresh"])
	}
	if got = decode(t, call(f, "PUT", "/api/v1/admin/spend", `{`+valid+`,"metricRefresh":"card"}`, admin)); got["metricRefresh"] != "card" {
		t.Errorf("card: %v", got["metricRefresh"])
	}
	var column *string
	var updatedBy string
	if err := db.QueryRow(context.Background(), `select metric_refresh, updated_by_user_id from spend_limit where org_id = 'org' and workflow_id is null`).
		Scan(&column, &updatedBy); err != nil || column != nil || updatedBy != "admin" {
		t.Errorf("stored row: %v %v %v", column, updatedBy, err)
	}

	err := pgx.BeginFunc(context.Background(), db, func(tx pgx.Tx) error {
		return admitRunSpend(context.Background(), tx, "org", "", "", "api", time.Now())
	})
	var reserved int64
	if err != nil || db.QueryRow(context.Background(), `select reserved_micros from spend_reservation where org_id = 'org'`).Scan(&reserved) != nil || reserved != 4_000_000 {
		t.Errorf("the next admission reserved %d (%v), want the saved cap", reserved, err)
	}

	var events int
	var first bool
	if err := db.QueryRow(context.Background(), `select count(*) over (), payload = $1::jsonb from audit_chain
		where org_id = 'org' and event = 'spend:limit_changed' order by seq limit 1`,
		`{"actorUserId":"admin","previous":{"runCapUsd":5,"orgHourlyUsd":200,"orgDailyUsd":500,"workflowHourlyUsd":50,"workflowDailyUsd":200,"warnPercent":80,"metricRefresh":"card"},
		  "next":{"runCapUsd":4,"orgHourlyUsd":150,"orgDailyUsd":400,"workflowHourlyUsd":40,"workflowDailyUsd":120,"warnPercent":75,"metricRefresh":"weekly"}}`).
		Scan(&events, &first); err != nil || events != 3 || !first {
		t.Errorf("audit: %d events, first payload matches %v, %v", events, first, err)
	}
}

func TestAdminWorkflowSpendOverride(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")
	const wf = "11111111-1111-1111-1111-111111111111"
	seed(t, db, `insert into workflow_definition (id, org_id, name) values ('`+wf+`', 'org', 'Daily report')`)
	path := "/api/v1/admin/spend/workflows/" + wf

	got := decode(t, call(f, "PUT", path, `{"hourlyUsd":6,"dailyUsd":null}`, admin))
	row := got["workflows"].([]any)[0].(map[string]any)
	if row["hourlyOverrideUsd"] != 6.0 || row["hourlyLimitUsd"] != 6.0 || row["dailyOverrideUsd"] != nil || row["dailyLimitUsd"] != 200.0 {
		t.Fatalf("override %v", row)
	}
	for _, c := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{"PUT", path, `{"hourlyUsd":30,"dailyUsd":20}`, 400, "Workflow hourly budget cannot exceed the daily budget."},
		{"PUT", path, `{"hourlyUsd":0}`, 400, "Workflow hourly budget must be a positive dollar amount."},
		{"PUT", path, `{"dailyUsd":900}`, 400, "Workflow daily budget cannot exceed $500.00."},
		{"PUT", "/api/v1/admin/spend/workflows/00000000-0000-0000-0000-000000000000", `{"hourlyUsd":1,"dailyUsd":2}`, 400, "Workflow not found."},
		{"PUT", "/api/v1/admin/spend/workflows/not-a-uuid", `{"hourlyUsd":1}`, 404, "Workflow not found."},
		{"DELETE", "/api/v1/admin/spend/workflows/not-a-uuid", "", 404, "Workflow not found."},
	} {
		w := call(f, c.method, c.path, c.body, admin)
		if w.Code != c.code || decode(t, w)["error"] != c.want {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, w.Code, w.Body.String())
		}
	}

	got = decode(t, call(f, "PUT", path, `not json`, admin))
	if row = got["workflows"].([]any)[0].(map[string]any); row["hourlyOverrideUsd"] != nil {
		t.Errorf("a body that is not JSON clears the override, as in Node: %v", row)
	}
	call(f, "PUT", path, `{"hourlyUsd":6}`, admin)
	got = decode(t, call(f, "DELETE", path, "", admin))
	if row = got["workflows"].([]any)[0].(map[string]any); row["hourlyOverrideUsd"] != nil || row["hourlyLimitUsd"] != 50.0 {
		t.Errorf("cleared %v", row)
	}
	var overrides int
	if err := db.QueryRow(context.Background(), `select count(*) from spend_limit where workflow_id is not null`).Scan(&overrides); err != nil || overrides != 0 {
		t.Errorf("override rows %d %v", overrides, err)
	}
	var match bool
	if err := db.QueryRow(context.Background(), `select payload = $2::jsonb from audit_chain where org_id = 'org' and entity_id = $1 order by seq limit 1`,
		wf, `{"actorUserId":"admin","next":{"dailyUsd":null,"hourlyUsd":6},"workflowId":"`+wf+`"}`).Scan(&match); err != nil || !match {
		t.Errorf("audit payload matches %v, %v", match, err)
	}
}

func TestAdminSpendAlertAck(t *testing.T) {
	f, _, db := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")
	var id string
	if err := db.QueryRow(context.Background(), `insert into behavior_alert (org_id, kind, subject, observed, threshold, window_seconds, details)
		values ('org', 'spend.budget_warning', 'org', 16000, 16000, 3600, '{"message":"Spend is at 80%.","windowStart":"2026-09-17T09:00:00.000Z"}')
		returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	alerts := decode(t, call(f, "GET", "/api/v1/admin/spend", "", admin))["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["id"] != id || alerts[0].(map[string]any)["message"] != "Spend is at 80%." {
		t.Fatalf("alerts %v", alerts)
	}
	path := "/api/v1/admin/spend/alerts/" + id
	if got := decode(t, call(f, "DELETE", path, "", admin)); len(got["alerts"].([]any)) != 0 {
		t.Errorf("after acknowledge %v", got["alerts"])
	}
	for _, p := range []string{path, "/api/v1/admin/spend/alerts/nope"} {
		if w := call(f, "DELETE", p, "", admin); w.Code != 404 || decode(t, w)["error"] != "Alert not found or already acknowledged." {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
	var by string
	if err := db.QueryRow(context.Background(), `select acknowledged_by from behavior_alert where id = $1`, id).Scan(&by); err != nil || by != "admin" {
		t.Errorf("acknowledged_by %q %v", by, err)
	}
}

func TestAdminWorkflowAPILimits(t *testing.T) {
	f, _, _ := signinFront(t, liveScalekit)
	admin := sessionCookie("admin")
	t.Setenv("OPENNEKO_WORKFLOW_API_ORG_ROLLING_COST_MICROS_BUDGET", "nope")
	if got := call(f, "GET", "/api/v1/admin/workflow-api-limits", "", admin).Body.String(); got != `{"limits":{"rollingTokenBudget":1000000,"rollingCostMicrosBudget":null}}` {
		t.Errorf("defaults %s", got)
	}
	for _, body := range []string{
		`{"rollingTokenBudget":0,"rollingCostMicrosBudget":50000000}`,
		`{"rollingTokenBudget":5000.5,"rollingCostMicrosBudget":50000000}`,
		`{"rollingTokenBudget":5000,"rollingCostMicrosBudget":10000000001}`,
		`{"rollingTokenBudget":"5000","rollingCostMicrosBudget":5000}`,
		`not json`,
	} {
		if w := call(f, "PUT", "/api/v1/admin/workflow-api-limits", body, admin); w.Code != 400 ||
			decode(t, w)["error"] != "Enter a token budget from 1,000 to 100,000,000 and a spend budget from $0.001 to $10,000." {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	want := `{"limits":{"rollingTokenBudget":10000000,"rollingCostMicrosBudget":10000000000}}`
	if got := call(f, "PUT", "/api/v1/admin/workflow-api-limits", `{"rollingTokenBudget":10000000,"rollingCostMicrosBudget":10000000000}`, admin).Body.String(); got != want {
		t.Errorf("saved %s", got)
	}
	if got := call(f, "GET", "/api/v1/admin/workflow-api-limits", "", admin).Body.String(); got != want {
		t.Errorf("read back %s", got)
	}
}
