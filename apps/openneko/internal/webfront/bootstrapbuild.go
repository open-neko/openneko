package webfront

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

var bootstrapRoleFocus = map[string]string{
	"CEO": "company-wide health, channel mix, geographic mix, product concentration.",
	"CFO": "margin, cost, AR/cash, FX/currency exposure if multi-currency.",
	"COO": "production capacity, work center throughput, fulfillment, lead times, workforce productivity.",
	"CRO": "sales rep performance, quota attainment, pipeline, channel sales mix, account health.",
	"CMO": "customer acquisition, retention, segment performance, marketing ROI, brand reach in their actual markets.",
	"CIO": "systems reliability, data freshness, integration health, infrastructure cost, security posture relative to their actual stack.",
	"CPO": "product adoption, feature usage, activation and retention of their actual product lines, roadmap signal quality.",
}

// buildBootstrapPrompt is buildPrompt() in packages/llm/src/bootstrap-metrics-writer.ts.
func buildBootstrapPrompt(orgName, businessProfile, industryInsights string, seats []string) string {
	total := strconv.Itoa(len(seats) * 4)
	perRole := make([]string, len(seats))
	focusLines := make([]string, len(seats))
	for i, r := range seats {
		perRole[i] = "4 for " + r
		f, ok := bootstrapRoleFocus[r]
		if !ok {
			f = "executive priorities relevant to this role for this specific company."
		}
		focusLines[i] = "- " + r + ": " + f
	}
	perRoleList, focus := strings.Join(perRole, ", "), strings.Join(focusLines, "\n")
	insightsBlock := industryInsights
	if jsTrim(industryInsights) == "" {
		insightsBlock = "(industry research is disabled for this org — work from business_profile alone)"
	}
	return "You are a senior BI consultant designing the first dashboard a new customer sees. You are handed a business_profile (what the company actually does) and an industry_insights briefing (industry context). You must propose exactly " +
		total +
		" starter dashboard cards: 4 per role for " +
		strings.Join(seats, ", ") +
		".\n\nNO TOOLS NEEDED. Do not call any tool — not `terminal`/`Bash`, not `read_file`/`Read`, not `write_file`/`Write`, not `execute_code`, not search/web/browser tools — every fact you need is inline below. Generate the JSON directly as your final answer.\n\nGROUNDING RULE — non-negotiable:\n- Every card must measure something the company ACTUALLY does, sells, operates, or employs, as named in the business_profile (products, channels, geographies, workforce, currencies, work centers, sales territories, etc.).\n- industry_insights is INTERPRETATION CONTEXT ONLY. It tells you what's normal in the industry. It does NOT give you license to invent product lines, markets, or segments the company is not in.\n- Concrete negative example: if business_profile lists only mountain, road, and touring bikes, you MUST NOT propose e-bike metrics, battery metrics, or e-bike-related KPIs — even if industry_insights talks about e-bike growth. Same rule for any other industry trend the company isn't actually exposed to.\n- If you can't ground a card in the business_profile, drop it and pick another.\n\nPer-role focus:\n" +
		focus +
		"\n\nField rules:\n- role: must be exactly one of " +
		strings.Join(seats, ", ") +
		".\n- slug: kebab-case, lowercase, unique within its role, descriptive (e.g. \"wholesale-vs-dtc-mix\", \"rep-quota-attainment\", \"wc-utilization\"). Match /^[a-z0-9]+(-[a-z0-9]+)*$/.\n- title: short, scannable, plain English (under 60 chars), no marketing fluff.\n- why: one sentence explaining why this matters for THIS specific company, citing a concrete fact from the business profile (a number, a product name, a territory, a workforce count). No generic \"important for executives\" prose.\n- chart_hint: one of \"kpi\" (single big number), \"line\" (trend), \"bar\" (categories), \"donut\" (mix/share), \"area\" (cumulative).\n\n================================================================================\nINPUT — companyName: " +
		orgName +
		"\n================================================================================\n\n================================================================================\nbusiness_profile:\n================================================================================\n\n" +
		businessProfile +
		"\n\n================================================================================\nindustry_insights:\n================================================================================\n\n" +
		insightsBlock +
		"\n\n================================================================================\nOUTPUT CONTRACT — respond with ONE JSON object, exactly this shape, no prose, no code fences:\n================================================================================\n\n{\n  \"metrics\": [\n    {\n      \"role\": \"CEO\",\n      \"slug\": \"kebab-case-slug\",\n      \"title\": \"Short title\",\n      \"why\": \"One sentence citing a concrete fact from the business profile.\",\n      \"chart_hint\": \"kpi\"\n    }\n  ]\n}\n\nRules for the JSON:\n- Output a SINGLE JSON object. No markdown fences, no prose before or after.\n- metrics array has exactly " +
		total +
		" entries: " +
		perRoleList +
		".\n- chart_hint must be one of: kpi, line, bar, donut, area.\n- slug must be unique within its role.\n"
}

