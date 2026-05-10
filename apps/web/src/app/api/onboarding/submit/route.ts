import { NextRequest, NextResponse } from "next/server";
import {
  db,
  eq,
  onboarding_wizard,
  organization,
  processing_job,
} from "@neko/db";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { getOrgId } from "@/lib/db";

/**
 * POST /api/onboarding/submit
 * body: { companyName, companyNote, fiscalYearStartMonth, activeSeats[], priorities[] }
 *
 * Writes the company name to organization.name, upserts the wizard row,
 * and enqueues a business_profile_build job. The worker runs the profiler,
 * then chains an industry_insights_build job automatically once the
 * business_profile is saved.
 */
export async function POST(request: NextRequest) {
  const body = await request.json();
  const {
    companyName,
    companyNote,
    fiscalYearStartMonth,
    activeSeats,
    priorities,
  } = body as {
    companyName: string;
    companyNote: string;
    fiscalYearStartMonth: number;
    activeSeats: string[];
    priorities: string[];
  };

  const trimmedName = typeof companyName === "string" ? companyName.trim() : "";
  if (!trimmedName) {
    return NextResponse.json(
      { error: "companyName is required" },
      { status: 400 },
    );
  }

  const orgId = await getOrgId();

  // 1. Persist the company name on the org row.
  await db()
    .update(organization)
    .set({ name: trimmedName, updated_at: new Date() })
    .where(eq(organization.id, orgId));

  // 2. Replace the wizard row (delete + insert).
  await db().delete(onboarding_wizard).where(eq(onboarding_wizard.org_id, orgId));
  await db().insert(onboarding_wizard).values({
    org_id: orgId,
    company_note: companyNote,
    fiscal_year_start_month: fiscalYearStartMonth,
    active_seats: activeSeats,
    priorities,
    step: "submitting",
    submitted_at: new Date(),
  });

  // 3. Insert + enqueue. pg-boss runs on its own pool so we can't wrap
  // these in a single Postgres transaction. If `enqueue()` throws after
  // the row is inserted, the worker would never see this job and the
  // user would stare at a "needs_wizard"/"processing" UI with nothing
  // behind it. Cover the gap by finalizing the row to `failed` on
  // enqueue error so the reconciler doesn't have to and the API caller
  // gets a real 500 to surface in the UI.
  const inserted = await db()
    .insert(processing_job)
    .values({
      org_id: orgId,
      kind: "business_profile_build",
      status: "queued",
      trigger: "wizard_submit",
    })
    .returning({ id: processing_job.id });
  const jobId = inserted[0]?.id;
  if (!jobId) {
    return NextResponse.json(
      { error: "failed to record job" },
      { status: 500 },
    );
  }
  try {
    await enqueue(QUEUE.BUSINESS_PROFILE_BUILD, {
      processingJobId: jobId,
      orgId,
    });
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    await db()
      .update(processing_job)
      .set({
        status: "failed",
        error: `enqueue failed: ${msg}`,
        finished_at: new Date(),
        updated_at: new Date(),
      })
      .where(eq(processing_job.id, jobId));
    return NextResponse.json(
      { error: `enqueue failed: ${msg}` },
      { status: 500 },
    );
  }

  return NextResponse.json({ jobId });
}
