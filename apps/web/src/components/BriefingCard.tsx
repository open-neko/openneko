"use client";

import { useState } from "react";
import { Copy, RotateCw, Search, X } from "lucide-react";
import Chart from "./Chart";
import type { ChartDataPoint } from "./Chart";
import KpiHeadline from "./KpiHeadline";
import { useDensity } from "./DensityProvider";

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

const MOOD_LABELS: Record<string, string> = {
  good: "On track",
  watch: "Watch",
  act: "Act now",
  bad: "Act now",
};

const MOOD_CHART_ACCENT: Record<string, string> = {
  good: "#4CAF82",
  watch: "#E9A23B",
  act: "#E05656",
  bad: "#E05656",
};

// Compact tiles carry a mini sparkline (the mockup look); the full chart only
// appears on expand. Plots the chart series' `v` values, mood-coloured.
function MiniSpark({ data, color }: { data: ChartDataPoint[]; color: string }) {
  const pts = data.map((d) => d.v).filter((n): n is number => typeof n === "number");
  if (pts.length < 2) return null;
  const w = 120;
  const h = 30;
  const min = Math.min(...pts);
  const max = Math.max(...pts);
  const span = max - min || 1;
  const step = w / (pts.length - 1);
  const xy = pts.map((p, i) => [i * step, h - ((p - min) / span) * (h - 6) - 3] as const);
  const line = xy.map((c, i) => `${i ? "L" : "M"}${c[0].toFixed(1)} ${c[1].toFixed(1)}`).join(" ");
  const [ex, ey] = xy[xy.length - 1];
  const gid = `ms${Math.round(min * 31 + max * 7 + pts.length)}`;
  return (
    <svg className="minispark" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor={color} stopOpacity="0.18" />
          <stop offset="1" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={`${line} L ${w} ${h} L 0 ${h} Z`} fill={`url(#${gid})`} />
      <path d={line} fill="none" stroke={color} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
      <circle cx={ex.toFixed(1)} cy={ey.toFixed(1)} r="2.4" fill={color} />
    </svg>
  );
}

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

export default function BriefingCard({ ins, index, onDismiss, onRetry, onDeepDive }: {
  ins: BriefingCardData;
  index: number;
  onDismiss?: () => void;
  onRetry?: (metricId: string) => void;
  onDeepDive?: (metricId: string) => void;
}) {
  // Comfortable keeps today's always-expanded card; Compact starts the tile
  // collapsed (metric only) and expands the detail + chart on click.
  const { density } = useDensity();
  const [open, setOpen] = useState(density === "comfortable");
  const [retrying, setRetrying] = useState(false);
  const state: BriefingCardState = ins.state ?? "ok";
  const moodKey = MOOD_LABELS[ins.mood] ? ins.mood : "good";
  const moodLabel = MOOD_LABELS[moodKey];
  const numeral = String(index + 1).padStart(2, "0");

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
      data-mood={moodKey}
      style={{ animation: `fadeUp 0.5s ease ${index * 0.07}s both` }}
      onClick={() => setOpen(!open)}
    >
      <div className="itop">
        <div className="inum">{numeral}</div>
        <div className="icontent">
          <div className="ieyebrow">
            <span className="ieyebrow-dot" aria-hidden="true" />
            <span>{moodLabel}</span>
          </div>
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
              mood={moodKey}
            />
          ) : null}
          {density === "compact" && state === "ok" && ins.chartData?.length > 1 && (
            <MiniSpark
              data={ins.chartData}
              color={MOOD_CHART_ACCENT[moodKey] ?? "var(--color-accent)"}
            />
          )}
        </div>
      </div>
      <div className="iactions">
        {onDeepDive && ins.metricId && state === "ok" && (
          <button
            className="ipin ipin-deep"
            onClick={(e) => {
              e.stopPropagation();
              onDeepDive(ins.metricId);
            }}
            title="Deep dive in Work"
            aria-label="Deep dive in Work"
          >
            <Search size={13} strokeWidth={2.25} />
          </button>
        )}
        {onRetry && ins.metricId && (
          <button
            className="ipin"
            onClick={handleRetry}
            disabled={refreshing}
            title={refreshing ? "Re-running…" : "Re-run this metric"}
            aria-label="Re-run this metric"
            aria-busy={refreshing}
          >
            <RotateCw
              size={13}
              strokeWidth={2}
              style={{ animation: refreshing ? "spin 0.9s linear infinite" : "none" }}
            />
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
        >
          <Copy size={13} strokeWidth={2} />
        </button>
        {onDismiss ? (
          <button
            className="ipin"
            onClick={(e) => { e.stopPropagation(); onDismiss(); }}
            title="Dismiss"
            aria-label="Dismiss"
          >
            <X size={14} strokeWidth={2} />
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
            <Chart
              type={ins.chart}
              data={ins.chartData}
              accent={MOOD_CHART_ACCENT[moodKey] ?? undefined}
              centerLabel={ins.metric}
              valueLabel={ins.label}
              baselineLabel="Prior Period"
            />
          </div>
        )}
      </div>
    </div>
  );
}
