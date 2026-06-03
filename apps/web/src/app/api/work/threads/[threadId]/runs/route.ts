import { NextRequest, NextResponse } from "next/server";
import { resolveAgentBackend, AgentBackendConfigError } from "@neko/llm";
import {
  agentRuntimeDepsFromEnv,
  createWorkRun,
  ensureAgentBroker,
  finishWorkRun,
  getWorkRun,
  runChatTurn,
} from "@neko/llm/work";
import { getPluginActionDescriptors } from "@/lib/auth";
import { createCoalescingEmit } from "@/lib/coalescing-emit";
import { getOrgId } from "@/lib/db";
import {
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

  // Memory writes are agent-driven: claude-agent uses the
  // mcp__neko_memory__save tool, Hermes emits a ```neko_memory fence
  // that run-chat-turn extracts and persists. No special user-side
  // command needed.

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

  const { emit, finalize } = createCoalescingEmit({
    orgId,
    threadId,
    runId: run.id,
  });

  const pluginActions = await getPluginActionDescriptors();

  // OPENNEKO_AGENT_RUNTIME=openshell → the agent loop runs in an OpenShell
  // sandbox (the web server stays the control plane, launches the box, and
  // relays events over the existing SSE); default `inprocess` is unchanged.
  const broker = await ensureAgentBroker();

  void runChatTurn(
    {
      orgId,
      threadId,
      runId: run.id,
      message,
      emit,
      signal: abortController.signal,
      pluginActions,
    },
    agentRuntimeDepsFromEnv(broker),
  )
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
    .finally(async () => {
      try {
        await finalize();
      } catch (err) {
        console.error(
          `[work-run/inproc] finalize failed for ${run.id}:`,
          err,
        );
      }
      unregisterRun(run.id);
    });

  return NextResponse.json({
    runId: run.id,
    threadId,
    backend: backend.id,
  });
}
