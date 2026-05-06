"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import Markdown from "react-markdown";
import type { StageKind } from "@/lib/db";

type Phase = "processing" | "review";
type Tab = "profile" | "insights";
type InsightsStatus = "ready" | "processing" | "disabled" | "pending";

const STAGE_LABELS: Record<StageKind, string> = {
  business_profile_build: "Profile",
  industry_insights_build: "Industry insights",
  bootstrap_metrics_build: "Picking metrics",
  metric_refresh: "Computing metrics",
};
const STAGE_ORDER: StageKind[] = [
  "business_profile_build",
  "industry_insights_build",
  "bootstrap_metrics_build",
];
// Default copy when the server hasn't reported a progress.message for the
// current stage yet (typical during the first few hundred ms of a stage).
const STAGE_FALLBACK_COPY: Record<StageKind, string> = {
  business_profile_build: "Reading your data sources…",
  industry_insights_build: "Researching your industry…",
  bootstrap_metrics_build: "Picking the metrics that matter…",
  metric_refresh: "Computing your numbers…",
};

export default function ProcessingPage() {
  const router = useRouter();
  const [phase, setPhase] = useState<Phase>("processing");
  const [tab, setTab] = useState<Tab>("profile");
  const [stageKind, setStageKind] = useState<StageKind | null>(null);
  const [stageMessage, setStageMessage] = useState<string | null>(null);
  const [profile, setProfile] = useState("");
  const [insights, setInsights] = useState("");
  const [insightsStatus, setInsightsStatus] = useState<InsightsStatus>("processing");

  // Phase 1: poll for profile readiness; surface the worker's reported
  // stage + progress.message so the user sees real progress instead of a
  // rotating placeholder.
  useEffect(() => {
    if (phase !== "processing") return;

    let cancelled = false;

    const poll = async () => {
      while (!cancelled) {
        try {
          const res = await fetch("/api/onboarding/status");
          const status = await res.json();
          if (status.state === "ready") {
            const pRes = await fetch("/api/profile");
            if (pRes.ok) {
              const data = await pRes.json();
              setProfile(data.businessProfile ?? "");
              if (data.industryInsights) setInsights(data.industryInsights);
              setInsightsStatus(data.industryInsightsStatus ?? "processing");
            }
            if (!cancelled) setPhase("review");
            return;
          }
          if (status.state === "processing" && !cancelled) {
            const cs = status.currentStage as { kind?: StageKind; message?: string | null } | undefined;
            setStageKind(cs?.kind ?? null);
            setStageMessage(cs?.message ?? null);
          }
          if (status.state === "needs_wizard") {
            router.replace("/onboarding");
            return;
          }
          if (status.state === "failed") {
            // Flag only — wizard fetches the actual message from the
            // status route. Don't pass the message through the URL;
            // that's a toast-spoofing vector.
            router.replace("/onboarding?failed=1");
            return;
          }
        } catch {
          // ignore, retry
        }
        await new Promise((r) => setTimeout(r, 3000));
      }
    };
    poll();

    return () => {
      cancelled = true;
    };
  }, [phase, router]);

  // On entering review: idempotently ensure an industry_insights_build job
  // exists (heals a broken chain from a failed/orphaned prior run).
  useEffect(() => {
    if (phase !== "review") return;
    if (insights || insightsStatus === "disabled") return;

    let cancelled = false;
    (async () => {
      try {
        const res = await fetch("/api/insights/ensure", { method: "POST" });
        if (!res.ok || cancelled) return;
        const body = await res.json();
        if (body.state === "disabled") setInsightsStatus("disabled");
        else if (body.state === "ready") {
          const pRes = await fetch("/api/profile");
          if (pRes.ok && !cancelled) {
            const pData = await pRes.json();
            if (pData.industryInsights) setInsights(pData.industryInsights);
            setInsightsStatus(pData.industryInsightsStatus ?? "ready");
          }
        } else {
          setInsightsStatus("processing");
        }
      } catch {
        // fall through — poll below will recover
      }
    })();
    return () => { cancelled = true; };
  }, [phase, insights, insightsStatus]);

  // Review phase: keep polling /api/profile until insights land (or research is off).
  useEffect(() => {
    if (phase !== "review") return;
    if (insights || insightsStatus === "disabled") return;

    let cancelled = false;
    const poll = async () => {
      while (!cancelled) {
        try {
          const res = await fetch("/api/profile");
          if (res.ok) {
            const data = await res.json();
            setInsightsStatus(data.industryInsightsStatus ?? "processing");
            if (data.industryInsights) {
              setInsights(data.industryInsights);
              return;
            }
          }
        } catch {
          // ignore, retry
        }
        await new Promise((r) => setTimeout(r, 4000));
      }
    };
    poll();
    return () => { cancelled = true; };
  }, [phase, insights, insightsStatus]);

  // ─── Phase 1: Processing ───
  if (phase === "processing") {
    const message =
      stageMessage ??
      (stageKind ? STAGE_FALLBACK_COPY[stageKind] : "Reading your data sources…");
    return (
      <div className="root" style={{ paddingTop: 120, textAlign: "center" }}>
        <div className="brand" style={{ justifyContent: "center" }}>
          <img className="brand-icon" src="/cat.png" alt="" width={32} height={32} />
          <span className="brand-name">Neko</span>
        </div>
        <div className="greet" style={{ marginTop: 48 }}>Setting things up.</div>
        <div className="greet-sub">Check back in a moment.</div>
        <StageStrip current={stageKind} />
        <div className="date-note" style={{ marginTop: 24 }}>{message}</div>
      </div>
    );
  }

  // ─── Phase 2: Review (tabs) ───
  const insightsPending = !insights && insightsStatus !== "disabled";

  return (
    <div className="root" style={{ paddingTop: 60 }}>
      <Brand />
      <div className="greet" style={{ marginTop: 40, animation: "fadeUp 0.5s ease 0.1s both" }}>
        Here&apos;s what we found.
      </div>
      <div className="greet-sub" style={{ animation: "fadeUp 0.5s ease 0.15s both" }}>
        A quick look at your business before we continue.
      </div>

      <div
        className="pills"
        style={{ marginTop: 24, animation: "fadeUp 0.5s ease 0.2s both" }}
      >
        <button
          className={`pill${tab === "profile" ? " on" : ""}`}
          onClick={() => setTab("profile")}
        >
          Business Profile
        </button>
        <button
          className={`pill${tab === "insights" ? " on" : ""}`}
          onClick={() => setTab("insights")}
        >
          Industry Insights
          {insightsPending && (
            <span
              style={{
                marginLeft: 8,
                fontSize: 12,
                opacity: 0.75,
              }}
            >
              · running
            </span>
          )}
        </button>
      </div>

      <div className="profile-card" style={{ animation: "fadeUp 0.6s ease 0.3s both" }}>
        {tab === "profile" ? (
          profile ? <ProfileMarkdown content={profile} /> : <ProfileEmpty />
        ) : insightsStatus === "disabled" ? (
          <InsightsDisabled />
        ) : insights ? (
          <ProfileMarkdown content={insights} />
        ) : (
          <InsightsLoading />
        )}
      </div>

      <div
        style={{
          marginTop: 28,
          display: "flex",
          gap: 10,
          alignItems: "center",
          flexWrap: "wrap",
          animation: "fadeUp 0.5s ease 0.45s both",
        }}
      >
        <button
          className="pill on"
          onClick={() => router.replace("/")}
          style={{ padding: "14px 32px", fontSize: 15 }}
        >
          Continue to your briefing
        </button>
        {insights && insightsStatus !== "disabled" && (
          <button
            className="pill"
            onClick={async () => {
              try {
                const res = await fetch("/api/insights/ensure", {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({ force: true }),
                });
                if (res.ok) {
                  setInsights("");
                  setInsightsStatus("processing");
                  setTab("insights");
                }
              } catch {
                // non-fatal; poll will eventually reflect state
              }
            }}
            style={{ padding: "14px 24px", fontSize: 14 }}
          >
            Regenerate insights
          </button>
        )}
      </div>
    </div>
  );
}

