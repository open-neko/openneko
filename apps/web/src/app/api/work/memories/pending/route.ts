import { NextRequest, NextResponse } from "next/server";
import { listPendingWorkMemories } from "@neko/llm/work";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET(request: NextRequest) {
  const url = new URL(request.url);
  const threadId = url.searchParams.get("threadId");
  const runId = url.searchParams.get("runId");
  const orgId = await getOrgId();
  const pending = await listPendingWorkMemories({
    orgId,
    threadId,
    runId,
    status: "proposed",
  });
  return NextResponse.json({ pending });
}
