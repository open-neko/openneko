"use client";

import Link from "next/link";
import { useState, useEffect, useCallback, useMemo } from "react";
import { useRouter } from "next/navigation";
import { applyMessage, getResolvedComponents } from "@/a2ui/surface";
import type { A2UIMessage, SurfaceState, A2UIComponent } from "@/a2ui/types";
import type { BriefingCardProps } from "@/a2ui/catalog";
import BriefingCard from "@/components/BriefingCard";
import type { BriefingCardData } from "@/components/BriefingCard";
import AppHeader from "@/components/AppHeader";
import CreatorCredit from "@/components/CreatorCredit";
import SectionNav from "@/components/SectionNav";

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
      <div className="root">
        <AppHeader>
          <SectionNav current="dashboard" />
        </AppHeader>

        <div className="dash-meta" style={{ marginBottom: 36 }}>
          <div className="pills">
            {roles.map((k) => (
              <button key={k} className={`pill${role === k ? " on" : ""}`} onClick={() => setRole(k)}>
                {k}
              </button>
            ))}
          </div>
        </div>

        {gateError ? (
          <div style={{ padding: "40px 0", textAlign: "center", color: "var(--text3)" }}>
            <div style={{ marginBottom: 8, color: "var(--text2)" }}>
              Can&apos;t reach the database right now.
            </div>
            <div style={{ fontSize: 13 }}>
              The briefing will load once the connection is back.
            </div>
            <button
              onClick={() => { setGateError(null); setGateChecked(false); setLoading(true); window.location.reload(); }}
              style={{ marginTop: 16, padding: "6px 14px", fontSize: 13, cursor: "pointer" }}
            >
              Retry
            </button>
          </div>
        ) : loading ? (
          <div style={{ padding: "40px 0", textAlign: "center", color: "var(--text3)" }}>
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
            <div className="greet" style={{ animation: "fadeUp 0.5s ease 0.05s both" }}>{greeting}</div>
            <div className="greet-sub-quote" style={{ animation: "fadeUp 0.5s ease 0.1s both" }}>{subtitle}</div>

            {metricsProgress &&
              metricsProgress.completed + metricsProgress.failed < metricsProgress.total && (
                <div
                  role="status"
                  style={{
                    marginTop: 16,
                    padding: "10px 14px",
                    borderRadius: 10,
                    border: "1px solid var(--border)",
                    background: "var(--accent-soft)",
                    color: "var(--accent)",
                    fontSize: 13,
                    display: "inline-block",
                  }}
                >
                  Building your briefing — {metricsProgress.completed + metricsProgress.failed} of {metricsProgress.total} cards complete
                </div>
              )}

            <div style={{ marginBottom: 24 }}>
              <div className="label">Today&apos;s Briefing</div>
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
          </>
        )}
      </div>

      <CreatorCredit />
    </>
  );
}
