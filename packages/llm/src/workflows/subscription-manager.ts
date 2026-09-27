import {
  graphjinSubscribe,
  type GraphjinSubscriptionHandle,
} from "../graphjin/client";
import {
  listEnabledSubscriptions,
  type SubscriptionRecord,
} from "./store";
import {
  buildSubscriptionQuery,
  parseSourceChangeFilter,
  parseSourceChangeMatch,
  parseWorkflowOutputMatch,
  type SourceChangeMatch,
  type WorkflowOutputMatch,
} from "./subscription-query";

export type SubscriptionMatchEvent =
  | {
      kind: "workflow_output";
      subscription: SubscriptionRecord;
      output: WorkflowOutputMatch;
    }
  | {
      kind: "source_change";
      subscription: SubscriptionRecord;
      match: SourceChangeMatch;
    };

export type SubscriptionTransport = {
  baseUrl: string;
};

export type ResolveTransport = (
  sub: SubscriptionRecord,
) => Promise<SubscriptionTransport>;

export type SubscriptionManagerOptions = {
  resolveTransport: ResolveTransport;
  onMatch: (event: SubscriptionMatchEvent) => void | Promise<void>;
  refreshIntervalMs?: number;
  onError?: (err: Error, sub?: SubscriptionRecord) => void;
};

export type SubscriptionManagerHandle = {
  /** Resolves once the initial set of subscriptions is connected. */
  ready: Promise<void>;
  /** Stop all subscriptions and the refresh loop. */
  stop: () => Promise<void>;
  /** Current subscription ids the manager is tracking (test helper). */
  activeSubscriptionIds: () => string[];
  /** Force a refresh now (test helper). */
  refresh: () => Promise<void>;
};

const DEFAULT_REFRESH_INTERVAL_MS = 60_000;

export function startSubscriptionManager(
  opts: SubscriptionManagerOptions,
): SubscriptionManagerHandle {
  const handles = new Map<string, GraphjinSubscriptionHandle>();
  const versions = new Map<string, number>();
  const opening = new Set<string>();
  const retryTimers = new Map<string, NodeJS.Timeout>();
  const retryAttempts = new Map<string, number>();
  let stopping = false;
  let refreshTimer: NodeJS.Timeout | null = null;
  let resolveReady: () => void;
  let rejectReady: (err: Error) => void;
  const ready = new Promise<void>((res, rej) => {
    resolveReady = res;
    rejectReady = rej;
  });

  const closeOne = (id: string) => {
    const handle = handles.get(id);
    if (!handle) return;
    handles.delete(id);
    versions.delete(id);
    handle.stop();
  };

  const scheduleReconnect = (sub: SubscriptionRecord) => {
    if (stopping || retryTimers.has(sub.id)) return;
    const attempt = (retryAttempts.get(sub.id) ?? 0) + 1;
    retryAttempts.set(sub.id, attempt);
    const delay = Math.min(1_000 * 2 ** Math.min(attempt - 1, 5), 30_000);
    const timer = setTimeout(() => {
      retryTimers.delete(sub.id);
      void refresh().catch((err) => opts.onError?.(err instanceof Error ? err : new Error(String(err)), sub));
    }, delay);
    timer.unref();
    retryTimers.set(sub.id, timer);
  };

  const openOne = async (sub: SubscriptionRecord): Promise<void> => {
    opening.add(sub.id);
    try {
      const payload = buildSubscriptionQuery({
        sourceKind: sub.sourceKind,
        filter: sub.filter,
        orgId: sub.orgId,
      });
      if (!payload) {
        console.warn(
          `[subscription-manager] skipping subscription ${sub.id} — source_kind="${sub.sourceKind}" not wired or filter invalid`,
        );
        return;
      }

      let transport: SubscriptionTransport;
      try {
        transport = await opts.resolveTransport(sub);
      } catch (err) {
        opts.onError?.(
          err instanceof Error ? err : new Error(String(err)),
          sub,
        );
        scheduleReconnect(sub);
        return;
      }

      let handle: GraphjinSubscriptionHandle;
      const disconnected = () => {
        if (handles.get(sub.id) !== handle) return;
        closeOne(sub.id);
        scheduleReconnect(sub);
      };
      try {
        handle = graphjinSubscribe<{ data?: unknown } & Record<string, unknown>>({
          baseUrl: transport.baseUrl,
          query: payload.query,
          variables: payload.variables,
          onNext: async (msg) => {
            retryAttempts.delete(sub.id);
            try {
              if (sub.sourceKind === "workflow_output") {
                const match = parseWorkflowOutputMatch(msg);
                if (!match) return;
                await opts.onMatch({
                  kind: "workflow_output",
                  subscription: sub,
                  output: match,
                });
                return;
              }
              if (sub.sourceKind === "source_change") {
                const filter = parseSourceChangeFilter(sub.filter);
                if (!filter) return;
                const match = parseSourceChangeMatch(msg, filter);
                if (!match) return;
                await opts.onMatch({
                  kind: "source_change",
                  subscription: sub,
                  match,
                });
                return;
              }
            } catch (err) {
              opts.onError?.(
                err instanceof Error ? err : new Error(String(err)),
                sub,
              );
            }
          },
          onError: (err) => {
            opts.onError?.(err, sub);
            disconnected();
          },
          onComplete: disconnected,
        });
      } catch (err) {
        opts.onError?.(err instanceof Error ? err : new Error(String(err)), sub);
        scheduleReconnect(sub);
        return;
      }
      // The handle's `ready` promise rejects when the WS connection fails;
      // the same failure already fires onError above, so absorb the rejection
      // here to keep it from surfacing as an unhandled rejection that crashes
      // the worker. Manager-level callers consume errors via opts.onError.
      handles.set(sub.id, handle);
      versions.set(sub.id, sub.updatedAt.getTime());
      handle.ready.catch(disconnected);
    } finally {
      opening.delete(sub.id);
    }
  };

  const refresh = async (): Promise<void> => {
    if (stopping) return;
    const rows = await listEnabledSubscriptions();
    const desired = new Set(rows.map((r) => r.id));
    for (const id of handles.keys()) {
      if (!desired.has(id)) closeOne(id);
    }
    for (const [id, timer] of retryTimers) {
      if (desired.has(id)) continue;
      clearTimeout(timer);
      retryTimers.delete(id);
      retryAttempts.delete(id);
    }
    for (const row of rows) {
      if (handles.has(row.id) && versions.get(row.id) !== row.updatedAt.getTime()) {
        closeOne(row.id);
      }
      if (handles.has(row.id) || opening.has(row.id) || retryTimers.has(row.id)) continue;
      await openOne(row);
    }
  };

  const run = async () => {
    try {
      await refresh();
      resolveReady();
    } catch (err) {
      rejectReady(err instanceof Error ? err : new Error(String(err)));
      return;
    }
    const interval = opts.refreshIntervalMs ?? DEFAULT_REFRESH_INTERVAL_MS;
    refreshTimer = setInterval(() => {
      void refresh().catch((err) => {
        opts.onError?.(err instanceof Error ? err : new Error(String(err)));
      });
    }, interval);
    refreshTimer.unref();
  };

  void run();

  return {
    ready,
    activeSubscriptionIds: () => Array.from(handles.keys()),
    refresh,
    stop: async () => {
      stopping = true;
      if (refreshTimer) clearInterval(refreshTimer);
      for (const timer of retryTimers.values()) clearTimeout(timer);
      retryTimers.clear();
      retryAttempts.clear();
      for (const id of Array.from(handles.keys())) closeOne(id);
    },
  };
}
