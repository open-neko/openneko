import { Suspense } from "react";
import { redirect } from "next/navigation";
import { connection } from "next/server";
import { db, eq, onboarding_wizard, organization } from "@neko/db";
import { getOperatorProfile } from "@neko/llm/work";
import { getOrgId } from "@/lib/db";
import { getCurrentActor } from "@/lib/actor";
import { getSetupCompleteAt } from "@/lib/org-state";
import OnboardingWizard, { type WizardInitial } from "./OnboardingWizard";
import PersonaStep from "./PersonaStep";

const ORG_NAME_STUB = "My Workspace";

export default async function OnboardingPage() {
  await connection();
  const orgId = await getOrgId();
  const setupCompleteAt = await getSetupCompleteAt(orgId);
  if (!setupCompleteAt) {
    redirect("/settings");
  }

  // CV3: onboarding is mode-aware. The ORG wizard below is the admin's
  // one-time setup; a MEMBER landing here after the org is set up gets the
  // per-user persona step instead (their operator_profile row drives the
  // agent's <operator-profile> block for their runs).
  const actor = await getCurrentActor();
  if (actor.role === "member" && actor.userId) {
    const profile = await getOperatorProfile(orgId, actor.userId);
    return (
      <Suspense fallback={null}>
        <PersonaStep initialRoleTemplate={profile?.roleTemplate ?? ""} />
      </Suspense>
    );
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
