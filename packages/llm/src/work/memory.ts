import {
  and,
  asc,
  db,
  desc,
  eq,
  isNull,
  ne,
  sql,
  work_memory,
  work_memory_event,
  work_message,
  work_pending_memory,
} from "@neko/db";
import { embedText, vectorLiteral } from "../embedding";

export const WORK_MEMORY_KINDS = [
  "preference",
  "business_rule",
  "metric_definition",
  "thread_note",
  "correction",
  "company_context",
  "other",
] as const;
export type WorkMemoryKind = (typeof WORK_MEMORY_KINDS)[number];

export const WORK_MEMORY_SCOPES = ["global", "thread", "database"] as const;
export type WorkMemoryScope = (typeof WORK_MEMORY_SCOPES)[number];

export const WORK_PENDING_MEMORY_STATUSES = [
  "proposed",
  "accepted",
  "declined",
] as const;
export type WorkPendingMemoryStatus = (typeof WORK_PENDING_MEMORY_STATUSES)[number];

export type WorkMemoryContext = {
  orgId: string;
  threadId?: string | null;
  runId?: string | null;
};

export type WorkMemory = {
  id: string;
  orgId: string;
  kind: WorkMemoryKind;
  scope: WorkMemoryScope;
  scopeId: string | null;
  text: string;
  pinned: boolean;
  confidence: number;
  metadata: Record<string, unknown>;
  sourceRunId: string | null;
  sourceThreadId: string | null;
  useCount: number;
  lastUsedAt: string | null;
  archivedAt: string | null;
  createdAt: string;
  updatedAt: string;
};

export type RememberWorkMemoryInput = WorkMemoryContext & {
  kind: WorkMemoryKind;
  scope?: WorkMemoryScope;
  scopeId?: string | null;
  text: string;
  pinned?: boolean;
  confidence?: number;
  metadata?: Record<string, unknown>;
};

export type WorkMemorySearchResult = {
  source: "saved_memory";
  memory: WorkMemory;
  score: number;
};

export type WorkArchiveSearchResult = {
  source: "thread_archive" | "run_archive";
  runId: string | null;
  threadId: string;
  role: "user" | "assistant";
  text: string;
  ts: string;
  score: number;
};

export type SearchWorkMemoryInput = WorkMemoryContext & {
  query: string;
  limit?: number;
  includeArchives?: boolean;
};

export function normalizeNewWorkMemoryScope(
  scope: WorkMemoryScope | undefined,
  ctx: { threadId?: string | null } = {},
): "global" | "thread" {
  if (scope === "thread" && ctx.threadId) return "thread";
  return "global";
}

export type WorkPendingMemoryConflict = {
  memoryId: string;
  text: string;
  similarity: number;
};

export type WorkPendingMemory = {
  id: string;
  orgId: string;
  threadId: string | null;
  runId: string | null;
  status: WorkPendingMemoryStatus;
  draftText: string;
  draftKind: WorkMemoryKind;
  draftScope: WorkMemoryScope;
  draftScopeId: string | null;
  confidence: number;
  reasoning: string | null;
  conflicts: WorkPendingMemoryConflict[];
  decisionText: string | null;
  decidedAt: string | null;
  memoryId: string | null;
  createdAt: string;
  updatedAt: string;
};

type WorkMemoryRow = typeof work_memory.$inferSelect;
type WorkPendingMemoryRow = typeof work_pending_memory.$inferSelect;

const CORE_GLOBAL_KINDS = new Set<WorkMemoryKind>([
  "preference",
  "business_rule",
  "metric_definition",
  "company_context",
]);

const CORE_THREAD_KINDS = new Set<WorkMemoryKind>([
  "business_rule",
  "metric_definition",
  "thread_note",
  "correction",
]);

