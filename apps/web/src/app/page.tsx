"use client";

import Link from "next/link";
import { useState, useEffect, useCallback, useMemo } from "react";
import { useRouter } from "next/navigation";
import { applyMessage, getResolvedComponents } from "@/a2ui/surface";
import type { A2UIMessage, SurfaceState, A2UIComponent } from "@/a2ui/types";
import type { BriefingCardProps } from "@/a2ui/catalog";
import BriefingCard from "@/components/BriefingCard";
import type { BriefingCardData } from "@/components/BriefingCard";
import FindingCard, { type FindingCardData } from "@/components/FindingCard";
import ActCard, {
  type ActCardData,
  type ActRowData,
} from "@/components/ActCard";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";
import HoursSavedHero, {
  type HoursSavedItem,
  type HoursSavedValue,
} from "@/components/HoursSavedHero";
import { formatSavedShort } from "@/lib/hours-saved";
import { cn } from "@/lib/cn";

type FindingsPayload = {
  summary: {
    id: string;
    summaryMd: string;
    createdAt: string;
  } | null;
  awaitingYou: {
    approvals: FindingCardData[];
    actFindings: FindingCardData[];
  };
  pinned: FindingCardData[];
  worthKnowing: FindingCardData[];
  quiet: { goodOutputs: number; windowHours: number };
};

type ReceiptRow = {
  id: string;
  kind: string;
  target: string | null;
  summary: string | null;
  scope: string;
  riskLevel: string | null;
  status: string;
  executedAt: string | null;
  executionStatus: string;
  minutesSaved: number | null;
  minutesSavedBasis: string | null;
  approverKind: "operator" | "policy" | "auto";
  approverLabel: string | null;
  workflowRunId: string | null;
  workflow: { id: string; name: string } | null;
  trigger: string | null;
};

type RecentActionsPayload = {
  receipts: ReceiptRow[];
  windowHours: number;
};

type AwaitingRow = {
  id: string;
  workflowRunId: string;
  workflow: { id: string; name: string };
  triggeredByObservation: { title: string } | null;
  kind: string;
  target: string | null;
  riskLevel: string | null;
  summary: string;
  status: string;
  runAt: string;
};

type AwaitingPayload = {
  actions: AwaitingRow[];
  count: number;
};

function approverPhrase(
  kind: ReceiptRow["approverKind"],
  label: string | null,
): string {
  if (kind === "operator") return label ? `you · ${label}` : "you";
  if (kind === "policy") return label ? `rule "${label}"` : "a rule";
  return "auto";
}

function groupAwaiting(
  rows: AwaitingRow[],
): Array<{ key: string; card: ActCardData }> {
  const groups = new Map<
    string,
    {
      key: string;
      runId: string;
      runAt: string;
      trigger: string | null;
      workflowName: string;
      rows: ActRowData[];
    }
  >();
  for (const r of rows) {
    const tone: ActRowData["tone"] =
      r.riskLevel === "high" || r.riskLevel === "critical" ? "action" : "watch";
    const row: ActRowData = {
      id: r.id,
      tone,
      headline: r.summary || r.kind,
      detail: null,
      target: r.target,
      status: r.status,
    };
    const existing = groups.get(r.workflowRunId);
    if (existing) {
      existing.rows.push(row);
    } else {
      groups.set(r.workflowRunId, {
        key: r.workflowRunId,
        runId: r.workflowRunId,
        runAt: r.runAt,
        trigger: r.triggeredByObservation?.title ?? null,
        workflowName: r.workflow.name,
        rows: [row],
      });
    }
  }
  return Array.from(groups.values()).map((g) => ({
    key: g.key,
    card: {
      runId: g.runId,
      runAt: g.runAt,
      trigger: g.trigger,
      state: "awaiting",
      workflowName: g.workflowName,
      rows: g.rows,
    },
  }));
}

function groupReceipts(
  receipts: ReceiptRow[],
): Array<{ key: string; card: ActCardData }> {
  const groups = new Map<
    string,
    { key: string; runId: string | null; runAt: string; trigger: string | null; workflowName: string | null; rows: ActRowData[] }
  >();
  for (const r of receipts) {
    const failed =
      r.executionStatus !== "succeeded" && r.executionStatus !== "executed";
    const key = r.workflowRunId ?? `__${r.id}`;
    const headline =
      r.summary ?? `${r.kind}${r.target ? ` → ${r.target}` : ""}`;
    const row: ActRowData = {
      id: r.id,
      tone: failed ? "action" : "good",
      headline,
      detail: null,
      target: r.summary ? r.target : null, // headline already includes target when summary is absent
      approverPhrase: approverPhrase(r.approverKind, r.approverLabel),
      status: r.status,
      minutesSaved: r.minutesSaved,
    };
    const existing = groups.get(key);
    if (existing) {
      existing.rows.push(row);
    } else {
      groups.set(key, {
        key,
        runId: r.workflowRunId,
        runAt: r.executedAt ?? new Date().toISOString(),
        trigger: r.trigger,
        workflowName: r.workflow?.name ?? null,
        rows: [row],
      });
    }
  }
  return Array.from(groups.values()).map((g) => ({
    key: g.key,
    card: {
      runId: g.runId,
      runAt: g.runAt,
      trigger: g.trigger,
      state: "live",
      workflowName: g.workflowName,
      rows: g.rows,
    },
  }));
}

