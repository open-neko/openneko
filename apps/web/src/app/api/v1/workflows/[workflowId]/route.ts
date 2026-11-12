import { NextResponse } from "next/server";
import { and, db, eq, workflow_definition } from "@neko/db";
import { deleteWorkflow } from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";
import { requireWorkflow } from "@/lib/entitlements";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ workflowId: string }>;
};

export async function PATCH(request: Request, context: RouteContext) {
  const { workflowId } = await context.params;
  const deniedWorkflow = await requireWorkflow(workflowId);
  if (deniedWorkflow) return deniedWorkflow;
  const orgId = await getOrgId();
  const body = await request.json().catch(() => ({}));

  const patch: {
    enabled?: boolean;
    cron_enabled?: boolean;
    paused_until?: Date | null;
  } = {};
  if (typeof body.enabled === "boolean") {
    patch.enabled = body.enabled;
    // Re-enabling clears any pause-for-today timer.
    if (body.enabled) patch.paused_until = null;
  }
  if (typeof body.cronEnabled === "boolean") patch.cron_enabled = body.cronEnabled;
  // OL7 "pause for today": park until the next UTC midnight; the cron
  // sweep re-enables it.
  if (body.pauseForToday === true) {
    const until = new Date();
    until.setUTCHours(24, 0, 0, 0);
    patch.enabled = false;
    patch.paused_until = until;
  }
  if (Object.keys(patch).length === 0) {
    return NextResponse.json({ error: "no recognized fields" }, { status: 400 });
  }

  const result = await db()
    .update(workflow_definition)
    .set({ ...patch, updated_at: new Date() })
    .where(
      and(
        eq(workflow_definition.org_id, orgId),
        eq(workflow_definition.id, workflowId),
      ),
    )
    .returning({ id: workflow_definition.id });

  if (result.length === 0) {
    return NextResponse.json({ error: "workflow not found" }, { status: 404 });
  }

  return NextResponse.json({ ok: true });
}

export async function DELETE(_request: Request, context: RouteContext) {
  const { workflowId } = await context.params;
  const deniedWorkflow = await requireWorkflow(workflowId);
  if (deniedWorkflow) return deniedWorkflow;
  const orgId = await getOrgId();

  const deleted = await deleteWorkflow(orgId, workflowId);
  if (!deleted) {
    return NextResponse.json({ error: "workflow not found" }, { status: 404 });
  }

  return NextResponse.json({ ok: true });
}
