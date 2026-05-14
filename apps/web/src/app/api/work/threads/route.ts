import { NextRequest, NextResponse } from "next/server";
import {
  action_request,
  and,
  db,
  desc,
  eq,
  metric,
  metric_snapshot,
  workflow_definition,
  workflow_output,
  workflow_run,
} from "@neko/db";
import { BRIEFING_CARD_SENTINEL } from "@/lib/briefing-card-context";
import { getOrgId } from "@/lib/db";
import {
  createWorkMessage,
  createWorkThread,
  listWorkThreads,
} from "@/lib/work-store";

export async function GET() {
  const threads = await listWorkThreads(await getOrgId());
  return NextResponse.json({ threads });
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const title = typeof body.title === "string" ? body.title.trim() : "";
  const seedMetricId =
    typeof body.seedMetricId === "string" && body.seedMetricId.length > 0
      ? body.seedMetricId
      : null;
  const seedWorkflowRunId =
    typeof body.seedWorkflowRunId === "string" &&
    body.seedWorkflowRunId.length > 0
      ? body.seedWorkflowRunId
      : null;

  const orgId = await getOrgId();

  // Resolve title from seed when possible — feels less arbitrary than
  // "Untitled thread" when the operator just clicked "Deep dive" or
  // "Ask a follow-up."
  let resolvedTitle = title;
  let runContext: string | null = null;
  let seedCard: { message: string; title: string } | null = null;
  if (seedWorkflowRunId) {
    const seed = await loadWorkflowRunContext(orgId, seedWorkflowRunId);
    if (seed) {
      runContext = seed.message;
      if (!resolvedTitle) resolvedTitle = `Follow-up · ${seed.workflowName}`;
    }
  }
  if (seedMetricId) {
    seedCard = await loadBriefingCardForSeed(orgId, seedMetricId);
    if (seedCard && !resolvedTitle) resolvedTitle = seedCard.title;
  }

  const thread = await createWorkThread(orgId, resolvedTitle);

  // When the dashboard's "Deep dive" action opens a new thread, the briefing
  // card travels into the thread as the opening user message — that way the
  // agent picks it up from the normal conversation history (getWorkThreadBundle)
  // with no extra plumbing, and reloads/new sessions still see it.
  if (seedCard) {
    await createWorkMessage({
      orgId,
      threadId: thread.id,
      runId: null,
      role: "user",
      content: seedCard.message,
    });
  }

  if (runContext) {
    await createWorkMessage({
      orgId,
      threadId: thread.id,
      runId: null,
      role: "user",
      content: runContext,
    });
  }

  return NextResponse.json({
    thread: {
      id: thread.id,
      title: thread.title || "Untitled thread",
      createdAt: thread.created_at.toISOString(),
      updatedAt: thread.updated_at.toISOString(),
      lastMessageAt: thread.last_message_at.toISOString(),
    },
  });
}

/**
 * Compose the context message that seeds a follow-up Ask thread. Plain
 * prose so the operator can read it back in the bubble; the agent picks
 * it up from normal thread history.
 */
