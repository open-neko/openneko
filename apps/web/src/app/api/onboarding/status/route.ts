import { NextResponse } from "next/server";
import {
  and,
  customer_profile,
  db,
  desc,
  eq,
  inArray,
  onboarding_wizard,
  processing_job,
} from "@neko/db";
import { getOrgId, type OnboardingStatus } from "@/lib/db";
import { isDemoMode, DEMO_SEATS } from "@/lib/demo-mode";

/**
 * GET /api/onboarding/status
 *
 * Decides which screen to show:
 *  - needs_wizard : no profile, no in-flight job, no recent failure → /onboarding
 *  - processing   : profile build job is running → /processing
 *  - ready        : a current profile exists → /
 *  - failed       : the most recent profile build failed and there's no
 *                   newer in-flight job → /onboarding with a toast. The UI
 *                   surfaces the error so the user knows why instead of
 *                   silently bouncing back to the wizard.
 */
export async function GET() {
  if (isDemoMode()) {
    return NextResponse.json({
      state: "ready",
      profileVersion: 0,
      seats: [...DEMO_SEATS],
    } satisfies OnboardingStatus);
  }

  const orgId = await getOrgId();

  let profileRows: Array<{ id: string; version: number }>;
  let jobRows: Array<{ id: string; status: string }>;
  let wizardRows: Array<{ active_seats: string[] | null }>;
  let lastJobRows: Array<{ id: string; status: string; error: string | null }>;
  try {
    [profileRows, jobRows, wizardRows, lastJobRows] = await Promise.all([
      db()
        .select({ id: customer_profile.id, version: customer_profile.version })
        .from(customer_profile)
        .where(
          and(
            eq(customer_profile.org_id, orgId),
            eq(customer_profile.is_current, true),
          ),
        )
        .limit(1),
      db()
        .select({ id: processing_job.id, status: processing_job.status })
        .from(processing_job)
        .where(
          and(
            eq(processing_job.org_id, orgId),
            eq(processing_job.kind, "business_profile_build"),
            inArray(processing_job.status, ["queued", "running"]),
          ),
        )
        .orderBy(desc(processing_job.created_at))
        .limit(1),
      db()
        .select({ active_seats: onboarding_wizard.active_seats })
        .from(onboarding_wizard)
        .where(eq(onboarding_wizard.org_id, orgId))
        .limit(1),
      db()
        .select({
          id: processing_job.id,
          status: processing_job.status,
          error: processing_job.error,
        })
        .from(processing_job)
        .where(
          and(
            eq(processing_job.org_id, orgId),
            eq(processing_job.kind, "business_profile_build"),
          ),
        )
        .orderBy(desc(processing_job.created_at))
        .limit(1),
    ]);
  } catch (err) {
    const message = err instanceof Error ? err.message : "Database unavailable";
    return NextResponse.json(
      { state: "db_error", message } satisfies OnboardingStatus,
      { status: 503 },
    );
  }

  let status: OnboardingStatus;
  if (profileRows.length > 0) {
    status = {
      state: "ready",
      profileVersion: profileRows[0].version,
      seats: wizardRows[0]?.active_seats ?? [],
    };
  } else if (jobRows.length > 0) {
    status = { state: "processing", jobId: jobRows[0].id };
  } else if (lastJobRows.length > 0 && lastJobRows[0].status === "failed") {
    status = {
      state: "failed",
      jobId: lastJobRows[0].id,
      message: lastJobRows[0].error ?? "The profiler agent failed.",
    };
  } else {
    status = { state: "needs_wizard" };
  }
  return NextResponse.json(status);
}
