"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { Button } from "@/components/ui/Button";

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

export default function PolicyDetailClient({ policyId }: { policyId: string }) {
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
        <AppHeader back={{ href: "/admin/rules", label: "Rules" }}>
          <SectionNav current="admin" />
        </AppHeader>

        <PageHeading
          title={policy?.name ?? "Loading rule"}
          description={policy?.description}
          meta={
            policy ? (policy.enabled ? "enabled" : "disabled") : undefined
          }
        />

        {error ? (
          <div className="text-danger text-sm">Couldn&apos;t load rule: {error}</div>
        ) : !policy ? (
          <div className="text-text3 text-sm">Loading rule…</div>
        ) : (
          <article className="bg-card border border-border rounded-2xl p-6">
            <Field label="Mode">
              <code className="font-mono text-ui-body-sm text-text2">{policy.mode}</code>
            </Field>

            <Field label="Applies to">
              <div className="text-ui-body-sm text-text">
                {policy.appliesToKinds.length === 0 ? (
                  <span className="italic text-text3">any action kind</span>
                ) : (
                  policy.appliesToKinds.map((k) => (
                    <code
                      key={k}
                      className="font-mono text-ui-caption bg-neutral-soft text-text2 px-1.5 py-0.5 rounded mr-1.5"
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
                <span className="text-ui-body-sm">
                  risk ≤{" "}
                  <code className="font-mono text-ui-caption text-text2">
                    {policy.riskThresholdAutoApprove}
                  </code>
                </span>
              </Field>
            )}

            {Object.keys(policy.limits).length > 0 && (
              <Field label="Limits">
                <code className="font-mono text-ui-caption text-text2">
                  {Object.entries(policy.limits)
                    .map(([k, v]) => `${k}: ${String(v)}`)
                    .join(" · ")}
                </code>
              </Field>
            )}

            {policy.approverRole && (
              <Field label="Approver role">
                <span className="text-ui-body-sm">{policy.approverRole}</span>
              </Field>
            )}

            <Field label="Priority">
              <span className="text-ui-body-sm">{policy.priority}</span>
            </Field>

            <Field label="Updated">
              <span className="text-ui-body-sm text-text2">
                {new Date(policy.updatedAt).toLocaleString()}
              </span>
            </Field>

            <div className="mt-6 pt-4 border-t border-border flex items-center justify-between gap-3">
              {policy.createdByThreadId ? (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() =>
                    router.push(`/work/${policy.createdByThreadId}`)
                  }
                >
                  view conversation
                </Button>
              ) : (
                <span />
              )}
              <Button
                variant="primary"
                size="sm"
                onClick={editInWork}
              >
                edit in /work
              </Button>
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
      <div className="text-ui-label font-bold tracking-[0.13em] uppercase text-text3 mb-1">
        {label}
      </div>
      <div className="text-text">{children}</div>
    </div>
  );
}
