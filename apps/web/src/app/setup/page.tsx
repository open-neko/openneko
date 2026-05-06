import { redirect } from "next/navigation";
import { connection } from "next/server";
import { hasCustomPassword } from "@neko/db";
import { getOrgId } from "@/lib/db";
import { getSetupCompleteAt } from "@/lib/org-state";
import { getDataSourceSettings } from "@/lib/data-source-settings";
import { getProviderSettingsPayload } from "@/lib/provider-settings";
import { getAgentSettingsPayload } from "@/lib/agent-backend-settings";
import SetupWizard from "./SetupWizard";

/**
 * Admin first-run wizard. Steps:
 *   0. Set DB password (forced when ~/.config/neko/config.json doesn't have one yet)
 *   1. Connect data source
 *   2. Choose agent + primary provider
 *   3. Research (optional)
 *
 * Once finished it stamps organization.setup_complete_at; the /onboarding
 * business wizard then becomes reachable.
 *
 * Re-running this page after setup-complete redirects to /settings — the
 * ongoing-edits surface where each concern has its own page.
 */
export default async function SetupPage() {
  await connection();
  const orgId = await getOrgId();
  const setupCompleteAt = await getSetupCompleteAt(orgId);
  if (setupCompleteAt) {
    redirect("/settings");
  }

  const [dataSource, providers, agent] = await Promise.all([
    getDataSourceSettings(orgId),
    getProviderSettingsPayload(orgId),
    getAgentSettingsPayload(orgId),
  ]);

  return (
    <SetupWizard
      initial={{
        dataSource,
        providers,
        agent,
        passwordChanged: hasCustomPassword(),
      }}
    />
  );
}
