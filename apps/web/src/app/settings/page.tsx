import Link from "next/link";
import { getOrgId } from "@/lib/db";
import { hasDataSourceSetup } from "@/lib/data-source-settings";
import {
  hasPrimaryProviderSetup,
  resolveResearchStatus,
} from "@/lib/provider-settings";
import { getAgentBackendSettings } from "@/lib/agent-backend-settings";

/**
 * Settings index — one card per concern. Each links to a dedicated page
 * so the operator only sees the form they came to edit. This replaces
 * the old single-page settings (3 stacked cards) which mixed unrelated
 * concerns and made the settings + onboarding flow read as one mixed-
 * persona experience.
 */
export default async function SettingsIndex() {
  const [dataReady, primaryReady, researchStatus, agent] = await Promise.all([
    hasDataSourceSetup((await getOrgId())),
    hasPrimaryProviderSetup((await getOrgId())),
    resolveResearchStatus((await getOrgId())),
    getAgentBackendSettings((await getOrgId())),
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
    <div className="root" style={{ paddingTop: 44 }}>
      <div className="settings-topbar">
        <div>
          <div className="brand">
            <img className="brand-icon" src="/cat.png" alt="" width={32} height={32} />
            <span className="brand-name">OpenNeko</span>
          </div>
          <div className="greet" style={{ marginTop: 28 }}>Workspace settings.</div>
          <div className="greet-sub">
            Operator-side configuration. The business onboarding lives at /onboarding.
          </div>
        </div>
        <Link className="settings-backlink inline-flex items-center gap-2 whitespace-nowrap" href="/">
          <span aria-hidden="true" className="settings-backlink-arrow text-base leading-none">←</span>
          <span>Back to briefing</span>
        </Link>
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