function StageStrip({ current }: { current: StageKind | null }) {
  // We render only the three stages the user is on /business-profile for —
  // metric_refresh runs after the page transitions to /review and the
  // dashboard, so it's not relevant here.
  const idx = current ? STAGE_ORDER.indexOf(current) : 0;
  return (
    <div
      style={{
        display: "flex",
        gap: 8,
        justifyContent: "center",
        marginTop: 36,
      }}
    >
      {STAGE_ORDER.map((kind, i) => {
        const state = i < idx ? "done" : i === idx ? "active" : "pending";
        const dot =
          state === "done" ? "✓" : state === "active" ? "●" : "○";
        return (
          <div
            key={kind}
            style={{
              padding: "8px 14px",
              borderRadius: 999,
              border: "1px solid var(--border)",
              background: state === "active" ? "var(--accent-soft)" : "transparent",
              color: state === "done" ? "var(--text2)" : state === "active" ? "var(--accent)" : "var(--text3)",
              fontSize: 13,
              display: "inline-flex",
              alignItems: "center",
              gap: 6,
            }}
          >
            <span aria-hidden style={{ fontSize: 11 }}>{dot}</span>
            <span>{STAGE_LABELS[kind]}</span>
          </div>
        );
      })}
    </div>
  );
}

function InsightsLoading() {
  return (
    <div style={{ padding: "24px 4px", textAlign: "center" }}>
      <div
        className="pm-h3"
        style={{ marginTop: 0, marginBottom: 8 }}
      >
        Researching your industry…
      </div>
      <div className="pm-p" style={{ color: "var(--text2)" }}>
        Deep-diving into industry trends and benchmarks — this usually takes a minute or two.
      </div>
    </div>
  );
}