export async function rememberWorkMemory(input: RememberWorkMemoryInput): Promise<WorkMemory> {
  assertKind(input.kind);
  const scope = normalizeNewWorkMemoryScope(input.scope, {
    threadId: input.threadId ?? null,
  });
  const now = new Date();
  const scopeId = resolveScopeId(scope, input);
  const pinned = input.pinned ?? shouldPinByDefault(input.kind, scope);
  const text = input.text.trim();
  // Embed up front so search-by-context can find it on the very next query.
  // Failure here is not fatal — we'd rather store the memory than lose it.
  let embedding: string | null = null;
  try {
    const vec = await embedText(text);
    embedding = vectorLiteral(vec);
  } catch (err) {
    console.error(
      "[work-memory] embedding failed; storing memory without vector:",
      err instanceof Error ? err.message : err,
    );
  }
  const rows = await db()
    .insert(work_memory)
    .values({
      org_id: input.orgId,
      kind: input.kind,
      scope,
      scope_id: scopeId,
      text,
      pinned,
      confidence: clamp(input.confidence ?? 0.8, 0, 1),
      metadata: input.metadata ?? {},
      source_run_id: input.runId ?? null,
      source_thread_id: input.threadId ?? null,
      ...(embedding ? { embedding: sql`${embedding}::vector` } : {}),
      created_at: now,
      updated_at: now,
    })
    .returning();
  const memory = rowToMemory(rows[0]);
  await insertWorkMemoryEvent({
    orgId: input.orgId,
    memoryId: memory.id,
    runId: input.runId ?? null,
    threadId: input.threadId ?? null,
    action: "remember",
    payload: { kind: input.kind, scope, scopeId, pinned, text: input.text.trim() },
  });
  return memory;
}

export async function archiveWorkMemory(
  orgId: string,
  id: string,
  ctx: Omit<WorkMemoryContext, "orgId"> & { reason?: string } = {},
): Promise<boolean> {
  const now = new Date();
  const rows = await db()
    .update(work_memory)
    .set({ archived_at: now, updated_at: now })
    .where(and(eq(work_memory.org_id, orgId), eq(work_memory.id, id), isNull(work_memory.archived_at)))
    .returning({ id: work_memory.id });
  if (!rows[0]) return false;
  await insertWorkMemoryEvent({
    orgId,
    memoryId: id,
    runId: ctx.runId ?? null,
    threadId: ctx.threadId ?? null,
    action: "forget",
    payload: { reason: ctx.reason ?? null },
  });
  return true;
}

export async function getWorkMemory(orgId: string, id: string): Promise<WorkMemory | null> {
  const rows = await db()
    .select()
    .from(work_memory)
    .where(and(eq(work_memory.org_id, orgId), eq(work_memory.id, id)))
    .limit(1);
  return rows[0] ? rowToMemory(rows[0]) : null;
}

export async function listWorkMemories(
  orgId: string,
  options: { includeArchived?: boolean; limit?: number } = {},
): Promise<WorkMemory[]> {
  const activeFilter = options.includeArchived ? undefined : isNull(work_memory.archived_at);
  const rows = await db()
    .select()
    .from(work_memory)
    .where(activeFilter ? and(eq(work_memory.org_id, orgId), activeFilter) : eq(work_memory.org_id, orgId))
    .orderBy(desc(work_memory.pinned), desc(work_memory.updated_at))
    .limit(options.limit ?? 200);
  return rows.map(rowToMemory);
}

export async function getCoreWorkMemories(
  ctx: WorkMemoryContext,
  limit = 16,
): Promise<WorkMemory[]> {
  const rows = await db()
    .select()
    .from(work_memory)
    .where(and(eq(work_memory.org_id, ctx.orgId), isNull(work_memory.archived_at)))
    .orderBy(desc(work_memory.pinned), desc(work_memory.updated_at))
    .limit(500);

  return rows
    .map(rowToMemory)
    .filter((memory) => {
      if (memory.pinned) return true;
      if (memory.scope === "global" && CORE_GLOBAL_KINDS.has(memory.kind)) return true;
      if (
        memory.scope === "thread" &&
        memory.scopeId === ctx.threadId &&
        CORE_THREAD_KINDS.has(memory.kind)
      ) {
        return true;
      }
      return false;
    })
    .slice(0, limit);
}

export async function formatWorkMemoryPromptContext(
  ctx: WorkMemoryContext,
  options: { contextQuery?: string; contextLimit?: number } = {},
): Promise<string> {
  const core = await getCoreWorkMemories(ctx);
  // When the caller hands us a contextQuery (the user's latest message,
  // a card title, a workflow intent, etc.) pull the top-N semantically
  // closest memories via pgvector. Merge with core (pinned + kind-based)
  // by id so we never repeat a memory.
  const seen = new Set(core.map((m) => m.id));
  const contextual = options.contextQuery?.trim()
    ? (
        await searchWorkMemoryByContext({
          orgId: ctx.orgId,
          query: options.contextQuery,
          limit: options.contextLimit ?? 5,
        })
      )
        .map((r) => r.memory)
        .filter((m) => !seen.has(m.id))
    : [];

  const merged = [...core, ...contextual];
  if (merged.length === 0) {
    return "No memories are currently saved for this workspace or thread.";
  }
  const lines = [
    "Memories the operator has saved — treat as durable context:",
    ...merged.map(
      (memory) => `- [${memory.id}] ${formatMemoryLabel(memory)}: ${memory.text}`,
    ),
  ];
  return lines.join("\n");
}

