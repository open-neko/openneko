import { NextRequest, NextResponse } from "next/server";
import {
  action_policy,
  action_request,
  and,
  db,
  desc,
  eq,
  inArray,
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

type Filter = "awaiting" | "fired" | "rejected" | "all";

function parseFilter(value: string | null): Filter {
  if (value === "fired" || value === "rejected" || value === "all") return value;
  return "awaiting";
}

function statusesForFilter(filter: Filter): string[] | null {
  if (filter === "awaiting") return ["pending_approval"];
  if (filter === "fired") return ["executed", "approved"];
  if (filter === "rejected") return ["rejected", "failed"];
  return null; // all
}

export async function GET(request: NextRequest) {
  const orgId = await getOrgId();
  const sp = new URL(request.url).searchParams;
  const countOnly = sp.get("countOnly") === "true";
  const filter = parseFilter(sp.get("filter"));

  // The nav badge always tracks pending_approval count regardless of the
  // current view — the operator should always know how many actions need
  // their call.
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

  const statuses = statusesForFilter(filter);
  const statusCondition = statuses
    ? statuses.length === 1
      ? eq(action_request.status, statuses[0])
      : inArray(action_request.status, statuses)
    : undefined;

  const rows = await db()
    .select({
      id: action_request.id,
      workflowRunId: action_request.workflow_run_id,
      triggeredByObservationId: action_request.triggered_by_observation_id,
      kind: action_request.kind,
      target: action_request.target,
      payload: action_request.payload,
      riskLevel: action_request.risk_level,
      summary: action_request.summary,
      scope: action_request.scope,
      status: action_request.status,
      approvedAt: action_request.approved_at,
      approvedByUserId: action_request.approved_by_user_id,
      policyId: action_request.policy_id,
      rejectionReason: action_request.rejection_reason,
      createdAt: action_request.created_at,
      runStartedAt: workflow_run.started_at,
      runCreatedAt: workflow_run.created_at,
      workflowId: workflow_definition.id,
      workflowName: workflow_definition.name,
      observationTitle: observation.title,
      policyName: action_policy.name,
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
    .leftJoin(
      action_policy,
      eq(action_request.policy_id, action_policy.id),
    )
    .where(
      statusCondition
        ? and(eq(action_request.org_id, orgId), statusCondition)
        : eq(action_request.org_id, orgId),
    )
    .orderBy(desc(action_request.created_at))
    .limit(filter === "awaiting" ? 200 : 100);

  // For the awaiting tab, sort by risk first then time desc — the operator
  // wants critical at the top. Other tabs are time-ordered (already from SQL).
  const sorted =
    filter === "awaiting"
      ? [...rows].sort((a, b) => {
          const ra = RISK_ORDER[a.riskLevel ?? ""] ?? 99;
          const rb = RISK_ORDER[b.riskLevel ?? ""] ?? 99;
          if (ra !== rb) return ra - rb;
          return b.createdAt.getTime() - a.createdAt.getTime();
        })
      : rows;

  return NextResponse.json({
    actions: sorted.map((r) => ({
      id: r.id,
      workflowRunId: r.workflowRunId,
      workflow: { id: r.workflowId, name: r.workflowName },
      triggeredByObservation: r.observationTitle
        ? { title: r.observationTitle }
        : null,
      kind: r.kind,
      target: r.target,
      payload: r.payload,
      riskLevel: r.riskLevel,
      summary: r.summary,
      scope: r.scope,
      status: r.status,
      approvedAt: r.approvedAt?.toISOString() ?? null,
      approverKind: r.approvedByUserId
        ? ("operator" as const)
        : r.policyId
          ? ("policy" as const)
          : r.approvedAt
            ? ("auto" as const)
            : null,
      approverLabel: r.approvedByUserId ?? r.policyName ?? null,
      rejectionReason: r.rejectionReason,
      runAt: (r.runStartedAt ?? r.runCreatedAt).toISOString(),
      createdAt: r.createdAt.toISOString(),
    })),
    count: sorted.length,
    filter,
  });
}
