"use client";

import Link from "next/link";
import AdminLoad from "./AdminLoad";
import { AdminShell } from "./AdminShell";
import { ExternalServicesPanel } from "./ExternalServicesPanel";
import { Card } from "@/components/ui/card";
import { StatCard, StatGrid } from "@/components/ui/stat-card";

export type AdminOverview = {
  setupComplete: boolean;
  activeUsers: number;
  activeAdmins: number;
  enabledSources: number;
  jwtSources: number;
  policies: { total: number; on: number };
  workflows: { total: number; on: number };
  pendingActions: number;
  dataReady: boolean;
  primaryReady: boolean;
  researchOn: boolean;
  customWorkflowBudget: boolean;
};

export type SigninStatus = {
  provider: { pluginName: string; providerLabel?: string } | null;
  pending: { pluginName: string } | null;
};

export default function AdminPage() {
  return (
    <AdminLoad
      title="Administration"
      paths={["/api/v1/admin/overview", "/api/v1/admin/signin/status"]}
    >
      {([overview, signin]) => (
        <AdminIndex overview={overview as AdminOverview} authGate={signin as SigninStatus} />
      )}
    </AdminLoad>
  );
}

function AdminIndex({ overview, authGate }: { overview: AdminOverview; authGate: SigninStatus }) {
  const {
    setupComplete,
    activeAdmins: adminCount,
    activeUsers: activeUserCount,
    enabledSources,
    jwtSources,
    pendingActions,
  } = overview;
  const { total: workflowTotal, on: workflowActive } = overview.workflows;
  const { total: policyTotal, on: policyEnabled } = overview.policies;
  const authProviderInstalled = Boolean(authGate.provider);

  const coreCards = [
    {
      href: "/admin/users",
      title: "Users",
      copy: authProviderInstalled
        ? "Admin and member accounts from the sign-in provider."
        : "No sign-in provider is set up; this deployment runs with solo admin access.",
      status: authProviderInstalled
        ? `${activeUserCount} active - ${adminCount} admin`
        : "Solo admin",
      ok: authProviderInstalled ? adminCount > 0 : true,
    },
    {
      href: "/admin/settings",
      title: "Settings",
      copy: "Setup wizard, provider, data source, research, and security configuration.",
      status: setupComplete ? "Setup complete" : "Setup incomplete",
      ok: setupComplete,
    },
    {
      href: "/admin/rules",
      title: "Rules",
      copy: "Skill learning, plus what OpenNeko can act on its own, queue for review, or never run.",
      status:
        policyTotal === 0
          ? "Defaults on demand"
          : `${policyEnabled}/${policyTotal} enabled`,
      ok: true,
    },
  ];

  const operateCards = [
    {
      href: "/workflows",
      title: "Workflows",
      copy: "Pause, resume, run now, inspect schedules, and manage workflow runs.",
      status:
        workflowTotal === 0
          ? "None yet"
          : `${workflowActive}/${workflowTotal} active`,
      ok: workflowTotal === 0 || workflowActive > 0,
    },
    {
      href: "/actions?filter=awaiting",
      title: "Actions",
      copy: "Review pending approvals and inspect action receipts across workflows and packs.",
      status:
        pendingActions === 0 ? "None pending" : `${pendingActions} pending`,
      ok: true,
    },
  ];

  return (
    <AdminShell
      title="Administration"
      subtitle="OpenNeko configuration, users, packs, and data access."
    >
      <StatGrid label="Workspace status">
        <StatCard
          label="Setup"
          value={setupComplete ? "Complete" : "Incomplete"}
          tone={setupComplete ? "ok" : "warn"}
        />
        <StatCard
          label="Auth"
          value={authGate.provider ? "Sign-in" : "Solo"}
          detail={authGate.provider?.providerLabel ?? "userless admin"}
        />
        <StatCard
          label="GraphJin"
          value={
            enabledSources > 0
              ? `${jwtSources}/${enabledSources}`
              : "0"
          }
          detail="JWT sources"
          tone={
            enabledSources > 0 && jwtSources === enabledSources
              ? "ok"
              : "warn"
          }
        />
      </StatGrid>

      <ExternalServicesPanel />

      <AdminCardGroup title="Core" cards={coreCards} />
      <AdminCardGroup title="Operate" cards={operateCards} />
    </AdminShell>
  );
}

type AdminCard = {
  href: string;
  title: string;
  copy: string;
  status: string;
  ok: boolean;
};

function AdminCardGroup({
  title,
  cards,
}: {
  title: string;
  cards: AdminCard[];
}) {
  return (
    <section className="mt-7">
      <h2 className="mb-4 font-display text-ui-caption font-semibold text-text3">
        {title}
      </h2>
      <div className="grid gap-4 md:grid-cols-2">
        {cards.map((card) => (
          <Card
            as={Link}
            key={card.href}
            href={card.href}
            className="flex flex-col gap-4 no-underline transition-[border-color] hover:border-accent"
          >
              <div className="min-w-0 flex-1">
                <h3 className="settings-card-title">{card.title}</h3>
                <p className="settings-card-copy">{card.copy}</p>
              </div>
              <StatusText value={card.status} ok={card.ok} />
          </Card>
        ))}
      </div>
    </section>
  );
}

function StatusText({ value, ok }: { value: string; ok: boolean }) {
  return (
    <div
      className={`text-ui-caption font-semibold leading-snug ${
        ok ? "text-success-mid" : "text-danger"
      }`}
    >
      {value}
    </div>
  );
}