export async function searchWorkMemory(
  input: SearchWorkMemoryInput,
): Promise<{ saved: WorkMemorySearchResult[]; archives: WorkArchiveSearchResult[] }> {
  const limit = clamp(Math.floor(input.limit ?? 8), 1, 20);
  const tokens = tokenize(input.query);
  const saved = await searchSavedWorkMemories(input, tokens, limit);
  const archives =
    input.includeArchives === false
      ? []
      : await searchWorkArchiveMessages(input, tokens, limit);

  if (saved.length > 0) {
    await touchWorkMemories(
      input,
      saved.map((result) => result.memory.id),
    );
  }

  return { saved, archives };
}

// Semantic search via pgvector. Embeds the query with the same
// transformers.js model used at write time and pulls the N nearest
// non-archived memories by cosine distance. Returned `score` is the
// cosine similarity (1 - distance), so higher is better.
export async function searchWorkMemoryByContext(args: {
  orgId: string;
  query: string;
  limit?: number;
}): Promise<WorkMemorySearchResult[]> {
  const limit = clamp(Math.floor(args.limit ?? 5), 1, 20);
  const trimmed = args.query.trim();
  if (!trimmed) return [];
  let queryVec: string;
  try {
    queryVec = vectorLiteral(await embedText(trimmed));
  } catch (err) {
    console.error(
      "[work-memory] context search embed failed:",
      err instanceof Error ? err.message : err,
    );
    return [];
  }
  // Drizzle's db().execute() against node-postgres returns the driver's
  // QueryResult ({ rows, rowCount, ... }) — not the rows directly.
  const result = await db().execute(sql`
    SELECT id, org_id, kind, scope, scope_id, text, pinned, confidence,
           metadata, source_run_id, source_thread_id, use_count, last_used_at,
           archived_at, created_at, updated_at,
           1 - (embedding <=> ${queryVec}::vector) AS score
      FROM work_memory
     WHERE org_id = ${args.orgId}
       AND archived_at IS NULL
       AND embedding IS NOT NULL
     ORDER BY embedding <=> ${queryVec}::vector
     LIMIT ${limit}
  `);
  const rows = (result as unknown as { rows?: Array<Record<string, unknown> & { score: number }> }).rows ?? [];
  if (rows.length === 0) return [];
  await touchWorkMemories({ orgId: args.orgId }, rows.map((r) => String(r.id)));
  return rows.map((r) => ({
    source: "saved_memory" as const,
    memory: rowToMemory(r as unknown as Parameters<typeof rowToMemory>[0]),
    score: Number(r.score) || 0,
  }));
}

export async function findConflictingWorkMemories(
  draft: {
    orgId: string;
    text: string;
    kind: WorkMemoryKind;
    scope: WorkMemoryScope;
    scopeId?: string | null;
  },
  threshold = 0.3,
): Promise<Array<{ memory: WorkMemory; similarity: number }>> {
  assertKind(draft.kind);
  assertScope(draft.scope);
  const scopedById = draft.scope === "thread";
  const rows = await db()
    .select()
    .from(work_memory)
    .where(
      scopedById
        ? and(
            eq(work_memory.org_id, draft.orgId),
            eq(work_memory.kind, draft.kind),
            eq(work_memory.scope, draft.scope),
            eq(work_memory.scope_id, draft.scopeId ?? ""),
            isNull(work_memory.archived_at),
          )
        : and(
            eq(work_memory.org_id, draft.orgId),
            eq(work_memory.kind, draft.kind),
            eq(work_memory.scope, draft.scope),
            isNull(work_memory.archived_at),
          ),
    );

  const draftTokens = new Set(tokenize(draft.text));
  if (draftTokens.size === 0) return [];

  return rows
    .map((row) => {
      const memory = rowToMemory(row);
      const memoryTokens = new Set(tokenize(memory.text));
      return { memory, similarity: jaccard(draftTokens, memoryTokens) };
    })
    .filter((result) => result.similarity >= threshold)
    .sort((a, b) => b.similarity - a.similarity);
}

