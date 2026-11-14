import { createHash } from "node:crypto";
import { enqueueWorkflowRunFire as defaultEnqueue } from "@neko/db/jobs";
import {
  countWorkflowRunsForSubscription as defaultCountInFlight,
  countWorkflowRunsSince as defaultCountRunsSince,
  createObservation as defaultCreateObservation,
  getWorkflow as defaultGetWorkflow,
  startOfTodayUtc,
  type SubscriptionRecord,
  type WorkflowRecord,
} from "./store";

export type MatchHandlerDecision =
  | { action: "enqueued"; observationId: string; jobId: string | null }
  | { action: "dropped"; reason: string };

export type ExternalEventMatch = {
  /** Event name, e.g. "invoice.paid". */
  name: string;
  /** Emitting system (plugin npm name, watcher id, "api", …). */
  source: string | null;
  payload: Record<string, unknown>;
  /** Caller-supplied dedupe key; defaults to a hash of name+source+payload. */
  dedupeKey?: string;
};

export type HandleExternalEventMatchOptions = {
  subscription: SubscriptionRecord;
  event: ExternalEventMatch;
  /** Override for tests. */
  enqueue?: typeof defaultEnqueue;
  createObservation?: typeof defaultCreateObservation;
  countWorkflowRunsForSubscription?: typeof defaultCountInFlight;
  countWorkflowRunsSince?: typeof defaultCountRunsSince;
  getWorkflow?: typeof defaultGetWorkflow;
};

/**
 * Handle an external_event subscription match: the trigger came from
 * outside the loop entirely (a watcher, a plugin webhook, the admin API),
 * so chain-depth/cycle/fanout don't apply — only the delivery guards
 * (consumer concurrency + daily budget). Writes a consumer-side
 * observation, then enqueues WORKFLOW_RUN_FIRE.
 */
export async function handleExternalEventMatch(
  opts: HandleExternalEventMatchOptions,
): Promise<MatchHandlerDecision> {
  const enqueue = opts.enqueue ?? defaultEnqueue;
  const createObservation = opts.createObservation ?? defaultCreateObservation;
  const countInFlight =
    opts.countWorkflowRunsForSubscription ?? defaultCountInFlight;
  const countRunsSince = opts.countWorkflowRunsSince ?? defaultCountRunsSince;
  const getWorkflow = opts.getWorkflow ?? defaultGetWorkflow;

  const guard = await applyDeliveryGuards({
    subscription: opts.subscription,
    countInFlight,
    countRunsSince,
    getWorkflow,
  });
  if (!guard.allowed) return { action: "dropped", reason: guard.reason };

  const observationRow = await createObservation({
    orgId: opts.subscription.orgId,
    consumerKind: "workflow",
    consumerWorkflowId: opts.subscription.workflowId,
    subscriptionId: opts.subscription.id,
    title: `${opts.event.source ?? "external"}: ${opts.event.name}`,
    body: JSON.stringify(opts.event.payload).slice(0, 4_000),
    mood: null,
  });

  const dedupeKey = buildExternalEventIdempotencyKey(
    opts.subscription,
    opts.event,
  );

  const jobId = await enqueue(
    {
      orgId: opts.subscription.orgId,
      workflowId: opts.subscription.workflowId,
      triggerKind: "subscription" as const,
      triggerPayload: {
        subscription_id: opts.subscription.id,
        observation_id: observationRow.id,
        event_name: opts.event.name,
        event_source: opts.event.source,
        payload: opts.event.payload,
      },
      triggeredBySubscriptionId: opts.subscription.id,
      triggeredByObservationId: observationRow.id,
    },
    { dedupeKey, dedupeSeconds: 3600 },
  );

  return {
    action: "enqueued",
    observationId: observationRow.id,
    jobId,
  };
}

function buildExternalEventIdempotencyKey(
  sub: SubscriptionRecord,
  event: ExternalEventMatch,
): string {
  const eventKey =
    event.dedupeKey ??
    createHash("sha256")
      .update(`${event.name}\0${event.source ?? ""}\0${JSON.stringify(event.payload)}`)
      .digest("hex")
      .slice(0, 16);
  if (sub.idempotencyKeyTemplate) {
    return sub.idempotencyKeyTemplate
      .replace("{subscription_id}", sub.id)
      .replace("{source_record_id}", eventKey)
      .replace("{source_version}", "none");
  }
  return `${sub.id}:${eventKey}`;
}

type GuardArgs = {
  subscription: SubscriptionRecord;
  countInFlight: typeof defaultCountInFlight;
  countRunsSince: typeof defaultCountRunsSince;
  getWorkflow: typeof defaultGetWorkflow;
};

type GuardResult =
  | { allowed: true }
  | { allowed: false; reason: string };

async function applyDeliveryGuards(args: GuardArgs): Promise<GuardResult> {
  const inFlight = await args.countInFlight(args.subscription.id, "running");
  if (inFlight >= args.subscription.maxConcurrentRuns) {
    return {
      allowed: false,
      reason: `subscription ${args.subscription.id} at max_concurrent_runs (${inFlight}/${args.subscription.maxConcurrentRuns})`,
    };
  }
  const consumer: WorkflowRecord | null = await args.getWorkflow(
    args.subscription.orgId,
    args.subscription.workflowId,
  );
  if (consumer?.dailyRunBudget != null) {
    const ranToday = await args.countRunsSince(
      args.subscription.orgId,
      args.subscription.workflowId,
      startOfTodayUtc(),
    );
    if (ranToday >= consumer.dailyRunBudget) {
      return {
        allowed: false,
        reason: `consumer workflow ${args.subscription.workflowId} reached daily_run_budget (${ranToday}/${consumer.dailyRunBudget})`,
      };
    }
  }
  return { allowed: true };
}
