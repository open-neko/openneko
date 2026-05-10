import { NextRequest, NextResponse } from "next/server";
import { listWorkMemories } from "@neko/llm/work";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET(request: NextRequest) {
  const url = new URL(request.url);
  const includeArchived = url.searchParams.get("includeArchived") === "true";
  const orgId = await getOrgId();
  const memories = await listWorkMemories(orgId, { includeArchived });
  return NextResponse.json({ memories });
}
