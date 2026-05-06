import {
  and,
  db,
  eq,
  metric,
  metric_snapshot,
  processing_job,
} from "@neko/db";
import { updateProgress } from "../progress.js";
import {
  resolveAgentBackendId,
  runMetricAgent,
  type MetricAgentInput,
  type MetricAgentResult,
} from "@neko/llm";
import { acquireAgentSlot } from "../agent-concurrency.js";

/**
 * metric_refresh job — runs the metric agent for ONE card and writes the
 * result to metric_snapshot. Two entry paths:
 *
 *   1. Bootstrap card: trigger_payload.metricId → load card from metric table.
 *   2. Ad-hoc chat question: trigger_payload.question + inline metadata
 *      (slug, title, why, chartHint, role). Creates a metric row with
 *      source='chat', active=false, then writes the snapshot against it.
 */
export async function runMetricRefresh(jobId: string, orgId: string) {
  await updateProgress(jobId, "Loading card");

  const jobRows = await db()
    .select({ trigger_payload: processing_job.trigger_payload })
    .from(processing_job)
    .where(eq(processing_job.id, jobId))
    .limit(1);
  const payload = jobRows[0]?.trigger_payload as
    | {
        metricId?: string;
        question?: string;
        slug?: string;
        title?: string;
        why?: string;
        chartHint?: string;
        role?: string;
      }
    | null
    | undefined;
  if (!payload) {
    throw new Error(`metric_refresh job ${jobId} has no trigger_payload`);
  }

  let metricRowId: string;
  let input: MetricAgentInput;

  if (payload.metricId) {
    // Path 1: bootstrap card — load from metric table.
    const cards = await db()
      .select({
        id: metric.id,
        role: metric.role,
        slug: metric.slug,
        title: metric.title,
        why: metric.why,
        chart_hint: metric.chart_hint,
      })
      .from(metric)
      .where(eq(metric.id, payload.metricId))
      .limit(1);
    const card = cards[0];
    if (!card) throw new Error(`metric ${payload.metricId} not found`);
    metricRowId = card.id;
    input = {
      orgId,
      role: card.role as MetricAgentInput["role"],
      slug: card.slug,
      title: card.title,
      why: card.why ?? card.title,
      chartHint: (card.chart_hint ?? "line") as MetricAgentInput["chartHint"],
    };
  } else if (payload.question) {
    // Path 2: ad-hoc chat question.
    const slug = payload.slug || `chat-${Date.now()}`;
    const role = (payload.role ?? "CEO") as MetricAgentInput["role"];
    const title = payload.title || payload.question.slice(0, 60);
    const why = payload.why || payload.question;
    const chartHint = (payload.chartHint ?? "bar") as MetricAgentInput["chartHint"];

    const existingRows = await db()
      .select({ id: metric.id })
      .from(metric)
      .where(
        and(
          eq(metric.org_id, orgId),
          eq(metric.role, role),
          eq(metric.slug, slug),
        ),
      )
      .limit(1);
    const existingId = existingRows[0]?.id;

    if (existingId) {
      metricRowId = existingId;
      // Re-link to the current job + reset refresh status. The status endpoint
      // locates the snapshot via metric.created_by_job, so without this update
      // a rerun (or any re-ask that hits the same slug) leaves the row pointing
      // at the first job's id, and the status route returns payload=null. The
      // last_refresh_status reset moves the card back to "pending" so the
      // dashboard re-skeletons while the new run is in flight.
      await db()
        .update(metric)
        .set({
          created_by_job: jobId,
          last_refresh_status: "pending",
          last_refresh_error: null,
          last_refresh_job_id: jobId,
        })
        .where(eq(metric.id, existingId));
    } else {
      const ins = await db()
        .insert(metric)
        .values({
          org_id: orgId,
          role,
          slug,
          source: "chat",
          title,
          why,
          chart_hint: chartHint,
          active: false,
          created_by_job: jobId,
          last_refresh_status: "pending",
          last_refresh_job_id: jobId,
        })
        .returning({ id: metric.id });
      const newId = ins[0]?.id;
      if (!newId) throw new Error("failed to insert chat metric row");
      metricRowId = newId;
    }

    input = { orgId, role, slug, title, why, chartHint };
  } else {
    throw new Error(
      `metric_refresh job ${jobId} has neither metricId nor question in trigger_payload`,
    );
  }

  await updateProgress(jobId, "Running agent");
  const backendId = await resolveAgentBackendId(orgId);

  // Stamp metric.last_refresh_status on BOTH the success and failure paths
  // so the briefing API can render a Retry button without joining
  // processing_job (which only knows the job id, not the metric id). The
  // outer makeHandler still marks processing_job.status='failed' on rethrow.
  try {
    const release = await acquireAgentSlot(backendId);
    let result: MetricAgentResult;
    try {
      result = await runMetricAgent({ ...input, jobId });
    } finally {
      release();
    }

    const validationError = validateResult(result);
    if (validationError) {
      throw new Error(`metric agent output invalid: ${validationError}`);
    }

    await updateProgress(jobId, "Saving snapshot");

    await db().insert(metric_snapshot).values({
      metric_id: metricRowId,
      status: result.mood,
      payload: result,
    });

    await db()
      .update(metric)
      .set({
        last_refresh_status: "ok",
        last_refresh_error: null,
        last_refresh_job_id: jobId,
        updated_at: new Date(),
      })
      .where(eq(metric.id, metricRowId));

    console.log(
      `[metric_refresh] org=${orgId} metric=${input.slug} mood=${result.mood} "${result.headlineMetric}"`,
    );
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    await db()
      .update(metric)
      .set({
        last_refresh_status: "failed",
        last_refresh_error: msg.slice(0, 500),
        last_refresh_job_id: jobId,
        updated_at: new Date(),
      })
      .where(eq(metric.id, metricRowId));
    throw e;
  }
}

