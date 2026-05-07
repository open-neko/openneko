import { NextResponse } from "next/server";
import { cancelWorkRun } from "@/lib/work-run-registry";

type RouteContext = {
  params: Promise<{ runId: string }>;
};

export async function POST(_: Request, context: RouteContext) {
  const { runId } = await context.params;
  const ok = cancelWorkRun(runId);
  return NextResponse.json({ ok });
}
