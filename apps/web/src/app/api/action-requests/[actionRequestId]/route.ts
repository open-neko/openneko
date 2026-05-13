import { NextResponse } from "next/server";
import { enqueue, QUEUE } from "@neko/db/jobs";
import {
  approveActionRequest,
  getActionRequest,
  InvalidActionStatusTransitionError,
  listActionExecutions,
  rejectActionRequest,
} from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ actionRequestId: string }>;
};

export async function GET(_req: Request, context: RouteContext) {
  const { actionRequestId } = await context.params;
  const orgId = await getOrgId();
  const request = await getActionRequest(orgId, actionRequestId);
  if (!request) {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
  const executions = await listActionExecutions(actionRequestId);
  return NextResponse.json({
    actionRequest: {
      id: request.id,
      workflowRunId: request.workflowRunId,
      triggeredByObservationId: request.triggeredByObservationId,
      policyId: request.policyId,
      scope: request.scope,
      kind: request.kind,
      target: request.target,
      payload: request.payload,
      riskLevel: request.riskLevel,
      status: request.status,
      summary: request.summary,
      approvedByUserId: request.approvedByUserId,
      approvedAt: request.approvedAt?.toISOString() ?? null,
      rejectionReason: request.rejectionReason,
      createdAt: request.createdAt.toISOString(),
      updatedAt: request.updatedAt.toISOString(),
    },
    executions: executions.map((e) => ({
      id: e.id,
      executor: e.executor,
      commandOrOperation: e.commandOrOperation,
      payload: e.payload,
      result: e.result,
      externalRef: e.externalRef,
      status: e.status,
      error: e.error,
      startedAt: e.startedAt?.toISOString() ?? null,
      finishedAt: e.finishedAt?.toISOString() ?? null,
      createdAt: e.createdAt.toISOString(),
    })),
  });
}

export async function PATCH(req: Request, context: RouteContext) {
  const { actionRequestId } = await context.params;
  const body = await req.json().catch(() => ({}));
  const decision = body.decision as string | undefined;
  if (decision !== "approve" && decision !== "reject") {
    return NextResponse.json(
      { error: "decision must be 'approve' or 'reject'" },
      { status: 400 },
    );
  }
  const orgId = await getOrgId();
  const approverUserId =
    typeof body.approverUserId === "string" ? body.approverUserId : null;
  const reason = typeof body.reason === "string" ? body.reason : undefined;

  try {
    if (decision === "approve") {
      const approved = await approveActionRequest({
        id: actionRequestId,
        orgId,
        approverUserId,
      });
      await enqueue(QUEUE.ACTION_EXECUTE, {
        orgId,
        actionRequestId: approved.id,
      });
      return NextResponse.json({ actionRequest: { id: approved.id, status: approved.status } });
    } else {
      const rejected = await rejectActionRequest({
        id: actionRequestId,
        orgId,
        approverUserId,
        reason,
      });
      return NextResponse.json({ actionRequest: { id: rejected.id, status: rejected.status } });
    }
  } catch (e) {
    if (e instanceof InvalidActionStatusTransitionError) {
      return NextResponse.json({ error: e.message }, { status: 409 });
    }
    throw e;
  }
}
