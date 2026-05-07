import "server-only";

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
import type { AgentBackendId, WorkEvent } from "@neko/llm";

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
  eventsByRun: Record<string, WorkEvent[]>;
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
      status: "running",
    })
    .returning();
  return rows[0];
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

export async function appendWorkRunEvent(args: {
  orgId: string;
  threadId: string;
  runId: string;
  seq: number;
  event: WorkEvent;
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

export async function getWorkRunEvents(orgId: string, runId: string): Promise<WorkEvent[]> {
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
  return rows.map((row) => row.payload as WorkEvent);
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

  const eventsByRun: Record<string, WorkEvent[]> = {};
  for (const row of events) {
    if (!eventsByRun[row.runId]) eventsByRun[row.runId] = [];
    eventsByRun[row.runId].push(row.payload as WorkEvent);
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
