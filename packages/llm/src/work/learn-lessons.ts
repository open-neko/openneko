import { createHash } from "node:crypto";
import { and, db, desc, eq, inArray, observation, observation_run, sql, work_run } from "@neko/db";
import type { LearnTarget } from "./learn-target";
import type { SkillLearnResult } from "./skill-learn";
import { normalizeRuleText, type PlaybookOperation } from "./skill-learn-playbook";

/** Observation consumer kind for a candidate lesson the learner judged. */
export const LESSON_CONSUMER = "learning";
const MAX_HISTORY = 20;

export type LessonVerdict = "promoted" | "dropped" | "rolled_back";

export type Lesson = {
  text: string;
  verdict: LessonVerdict;
  reason: string;
  runIds: string[];
  quotes: string[];
  ruleId?: string;
};

export type LessonDetail = {
  type: "lesson";
  targetKind: string;
  targetId: string;
  ruleId?: string;
  verdict: LessonVerdict;
  reason: string;
  /** The store version of the latest verdict. */
  version: string | null;
  /** The store version the lesson first entered, when it was promoted. */
  enteredVersion: string | null;
  eventId: string | null;
  runIds: string[];
  quotes: string[];
  history: Array<{ verdict: LessonVerdict; reason: string; version: string | null; at: string }>;
  dismissedBy?: string | null;
};

function cleanText(text: string | undefined): string {
  return (text ?? "").replace(/\s+/g, " ").trim();
}

function fromOperation(operation: PlaybookOperation): Pick<Lesson, "text" | "runIds" | "quotes"> {
  return {
    text: cleanText(operation.text),
    runIds: operation.evidenceRunIds ?? [],
    quotes: operation.evidenceQuotes ?? [],
  };
}

/** Every rule operation the gates judged, with its verdict. Removals are not lessons. */
export function lessonsFromResult(result: SkillLearnResult): Lesson[] {
  const trace = result.trace;
  const lessons: Lesson[] = [];
  const rules = new Map((result.rules ?? []).map((rule) => [normalizeRuleText(rule.text), rule]));
  const textOps = (ops: readonly PlaybookOperation[] | undefined) =>
    (ops ?? []).filter((op) => op.op !== "remove" && cleanText(op.text));

  if (result.decision === "applied") {
    for (const op of textOps(trace.applied)) {
      const base = fromOperation(op);
      const rule = rules.get(normalizeRuleText(base.text));
      lessons.push({
        ...base,
        ...(rule ? { runIds: rule.evidenceRunIds, quotes: rule.evidenceQuotes, ruleId: rule.id } : {}),
        verdict: "promoted",
        reason: result.reason,
      });
    }
  }
  if (result.decision === "rejected") {
    for (const op of textOps(trace.applied)) {
      lessons.push({ ...fromOperation(op), verdict: "dropped", reason: `batch rejected: ${result.reason}` });
    }
  }
  for (const dropped of [...(trace.dropped ?? []), ...(trace.unsafe ?? [])]) {
    if (dropped.operation.op === "remove" || !cleanText(dropped.operation.text)) continue;
    lessons.push({ ...fromOperation(dropped.operation), verdict: "dropped", reason: dropped.reason });
  }
  if (result.decision === "rolled_back") {
    for (const rule of trace.rules ?? []) {
      lessons.push({
        text: cleanText(rule.text),
        verdict: "rolled_back",
        reason: result.reason,
        runIds: rule.evidenceRunIds,
        quotes: rule.evidenceQuotes,
        ruleId: rule.id,
      });
    }
  }
  return lessons.filter((lesson) => lesson.text);
}

export function lessonDedupeKey(target: LearnTarget | { kind: string; id: string }, text: string): string {
  const digest = createHash("sha256").update(normalizeRuleText(text)).digest("hex").slice(0, 24);
  return `lesson:${target.kind}:${target.id}:${digest}`;
}

const MOOD: Record<LessonVerdict, string | null> = { promoted: "good", dropped: null, rolled_back: "watch" };
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * Records each judged lesson as a learning observation with run lineage. One
 * row exists per target and rule text; later verdicts update it and extend its
 * history. A dismissed lesson stays dismissed.
 */
