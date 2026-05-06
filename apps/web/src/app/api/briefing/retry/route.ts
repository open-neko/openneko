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
      return NextResponse.json({ jobId: job.id, alreadyRunning: true });
    }
  }

  // Insert a fresh processing_job + enqueue. The worker will mark
  // metric.last_refresh_status='ok' or 'failed' when it finishes.
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

  await enqueue(QUEUE.METRIC_REFRESH, { processingJobId: jobId, orgId });

  return NextResponse.json({ jobId, alreadyRunning: false });
}
