import { rm } from "node:fs/promises";
import { NextRequest, NextResponse } from "next/server";
import {
  getLibraryDocument,
  removeLibraryDerivedMarkdown,
  removeLibraryDocument,
  resolveLibrarySourcePath,
} from "@neko/llm";
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

/**
 * Remove a document from your library: deletes the tracking row and
 * archives your concepts distilled from it — the retroactive
 * "don't catalog this" control. Library-direct uploads also lose the
 * raw file; thread attachments stay with their thread.
 */
export async function DELETE(_request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  const orgId = await getOrgId();
  const actor = await getCurrentActor();

  try {
    const document = await removeLibraryDocument({
      orgId,
      id,
      userId: actor.userId,
    });
    await removeLibraryDerivedMarkdown(
      orgId,
      document.extractedRelativePath,
    ).catch(() => {
      // Missing derived state is fine after row removal.
    });
    if (document.relativePath.startsWith("library/uploads/")) {
      const absolute = resolveLibrarySourcePath(orgId, document.relativePath);
      await rm(absolute, { force: true }).catch(() => {
        // Missing file is fine — the row removal is what matters.
      });
    }
    return NextResponse.json({ ok: true });
  } catch {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
}
