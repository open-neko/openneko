"use client";

import { useState } from "react";
import Chart from "./Chart";
import type { ChartDataPoint } from "./Chart";

const MOOD_STYLES: Record<string, { bg: string; dot: string }> = {
  good: { bg: "#E8F5EC", dot: "#4CAF82" },
  watch: { bg: "#FFF4E5", dot: "#E9A23B" },
  act: { bg: "#FEECEC", dot: "#E05656" },
};

export interface BriefingCardData {
  id: string;
  metricId: string;
  source: string;
  mood: string;
  text: string;
  metric: string;
  label: string;
  detail: string;
  chart: string;
  chartData: ChartDataPoint[];
}

export default function BriefingCard({ ins, index, onDismiss }: {
  ins: BriefingCardData;
  index: number;
  onDismiss: () => void;
}) {
  const [open, setOpen] = useState(true);
  const m = MOOD_STYLES[ins.mood] ?? MOOD_STYLES.good;

  // Compute KPI delta for inline display
  const kpiDelta = (() => {
    if (ins.chartData?.length !== 1) return null;
    const item = ins.chartData[0];
    if (item.t == null || item.t === 0) return null;
    const delta = ((item.v - item.t) / item.t) * 100;
    const isUp = delta >= 0;
    const isBigDrop = delta <= -20;
    return { delta, isUp, isBigDrop };
  })();

  return (
    <div
      className={`icard${open ? " exp" : ""}`}
      style={{ animation: `fadeUp 0.5s ease ${index * 0.07}s both` }}
      onClick={() => setOpen(!open)}
    >
      <div className="itop">
        <div className="mdot" style={{ background: m.dot, boxShadow: `0 0 0 4px ${m.bg}` }} />
        <div className="icontent">
          <div className="itext">{ins.text}</div>
          <div className="imeta">
            <span className="imetric">{ins.metric}</span>
            <span className="ilabel">{ins.label}</span>
            {kpiDelta && (
              <span className="idelta" style={{
                fontSize: 13, fontWeight: 600, padding: "2px 8px", borderRadius: 6,
                background: kpiDelta.isBigDrop ? "#FEECEC" : kpiDelta.isUp ? "#E8F5EC" : "#FFF4E5",
                color: kpiDelta.isBigDrop ? "#E05656" : kpiDelta.isUp ? "#4CAF82" : "#E9A23B",
              }}>
                {kpiDelta.isUp ? "↑" : "↓"} {Math.abs(kpiDelta.delta).toFixed(1)}%
              </span>
            )}
          </div>
        </div>
      </div>
      <div className="iactions">
        <button
          className="ipin"
          onClick={(e) => { e.stopPropagation(); onDismiss(); }}
          title="Dismiss"
        >
          ✕
        </button>
      </div>
      <div className={`idetail${open ? " open" : ""}`}>
        <div className="dtext">{ins.detail}</div>
        {ins.chartData?.length > 1 && (
          <div className="dchart">
            <Chart type={ins.chart} data={ins.chartData} centerLabel={ins.metric} valueLabel={ins.label} baselineLabel="Prior Period" />
          </div>
        )}
      </div>
    </div>
  );
}
