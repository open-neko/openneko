import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { getWorkRunEvents } from "@/lib/work-store";

type RouteContext = {
  params: Promise<{ runId: string }>;
};

export async function GET(_: Request, context: RouteContext) {
  const { runId } = await context.params;
  const events = await getWorkRunEvents(await getOrgId(), runId);
  return NextResponse.json({ runId, events });
}
