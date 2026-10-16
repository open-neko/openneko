/**
 * Run signals and the internal subscriptions that consume them. A run signal
 * has the same parts as a `subscription` row: a source kind, a consumer and an
 * idempotency key. The `subscription` table needs a consumer workflow, so the
 * internal consumers are declared here until that table can name one.
 */
export type RunSignalKind = "run_finished" | "human_feedback";

export type RunSignal = {
  kind: RunSignalKind;
  orgId: string;
  /** For human_feedback, the finished run the feedback answers. */
  runId: string;
};

export type InternalSubscription = {
  id: string;
  sourceKinds: readonly RunSignalKind[];
  idempotencyKey: (signal: RunSignal) => string;
  deliver: (signal: RunSignal) => Promise<unknown>;
};

export const LEARNING_SUBSCRIPTION: InternalSubscription = {
  id: "learning",
  sourceKinds: ["run_finished", "human_feedback"],
  idempotencyKey: (signal) => `learning:${signal.kind}:${signal.runId}`,
  deliver: async (signal) => {
    const { deliverLearningSignal } = await import("./skill-learn-store");
    return deliverLearningSignal(signal);
  },
};

export const INTERNAL_SUBSCRIPTIONS: readonly InternalSubscription[] = [LEARNING_SUBSCRIPTION];

const DEDUPE_WINDOW_MS = 60_000;
const MAX_REMEMBERED = 5_000;
const delivered = new Map<string, number>();

function firstDelivery(key: string, now: number): boolean {
  const last = delivered.get(key);
  if (last !== undefined && now - last < DEDUPE_WINDOW_MS) return false;
  delivered.set(key, now);
  if (delivered.size > MAX_REMEMBERED) {
    for (const [stale, at] of delivered) if (now - at >= DEDUPE_WINDOW_MS) delivered.delete(stale);
    if (delivered.size > MAX_REMEMBERED) delivered.clear();
  }
  return true;
}

/**
 * Delivers a signal to every internal subscription for its kind. A repeat of
 * the same key inside the window is dropped here; the queues' singleton keys
 * dedupe across workers. A consumer failure never fails the emitter.
 */
export async function emitRunSignal(
  signal: RunSignal,
  subscriptions: readonly InternalSubscription[] = INTERNAL_SUBSCRIPTIONS,
  now = Date.now(),
): Promise<string[]> {
  const reached: string[] = [];
  for (const subscription of subscriptions) {
    if (!subscription.sourceKinds.includes(signal.kind)) continue;
    if (!firstDelivery(subscription.idempotencyKey(signal), now)) continue;
    try {
      await subscription.deliver(signal);
      reached.push(subscription.id);
    } catch (error) {
      console.warn(
        `[run-signal] ${subscription.id} failed on ${signal.kind} for run ${signal.runId}: ${error instanceof Error ? error.message : error}`,
      );
    }
  }
  return reached;
}

export function resetRunSignalDedupe(): void {
  delivered.clear();
}
