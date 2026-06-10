import {
  and,
  asc,
  db,
  desc,
  eq,
  gte,
  sql,
  work_message,
  work_run,
  work_run_event,
  work_thread,
  workflow_run,
} from "@neko/db";
import type { AgentBackendId } from "../agent-backend";
import type { AgentEvent } from "../agent-backend";

export type WorkThreadSummary = {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  lastMessageAt: string;
};

export type WorkMessageRecord = {
  id: string;
  runId: string | null;
  role: "user" | "assistant";
  content: string;
  createdAt: string;
};

export type WorkRunRecord = {
  id: string;
  backend: AgentBackendId;
  status: string;
  error: string | null;
  createdAt: string;
  finishedAt: string | null;
  analysisMinutesSaved: number | null;
  analysisMinutesBasis: string | null;
};

export type WorkThreadBundle = {
  thread: {
    id: string;
    title: string;
    backendState: Record<string, unknown>;
    createdAt: string;
    updatedAt: string;
    lastMessageAt: string;
  };
  runs: WorkRunRecord[];
  messages: WorkMessageRecord[];
  eventsByRun: Record<string, AgentEvent[]>;
};

export async function listWorkThreads(
  orgId: string,
  channel = "web",
): Promise<WorkThreadSummary[]> {
  // Workflow runs reuse the work_thread / work_run plumbing for their
  // transcripts (so they get the same surface, events, memory hooks).
  // But /work (Ask) is strictly human ↔ agent — its sidebar must not
  // surface threads created by a workflow trigger. Exclude any thread
  // that has a workflow_run pointing at it.
  //
  // Channels are isolated: a surface lists only its own threads (the web Ask
  // UI passes "web"), so Telegram/Slack conversations never appear here.
  const rows = await db()
    .select()
    .from(work_thread)
    .where(
      and(
        eq(work_thread.org_id, orgId),
        eq(work_thread.channel, channel),
        sql`NOT EXISTS (SELECT 1 FROM ${workflow_run} wr WHERE wr.thread_id = ${work_thread.id})`,
      ),
    )
    .orderBy(desc(work_thread.last_message_at), desc(work_thread.created_at));
  return rows.map((row) => ({
    id: row.id,
    title: row.title || "Untitled thread",
    createdAt: row.created_at.toISOString(),
    updatedAt: row.updated_at.toISOString(),
    lastMessageAt: row.last_message_at.toISOString(),
  }));
}

export async function createWorkThread(
  orgId: string,
  title = "",
  channel = "web",
) {
  const rows = await db()
    .insert(work_thread)
    .values({
      org_id: orgId,
      title,
      channel,
    })
    .returning();
  return rows[0];
}

export async function deleteWorkThread(orgId: string, threadId: string): Promise<boolean> {
  const rows = await db()
    .delete(work_thread)
    .where(and(eq(work_thread.org_id, orgId), eq(work_thread.id, threadId)))
    .returning({ id: work_thread.id });
  return rows.length > 0;
}

// Truncates the thread at and after the given run: deletes that run plus
// every later run in the thread (events cascade via FK), wipes the user
// + assistant messages tied to those runs, and clears the thread's
// backendState so a Claude-agent resume can't re-inject the dropped turns
// from its persisted SDK session. Returns the run's `created_at` so the
// caller can verify it pointed at a real row.
export async function truncateWorkThreadFromRun(
  orgId: string,
  threadId: string,
  runId: string,
): Promise<{ ok: boolean }> {
  const targetRows = await db()
    .select({ created_at: work_run.created_at })
    .from(work_run)
    .where(
      and(
        eq(work_run.org_id, orgId),
        eq(work_run.thread_id, threadId),
        eq(work_run.id, runId),
      ),
    )
    .limit(1);
  const target = targetRows[0];
  if (!target) return { ok: false };

  await db()
    .delete(work_message)
    .where(
      and(
        eq(work_message.org_id, orgId),
        eq(work_message.thread_id, threadId),
        gte(work_message.created_at, target.created_at),
      ),
    );
  await db()
    .delete(work_run)
    .where(
      and(
        eq(work_run.org_id, orgId),
        eq(work_run.thread_id, threadId),
        gte(work_run.created_at, target.created_at),
      ),
    );
  await db()
    .update(work_thread)
    .set({ backend_state: {}, updated_at: new Date(), last_message_at: new Date() })
    .where(eq(work_thread.id, threadId));
  return { ok: true };
}

