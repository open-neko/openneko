import { detectMutationLoop } from "./cycle-detection";
import {
  createSubscription,
  getDataSourceForOrg,
  saveWorkflow,
  upsertWorkflowSourceChangeSubscription,
  versionWorkflowDefinition,
  type SaveWorkflowInput,
  type SaveWorkflowResult,
  type SubscriptionRecord,
} from "./store";
import { db } from "@neko/db";
import { parseSourceChangeFilter } from "./subscription-query";
import { upsertWatcher, type WatcherRecord } from "./watchers";

export type WorkflowTriggerError = { code: string; message: string };

export type SaveWorkflowWithTriggerResult = SaveWorkflowResult & {
  subscription?: SubscriptionRecord;
  watcher?: WatcherRecord;
  triggerError?: WorkflowTriggerError;
};

export class HarnessWorkflowValidationError extends Error {
  constructor(public readonly code: string, message: string) {
    super(message);
    this.name = "HarnessWorkflowValidationError";
  }
}

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

  // OL4: a `watch` condition trigger persists a watcher tied to the
  // workflow (the sweep fires it when the condition holds).
  const watch = input.triggers?.watch;
  let watcherRecord: WatcherRecord | undefined;
  if (watch) {
    try {
      watcherRecord = await upsertWatcher({
        orgId: input.orgId,
        workflowId: saved.workflow.id,
        name: saved.workflow.name,
        description: `Watcher for workflow "${saved.workflow.name}"`,
        query: watch.query,
        valuePath: watch.value_path,
        op: watch.op,
        threshold: watch.threshold,
        cadenceSeconds: watch.cadence_seconds,
        debounceSeconds: watch.debounce_seconds,
        severity: watch.severity,
      });
    } catch (err) {
      return {
        ...saved,
        triggerError: {
          code: "invalid_watcher",
          message: err instanceof Error ? err.message : String(err),
        },
      };
    }
  }

  const when = input.triggers?.when;
  if (!when) return watcherRecord ? { ...saved, watcher: watcherRecord } : saved;

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

  return {
    ...saved,
    subscription,
    ...(watcherRecord ? { watcher: watcherRecord } : {}),
  };
}

/** Harness treats a definition and its requested triggers as one database
 * mutation. Known validation failures roll the whole transaction back; an
 * unknown commit/receipt failure is left unresolved by the host journal. */
export async function saveHarnessWorkflowWithTrigger(
  input: SaveWorkflowInput,
): Promise<SaveWorkflowWithTriggerResult> {
  const result = await db().transaction(async (tx) => {
    const saved = await saveWorkflow(input, tx, false);
    const watch = input.triggers?.watch;
    let watcherRecord: WatcherRecord | undefined;
    if (watch) {
      try {
        watcherRecord = await upsertWatcher({
          orgId: input.orgId,
          workflowId: saved.workflow.id,
          name: saved.workflow.name,
          description: `Watcher for workflow "${saved.workflow.name}"`,
          query: watch.query,
          valuePath: watch.value_path,
          op: watch.op,
          threshold: watch.threshold,
          cadenceSeconds: watch.cadence_seconds,
          debounceSeconds: watch.debounce_seconds,
          severity: watch.severity,
        }, tx, true);
      } catch (error) {
        if (error instanceof Error && error.message === "A watcher with this name belongs to another workflow") {
          throw new HarnessWorkflowValidationError("watcher_name_conflict", error.message);
        }
        throw error;
      }
    }
    const when = input.triggers?.when;
    if (!when) return watcherRecord ? { ...saved, watcher: watcherRecord } : saved;

    const filter = {
      table: when.table,
      where: when.where,
      select: when.select,
      primary_key: when.primary_key,
      version_column: when.version_column,
    };
    const parsed = parseSourceChangeFilter(filter);
    if (!parsed) throw new HarnessWorkflowValidationError("invalid_trigger", "Invalid data-change trigger columns");
    if (!(await getDataSourceForOrg(input.orgId, tx))) {
      throw new HarnessWorkflowValidationError("no_data_source", "A data-change trigger requires a configured source");
    }
    const hasIdempotency = Boolean(when.idempotency_key_template);
    if (!when.acknowledge_mutation_loop && !hasIdempotency) {
      const loop = detectMutationLoop({ filter: parsed, workflow: saved.workflow });
      if (loop.loops) throw new HarnessWorkflowValidationError("mutation_loop", `Data-change trigger may loop: ${loop.reason}`);
    }
    const subscription = await upsertWorkflowSourceChangeSubscription({
      orgId: input.orgId,
      workflowId: saved.workflow.id,
      sourceKind: "source_change",
      filter,
      enabled: when.enabled ?? true,
      idempotencyKeyTemplate: when.idempotency_key_template ?? null,
    }, tx);
    return { ...saved, subscription, ...(watcherRecord ? { watcher: watcherRecord } : {}) };
  });
  await versionWorkflowDefinition(result.workflow, result.action === "created" ? "Added" : "Updated");
  return result;
}
