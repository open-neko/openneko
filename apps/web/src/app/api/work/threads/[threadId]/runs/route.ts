import { NextRequest, NextResponse } from "next/server";
import {
  ensureGraphjinGuard,
  ensureWorkWorkspace,
  resolveBinaryOnPath,
  resolveWorkAgentBackend,
  type WorkEvent,
  type WorkTranscriptMessage,
} from "@neko/llm";
import { getOrgId } from "@/lib/db";
import { registerWorkRun } from "@/lib/work-run-registry";
import {
  appendWorkRunEvent,
  createWorkMessage,
  createWorkRun,
  finishWorkRun,
  getWorkThread,
  getWorkThreadBundle,
  setWorkThreadBackendState,
  suggestWorkThreadTitle,
  touchWorkThread,
} from "@/lib/work-store";

type RouteContext = {
  params: Promise<{ threadId: string }>;
};

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const SSE_HEADERS = {
  "content-type": "text/event-stream; charset=utf-8",
  "cache-control": "no-cache, no-transform",
  connection: "keep-alive",
};

function frame(data: unknown): Uint8Array {
  return new TextEncoder().encode(`data: ${JSON.stringify(data)}\n\n`);
}

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

  const backend = await resolveWorkAgentBackend(orgId);
  const run = await createWorkRun(orgId, threadId, backend.id);
  const workspace = await ensureWorkWorkspace(orgId, threadId, run.id);
  const graphjinBinary = await resolveBinaryOnPath("graphjin");
  if (!graphjinBinary) {
    await finishWorkRun(run.id, "failed", "graphjin CLI is not installed on PATH.");
    return NextResponse.json(
      { error: "graphjin CLI is not installed on PATH." },
      { status: 500 },
    );
  }
  await ensureGraphjinGuard(workspace.binRoot, graphjinBinary);

  const title = !thread.title ? suggestWorkThreadTitle(message) : undefined;
  if (title) {
    await touchWorkThread(threadId, { title });
  }
  await createWorkMessage({
    orgId,
    threadId,
    runId: run.id,
    role: "user",
    content: message,
  });

  const bundle = await getWorkThreadBundle(orgId, threadId);
  if (!bundle) {
    await finishWorkRun(run.id, "failed", "Thread disappeared before run start.");
    return NextResponse.json({ error: "Thread not found" }, { status: 404 });
  }

  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        const abortController = new AbortController();
        const unregister = registerWorkRun(run.id, abortController);
        let seq = 0;
        let closed = false;

        const safeEnqueue = (payload: unknown) => {
          if (closed) return;
          try {
            controller.enqueue(frame(payload));
          } catch {
            closed = true;
          }
        };

        const emit = async (event: WorkEvent) => {
          seq += 1;
          await appendWorkRunEvent({
            orgId,
            threadId,
            runId: run.id,
            seq,
            event,
          });
          if (event.type === "message" && event.role === "assistant") {
            await createWorkMessage({
              orgId,
              threadId,
              runId: run.id,
              role: "assistant",
              content: event.content,
            });
          }
          safeEnqueue(event);
        };

        safeEnqueue({ type: "hello", runId: run.id, threadId });

        void (async () => {
          try {
            const result = await backend.run({
              orgId,
              threadId,
              runId: run.id,
              workspace,
              backendState: bundle.thread.backendState,
              messages: bundle.messages.map(
                (row): WorkTranscriptMessage => ({
                  id: row.id,
                  role: row.role,
                  content: row.content,
                  runId: row.runId,
                  createdAt: row.createdAt,
                }),
              ),
              currentUserMessage: message,
              signal: abortController.signal,
              onEvent: emit,
            });
            if (result.backendState && result.backendState !== bundle.thread.backendState) {
              await setWorkThreadBackendState(threadId, result.backendState);
            }
            await finishWorkRun(run.id, result.status, result.error ?? null);
            await emit({ type: "done", result: { status: result.status } });
          } catch (error) {
            const message =
              error instanceof Error ? error.message : "Work run failed unexpectedly.";
            await emit({ type: "error", message });
            await finishWorkRun(run.id, "failed", message);
            await emit({ type: "done", result: { status: "failed" } });
          } finally {
            unregister();
            closed = true;
            controller.close();
          }
        })();

        request.signal.addEventListener(
          "abort",
          () => {
            abortController.abort();
          },
          { once: true },
        );
      },
    }),
    { headers: SSE_HEADERS },
  );
}
