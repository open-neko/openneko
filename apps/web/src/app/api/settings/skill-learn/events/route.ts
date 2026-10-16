import { NextResponse } from "next/server";
import { listLearningEvents } from "@neko/llm/work";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

/** The last 20 notable learning events, one plain line each. */
export async function GET() {
  const allowed = await requireAdminActor();
  if (isDenied(allowed)) return allowed;
  const events = await listLearningEvents(await getOrgId(), 20);
  return NextResponse.json({
    events: events.map((event) => ({
      id: event.id,
      kind: event.kind,
      line: event.line,
      at: event.at.toISOString(),
      lessonId: event.lessonId,
      status: event.status,
    })),
  });
}
