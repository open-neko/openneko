import { NextRequest, NextResponse } from "next/server";
import { dismissLesson, listLessons, type LessonVerdict } from "@neko/llm/work";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const VERDICTS = new Set<LessonVerdict>(["promoted", "dropped", "rolled_back"]);

export async function GET(request: NextRequest) {
  const allowed = await requireAdminActor();
  if (isDenied(allowed)) return allowed;
  const url = new URL(request.url);
  const verdict = url.searchParams.get("verdict");
  const status = url.searchParams.get("status");
  const limit = Number(url.searchParams.get("limit") ?? "100");
  const lessons = await listLessons(await getOrgId(), {
    ...(verdict && VERDICTS.has(verdict as LessonVerdict) ? { verdict: verdict as LessonVerdict } : {}),
    ...(status ? { status } : {}),
    limit: Number.isFinite(limit) ? limit : 100,
  });
  return NextResponse.json({
    lessons: lessons.map((lesson) => ({
      id: lesson.id,
      text: lesson.text,
      status: lesson.status,
      target: { kind: lesson.detail.targetKind, id: lesson.detail.targetId },
      ruleId: lesson.detail.ruleId ?? null,
      verdict: lesson.detail.verdict,
      reason: lesson.detail.reason,
      version: lesson.detail.version,
      enteredVersion: lesson.detail.enteredVersion,
      runIds: lesson.runIds,
      quotes: lesson.detail.quotes,
      history: lesson.detail.history,
      firstSeenAt: lesson.firstSeenAt.toISOString(),
      lastSeenAt: lesson.lastSeenAt.toISOString(),
    })),
  });
}

/** Dismisses a lesson. Its text goes on the target's blocklist. */
export async function PATCH(request: NextRequest) {
  const allowed = await requireAdminActor();
  if (isDenied(allowed)) return allowed;
  const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
  const lessonId = typeof body?.lessonId === "string" ? body.lessonId : "";
  if (!lessonId || body?.status !== "dismissed") {
    return NextResponse.json({ error: "Send lessonId and status: dismissed." }, { status: 400 });
  }
  const result = await dismissLesson({ orgId: await getOrgId(), lessonId, userId: allowed.userId });
  if (!result) return NextResponse.json({ error: "Lesson not found." }, { status: 404 });
  return NextResponse.json({ ok: true, ...result });
}