function ProfileEmpty() {
  return (
    <div style={{ padding: "24px 4px", textAlign: "center" }}>
      <div className="pm-h3" style={{ marginTop: 0, marginBottom: 8 }}>
        Your business profile is being assembled
      </div>
      <div className="pm-p" style={{ color: "var(--text2)" }}>
        Neko reads what you shared in onboarding plus signals from your connected
        data sources. The profile will appear here as soon as it&apos;s ready.
      </div>
    </div>
  );
}

function InsightsDisabled() {
  return (
    <div style={{ padding: "24px 4px", textAlign: "center" }}>
      <div className="pm-h3" style={{ marginTop: 0, marginBottom: 8 }}>
        Industry research is off
      </div>
      <div className="pm-p" style={{ color: "var(--text2)" }}>
        Enable Perplexity-backed industry research later from Settings.
      </div>
    </div>
  );
}

function Brand() {
  return (
    <div className="brand" style={{ justifyContent: "center", animation: "fadeUp 0.5s ease both" }}>
      <img className="brand-icon" src="/cat.png" alt="" width={32} height={32} />
      <span className="brand-name">Neko</span>
    </div>
  );
}

const mdComponents = {
  h1: (p: React.ComponentProps<"h2">) => <h2 className="pm-h2" {...p} />,
  h2: (p: React.ComponentProps<"h3">) => <h3 className="pm-h3" {...p} />,
  p: (p: React.ComponentProps<"p">) => <p className="pm-p" {...p} />,
  ul: (p: React.ComponentProps<"ul">) => <ul className="pm-ul" {...p} />,
  a: (p: React.ComponentProps<"a">) => <a className="pm-cite" target="_blank" rel="noopener noreferrer" {...p} />,
};

function ProfileMarkdown({ content }: { content: string }) {
  if (!content) return null;
  return <Markdown components={mdComponents}>{content}</Markdown>;
}
