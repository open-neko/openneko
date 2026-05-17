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

export default function PoliciesPage() {
  const router = useRouter();
  const [policies, setPolicies] = useState<Policy[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void fetch("/api/policies", { cache: "no-store" })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<{ policies: Policy[] }>;
      })
      .then((data) => {
        if (cancelled) return;
        setPolicies(data.policies ?? []);
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
        ) : policies.length === 0 ? (
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
