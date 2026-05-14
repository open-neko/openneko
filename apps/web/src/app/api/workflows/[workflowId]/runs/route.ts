import { NextRequest, NextResponse } from "next/server";
import { AgentBackendConfigError } from "@neko/llm";
import { getWorkRun } from "@neko/llm/work";
import {
  prepareWorkflowRun,
  runWorkflowTurn,
} from "@neko/llm/workflows";
import { createCoalescingEmit } from "@/lib/coalescing-emit";
import { getOrgId } from "@/lib/db";
import {
  registerRun,
  unregisterRun,
} from "@/lib/neko-run-registry";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ workflowId: string }>;
};

export async function POST(request: NextRequest, context: RouteContext) {
  const { workflowId } = await context.params;
  const body = await request.json().catch(() => ({}));
  const userMessage =
    typeof body.userMessage === "string" ? body.userMessage.trim() : undefined;

  const orgId = await getOrgId();

  let prepared;
  try {
    prepared = await prepareWorkflowRun({
      orgId,
      workflowId,
      triggerKind: "manual",
      triggerPayload: { userMessage: userMessage ?? null },
    });
  } catch (e) {
    if (e instanceof AgentBackendConfigError) {
      return NextResponse.json({ error: e.message }, { status: 400 });
    }
    const msg = e instanceof Error ? e.message : String(e);
    return NextResponse.json({ error: msg }, { status: 400 });
  }

  const abortController = new AbortController();
  registerRun({
    runId: prepared.workRunId,
    threadId: prepared.threadId,
    orgId,
    abortController,
    subscribers: new Set(),
  });

  const { emit, finalize } = createCoalescingEmit({
    orgId,
    threadId: prepared.threadId,
    runId: prepared.workRunId,
  });

  void runWorkflowTurn({
    prepared,
    userMessage,
    mode: "live",
    emit,
    signal: abortController.signal,
  })
    .catch(async (err) => {
      console.error(
        `[workflow-run] run ${prepared.workflowRun.id} threw:`,
        err,
      );
      try {
        const current = await getWorkRun(orgId, prepared.workRunId);
        const terminal =
          current?.status === "completed" ||
          current?.status === "failed" ||
          current?.status === "cancelled";
        if (terminal) return;
        const errMsg = err instanceof Error ? err.message : String(err);
        await emit({ type: "error", message: errMsg });
        await emit({ type: "done", result: { status: "failed" } });
      } catch (cleanupErr) {
        console.error(
          `[workflow-run] cleanup failed for ${prepared.workflowRun.id}:`,
          cleanupErr,
        );
      }
    })
    .finally(async () => {
      try {
        await finalize();
      } catch (err) {
        console.error(
          `[workflow-run] finalize failed for ${prepared.workflowRun.id}:`,
          err,
        );
      }
      unregisterRun(prepared.workRunId);
    });

  return NextResponse.json({
    workflowRunId: prepared.workflowRun.id,
    workRunId: prepared.workRunId,
    threadId: prepared.threadId,
  });
}
