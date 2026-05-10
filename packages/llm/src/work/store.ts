import {
  and,
  asc,
  db,
  desc,
  eq,
  work_message,
  work_run,
  work_run_event,
  work_thread,
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

export async function listWorkThreads(orgId: string): Promise<WorkThreadSummary[]> {
  const rows = await db()
    .select()
    .from(work_thread)
    .where(eq(work_thread.org_id, orgId))
    .orderBy(desc(work_thread.last_message_at), desc(work_thread.created_at));
  return rows.map((row) => ({
    id: row.id,
    title: row.title || "Untitled thread",
    createdAt: row.created_at.toISOString(),
    updatedAt: row.updated_at.toISOString(),
    lastMessageAt: row.last_message_at.toISOString(),
  }));
}

export async function createWorkThread(orgId: string, title = "") {
  const rows = await db()
    .insert(work_thread)
    .values({
      org_id: orgId,
      title,
    })
    .returning();
  return rows[0];
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

export async function appendWorkRunEvent(args: {
  orgId: string;
  threadId: string;
  runId: string;
  seq: number;
  event: AgentEvent;
}) {
  await db().insert(work_run_event).values({
    org_id: args.orgId,
    thread_id: args.threadId,
    run_id: args.runId,
    seq: args.seq,
    kind: args.event.type,
    payload: args.event,
  });
}

export async function getWorkRunEvents(orgId: string, runId: string): Promise<AgentEvent[]> {
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
    .orderBy(asc(work_run_event.seq));
  return rows.map((row) => row.payload as AgentEvent);
}

export async function getWorkRunEventsAfter(
  orgId: string,
  runId: string,
  afterSeq: number,
): Promise<{ seq: number; event: AgentEvent }[]> {
  const rows = await db()
    .select({
      seq: work_run_event.seq,
      payload: work_run_event.payload,
    })
    .from(work_run_event)
    .where(
      and(
        eq(work_run_event.org_id, orgId),
        eq(work_run_event.run_id, runId),
      ),
    )
    .orderBy(asc(work_run_event.seq));
  return rows
    .filter((r) => r.seq > afterSeq)
    .map((r) => ({ seq: r.seq, event: r.payload as AgentEvent }));
}

export async function getWorkRun(orgId: string, runId: string) {
  const rows = await db()
    .select()
    .from(work_run)
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
        seq: work_run_event.seq,
      })
      .from(work_run_event)
      .where(
        and(
          eq(work_run_event.org_id, orgId),
          eq(work_run_event.thread_id, threadId),
        ),
      )
      .orderBy(asc(work_run_event.seq)),
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
