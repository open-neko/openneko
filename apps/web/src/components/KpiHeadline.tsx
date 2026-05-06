"use client";

import type { ChartDataPoint } from "./Chart";

/**
 * Renders a single KPI metric: headline number + label + period-over-period
 * delta arrow.
 *
 * Why this exists as its own component: chartType="kpi" doesn't map to a
 * traditional chart visualization — it's a single number with a baseline.
 * Rather than overload `<Chart type="kpi" />` (and have it return null,
 * silently dropping the metric for any caller that doesn't know to render
 * the headline themselves), the kpi presentation lives here. Both
 * BriefingCard and ChatBubble use it, so kpi data renders consistently
 * everywhere instead of being re-implemented per surface.
 */
export interface KpiHeadlineProps {
  /** Pre-formatted display string, e.g. "$4.7M", "100%", "31,465". */
  metric: string;
  /** Short label under the metric, e.g. "Revenue MTD". */
  label?: string;
  /** Single data point: v = current, t = baseline (prior period). */
  data?: ChartDataPoint[];
  /** Visual size — chat bubbles want it bigger; card meta-rows want it small. */
  size?: "card" | "chat";
}

export default function KpiHeadline({ metric, label, data, size = "card" }: KpiHeadlineProps) {
  const delta = computeDelta(data);
  return (
    <div className={`kpi kpi-${size}`}>
      <span className="kpi-metric">{metric}</span>
      {label && <span className="kpi-label">{label}</span>}
      {delta && (
        <span
          className="kpi-delta"
          style={{
            background: delta.isBigDrop ? "#FEECEC" : delta.isUp ? "#E8F5EC" : "#FFF4E5",
            color: delta.isBigDrop ? "#E05656" : delta.isUp ? "#4CAF82" : "#E9A23B",
          }}
        >
          {delta.isUp ? "↑" : "↓"} {Math.abs(delta.pct).toFixed(1)}%
        </span>
      )}
    </div>
  );
}

function computeDelta(data?: ChartDataPoint[]) {
  if (!data || data.length !== 1) return null;
  const item = data[0];
  if (item.t == null || item.t === 0) return null;
  const pct = ((item.v - item.t) / item.t) * 100;
  if (Math.abs(pct) < 0.05) return null;
  return { pct, isUp: pct >= 0, isBigDrop: pct <= -20 };
}
