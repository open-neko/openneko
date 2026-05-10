import { NextRequest } from "next/server";
import { getOrgId } from "@/lib/db";
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

const POLL_INTERVAL_MS = 250;
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
      let lastSeq = afterSeq;

      const safeEnqueue = (chunk: Uint8Array): void => {
        if (closed) return;
        try {
          controller.enqueue(chunk);
        } catch {
          closed = true;
        }
      };

      request.signal.addEventListener(
        "abort",
        () => {
          closed = true;
        },
        { once: true },
      );

      safeEnqueue(comment("hello"));

      safeEnqueue(
        frame({
          type: "hello",
          runId,
          threadId,
          backend: run.backend,
        }),
      );

      while (!closed) {
        const newEvents = await getWorkRunEventsAfter(orgId, runId, lastSeq);
        for (const { seq, event } of newEvents) {
          safeEnqueue(frame(event, seq));
          lastSeq = seq;
        }

        const current = await getWorkRun(orgId, runId);
        if (current && (current.status === "completed" || current.status === "failed" || current.status === "cancelled")) {
          const tail = await getWorkRunEventsAfter(orgId, runId, lastSeq);
          for (const { seq, event } of tail) {
            safeEnqueue(frame(event, seq));
            lastSeq = seq;
          }
          break;
        }

        if (Date.now() - t0 > MAX_LIFETIME_MS) {
          break;
        }

        if ((Date.now() - t0) % 30_000 < POLL_INTERVAL_MS) {
          safeEnqueue(comment("keepalive"));
        }

        await new Promise((r) => setTimeout(r, POLL_INTERVAL_MS));
      }

      try {
        controller.close();
      } catch {}
    },
  });

  return new Response(stream, { headers: SSE_HEADERS });
}
