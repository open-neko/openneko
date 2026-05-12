import { NextResponse } from "next/server";
import { abortRun } from "@/lib/neko-run-registry";

type RouteContext = {
  params: Promise<{ runId: string }>;
};

export async function POST(_: Request, context: RouteContext) {
  const { runId } = await context.params;
  const ok = abortRun(runId);
  return NextResponse.json({ ok });
}
