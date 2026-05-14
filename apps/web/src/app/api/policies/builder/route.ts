import { NextRequest, NextResponse } from "next/server";
import { AgentBackendConfigError, resolveAgentBackend } from "@neko/llm";
import {
  createWorkRun,
  finishWorkRun,
  getWorkRun,
} from "@neko/llm/work";
import { runPolicyBuilderTurn } from "@neko/llm/workflows";
import { createCoalescingEmit } from "@/lib/coalescing-emit";
import { getOrgId } from "@/lib/db";
import {
  registerRun,
  unregisterRun,
} from "@/lib/neko-run-registry";
import {
  createWorkMessage,
  createWorkThread,
  getWorkThread,
  suggestWorkThreadTitle,
  touchWorkThread,
} from "@/lib/work-store";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const message = typeof body.message === "string" ? body.message.trim() : "";
  const providedThreadId =
    typeof body.threadId === "string" && body.threadId.length > 0
      ? body.threadId
      : null;
  const editingPolicyId =
    typeof body.editingPolicyId === "string" && body.editingPolicyId.length > 0
      ? body.editingPolicyId
      : undefined;

  if (!message) {
    return NextResponse.json({ error: "message is required" }, { status: 400 });
  }

  const orgId = await getOrgId();

  let backend;
  try {
    backend = await resolveAgentBackend(orgId);
  } catch (e) {
    if (e instanceof AgentBackendConfigError) {
      return NextResponse.json({ error: e.message }, { status: 400 });
    }
    throw e;
  }

  let threadId: string;
  if (providedThreadId) {
    const thread = await getWorkThread(orgId, providedThreadId);
    if (!thread) {
      return NextResponse.json({ error: "Thread not found" }, { status: 404 });
    }
    threadId = thread.id;
  } else {
    const thread = await createWorkThread(orgId, "Policy builder");
    threadId = thread.id;
  }

  const run = await createWorkRun(orgId, threadId, backend.id);

  await touchWorkThread(threadId, { title: suggestWorkThreadTitle(message) });
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

  const { emit, finalize } = createCoalescingEmit({
    orgId,
    threadId,
    runId: run.id,
  });

  void runPolicyBuilderTurn({
    orgId,
    threadId,
    runId: run.id,
    message,
    emit,
    editingPolicyId,
    signal: abortController.signal,
  })
    .catch(async (err) => {
      console.error(`[policy-builder] run ${run.id} threw:`, err);
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
          `[policy-builder] cleanup failed for ${run.id}:`,
          cleanupErr,
        );
      }
    })
    .finally(async () => {
      try {
        await finalize();
      } catch (err) {
        console.error(
          `[policy-builder] finalize failed for ${run.id}:`,
          err,
        );
      }
      unregisterRun(run.id);
    });

  return NextResponse.json({
    threadId,
    runId: run.id,
    backend: backend.id,
  });
}
