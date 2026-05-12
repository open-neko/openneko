"use client";

import {
  AreaChart, Area, BarChart, Bar, LineChart, Line,
  PieChart, Pie, Cell,
  XAxis, YAxis, Tooltip, ResponsiveContainer, CartesianGrid,
} from "recharts";
import type { ChartDataPoint } from "@/a2ui/catalog";
export type { ChartDataPoint } from "@/a2ui/catalog";
import { formatCompact } from "@/lib/format-number";

const axisProps = { tick: { fontSize: 11, fill: "#b0aa9f" }, axisLine: false, tickLine: false } as const;
const tooltipStyle = { contentStyle: { borderRadius: 10, border: "none", boxShadow: "0 4px 16px rgba(0,0,0,0.08)", fontSize: 13 }, itemStyle: { color: "#7A756A" } };
const BASELINE_COLOR = "#5C5751";

export default function Chart({ type, accent = "#6B5CE7", h = 130, data, centerLabel, valueLabel, baselineLabel }: {
  type: string;
  accent?: string;
  h?: number;
  data: ChartDataPoint[];
  centerLabel?: string;
  valueLabel?: string;
  baselineLabel?: string;
}) {
  if (!data || data.length === 0) return null;

  // Defensive: agents sometimes mis-classify chartType. Coerce to a sensible
  // shape based on the actual data.
  // - KPI demands exactly 1 item; multi-item data is a category breakdown.
  // - Non-kpi types with a single point are degenerate; treat as kpi.
  let resolvedType = type;
  if (type === "kpi" && data.length > 1) {
    resolvedType = data.length <= 6 ? "donut" : "bar";
  } else if (type !== "kpi" && data.length === 1) {
    resolvedType = "kpi";
  }

  if (resolvedType === "kpi") {
    // kpi data isn't a visualization — it's a headline number with a
    // baseline. Use <KpiHeadline> for that. Chart returns null here so
    // any caller that accidentally passes kpi to Chart silently no-ops
    // instead of rendering empty axes; lint/tests should catch the
    // misuse but this is a defensive fallback.
    return null;
  }

  if (resolvedType === "donut") {
    // Violet-to-blue analogous ramp — calm, monochromatic, no mood colors.
    const COLORS = [accent, "#8B7CF0", "#A89AEE", "#818CF8", "#93A4F8", "#A5BEF5"];
    const total = data.reduce((s, d) => s + d.v, 0);
    const donutFormatter = (value: number | string | undefined, name: string | number | undefined) => [
      `${((Number(value ?? 0) / total) * 100).toFixed(0)}%`,
      String(name ?? ""),
    ] as [string, string];
    return (
      <ResponsiveContainer width="100%" height={h}>
        <PieChart>
          <Pie
            data={data}
            dataKey="v"
            nameKey="d"
            cx="50%"
            cy="50%"
            innerRadius={h * 0.25}
            outerRadius={h * 0.4}
            paddingAngle={2}
            strokeWidth={0}
          >
            {data.map((_, i) => (
              <Cell key={i} fill={COLORS[i % COLORS.length]} />
            ))}
          </Pie>
          <Tooltip {...tooltipStyle} formatter={donutFormatter as never} />
          <text x="50%" y="50%" textAnchor="middle" dominantBaseline="middle"
            style={{ fontSize: 13, fill: "#7A756A" }}>
            {centerLabel ?? formatCompact(total)}
          </text>
        </PieChart>
      </ResponsiveContainer>
    );
  }

  const seriesFormatter = (value: number | string | undefined, name: string | number | undefined) => [
    formatCompact(Number(value ?? 0)),
    name === "v"
      ? (valueLabel ?? "Value")
      : name === "t"
        ? (baselineLabel ?? "Prior")
        : String(name),
  ] as [string, string];
  const yTickFormatter = (v: number) => formatCompact(v);

  if (resolvedType === "area") {
    const gradientId = `g${accent.replace("#", "")}`;
    return (
      <ResponsiveContainer width="100%" height={h}>
        <AreaChart data={data}>
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={accent} stopOpacity={0.25} />
              <stop offset="100%" stopColor={accent} stopOpacity={0.01} />
            </linearGradient>
          </defs>
          <CartesianGrid strokeDasharray="3 3" stroke="#f0eee8" />
          <XAxis dataKey="d" {...axisProps} />
          <YAxis {...axisProps} width={32} tickFormatter={yTickFormatter} />
          <Tooltip {...tooltipStyle} formatter={seriesFormatter as never} />
          <Area type="monotone" dataKey="v" name={valueLabel ?? "Value"} stroke={accent} strokeWidth={2} fill={`url(#${gradientId})`} dot={{ r: 3, fill: "#fff", stroke: accent, strokeWidth: 1.5 }} />
        </AreaChart>
      </ResponsiveContainer>
    );
  }

  if (resolvedType === "bar") {
    return (
      <ResponsiveContainer width="100%" height={h}>
        <BarChart data={data} barSize={16} barGap={3}>
          <CartesianGrid strokeDasharray="3 3" stroke="#f0eee8" />
          <XAxis dataKey="d" {...axisProps} />
          <YAxis {...axisProps} width={32} tickFormatter={yTickFormatter} />
          <Tooltip {...tooltipStyle} formatter={seriesFormatter as never} />
          <Bar dataKey="v" name={valueLabel ?? "Value"} fill={accent} radius={[5, 5, 0, 0]} />
          <Bar dataKey="t" name={baselineLabel ?? "Prior"} fill={accent + "55"} radius={[5, 5, 0, 0]} />
        </BarChart>
      </ResponsiveContainer>
    );
  }

  return (
    <ResponsiveContainer width="100%" height={h}>
      <LineChart data={data}>
        <CartesianGrid strokeDasharray="3 3" stroke="#f0eee8" />
        <XAxis dataKey="d" {...axisProps} />
        <YAxis {...axisProps} width={32} tickFormatter={yTickFormatter} />
        <Tooltip {...tooltipStyle} formatter={seriesFormatter as never} />
        <Line type="monotone" dataKey="v" name={valueLabel ?? "Value"} stroke={accent} strokeWidth={2.5} dot={{ r: 3, fill: "#fff", stroke: accent, strokeWidth: 2 }} />
        <Line type="monotone" dataKey="t" name={baselineLabel ?? "Prior"} stroke={BASELINE_COLOR} strokeOpacity={0.55} strokeWidth={1.75} strokeDasharray="5 4" dot={false} />
      </LineChart>
    </ResponsiveContainer>
  );
}