export async function createPendingWorkMemory(input: {
  orgId: string;
  threadId?: string | null;
  runId?: string | null;
  draftText: string;
  draftKind: WorkMemoryKind;
  draftScope: WorkMemoryScope;
  draftScopeId?: string | null;
  confidence: number;
  reasoning?: string | null;
  conflicts?: WorkPendingMemoryConflict[];
}): Promise<WorkPendingMemory> {
  assertKind(input.draftKind);
  assertScope(input.draftScope);
  const draftScope = normalizeNewWorkMemoryScope(input.draftScope, {
    threadId: input.threadId ?? null,
  });
  const draftScopeId =
    draftScope === "thread" ? input.threadId ?? input.draftScopeId ?? null : null;
  const rows = await db()
    .insert(work_pending_memory)
    .values({
      org_id: input.orgId,
      thread_id: input.threadId ?? null,
      run_id: input.runId ?? null,
      draft_text: input.draftText.trim(),
      draft_kind: input.draftKind,
      draft_scope: draftScope,
      draft_scope_id: draftScopeId,
      confidence: clamp(input.confidence, 0, 1),
      reasoning: input.reasoning ?? null,
      conflict: input.conflicts && input.conflicts.length > 0 ? input.conflicts : null,
    })
    .returning();
  return rowToPending(rows[0]);
}

export async function listPendingWorkMemories(input: {
  orgId: string;
  threadId?: string | null;
  runId?: string | null;
  status?: WorkPendingMemoryStatus;
  limit?: number;
}): Promise<WorkPendingMemory[]> {
  const status = input.status ?? "proposed";
  const base = and(eq(work_pending_memory.org_id, input.orgId), eq(work_pending_memory.status, status));
  const where = input.runId
    ? and(base, eq(work_pending_memory.run_id, input.runId))
    : input.threadId
      ? and(base, eq(work_pending_memory.thread_id, input.threadId))
      : base;
  const rows = await db()
    .select()
    .from(work_pending_memory)
    .where(where)
    .orderBy(asc(work_pending_memory.created_at))
    .limit(input.limit ?? 200);
  return rows.map(rowToPending);
}

export async function acceptPendingWorkMemory(input: {
  orgId: string;
  id: string;
  text?: string;
  scope?: WorkMemoryScope;
  scopeId?: string | null;
  pinned?: boolean;
}): Promise<{ pending: WorkPendingMemory; memory: WorkMemory }> {
  const pending = await getPendingWorkMemory(input.orgId, input.id);
  if (!pending) throw new Error(`Pending memory not found: ${input.id}`);
  if (pending.status !== "proposed") {
    throw new Error(`Pending memory ${input.id} already ${pending.status}`);
  }
  const scope = input.scope ?? pending.draftScope;
  const normalizedScope = normalizeNewWorkMemoryScope(scope, {
    threadId: pending.threadId,
  });
  const memory = await rememberWorkMemory({
    orgId: input.orgId,
    threadId: pending.threadId,
    runId: pending.runId,
    text: input.text?.trim() || pending.draftText,
    kind: pending.draftKind,
    scope: normalizedScope,
    scopeId: input.scopeId ?? pending.draftScopeId,
    pinned: input.pinned,
    confidence: pending.confidence,
    metadata: { origin: "auto_memory_accepted", pendingId: pending.id },
  });
  const now = new Date();
  await db()
    .update(work_pending_memory)
    .set({
      status: "accepted",
      decision_text: input.text?.trim() ?? null,
      decided_at: now,
      memory_id: memory.id,
      updated_at: now,
    })
    .where(and(eq(work_pending_memory.org_id, input.orgId), eq(work_pending_memory.id, input.id)));
  return { pending: (await getPendingWorkMemory(input.orgId, input.id))!, memory };
}

export async function declinePendingWorkMemory(
  orgId: string,
  id: string,
  reason?: string,
): Promise<WorkPendingMemory> {
  const pending = await getPendingWorkMemory(orgId, id);
  if (!pending) throw new Error(`Pending memory not found: ${id}`);
  if (pending.status !== "proposed") {
    throw new Error(`Pending memory ${id} already ${pending.status}`);
  }
  const now = new Date();
  await db()
    .update(work_pending_memory)
    .set({
      status: "declined",
      decision_text: reason ?? null,
      decided_at: now,
      updated_at: now,
    })
    .where(and(eq(work_pending_memory.org_id, orgId), eq(work_pending_memory.id, id)));
  return (await getPendingWorkMemory(orgId, id))!;
}

