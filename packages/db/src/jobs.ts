/**
 * The job queue. `openneko web` runs every queue on River; these functions
 * insert River jobs. The pg-boss instance is left only to remove the old
 * queues at worker boot.
 *
 * Connection comes from buildPoolConfig() (see ./connection.ts).
 */

import PgBoss from "pg-boss";
import { buildPoolConfig } from "./connection";
import { pool } from "./index";

/**
 * Queues that `openneko` runs on River. `jobs.Moved` in
 * apps/openneko/internal/jobs holds the same names.
 */
export const MOVED_QUEUES = [
  "workflow_output_ttl_sweep",
  "metric_refresh_scheduled_sweep",
  "workflow_cron_sweep",
  "embedding_index",
  "embedding_sweep",
  "directory_sync",
  "action_execute",
  "skill_learn",
  "bot_name_sweep",
  "business_profile_build",
  "industry_insights_build",
  "bootstrap_metrics_build",
  "metric_refresh",
  "workflow_run_fire",
  "workflow_api_sweep",
  "work_run",
  "channel_deliver",
] as const;

/**
 * Queues of removed features. The worker deletes them at boot, which also
 * drops their pending jobs and recurring schedules on upgraded installs.
 */
export const RETIRED_QUEUES = [
  "records_import",
  "records_identity_link",
  "records_salesforce_export",
  "records_salesforce_sync",
  "records_salesforce_sync_sweep",
  "records_salesforce_cutover",
  "records_backup_verify",
  "records_ops_watch",
  "records_watch_evaluate",
  "records_watch_sweep",
] as const;

export type ProcessingJobPayload = {
  queuedAt?: number;
  processingJobId: string;
  orgId: string;
};

export type WorkRunPayload = ProcessingJobPayload & {
  queuedAt?: number;
  /** work_run.id — the row the worker will update. */
  runId: string;
  /** work_thread.id this run belongs to. */
  threadId: string;
  /** The user message that kicked off this run. */
  message: string;
  /** Delivery channel ("web", "telegram", …). Omitted ⇒ "web". Gates a2ui
   *  rendering — see docs/PER_CHANNEL_RENDERING.md. */
  channel?: string;
  /** For channel-initiated runs: the channel plugin + sender to deliver the
   *  reply back to. Absent for web runs (the UI streams over SSE). */
  channelPlugin?: string;
  recipient?: Record<string, unknown>;
  /** CH1: channel-native identity of the human who sent the message
   *  (Telegram from.id, Slack event.user+team). K1 resolves
   *  it to an actor; CH3 links it to an app_user. */
  sender?: { id: string; displayName?: string; workspaceId?: string };
};

/**
 * A durable, retryable outbound channel delivery. Enqueued (not delivered
 * inline) so a transient network failure or a restart can't drop the
 * message.
 */
export type ChannelDeliverPayload = {
  orgId: string;
  channelPlugin: string;
  recipient: Record<string, unknown>;
  /** InteractionEvent[] to deliver (serialized JSON). */
  events: unknown[];
};

/**
 * Queues one channel chat turn on River. `jobs.WorkRunArgs` in
 * apps/openneko/internal/jobs reads the row; the queue and max_attempts
 * match `jobs.QueueWorkRun` and `jobs.WorkRunAttempts`.
 */
export async function enqueueWorkRun(payload: WorkRunPayload): Promise<string> {
  const { rows } = await pool().query<{ id: string }>(
    `insert into river.river_job (kind, args, max_attempts, queue)
     values ('work_run', $1::jsonb, 3, 'work_run') returning id::text`,
    [JSON.stringify({ ...payload, queuedAt: Date.now() })],
  );
  return rows[0]!.id;
}

/**
 * Queues one channel delivery on River. With a key, no job is added while a
 * job with the key is waiting, running or done, as `Inserter.ChannelDeliver`
 * in apps/openneko/internal/jobs does; max_attempts matches
 * `jobs.ChannelDeliverAttempts`. Returns null for a duplicate.
 */
export async function enqueueChannelDeliver(
  payload: ChannelDeliverPayload,
  key?: string,
): Promise<string | null> {
  const client = await pool().connect();
  try {
    await client.query("begin");
    if (key) {
      await client.query(`select pg_advisory_xact_lock(hashtext('channel_deliver:' || $1))`, [key]);
      const seen = await client.query(
        `select 1 from river.river_job where kind = 'channel_deliver' and args->>'key' = $1
           and state not in ('cancelled', 'discarded') limit 1`,
        [key],
      );
      if (seen.rowCount) {
        await client.query("commit");
        return null;
      }
    }
    const { rows } = await client.query<{ id: string }>(
      `insert into river.river_job (kind, args, max_attempts, queue)
       values ('channel_deliver', $1::jsonb, 9, 'default') returning id::text`,
      [JSON.stringify({ ...payload, ...(key ? { key } : {}) })],
    );
    await client.query("commit");
    return rows[0]!.id;
  } catch (error) {
    await client.query("rollback").catch(() => {});
    throw error;
  } finally {
    client.release();
  }
}

