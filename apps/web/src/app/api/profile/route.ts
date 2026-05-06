import { NextResponse } from "next/server";
import {
  and,
  customer_profile,
  db,
  eq,
  inArray,
  processing_job,
} from "@neko/db";
import { getOrgId } from "@/lib/db";
import { resolveResearchStatus } from "@/lib/provider-settings";

/**
 * GET /api/profile
 *
 * Returns the current business profile and industry insights.
 * Called by the processing page during the onboarding reveal flow.
 */
export async function GET() {
  const researchStatus = await resolveResearchStatus((await getOrgId()));

  const [profileRows, jobRows] = await Promise.all([
    db()
      .select({
        business_profile: customer_profile.business_profile,
        industry_insights: customer_profile.industry_insights,
        company_note: customer_profile.company_note,
      })
      .from(customer_profile)
      .where(
        and(
          eq(customer_profile.org_id, (await getOrgId())),
          eq(customer_profile.is_current, true),
        ),
      )
      .limit(1),
    db()
      .select({ id: processing_job.id })
      .from(processing_job)
      .where(
        and(
          eq(processing_job.org_id, (await getOrgId())),
          eq(processing_job.kind, "industry_insights_build"),
          inArray(processing_job.status, ["queued", "running"]),
        ),
      )
      .limit(1),
  ]);

  const profile = profileRows[0];
  if (!profile) {
    return NextResponse.json({ error: "no profile" }, { status: 404 });
  }

  return NextResponse.json({
    businessProfile: profile.business_profile ?? "",
    industryInsights: profile.industry_insights ?? "",
    companyNote: profile.company_note ?? "",
    industryInsightsStatus:
      profile.industry_insights
        ? "ready"
        : researchStatus === "disabled"
          ? "disabled"
          : jobRows.length > 0
            ? "processing"
            : "pending",
  });
}
