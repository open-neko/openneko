import { sql } from "@neko/db";

type SQL = ReturnType<typeof sql>;

/**
 * Matches any word of the query. plainto_tsquery joins words with AND, which
 * misses most questions, so the ANDs become ORs and ts_rank_cd ranks rows
 * that match more words first.
 */
export function keywordTsQuery(query: string): SQL {
  return sql`nullif(replace(plainto_tsquery('english', ${query})::text, ' & ', ' | '), '')::tsquery`;
}

/** Alternates semantic hits with keyword hits on rows that have no current vector. */
export function interleaveResults<T>(
  semantic: ReadonlyArray<T>,
  unindexed: ReadonlyArray<T>,
  key: (item: T) => string,
  limit: number,
): T[] {
  if (unindexed.length === 0) return semantic.slice(0, limit);
  const out: T[] = [];
  const seen = new Set<string>();
  const push = (item: T | undefined) => {
    if (item === undefined || out.length >= limit) return;
    const id = key(item);
    if (seen.has(id)) return;
    seen.add(id);
    out.push(item);
  };
  for (let i = 0; i < Math.max(semantic.length, unindexed.length) && out.length < limit; i++) {
    push(semantic[i]);
    push(unindexed[i]);
  }
  return out;
}
