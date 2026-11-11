import { NextRequest, NextResponse } from "next/server";
import { getLibraryDocument, removeLibraryDocument } from "@neko/llm";
import { isLibraryStorePath, removeLibraryFile } from "@neko/llm/library/store";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ id: string }> };

export async function GET(_request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id)) {
    return NextResponse.json({ error: "Not found." }, { status: 404 });
  }
  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  const document = await getLibraryDocument(orgId, id);
  return document && document.userId === actor.userId
    ? NextResponse.json({ document }) : NextResponse.json({ error: "Not found." }, { status: 404 });
}

export async function DELETE(_request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  const orgId = await getOrgId();
  const actor = await getCurrentActor();

  try {
    const document = await removeLibraryDocument({ orgId, id, userId: actor.userId });
    if (actor.userId && isLibraryStorePath(document.relativePath)) {
      await removeLibraryFile({ orgId, userId: actor.userId, relativePath: document.relativePath }).catch(() => {});
    }
    return NextResponse.json({ ok: true });
  } catch {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
}
