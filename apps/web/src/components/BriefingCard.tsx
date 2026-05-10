"use client";

import { useState } from "react";
import Chart from "./Chart";
import type { ChartDataPoint } from "./Chart";
import KpiHeadline from "./KpiHeadline";

async function copyCardToClipboard(ins: BriefingCardData): Promise<void> {
  const lines: string[] = [];
  if (ins.text) lines.push(ins.text);
  if (ins.metric || ins.label) {
    lines.push(`${ins.metric}${ins.label ? ` (${ins.label})` : ""}`.trim());
  }
  if (ins.detail) lines.push(ins.detail);
  const text = lines.join("\n");
  try {
    await navigator.clipboard.writeText(text);
  } catch {}
}

const MOOD_STYLES: Record<string, { bg: string; dot: string }> = {
  good: { bg: "#E8F5EC", dot: "#4CAF82" },
  watch: { bg: "#FFF4E5", dot: "#E9A23B" },
  act: { bg: "#FEECEC", dot: "#E05656" },
  bad: { bg: "#FEECEC", dot: "#E05656" },
};

export type BriefingCardState = "ok" | "pending" | "failed";

export interface BriefingCardData {
  id: string;
  metricId: string;
  source: string;
  state?: BriefingCardState;
  error?: string;
  mood: string;
  text: string;
  metric: string;
  label: string;
  detail: string;
  chart: string;
  chartData: ChartDataPoint[];
}

export default function BriefingCard({ ins, index, onDismiss, onRetry }: {
  ins: BriefingCardData;
  index: number;
  onDismiss?: () => void;
  onRetry?: (metricId: string) => void;
}) {
  const [open, setOpen] = useState(true);
  const [retrying, setRetrying] = useState(false);
  const state: BriefingCardState = ins.state ?? "ok";
  const m = MOOD_STYLES[ins.mood] ?? MOOD_STYLES.good;

  const refreshing = retrying || state === "pending";

  const handleRetry = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!onRetry || !ins.metricId || refreshing) return;
    setRetrying(true);
    try {
      await onRetry(ins.metricId);
    } finally {
      setRetrying(false);
    }
  };

  return (
    <div
      className={`icard${open ? " exp" : ""}${state === "failed" ? " icard-failed" : ""}${state === "pending" ? " icard-pending" : ""}`}
      style={{ animation: `fadeUp 0.5s ease ${index * 0.07}s both` }}
      onClick={() => setOpen(!open)}
    >
      <div className="itop">
        <div className="mdot" style={{ background: m.dot, boxShadow: `0 0 0 4px ${m.bg}` }} />
        <div className="icontent">
          <div className="itext">{ins.text}</div>
          {state === "pending" ? (
            <div className="iskel" aria-label="Refreshing metric" aria-busy="true">
              <div className="skel skel-metric" />
              <div className="skel skel-label" />
            </div>
          ) : ins.metric ? (
            <KpiHeadline
              metric={ins.metric}
              label={ins.label}
              data={state === "ok" ? ins.chartData : undefined}
              size="card"
            />
          ) : null}
        </div>
      </div>
      <div className="iactions">
        {onRetry && ins.metricId && (
          <button
            className="ipin"
            onClick={handleRetry}
            disabled={refreshing}
            title={refreshing ? "Re-running…" : "Re-run this metric"}
            aria-label="Re-run this metric"
            aria-busy={refreshing}
            style={{ marginRight: 6 }}
          >
            <span style={{ display: "inline-block", animation: refreshing ? "spin 0.9s linear infinite" : "none" }}>↻</span>
          </button>
        )}
        <button
          className="ipin"
          onClick={async (e) => {
            e.stopPropagation();
            await copyCardToClipboard(ins);
          }}
          title="Copy card text"
          aria-label="Copy card text"
          style={{ marginRight: 6 }}
        >
          ⧉
        </button>
        {onDismiss ? (
          <button
            className="ipin"
            onClick={(e) => { e.stopPropagation(); onDismiss(); }}
            title="Dismiss"
            aria-label="Dismiss"
          >
            ✕
          </button>
        ) : null}
      </div>
      <div className={`idetail${open ? " open" : ""}`}>
        {state === "pending" ? (
          <div className="dskel" aria-hidden="true">
            <div className="skel skel-line" />
            <div className="skel skel-line skel-line-short" />
          </div>
        ) : (
          <div className="dtext">{ins.detail}</div>
        )}
        {state === "ok" && ins.chartData?.length > 1 && (
          <div className="dchart">
            <Chart type={ins.chart} data={ins.chartData} centerLabel={ins.metric} valueLabel={ins.label} baselineLabel="Prior Period" />
          </div>
        )}
      </div>
    </div>
  );
}