export async function recordLessons(input: {
  orgId: string;
  target: LearnTarget | { kind: string; id: string };
  lessons: readonly Lesson[];
  decision: string;
  eventId?: string | null;
  currentVersion?: string | null;
  now?: Date;
}): Promise<string[]> {
  const now = input.now ?? new Date();
  const version =
    input.decision === "applied" || input.decision === "rolled_back" || input.decision === "created" || input.decision === "extended"
      ? (input.eventId ?? null)
      : (input.currentVersion ?? null);
  const ids: string[] = [];
  for (const lesson of input.lessons) {
    const dedupeKey = lessonDedupeKey(input.target, lesson.text);
    const [existing] = await db()
      .select({ id: observation.id, detail: observation.detail, status: observation.status })
      .from(observation)
      .where(and(eq(observation.org_id, input.orgId), eq(observation.dedupe_key, dedupeKey)))
      .limit(1);
    const prior = existing?.detail as Partial<LessonDetail> | undefined;
    const detail: LessonDetail = {
      type: "lesson",
      targetKind: input.target.kind,
      targetId: input.target.id,
      ...(lesson.ruleId ?? prior?.ruleId ? { ruleId: lesson.ruleId ?? prior?.ruleId } : {}),
      verdict: lesson.verdict,
      reason: lesson.reason,
      version,
      enteredVersion: prior?.enteredVersion ?? (lesson.verdict === "promoted" ? version : null),
      eventId: input.eventId ?? null,
      runIds: [...new Set([...(prior?.runIds ?? []), ...lesson.runIds])],
      quotes: [...new Set([...(prior?.quotes ?? []), ...lesson.quotes])],
      history: [
        ...(prior?.history ?? []),
        { verdict: lesson.verdict, reason: lesson.reason, version, at: now.toISOString() },
      ].slice(-MAX_HISTORY),
      ...(prior?.dismissedBy ? { dismissedBy: prior.dismissedBy } : {}),
    };
    let id: string;
    if (existing) {
      id = existing.id;
      await db()
        .update(observation)
        .set({ detail, mood: MOOD[lesson.verdict], last_seen_at: now, updated_at: now })
        .where(eq(observation.id, existing.id));
    } else {
      const [row] = await db()
        .insert(observation)
        .values({
          org_id: input.orgId,
          consumer_kind: LESSON_CONSUMER,
          title: lesson.text.length > 120 ? `${lesson.text.slice(0, 119)}…` : lesson.text,
          body: lesson.text,
          mood: MOOD[lesson.verdict],
          detail,
          dedupe_key: dedupeKey,
          first_seen_at: now,
          last_seen_at: now,
          created_at: now,
          updated_at: now,
        })
        .onConflictDoNothing()
        .returning({ id: observation.id });
      if (!row) continue;
      id = row.id;
    }
    ids.push(id);
    const runIds = lesson.runIds.filter((runId) => UUID_RE.test(runId));
    if (runIds.length > 0) {
      const known = await db()
        .select({ id: work_run.id })
        .from(work_run)
        .where(and(eq(work_run.org_id, input.orgId), inArray(work_run.id, runIds)));
      const aligned = lesson.quotes.length === lesson.runIds.length;
      const quoteFor = (runId: string) => (aligned ? (lesson.quotes[lesson.runIds.indexOf(runId)] ?? null) : null);
      if (known.length > 0) {
        await db()
          .insert(observation_run)
          .values(known.map((run) => ({ observation_id: id, run_id: run.id, quote: quoteFor(run.id) })))
          .onConflictDoNothing();
      }
    }
  }
  return ids;
}

/** Records a learner result's lessons; a failure never breaks learning. */
export async function recordResultLessons(
  orgId: string,
  target: LearnTarget,
  result: SkillLearnResult,
  now?: Date,
): Promise<string[]> {
  const lessons = lessonsFromResult(result);
  if (lessons.length === 0) return [];
  return recordLessons({
    orgId,
    target,
    lessons,
    decision: result.decision,
    eventId: result.eventId ?? null,
    currentVersion: result.trace.contentHash ?? null,
    now,
  }).catch((error: unknown) => {
    console.warn(`[learn-lessons] record failed for ${target.kind}:${target.id}: ${error instanceof Error ? error.message : error}`);
    return [];
  });
}

export type LessonRow = {
  id: string;
  text: string;
  status: string;
  detail: LessonDetail;
  runIds: string[];
  firstSeenAt: Date;
  lastSeenAt: Date;
};

export async function listLessons(
  orgId: string,
  options: { status?: string; verdict?: LessonVerdict; limit?: number } = {},
): Promise<LessonRow[]> {
  const rows = await db()
    .select({
      id: observation.id,
      text: observation.body,
      status: observation.status,
      detail: observation.detail,
      firstSeenAt: observation.first_seen_at,
      lastSeenAt: observation.last_seen_at,
      runIds: sql<string[]>`coalesce((select array_agg(r.run_id::text) from ${observation_run} r where r.observation_id = ${observation.id}), '{}')`,
    })
    .from(observation)
    .where(
      and(
        eq(observation.org_id, orgId),
        eq(observation.consumer_kind, LESSON_CONSUMER),
        ...(options.status ? [eq(observation.status, options.status)] : []),
        ...(options.verdict ? [sql`${observation.detail}->>'verdict' = ${options.verdict}`] : []),
      ),
    )
    .orderBy(desc(observation.last_seen_at))
    .limit(Math.min(Math.max(options.limit ?? 100, 1), 500));
  return rows.map((row) => ({ ...row, text: row.text ?? "", detail: row.detail as LessonDetail }));
}

export async function getLesson(orgId: string, lessonId: string): Promise<LessonRow | null> {
  if (!UUID_RE.test(lessonId)) return null;
  const [row] = await db()
    .select({
      id: observation.id,
      text: observation.body,
      status: observation.status,
      detail: observation.detail,
      firstSeenAt: observation.first_seen_at,
      lastSeenAt: observation.last_seen_at,
    })
    .from(observation)
    .where(
      and(eq(observation.org_id, orgId), eq(observation.id, lessonId), eq(observation.consumer_kind, LESSON_CONSUMER)),
    )
    .limit(1);
  return row ? { ...row, text: row.text ?? "", detail: row.detail as LessonDetail, runIds: [] } : null;
}
