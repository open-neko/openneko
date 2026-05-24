import { detectMutationLoop } from "./cycle-detection";
import {
  createSubscription,
  getDataSourceForOrg,
  saveWorkflow,
  type SaveWorkflowInput,
  type SaveWorkflowResult,
  type SubscriptionRecord,
} from "./store";
import { parseSourceChangeFilter } from "./subscription-query";

export type WorkflowTriggerError = { code: string; message: string };

export type SaveWorkflowWithTriggerResult = SaveWorkflowResult & {
  subscription?: SubscriptionRecord;
  triggerError?: WorkflowTriggerError;
};

// Single entry point both backends use to save a workflow. When the workflow
// carries a `triggers.when` data condition, it also wires the matching
// subscription row — so "do Y when data X changes" is one save, the same way
// `triggers.cron` makes "do Y on a schedule" one save. The workflow is always
// persisted; a rejected trigger comes back as triggerError so the caller can
// surface it without losing the workflow (re-saving upserts by name).
export async function saveWorkflowWithTrigger(
  input: SaveWorkflowInput,
): Promise<SaveWorkflowWithTriggerResult> {
  const saved = await saveWorkflow(input);
  const when = input.triggers?.when;
  if (!when) return saved;

  const filter = {
    table: when.table,
    where: when.where,
    select: when.select,
    primary_key: when.primary_key,
    version_column: when.version_column,
  };

  const parsed = parseSourceChangeFilter(filter);
  if (!parsed) {
    return {
      ...saved,
      triggerError: {
        code: "invalid_trigger",
        message:
          "data trigger rejected — `table` and `primary_key` must be valid column identifiers and `primary_key` non-empty.",
      },
    };
  }

  const dataSource = await getDataSourceForOrg(input.orgId);
  if (!dataSource) {
    return {
      ...saved,
      triggerError: {
        code: "no_data_source",
        message:
          "no data source is configured for this org — a data-change trigger needs one.",
      },
    };
  }

  const hasIdempotency =
    typeof when.idempotency_key_template === "string" &&
    when.idempotency_key_template.length > 0;
  if (!when.acknowledge_mutation_loop && !hasIdempotency) {
    const loop = detectMutationLoop({ filter: parsed, workflow: saved.workflow });
    if (loop.loops) {
      return {
        ...saved,
        triggerError: { code: "mutation_loop", message: loop.reason },
      };
    }
  }

  const subscription = await createSubscription({
    orgId: input.orgId,
    workflowId: saved.workflow.id,
    sourceKind: "source_change",
    filter,
    enabled: when.enabled ?? true,
    idempotencyKeyTemplate: when.idempotency_key_template ?? null,
  });

  return { ...saved, subscription };
}
