import { NextRequest, NextResponse } from "next/server";
import {
  listActionRequests,
  type ActionRequestStatus,
} from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const VALID_STATUSES: ActionRequestStatus[] = [
  "draft",
  "pending_approval",
  "approved",
  "rejected",
  "expired",
  "executed",
  "failed",
  "cancelled",
];

export async function GET(req: NextRequest) {
  const orgId = await getOrgId();
  const url = new URL(req.url);
  const statusRaw = url.searchParams.get("status");
  const status =
    statusRaw && VALID_STATUSES.includes(statusRaw as ActionRequestStatus)
      ? (statusRaw as ActionRequestStatus)
      : undefined;
  const workflowRunId =
    url.searchParams.get("workflowRunId") ?? undefined;
  const limit = Math.min(
    Math.max(Number(url.searchParams.get("limit") ?? "100"), 1),
    500,
  );

  const rows = await listActionRequests({
    orgId,
    status,
    workflowRunId,
    limit,
  });

  return NextResponse.json({
    actionRequests: rows.map((r) => ({
      id: r.id,
      workflowRunId: r.workflowRunId,
      triggeredByObservationId: r.triggeredByObservationId,
      policyId: r.policyId,
      scope: r.scope,
      kind: r.kind,
      target: r.target,
      payload: r.payload,
      riskLevel: r.riskLevel,
      status: r.status,
      summary: r.summary,
      approvedByUserId: r.approvedByUserId,
      approvedAt: r.approvedAt?.toISOString() ?? null,
      rejectionReason: r.rejectionReason,
      createdAt: r.createdAt.toISOString(),
      updatedAt: r.updatedAt.toISOString(),
    })),
  });
}