export default function Dashboard() {
  const router = useRouter();
  const [gateChecked, setGateChecked] = useState(false);
  const [roles, setRoles] = useState<string[]>([]);
  const [role, setRole] = useState("");
  const [gateError, setGateError] = useState<string | null>(null);

  // Onboarding gate: redirect to wizard or processing if no current profile.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/onboarding/status");
        const status = await res.json().catch(() => ({ state: "db_error", message: "Could not reach server" }));
        if (cancelled) return;
        if (status.state === "db_error") {
          setGateError(status.message ?? "Database unavailable");
          setGateChecked(true);
          return;
        }
        if (status.state === "needs_wizard") {
          router.replace("/onboarding");
          return;
        }
        if (status.state === "failed") {
          router.replace("/onboarding?failed=1");
          return;
        }
        if (status.state === "processing") {
          router.replace("/business-profile");
          return;
        }
        const seats: string[] = Array.isArray(status.seats) ? status.seats : [];
        setRoles(seats);
        setRole(seats[0] ?? "");
        setGateChecked(true);
      } catch (err) {
        if (cancelled) return;
        setGateError(err instanceof Error ? err.message : "Could not reach server");
        setGateChecked(true);
      }
    })();
    return () => { cancelled = true; };
  }, [router]);

  const [surfaces, setSurfaces] = useState<Map<string, SurfaceState>>(new Map());
  const [loading, setLoading] = useState(true);
  const [findings, setFindings] = useState<FindingsPayload | null>(null);
  const [recentActions, setRecentActions] =
    useState<RecentActionsPayload | null>(null);
  const [awaiting, setAwaiting] = useState<AwaitingPayload | null>(null);
  const [hoursSaved, setHoursSaved] = useState<HoursSavedValue | null>(null);

  // Tributaries: workflow_output findings + pending approvals + live summary.
  // Polled alongside the KPI briefing so newly-produced findings appear
  // without a reload.
  const fetchFindings = useCallback(async () => {
    try {
      const res = await fetch("/api/briefing/findings", { cache: "no-store" });
      if (!res.ok) return;
      const data = (await res.json()) as FindingsPayload;
      setFindings(data);
    } catch {
      // best-effort; the page still renders without findings
    }
  }, []);

  const fetchRecentActions = useCallback(async () => {
    try {
      const res = await fetch("/api/briefing/recent-actions", {
        cache: "no-store",
      });
      if (!res.ok) return;
      const data = (await res.json()) as RecentActionsPayload;
      setRecentActions(data);
    } catch {
      // best-effort
    }
  }, []);

  const fetchAwaiting = useCallback(async () => {
    try {
      const res = await fetch("/api/approvals?filter=awaiting", {
        cache: "no-store",
      });
      if (!res.ok) return;
      const data = (await res.json()) as AwaitingPayload;
      setAwaiting(data);
    } catch {
      // best-effort
    }
  }, []);

  const fetchHoursSaved = useCallback(async () => {
    try {
      const res = await fetch("/api/briefing/value", { cache: "no-store" });
      if (!res.ok) return;
      const data = (await res.json()) as HoursSavedValue;
      setHoursSaved(data);
    } catch {
      // best-effort; the page still renders without the value hero
    }
  }, []);

  useEffect(() => {
    if (!gateChecked) return;
    void fetchFindings();
    void fetchRecentActions();
    void fetchAwaiting();
    void fetchHoursSaved();
    const id = setInterval(() => {
      void fetchFindings();
      void fetchRecentActions();
      void fetchAwaiting();
      void fetchHoursSaved();
    }, 30_000);
    return () => clearInterval(id);
  }, [gateChecked, fetchFindings, fetchRecentActions, fetchAwaiting, fetchHoursSaved]);

  const surfaceId = `briefing-${role.toLowerCase()}`;
  const surface = surfaces.get(surfaceId);

  // Fetch briefing from API. `silent=true` skips the loading toggle so
  // background polls (pending-card refresh, retry, dismiss) don't unmount
  // the entire briefing tree by flipping the page-level loading ternary.
  // Only the initial role-change fetch should show "Loading…".
  const fetchBriefing = useCallback(async (r: string, silent = false) => {
    if (!silent) setLoading(true);
    try {
      const res = await fetch(`/api/briefing?role=${r}`);
      const messages: A2UIMessage[] = await res.json();

      setSurfaces((prev) => {
        let next = prev;
        for (const msg of messages) {
          next = applyMessage(next, msg);
        }
        return next;
      });
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!gateChecked) return;
    if (!role) {
      setLoading(false);
      return;
    }
    fetchBriefing(role);
  }, [role, fetchBriefing, gateChecked]);

  // Track whether the current role's surface has any cards still pending
  // (no snapshot yet, refresh job not yet succeeded).
  const hasPendingCards = useMemo(() => {
    if (!surface) return false;
    const comps = getResolvedComponents(surface).filter(
      (c) => c.component === "BriefingCard",
    );
    return comps.some((c) => {
      const p = c as unknown as BriefingCardProps;
      return p.state === "pending";
    });
  }, [surface]);

  // While any card is pending, re-fetch every 30s so newly-completed
  // metric_refresh jobs show up without a reload.
  useEffect(() => {
    if (!hasPendingCards || !role) return;
    const id = setInterval(() => {
      fetchBriefing(role, true);
    }, 30_000);
    return () => clearInterval(id);
  }, [hasPendingCards, role, fetchBriefing]);

  // Cross-role progress tracker for the metric_refresh fan-out at onboarding.
  const [metricsProgress, setMetricsProgress] = useState<
    { total: number; completed: number; failed: number } | null
  >(null);
  useEffect(() => {
    if (!gateChecked) return;
    let cancelled = false;
    let stopped = false;
    const tick = async () => {
      if (cancelled) return;
      try {
        const res = await fetch("/api/onboarding/status");
        const status = await res.json();
        if (cancelled) return;
        if (status.metricsProgress) {
          setMetricsProgress((prev) => {
            const next = status.metricsProgress;
            if (
              prev &&
              prev.total === next.total &&
              prev.completed === next.completed &&
              prev.failed === next.failed
            ) {
              return prev;
            }
            return next;
          });
          const settled =
            status.metricsProgress.completed + status.metricsProgress.failed;
          if (settled >= status.metricsProgress.total) {
            stopped = true;
          }
        } else {
          setMetricsProgress((prev) => (prev === null ? prev : null));
          stopped = true;
        }
      } catch {}
    };
    tick();
    const id = setInterval(() => {
      if (stopped) {
        clearInterval(id);
        return;
      }
      tick();
    }, 10_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [gateChecked]);

  const resolved = surface ? getResolvedComponents(surface) : [];
  const rootComp = resolved.find((c) => c.id === "root");
  const briefingComps = resolved.filter((c) => c.component === "BriefingCard");

  const greeting = (rootComp as A2UIComponent & { greeting?: string })?.greeting ?? "";
  const subtitle = (rootComp as A2UIComponent & { subtitle?: string })?.subtitle ?? "";
  const isExample = (rootComp as A2UIComponent & { isExample?: boolean })?.isExample === true;

  const briefingCards: BriefingCardData[] = briefingComps.map((c) => {
    const props = c as unknown as BriefingCardProps & { id: string };
    return {
      id: props.id,
      metricId: props.metricId,
      source: props.source,
      state: props.state,
      error: props.error,
      mood: props.mood,
      text: props.text,
      metric: props.metric,
      label: props.label,
      detail: props.detail,
      chart: props.chartType,
      chartData: props.chartData,
    };
  });

  const dismissCard = async (metricId: string) => {
    await fetch("/api/briefing/pin", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ metricId, active: false }),
    });
    fetchBriefing(role, true);
  };

  const retryCard = async (metricId: string) => {
    await fetch("/api/briefing/retry", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ metricId }),
    });
    fetchBriefing(role, true);
  };

  const deepDiveCard = useCallback(
    async (metricId: string) => {
      try {
        const res = await fetch("/api/work/threads", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ seedMetricId: metricId }),
        });
        if (!res.ok) return;
        const data = (await res.json()) as { thread: { id: string } };
        if (!data.thread?.id) return;
        router.push(`/work/${data.thread.id}`);
      } catch {}
    },
    [router],
  );

  // Render the formatted date only after mount. toLocaleDateString depends
  // on the runtime's ICU data, which can differ between Node and the
  // browser — an empty initial value keeps server + first client render
  // in agreement, and useEffect populates the real string after hydration.
  const [ds, setDs] = useState("");
  useEffect(() => {
    setDs(
      new Date().toLocaleDateString("en-IN", {
        weekday: "short",
        month: "long",
        day: "numeric",
      }),
    );
  }, []);

  return (
    <>
      <div className="root dash-root">
        <AppHeader>
          <SectionNav current="dashboard" />
        </AppHeader>

        <div className="flex items-center gap-2.5 flex-wrap mb-9">
          <div className="flex gap-[7px] flex-wrap">
            {roles.map((k) => {
              const isOn = role === k;
              return (
                <button
                  key={k}
                  onClick={() => setRole(k)}
                  className={cn(
                    "px-4.5 py-2.5 rounded-full border-[1.5px] font-body text-[14.5px] font-medium cursor-pointer",
                    "transition-[color,background,border-color,transform,box-shadow] duration-200",
                    !isOn &&
                      "bg-white/60 border-border text-text2 hover:border-accent hover:text-accent hover:bg-accent-soft hover:-translate-y-px",
                    isOn &&
                      "bg-text border-text text-bg shadow-[0_2px_10px_rgba(20,18,12,0.18)] before:content-[''] before:inline-block before:w-1.5 before:h-1.5 before:rounded-full before:bg-success before:mr-2 before:align-[1px] before:shadow-[0_0_0_3px_rgba(108,255,127,0.18)]",
                  )}
                >
                  {k}
                </button>
              );
            })}
          </div>
        </div>

        {gateError ? (
          <div className="py-10 text-center text-text3">
            <div className="mb-2 text-text2">
              Can&apos;t reach the database right now.
            </div>
            <div className="text-[13px]">
              The briefing will load once the connection is back.
            </div>
            <button
              onClick={() => { setGateError(null); setGateChecked(false); setLoading(true); window.location.reload(); }}
              className="mt-4 px-3.5 py-1.5 text-[13px] cursor-pointer"
            >
              Retry
            </button>
          </div>
        ) : loading ? (
          <div className="py-10 text-center text-text3">
            Loading briefing...
          </div>
        ) : (
          <>
            {isExample && (
              <Link
                href="/onboarding"
                className="example-banner"
                style={{ animation: "fadeUp 0.5s ease both" }}
              >
                Example metrics · set up your own →
              </Link>
            )}
            <div
              className="greet-eyebrow"
              style={{ animation: "fadeUp 0.5s ease both" }}
            >
              <span className="greet-eyebrow-rule" aria-hidden="true" />
              <span className="greet-eyebrow-accent">{role ? `${role} Briefing` : "Today"}</span>
              {ds && <span aria-hidden="true">·</span>}
              {ds && <span>{ds}</span>}
            </div>
            {/* Legacy greeting + subtitle from the KPI-only briefing API.
                When the live summary is present, it's the canonical
                read-on-the-business; the legacy greeting goes silent so the
                page doesn't contradict itself. */}
            {!findings?.summary && (
              <>
                <div className="greet" style={{ animation: "fadeUp 0.5s ease 0.05s both" }}>{greeting}</div>
                <div className="greet-sub-quote" style={{ animation: "fadeUp 0.5s ease 0.1s both" }}>{subtitle}</div>
              </>
            )}

            {findings?.summary && (
              <div
                className="my-5 mb-7 text-base leading-[1.55] text-text max-w-[620px]"
                style={{ animation: "fadeUp 0.5s ease 0.15s both" }}
              >
                <p className="m-0">{findings.summary.summaryMd}</p>
                <div className="mt-2 font-mono text-[11.5px] text-text3 italic">
                  as of {new Date(findings.summary.createdAt).toLocaleTimeString("en-IN", {
                    hour: "numeric",
                    minute: "2-digit",
                    hour12: true,
                  })}
                </div>
              </div>
            )}

            {hoursSaved && hoursSaved.totalMinutes > 0 && (
              <HoursSavedHero
                value={hoursSaved}
                items={(recentActions?.receipts ?? [])
                  .filter((r) => (r.minutesSaved ?? 0) > 0)
                  .map<HoursSavedItem>((r) => ({
                    label: r.summary || r.kind,
                    minutes: r.minutesSaved ?? 0,
                    basis: r.minutesSavedBasis,
                  }))}
              />
            )}

            {awaiting && awaiting.actions.length > 0 && (
              <section
                className="dash-call"
                style={{ animation: "fadeUp 0.5s ease 0.2s both" }}
                aria-label="Decisions needed"
              >
                <div className="dash-call-eyebrow">
                  <span className="dash-call-dot" aria-hidden="true" />
                  <span className="dash-call-label">Needs your call</span>
                  <span className="font-mono dash-call-count">
                    {awaiting.count}
                  </span>
                  <a className="dash-call-skim" href="/actions?filter=awaiting">
                    open all →
                  </a>
                </div>
                <div className="act-list">
                  {groupAwaiting(awaiting.actions).map((group, i) => (
                    <ActCard key={group.key} data={group.card} index={i} />
                  ))}
                </div>
              </section>
            )}

            {findings && findings.awaitingYou.actFindings.length > 0 && (
              <section
                className="mb-7"
                style={{ animation: "fadeUp 0.5s ease 0.21s both" }}
              >
                <div className="text-[11px] font-bold tracking-[0.13em] uppercase text-text3 mb-3">
                  Worth your read
                </div>
                <div className="flex flex-col gap-3">
                  {findings.awaitingYou.actFindings.map((f, i) => (
                    <FindingCard key={f.id} data={f} index={i} />
                  ))}
                </div>
              </section>
            )}

            {findings && findings.pinned.length > 0 && (
              <section
                className="mb-7"
                style={{ animation: "fadeUp 0.5s ease 0.22s both" }}
              >
                <div className="text-[11px] font-bold tracking-[0.13em] uppercase text-text3 mb-3">
                  Pinned
                </div>
                <div className="flex flex-col gap-3">
                  {findings.pinned.map((f, i) => (
                    <FindingCard
                      key={f.pinId ?? f.id}
                      data={f}
                      index={i}
                      onUnpin={async (pinId) => {
                        await fetch(`/api/briefing/pins/${pinId}`, {
                          method: "DELETE",
                        });
                        void fetchFindings();
                      }}
                    />
                  ))}
                </div>
              </section>
            )}

            {findings && findings.worthKnowing.length > 0 && (
              <section
                className="mb-7"
                style={{ animation: "fadeUp 0.5s ease 0.25s both" }}
              >
                <div className="text-[11px] font-bold tracking-[0.13em] uppercase text-text3 mb-3">
                  Worth knowing
                </div>
                <div className="flex flex-col gap-3">
                  {findings.worthKnowing.map((f, i) => (
                    <FindingCard key={f.id} data={f} index={i} />
                  ))}
                </div>
              </section>
            )}

            {findings && findings.quiet.goodOutputs > 0 && (
              <div
                className="-mt-1.5 mb-7 text-[13px] text-text3 italic"
                style={{ animation: "fadeUp 0.5s ease 0.3s both" }}
              >
                {findings.quiet.goodOutputs} healthy run
                {findings.quiet.goodOutputs === 1 ? "" : "s"} in the last
                {" "}{findings.quiet.windowHours}h.
              </div>
            )}

            {metricsProgress &&
              metricsProgress.completed + metricsProgress.failed < metricsProgress.total && (
                <div
                  role="status"
                  className="mt-4 px-3.5 py-2.5 rounded-[10px] border border-border bg-accent-soft text-accent text-[13px] inline-block"
                >
                  Building your briefing — {metricsProgress.completed + metricsProgress.failed} of {metricsProgress.total} cards complete
                </div>
              )}

            <div className="mb-6">
              <div className="label">Today&apos;s Briefing</div>
              <div className="brief-grid">
                {briefingCards.map((ins, i) => (
                  <BriefingCard
                    key={ins.id}
                    ins={ins}
                    index={i}
                    onDismiss={() => dismissCard(ins.metricId)}
                    onRetry={retryCard}
                    onDeepDive={deepDiveCard}
                  />
                ))}
              </div>
            </div>

            {recentActions && recentActions.receipts.length > 0 && (
              <details
                className="dash-proof"
                style={{ animation: "fadeUp 0.5s ease 0.35s both" }}
              >
                <summary className="dash-proof-summary">
                  <span className="font-mono dash-proof-tick" aria-hidden="true">↳</span>
                  <span className="dash-proof-count">
                    {recentActions.receipts.length}
                  </span>
                  <span className="dash-proof-label">
                    fired on your behalf in the last {recentActions.windowHours}h
                    {hoursSaved && hoursSaved.windowMinutes > 0 && (
                      <> · saved you {formatSavedShort(hoursSaved.windowMinutes)}</>
                    )}
                  </span>
                  <span className="dash-proof-caret" aria-hidden="true">▸</span>
                </summary>
                <div className="dash-proof-body act-list">
                  {groupReceipts(recentActions.receipts).map((group, i) => (
                    <ActCard key={group.key} data={group.card} index={i} />
                  ))}
                </div>
              </details>
            )}
          </>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}
