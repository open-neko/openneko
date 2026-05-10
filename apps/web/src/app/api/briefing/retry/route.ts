import { NextRequest, NextResponse } from "next/server";
import {
  and,
  db,
  desc,
  eq,
  inArray,
  metric,
  processing_job,
} from "@neko/db";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { getOrgId } from "@/lib/db";

/**
 * POST /api/briefing/retry  { metricId }
 *
 * Re-enqueues a metric_refresh job for a single failed (or stuck) card.
 * Idempotent: if a queued/running job already exists for this metric we
 * return its id without inserting a duplicate. The card-side state
 * machine resets to "pending" via the metric.last_refresh_status update
 * so the briefing UI re-skeletons immediately.
 *
 * The call site is the BriefingCard's Retry button (rendered when
 * state="failed"); the dashboard's polling loop picks the new job up
 * via /api/briefing on the next refetch.
 */
export async function POST(request: NextRequest) {
  let body: { metricId?: string };
  try {
    body = await request.json();
  } catch {
    return NextResponse.json({ error: "invalid JSON body" }, { status: 400 });
  }
  const metricId = body.metricId;
  if (!metricId || typeof metricId !== "string") {
    return NextResponse.json(
      { error: "metricId is required" },
      { status: 400 },
    );
  }

  const orgId = await getOrgId();

  // Verify the metric belongs to this org. Without the org check anyone
  // could trigger refreshes on metrics they don't own once auth lands.
  const metricRows = await db()
    .select({ id: metric.id })
    .from(metric)
    .where(and(eq(metric.id, metricId), eq(metric.org_id, orgId)))
    .limit(1);
  if (!metricRows[0]) {
    return NextResponse.json({ error: "metric not found" }, { status: 404 });
  }

  // Idempotency — return the existing in-flight job if there is one.
  // pg-boss does not dedupe, so without this we'd double-enqueue and
  // get two snapshot rows for the same card.
  const inFlight = await db()
    .select({ id: processing_job.id })
    .from(processing_job)
    .where(
      and(
        eq(processing_job.org_id, orgId),
        eq(processing_job.kind, "metric_refresh"),
        inArray(processing_job.status, ["queued", "running"]),
      ),
    )
    .orderBy(desc(processing_job.created_at))
    .limit(20);

  for (const job of inFlight) {
    const row = await db()
      .select({ trigger_payload: processing_job.trigger_payload })
      .from(processing_job)
      .where(eq(processing_job.id, job.id))
      .limit(1);
    const payload = row[0]?.trigger_payload as { metricId?: string } | null;
    if (payload?.metricId === metricId) {
      // The job is already queued/running — but the metric's
      // last_refresh_status may still be the previous 'ok'/'failed'
      // value (e.g. when the in-flight job was enqueued by the
      // 24h cron sweep, which doesn't touch last_refresh_status).
      // Without flipping it here, the card keeps showing the prior
      // snapshot and the user's retry click looks like a no-op even
      // though we did the right thing server-side. Stamp pending so
      // the next /api/briefing fetch returns state='pending' and the
      // card flips to its skeleton.
      await db()
        .update(metric)
        .set({
          last_refresh_status: "pending",
          last_refresh_error: null,
          last_refresh_job_id: job.id,
          updated_at: new Date(),
        })
        .where(eq(metric.id, metricId));
      return NextResponse.json({ jobId: job.id, alreadyRunning: true });
    }
  }

  // Insert a fresh processing_job + flip the metric to pending +
  // enqueue. pg-boss runs on its own pool so these can't be one
  // transaction; if the enqueue throws after the row is in place the
  // metric would skeleton forever waiting for a job that never runs.
  // Roll the row + metric back to a failed terminal state on enqueue
  // error so the user can hit retry again immediately.
  const inserted = await db()
    .insert(processing_job)
    .values({
      org_id: orgId,
      kind: "metric_refresh",
      status: "queued",
      trigger: "retry",
      trigger_payload: { metricId },
    })
    .returning({ id: processing_job.id });
  const jobId = inserted[0]?.id;
  if (!jobId) {
    return NextResponse.json(
      { error: "failed to enqueue retry" },
      { status: 500 },
    );
  }

  await db()
    .update(metric)
    .set({
      last_refresh_status: "pending",
      last_refresh_error: null,
      last_refresh_job_id: jobId,
      updated_at: new Date(),
    })
    .where(eq(metric.id, metricId));

  try {
    await enqueue(QUEUE.METRIC_REFRESH, { processingJobId: jobId, orgId });
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    const now = new Date();
    await db()
      .update(processing_job)
      .set({ status: "failed", error: `enqueue failed: ${msg}`, finished_at: now, updated_at: now })
      .where(eq(processing_job.id, jobId));
    await db()
      .update(metric)
      .set({ last_refresh_status: "failed", last_refresh_error: `enqueue failed: ${msg}`, updated_at: now })
      .where(eq(metric.id, metricId));
    return NextResponse.json(
      { error: `enqueue failed: ${msg}` },
      { status: 500 },
    );
  }

  return NextResponse.json({ jobId, alreadyRunning: false });
}
