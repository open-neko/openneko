"use client";

import { useState } from "react";
import { toast } from "sonner";
import { ActionGroup } from "@/components/ui/action-group";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

type RuleBrief = { id: string; name: string; mode: string; priority: number };

export type RuleProposal = {
  id: string;
  name: string;
  rule: {
    description?: string;
    appliesToKinds?: string[] | null;
    appliesToScopes?: string[] | null;
    mode?: string;
    riskThresholdAutoApprove?: string | null;
    allowedTargets?: { patterns?: unknown; on_miss?: string } | null;
    deniedTargets?: { patterns?: unknown } | null;
    limits?: Record<string, unknown> | null;
    approverRole?: string | null;
    priority?: number;
    enabled?: boolean;
  };
  proposedBy: string | null;
  createdAt: string;
  replaces: RuleBrief | null;
  overrides: RuleBrief[];
};

function patterns(value: { patterns?: unknown } | null | undefined): string {
  const list = Array.isArray(value?.patterns) ? value.patterns.filter((p): p is string => typeof p === "string") : [];
  return list.join(", ");
}

function brief(rule: RuleBrief): string {
  return `${rule.name} (${rule.mode.replace(/_/g, " ")}, priority ${rule.priority})`;
}

export default function ProposedRules({ proposals, onDecided }: { proposals: RuleProposal[]; onDecided: () => void }) {
  if (proposals.length === 0) return null;
  return (
    <section className="settings-card mt-6 grid gap-4" aria-labelledby="proposed-rules-title">
      <div>
        <h2 id="proposed-rules-title" className="settings-card-title">Proposed rules</h2>
        <p className="settings-card-copy">
          A chat run proposed these rules. A rule has no effect until you confirm it. Read the full rule and what it overrides first.
        </p>
      </div>
      <ul className="m-0 grid list-none gap-4 p-0">
        {proposals.map((proposal) => (
          <ProposalRow key={proposal.id} proposal={proposal} onDecided={onDecided} />
        ))}
      </ul>
    </section>
  );
}

function ProposalRow({ proposal, onDecided }: { proposal: RuleProposal; onDecided: () => void }) {
  const [busy, setBusy] = useState<"confirm" | "reject" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const rule = proposal.rule;
  const kinds = rule.appliesToKinds?.length ? rule.appliesToKinds.join(", ") : "every action";
  const scopes = rule.appliesToScopes?.length ? rule.appliesToScopes.join(", ") : "every scope";
  const allowed = patterns(rule.allowedTargets);
  const denied = patterns(rule.deniedTargets);
  const limits = rule.limits && Object.keys(rule.limits).length > 0 ? JSON.stringify(rule.limits) : "";

  async function decide(verb: "confirm" | "reject") {
    setBusy(verb);
    setError(null);
    try {
      const response = await fetch(`/api/v1/policies/proposals/${encodeURIComponent(proposal.id)}/${verb}`, { method: "POST" });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body.error ?? `Could not ${verb} the rule.`);
      toast.success(verb === "confirm" ? `${proposal.name} is in effect.` : `${proposal.name} is rejected.`);
      onDecided();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `Could not ${verb} the rule.`);
    } finally {
      setBusy(null);
    }
  }

  return (
    <li className="grid gap-3 border-t border-border pt-4 first:border-t-0 first:pt-0">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-semibold text-text">{proposal.name}</span>
        <Badge variant="watch">{(rule.mode ?? "unknown").replace(/_/g, " ")}</Badge>
        {rule.enabled === false ? <Badge variant="muted">disabled</Badge> : null}
      </div>
      {rule.description ? <p className="m-0 text-ui-body-sm text-text2">{rule.description}</p> : null}
      <dl className="m-0 grid gap-1 text-ui-body-sm">
        <Line label="Applies to">{kinds} · {scopes}</Line>
        <Line label="Priority">{rule.priority ?? 100}</Line>
        {rule.riskThresholdAutoApprove ? <Line label="Auto-approve">risk {rule.riskThresholdAutoApprove} and below</Line> : null}
        {allowed ? <Line label="Allowed">{allowed}</Line> : null}
        {allowed ? (
          <Line label="Other targets">
            {rule.allowedTargets?.on_miss === "next" ? "go to the next rule (on_miss: next)" : "are denied"}
          </Line>
        ) : null}
        {denied ? <Line label="Denied">{denied}</Line> : null}
        {limits ? <Line label="Limits">{limits}</Line> : null}
        {rule.approverRole ? <Line label="Approver">{rule.approverRole}</Line> : null}
        <Line label="Replaces">{proposal.replaces ? brief(proposal.replaces) : "no rule"}</Line>
        <Line label="Overrides">
          {proposal.overrides.length ? proposal.overrides.map(brief).join("; ") : "no other rule"}
        </Line>
        <Line label="Proposed by">{proposal.proposedBy ?? "unknown"}</Line>
      </dl>
      {error ? <p role="alert" className="m-0 text-ui-body-sm text-danger">{error}</p> : null}
      <ActionGroup align="start">
        <Button size="sm" variant="primary" disabled={busy !== null} onClick={() => void decide("confirm")}>
          {busy === "confirm" ? "Confirming…" : "Confirm"}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void decide("reject")}>
          {busy === "reject" ? "Rejecting…" : "Reject"}
        </Button>
      </ActionGroup>
    </li>
  );
}

function Line({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[110px_minmax(0,1fr)] items-baseline gap-2.5 max-[480px]:grid-cols-1 max-[480px]:gap-0.5">
      <dt className="text-ui-caption font-semibold text-text3">{label}</dt>
      <dd className="m-0 break-words text-text">{children}</dd>
    </div>
  );
}
