"use client";

import { useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";

type PolicyDetail = {
  policy: {
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
    createdByThreadId?: string | null;
    createdAt: string;
    updatedAt: string;
  };
};

export default function PolicyDetailPage() {
  const params = useParams<{ policyId: string }>();
  const policyId = params?.policyId;
  const router = useRouter();
  const [policy, setPolicy] = useState<PolicyDetail["policy"] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!policyId) return;
    let cancelled = false;
    void fetch(`/api/policies/${policyId}`, { cache: "no-store" })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<PolicyDetail>;
      })
      .then((data) => {
        if (cancelled) return;
        setPolicy(data.policy);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load rule");
      });
    return () => {
      cancelled = true;
    };
  }, [policyId]);

  const editInWork = () => {
    if (!policy) return;
    router.push(
      `/work?seed=${encodeURIComponent(`Update the '${policy.name}' rule to `)}`,
    );
  };

  return (
    <>
      <div
        className="root"
        style={{ "--page-width": "min(900px, 100%)" } as React.CSSProperties}
      >
        <AppHeader>
          <SectionNav current="settings" />
        </AppHeader>

        <div className="mt-1 mb-3.5 font-mono text-[12.5px] text-text3 flex items-center gap-2">
          <button
            type="button"
            className="bg-transparent border-0 text-text3 cursor-pointer font-[inherit] p-0 hover:text-accent"
            onClick={() => router.push("/settings/rules")}
          >
            ← Rules
          </button>
          <span className="opacity-50">/</span>
          <span>{policy?.name ?? "…"}</span>
        </div>

        {error ? (
          <div className="text-danger text-sm">Couldn&apos;t load rule: {error}</div>
        ) : !policy ? (
          <div className="text-text3 text-sm">Loading rule…</div>
        ) : (
          <article className="bg-card border border-border rounded-2xl p-6">
            <header className="flex items-baseline justify-between gap-4 mb-4 pb-4 border-b border-border">
              <h1 className="font-display text-2xl font-extrabold tracking-[-0.02em] text-text">
                {policy.name}
              </h1>
              <span
                className={`text-[10px] font-bold tracking-[0.12em] uppercase px-2.5 py-[3px] rounded-full ${
                  policy.enabled
                    ? "bg-success-soft text-success-mid"
                    : "bg-neutral text-text3"
                }`}
              >
                {policy.enabled ? "enabled" : "disabled"}
              </span>
            </header>

            {policy.description && (
              <Field label="Description">
                <p className="leading-[1.55]">{policy.description}</p>
              </Field>
            )}

            <Field label="Mode">
              <code className="font-mono text-[13px] text-text2">{policy.mode}</code>
            </Field>

            <Field label="Applies to">
              <div className="text-[13px] text-text">
                {policy.appliesToKinds.length === 0 ? (
                  <span className="italic text-text3">any action kind</span>
                ) : (
                  policy.appliesToKinds.map((k) => (
                    <code
                      key={k}
                      className="font-mono text-[12px] bg-neutral-soft text-text2 px-1.5 py-0.5 rounded mr-1.5"
                    >
                      {k}
                    </code>
                  ))
                )}
                <span className="text-text3 ml-1">
                  · scope: {policy.appliesToScopes.join(", ") || "—"}
                </span>
              </div>
            </Field>

            {policy.riskThresholdAutoApprove && (
              <Field label="Auto-approve">
                <span className="text-[13px]">
                  risk ≤{" "}
                  <code className="font-mono text-[12px] text-text2">
                    {policy.riskThresholdAutoApprove}
                  </code>
                </span>
              </Field>
            )}

            {Object.keys(policy.limits).length > 0 && (
              <Field label="Limits">
                <code className="font-mono text-[12px] text-text2">
                  {Object.entries(policy.limits)
                    .map(([k, v]) => `${k}: ${String(v)}`)
                    .join(" · ")}
                </code>
              </Field>
            )}

            {policy.approverRole && (
              <Field label="Approver role">
                <span className="text-[13px]">{policy.approverRole}</span>
              </Field>
            )}

            <Field label="Priority">
              <span className="text-[13px]">{policy.priority}</span>
            </Field>

            <Field label="Updated">
              <span className="text-[13px] text-text2">
                {new Date(policy.updatedAt).toLocaleString()}
              </span>
            </Field>

            <div className="mt-6 pt-4 border-t border-border flex items-center justify-between gap-3">
              {policy.createdByThreadId ? (
                <button
                  type="button"
                  className="bg-transparent border-0 text-text3 cursor-pointer text-xs font-semibold py-1 px-0 hover:text-accent hover:underline hover:underline-offset-[3px]"
                  onClick={() =>
                    router.push(`/work/${policy.createdByThreadId}`)
                  }
                >
                  view conversation
                </button>
              ) : (
                <span />
              )}
              <button
                type="button"
                className="px-3 py-2 rounded-lg border border-accent bg-accent text-white font-body text-[13px] font-semibold cursor-pointer hover:bg-[#5a4cd1] hover:border-[#5a4cd1]"
                onClick={editInWork}
              >
                edit in /work
              </button>
            </div>
          </article>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mb-4 last:mb-0">
      <div className="text-[10.5px] font-bold tracking-[0.13em] uppercase text-text3 mb-1">
        {label}
      </div>
      <div className="text-text">{children}</div>
    </div>
  );
}
