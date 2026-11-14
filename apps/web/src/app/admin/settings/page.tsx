"use client";

import Link from "next/link";
import { ArrowUpRight } from "lucide-react";
import AppHeader from "@/components/AppHeader";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import AdminLoad from "@/app/admin/AdminLoad";
import type { AdminOverview, SigninStatus } from "@/app/admin/page";
import SetupWizard from "./SetupWizard";

type WizardInitial = Parameters<typeof SetupWizard>[0]["initial"];

type SpendStatus = {
  limits: { warnPercent: number };
  org: { day: { spentUsd: number; heldUsd: number; limitUsd: number } };
  alerts: unknown[];
};

export default function SettingsPage() {
  return (
    <AdminLoad title="Workspace settings" paths={["/api/v1/admin/overview"]}>
      {([overview]) =>
        (overview as AdminOverview).setupComplete ? (
          <AdminLoad
            title="Workspace settings"
            paths={[
              "/api/v1/admin/signin/status",
              "/api/v1/settings/graphjin-config",
              "/api/v1/admin/spend",
              "/api/v1/settings/install-policy",
            ]}
          >
            {([signin, graphjin, spend, install]) => (
              <SettingsIndex
                overview={overview as AdminOverview}
                authGate={signin as SigninStatus}
                sourceConfigEnabled={
                  (graphjin as { settings: { sourceConfigEnabled: boolean } }).settings
                    .sourceConfigEnabled
                }
                spend={spend as SpendStatus}
                allowGitUrlInstalls={
                  (install as { policy: { allowGitUrlInstalls: boolean } }).policy
                    .allowGitUrlInstalls
                }
              />
            )}
          </AdminLoad>
        ) : (
          <AdminLoad
            title="Workspace settings"
            paths={[
              "/api/v1/settings/data-source",
              "/api/v1/settings/provider",
              "/api/v1/settings/agent",
              "/api/v1/admin/change-password",
            ]}
          >
            {([dataSource, providers, agent, password]) => (
              <SetupWizard
                initial={
                  {
                    dataSource,
                    providers,
                    agent,
                    passwordChanged: (password as { changed: boolean }).changed,
                  } as WizardInitial
                }
              />
            )}
          </AdminLoad>
        )
      }
    </AdminLoad>
  );
}

