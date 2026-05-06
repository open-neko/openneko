/**
 * Bootstrap metrics writer — backend-agnostic.
 *
 * Per-org backend (Hermes or Claude Agent) is resolved from
 * llm_provider_config (scope='agent') by `resolveAgentBackend`. Same
 * mechanism the profiler and metric agents use, so the three stay in
 * lockstep.
 *
 * Reads business_profile + industry_insights and proposes 4 starter
 * dashboard cards per CXO seat selected in onboarding, grounded strictly
 * in facts named in the business_profile. No tools needed — the agent
 * generates JSON directly from the inline context.
 *
 * Critical grounding rule: cards must be measurable from the business_profile.
 * industry_insights is interpretation context only — it does NOT introduce new
 * product lines, markets, or segments. e.g. if the profile lists only mountain
 * and road bikes, do NOT propose e-bike metrics.
 */

import { resolveAgentBackend } from "./agent-backend-resolver";
import { parseJsonFromOutput } from "./hermes-runner";

export type BootstrapMetric = {
  role: string;
  slug: string;
  title: string;
  why: string;
  chart_hint: "kpi" | "line" | "bar" | "donut" | "area";
};

export type BootstrapMetricsProgress = (note: string) => void;

const VALID_CHARTS = new Set(["kpi", "line", "bar", "donut", "area"]);
const SLUG_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

const ROLE_FOCUS: Record<string, string> = {
  CEO: "company-wide health, channel mix, geographic mix, product concentration.",
  CFO: "margin, cost, AR/cash, FX/currency exposure if multi-currency.",
  COO: "production capacity, work center throughput, fulfillment, lead times, workforce productivity.",
  CRO: "sales rep performance, quota attainment, pipeline, channel sales mix, account health.",
  CMO: "customer acquisition, retention, segment performance, marketing ROI, brand reach in their actual markets.",
  CIO: "systems reliability, data freshness, integration health, infrastructure cost, security posture relative to their actual stack.",
  CPO: "product adoption, feature usage, activation and retention of their actual product lines, roadmap signal quality.",
};

function buildPrompt(args: {
  orgName: string;
  businessProfile: string;
  industryInsights: string;
  seats: string[];
}): string {
  const { orgName, businessProfile, industryInsights, seats } = args;
  const total = seats.length * 4;
  const perRoleList = seats.map((r) => `4 for ${r}`).join(", ");
  const focus = seats
    .map(
      (r) =>
        `- ${r}: ${ROLE_FOCUS[r] ?? "executive priorities relevant to this role for this specific company."}`,
    )
    .join("\n");
  const insightsBlock = industryInsights.trim()
    ? industryInsights
    : "(industry research is disabled for this org — work from business_profile alone)";

  return `You are a senior BI consultant designing the first dashboard a new customer sees. You are handed a business_profile (what the company actually does) and an industry_insights briefing (industry context). You must propose exactly ${total} starter dashboard cards: 4 per role for ${seats.join(", ")}.

NO TOOLS NEEDED. Do not call Bash, Read, Write, or any other tool — every fact you need is inline below. Generate the JSON directly as your final answer.

GROUNDING RULE — non-negotiable:
- Every card must measure something the company ACTUALLY does, sells, operates, or employs, as named in the business_profile (products, channels, geographies, workforce, currencies, work centers, sales territories, etc.).
- industry_insights is INTERPRETATION CONTEXT ONLY. It tells you what's normal in the industry. It does NOT give you license to invent product lines, markets, or segments the company is not in.
- Concrete negative example: if business_profile lists only mountain, road, and touring bikes, you MUST NOT propose e-bike metrics, battery metrics, or e-bike-related KPIs — even if industry_insights talks about e-bike growth. Same rule for any other industry trend the company isn't actually exposed to.
- If you can't ground a card in the business_profile, drop it and pick another.

Per-role focus:
${focus}

Field rules:
- role: must be exactly one of ${seats.join(", ")}.
- slug: kebab-case, lowercase, unique within its role, descriptive (e.g. "wholesale-vs-dtc-mix", "rep-quota-attainment", "wc-utilization"). Match /^[a-z0-9]+(-[a-z0-9]+)*$/.
- title: short, scannable, plain English (under 60 chars), no marketing fluff.
- why: one sentence explaining why this matters for THIS specific company, citing a concrete fact from the business profile (a number, a product name, a territory, a workforce count). No generic "important for executives" prose.
- chart_hint: one of "kpi" (single big number), "line" (trend), "bar" (categories), "donut" (mix/share), "area" (cumulative).

================================================================================
INPUT — companyName: ${orgName}
================================================================================

================================================================================
business_profile:
================================================================================

${businessProfile}

================================================================================
industry_insights:
================================================================================

${insightsBlock}

================================================================================
OUTPUT CONTRACT — respond with ONE JSON object, exactly this shape, no prose, no code fences:
================================================================================

{
  "metrics": [
    {
      "role": "CEO",
      "slug": "kebab-case-slug",
      "title": "Short title",
      "why": "One sentence citing a concrete fact from the business profile.",
      "chart_hint": "kpi"
    }
  ]
}

Rules for the JSON:
- Output a SINGLE JSON object. No markdown fences, no prose before or after.
- metrics array has exactly ${total} entries: ${perRoleList}.
- chart_hint must be one of: kpi, line, bar, donut, area.
- slug must be unique within its role.
`;
}

