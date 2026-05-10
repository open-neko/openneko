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
// Cap how long we'll long-poll for a single open EventSource. The
// browser will auto-reconnect (EventSource default behavior) so
// closing-and-resuming is cheaper than holding a connection open
// indefinitely with a sleeping pg client.
const MAX_LIFETIME_MS = 10 * 60_000;

function frame(data: unknown, id?: number): Uint8Array {
  // When `id` is set, the browser remembers it and sends it back
  // as `Last-Event-ID` on reconnect — this is how we resume
  // without replaying events the client has already processed.
  const idLine = typeof id === "number" ? `id: ${id}\n` : "";
  return new TextEncoder().encode(
    `${idLine}data: ${JSON.stringify(data)}\n\n`,
  );
}

function comment(text: string): Uint8Array {
  // SSE comments (`: ...`) keep the connection alive without
  // delivering an event to the client's onmessage handler.
  return new TextEncoder().encode(`: ${text}\n\n`);
}

/**
 * GET /api/work/threads/{threadId}/runs/{runId}/events
 *
 * Server-Sent Events long-poll over work_run_event. The worker
 * writes AgentEvents into work_run_event by seq; this endpoint
 * polls the table and pushes new rows down the SSE channel until
 * the matching work_run row reaches a terminal status.
 *
 * Filtering: callers can pass `?afterSeq=N` to resume a stream
 * after a reconnect without replaying events the client already
 * processed. Default 0 = replay everything we have.
 *
 * Lifecycle:
 *   1. Stream all existing events with seq > afterSeq (catch-up).
 *   2. Loop: sleep POLL_INTERVAL_MS, fetch new events, push.
 *   3. Stop when work_run.status is terminal AND we've drained
 *      events up to the last seq we observed.
 *   4. Cap total lifetime at MAX_LIFETIME_MS and close — the
 *      browser EventSource reconnects automatically.
 */
export async function GET(request: NextRequest, context: RouteContext) {
  const { threadId, runId } = await context.params;
  const url = new URL(request.url);
  // EventSource sets Last-Event-ID on auto-reconnect; the explicit
  // `?afterSeq=N` query param is a manual override (e.g. for
  // server-driven `_reconnect_hint`). Last-Event-ID wins when both
  // are present — the browser is the more authoritative source for
  // "highest event id we've actually rendered".
  const lastEventIdHeader = request.headers.get("last-event-id");
  const afterSeqParam = Number(url.searchParams.get("afterSeq") ?? "0") || 0;
  const afterSeq = Number(lastEventIdHeader) || afterSeqParam;

  const orgId = await getOrgId();

  // Validate the run belongs to this org's thread before opening
  // the stream. Without this, anyone could tail any runId.
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

      // Close the stream on client disconnect so we stop polling
      // a run nobody's watching anymore.
      request.signal.addEventListener(
        "abort",
        () => {
          closed = true;
        },
        { once: true },
      );

      // Initial keepalive comment so the browser flushes headers
      // and we're sure the connection is open before the first
      // event might take 250ms to arrive.
      safeEnqueue(comment("hello"));

      // Top-level kickoff event so the client knows the run + backend
      // immediately, without waiting for the worker's first emit.
      // No SSE id — replay-on-reconnect re-issues this harmlessly.
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

        // Re-read the run row each iteration — its status is the
        // authoritative "is this run still going" signal. A `done`
        // event in the stream tells the client to close locally,
        // but we also close server-side here so a stale browser
        // doesn't keep polling forever.
        const current = await getWorkRun(orgId, runId);
        if (current && (current.status === "completed" || current.status === "failed" || current.status === "cancelled")) {
          // One last drain pass after we noticed the terminal state,
          // since the worker may have written `done` AFTER we
          // already got `getWorkRunEventsAfter` for this iteration.
          const tail = await getWorkRunEventsAfter(orgId, runId, lastSeq);
          for (const { seq, event } of tail) {
            safeEnqueue(frame(event, seq));
            lastSeq = seq;
          }
          break;
        }

        if (Date.now() - t0 > MAX_LIFETIME_MS) {
          // Server-side close. The browser EventSource will
          // auto-reconnect and send `Last-Event-ID: <lastSeq>`
          // so we resume from there without replaying.
          break;
        }

        // Periodic comment keeps proxies / load balancers from
        // closing an idle connection between polls.
        if ((Date.now() - t0) % 30_000 < POLL_INTERVAL_MS) {
          safeEnqueue(comment("keepalive"));
        }

        await new Promise((r) => setTimeout(r, POLL_INTERVAL_MS));
      }

      try {
        controller.close();
      } catch {
        // already closed
      }
    },
  });

  return new Response(stream, { headers: SSE_HEADERS });
}
