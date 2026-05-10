import { NextRequest, NextResponse } from "next/server";
import { archiveWorkMemory, getWorkMemory } from "@neko/llm/work";
import { getOrgId } from "@/lib/db";

type RouteContext = {
  params: Promise<{ id: string }>;
};

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET(_request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  const orgId = await getOrgId();
  const memory = await getWorkMemory(orgId, id);
  if (!memory) {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
  return NextResponse.json({ memory });
}

export async function DELETE(request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  const body = await request.json().catch(() => ({}));
  const reason = typeof body.reason === "string" ? body.reason : undefined;
  const orgId = await getOrgId();
  const ok = await archiveWorkMemory(orgId, id, { reason });
  if (!ok) {
    return NextResponse.json(
      { error: "not found or already archived" },
      { status: 404 },
    );
  }
  return NextResponse.json({ ok: true });
}
