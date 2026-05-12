import { NextRequest, NextResponse } from "next/server";
import type { AgentEvent } from "@neko/llm";
import { resolveAgentBackend, AgentBackendConfigError } from "@neko/llm";
import {
  appendWorkRunEvent,
  createWorkRun,
  finishWorkRun,
  getWorkRun,
  runChatTurn,
} from "@neko/llm/work";
import { getOrgId } from "@/lib/db";
import {
  notifyRunSubscribers,
  registerRun,
  unregisterRun,
} from "@/lib/neko-run-registry";
import {
  createWorkMessage,
  getWorkThread,
  suggestWorkThreadTitle,
  touchWorkThread,
} from "@/lib/work-store";

type RouteContext = {
  params: Promise<{ threadId: string }>;
};

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function POST(request: NextRequest, context: RouteContext) {
  const { threadId } = await context.params;
  const body = await request.json().catch(() => ({}));
  const message = typeof body.message === "string" ? body.message.trim() : "";
  if (!message) {
    return NextResponse.json({ error: "message is required" }, { status: 400 });
  }

  const orgId = await getOrgId();
  const thread = await getWorkThread(orgId, threadId);
  if (!thread) {
    return NextResponse.json({ error: "Thread not found" }, { status: 404 });
  }

  let backend;
  try {
    backend = await resolveAgentBackend(orgId);
  } catch (e) {
    if (e instanceof AgentBackendConfigError) {
      return NextResponse.json({ error: e.message }, { status: 400 });
    }
    throw e;
  }

  const run = await createWorkRun(orgId, threadId, backend.id);

  if (!thread.title) {
    await touchWorkThread(threadId, { title: suggestWorkThreadTitle(message) });
  }
  await createWorkMessage({
    orgId,
    threadId,
    runId: run.id,
    role: "user",
    content: message,
  });

  const abortController = new AbortController();
  registerRun({
    runId: run.id,
    threadId,
    orgId,
    abortController,
    subscribers: new Set(),
  });

  let seq = 0;
  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({ orgId, threadId, runId: run.id, seq, event });
    notifyRunSubscribers(run.id, event, seq);
  };

  void runChatTurn({
    orgId,
    threadId,
    runId: run.id,
    message,
    emit,
    signal: abortController.signal,
  })
    .catch(async (err) => {
      console.error(`[work-run/inproc] run ${run.id} threw:`, err);
      try {
        const current = await getWorkRun(orgId, run.id);
        const terminal =
          current?.status === "completed" ||
          current?.status === "failed" ||
          current?.status === "cancelled";
        if (terminal) return;

        const errMsg = err instanceof Error ? err.message : String(err);
        await emit({ type: "error", message: errMsg });
        await emit({ type: "done", result: { status: "failed" } });
        await finishWorkRun(run.id, "failed", errMsg);
      } catch (cleanupErr) {
        console.error(
          `[work-run/inproc] cleanup failed for ${run.id}:`,
          cleanupErr,
        );
      }
    })
    .finally(() => {
      unregisterRun(run.id);
    });

  return NextResponse.json({
    runId: run.id,
    threadId,
    backend: backend.id,
  });
}