async function getPendingWorkMemory(
  orgId: string,
  id: string,
): Promise<WorkPendingMemory | null> {
  const rows = await db()
    .select()
    .from(work_pending_memory)
    .where(and(eq(work_pending_memory.org_id, orgId), eq(work_pending_memory.id, id)))
    .limit(1);
  return rows[0] ? rowToPending(rows[0]) : null;
}

async function searchSavedWorkMemories(
  input: SearchWorkMemoryInput,
  tokens: string[],
  limit: number,
): Promise<WorkMemorySearchResult[]> {
  const rows = await db()
    .select()
    .from(work_memory)
    .where(and(eq(work_memory.org_id, input.orgId), isNull(work_memory.archived_at)))
    .orderBy(desc(work_memory.pinned), desc(work_memory.updated_at))
    .limit(1000);
  const query = input.query.trim();
  return rows
    .map(rowToMemory)
    .map((memory) => ({ source: "saved_memory" as const, memory, score: scoreMemory(memory, query, tokens, input) }))
    .filter((result) => result.score > 0 || tokens.length === 0)
    .sort((a, b) => b.score - a.score || b.memory.updatedAt.localeCompare(a.memory.updatedAt))
    .slice(0, limit);
}

async function searchWorkArchiveMessages(
  input: SearchWorkMemoryInput,
  tokens: string[],
  limit: number,
): Promise<WorkArchiveSearchResult[]> {
  if (tokens.length === 0) return [];
  const rows = await db()
    .select({
      runId: work_message.run_id,
      threadId: work_message.thread_id,
      role: work_message.role,
      content: work_message.content,
      createdAt: work_message.created_at,
    })
    .from(work_message)
    .where(
      input.runId
        ? and(eq(work_message.org_id, input.orgId), ne(work_message.run_id, input.runId))
        : eq(work_message.org_id, input.orgId),
    )
    .orderBy(desc(work_message.created_at))
    .limit(500);

  return rows
    .map((row) => {
      const text = row.content.trim();
      if (!text) return null;
      const score = scoreArchive(text, row, input.query, tokens, input);
      if (score <= 0) return null;
      return {
        source: row.runId ? "run_archive" : "thread_archive",
        runId: row.runId,
        threadId: row.threadId,
        role: row.role as "user" | "assistant",
        text: truncate(text, 1200),
        ts: row.createdAt.toISOString(),
        score,
      } satisfies WorkArchiveSearchResult;
    })
    .filter((result): result is WorkArchiveSearchResult => result !== null)
    .sort((a, b) => b.score - a.score || b.ts.localeCompare(a.ts))
    .slice(0, limit);
}

async function touchWorkMemories(ctx: WorkMemoryContext, ids: string[]): Promise<void> {
  const unique = Array.from(new Set(ids));
  if (unique.length === 0) return;
  const now = new Date();
  for (const id of unique) {
    await db()
      .update(work_memory)
      .set({
        use_count: sql`${work_memory.use_count} + 1`,
        last_used_at: now,
      })
      .where(and(eq(work_memory.org_id, ctx.orgId), eq(work_memory.id, id)));
    await insertWorkMemoryEvent({
      orgId: ctx.orgId,
      memoryId: id,
      runId: ctx.runId ?? null,
      threadId: ctx.threadId ?? null,
      action: "search_hit",
      payload: {},
    });
  }
}

async function insertWorkMemoryEvent(input: {
  orgId: string;
  memoryId: string | null;
  runId: string | null;
  threadId: string | null;
  action: string;
  payload: Record<string, unknown>;
}): Promise<void> {
  await db().insert(work_memory_event).values({
    org_id: input.orgId,
    memory_id: input.memoryId,
    run_id: input.runId,
    thread_id: input.threadId,
    action: input.action,
    payload: input.payload,
  });
}

