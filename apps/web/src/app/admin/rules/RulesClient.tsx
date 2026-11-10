"use client";

import { useEffect, useState } from "react";
import { SkeletonList } from "@/components/ui/skeleton";
import { useRouter } from "next/navigation";
import { Plus } from "lucide-react";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import PageHeading from "@/components/PageHeading";
import SectionNav from "@/components/SectionNav";
import { ActionGroup } from "@/components/ui/action-group";
import { Button } from "@/components/ui/button";
import { Disclosure } from "@/components/ui/disclosure";
import { Badge, type BadgeVariant } from "@/components/ui/badge";
import { SearchInput } from "@/components/ui/search-input";
import { cn } from "@/lib/cn";
import { matchesListSearch } from "@/lib/list-search";
import SkillLearnCard from "./SkillLearnCard";

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

function modePillVariant(mode: string): BadgeVariant {
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
  const patterns = targetPatterns(obj);
  if (patterns.length === 0) return null;
  const miss = label === "Allowed" && obj?.on_miss === "next" ? "; others go to the next rule" : "";
  return `${label} targets: ${patterns.join(", ")}${miss}`;
}

export function targetPatterns(obj: Record<string, unknown> | null): string[] {
  return Array.isArray(obj?.patterns) ? obj.patterns.filter((p): p is string => typeof p === "string") : [];
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

export default function RulesClient() {
  const router = useRouter();
  const [policies, setPolicies] = useState<Policy[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  useEffect(() => {
    let cancelled = false;
    void fetch("/api/policies", { cache: "no-store" })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<{ policies: Policy[] }>;
      })
      .then((policiesResult) => {
        if (cancelled) return;
        setPolicies(policiesResult.policies ?? []);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const filteredPolicies = policies?.filter((policy) =>
    matchesListSearch(
      query,
      policy.name,
      policy.description,
      policy.mode,
      ...policy.appliesToKinds,
      ...policy.appliesToScopes,
    ),
  );

  return (
    <>
      <div
        className="root"
      >
        <AppHeader back={{ href: "/admin", label: "Admin" }}>
          <SectionNav current="admin" />
        </AppHeader>

        <PageHeading
          title="Rules"
          description="Control skill learning and what agents may execute automatically, queue for review, or never run."
          actions={
          <Button
            variant="primary"
            onClick={() =>
              router.push(
                `/work?seed=${encodeURIComponent("Add a new rule that ")}`,
              )
            }
          >
            <Plus aria-hidden="true" />
            New rule
          </Button>
          }
        />

        <div className="mb-5 max-w-[520px]">
          <SearchInput
            label="Search rules"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search rules"
          />
        </div>

        <SkillLearnCard />

        {error ? (
          <div className="py-14 text-center text-sm text-danger">{error}</div>
        ) : policies === null ? (
          <SkeletonList rows={3} variant="row" label="Loading rules" />
        ) : (
          <>
            {policies.length === 0 ? (
              <div className="py-14 px-6 text-center text-sm text-text3 leading-[1.55] max-w-[520px] mx-auto">
                No rules yet. Defaults are seeded automatically the first time a
                workflow proposes an action that needs gating.
              </div>
            ) : filteredPolicies?.length === 0 ? (
              <div className="py-10 text-center text-ui-body-sm text-text2">
                No rules match “{query}”.
              </div>
            ) : (
              <section className="mt-8">
              <h2 className="mb-2.5 font-display text-ui-section font-bold tracking-[-0.01em] text-text">
                Rules
              </h2>
              <ul className="list-none p-0 m-0 flex flex-col gap-2">
                {filteredPolicies?.map((p) => (
                  <PolicyCard
                    key={p.id}
                    policy={p}
                    onEdit={() =>
                      router.push(
                        `/work?seed=${encodeURIComponent(`Update the '${p.name}' rule to `)}`,
                      )
                    }
                    onOpen={() => router.push(`/admin/rules/${p.id}`)}
                  />
                ))}
              </ul>
              </section>
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
  onOpen,
}: {
  policy: Policy;
  onEdit: () => void;
  onOpen: () => void;
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
    <Disclosure
      title={policy.name}
      meta={
        <Badge variant={modePillVariant(policy.mode)}>
          {describeMode(policy.mode)}
        </Badge>
      }
      className={cn(!policy.enabled && "opacity-60")}
    >
      {policy.description && (
        <p className="mb-3 text-ui-body text-text2 leading-[1.55]">
          {policy.description}
        </p>
      )}

      <dl className="m-0 flex flex-col gap-1 text-ui-body-sm">
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
      <ActionGroup align="start" className="mt-4">
        <Button
          size="sm"
          onClick={onOpen}
          aria-label={`Open rule ${policy.name}`}
        >
          View details
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={onEdit}
          aria-label={`Edit rule ${policy.name}`}
        >
          Edit in Work
        </Button>
      </ActionGroup>
    </Disclosure>
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
    <div className="grid grid-cols-[110px_minmax(0,1fr)] gap-2.5 items-baseline max-[480px]:grid-cols-1 max-[480px]:gap-0.5">
      <dt className="text-ui-caption font-semibold text-text3">
        {label}
      </dt>
      <dd className="m-0 text-text break-words">{children}</dd>
    </div>
  );
}