export type WorkflowRunFirePayload = {
  queuedAt?: number;
  orgId: string;
  workflowId: string;
  triggerKind: "manual" | "cron" | "subscription" | "watcher" | "api";
  /** Durable cron firing ledger id. Cron deliveries use this as their
   * consumer-side idempotency boundary before creating a workflow run. */
  scheduleFiringId?: string;
  triggerPayload?: Record<string, unknown>;
  userMessage?: string;
  threadId?: string;
  parentChainDepth?: number;
  triggeredBySubscriptionId?: string;
  triggeredByOutputId?: string;
  triggeredByObservationId?: string;
  /** External API runs are pre-created transactionally by admission. */
  apiAdmissionId?: string;
  workflowRunId?: string;
  workRunId?: string;
  executionMode?: "single" | "batch";
  admittedAt?: string;
  queueAttempt?: number;
};

export type ActionExecutePayload = {
  orgId: string;
  actionRequestId: string;
};

/**
 * Queues an approved action on River. `jobs.ActionExecuteArgs` in
 * apps/openneko/internal/jobs reads the row; max_attempts matches
 * `jobs.ActionExecuteAttempts`.
 */
export async function enqueueActionExecute(payload: ActionExecutePayload): Promise<string> {
  const { rows } = await pool().query<{ id: string }>(
    `insert into river.river_job (kind, args, max_attempts, queue)
     values ('action_execute', $1::jsonb, 3, 'default') returning id::text`,
    [JSON.stringify({ orgId: payload.orgId, actionRequestId: payload.actionRequestId })],
  );
  return rows[0]!.id;
}

/**
 * Queues one metric refresh on River for its processing_job row.
 * `jobs.MetricRefreshArgs` in apps/openneko/internal/jobs reads the row;
 * the queue and max_attempts match `jobs.QueueMetricRefresh` and
 * `jobs.BuildAttempts`.
 */
export async function enqueueMetricRefresh(payload: ProcessingJobPayload, maxAttempts = 3): Promise<string> {
  const { rows } = await pool().query<{ id: string }>(
    `insert into river.river_job (kind, args, max_attempts, queue)
     values ('metric_refresh', $1::jsonb, $2, 'metric_refresh') returning id::text`,
    [JSON.stringify({ orgId: payload.orgId, processingJobId: payload.processingJobId }), maxAttempts],
  );
  return rows[0]!.id;
}

/**
 * Queues one cron, subscription or watcher workflow run on River. API runs
 * are queued by `openneko web` when it admits them.
 * `jobs.WorkflowRunFireArgs` in apps/openneko/internal/jobs reads the row;
 * the queue, max_attempts and dedupe value match `Inserter.WorkflowRunFire`.
 * With a dedupe key, no job is added while one with the key exists in the
 * same window, as a pg-boss singleton did. Returns null for a duplicate.
 */
export async function enqueueWorkflowRunFire(
  payload: WorkflowRunFirePayload,
  opts: { dedupeKey?: string; dedupeSeconds?: number } = {},
): Promise<string | null> {
  if (payload.triggerKind === "api") {
    throw new Error("API workflow runs are queued by openneko web when it admits them");
  }
  const client = await pool().connect();
  try {
    await client.query("begin");
    let dedupe: string | null = null;
    if (opts.dedupeKey) {
      const seconds = Math.max(Math.floor(opts.dedupeSeconds ?? 3600), 1);
      const slot = await client.query<{ dedupe: string }>(
        `select $1 || '@' || (floor(extract(epoch from now()) / $2)::bigint * $2)::text as dedupe`,
        [opts.dedupeKey, seconds],
      );
      dedupe = slot.rows[0]!.dedupe;
      await client.query(`select pg_advisory_xact_lock(hashtext('workflow_run_fire:' || $1))`, [dedupe]);
      const seen = await client.query(
        `select 1 from river.river_job where kind = 'workflow_run_fire' and args->>'dedupe' = $1 and state <> 'cancelled' limit 1`,
        [dedupe],
      );
      if (seen.rowCount) {
        await client.query("commit");
        return null;
      }
    }
    const args = { ...payload, queuedAt: Date.now(), ...(dedupe ? { dedupe } : {}) };
    const { rows } = await client.query<{ id: string }>(
      `insert into river.river_job (kind, args, max_attempts, queue)
       values ('workflow_run_fire', $1::jsonb, 3, 'workflow_run') returning id::text`,
      [JSON.stringify(args)],
    );
    await client.query("commit");
    return rows[0]!.id;
  } catch (error) {
    await client.query("rollback").catch(() => {});
    throw error;
  } finally {
    client.release();
  }
}

export type SkillLearnPayload = {
  orgId: string;
  skillName: string;
};

/**
 * Queues one skill learning run on River unless one for the same org and
 * skill still waits. `jobs.SkillLearnArgs` in
 * apps/openneko/internal/jobs reads the row; max_attempts matches
 * `jobs.SkillLearnAttempts`.
 */
export async function enqueueSkillLearn(payload: SkillLearnPayload): Promise<void> {
  const args = JSON.stringify({ orgId: payload.orgId, skillName: payload.skillName });
  await pool().query(
    `insert into river.river_job (kind, args, max_attempts, queue)
     select 'skill_learn', $1::jsonb, 3, 'default'
     where not exists (
       select 1 from river.river_job where kind = 'skill_learn' and args = $1::jsonb
         and state in ('available', 'pending', 'retryable', 'scheduled'))`,
    [args],
  );
}

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

export async function stopBoss(): Promise<void> {
  if (_boss) {
    await _boss.stop({ graceful: true });
    _boss = null;
    _starting = null;
  }
}
