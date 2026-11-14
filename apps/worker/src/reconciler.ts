import { and, db, eq, inArray, lte, work_run, workflow_run } from "@neko/db";

const STALE_RUN_REASON =
  "Interrupted — the worker stopped before the run finished.";
const STALE_QUEUED_REASON =
  "Never started — the process that accepted this run stopped before launching it. Retry to run it again.";
// Queued runs are launched in-process moments after the insert; one stuck
// longer than this has lost its launcher (web restart/sleep, launch error).
const STALE_QUEUED_MIN_AGE_MS = 120_000;

/**
 * Cancel work_run / workflow_run rows stranded in "running" — a run that
 * outlived the process tracking it (worker SIGKILLed mid-run, before its own
 * finalizer could mark it cancelled). With minAgeMs past the agent's own timeout,
 * a still-"running" row is provably dead.
 */
export async function reconcileStaleRuns(opts?: {
  minAgeMs?: number;
}): Promise<{ cancelled: number }> {
  const cutoff = new Date(Date.now() - (opts?.minAgeMs ?? 0));
  const now = new Date();

  const stale = await db()
    .update(work_run)
    .set({
      status: "cancelled",
      error: STALE_RUN_REASON,
      finished_at: now,
      updated_at: now,
    })
    .where(and(
      eq(work_run.status, "running"),
      lte(work_run.updated_at, cutoff),
    ))
    .returning({ id: work_run.id });

  // Queued chat runs have no durable queue behind them — the web process
  // launches them in-memory right after the insert. If that process dies or
  // errors at launch (laptop sleep, EADDRINUSE, restart), the row sits
  // "queued" forever. Cancel honestly so the UI offers Retry.
  const queuedCutoff = new Date(
    Date.now() - Math.max(opts?.minAgeMs ?? 0, STALE_QUEUED_MIN_AGE_MS),
  );
  const staleQueued = await db()
    .update(work_run)
    .set({
      status: "cancelled",
      error: STALE_QUEUED_REASON,
      finished_at: now,
      updated_at: now,
    })
    .where(
      and(eq(work_run.status, "queued"), lte(work_run.created_at, queuedCutoff)),
    )
    .returning({ id: work_run.id });
  stale.push(...staleQueued);
  if (stale.length === 0) return { cancelled: 0 };

  await db()
    .update(workflow_run)
    .set({
      status: "cancelled",
      error: STALE_RUN_REASON,
      finished_at: now,
      updated_at: now,
    })
    .where(
      and(
        inArray(workflow_run.work_run_id, stale.map((r) => r.id)),
        inArray(workflow_run.status, ["running", "queued"]),
      ),
    );
  return { cancelled: stale.length };
}