export async function runBootstrapMetricsWriter(args: {
  orgId: string;
  orgName: string;
  businessProfile: string;
  industryInsights: string;
  seats: string[];
  /** processing_job.id — tags Hermes's scratch dir for DB correlation. */
  jobId?: string;
  onProgress?: BootstrapMetricsProgress;
  /** Pipe backend stderr to the parent process. Test harness only. */
  debug?: boolean;
}): Promise<{ metrics: BootstrapMetric[] }> {
  const { orgId, orgName, businessProfile, industryInsights, seats, jobId, onProgress, debug } = args;

  if (seats.length === 0) {
    throw new Error("bootstrap-metrics: no active seats selected");
  }

  const backend = await resolveAgentBackend(orgId);
  console.log(`[bootstrap-metrics] org=${orgId} backend=${backend.id}`);

  const prompt = buildPrompt({ orgName, businessProfile, industryInsights, seats });

  onProgress?.("Generating bootstrap metrics");
  const startedAt = Date.now();
  const stdout = await backend.run({
    prompt,
    tag: jobId ?? orgId,
    debug: debug === true,
  });
  const elapsedSec = ((Date.now() - startedAt) / 1000).toFixed(0);

  const parsed = parseJsonFromOutput(stdout) as { metrics?: unknown };
  const metrics = validate(parsed.metrics, seats);
  console.log(
    `[bootstrap-metrics] org=${orgId} generated ${metrics.length} cards for seats ${seats.join(", ")} in ${elapsedSec}s`,
  );
  onProgress?.(`Generated ${metrics.length} cards`);
  return { metrics };
}

function validate(raw: unknown, seats: string[]): BootstrapMetric[] {
  if (!Array.isArray(raw)) {
    throw new Error(`bootstrap-metrics: expected array, got ${typeof raw}`);
  }
  const seatSet = new Set(seats);
  const metrics = raw as Partial<BootstrapMetric>[];
  const errs: string[] = [];
  const seenByRole = new Map<string, Set<string>>();
  const cleaned: BootstrapMetric[] = [];

  for (const [i, m] of metrics.entries()) {
    const where = `[${i}]`;
    if (!m || typeof m !== "object") {
      errs.push(`${where} not an object`);
      continue;
    }
    if (typeof m.role !== "string" || !seatSet.has(m.role)) {
      errs.push(`${where} bad role: ${m.role}`);
      continue;
    }
    if (typeof m.slug !== "string" || !SLUG_RE.test(m.slug)) {
      errs.push(`${where} bad slug: ${m.slug}`);
      continue;
    }
    if (typeof m.title !== "string" || !m.title.trim()) {
      errs.push(`${where} missing title`);
      continue;
    }
    if (typeof m.why !== "string" || !m.why.trim()) {
      errs.push(`${where} missing why`);
      continue;
    }
    if (!VALID_CHARTS.has(m.chart_hint as string)) {
      errs.push(`${where} bad chart_hint: ${m.chart_hint}`);
      continue;
    }
    const role = m.role;
    let bag = seenByRole.get(role);
    if (!bag) {
      bag = new Set();
      seenByRole.set(role, bag);
    }
    if (bag.has(m.slug)) {
      errs.push(`${where} duplicate slug ${m.slug} in role ${role}`);
      continue;
    }
    bag.add(m.slug);
    cleaned.push({
      role,
      slug: m.slug,
      title: m.title.trim(),
      why: m.why.trim(),
      chart_hint: m.chart_hint as BootstrapMetric["chart_hint"],
    });
  }

  for (const role of seats) {
    const n = seenByRole.get(role)?.size ?? 0;
    if (n !== 4) errs.push(`role ${role} has ${n} cards, expected 4`);
  }

  if (errs.length > 0) {
    throw new Error(
      `bootstrap-metrics validation failed:\n  ${errs.join("\n  ")}`,
    );
  }
  return cleaned;
}
