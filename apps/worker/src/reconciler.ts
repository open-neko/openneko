/**
 * Reconcile stale processing_job rows against pg-boss's authoritative state.
 *
 * Failure modes this catches:
 *
 *   1. Worker crashed mid-handler: `processing_job.status` stuck on
 *      `running`; pg-boss eventually marked the job `failed`/`expired`
 *      after retries. Our row never got `markFailed`'d.
 *   2. pg-boss's `expire_in` fired while the handler was still working
 *      (long agent runs). pg-boss gives up and marks `failed`; the
 *      worker is still computing and may eventually crash or just
 *      never write the success/failure ack.
 *   3. `markSucceeded`/`markFailed` itself failed (DB blip after the
 *      handler returned). pg-boss is `completed`/`failed`, our row is
 *      not.
 *   4. API route inserted `processing_job` but `enqueue()` to pg-boss
 *      threw. Our row is `queued`, pg-boss has no entry — the job is
 *      orphaned and will never run.
 *
 * The function joins each non-terminal `processing_job` against
 * `pgboss.job` (by `data->>'processingJobId'`) and either:
 *   - mirrors pg-boss's terminal state to ours (succeeded/failed)
 *   - resets `running` -> `queued` when pg-boss is still working on it
 *     (so the UI shows pending instead of stuck-running and pg-boss
 *     can redeliver after `expire_in`)
 *   - finalizes the row as failed when pg-boss has no record at all
 *     (orphaned by a botched insert+enqueue)
 *
 * For `metric_refresh` failures the row update also propagates to the
 * owning `metric.last_refresh_status` so card UI doesn't show a
 * skeleton for a metric whose backing job is dead.
 *
 * Safe to call repeatedly. Designed for two call sites:
 *   - worker startup (no minAgeMs — finalize everything cold)
 *   - periodic sweep (minAgeMs > expected handler runtime — only act
 *     on rows that have been stale long enough that an active handler
 *     can be ruled out)
 */

import {
  and,
  db,
  eq,
  inArray,
  lte,
  metric,
  processing_job,
  sql,
} from "@neko/db";

export type ReconcileSummary = {
  succeeded: number;
  failed: number;
  requeued: number;
  lost: number;
};

type BossRow = {
  state: string;
  output: { value?: { message?: string } } | null;
};

// pg-boss v10 states: created | retry | active | completed | cancelled | failed.
// (Timeouts collapse into `failed` with `output.value.message` set.)
const TERMINAL_BOSS_STATES = new Set(["completed", "failed", "cancelled"]);
const ACTIVE_BOSS_STATES = new Set(["created", "active", "retry"]);

export async function reconcileStaleProcessingJobs(opts?: {
  /**
   * Skip rows whose `updated_at` is newer than this many ms ago. Use
   * 0 (default) at startup to reconcile everything; pass a value
   * larger than the longest expected handler runtime for the periodic
   * sweep so we don't race against a healthy handler that just hasn't
   * called markRunning yet.
   */
  minAgeMs?: number;
}): Promise<ReconcileSummary> {
  const minAgeMs = opts?.minAgeMs ?? 0;

  // When minAgeMs <= 0 the caller wants to reconcile every
  // non-terminal row regardless of age (boot path, tests). Skip
  // the time predicate entirely — comparing JS `Date.now()` to a
  // Postgres `now()`-stamped column is racy at sub-millisecond
  // resolution and we'd miss rows that were just inserted.
  const baseFilter = and(
    inArray(processing_job.status, ["queued", "running"]),
  );
  const filter = minAgeMs > 0
    ? and(
        baseFilter,
        lte(processing_job.updated_at, new Date(Date.now() - minAgeMs)),
      )
    : baseFilter;

  const candidates = await db()
    .select({
      id: processing_job.id,
      kind: processing_job.kind,
      status: processing_job.status,
    })
    .from(processing_job)
    .where(filter);

  const summary: ReconcileSummary = {
    succeeded: 0,
    failed: 0,
    requeued: 0,
    lost: 0,
  };

  for (const c of candidates) {
    const bossRows = await db().execute<BossRow>(sql`
      SELECT state, output
      FROM pgboss.job
      WHERE name = ${c.kind}
        AND data->>'processingJobId' = ${c.id}
      ORDER BY created_on DESC
      LIMIT 1
    `);
    const boss = (bossRows.rows[0] as BossRow | undefined) ?? null;
    const now = new Date();

    if (!boss) {
      // No pg-boss row → the row was inserted but `enqueue()` never
      // landed (or pg-boss retention purged it after permanent
      // failure long ago). Either way the job is not coming back.
      const reason = "lost from queue (no pg-boss entry found)";
      await db()
        .update(processing_job)
        .set({ status: "failed", error: reason, finished_at: now, updated_at: now })
        .where(eq(processing_job.id, c.id));
      await failOwningMetricIfRefresh(c.id, c.kind, reason);
      summary.lost++;
      continue;
    }

    if (boss.state === "completed") {
      // pg-boss already accepted success but our `markSucceeded` never
      // landed (handler succeeded, ack write blipped). Mirror it.
      await db()
        .update(processing_job)
        .set({
          status: "succeeded",
          finished_at: now,
          error: null,
          updated_at: now,
        })
        .where(eq(processing_job.id, c.id));
      summary.succeeded++;
      continue;
    }

    if (TERMINAL_BOSS_STATES.has(boss.state)) {
      // failed | expired | cancelled — pg-boss is done with this job.
      const msg =
        boss.output?.value?.message ?? `pg-boss state=${boss.state}`;
      await db()
        .update(processing_job)
        .set({ status: "failed", error: msg, finished_at: now, updated_at: now })
        .where(eq(processing_job.id, c.id));
      await failOwningMetricIfRefresh(c.id, c.kind, msg);
      summary.failed++;
      continue;
    }

    if (ACTIVE_BOSS_STATES.has(boss.state)) {
      // pg-boss is still managing the job. If we're stuck on `running`
      // (handler crashed mid-flight), reset to `queued` so the UI
      // shows pending and pg-boss redelivers after `expire_in`.
      // Leave `queued` rows alone — they're correct already.
      if (c.status === "running") {
        await db()
          .update(processing_job)
          .set({
            status: "queued",
            started_at: null,
            finished_at: null,
            error: null,
            updated_at: now,
          })
          .where(eq(processing_job.id, c.id));
        summary.requeued++;
      }
      continue;
    }

    // Unknown pg-boss state — log and move on. We don't want to
    // misfile a row by guessing.
    console.warn(
      `[reconciler] unknown pg-boss state="${boss.state}" for processing_job=${c.id}; leaving as-is`,
    );
  }

  return summary;
}

/**
 * Mirror a metric_refresh failure onto the owning `metric` row so the
 * dashboard card flips from skeleton to the failed state instead of
 * spinning forever. No-op for non-metric_refresh kinds.
 */
async function failOwningMetricIfRefresh(
  processingJobId: string,
  kind: string,
  errorMsg: string,
): Promise<void> {
  if (kind !== "metric_refresh") return;
  await db()
    .update(metric)
    .set({
      last_refresh_status: "failed",
      last_refresh_error: errorMsg,
      updated_at: new Date(),
    })
    .where(eq(metric.last_refresh_job_id, processingJobId));
}