function SettingsIndex({
  overview,
  authGate,
  sourceConfigEnabled,
  spend,
  allowGitUrlInstalls,
}: {
  overview: AdminOverview;
  authGate: SigninStatus;
  sourceConfigEnabled: boolean;
  spend: SpendStatus;
  allowGitUrlInstalls: boolean;
}) {
  const { dataReady, primaryReady, enabledSources, jwtSources } = overview;
  const liveProvider = authGate.provider?.pluginName ?? null;
  const pendingProvider = authGate.pending?.pluginName ?? null;
  const authStatus = (provider: "scalekit" | "magic-link") =>
    liveProvider === provider
      ? { status: "Live", statusTone: "success" as const }
      : pendingProvider === provider
        ? { status: "Setup pending", statusTone: "watch" as const }
        : { status: "Off", statusTone: "neutral" as const };
  const securityStatus = allowGitUrlInstalls
    ? { status: "Git-URL skills allowed", statusTone: "watch" as const }
    : { status: "Locked down", statusTone: "success" as const };

  type SettingsCard = {
    href: string;
    title: string;
    copy: string;
    status?: string;
    statusTone?: "success" | "watch" | "neutral";
  };
  const spentToday = spend.org.day.spentUsd + spend.org.day.heldUsd;
  const groups: Array<{ title: string; cards: SettingsCard[] }> = [
    {
      title: "Connections",
      cards: [
        {
          href: "/admin/settings/data",
          title: "Data source",
          copy: "The connection OpenNeko uses to read your business data.",
          status: dataReady ? "Connected" : "Not set",
          statusTone: dataReady ? "success" : "watch",
        },
        {
          href: "/admin/settings/packs",
          title: "Solution packs",
          copy: "Install and manage packs for applications such as Magento.",
          status: "Magento ready",
          statusTone: "success",
        },
        {
          href: "/admin/settings/channels",
          title: "Channels",
          copy: "Slack, Telegram, and other places where people talk to OpenNeko. Each one turns on when its settings are saved.",
        },
        {
          href: "/admin/settings/graphjin",
          title: "Data access rules",
          copy: "How OpenNeko signs in to each source, and what each role can read.",
          status: sourceConfigEnabled
            ? enabledSources === 0
              ? "Ask-based config on · No source"
              : `Ask-based config on · ${jwtSources} of ${enabledSources} signed`
            : "Ask-based config off",
          statusTone: !sourceConfigEnabled
            ? "neutral"
            : enabledSources > 0 && jwtSources === enabledSources
              ? "success"
              : "watch",
        },
      ],
    },
    {
      title: "Agent",
      cards: [
        {
          href: "/admin/settings/agent",
          title: "Model and provider",
          copy: "The AI model OpenNeko uses, and the provider that runs it.",
          status: primaryReady ? "Ready" : "Model not set",
          statusTone: primaryReady ? "success" : "watch",
        },
        {
          href: "/admin/settings/models",
          title: "Models and task routes",
          copy: "More models, a model for each task, and a fallback model when a provider fails.",
        },
        {
          href: "/admin/settings/research",
          title: "Industry research",
          copy: "Optional research on your industry that OpenNeko runs during setup.",
          status: overview.researchOn ? "On" : "Off",
          statusTone: overview.researchOn ? "success" : "neutral",
        },
      ],
    },
    {
      title: "Access",
      cards: [
        {
          href: "/admin/settings/sso",
          title: "Single sign-on",
          copy: "People sign in with Okta, Entra ID, or another identity provider. Their groups map to roles.",
          ...authStatus("scalekit"),
        },
        {
          href: "/admin/settings/signin",
          title: "Email-link sign-in",
          copy: "Invited people sign in with a one-time link sent to their email.",
          ...authStatus("magic-link"),
        },
        {
          href: "/admin/settings/security",
          title: "Install policy",
          copy: "Whether operators can install community skills from git URLs.",
          ...securityStatus,
        },
      ],
    },
    {
      title: "Spending",
      cards: [
        {
          href: "/admin/settings/spend",
          title: "Spending limits",
          copy: "Caps on AI model spend per run, per hour, and per day.",
          status:
            spend.alerts.length > 0
              ? `${spend.alerts.length} open ${spend.alerts.length === 1 ? "alert" : "alerts"}`
              : `$${spentToday.toFixed(2)} of $${spend.org.day.limitUsd.toFixed(2)} today`,
          statusTone:
            spend.alerts.length > 0 || spentToday >= (spend.org.day.limitUsd * spend.limits.warnPercent) / 100
              ? "watch"
              : "success",
        },
        {
          href: "/admin/settings/workflows",
          title: "Workflow API",
          copy: "Public URL and budgets for workflows that other systems start through the API.",
          status: overview.customWorkflowBudget ? "Custom budget" : "Default budget",
          statusTone: "neutral",
        },
      ],
    },
  ];

  return (
    <div className="root">
      <AppHeader back={{ href: "/admin", label: "Admin" }}>
        <SectionNav current="admin" />
      </AppHeader>
      <PageHeading
        title="Workspace settings"
        description="Connections, the agent, sign-in, and spending for this workspace."
      />

      {groups.map((group) => (
        <section key={group.title} className="settings-index-group" aria-labelledby={`settings-${group.title}`}>
          <h2 id={`settings-${group.title}`} className="settings-index-group-title">
            {group.title}
          </h2>
          <div className="settings-index-grid">
            {group.cards.map((card) => (
              <Link
                key={card.href}
                href={card.href}
                className="settings-card settings-index-card no-underline"
              >
                <div className="settings-index-card-top">
                  <h3 className="settings-card-title">{card.title}</h3>
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
        </section>
      ))}
    </div>
  );
}
