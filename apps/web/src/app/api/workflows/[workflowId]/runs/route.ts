import { NextRequest, NextResponse } from "next/server";
import type { AgentEvent } from "@neko/llm";
import { AgentBackendConfigError } from "@neko/llm";
import { appendWorkRunEvent, getWorkRun } from "@neko/llm/work";
import {
  prepareWorkflowRun,
  runWorkflowTurn,
} from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";
import {
  notifyRunSubscribers,
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

  let seq = 0;
  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({
      orgId,
      threadId: prepared.threadId,
      runId: prepared.workRunId,
      seq,
      event,
    });
    notifyRunSubscribers(prepared.workRunId, event, seq);
  };

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
    .finally(() => {
      unregisterRun(prepared.workRunId);
    });

  return NextResponse.json({
    workflowRunId: prepared.workflowRun.id,
    workRunId: prepared.workRunId,
    threadId: prepared.threadId,
  });
}
