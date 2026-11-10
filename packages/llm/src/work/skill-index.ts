import { and, db, eq, inArray, skill_embedding, sql } from "@neko/db";
import { embedText, embeddingModel, vectorLiteral } from "../embedding";
import { entitlementActorForRun, runHeldItemIds } from "./entitlement-scope";
import { interleaveResults, keywordTsQuery } from "./keyword-search";

export type SkillSearchResult = { name: string; score: number };

/** A skill is indexed by its name and description; longer guide text blurs the ranking. */
function skillDocument(skill: { name: string; description: string }): string {
  return [skill.name, skill.description].filter(Boolean).join("\n");
}

/**
 * Keeps the org's skill index in step with its installed skills. A new or
 * changed description clears the vector; the embedding job indexes it in the
 * background when embedding is on, so a chat turn never waits on the model.
 */
export async function syncSkillIndex(
  orgId: string,
  skills: ReadonlyArray<{ name: string; description: string }>,
): Promise<void> {
  const existing = await db()
    .select({ name: skill_embedding.skill_name, document: skill_embedding.document })
    .from(skill_embedding)
    .where(eq(skill_embedding.org_id, orgId));
  const known = new Map(existing.map((row) => [row.name, row.document]));
  const installed = new Set(skills.map((skill) => skill.name));

  for (const skill of skills) {
    const document = skillDocument(skill);
    if (known.get(skill.name) === document) continue;
    await db()
      .insert(skill_embedding)
      .values({ org_id: orgId, skill_name: skill.name, document, embedding: null, embedding_model: null })
      .onConflictDoUpdate({
        target: [skill_embedding.org_id, skill_embedding.skill_name],
        set: { document, embedding: null, embedding_model: null, updated_at: new Date() },
      });
  }
  const removed = existing.map((row) => row.name).filter((name) => !installed.has(name));
  if (removed.length) {
    await db()
      .delete(skill_embedding)
      .where(and(eq(skill_embedding.org_id, orgId), inArray(skill_embedding.skill_name, removed)));
  }
}

async function keywordSkillSearch(
  orgId: string,
  held: string[] | null | undefined,
  query: string,
  limit: number,
  unindexedModel?: string,
): Promise<SkillSearchResult[]> {
  const tsquery = keywordTsQuery(query);
  const document = sql`to_tsvector('english', ${skill_embedding.document})`;
  const rank = sql<number>`ts_rank_cd(${document}, ${tsquery})`;
  const rows = await db()
    .select({ name: skill_embedding.skill_name, rank })
    .from(skill_embedding)
    .where(
      and(
        eq(skill_embedding.org_id, orgId),
        ...(held ? [inArray(skill_embedding.skill_name, held)] : []),
        ...(unindexedModel
          ? [sql`(${skill_embedding.embedding} IS NULL OR ${skill_embedding.embedding_model} IS DISTINCT FROM ${unindexedModel})`]
          : []),
        sql`${document} @@ ${tsquery}`,
      ),
    )
    .orderBy(sql`${rank} desc`, skill_embedding.skill_name)
    .limit(limit);
  return rows.map((row) => ({ name: row.name, score: Number(row.rank) || 0 }));
}

/**
 * The installed skills that best match the query and that the run's actor
 * holds. With embedding on, skills rank by meaning and skills with no current
 * vector are found by keyword. With embedding off or failing, by keyword only.
 */
export async function searchSkillsForRun(input: {
  orgId: string;
  runId: string;
  query: string;
  limit?: number;
}): Promise<SkillSearchResult[]> {
  const query = input.query.trim();
  if (!query) return [];
  const actor = await entitlementActorForRun(input.orgId, input.runId);
  if (!actor) return [];
  const held = await runHeldItemIds(actor, "skill");
  if (held && held.length === 0) return [];
  const limit = Math.min(Math.max(input.limit ?? 3, 1), 10);

  const model = embeddingModel();
  if (!model) return keywordSkillSearch(input.orgId, held, query, limit);
  let vector: string;
  try {
    vector = vectorLiteral(await embedText(query));
  } catch (error) {
    console.warn(
      "[skill-index] embedding unavailable; searching by keyword:",
      error instanceof Error ? error.message : error,
    );
    return keywordSkillSearch(input.orgId, held, query, limit);
  }
  const distance = sql<number>`${skill_embedding.embedding} <=> ${vector}::vector`;
  const [rows, unindexed] = await Promise.all([
    db()
      .select({ name: skill_embedding.skill_name, distance })
      .from(skill_embedding)
      .where(
        and(
          eq(skill_embedding.org_id, input.orgId),
          eq(skill_embedding.embedding_model, model),
          sql`${skill_embedding.embedding} IS NOT NULL`,
          ...(held ? [inArray(skill_embedding.skill_name, held)] : []),
        ),
      )
      .orderBy(distance)
      .limit(limit),
    keywordSkillSearch(input.orgId, held, query, limit, model),
  ]);
  const semantic = rows.map((row) => ({ name: row.name, score: 1 - Number(row.distance) }));
  return interleaveResults(semantic, unindexed, (result) => result.name, limit);
}
