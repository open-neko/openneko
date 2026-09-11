import Link from "next/link";
import { connection } from "next/server";
import { data_source, db, eq, hasCustomPassword } from "@neko/db";
import { ArrowUpRight } from "lucide-react";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { AdminDenied } from "@/app/admin/AdminShell";
import { getCurrentActor } from "@/lib/actor";
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
  getAgentSettingsPayload,
} from "@/lib/agent-backend-settings";
import { getGraphjinConfigSettingsPayload } from "@/lib/graphjin-config-settings";
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
  const actor = await getCurrentActor();
  if (actor.role !== "admin") return <AdminDenied />;

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
  const [
    dataReady,
    primaryReady,
    researchStatus,
    sources,
    graphjinConfig,
  ] = await Promise.all([
    hasDataSourceSetup(orgId),
    hasPrimaryProviderSetup(orgId),
    resolveResearchStatus(orgId),
    db()
      .select({
        id: data_source.id,
        authMode: data_source.auth_mode,
        enabled: data_source.enabled,
      })
      .from(data_source)
      .where(eq(data_source.org_id, orgId)),
    getGraphjinConfigSettingsPayload(orgId),
  ]);

  const enabledSources = sources.filter((source) => source.enabled);
  const jwtSources = enabledSources.filter(
    (source) => source.authMode === "jwt",
  ).length;

  const cards: {
    href: string;
    title: string;
    copy: string;
    status?: string;
    statusTone?: "success" | "watch" | "neutral";
  }[] = [
    {
      href: "/admin/settings/data",
      title: "Data source",
      copy: "GraphJin server endpoint OpenNeko should connect to.",
      status: dataReady ? "Configured" : "Not set",
      statusTone: dataReady ? "success" : "watch",
    },
    {
      href: "/admin/settings/agent",
      title: "Agent",
      copy: "The Hermes runtime works with any supported primary provider.",
      status: primaryReady ? "Hermes runtime" : "Primary provider not set",
      statusTone: primaryReady ? "success" : "watch",
    },
    {
      href: "/admin/settings/graphjin",
      title: "GraphJin Config",
      copy: "Source-mode endpoints and RBAC token claims passed to GraphJin.",
      status:
        graphjinConfig.settings.sourceConfigEnabled
          ? enabledSources.length === 0
            ? "MCP on · no source"
            : `MCP on · ${jwtSources}/${enabledSources.length} JWT`
          : "MCP config off",
      statusTone: !graphjinConfig.settings.sourceConfigEnabled
        ? "neutral"
        : enabledSources.length > 0 && jwtSources === enabledSources.length
          ? "success"
          : "watch",
    },
    {
      href: "/admin/settings/packs",
      title: "Solution packs",
      copy: "Connect and administer Magento and future application packs without using the terminal.",
      status: "Magento ready",
      statusTone: "success",
    },
    {
      href: "/admin/settings/research",
      title: "Research",
      copy: "Optional industry research run during onboarding.",
      status: researchStatus === "enabled" ? "Enabled" : "Disabled",
      statusTone: researchStatus === "enabled" ? "success" : "neutral",
    },
  ];
  cards.push({
    href: "/admin/settings/sso",
    title: "Single sign-on",
    copy: "Connect your IdP (Okta, Entra ID, and others) through Scalekit and map groups to roles.",
  });
  cards.push({
    href: "/admin/settings/signin",
    title: "Email-link sign-in",
    copy: "Passwordless magic-link sign-in for provisioned users: email delivery, first admins, and gate status.",
  });
  cards.push({
    href: "/admin/settings/security",
    title: "Security",
    copy: "Trust floor for plugin and skill installs: which marketplaces are allowed, and whether unverified or community installs are permitted.",
  });

  return (
    <div className="root">
      <AppHeader>
        <SectionNav current="admin" />
      </AppHeader>
      <PageHeading
        title="Workspace settings"
        description="Configure the agent runtime, data access, research, and trust policy."
      />

      <div className="settings-index-grid">
        {cards.map((card) => (
          <Link
            key={card.href}
            href={card.href}
            className="settings-card settings-index-card no-underline"
          >
            <div className="settings-index-card-top">
              <h2 className="settings-card-title">{card.title}</h2>
              <ArrowUpRight className="settings-index-card-arrow" aria-hidden="true" />
            </div>
            <p className="settings-card-copy">{card.copy}</p>
            {card.status ? (
              <div className="settings-index-card-foot">
                <span
                  className="settings-index-status"
                  data-tone={card.statusTone ?? "neutral"}
                >
                  {card.status}
                </span>
              </div>
            ) : null}
          </Link>
        ))}
      </div>
    </div>
  );
}
