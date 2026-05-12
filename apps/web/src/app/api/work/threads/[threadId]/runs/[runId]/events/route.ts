import { NextRequest } from "next/server";
import type { AgentEvent } from "@neko/llm";
import { createNotifyClient, type NotifyClient } from "@neko/db";
import { getOrgId } from "@/lib/db";
import { subscribeToRun } from "@/lib/neko-run-registry";
import { getWorkRun, getWorkRunEventsAfter } from "@/lib/work-store";

type RouteContext = {
  params: Promise<{ threadId: string; runId: string }>;
};

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const SSE_HEADERS = {
  "content-type": "text/event-stream; charset=utf-8",
  "cache-control": "no-cache, no-transform",
  connection: "keep-alive",
};

// LISTEN drives wake-ups; the loop interval is a keepalive backstop.
const LOOP_INTERVAL_MS = 5_000;
const MAX_LIFETIME_MS = 10 * 60_000;

function frame(data: unknown, id?: number): Uint8Array {
  const idLine = typeof id === "number" ? `id: ${id}\n` : "";
  return new TextEncoder().encode(
    `${idLine}data: ${JSON.stringify(data)}\n\n`,
  );
}

function comment(text: string): Uint8Array {
  return new TextEncoder().encode(`: ${text}\n\n`);
}

export async function GET(request: NextRequest, context: RouteContext) {
  const { threadId, runId } = await context.params;
  const url = new URL(request.url);
  const lastEventIdHeader = request.headers.get("last-event-id");
  const afterSeqParam = Number(url.searchParams.get("afterSeq") ?? "0") || 0;
  const afterSeq = Number(lastEventIdHeader) || afterSeqParam;

  const orgId = await getOrgId();

  const run = await getWorkRun(orgId, runId);
  if (!run || run.thread_id !== threadId) {
    return new Response("Not Found", { status: 404 });
  }

  const stream = new ReadableStream<Uint8Array>({
    async start(controller) {
      const t0 = Date.now();
      let closed = false;
      let lastSentSeq = afterSeq;
      const sentSeqs = new Set<number>();

      const safeEnqueue = (chunk: Uint8Array): void => {
        if (closed) return;
        try {
          controller.enqueue(chunk);
        } catch {
          closed = true;
        }
      };

      const sendIfNew = (event: AgentEvent, seq: number): void => {
        if (seq <= afterSeq) return;
        if (sentSeqs.has(seq)) return;
        sentSeqs.add(seq);
        safeEnqueue(frame(event, seq));
        if (seq > lastSentSeq) lastSentSeq = seq;
      };

      request.signal.addEventListener(
        "abort",
        () => {
          closed = true;
        },
        { once: true },
      );

      const unsubscribe = subscribeToRun(runId, sendIfNew);

      let notifyResolver: (() => void) | null = null;
      const waitForNotify = () =>
        new Promise<void>((resolve) => {
          notifyResolver = resolve;
        });
      const wakeFromNotify = () => {
        const r = notifyResolver;
        notifyResolver = null;
        r?.();
      };

      let listenClient: NotifyClient | null = null;
      try {
        listenClient = await createNotifyClient("work_run_event");
        listenClient.on((channel, payload) => {
          if (channel === "work_run_event" && payload === runId) {
            wakeFromNotify();
          }
        });
      } catch (err) {
        console.warn("[work-events] LISTEN setup failed; falling back to interval", err);
      }

      safeEnqueue(comment("hello"));
      safeEnqueue(
        frame({
          type: "hello",
          runId,
          threadId,
          backend: run.backend,
        }),
      );

      let keepaliveTimer = Date.now();

      try {
        while (!closed) {
          const newEvents = await getWorkRunEventsAfter(
            orgId,
            runId,
            lastSentSeq,
          );
          for (const { seq, event } of newEvents) {
            sendIfNew(event, seq);
          }

          const current = await getWorkRun(orgId, runId);
          if (
            current &&
            (current.status === "completed" ||
              current.status === "failed" ||
              current.status === "cancelled")
          ) {
            const tail = await getWorkRunEventsAfter(orgId, runId, lastSentSeq);
            for (const { seq, event } of tail) {
              sendIfNew(event, seq);
            }
            break;
          }

          if (Date.now() - t0 > MAX_LIFETIME_MS) {
            break;
          }

          if (Date.now() - keepaliveTimer > 30_000) {
            safeEnqueue(comment("keepalive"));
            keepaliveTimer = Date.now();
          }

          await Promise.race([
            waitForNotify(),
            new Promise((r) => setTimeout(r, LOOP_INTERVAL_MS)),
          ]);
        }
      } finally {
        unsubscribe?.();
        if (listenClient) await listenClient.close();
        try {
          controller.close();
        } catch {}
      }
    },
  });

  return new Response(stream, { headers: SSE_HEADERS });
}
