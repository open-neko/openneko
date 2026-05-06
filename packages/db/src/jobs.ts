/**
 * Shared pg-boss job queue.
 *
 * Both web (enqueue) and worker (consume) import from here so queue names
 * and the boss singleton are aligned. Backed by the same Postgres as the
 * Drizzle client; pg-boss auto-creates its `pgboss.*` schema on first start.
 *
 * Connection comes from buildPoolConfig() (see ./connection.ts).
 */

import PgBoss from "pg-boss";
import { buildPoolConfig } from "./connection";

export const QUEUE = {
  BUSINESS_PROFILE_BUILD: "business_profile_build",
  INDUSTRY_INSIGHTS_BUILD: "industry_insights_build",
  BOOTSTRAP_METRICS_BUILD: "bootstrap_metrics_build",
  METRIC_REFRESH: "metric_refresh",
  METRIC_REFRESH_SCHEDULED_SWEEP: "metric_refresh_scheduled_sweep",
} as const;

export type QueueName = (typeof QUEUE)[keyof typeof QUEUE];

export type ProcessingJobPayload = {
  processingJobId: string;
  orgId: string;
};

export type MetricRefreshPayload = ProcessingJobPayload & {
  trigger?: string;
};

let _boss: PgBoss | null = null;
let _starting: Promise<PgBoss> | null = null;

/**
 * Returns a started pg-boss instance, creating the singleton on first call.
 * Concurrent callers all await the same start so we never start twice.
 */
export async function boss(): Promise<PgBoss> {
  if (_boss) return _boss;
  if (_starting) return _starting;
  _starting = (async () => {
    const cfg = buildPoolConfig();
    const instance = new PgBoss({
      host: cfg.host,
      port: cfg.port,
      user: cfg.user,
      password: typeof cfg.password === "string" ? cfg.password : undefined,
      database: cfg.database,
      ssl: cfg.ssl,
      // Keep retention conservative for a CXO briefing tool — failed jobs
      // stay 7 days for forensic value, completed jobs purge sooner.
      retentionDays: 7,
    });
    instance.on("error", (e) => console.error("[pg-boss] error:", e));
    await instance.start();
    _boss = instance;
    return instance;
  })();
  return _starting;
}

// Retry policy is hardcoded; if we ever need it tunable, surface as a
// scope='agent' / scope='worker' setting and read from DB at boot.
const DEFAULT_SEND_OPTS: PgBoss.SendOptions = {
  retryLimit: 2,
  retryDelay: 30,
};

// pg-boss v10 stopped auto-creating queues on send() — silently returns null
// if the queue doesn't exist yet. Track which queues we've created so the
// first enqueue per queue name implicitly provisions it.
const _ensuredQueues = new Set<string>();

async function ensureQueue(name: string): Promise<void> {
  if (_ensuredQueues.has(name)) return;
  const b = await boss();
  await b.createQueue(name);
  _ensuredQueues.add(name);
}

/**
 * Enqueue a job by queue name. Web routes call this after writing the
 * processing_job row that backs the UI progress feed; worker handlers
 * call it to chain follow-up jobs. Default send options apply our
 * retry policy; callers can override per-call.
 */
export async function enqueue<T extends object>(
  queue: QueueName,
  data: T,
  opts?: PgBoss.SendOptions,
): Promise<string | null> {
  await ensureQueue(queue);
  const b = await boss();
  return b.send(queue, data as object, { ...DEFAULT_SEND_OPTS, ...(opts ?? {}) });
}

export async function stopBoss(): Promise<void> {
  if (_boss) {
    await _boss.stop({ graceful: true });
    _boss = null;
    _starting = null;
    _ensuredQueues.clear();
  }
}
