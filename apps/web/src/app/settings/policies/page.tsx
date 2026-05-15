"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";

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
      <div className="root policies-root">
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>

        <div className="policies-crumb">
          <button
            type="button"
            className="policies-crumb-link"
            onClick={() => router.push("/settings")}
          >
            ← Settings
          </button>
          <span className="policies-crumb-sep">/</span>
          <span>Rules</span>
        </div>

        <div className="policies-head">
          <h1 className="policies-title">Rules</h1>
          <button
            type="button"
            className="policies-new-btn"
            onClick={() => router.push("/settings/policies/new")}
          >
            + New rule
          </button>
        </div>

        <p className="policies-intro">
          Rules decide what OpenNeko can do on its own, what queues for your
          review, and what's never allowed. To add or change a rule, use the
          buttons here — the agent walks you through it in plain language.
        </p>

        {error ? (
          <div className="policies-error">{error}</div>
        ) : policies === null ? (
          <div className="policies-loading">Loading…</div>
        ) : policies.length === 0 ? (
          <div className="policies-empty">
            No rules yet. Defaults are seeded automatically the first time a
            workflow proposes an action that needs gating.
          </div>
        ) : (
          <ul className="policies-list">
            {policies.map((p) => (
              <PolicyCard
                key={p.id}
                policy={p}
                onEdit={() => router.push(`/settings/policies/${p.id}/edit`)}
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
    <li className={`policy-card${!policy.enabled ? " is-disabled" : ""}`}>
      <div className="policy-head">
        <div className="policy-name">{policy.name}</div>
        <div className="policy-head-right">
          <span className={`policy-mode policy-mode-${policy.mode}`}>
            {describeMode(policy.mode)}
          </span>
          <button
            type="button"
            className="policy-edit-btn"
            onClick={onEdit}
            aria-label={`Edit policy ${policy.name}`}
          >
            edit
          </button>
        </div>
      </div>

      {policy.description && (
        <p className="policy-description">{policy.description}</p>
      )}

      <dl className="policy-meta">
        <Row label="Applies to">
          <span className="policy-mono">{appliesKinds}</span>
          {appliesScopes && (
            <>
              {" "}· scope{" "}
              <span className="policy-mono">{appliesScopes}</span>
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
    </li>
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
    <div className="policy-row">
      <dt className="policy-row-label">{label}</dt>
      <dd className="policy-row-value">{children}</dd>
    </div>
  );
}
