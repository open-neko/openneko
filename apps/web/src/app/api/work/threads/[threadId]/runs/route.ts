import { NextRequest, NextResponse } from "next/server";
import { resolveAgentBackend, AgentBackendConfigError } from "@neko/llm";
import {
  createWorkRun,
  finishWorkRun,
  getWorkRun,
  rememberWorkMemory,
  runChatTurn,
} from "@neko/llm/work";
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

// `save:` (or `/save`) at the start of a user message short-circuits the LLM
// entirely and persists the body as one or more memories. Numbered list items
// (\n1. ..., \n2. ...) become separate memories; otherwise the whole body is
// one. No model in the loop = never lies, never invents tool calls, always
// works regardless of which agent backend is configured.
const SAVE_PREFIX_RE = /^\s*\/?save\s*:?\s*/i;

function looksLikeSaveCommand(message: string): boolean {
  return SAVE_PREFIX_RE.test(message);
}

function parseSaveBody(message: string): string[] {
  const body = message.replace(SAVE_PREFIX_RE, "").trim();
  if (!body) return [];
  // Split on lines that look like "1.", "2.", etc. starting a new item.
  const parts = body.split(/\n(?=\s*\d+\.\s+)/);
  if (parts.length > 1) {
    return parts.map((p) => p.replace(/^\s*\d+\.\s+/, "").trim()).filter(Boolean);
  }
  return [body];
}

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

  // Short-circuit save: <body> — bypass the LLM, insert N memories directly.
  if (looksLikeSaveCommand(message)) {
    const items = parseSaveBody(message);
    if (items.length === 0) {
      return NextResponse.json(
        { error: "save: needs a body — e.g. `save: don't sum from a flattened nested response`" },
        { status: 400 },
      );
    }
    // Backend id is required by the run row schema. Resolve so the run is
    // tagged with whatever backend was configured even though we don't
    // actually invoke it for save: short-circuits.
    let saveBackendId: "hermes" | "claude-agent" = "hermes";
    try {
      const b = await resolveAgentBackend(orgId);
      saveBackendId = b.id;
    } catch {
      // fall back to hermes; backend resolution failures shouldn't block a save.
    }
    const run = await createWorkRun(orgId, threadId, saveBackendId);
    if (!thread.title) {
      await touchWorkThread(threadId, { title: suggestWorkThreadTitle(message) });
    }
    await createWorkMessage({
      orgId, threadId, runId: run.id, role: "user", content: message,
    });
    const saved = [];
    for (const text of items) {
      const memory = await rememberWorkMemory({
        orgId,
        threadId,
        runId: run.id,
        text,
        kind: "business_rule",
        scope: "global",
        pinned: true,
        confidence: 1,
        metadata: { source: "save_command" },
      });
      saved.push(memory.id);
    }
    const ack =
      saved.length === 1
        ? `Saved 1 global memory.`
        : `Saved ${saved.length} global memories.`;
    await createWorkMessage({
      orgId, threadId, runId: run.id, role: "assistant", content: ack,
    });
    await finishWorkRun(run.id, "completed", null);
    return NextResponse.json({ runId: run.id, savedMemoryIds: saved });
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

  const { emit, finalize } = createCoalescingEmit({
    orgId,
    threadId,
    runId: run.id,
  });

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