export async function getWorkThread(orgId: string, threadId: string) {
  const rows = await db()
    .select()
    .from(work_thread)
    .where(
      and(
        eq(work_thread.org_id, orgId),
        eq(work_thread.id, threadId),
      ),
    )
    .limit(1);
  return rows[0] ?? null;
}

export async function setWorkThreadBackendState(
  threadId: string,
  backendState: Record<string, unknown>,
) {
  await db()
    .update(work_thread)
    .set({
      backend_state: backendState,
      updated_at: new Date(),
    })
    .where(eq(work_thread.id, threadId));
}

export async function touchWorkThread(
  threadId: string,
  opts: { title?: string } = {},
) {
  const patch: Record<string, unknown> = {
    updated_at: new Date(),
    last_message_at: new Date(),
  };
  if (opts.title !== undefined) patch.title = opts.title;
  await db().update(work_thread).set(patch).where(eq(work_thread.id, threadId));
}

export async function createWorkRun(
  orgId: string,
  threadId: string,
  backend: AgentBackendId,
) {
  const rows = await db()
    .insert(work_run)
    .values({
      org_id: orgId,
      thread_id: threadId,
      backend,
      status: "queued",
    })
    .returning();
  return rows[0];
}

export async function markWorkRunRunning(runId: string) {
  await db()
    .update(work_run)
    .set({ status: "running", updated_at: new Date() })
    .where(eq(work_run.id, runId));
}

export async function finishWorkRun(
  runId: string,
  status: "completed" | "failed" | "cancelled",
  error: string | null,
) {
  await db()
    .update(work_run)
    .set({
      status,
      error,
      updated_at: new Date(),
      finished_at: new Date(),
    })
    .where(eq(work_run.id, runId));
}

// Persist a run's agent-estimated analysis value (server-clamped minutes +
// the one-line basis). Separate from finishWorkRun because the estimate is
// parsed from the run's value fence after the run is marked finished.
export async function setWorkRunValue(
  runId: string,
  args: { minutes: number | null; basis: string | null },
) {
  await db()
    .update(work_run)
    .set({
      analysis_minutes_saved: args.minutes,
      analysis_minutes_basis: args.basis,
      updated_at: new Date(),
    })
    .where(eq(work_run.id, runId));
}

export async function createWorkMessage(args: {
  orgId: string;
  threadId: string;
  runId: string | null;
  role: "user" | "assistant";
  content: string;
}) {
  const rows = await db()
    .insert(work_message)
    .values({
      org_id: args.orgId,
      thread_id: args.threadId,
      run_id: args.runId,
      role: args.role,
      content: args.content,
    })
    .returning();
  await touchWorkThread(args.threadId);
  return rows[0];
}

export async function saveAssistantWorkMessage(args: {
  orgId: string;
  threadId: string;
  runId: string;
  content: string;
}) {
  const existing = await db()
    .select({ id: work_message.id })
    .from(work_message)
    .where(
      and(
        eq(work_message.org_id, args.orgId),
        eq(work_message.thread_id, args.threadId),
        eq(work_message.run_id, args.runId),
        eq(work_message.role, "assistant"),
      ),
    )
    .limit(1);

  if (existing[0]) {
    const rows = await db()
      .update(work_message)
      .set({ content: args.content })
      .where(eq(work_message.id, existing[0].id))
      .returning();
    await touchWorkThread(args.threadId);
    return rows[0];
  }

  return createWorkMessage({
    orgId: args.orgId,
    threadId: args.threadId,
    runId: args.runId,
    role: "assistant",
    content: args.content,
  });
}

/**
 * Append an event to a run's event stream. Returns the Postgres-
 * assigned `id` (bigserial, globally monotonic). Callers don't manage
 * any seq — ordering is naturally preserved by insertion order, and
 * the SSE cursor is just "id > $lastId".
 */
export async function appendWorkRunEvent(args: {
  orgId: string;
  threadId: string;
  runId: string;
  event: AgentEvent;
}): Promise<number> {
  const [row] = await db()
    .insert(work_run_event)
    .values({
      org_id: args.orgId,
      thread_id: args.threadId,
      run_id: args.runId,
      kind: args.event.type,
      payload: args.event,
    })
    .returning({ id: work_run_event.id });
  return row?.id ?? 0;
}

