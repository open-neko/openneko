import { NextRequest, NextResponse } from "next/server";
import {
  action_request,
  and,
  db,
  desc,
  eq,
  observation,
  sql,
  workflow_definition,
  workflow_run,
} from "@neko/db";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

// Risk levels sort highest-urgency first; missing or unknown levels go last.
const RISK_ORDER: Record<string, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
};

export async function GET(request: NextRequest) {
  const orgId = await getOrgId();
  const countOnly =
    new URL(request.url).searchParams.get("countOnly") === "true";

  if (countOnly) {
    const [row] = await db()
      .select({ count: sql<number>`count(*)::int` })
      .from(action_request)
      .where(
        and(
          eq(action_request.org_id, orgId),
          eq(action_request.status, "pending_approval"),
        ),
      );
    return NextResponse.json({ count: row?.count ?? 0 });
  }

  const rows = await db()
    .select({
      // action_request fields
      id: action_request.id,
      workflowRunId: action_request.workflow_run_id,
      triggeredByObservationId: action_request.triggered_by_observation_id,
      kind: action_request.kind,
      target: action_request.target,
      payload: action_request.payload,
      riskLevel: action_request.risk_level,
      summary: action_request.summary,
      scope: action_request.scope,
      createdAt: action_request.created_at,
      // workflow_run fields
      runStartedAt: workflow_run.started_at,
      runCreatedAt: workflow_run.created_at,
      // workflow_definition fields
      workflowId: workflow_definition.id,
      workflowName: workflow_definition.name,
      // observation fields (nullable)
      observationTitle: observation.title,
    })
    .from(action_request)
    .innerJoin(workflow_run, eq(action_request.workflow_run_id, workflow_run.id))
    .innerJoin(
      workflow_definition,
      eq(workflow_run.workflow_id, workflow_definition.id),
    )
    .leftJoin(
      observation,
      eq(action_request.triggered_by_observation_id, observation.id),
    )
    .where(
      and(
        eq(action_request.org_id, orgId),
        eq(action_request.status, "pending_approval"),
      ),
    )
    .orderBy(desc(action_request.created_at));

  // Sort by risk level first (critical → low), then by createdAt desc.
  const sorted = [...rows].sort((a, b) => {
    const ra = RISK_ORDER[a.riskLevel ?? ""] ?? 99;
    const rb = RISK_ORDER[b.riskLevel ?? ""] ?? 99;
    if (ra !== rb) return ra - rb;
    return b.createdAt.getTime() - a.createdAt.getTime();
  });

  return NextResponse.json({
    approvals: sorted.map((r) => ({
      id: r.id,
      workflowRunId: r.workflowRunId,
      workflow: {
        id: r.workflowId,
        name: r.workflowName,
      },
      triggeredByObservation: r.observationTitle
        ? { title: r.observationTitle }
        : null,
      kind: r.kind,
      target: r.target,
      payload: r.payload,
      riskLevel: r.riskLevel,
      summary: r.summary,
      scope: r.scope,
      runAt: (r.runStartedAt ?? r.runCreatedAt).toISOString(),
      createdAt: r.createdAt.toISOString(),
    })),
    count: sorted.length,
  });
}
