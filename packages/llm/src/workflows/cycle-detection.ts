import { db, sql } from "@neko/db";
import type { WorkflowRecord } from "./store";
import type { SourceChangeFilter } from "./subscription-query";

/**
 * Walk the lineage chain backwards from `producingRunId` and return true
 * if `consumerWorkflowId` appears at any depth. Single recursive CTE so
 * the walk is one DB round trip regardless of depth.
 *
 * Chain hops: workflow_run.triggered_by_observation_id →
 * observation.source_output_id → workflow_output.workflow_run_id →
 * (next) workflow_run. Terminates when an ancestor has no
 * triggered_by_observation_id (manual/cron seed) or any link is null.
 */
export async function isWorkflowInAncestorChain(
  producingRunId: string,
  consumerWorkflowId: string,
  maxDepth = 64,
): Promise<boolean> {
  const result = await db().execute<{ exists: boolean }>(
    sql`with recursive chain as (
      select
        wr.id as run_id,
        wr.workflow_id,
        wr.triggered_by_observation_id,
        0 as depth
      from workflow_run wr
      where wr.id = ${producingRunId}
      union all
      select
        producer.id,
        producer.workflow_id,
        producer.triggered_by_observation_id,
        chain.depth + 1
      from chain
      join observation o on o.id = chain.triggered_by_observation_id
      join workflow_output wo on wo.id = o.source_output_id
      join workflow_run producer on producer.id = wo.workflow_run_id
      where chain.triggered_by_observation_id is not null
        and chain.depth < ${maxDepth}
    )
    select exists (
      select 1 from chain where workflow_id = ${consumerWorkflowId}
    ) as exists`,
  );
  const row = (result.rows ?? result)[0] as { exists: boolean } | undefined;
  return Boolean(row?.exists);
}

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