export async function getWorkRunEvents(
  orgId: string,
  runId: string,
): Promise<AgentEvent[]> {
  const rows = await db()
    .select({
      payload: work_run_event.payload,
    })
    .from(work_run_event)
    .where(
      and(
        eq(work_run_event.org_id, orgId),
        eq(work_run_event.run_id, runId),
      ),
    )
    .orderBy(asc(work_run_event.id));
  return rows.map((row) => row.payload as AgentEvent);
}

export async function getWorkRunEventsAfter(
  orgId: string,
  runId: string,
  afterId: number,
): Promise<{ id: number; event: AgentEvent; createdAt: Date }[]> {
  const rows = await db()
    .select({
      id: work_run_event.id,
      payload: work_run_event.payload,
      created_at: work_run_event.created_at,
    })
    .from(work_run_event)
    .where(
      and(
        eq(work_run_event.org_id, orgId),
        eq(work_run_event.run_id, runId),
      ),
    )
    .orderBy(asc(work_run_event.id));
  return rows
    .filter((r) => r.id > afterId)
    .map((r) => ({
      id: r.id,
      event: r.payload as AgentEvent,
      createdAt: r.created_at,
    }));
}

export async function getWorkRun(orgId: string, runId: string) {
  const rows = await db()
    .select()
    .from(work_run)
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  return rows[0] ?? null;
}

/**
 * Lookup the thread a /work run belongs to. Used by the action-execute
 * worker job to figure out which thread to emit the terminal
 * action_request_result event into.
 */
export async function getWorkThreadForRun(
  orgId: string,
  runId: string,
): Promise<{ id: string } | null> {
  const rows = await db()
    .select({ id: work_thread.id })
    .from(work_run)
    .innerJoin(work_thread, eq(work_run.thread_id, work_thread.id))
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  return rows[0] ?? null;
}

export async function getWorkThreadBundle(
  orgId: string,
  threadId: string,
): Promise<WorkThreadBundle | null> {
  const thread = await getWorkThread(orgId, threadId);
  if (!thread) return null;

  const [runs, messages, events] = await Promise.all([
    db()
      .select()
      .from(work_run)
      .where(
        and(
          eq(work_run.org_id, orgId),
          eq(work_run.thread_id, threadId),
        ),
      )
      .orderBy(asc(work_run.created_at)),
    db()
      .select()
      .from(work_message)
      .where(
        and(
          eq(work_message.org_id, orgId),
          eq(work_message.thread_id, threadId),
        ),
      )
      .orderBy(asc(work_message.created_at)),
    db()
      .select({
        runId: work_run_event.run_id,
        payload: work_run_event.payload,
      })
      .from(work_run_event)
      .where(
        and(
          eq(work_run_event.org_id, orgId),
          eq(work_run_event.thread_id, threadId),
        ),
      )
      .orderBy(asc(work_run_event.id)),
  ]);

  const eventsByRun: Record<string, AgentEvent[]> = {};
  for (const row of events) {
    if (!eventsByRun[row.runId]) eventsByRun[row.runId] = [];
    eventsByRun[row.runId].push(row.payload as AgentEvent);
  }

  return {
    thread: {
      id: thread.id,
      title: thread.title || "Untitled thread",
      backendState: (thread.backend_state ?? {}) as Record<string, unknown>,
      createdAt: thread.created_at.toISOString(),
      updatedAt: thread.updated_at.toISOString(),
      lastMessageAt: thread.last_message_at.toISOString(),
    },
    runs: runs.map((row) => ({
      id: row.id,
      backend: row.backend as AgentBackendId,
      status: row.status,
      error: row.error,
      createdAt: row.created_at.toISOString(),
      finishedAt: row.finished_at ? row.finished_at.toISOString() : null,
      analysisMinutesSaved: row.analysis_minutes_saved,
      analysisMinutesBasis: row.analysis_minutes_basis,
    })),
    messages: messages.map((row) => ({
      id: row.id,
      runId: row.run_id,
      role: row.role as "user" | "assistant",
      content: row.content,
      createdAt: row.created_at.toISOString(),
    })),
    eventsByRun,
  };
}

export function suggestWorkThreadTitle(message: string): string {
  const text = message.trim().replace(/\s+/g, " ");
  if (!text) return "New thread";
  return text.length > 80 ? `${text.slice(0, 77)}...` : text;
}
