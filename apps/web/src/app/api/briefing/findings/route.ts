import { NextResponse } from "next/server";
import {
  action_request,
  and,
  briefing,
  db,
  desc,
  eq,
  gte,
  sql,
  workflow_definition,
  workflow_output,
  workflow_run,
} from "@neko/db";
import { getOrgId } from "@/lib/db";

// 5-minute freshness window for the auto-generated live summary. If a row
// exists newer than this, reuse it. Otherwise compose a fresh template.
const SUMMARY_FRESHNESS_MS = 5 * 60 * 1000;

function composeSummary(args: {
  pendingApprovals: number;
  actFindings: number;
  watchFindings: number;
  goodRuns: number;
  windowHours: number;
}): string {
  const parts: string[] = [];

  if (args.actFindings > 0) {
    parts.push(
      args.actFindings === 1
        ? "One finding wants your attention."
        : `${args.actFindings} findings want your attention.`,
    );
  }

  if (args.pendingApprovals > 0) {
    parts.push(
      args.pendingApprovals === 1
        ? "One approval is queued."
        : `${args.pendingApprovals} approvals are queued.`,
    );
  }

  if (args.watchFindings > 0) {
    parts.push(
      args.watchFindings === 1
        ? "One thing is worth a look."
        : `${args.watchFindings} things are worth a look.`,
    );
  }

  if (args.goodRuns > 0 && parts.length > 0) {
    parts.push(
      args.goodRuns === 1
        ? "One other workflow ran cleanly."
        : `${args.goodRuns} other workflows ran cleanly.`,
    );
  } else if (args.goodRuns > 0) {
    parts.push(
      args.goodRuns === 1
        ? `One workflow ran cleanly in the last ${args.windowHours}h.`
        : `${args.goodRuns} workflows ran cleanly in the last ${args.windowHours}h.`,
    );
  }

  if (parts.length === 0) {
    return "Quiet shift — no workflows have run yet today.";
  }

  return parts.join(" ");
}

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

// Briefing tributaries: pending approvals, mood=act findings, live summary.
// KPI cards continue to be served by the existing /api/briefing route.

const RISK_ORDER: Record<string, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
};

const RECENT_FINDING_WINDOW_HOURS = 24;
const ACT_LIMIT = 8;
const APPROVAL_LIMIT = 8;

