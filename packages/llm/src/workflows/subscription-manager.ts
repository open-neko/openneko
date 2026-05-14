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
  parseWorkflowOutputMatch,
  type WorkflowOutputMatch,
} from "./subscription-query";

export type SubscriptionMatchEvent =
  | {
      kind: "workflow_output";
      subscription: SubscriptionRecord;
      output: WorkflowOutputMatch;
    };

export type SubscriptionManagerOptions = {
  baseUrl: string;
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
  let stopping = false;
  let refreshTimer: NodeJS.Timeout | null = null;
  let resolveReady: () => void;
  let rejectReady: (err: Error) => void;
  const ready = new Promise<void>((res, rej) => {
    resolveReady = res;
    rejectReady = rej;
  });

  const openOne = (sub: SubscriptionRecord) => {
    const payload = buildSubscriptionQuery({
      sourceKind: sub.sourceKind,
      filter: sub.filter,
      orgId: sub.orgId,
    });
    if (!payload) {
      console.warn(
        `[subscription-manager] skipping subscription ${sub.id} — source_kind="${sub.sourceKind}" not wired yet`,
      );
      return;
    }
    const handle = graphjinSubscribe<{ workflow_output?: unknown }>({
      baseUrl: opts.baseUrl,
      query: payload.query,
      variables: payload.variables,
      onNext: async (msg) => {
        if (sub.sourceKind === "workflow_output") {
          const match = parseWorkflowOutputMatch(msg);
          if (!match) return;
          try {
            await opts.onMatch({
              kind: "workflow_output",
              subscription: sub,
              output: match,
            });
          } catch (err) {
            opts.onError?.(
              err instanceof Error ? err : new Error(String(err)),
              sub,
            );
          }
        }
      },
      onError: (err) => {
        opts.onError?.(err, sub);
      },
    });
    handles.set(sub.id, handle);
  };

  const closeOne = (id: string) => {
    const handle = handles.get(id);
    if (!handle) return;
    handle.stop();
    handles.delete(id);
  };

  const refresh = async (): Promise<void> => {
    if (stopping) return;
    const rows = await listEnabledSubscriptions();
    const desired = new Set(rows.map((r) => r.id));
    for (const id of handles.keys()) {
      if (!desired.has(id)) closeOne(id);
    }
    for (const row of rows) {
      if (!handles.has(row.id)) openOne(row);
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
      for (const id of Array.from(handles.keys())) closeOne(id);
    },
  };
}
