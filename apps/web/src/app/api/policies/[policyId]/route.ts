import { NextResponse } from "next/server";
import { getActionPolicy, updateActionPolicy } from "@neko/llm/workflows";
import { and, db, eq, user_group } from "@neko/db";
import { getOrgId } from "@/lib/db";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ policyId: string }>;
};

export async function GET(_: Request, context: RouteContext) {
  const allowed = await requireAdminActor();
  if (isDenied(allowed)) return allowed;
  const { policyId } = await context.params;
  const orgId = await getOrgId();
  const policy = await getActionPolicy(orgId, policyId);
  if (!policy) {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
  return NextResponse.json({
    policy: {
      id: policy.id,
      name: policy.name,
      description: policy.description,
      appliesToKinds: policy.appliesToKinds,
      appliesToScopes: policy.appliesToScopes,
      mode: policy.mode,
      riskThresholdAutoApprove: policy.riskThresholdAutoApprove,
      allowedTargets: policy.allowedTargets,
      deniedTargets: policy.deniedTargets,
      limits: policy.limits,
      approverRole: policy.approverRole,
      approverGroupId: policy.approverGroupId,
      priority: policy.priority,
      enabled: policy.enabled,
      createdByThreadId: policy.createdByThreadId,
      createdByRunId: policy.createdByRunId,
      createdAt: policy.createdAt.toISOString(),
      updatedAt: policy.updatedAt.toISOString(),
    },
  });
}

/** Changes who may approve requests that match this rule. */
export async function PATCH(request: Request, context: RouteContext) {
  const allowed = await requireAdminActor();
  if (isDenied(allowed)) return allowed;
  const { policyId } = await context.params;
  const body = (await request.json().catch(() => null)) as { approverGroupId?: unknown } | null;
  const approverGroupId = body?.approverGroupId;
  if (approverGroupId !== null && typeof approverGroupId !== "string") {
    return NextResponse.json({ error: "approverGroupId must be a group id or null" }, { status: 400 });
  }
  const orgId = await getOrgId();
  let approverRole: "admin" | null = null;
  if (approverGroupId) {
    const [group] = await db()
      .select({ slug: user_group.slug })
      .from(user_group)
      .where(and(eq(user_group.org_id, orgId), eq(user_group.id, approverGroupId)))
      .limit(1);
    if (!group) return NextResponse.json({ error: "group not found" }, { status: 404 });
    approverRole = group.slug === "administrators" ? "admin" : null;
  }
  const policy = await updateActionPolicy(orgId, policyId, { approverGroupId, approverRole });
  if (!policy) return NextResponse.json({ error: "not found" }, { status: 404 });
  return NextResponse.json({ policy: { id: policy.id, approverGroupId: policy.approverGroupId, approverRole: policy.approverRole } });
}