export async function GET() {
  const orgId = await getOrgId();

  // 1. Pending approvals — sorted risk desc, time desc.
  const approvalsRaw = await db()
    .select({
      id: action_request.id,
      workflowRunId: action_request.workflow_run_id,
      kind: action_request.kind,
      target: action_request.target,
      summary: action_request.summary,
      riskLevel: action_request.risk_level,
      createdAt: action_request.created_at,
      workflowId: workflow_definition.id,
      workflowName: workflow_definition.name,
    })
    .from(action_request)
    .innerJoin(workflow_run, eq(action_request.workflow_run_id, workflow_run.id))
    .innerJoin(
      workflow_definition,
      eq(workflow_run.workflow_id, workflow_definition.id),
    )
    .where(
      and(
        eq(action_request.org_id, orgId),
        eq(action_request.status, "pending_approval"),
      ),
    )
    .orderBy(desc(action_request.created_at))
    .limit(APPROVAL_LIMIT);

  const approvals = approvalsRaw.sort((a, b) => {
    const ra = RISK_ORDER[a.riskLevel ?? ""] ?? 99;
    const rb = RISK_ORDER[b.riskLevel ?? ""] ?? 99;
    if (ra !== rb) return ra - rb;
    return b.createdAt.getTime() - a.createdAt.getTime();
  });

  // 2. Recent mood=act findings — workflow_outputs from the last N hours.
  const since = new Date(Date.now() - RECENT_FINDING_WINDOW_HOURS * 3600 * 1000);
  const actRaw = await db()
    .select({
      id: workflow_output.id,
      workflowRunId: workflow_output.workflow_run_id,
      kind: workflow_output.kind,
      title: workflow_output.title,
      body: workflow_output.body,
      scope: workflow_output.scope,
      mood: workflow_output.mood,
      createdAt: workflow_output.created_at,
      workflowId: workflow_definition.id,
      workflowName: workflow_definition.name,
    })
    .from(workflow_output)
    .innerJoin(workflow_run, eq(workflow_output.workflow_run_id, workflow_run.id))
    .innerJoin(
      workflow_definition,
      eq(workflow_run.workflow_id, workflow_definition.id),
    )
    .where(
      and(
        eq(workflow_output.org_id, orgId),
        eq(workflow_output.mood, "act"),
        gte(workflow_output.created_at, since),
      ),
    )
    .orderBy(desc(workflow_output.created_at))
    .limit(ACT_LIMIT);

  // 3. mood=watch findings (Worth knowing) — same window, lower urgency.
  const watchRaw = await db()
    .select({
      id: workflow_output.id,
      workflowRunId: workflow_output.workflow_run_id,
      kind: workflow_output.kind,
      title: workflow_output.title,
      body: workflow_output.body,
      scope: workflow_output.scope,
      mood: workflow_output.mood,
      createdAt: workflow_output.created_at,
      workflowId: workflow_definition.id,
      workflowName: workflow_definition.name,
    })
    .from(workflow_output)
    .innerJoin(workflow_run, eq(workflow_output.workflow_run_id, workflow_run.id))
    .innerJoin(
      workflow_definition,
      eq(workflow_run.workflow_id, workflow_definition.id),
    )
    .where(
      and(
        eq(workflow_output.org_id, orgId),
        eq(workflow_output.mood, "watch"),
        gte(workflow_output.created_at, since),
      ),
    )
    .orderBy(desc(workflow_output.created_at))
    .limit(ACT_LIMIT);

  // 4. Roll-up count for quiet section (mood=good in window).
  const [goodCountRow] = await db()
    .select({ count: sql<number>`count(*)::int` })
    .from(workflow_output)
    .where(
      and(
        eq(workflow_output.org_id, orgId),
        eq(workflow_output.mood, "good"),
        gte(workflow_output.created_at, since),
      ),
    );

  // 5. Live summary paragraph — auto-generated template, v1 fallback before
  // the dedicated internal workflow is seeded. Cached for 5 minutes; refreshed
  // on stale read or when activity shifts. Stored under role="_summary" so
  // it's distinct from the legacy role-keyed daily briefings.
  const SUMMARY_ROLE = "_summary";
  const [existingSummary] = await db()
    .select({
      id: briefing.id,
      summaryMd: briefing.summary_md,
      createdAt: briefing.created_at,
      forDate: briefing.for_date,
    })
    .from(briefing)
    .where(and(eq(briefing.org_id, orgId), eq(briefing.role, SUMMARY_ROLE)))
    .orderBy(desc(briefing.created_at))
    .limit(1);

  let latestSummary = existingSummary ?? null;
  const isStale =
    !latestSummary ||
    Date.now() - latestSummary.createdAt.getTime() > SUMMARY_FRESHNESS_MS;

  if (isStale) {
    const summary = composeSummary({
      pendingApprovals: approvals.length,
      actFindings: actRaw.length,
      watchFindings: watchRaw.length,
      goodRuns: goodCountRow?.count ?? 0,
      windowHours: RECENT_FINDING_WINDOW_HOURS,
    });

    const today = new Date().toISOString().slice(0, 10);

    try {
      // Upsert: one row per (org, _summary, date). In-place updates keep
      // the row count small over time.
      const [upserted] = await db()
        .insert(briefing)
        .values({
          org_id: orgId,
          role: SUMMARY_ROLE,
          for_date: today,
          profile_version: 1,
          summary_md: summary,
          insights: [],
        })
        .onConflictDoUpdate({
          target: [briefing.org_id, briefing.role, briefing.for_date],
          set: { summary_md: summary, updated_at: new Date() },
        })
        .returning({
          id: briefing.id,
          summaryMd: briefing.summary_md,
          createdAt: briefing.created_at,
          forDate: briefing.for_date,
        });
      if (upserted) latestSummary = upserted;
    } catch (err) {
      console.warn("[briefing/findings] summary upsert failed:", err);
    }
  }

  return NextResponse.json({
    summary: latestSummary
      ? {
          id: latestSummary.id,
          summaryMd: latestSummary.summaryMd,
          createdAt: latestSummary.createdAt.toISOString(),
        }
      : null,
    awaitingYou: {
      approvals: approvals.map((a) => ({
        id: a.id,
        kind: "approval" as const,
        workflowRunId: a.workflowRunId,
        workflow: { id: a.workflowId, name: a.workflowName },
        title: a.summary || a.kind,
        target: a.target,
        riskLevel: a.riskLevel,
        createdAt: a.createdAt.toISOString(),
      })),
      actFindings: actRaw.map((o) => ({
        id: o.id,
        kind: "finding" as const,
        workflowRunId: o.workflowRunId,
        workflow: { id: o.workflowId, name: o.workflowName },
        title: o.title,
        body: o.body,
        scope: o.scope,
        mood: o.mood,
        outputKind: o.kind,
        createdAt: o.createdAt.toISOString(),
      })),
    },
    worthKnowing: watchRaw.map((o) => ({
      id: o.id,
      kind: "finding" as const,
      workflowRunId: o.workflowRunId,
      workflow: { id: o.workflowId, name: o.workflowName },
      title: o.title,
      body: o.body,
      scope: o.scope,
      mood: o.mood,
      outputKind: o.kind,
      createdAt: o.createdAt.toISOString(),
    })),
    quiet: {
      goodOutputs: goodCountRow?.count ?? 0,
      windowHours: RECENT_FINDING_WINDOW_HOURS,
    },
  });
}
