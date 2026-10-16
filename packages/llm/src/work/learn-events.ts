import { and, db, desc, eq, gte, inArray, observation, sql } from "@neko/db";
import { LESSON_CONSUMER, type LessonDetail } from "./learn-lessons";

/** Notable learning events an org records per UTC day, at most. */
export const MAX_LEARNING_EVENTS_PER_DAY = 5;
export const LEARNING_EVENT_CONSUMER = "learning_event";

export type LearningEventKind = "skill_created" | "rolled_back" | "outcome_change" | "budget_hit";

export type LearningEventResult =
  | { recorded: true; observationId: string }
  | { recorded: false; reason: "duplicate" | "daily_cap" };

export function utcDay(now: Date): string {
  return now.toISOString().slice(0, 10);
}

/**
 * Records one notable learning event for the admin learning settings. Each
 * event key records once, and an org records at most
 * MAX_LEARNING_EVENTS_PER_DAY a day. Nothing goes to chat or channels.
 */
export async function recordLearningEvent(input: {
  orgId: string;
  kind: LearningEventKind;
  key: string;
  title: string;
  body: string;
  mood?: "good" | "watch" | "act" | null;
  now?: Date;
  maxPerDay?: number;
}): Promise<LearningEventResult> {
  const now = input.now ?? new Date();
  const dedupeKey = `event:${input.kind}:${input.key}`;
  const dayStart = new Date(`${utcDay(now)}T00:00:00.000Z`);
  return db().transaction(async (tx) => {
    await tx.execute(sql`select pg_advisory_xact_lock(hashtext(${input.orgId}), hashtext('learning-events'))`);
    const [seen] = await tx
      .select({ id: observation.id })
      .from(observation)
      .where(and(eq(observation.org_id, input.orgId), eq(observation.dedupe_key, dedupeKey)))
      .limit(1);
    if (seen) return { recorded: false, reason: "duplicate" } as const;
    const [today] = await tx
      .select({ count: sql<number>`count(*)::int` })
      .from(observation)
      .where(
        and(
          eq(observation.org_id, input.orgId),
          eq(observation.consumer_kind, LEARNING_EVENT_CONSUMER),
          gte(observation.created_at, dayStart),
        ),
      );
    if ((today?.count ?? 0) >= (input.maxPerDay ?? MAX_LEARNING_EVENTS_PER_DAY)) {
      return { recorded: false, reason: "daily_cap" } as const;
    }
    const [row] = await tx
      .insert(observation)
      .values({
        org_id: input.orgId,
        consumer_kind: LEARNING_EVENT_CONSUMER,
        title: input.title,
        body: input.body,
        mood: input.mood ?? null,
        detail: { type: LEARNING_EVENT_CONSUMER, kind: input.kind, key: input.key },
        dedupe_key: dedupeKey,
        created_at: now,
        updated_at: now,
        first_seen_at: now,
        last_seen_at: now,
      })
      .returning({ id: observation.id });
    return { recorded: true, observationId: row!.id } as const;
  });
}

/** Never lets an event failure break learning. */
export async function recordLearningEventQuietly(input: Parameters<typeof recordLearningEvent>[0]): Promise<void> {
  await recordLearningEvent(input).catch((error: unknown) => {
    console.warn(`[learn-events] ${input.kind} failed: ${error instanceof Error ? error.message : error}`);
  });
}

export type LearningEventLine = {
  id: string;
  kind: LearningEventKind | "rule_learned";
  line: string;
  at: Date;
  /** Set when the line is a lesson an admin can dismiss. */
  lessonId: string | null;
  status: string;
};

const LINE_LIMIT = 20;

/**
 * Recent notable learning events, newest first: recorded events and learned
 * rules. One plain line each; a learned rule line can be dismissed.
 */
export async function listLearningEvents(orgId: string, limit = LINE_LIMIT): Promise<LearningEventLine[]> {
  const rows = await db()
    .select({
      id: observation.id,
      consumerKind: observation.consumer_kind,
      title: observation.title,
      body: observation.body,
      status: observation.status,
      detail: observation.detail,
      at: observation.last_seen_at,
    })
    .from(observation)
    .where(
      and(
        eq(observation.org_id, orgId),
        inArray(observation.consumer_kind, [LEARNING_EVENT_CONSUMER, LESSON_CONSUMER]),
        sql`(${observation.consumer_kind} = ${LEARNING_EVENT_CONSUMER}
          or ${observation.detail}->>'verdict' = 'promoted')`,
      ),
    )
    .orderBy(desc(observation.last_seen_at))
    .limit(Math.min(Math.max(limit, 1), 100));
  const seen = new Set<string>();
  const lines: LearningEventLine[] = [];
  for (const row of rows) {
    if (row.consumerKind === LESSON_CONSUMER) {
      const detail = row.detail as unknown as LessonDetail;
      const key = `lesson:${row.id}`;
      if (seen.has(key)) continue;
      seen.add(key);
      lines.push({
        id: row.id,
        kind: "rule_learned",
        line: `Learned a rule for ${detail.targetKind} ${detail.targetId}: ${row.body ?? ""}`,
        at: row.at,
        lessonId: row.id,
        status: row.status,
      });
    } else {
      const detail = row.detail as { kind?: LearningEventKind; key?: string };
      const key = `${detail.kind}:${row.title}`;
      if (seen.has(key)) continue;
      seen.add(key);
      lines.push({
        id: row.id,
        kind: detail.kind ?? "rolled_back",
        line: [row.title, row.body].filter(Boolean).join(" "),
        at: row.at,
        lessonId: null,
        status: row.status,
      });
    }
  }
  return lines;
}
