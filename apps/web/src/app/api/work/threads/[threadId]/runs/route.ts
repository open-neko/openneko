import { NextRequest, NextResponse } from "next/server";
import { resolveAgentBackend } from "@neko/llm";
import {
  appendWorkRunEvent,
  createWorkRun,
  finishWorkRun,
} from "@neko/llm/work";
import {
  db,
  eq,
  processing_job,
} from "@neko/db";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { getOrgId } from "@/lib/db";
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

/**
 * POST /api/work/threads/{threadId}/runs
 *
 * Enqueues a work_run job onto pg-boss. The agent itself runs in
 * the worker process (apps/worker/src/jobs/work-run.ts) — no
 * inline Hermes spawn from Next.js. The browser receives the new
 * runId here, then opens an EventSource on
 * `/api/work/threads/{threadId}/runs/{runId}/events` to tail
 * AgentEvents the worker writes to work_run_event.
 *
 * Atomic-ish: insert work_run + processing_job, then enqueue. If
 * the enqueue throws after either insert, both rows (plus the
 * work_run) are finalized to `failed` so the user can re-submit
 * without staring at a stuck "running" state.
 */
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

  // Resolve backend up-front so we record what was selected at
  // enqueue time. The worker re-resolves at pickup as the source of
  // truth, but stamping it now also gives the SSE `hello` event
  // something to render before the worker starts.
  const backend = await resolveAgentBackend(orgId);
  const run = await createWorkRun(orgId, threadId, backend.id);

  // Title the thread from the first user message if it's still
  // unset, then save the user message itself.
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

  // Emit a single "hello" event at seq=1 so the SSE tail has
  // something to show immediately on first poll, before the worker
  // has had a chance to pick the job up. Same shape the inline
  // route used to emit; keeps the client renderer unchanged.
  await appendWorkRunEvent({
    orgId,
    threadId,
    runId: run.id,
    seq: 1,
    event: { type: "status", message: `Queued for ${backend.id}…` } as never,
  });

  // Insert the processing_job row + enqueue. Any failure in either
  // path finalizes the work_run so the UI doesn't spin forever.
  let processingJobId: string;
  try {
    const inserted = await db()
      .insert(processing_job)
      .values({
        org_id: orgId,
        kind: "work_run",
        status: "queued",
        trigger: "work_message",
        trigger_payload: {
          runId: run.id,
          threadId,
          message,
        },
      })
      .returning({ id: processing_job.id });
    processingJobId = inserted[0]!.id;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    await finishWorkRun(run.id, "failed", `processing_job insert failed: ${msg}`);
    return NextResponse.json(
      { error: `processing_job insert failed: ${msg}` },
      { status: 500 },
    );
  }

  try {
    await enqueue(QUEUE.WORK_RUN, {
      processingJobId,
      orgId,
      runId: run.id,
      threadId,
      message,
    });
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    const now = new Date();
    await db()
      .update(processing_job)
      .set({
        status: "failed",
        error: `enqueue failed: ${msg}`,
        finished_at: now,
        updated_at: now,
      })
      .where(eq(processing_job.id, processingJobId));
    await finishWorkRun(run.id, "failed", `enqueue failed: ${msg}`);
    return NextResponse.json(
      { error: `enqueue failed: ${msg}` },
      { status: 500 },
    );
  }

  return NextResponse.json({
    runId: run.id,
    threadId,
    backend: backend.id,
    jobId: processingJobId,
  });
}
