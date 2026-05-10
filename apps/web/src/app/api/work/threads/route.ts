import { NextRequest, NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { createWorkThread, listWorkThreads } from "@/lib/work-store";

export async function GET() {
  const threads = await listWorkThreads(await getOrgId());
  return NextResponse.json({ threads });
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const title = typeof body.title === "string" ? body.title.trim() : "";
  const thread = await createWorkThread(await getOrgId(), title);
  return NextResponse.json({
    thread: {
      id: thread.id,
      title: thread.title || "Untitled thread",
      createdAt: thread.created_at.toISOString(),
      updatedAt: thread.updated_at.toISOString(),
      lastMessageAt: thread.last_message_at.toISOString(),
    },
  });
}
