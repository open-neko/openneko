import { NextResponse } from "next/server";
import { getActionPolicy } from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ policyId: string }>;
};

export async function GET(_: Request, context: RouteContext) {
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
      priority: policy.priority,
      enabled: policy.enabled,
      createdAt: policy.createdAt.toISOString(),
      updatedAt: policy.updatedAt.toISOString(),
    },
  });
}