type bootstrapMetric struct{ Role, Slug, Title, Why, ChartHint string }

var (
	bootstrapCharts = []string{"kpi", "line", "bar", "donut", "area"}
	bootstrapSlug   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

func jsMember(v any, key string) any {
	if obj, ok := v.(*jsObj); ok && obj != nil {
		if val, ok := obj.vals[key]; ok {
			return val
		}
	}
	return jsUndefined{}
}

func jsTypeof(v any) string {
	switch v.(type) {
	case jsUndefined:
		return "undefined"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	}
	return "object"
}

// validateBootstrapMetrics is validate() in bootstrap-metrics-writer.ts:
// exactly four valid cards for each seat, with no slug twice in a role.
func validateBootstrapMetrics(raw any, seats []string) ([]bootstrapMetric, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("bootstrap-metrics: expected array, got " + jsTypeof(raw))
	}
	var errs []string
	seen := map[string]map[string]bool{}
	var out []bootstrapMetric
	for i, m := range list {
		where := "[" + strconv.Itoa(i) + "]"
		if _, ok := m.(*jsObj); !ok {
			if _, isList := m.([]any); !isList {
				errs = append(errs, where+" not an object")
				continue
			}
		}
		role, slug, title, why, chart := jsMember(m, "role"), jsMember(m, "slug"), jsMember(m, "title"), jsMember(m, "why"), jsMember(m, "chart_hint")
		r, ok := role.(string)
		if !ok || !slices.Contains(seats, r) {
			errs = append(errs, where+" bad role: "+jsTemplate(role))
			continue
		}
		s, ok := slug.(string)
		if !ok || !bootstrapSlug.MatchString(s) {
			errs = append(errs, where+" bad slug: "+jsTemplate(slug))
			continue
		}
		t, ok := title.(string)
		if !ok || jsTrim(t) == "" {
			errs = append(errs, where+" missing title")
			continue
		}
		w, ok := why.(string)
		if !ok || jsTrim(w) == "" {
			errs = append(errs, where+" missing why")
			continue
		}
		c, ok := chart.(string)
		if !ok || !slices.Contains(bootstrapCharts, c) {
			errs = append(errs, where+" bad chart_hint: "+jsTemplate(chart))
			continue
		}
		if seen[r] == nil {
			seen[r] = map[string]bool{}
		}
		if seen[r][s] {
			errs = append(errs, where+" duplicate slug "+s+" in role "+r)
			continue
		}
		seen[r][s] = true
		out = append(out, bootstrapMetric{Role: r, Slug: s, Title: jsTrim(t), Why: jsTrim(w), ChartHint: c})
	}
	for _, r := range seats {
		if n := len(seen[r]); n != 4 {
			errs = append(errs, fmt.Sprintf("role %s has %d cards, expected 4", r, n))
		}
	}
	if len(errs) > 0 {
		return nil, errors.New("bootstrap-metrics validation failed:\n  " + strings.Join(errs, "\n  "))
	}
	return out, nil
}

