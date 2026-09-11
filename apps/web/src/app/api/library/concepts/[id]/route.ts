import { NextRequest, NextResponse } from "next/server";
import { archiveLibraryConcept, editLibraryConcept, readLibraryConcept } from "@neko/llm/work";
import { materializeTeamLibrary } from "@neko/llm";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ id: string }> };

function parseEdit(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const fields = { title: 240, description: 2000, type: 100, body: 200_000, updatedAt: 30 };
  const raw = value as Record<string, unknown>;
  if (Object.keys(raw).some(key => !Object.hasOwn(fields, key))
    || Object.entries(fields).some(([key, max]) => typeof raw[key] !== "string" || (raw[key] as string).length > max)) return null;
  const text = raw as Record<keyof typeof fields, string>;
  const data = { title: text.title.trim(), description: text.description.trim(), type: text.type.trim(), body: text.body.trim(), updatedAt: text.updatedAt };
  if (!data.title || !data.type || !data.body || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(data.updatedAt)
    || !Number.isFinite(Date.parse(data.updatedAt))) return null;
  return data;
}

export async function PATCH(request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id)) return NextResponse.json({ error: "Not found." }, { status: 404 });
  if (Number(request.headers.get("content-length")) > 1_000_000) {
    return NextResponse.json({ error: "Concept is too large." }, { status: 413 });
  }
  const parsed = parseEdit(await request.json().catch(() => null));
  if (!parsed) return NextResponse.json({ error: "Provide a title, type and content within the field limits." }, { status: 400 });
  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  const result = await editLibraryConcept({ orgId, userId: actor.userId, isAdmin: actor.role === "admin" }, { id, ...parsed });
  if (result.status === "not_found") return NextResponse.json({ error: "Not found or not editable." }, { status: 404 });
  if (result.status === "conflict") return NextResponse.json({ error: "This concept changed while you were editing. Copy your changes, then reload the latest version." }, { status: 409 });
  if (result.concept.userId === null) await materializeTeamLibrary(orgId);
  return NextResponse.json(result);
}

export async function GET(_request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id)) {
    return NextResponse.json({ error: "Not found." }, { status: 404 });
  }
  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  const concept = await readLibraryConcept({ orgId, userId: actor.userId, isAdmin: actor.role === "admin" }, id);
  return concept ? NextResponse.json({ concept }) : NextResponse.json({ error: "Not found." }, { status: 404 });
}

/** Owner archives one of their personal concepts. */
export async function DELETE(_request: NextRequest, context: RouteContext) {
  const { id } = await context.params;
  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  try {
    const concept = await archiveLibraryConcept({
      orgId,
      id,
      userId: actor.userId,
    });
    return NextResponse.json({ ok: true, concept });
  } catch {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
}
