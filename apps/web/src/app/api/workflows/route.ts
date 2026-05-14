import { NextResponse } from "next/server";
import { listWorkflows } from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET() {
  const orgId = await getOrgId();
  const workflows = await listWorkflows(orgId);
  return NextResponse.json({
    workflows: workflows.map((w) => ({
      id: w.id,
      name: w.name,
      description: w.description,
      goal: w.goal,
      enabled: w.enabled,
      status: w.status,
      cron: w.cron,
      cronTimezone: w.cronTimezone,
      cronEnabled: w.cronEnabled,
      steps: w.steps,
      createdAt: w.createdAt.toISOString(),
      updatedAt: w.updatedAt.toISOString(),
    })),
  });
}
