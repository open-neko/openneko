"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import AppHeader from "@/components/AppHeader";
import SectionNav from "@/components/SectionNav";
import EditableMarkdown from "@/components/EditableMarkdown";
import { useDebouncedSave } from "@/hooks/useDebouncedSave";
import type { StageKind } from "@/lib/db";

type Phase = "loading" | "processing" | "review";
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
const STAGE_FALLBACK_COPY: Record<StageKind, readonly string[]> = {
  business_profile_build: [
    "Reading your data sources…",
    "Mapping tables and relationships…",
    "Drafting your business profile…",
  ],
  industry_insights_build: [
    "Researching your industry…",
    "Gathering competitive context…",
    "Distilling industry trends…",
  ],
  bootstrap_metrics_build: ["Picking the metrics that matter…"],
  metric_refresh: ["Computing your numbers…"],
};
const FALLBACK_DEFAULT = "Reading your data sources…";
const FALLBACK_CYCLE_MS = 3500;

export default function ProcessingPage() {
  const router = useRouter();
  const [phase, setPhase] = useState<Phase>("loading");
  const [tab, setTab] = useState<Tab>("profile");
  const [stageKind, setStageKind] = useState<StageKind | null>(null);
  const [stageMessage, setStageMessage] = useState<string | null>(null);
  const [fallbackIdx, setFallbackIdx] = useState(0);
  const [profile, setProfile] = useState("");
  const [insights, setInsights] = useState("");
  const [insightsStatus, setInsightsStatus] = useState<InsightsStatus>("processing");
  const profileEditedRef = useRef(false);
  const insightsEditedRef = useRef(false);

  const saveProfile = useCallback(async (v: string) => {
    const res = await fetch("/api/profile", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ businessProfile: v }),
    });
    if (!res.ok) throw new Error(`save failed: ${res.status}`);
  }, []);
  const saveInsights = useCallback(async (v: string) => {
    const res = await fetch("/api/profile", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ industryInsights: v }),
    });
    if (!res.ok) throw new Error(`save failed: ${res.status}`);
  }, []);
  const profileSaver = useDebouncedSave<string>({ delayMs: 800, save: saveProfile });
  const insightsSaver = useDebouncedSave<string>({ delayMs: 800, save: saveInsights });
  const flushAll = useCallback(
    async () => {
      await Promise.all([profileSaver.flush(), insightsSaver.flush()]);
    },
    [profileSaver, insightsSaver],
  );
  const saveError = profileSaver.lastError ?? insightsSaver.lastError;

  const handleProfileChange = useCallback(
    (next: string) => {
      profileEditedRef.current = true;
      setProfile(next);
      profileSaver.schedule(next);
    },
    [profileSaver],
  );
  const handleInsightsChange = useCallback(
    (next: string) => {
      insightsEditedRef.current = true;
      setInsights(next);
      insightsSaver.schedule(next);
    },
    [insightsSaver],
  );

  useEffect(() => {
    if (phase === "review") return;

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
              if (!profileEditedRef.current) setProfile(data.businessProfile ?? "");
              if (data.industryInsights && !insightsEditedRef.current) {
                setInsights(data.industryInsights);
              }
              setInsightsStatus(data.industryInsightsStatus ?? "processing");
            }
            if (!cancelled) setPhase("review");
            return;
          }
          if (status.state === "processing" && !cancelled) {
            const cs = status.currentStage as { kind?: StageKind; message?: string | null } | undefined;
            const nextStage = cs?.kind ?? null;
            const nextMsg = cs?.message ?? null;
            setStageKind((prev) => {
              if (prev !== nextStage) setFallbackIdx(0);
              return nextStage;
            });
            setStageMessage(nextMsg);
            setPhase("processing");
          }
          if (status.state === "needs_wizard") {
            router.replace("/onboarding");
            return;
          }
          if (status.state === "failed") {
            router.replace("/onboarding?failed=1");
            return;
          }
        } catch {}
        await new Promise((r) => setTimeout(r, 3000));
      }
    };
    poll();

    return () => {
      cancelled = true;
    };
  }, [phase, router]);

  useEffect(() => {
    if (phase !== "processing") return;
    if (stageMessage) return;
    const id = setInterval(() => {
      setFallbackIdx((i) => i + 1);
    }, FALLBACK_CYCLE_MS);
    return () => clearInterval(id);
  }, [phase, stageMessage, stageKind]);

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
            if (pData.industryInsights && !insightsEditedRef.current) {
              setInsights(pData.industryInsights);
            }
            setInsightsStatus(pData.industryInsightsStatus ?? "ready");
          }
        } else {
          setInsightsStatus("processing");
        }
      } catch {}
    })();
    return () => { cancelled = true; };
  }, [phase, insights, insightsStatus]);

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
              if (!insightsEditedRef.current) setInsights(data.industryInsights);
              return;
            }
          }
        } catch {}
        await new Promise((r) => setTimeout(r, 4000));
      }
    };
    poll();
    return () => { cancelled = true; };
  }, [phase, insights, insightsStatus]);

  if (phase === "loading") {
    return (
      <div className="root">
        <AppHeader>
          <SectionNav current="business-profile" />
        </AppHeader>
      </div>
    );
  }

  if (phase === "processing") {
    let message: string;
    if (stageMessage) {
      message = stageMessage;
    } else if (stageKind) {
      const cycle = STAGE_FALLBACK_COPY[stageKind];
      message = cycle[fallbackIdx % cycle.length] ?? FALLBACK_DEFAULT;
    } else {
      message = FALLBACK_DEFAULT;
    }
    return (
      <div className="root" style={{ textAlign: "center" }}>
        <AppHeader>
          <SectionNav current="business-profile" />
        </AppHeader>
        <div className="greet" style={{ marginTop: 48 }}>Setting things up.</div>
        <div className="greet-sub">Check back in a moment.</div>
        <StageStrip current={stageKind} />
        <div
          key={message}
          className="date-note"
          style={{ marginTop: 24, animation: "fadeUp 0.5s ease both" }}
        >
          {message}
        </div>
      </div>
    );
  }

  const insightsPending = !insights && insightsStatus !== "disabled";

  return (
    <div className="root">
      <AppHeader>
        <SectionNav current="business-profile" />
      </AppHeader>
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
          onClick={async () => {
            await flushAll();
            setTab("profile");
          }}
        >
          Business Profile
        </button>
        <button
          className={`pill${tab === "insights" ? " on" : ""}`}
          onClick={async () => {
            await flushAll();
            setTab("insights");
          }}
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
          <EditableMarkdown
            value={profile}
            onChange={handleProfileChange}
            onCommit={() => profileSaver.flush()}
            placeholder={<ProfileEmpty />}
            components={mdComponents}
            ariaLabel="Business profile"
          />
        ) : insightsStatus === "disabled" ? (
          <InsightsDisabled />
        ) : insights ? (
          <EditableMarkdown
            value={insights}
            onChange={handleInsightsChange}
            onCommit={() => insightsSaver.flush()}
            components={mdComponents}
            ariaLabel="Industry insights"
          />
        ) : (
          <InsightsLoading />
        )}
      </div>
      {saveError && (
        <div
          style={{
            marginTop: 8,
            fontSize: 12,
            color: "var(--text3)",
            textAlign: "right",
          }}
          role="status"
        >
          Couldn&apos;t save — will retry on your next edit.
        </div>
      )}

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
          onClick={async () => {
            await flushAll();
            router.replace("/");
          }}
          style={{ padding: "14px 32px", fontSize: 15 }}
        >
          Continue to your briefing
        </button>
        {insights && insightsStatus !== "disabled" && (
          <button
            className="pill"
            onClick={async () => {
              await flushAll();
              try {
                const res = await fetch("/api/insights/ensure", {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({ force: true }),
                });
                if (res.ok) {
                  insightsEditedRef.current = false;
                  setInsights("");
                  setInsightsStatus("processing");
                  setTab("insights");
                }
              } catch {}
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
        OpenNeko reads what you shared in onboarding plus signals from your connected
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
        Enable industry research from Settings.
      </div>
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
