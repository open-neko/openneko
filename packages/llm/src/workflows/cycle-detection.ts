import type { WorkflowRecord } from "./store";
import type { SourceChangeFilter } from "./subscription-query";

export type MutationLoopCheck =
  | { loops: false }
  | { loops: true; reason: string; mutationKeyword: string };

const MUTATION_KEYWORDS = [
  "update",
  "insert",
  "write",
  "mutate",
  "modify",
  "set",
  "delete",
  "upsert",
  "patch",
  "create",
] as const;

/**
 * Save-time mutation-loop heuristic for source_change subscriptions. If the
 * consumer workflow's text (goal + description + steps) mentions both the
 * watched table and a mutation verb, the responder is likely to write back
 * to the table and re-trigger itself. Best-effort — false positives are
 * possible; operators can override by setting idempotency_key_template or
 * acknowledging the warning explicitly.
 */
export function detectMutationLoop(args: {
  filter: SourceChangeFilter;
  workflow: Pick<WorkflowRecord, "goal" | "description" | "steps">;
}): MutationLoopCheck {
  const corpus = [
    args.workflow.goal ?? "",
    args.workflow.description ?? "",
    ...args.workflow.steps.map((s) => s.description),
  ]
    .join("\n")
    .toLowerCase();

  const tableLower = args.filter.table.toLowerCase();
  if (!corpus.includes(tableLower)) return { loops: false };

  const keyword = MUTATION_KEYWORDS.find((w) => corpus.includes(w));
  if (!keyword) return { loops: false };

  return {
    loops: true,
    mutationKeyword: keyword,
    reason: `consumer workflow text mentions "${args.filter.table}" and the mutation verb "${keyword}" — it may write back to the watched table and re-trigger itself. Set idempotency_key_template (e.g. include {primary_key}) or pass acknowledgeMutationLoop: true to confirm.`,
  };
}