function rowToMemory(row: WorkMemoryRow): WorkMemory {
  return {
    id: row.id,
    orgId: row.org_id,
    kind: row.kind as WorkMemoryKind,
    scope: row.scope as WorkMemoryScope,
    scopeId: row.scope_id,
    text: row.text,
    pinned: row.pinned,
    confidence: row.confidence,
    metadata: asRecord(row.metadata),
    sourceRunId: row.source_run_id,
    sourceThreadId: row.source_thread_id,
    useCount: row.use_count,
    lastUsedAt: row.last_used_at ? row.last_used_at.toISOString() : null,
    archivedAt: row.archived_at ? row.archived_at.toISOString() : null,
    createdAt: row.created_at.toISOString(),
    updatedAt: row.updated_at.toISOString(),
  };
}

function rowToPending(row: WorkPendingMemoryRow): WorkPendingMemory {
  return {
    id: row.id,
    orgId: row.org_id,
    threadId: row.thread_id,
    runId: row.run_id,
    status: row.status as WorkPendingMemoryStatus,
    draftText: row.draft_text,
    draftKind: row.draft_kind as WorkMemoryKind,
    draftScope: row.draft_scope as WorkMemoryScope,
    draftScopeId: row.draft_scope_id,
    confidence: row.confidence,
    reasoning: row.reasoning,
    conflicts: Array.isArray(row.conflict) ? (row.conflict as WorkPendingMemoryConflict[]) : [],
    decisionText: row.decision_text,
    decidedAt: row.decided_at ? row.decided_at.toISOString() : null,
    memoryId: row.memory_id,
    createdAt: row.created_at.toISOString(),
    updatedAt: row.updated_at.toISOString(),
  };
}

function scoreMemory(
  memory: WorkMemory,
  query: string,
  tokens: string[],
  ctx: WorkMemoryContext,
): number {
  let score = scoreText(memory.text, query, tokens);
  score += scoreText(`${memory.kind} ${memory.scope} ${memory.scopeId ?? ""}`, query, tokens) * 0.4;
  if (memory.pinned) score += 3;
  if (memory.scope === "global") score += 1;
  if (memory.scope === "thread" && memory.scopeId === ctx.threadId) score += 3;
  return score;
}

function scoreArchive(
  text: string,
  row: { threadId: string; role: string },
  query: string,
  tokens: string[],
  ctx: WorkMemoryContext,
): number {
  let score = scoreText(text, query, tokens);
  if (row.threadId === ctx.threadId) score += 2;
  if (row.role === "user") score += 1;
  return score;
}

function scoreText(text: string, query: string, tokens: string[]): number {
  const lower = text.toLowerCase();
  const phrase = query.trim().toLowerCase();
  let score = phrase && lower.includes(phrase) ? 8 : 0;
  for (const token of tokens) {
    if (lower.includes(token)) score += 2;
  }
  return score;
}

function formatMemoryLabel(memory: WorkMemory): string {
  const scope = memory.scopeId ? `${memory.scope}:${memory.scopeId}` : memory.scope;
  return `${memory.kind} (${scope})`;
}

function resolveScopeId(scope: "global" | "thread", input: WorkMemoryContext & { scopeId?: string | null }): string | null {
  if (scope === "global") return null;
  if (input.scopeId) return input.scopeId;
  if (scope === "thread") return input.threadId ?? null;
  return null;
}

function shouldPinByDefault(kind: WorkMemoryKind, scope: WorkMemoryScope): boolean {
  if (scope === "thread") return false;
  return kind === "preference" || kind === "business_rule" || kind === "metric_definition";
}

function tokenize(query: string): string[] {
  const seen = new Set<string>();
  const raw = query.toLowerCase().match(/[a-z0-9][a-z0-9_-]+/g) ?? [];
  for (const token of raw) {
    if (token.length >= 2) seen.add(token);
    if (seen.size >= 12) break;
  }
  return Array.from(seen);
}

function jaccard(a: Set<string>, b: Set<string>): number {
  if (a.size === 0 && b.size === 0) return 0;
  let intersection = 0;
  for (const token of a) if (b.has(token)) intersection += 1;
  const union = a.size + b.size - intersection;
  return union === 0 ? 0 : intersection / union;
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max - 3)}...`;
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function assertKind(kind: string): asserts kind is WorkMemoryKind {
  if (!WORK_MEMORY_KINDS.includes(kind as WorkMemoryKind)) {
    throw new Error(`Invalid work memory kind: ${kind}`);
  }
}

function assertScope(scope: string): asserts scope is WorkMemoryScope {
  if (!WORK_MEMORY_SCOPES.includes(scope as WorkMemoryScope)) {
    throw new Error(`Invalid work memory scope: ${scope}`);
  }
}

function clamp(n: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, n));
}
