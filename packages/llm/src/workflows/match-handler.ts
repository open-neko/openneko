import { enqueue as defaultEnqueue, QUEUE } from "@neko/db/jobs";
import { isWorkflowInAncestorChain as defaultIsCycle } from "./cycle-detection";
import {
  countSubscriptionsMatchingOutput as defaultCountSubsMatching,
  countWorkflowRunsForSubscription as defaultCountInFlight,
  createObservation as defaultCreateObservation,
  type SubscriptionRecord,
} from "./store";
import type { WorkflowOutputMatch } from "./subscription-query";

export type MatchHandlerDecision =
  | { action: "enqueued"; observationId: string; jobId: string | null }
  | { action: "dropped"; reason: string };

export type HandleSubscriptionMatchOptions = {
  subscription: SubscriptionRecord;
  output: WorkflowOutputMatch;
  globalMaxChainDepth?: number;
  globalMaxFanoutPerOutput?: number;
  fanoutWindowMs?: number;
  /** Override for tests. */
  enqueue?: typeof defaultEnqueue;
  createObservation?: typeof defaultCreateObservation;
  countSubscriptionsMatchingOutput?: typeof defaultCountSubsMatching;
  countWorkflowRunsForSubscription?: typeof defaultCountInFlight;
  isWorkflowInAncestorChain?: typeof defaultIsCycle;
  /** Read the chain depth of the producing run. Defaults to a real DB query. */
  resolveProducingRunChainDepth?: (
    workflowRunId: string,
  ) => Promise<number | null>;
};

const DEFAULT_MAX_CHAIN_DEPTH = 20;
const DEFAULT_MAX_FANOUT_PER_OUTPUT = 32;
const DEFAULT_FANOUT_WINDOW_MS = 60_000;

/**
 * Handle a subscription match: enforce loop-safety limits, write a
 * consumer-side observation row, then enqueue a WORKFLOW_RUN_FIRE job
 * for the consuming workflow. Returns the decision so callers can log
 * or assert against it.
 */
export async function handleSubscriptionMatch(
  opts: HandleSubscriptionMatchOptions,
): Promise<MatchHandlerDecision> {
  const enqueue = opts.enqueue ?? defaultEnqueue;
  const createObservation = opts.createObservation ?? defaultCreateObservation;
  const countSubsMatching =
    opts.countSubscriptionsMatchingOutput ?? defaultCountSubsMatching;
  const countInFlight =
    opts.countWorkflowRunsForSubscription ?? defaultCountInFlight;
  const isCycle = opts.isWorkflowInAncestorChain ?? defaultIsCycle;
  const globalMaxChainDepth =
    opts.globalMaxChainDepth ?? DEFAULT_MAX_CHAIN_DEPTH;
  const globalMaxFanout =
    opts.globalMaxFanoutPerOutput ?? DEFAULT_MAX_FANOUT_PER_OUTPUT;
  const fanoutWindowMs = opts.fanoutWindowMs ?? DEFAULT_FANOUT_WINDOW_MS;

  if (opts.output.org_id !== opts.subscription.orgId) {
    return {
      action: "dropped",
      reason: `org mismatch (sub=${opts.subscription.orgId} output=${opts.output.org_id})`,
    };
  }

  const maxChain =
    opts.subscription.maxChainDepthOverride ?? globalMaxChainDepth;
  if (opts.resolveProducingRunChainDepth) {
    const parentDepth = await opts.resolveProducingRunChainDepth(
      opts.output.workflow_run_id,
    );
    if (parentDepth !== null && parentDepth + 1 > maxChain) {
      return {
        action: "dropped",
        reason: `chain depth ${parentDepth + 1} exceeds max ${maxChain}`,
      };
    }
  }

  // Precise cycle check: walk the lineage backwards from the producing
  // run. If the consumer workflow already appears in the chain, firing
  // would close a cycle. Catches multi-workflow loops (A→B→A) and any
  // misauthored self-subscription the save-time check missed.
  const cycle = await isCycle(
    opts.output.workflow_run_id,
    opts.subscription.workflowId,
  );
  if (cycle) {
    return {
      action: "dropped",
      reason: `cycle detected — consumer workflow ${opts.subscription.workflowId} is already in the producing run's chain`,
    };
  }

  const fanout = await countSubsMatching(opts.output.id, fanoutWindowMs);
  if (fanout >= globalMaxFanout) {
    return {
      action: "dropped",
      reason: `fanout cap reached (${fanout}/${globalMaxFanout}) for output ${opts.output.id}`,
    };
  }

  const inFlight = await countInFlight(opts.subscription.id, "running");
  if (inFlight >= opts.subscription.maxConcurrentRuns) {
    return {
      action: "dropped",
      reason: `subscription ${opts.subscription.id} at max_concurrent_runs (${inFlight}/${opts.subscription.maxConcurrentRuns})`,
    };
  }

  const observationRow = await createObservation({
    orgId: opts.subscription.orgId,
    sourceOutputId: opts.output.id,
    consumerKind: "workflow",
    consumerWorkflowId: opts.subscription.workflowId,
    subscriptionId: opts.subscription.id,
    title: opts.output.title || null,
    mood: (opts.output.mood as "good" | "watch" | "act" | null) ?? null,
  });

  const singletonKey = buildIdempotencyKey(opts.subscription, opts.output);

  const jobId = await enqueue(
    QUEUE.WORKFLOW_RUN_FIRE,
    {
      orgId: opts.subscription.orgId,
      workflowId: opts.subscription.workflowId,
      triggerKind: "subscription" as const,
      triggerPayload: {
        subscription_id: opts.subscription.id,
        output_id: opts.output.id,
        observation_id: observationRow.id,
      },
      triggeredBySubscriptionId: opts.subscription.id,
      triggeredByOutputId: opts.output.id,
      triggeredByObservationId: observationRow.id,
    },
    {
      singletonKey,
      singletonHours: 1,
    },
  );

  return {
    action: "enqueued",
    observationId: observationRow.id,
    jobId,
  };
}

function buildIdempotencyKey(
  sub: SubscriptionRecord,
  output: WorkflowOutputMatch,
): string {
  if (sub.idempotencyKeyTemplate) {
    return sub.idempotencyKeyTemplate
      .replace("{subscription_id}", sub.id)
      .replace("{source_record_id}", output.id)
      .replace("{source_version}", output.created_at);
  }
  return `${sub.id}:${output.id}:${output.created_at}`;
}
