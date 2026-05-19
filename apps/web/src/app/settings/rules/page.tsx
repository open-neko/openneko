"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import { Card } from "@/components/ui/Card";
import { Pill, type PillVariant } from "@/components/ui/Pill";
import { cn } from "@/lib/cn";

type Policy = {
  id: string;
  name: string;
  description: string;
  appliesToKinds: string[];
  appliesToScopes: string[];
  mode: string;
  riskThresholdAutoApprove: string | null;
  allowedTargets: Record<string, unknown> | null;
  deniedTargets: Record<string, unknown> | null;
  limits: Record<string, unknown>;
  approverRole: string | null;
  priority: number;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
};

function describeMode(mode: string): string {
  switch (mode) {
    case "auto_approve":
      return "auto-approves";
    case "approval_required":
      return "requires approval";
    case "observe_only":
      return "observes only";
    case "draft_only":
      return "drafts only";
    case "never":
      return "never executes";
    default:
      return mode.replace(/_/g, " ");
  }
}

function modePillVariant(mode: string): PillVariant {
  switch (mode) {
    case "auto_approve":
      return "success";
    case "approval_required":
      return "watch";
    case "never":
      return "danger";
    default:
      return "muted";
  }
}

function describeAutoApproveThreshold(level: string | null): string | null {
  if (!level) return null;
  return `auto-approves risk ${level} and below`;
}

function describeTargets(
  obj: Record<string, unknown> | null,
  label: "Allowed" | "Denied",
): string | null {
  if (!obj) return null;
  const keys = Object.keys(obj);
  if (keys.length === 0) return null;
  return `${label} targets: ${keys.join(", ")}`;
}

function describeLimits(limits: Record<string, unknown>): string | null {
  if (!limits || Object.keys(limits).length === 0) return null;
  const parts: string[] = [];
  if (typeof limits.daily_cap === "number") {
    parts.push(`${limits.daily_cap}/day`);
  }
  if (typeof limits.hourly_cap === "number") {
    parts.push(`${limits.hourly_cap}/hour`);
  }
  if (typeof limits.concurrency === "number") {
    parts.push(`${limits.concurrency} concurrent`);
  }
  if (parts.length === 0) return null;
  return `Limits: ${parts.join(" · ")}`;
}

type PluginActionDescriptor = {
  kind: string;
  description: string;
  default_mode?: "auto" | "ask" | "deny";
};

function resolveEffectiveMode(
  kind: string,
  policies: Policy[],
): { mode: string; policyId: string | null } {
  // Most specific match wins: a kind-explicit, external-scoped,
  // enabled, highest-priority policy. Fall back to a kind-empty
  // external_default if nothing kind-specific exists. Mirrors how
  // evaluateActionPolicy walks policies at runtime — but kept
  // visually simple here (a single "current mode" per kind).
  const candidates = policies
    .filter((p) => p.enabled)
    .filter(
      (p) => p.appliesToScopes.length === 0 || p.appliesToScopes.includes("external"),
    );
  const kindSpecific = candidates
    .filter((p) => p.appliesToKinds.includes(kind))
    .sort((a, b) => a.priority - b.priority);
  if (kindSpecific[0]) {
    return { mode: kindSpecific[0].mode, policyId: kindSpecific[0].id };
  }
  const generic = candidates
    .filter((p) => p.appliesToKinds.length === 0)
    .sort((a, b) => a.priority - b.priority);
  if (generic[0]) {
    return { mode: generic[0].mode, policyId: generic[0].id };
  }
  return { mode: "no policy", policyId: null };
}

