import { Suspense } from "react";
import { redirect } from "next/navigation";
import { connection } from "next/server";
import { db, eq, onboarding_wizard, organization } from "@neko/db";
import { getOrgId } from "@/lib/db";
import { getSetupCompleteAt } from "@/lib/org-state";
import OnboardingWizard, { type WizardInitial } from "./OnboardingWizard";

const ORG_NAME_STUB = "My Workspace";

export default async function OnboardingPage() {
  await connection();
  // Single gate: business onboarding only opens after admin setup is done.
  // /setup writes setup_complete_at on its Finish handler. Mixing the
  // 3-predicate config check here used to push the business user into
  // settings screens that aren't theirs to fill in.
  const orgId = await getOrgId();
  const setupCompleteAt = await getSetupCompleteAt(orgId);
  if (!setupCompleteAt) {
    redirect("/setup");
  }

  const [wizardRows, orgRows] = await Promise.all([
    db()
      .select({
        company_note: onboarding_wizard.company_note,
        fiscal_year_start_month: onboarding_wizard.fiscal_year_start_month,
        active_seats: onboarding_wizard.active_seats,
        priorities: onboarding_wizard.priorities,
      })
      .from(onboarding_wizard)
      .where(eq(onboarding_wizard.org_id, orgId))
      .limit(1),
    db()
      .select({ name: organization.name })
      .from(organization)
      .where(eq(organization.id, orgId))
      .limit(1),
  ]);

  const row = wizardRows[0];
  const orgName = orgRows[0]?.name ?? "";
  const initial: WizardInitial = {
    companyName: orgName === ORG_NAME_STUB ? "" : orgName,
    companyNote: row?.company_note ?? "",
    fiscalYearStartMonth: row?.fiscal_year_start_month ?? 1,
    activeSeats: row?.active_seats ?? [],
    priorities: row?.priorities ?? [],
  };

  return (
    <Suspense fallback={null}>
      <OnboardingWizard initial={initial} />
    </Suspense>
  );
}