const VALID_MOODS = new Set(["good", "watch", "bad"]);
const VALID_CHART_TYPES = new Set(["kpi", "line", "bar", "donut", "area"]);
// Headline strings that mean "the agent failed to fetch real data and is
// narrating the failure." Treat them as a job failure — the worker marks
// the job failed and the briefing UI surfaces a Retry button for the card.
// Without this gate, the result row goes into metric_snapshot with
// `headlineMetric: "Error"` and the dashboard renders the error narrative
// as if it were a metric.
const FAIL_SENTINELS = new Set([
  "error",
  "errors",
  "n/a",
  "na",
  "unavailable",
  "data unavailable",
  "data unavilable", // tolerate the agent's typo seen in the wild
  "no data",
  "null",
  "undefined",
  "—",
  "-",
  "?",
  "tbd",
]);
const ALL_PUNCT_RE = /^[\s\-—?_.]+$/;
const VALID_TIME_GRAINS = new Set([
  "day",
  "week",
  "month",
  "quarter",
  "year",
  "all_time",
  "snapshot",
]);
const ISO_DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

export function validateResult(r: MetricAgentResult): string | null {
  if (!r.headlineMetric || !r.headlineMetric.trim()) return "empty headlineMetric";
  const headlineNorm = r.headlineMetric.trim().toLowerCase();
  if (FAIL_SENTINELS.has(headlineNorm) || ALL_PUNCT_RE.test(headlineNorm)) {
    return `agent emitted error sentinel as headlineMetric: "${r.headlineMetric}"`;
  }
  if (!r.headlineLabel) return "empty headlineLabel";
  if (!r.insightText) return "empty insightText";
  if (!VALID_MOODS.has(r.mood)) return `invalid mood: ${r.mood}`;
  if (!VALID_CHART_TYPES.has(r.chartType)) return `invalid chartType: ${r.chartType}`;
  if (!Array.isArray(r.chartData) || r.chartData.length === 0) return "empty chartData";
  for (const item of r.chartData) {
    if (typeof item.v !== "number" || isNaN(item.v)) return "chartData item has non-numeric v";
    if (!item.d) return "chartData item missing d label";
  }
  if (r.chartType === "kpi" && r.chartData.length !== 1) {
    return `kpi chartType requires exactly 1 chartData item, got ${r.chartData.length}`;
  }
  if (r.chartType === "kpi" && r.chartData[0].t == null) {
    return "kpi chartType requires a baseline (t) value";
  }

  // Time window — required on every snapshot.
  if (!r.timeWindow) return "missing timeWindow";
  if (!VALID_TIME_GRAINS.has(r.timeWindow.grain)) {
    return `invalid timeWindow.grain: ${r.timeWindow.grain}`;
  }
  if (!r.timeWindow.label) return "empty timeWindow.label";
  if (r.timeWindow.grain !== "all_time") {
    if (!r.timeWindow.start || !ISO_DATE_RE.test(r.timeWindow.start)) {
      return `timeWindow.start must be yyyy-mm-dd (got "${r.timeWindow.start}")`;
    }
    if (!r.timeWindow.end || !ISO_DATE_RE.test(r.timeWindow.end)) {
      return `timeWindow.end must be yyyy-mm-dd (got "${r.timeWindow.end}")`;
    }
  }
  return null;
}