async function loadWorkflowRunContext(
  orgId: string,
  workflowRunId: string,
): Promise<{ workflowName: string; message: string } | null> {
  const runRows = await db()
    .select({
      id: workflow_run.id,
      workflowId: workflow_run.workflow_id,
      triggerKind: workflow_run.trigger_kind,
      status: workflow_run.status,
      startedAt: workflow_run.started_at,
      finishedAt: workflow_run.finished_at,
      createdAt: workflow_run.created_at,
      summary: workflow_run.summary,
    })
    .from(workflow_run)
    .where(
      and(
        eq(workflow_run.org_id, orgId),
        eq(workflow_run.id, workflowRunId),
      ),
    )
    .limit(1);
  const run = runRows[0];
  if (!run) return null;

  const wfRows = await db()
    .select({
      id: workflow_definition.id,
      name: workflow_definition.name,
      goal: workflow_definition.goal,
    })
    .from(workflow_definition)
    .where(
      and(
        eq(workflow_definition.org_id, orgId),
        eq(workflow_definition.id, run.workflowId),
      ),
    )
    .limit(1);
  const wf = wfRows[0];
  if (!wf) return null;

  const outputs = await db()
    .select({
      title: workflow_output.title,
      body: workflow_output.body,
      mood: workflow_output.mood,
      scope: workflow_output.scope,
      kind: workflow_output.kind,
    })
    .from(workflow_output)
    .where(eq(workflow_output.workflow_run_id, workflowRunId))
    .orderBy(desc(workflow_output.created_at));

  const actions = await db()
    .select({
      kind: action_request.kind,
      target: action_request.target,
      status: action_request.status,
      summary: action_request.summary,
      riskLevel: action_request.risk_level,
    })
    .from(action_request)
    .where(
      and(
        eq(action_request.org_id, orgId),
        eq(action_request.workflow_run_id, workflowRunId),
      ),
    )
    .orderBy(desc(action_request.created_at));

  const when =
    (run.startedAt ?? run.createdAt)?.toLocaleString("en-IN", {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
      hour12: true,
    }) ?? "";

  const lines: string[] = [
    `I want to follow up on a workflow run.`,
    ``,
    `Workflow: ${wf.name}`,
    `Run: ${run.status} · ${run.triggerKind} · ${when}`,
  ];
  if (wf.goal) lines.push(`Goal: ${wf.goal}`);

  if (outputs.length > 0) {
    lines.push(``, `Outputs produced:`);
    for (const o of outputs) {
      const mood = o.mood ? ` [${o.mood}]` : "";
      const scope = o.scope ? ` (scope ${o.scope})` : "";
      lines.push(`- ${o.title}${mood}${scope}`);
      if (o.body) lines.push(`  ${o.body.replace(/\s+/g, " ").slice(0, 400)}`);
    }
  }

  if (actions.length > 0) {
    lines.push(``, `Actions proposed:`);
    for (const a of actions) {
      const risk = a.riskLevel ? ` [risk ${a.riskLevel}]` : "";
      const target = a.target ? ` → ${a.target}` : "";
      lines.push(
        `- ${a.kind}${target}${risk} (${a.status}) — ${a.summary ?? ""}`,
      );
    }
  }

  lines.push(``, `(I'll ask my question below.)`);

  return { workflowName: wf.name, message: lines.join("\n") };
}

/**
 * Resolve a metric + its latest snapshot into the work_message content used
 * to seed a deep-dive thread: BRIEFING_CARD_SENTINEL followed by the full
 * BriefingCardData payload as JSON. Returns null when the metric is missing
 * or belongs to a different org so the caller can silently skip the seed.
 * Exported for the route's integration tests.
 */
export async function loadBriefingCardForSeed(
  orgId: string,
  metricId: string,
): Promise<{ message: string; title: string } | null> {
  const metricRows = await db()
    .select({
      id: metric.id,
      title: metric.title,
      source: metric.source,
      chart_hint: metric.chart_hint,
    })
    .from(metric)
    .where(and(eq(metric.id, metricId), eq(metric.org_id, orgId)))
    .limit(1);
  const m = metricRows[0];
  if (!m) return null;

  const snapRows = await db()
    .select({ status: metric_snapshot.status, payload: metric_snapshot.payload })
    .from(metric_snapshot)
    .where(eq(metric_snapshot.metric_id, m.id))
    .orderBy(desc(metric_snapshot.captured_at))
    .limit(1);
  const snap = snapRows[0];
  const p = (snap?.payload as {
    mood?: string;
    headlineMetric?: string;
    headlineLabel?: string;
    insightText?: string;
    detailText?: string;
    chartType?: string;
    chartData?: Array<{ d: string; v: number; t?: number }>;
  } | null) ?? null;

  const card = {
    id: `seed-${m.id}`,
    metricId: m.id,
    source: m.source ?? "briefing",
    state: "ok" as const,
    mood: p?.mood ?? snap?.status ?? "good",
    text: m.title,
    metric: p?.headlineMetric ?? "",
    label: p?.headlineLabel ?? "",
    detail: [p?.insightText, p?.detailText].filter(Boolean).join(" "),
    chart: p?.chartType ?? m.chart_hint ?? "kpi",
    chartData: p?.chartData ?? [],
  };

  return {
    message: `${BRIEFING_CARD_SENTINEL}${JSON.stringify(card)}`,
    title: m.title,
  };
}