export default function PoliciesPage() {
  const router = useRouter();
  const [policies, setPolicies] = useState<Policy[] | null>(null);
  const [pluginDescriptors, setPluginDescriptors] = useState<
    PluginActionDescriptor[]
  >([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void Promise.all([
      fetch("/api/policies", { cache: "no-store" }).then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<{ policies: Policy[] }>;
      }),
      fetch("/api/plugins/action-descriptors", { cache: "no-store" })
        .then((r) => (r.ok ? r.json() : { descriptors: [] }))
        .then(
          (data) =>
            (data as { descriptors?: PluginActionDescriptor[] }).descriptors ?? [],
        )
        .catch(() => [] as PluginActionDescriptor[]),
    ])
      .then(([policiesResult, descriptors]) => {
        if (cancelled) return;
        setPolicies(policiesResult.policies ?? []);
        setPluginDescriptors(descriptors);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <>
      <div
        className="root"
        style={{ "--page-width": "min(1000px, 100%)" } as React.CSSProperties}
      >
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>

        <div className="mt-1 mb-3.5 font-mono text-[12.5px] text-text3 flex items-center gap-2">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-[inherit] p-0 hover:text-accent"
            onClick={() => router.push("/settings")}
          >
            ← Settings
          </button>
          <span className="opacity-50">/</span>
          <span>Rules</span>
        </div>

        <div className="flex items-baseline justify-between gap-4 mb-3">
          <h1 className="font-display text-3xl font-extrabold tracking-[-0.02em] text-text">
            Rules
          </h1>
          <button
            type="button"
            className="px-4 py-2 rounded-full border-[1.5px] border-border bg-white/60 font-body text-sm font-semibold text-text2 cursor-pointer transition-[border-color,color,background,transform] duration-200 hover:border-accent hover:text-accent hover:bg-accent-soft hover:-translate-y-px"
            onClick={() => router.push("/settings/rules/new")}
          >
            + New rule
          </button>
        </div>

        <p className="text-sm leading-normal text-text2 mb-6 max-w-[640px]">
          Rules decide what OpenNeko can do on its own, what queues for your
          review, and what's never allowed. To add or change a rule, use the
          buttons here — the agent walks you through it in plain language.
        </p>

        {error ? (
          <div className="py-14 text-center text-sm text-danger">{error}</div>
        ) : policies === null ? (
          <div className="py-14 text-center text-sm text-text3">Loading…</div>
        ) : (
          <>
            {pluginDescriptors.length > 0 ? (
              <InstalledPluginsSection
                descriptors={pluginDescriptors}
                policies={policies}
                onEditPolicy={(policyId) =>
                  router.push(`/settings/rules/${policyId}/edit`)
                }
              />
            ) : null}

            {policies.length === 0 ? (
              <div className="py-14 px-6 text-center text-sm text-text3 leading-[1.55] max-w-[520px] mx-auto">
                No rules yet. Defaults are seeded automatically the first time a
                workflow proposes an action that needs gating.
              </div>
            ) : (
              <ul className="list-none p-0 m-0 flex flex-col gap-3.5">
                {policies.map((p) => (
                  <PolicyCard
                    key={p.id}
                    policy={p}
                    onEdit={() => router.push(`/settings/rules/${p.id}/edit`)}
                  />
                ))}
              </ul>
            )}
          </>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}

function PolicyCard({
  policy,
  onEdit,
}: {
  policy: Policy;
  onEdit: () => void;
}) {
  const appliesKinds = policy.appliesToKinds.length
    ? policy.appliesToKinds.join(", ")
    : "any action";
  const appliesScopes = policy.appliesToScopes.length
    ? policy.appliesToScopes.join(", ")
    : null;
  const autoApprove = describeAutoApproveThreshold(
    policy.riskThresholdAutoApprove,
  );
  const allowed = describeTargets(policy.allowedTargets, "Allowed");
  const denied = describeTargets(policy.deniedTargets, "Denied");
  const limits = describeLimits(policy.limits);

  return (
    <Card
      as="li"
      className={cn("px-5 py-4.5", !policy.enabled && "opacity-60")}
    >
      <div className="flex items-center justify-between gap-3 mb-2">
        <div className="font-display text-[17px] font-bold tracking-[-0.01em] text-text">
          {policy.name}
        </div>
        <div className="flex items-center gap-2">
          <Pill variant={modePillVariant(policy.mode)}>
            {describeMode(policy.mode)}
          </Pill>
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-[inherit] text-xs px-1.5 py-1 hover:text-accent hover:underline underline-offset-2"
            onClick={onEdit}
            aria-label={`Edit rule ${policy.name}`}
          >
            edit
          </button>
        </div>
      </div>

      {policy.description && (
        <p className="text-[13.5px] text-text2 mt-0 mb-3 leading-[1.55]">
          {policy.description}
        </p>
      )}

      <dl className="m-0 flex flex-col gap-1 text-[13px]">
        <Row label="Applies to">
          <span className="font-mono text-xs text-text2">{appliesKinds}</span>
          {appliesScopes && (
            <>
              {" "}· scope{" "}
              <span className="font-mono text-xs text-text2">{appliesScopes}</span>
            </>
          )}
        </Row>
        {autoApprove && <Row label="Auto-approve">{autoApprove}</Row>}
        {allowed && <Row label="Allowed">{allowed.replace(/^Allowed targets:\s*/, "")}</Row>}
        {denied && <Row label="Denied">{denied.replace(/^Denied targets:\s*/, "")}</Row>}
        {limits && <Row label="Limits">{limits.replace(/^Limits:\s*/, "")}</Row>}
        {policy.approverRole && <Row label="Approver">{policy.approverRole}</Row>}
        {!policy.enabled && <Row label="Status">disabled</Row>}
      </dl>
    </Card>
  );
}

function Row({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid grid-cols-[110px_minmax(0,1fr)] gap-2.5 items-baseline">
      <dt className="text-[10.5px] font-bold tracking-[0.13em] uppercase text-text3">
        {label}
      </dt>
      <dd className="m-0 text-text break-words">{children}</dd>
    </div>
  );
}

function pluginNameFromKind(kind: string): string {
  // Cheap heuristic — most kinds are namespaced like `slack_send_message`
  // or `web_search`. We don't actually have a reverse-lookup from kind
  // to plugin without the descriptor, so callers group descriptors
  // outside this function. Used only as a fallback header.
  return kind.split("_")[0] ?? kind;
}

function describeDefaultMode(mode: string | undefined): string {
  if (mode === "auto") return "auto by default";
  if (mode === "ask") return "asks by default";
  if (mode === "deny") return "denied by default";
  return "no default declared";
}

function InstalledPluginsSection({
  descriptors,
  policies,
  onEditPolicy,
}: {
  descriptors: PluginActionDescriptor[];
  policies: Policy[];
  onEditPolicy: (policyId: string) => void;
}) {
  // descriptors are flat across plugins; the kind name itself carries
  // the plugin namespace (e.g. send_slack_*, web_search) since we
  // committed to Option-A namespacing earlier in the design. Group
  // for display by the leading token — close enough until plugins
  // tell us their package name in the descriptor (a small follow-up).
  const groups = new Map<string, PluginActionDescriptor[]>();
  for (const d of descriptors) {
    const key = pluginNameFromKind(d.kind);
    const list = groups.get(key) ?? [];
    list.push(d);
    groups.set(key, list);
  }

  return (
    <section className="mb-6">
      <div className="flex items-baseline justify-between mb-2.5">
        <h2 className="font-display text-[18px] font-bold tracking-[-0.01em] text-text">
          Installed plugins
        </h2>
        <span className="font-mono text-[11px] text-text3">
          {descriptors.length} action kind{descriptors.length === 1 ? "" : "s"}
        </span>
      </div>
      <p className="text-[13px] leading-[1.5] text-text2 mb-3 max-w-[640px]">
        Plugins contribute action kinds the agent can call from /work. Each
        kind's effective approval mode comes from the rule whose
        <em> applies-to-kinds </em>
        includes it; everything else falls through to{" "}
        <code className="font-mono text-[12px] bg-neutral px-1 rounded">
          external_default
        </code>
        . Click a kind to edit the rule that governs it.
      </p>
      <ul className="list-none p-0 m-0 flex flex-col gap-3">
        {[...groups.entries()].map(([groupName, kinds]) => (
          <Card key={groupName} as="li" className="px-5 py-4">
            <div className="font-display text-[14px] font-bold uppercase tracking-[0.1em] text-text3 mb-2">
              {groupName}
            </div>
            <div className="flex flex-col gap-2">
              {kinds.map((descriptor) => {
                const { mode, policyId } = resolveEffectiveMode(
                  descriptor.kind,
                  policies,
                );
                return (
                  <div
                    key={descriptor.kind}
                    className="flex items-center justify-between gap-3 px-3 py-2 rounded-[10px] bg-neutral-soft"
                  >
                    <div className="flex flex-col gap-0.5 min-w-0">
                      <code className="font-mono text-[12px] text-text">
                        {descriptor.kind}
                      </code>
                      <span className="text-[11px] text-text3 truncate">
                        {descriptor.description}
                      </span>
                    </div>
                    <div className="flex items-center gap-2 flex-none">
                      <Pill variant={modePillVariant(mode)}>
                        {describeMode(mode)}
                      </Pill>
                      <span className="text-[10.5px] text-text3">
                        {describeDefaultMode(descriptor.default_mode)}
                      </span>
                      {policyId ? (
                        <button
                          type="button"
                          onClick={() => onEditPolicy(policyId)}
                          className="text-[11px] font-semibold text-text2 hover:text-accent bg-transparent border-0 p-0 cursor-pointer"
                        >
                          edit rule →
                        </button>
                      ) : null}
                    </div>
                  </div>
                );
              })}
            </div>
          </Card>
        ))}
      </ul>
    </section>
  );
}
