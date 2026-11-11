import { NextResponse } from "next/server";
import { enqueue, QUEUE } from "@neko/db/jobs";
import {
  approveActionRequest,
  getActionRequest,
  InvalidActionStatusTransitionError,
  rejectActionRequest,
  retryActionRequest,
  UnsafeActionRetryError,
} from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";
import { actionRecordVisibility } from "@/lib/entitlements";
import { getCurrentActor } from "@/lib/actor";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ actionRequestId: string }>;
};

export async function PATCH(req: Request, context: RouteContext) {
  const { actionRequestId } = await context.params;
  const body = await req.json().catch(() => ({}));
  const decision = body.decision as string | undefined;
  if (decision !== "approve" && decision !== "reject" && decision !== "retry") {
    return NextResponse.json(
      { error: "decision must be 'approve', 'reject', or 'retry'" },
      { status: 400 },
    );
  }
  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  const existing = await getActionRequest(orgId, actionRequestId);
  if (
    !existing ||
    (existing.kind === "source_config_admin" && actor.role !== "admin") ||
    !(await actionRecordVisibility([existing]))(existing)
  ) {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
  // Attribution is session-derived; never trust a caller-supplied user id.
  const approverUserId = actor.userId;
  const reason = typeof body.reason === "string" ? body.reason : undefined;

  try {
    if (decision === "approve") {
      const approved = await approveActionRequest({
        approver: actor,
        id: actionRequestId,
        orgId,
        approverUserId,
      });
      await enqueue(QUEUE.ACTION_EXECUTE, {
        orgId,
        actionRequestId: approved.id,
      });
      return NextResponse.json({ actionRequest: { id: approved.id, status: approved.status } });
    } else if (decision === "retry") {
      const retried = await retryActionRequest({
        id: actionRequestId,
        orgId,
        actor,
      });
      await enqueue(QUEUE.ACTION_EXECUTE, { orgId, actionRequestId: retried.id });
      return NextResponse.json({ actionRequest: { id: retried.id, status: retried.status } });
    } else {
      const rejected = await rejectActionRequest({
        approver: actor,
        id: actionRequestId,
        orgId,
        approverUserId,
        reason,
      });
      return NextResponse.json({ actionRequest: { id: rejected.id, status: rejected.status } });
    }
  } catch (e) {
    if (e instanceof InvalidActionStatusTransitionError || e instanceof UnsafeActionRetryError) {
      return NextResponse.json({ error: e.message }, { status: 409 });
    }
    throw e;
  }
}