// buildBootstrapMetrics is the bootstrap_metrics_build job: four starter
// cards for each seat that has no metric yet, each followed by a
// metric_refresh job on River.
func (a *API) buildBootstrapMetrics(ctx context.Context, jobID, orgID string) error {
	a.jobProgress(ctx, jobID, "Loading profile")
	var orgName string
	var profileID, businessProfile, industryInsights *string
	var seats []string
	err := a.DB.QueryRow(ctx, `select coalesce((select name from organization where id = $1), $1), p.id::text, p.business_profile, p.industry_insights,
		  coalesce((select active_seats from onboarding_wizard where org_id = $1 limit 1), '{}')
		from (select 1) one left join lateral (select id, business_profile, industry_insights from customer_profile
		  where org_id = $1 and is_current limit 1) p on true`, orgID).Scan(&orgName, &profileID, &businessProfile, &industryInsights, &seats)
	if err != nil {
		return err
	}
	if profileID == nil {
		return errors.New("no current customer_profile for org " + orgID + " — run business_profile_build first")
	}
	if businessProfile == nil || *businessProfile == "" {
		return errors.New("customer_profile " + *profileID + " has no business_profile")
	}
	if len(seats) == 0 {
		return errors.New("no active_seats on onboarding_wizard for org " + orgID + " — wizard not submitted?")
	}
	seeded, existing := map[string]bool{}, map[string]bool{}
	rows, err := a.DB.Query(ctx, `select role, slug from metric where org_id = $1`, orgID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var role, slug string
		if err := rows.Scan(&role, &slug); err != nil {
			rows.Close()
			return err
		}
		seeded[role], existing[role+"::"+slug] = true, true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var need, done []string
	for _, s := range seats {
		if seeded[s] {
			done = append(done, s)
		} else {
			need = append(need, s)
		}
	}
	if len(need) == 0 {
		log.Printf("[bootstrap-metrics] org=%s all %d seat(s) already bootstrapped — nothing to do", orgID, len(seats))
		a.jobProgress(ctx, jobID, "Bootstrap metrics already present")
		return nil
	}
	if len(done) > 0 {
		log.Printf("[bootstrap-metrics] org=%s skipping already-seeded seats: %s", orgID, strings.Join(done, ", "))
	}
	a.jobProgress(ctx, jobID, "Generating cards")
	if err := a.ensureHostConfigProvisioned(ctx, orgID); err != nil {
		return err
	}
	a.jobProgress(ctx, jobID, "Generating bootstrap metrics")
	started := time.Now()
	text, err := a.runAgentJob(ctx, agentJob{OrgID: orgID, RunID: jobID, Label: "bootstrap-metrics", MaxAttempts: 1,
		Prompt: buildBootstrapPrompt(orgName, *businessProfile, deref(industryInsights), need)})
	if err != nil {
		return err
	}
	parsed, err := parseAgentJSON(text)
	if err != nil {
		return err
	}
	metrics, err := validateBootstrapMetrics(jsMember(parsed, "metrics"), need)
	if err != nil {
		return err
	}
	log.Printf("[bootstrap-metrics] org=%s generated %d cards for seats %s in %.0fs", orgID, len(metrics), strings.Join(need, ", "), time.Since(started).Seconds())
	a.jobProgress(ctx, jobID, fmt.Sprintf("Generated %d cards", len(metrics)))
	a.jobProgress(ctx, jobID, "Saving cards")
	if a.Jobs == nil {
		return errors.New("the job queue is not ready")
	}
	inserted, skipped, chained := 0, 0, 0
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		for _, m := range metrics {
			if existing[m.Role+"::"+m.Slug] {
				skipped++
				continue
			}
			var metricID, refreshID string
			var status *string
			if a.MetricsRefresh {
				pending := "pending"
				status = &pending
			}
			if err := tx.QueryRow(ctx, `insert into metric (org_id, role, slug, source, title, description, why, chart_hint, created_by_job, last_refresh_status)
				values ($1, $2, $3, 'bootstrap', $4, $5, $5, $6, $7, $8) returning id::text`,
				orgID, m.Role, m.Slug, m.Title, m.Why, m.ChartHint, jobID, status).Scan(&metricID); err != nil {
				return err
			}
			inserted++
			if !a.MetricsRefresh {
				continue
			}
			if err := tx.QueryRow(ctx, `insert into processing_job (org_id, kind, status, trigger, trigger_payload)
				values ($1, 'metric_refresh', 'queued', 'chain_after_bootstrap_metrics_build', jsonb_build_object('metricId', $2::text)) returning id::text`,
				orgID, metricID).Scan(&refreshID); err != nil {
				return err
			}
			if err := a.Jobs.Build(ctx, tx, jobs.KindMetricRefresh, orgID, refreshID, jobs.BuildAttempts); err != nil {
				return err
			}
			chained++
		}
		return nil
	})
	if err != nil {
		return err
	}
	log.Printf("[bootstrap-metrics] org=%s inserted=%d skipped=%d (of %d generated)", orgID, inserted, skipped, len(metrics))
	log.Printf("[bootstrap-metrics] chained %d metric_refresh job(s)", chained)
	return nil
}
