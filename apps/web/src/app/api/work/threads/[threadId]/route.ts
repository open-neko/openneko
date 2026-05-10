import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { getWorkThreadBundle } from "@/lib/work-store";

type RouteContext = {
  params: Promise<{ threadId: string }>;
};

export async function GET(_: Request, context: RouteContext) {
  const { threadId } = await context.params;
  const bundle = await getWorkThreadBundle(await getOrgId(), threadId);
  if (!bundle) {
    return NextResponse.json({ error: "Thread not found" }, { status: 404 });
  }
  return NextResponse.json(bundle);
}
