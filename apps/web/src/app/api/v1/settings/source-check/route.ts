import { NextRequest, NextResponse } from "next/server";
import { checkSourceForOrg } from "@neko/llm/graphjin/source-check";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function POST(request: NextRequest) {
  const actor = await getCurrentActor();
  if (actor.role !== "admin") {
    return NextResponse.json({ error: "admin only" }, { status: 403 });
  }
  const body = (await request.json().catch(() => ({}))) as Record<string, unknown>;
  const answers = {
    type: body.type,
    host: body.host,
    port: body.port,
    dbname: body.dbname,
    user: body.user,
    secretRef: body.secretRef,
  };
  return NextResponse.json({ check: await checkSourceForOrg({ orgId: await getOrgId(), answers }) });
}
