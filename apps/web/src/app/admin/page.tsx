import Link from "next/link";
import { connection } from "next/server";
import {
  action_policy,
  action_request,
  and,
  app_user,
  data_source,
  db,
  eq,
  sql,
  workflow_definition,
} from "@neko/db";
import { getCurrentActor } from "@/lib/actor";
import { getOrgId } from "@/lib/db";
import { getSetupCompleteAt } from "@/lib/org-state";
import { getPluginStatus } from "@/lib/auth";
import { AdminDenied, AdminShell } from "./AdminShell";

export default async function AdminPage() {
  await connection();
  const actor = await getCurrentActor();
  if (actor.role !== "admin") return <AdminDenied />;

  const orgId = await getOrgId();
  const [
    setupCompleteAt,
    pluginStatus,
    users,
    sources,
    policyCounts,
    workflowCounts,
    pendingActionCounts,
  ] = await Promise.all([
    getSetupCompleteAt(orgId),
    getPluginStatus(),
    db()
      .select({
        id: app_user.id,
        role: app_user.role,
        disabledAt: app_user.disabled_at,
      })
      .from(app_user)
      .where(eq(app_user.org_id, orgId)),
    db()
      .select({
        id: data_source.id,
        authMode: data_source.auth_mode,
        enabled: data_source.enabled,
      })
      .from(data_source)
      .where(eq(data_source.org_id, orgId)),
    db()
      .select({
        total: sql<number>`count(*)::int`,
        enabled: sql<number>`count(*) filter (where ${action_policy.enabled} = true)::int`,
      })
      .from(action_policy)
      .where(eq(action_policy.org_id, orgId)),
    db()
      .select({
        total: sql<number>`count(*)::int`,
        active: sql<number>`count(*) filter (where ${workflow_definition.enabled} = true)::int`,
      })
      .from(workflow_definition)
      .where(eq(workflow_definition.org_id, orgId)),
    db()
      .select({ total: sql<number>`count(*)::int` })
      .from(action_request)
      .where(
        and(
          eq(action_request.org_id, orgId),
          eq(action_request.status, "pending_approval"),
        ),
      ),
  ]);

  const adminCount = users.filter(
    (user) => user.role === "admin" && !user.disabledAt,
  ).length;
  const activeUserCount = users.filter((user) => !user.disabledAt).length;
  const enabledSources = sources.filter((source) => source.enabled);
  const jwtSources = enabledSources.filter(
    (source) => source.authMode === "jwt",
  ).length;
  const workflowTotal = workflowCounts[0]?.total ?? 0;
  const workflowActive = workflowCounts[0]?.active ?? 0;
  const policyTotal = policyCounts[0]?.total ?? 0;
  const policyEnabled = policyCounts[0]?.enabled ?? 0;
  const pendingActions = pendingActionCounts[0]?.total ?? 0;
  const authProviderInstalled = Boolean(pluginStatus.authProvider);

  const coreCards = [
    {
      href: "/admin/users",
      title: "Users",
      copy: authProviderInstalled
        ? "Admin and member accounts from the installed auth plugin."
        : "No auth plugin installed; this deployment runs with solo admin access.",
      status: authProviderInstalled
        ? `${activeUserCount} active - ${adminCount} admin`
        : "Solo admin",
      ok: authProviderInstalled ? adminCount > 0 : true,
    },
    {
      href: "/admin/settings",
      title: "Settings",
      copy: "Setup wizard, provider, data source, research, and security configuration.",
      status: setupCompleteAt ? "Setup complete" : "Setup incomplete",
      ok: Boolean(setupCompleteAt),
    },
    {
      href: "/admin/plugins",
      title: "Plugins",
      copy: "Registry status, declared action kinds, auth, and channel providers.",
      status:
        pluginStatus.flagged.length > 0
          ? `${pluginStatus.flagged.length} flagged`
          : `${pluginStatus.loaded.length} loaded`,
      ok: pluginStatus.flagged.length === 0,
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
      copy: "Review pending approvals and inspect action receipts across workflows and plugins.",
      status:
        pendingActions === 0 ? "None pending" : `${pendingActions} pending`,
      ok: true,
    },
  ];

  return (
    <AdminShell
      title="Administration"
      subtitle="OpenNeko configuration, users, plugins, and data access."
      wide
    >
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <SummaryStat
          label="Setup"
          value={setupCompleteAt ? "Complete" : "Incomplete"}
          tone={setupCompleteAt ? "ok" : "warn"}
        />
        <SummaryStat
          label="Plugins"
          value={`${pluginStatus.loaded.length}`}
          detail={`${pluginStatus.kinds.length} action kinds`}
        />
        <SummaryStat
          label="Auth"
          value={pluginStatus.authProvider ? "Plugin" : "Solo"}
          detail={pluginStatus.authProvider ?? "userless admin"}
        />
        <SummaryStat
          label="GraphJin"
          value={
            enabledSources.length > 0
              ? `${jwtSources}/${enabledSources.length}`
              : "0"
          }
          detail="JWT sources"
          tone={
            enabledSources.length > 0 && jwtSources === enabledSources.length
              ? "ok"
              : "warn"
          }
        />
      </div>

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
      <div className="mb-3 text-ui-label font-bold uppercase tracking-[0.14em] text-text3">
        {title}
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        {cards.map((card) => (
          <Link
            key={card.href}
            href={card.href}
            className="settings-card block no-underline"
          >
            <div className="settings-card-head mb-0">
              <div className="min-w-0">
                <h2 className="settings-card-title">{card.title}</h2>
                <p className="settings-card-copy">{card.copy}</p>
              </div>
              <StatusText value={card.status} ok={card.ok} />
            </div>
          </Link>
        ))}
      </div>
    </section>
  );
}

function StatusText({ value, ok }: { value: string; ok: boolean }) {
  return (
    <div
      className={`min-w-[128px] text-right text-xs font-semibold leading-snug ${
        ok ? "text-success-mid" : "text-danger"
      }`}
    >
      {value}
    </div>
  );
}

function SummaryStat({
  label,
  value,
  detail,
  tone = "neutral",
}: {
  label: string;
  value: string;
  detail?: string;
  tone?: "neutral" | "ok" | "warn";
}) {
  const toneClass =
    tone === "ok"
      ? "text-success-ink"
      : tone === "warn"
        ? "text-danger"
        : "text-text";
  return (
    <div className="rounded-[14px] border border-border bg-card px-4 py-3 shadow-soft">
      <div className="text-ui-label font-bold uppercase tracking-[0.12em] text-text3">
        {label}
      </div>
      <div className={`mt-2 font-display text-2xl font-bold ${toneClass}`}>
        {value}
      </div>
      {detail ? <div className="mt-1 text-xs text-text2">{detail}</div> : null}
    </div>
  );
}
