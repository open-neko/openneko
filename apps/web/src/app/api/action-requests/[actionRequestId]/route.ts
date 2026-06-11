import { NextResponse } from "next/server";
import {
  action_policy,
  and,
  db,
  eq,
  workflow_definition,
  workflow_output,
  workflow_run,
} from "@neko/db";
import { enqueue, QUEUE } from "@neko/db/jobs";
import {
  approveActionRequest,
  getActionRequest,
  InvalidActionStatusTransitionError,
  listActionExecutions,
  rejectActionRequest,
} from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";
import { getCurrentActor } from "@/lib/actor";

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

  let workflow: { id: string; name: string } | null = null;
  if (request.workflowRunId) {
    const rows = await db()
      .select({
        id: workflow_definition.id,
        name: workflow_definition.name,
      })
      .from(workflow_run)
      .innerJoin(
        workflow_definition,
        eq(workflow_run.workflow_id, workflow_definition.id),
      )
      .where(
        and(
          eq(workflow_run.org_id, orgId),
          eq(workflow_run.id, request.workflowRunId),
        ),
      )
      .limit(1);
    workflow = rows[0] ?? null;
  }

  let policy: { id: string; name: string; mode: string } | null = null;
  if (request.policyId) {
    const rows = await db()
      .select({
        id: action_policy.id,
        name: action_policy.name,
        mode: action_policy.mode,
      })
      .from(action_policy)
      .where(
        and(
          eq(action_policy.org_id, orgId),
          eq(action_policy.id, request.policyId),
        ),
      )
      .limit(1);
    policy = rows[0] ?? null;
  }

  let upstreamOutput:
    | { id: string; title: string; workflowRunId: string | null }
    | null = null;
  if (request.triggeredByObservationId) {
    const rows = await db()
      .select({
        id: workflow_output.id,
        title: workflow_output.title,
        workflowRunId: workflow_output.workflow_run_id,
      })
      .from(workflow_output)
      .where(
        and(
          eq(workflow_output.org_id, orgId),
          eq(workflow_output.id, request.triggeredByObservationId),
        ),
      )
      .limit(1);
    upstreamOutput = rows[0] ?? null;
  }

  const approverKind: "operator" | "policy" | "auto" | null =
    request.approvedByUserId
      ? "operator"
      : request.policyId
        ? "policy"
        : request.approvedAt
          ? "auto"
          : null;

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
    workflow,
    policy,
    upstreamOutput,
    approverKind,
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
      approver: await getCurrentActor(),
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
      approver: await getCurrentActor(),
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
