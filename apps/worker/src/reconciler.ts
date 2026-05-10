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

const TERMINAL_BOSS_STATES = new Set(["completed", "failed", "cancelled"]);
const ACTIVE_BOSS_STATES = new Set(["created", "active", "retry"]);

export async function reconcileStaleProcessingJobs(opts?: {
  minAgeMs?: number;
}): Promise<ReconcileSummary> {
  const minAgeMs = opts?.minAgeMs ?? 0;

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

    console.warn(
      `[reconciler] unknown pg-boss state="${boss.state}" for processing_job=${c.id}; leaving as-is`,
    );
  }

  return summary;
}

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
