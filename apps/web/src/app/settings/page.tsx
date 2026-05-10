import Link from "next/link";
import { connection } from "next/server";
import { hasCustomPassword } from "@neko/db";
import AppHeader from "@/components/AppHeader";
import SectionNav from "@/components/SectionNav";
import { getOrgId } from "@/lib/db";
import { getSetupCompleteAt } from "@/lib/org-state";
import {
  getDataSourceSettings,
  hasDataSourceSetup,
} from "@/lib/data-source-settings";
import {
  getProviderSettingsPayload,
  hasPrimaryProviderSetup,
  resolveResearchStatus,
} from "@/lib/provider-settings";
import {
  getAgentBackendSettings,
  getAgentSettingsPayload,
} from "@/lib/agent-backend-settings";
import SetupWizard from "./SetupWizard";

/**
 * Single admin surface — wizard until first-run is finished, then a
 * card index for ongoing edits. The wizard's gating (linear steps,
 * required-prereqs check on Finish) is preserved; the only thing
 * collapsed is the URL surface — admins no longer juggle /setup +
 * /settings as separate pages.
 *
 * The branch is decided server-side by setup_complete_at, so admins
 * can't bypass first-run gating by hitting a different URL.
 */
export default async function SettingsPage() {
  await connection();
  const orgId = await getOrgId();
  const setupCompleteAt = await getSetupCompleteAt(orgId);

  // ── First-run mode: render the linear wizard. ──
  if (!setupCompleteAt) {
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

  // ── Ongoing-edits mode: card index linking to focused sub-pages. ──
  const [dataReady, primaryReady, researchStatus, agent] = await Promise.all([
    hasDataSourceSetup(orgId),
    hasPrimaryProviderSetup(orgId),
    resolveResearchStatus(orgId),
    getAgentBackendSettings(orgId),
  ]);

  const cards = [
    {
      href: "/settings/data",
      title: "Data source",
      copy: "GraphQL endpoint OpenNeko reads business data from.",
      status: dataReady ? "Configured" : "Not set",
      statusOk: dataReady,
    },
    {
      href: "/settings/agent",
      title: "Agent",
      copy:
        agent.backend === "claude-agent"
          ? "Claude Agent — locked to Anthropic primary provider."
          : "Hermes — works with any primary provider.",
      status: primaryReady ? `Backend: ${agent.backend}` : "Primary provider not set",
      statusOk: primaryReady,
    },
    {
      href: "/settings/research",
      title: "Research",
      copy: "Optional industry research run during onboarding.",
      status: researchStatus === "enabled" ? "Enabled" : "Disabled",
      statusOk: true,
    },
  ];

  return (
    <div className="root">
      <AppHeader>
        <SectionNav current="settings" />
      </AppHeader>
      <div className="greet">Workspace settings.</div>
      <div className="greet-sub" style={{ marginBottom: 24 }}>
        Operator-side configuration. The business onboarding lives at /onboarding.
      </div>

      <div style={{ display: "flex", flexDirection: "column", gap: 16, marginTop: 24 }}>
        {cards.map((card) => (
          <Link key={card.href} href={card.href} className="settings-card" style={{ textDecoration: "none" }}>
            <div className="settings-card-head">
              <div>
                <h2 className="settings-card-title">{card.title}</h2>
                <p className="settings-card-copy">{card.copy}</p>
              </div>
              <div className="settings-source">
                <strong style={{ color: card.statusOk ? "var(--accent)" : "#c33" }}>{card.status}</strong>
              </div>
            </div>
          </Link>
        ))}
      </div>
    </div>
  );
}
