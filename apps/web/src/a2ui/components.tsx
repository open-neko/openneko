"use client";

/**
 * Neko Component Registrations
 *
 * Registers React renderers for each A2UI component type
 * in the Neko catalog. Import this module once at app startup
 * to populate the registry.
 */

import { registerComponent, renderChildren } from "./renderer";
import type { RenderContext } from "./renderer";
import type { A2UIComponent } from "./types";
import type { BriefingCardProps, BriefingProps } from "./catalog";
import BriefingCard from "@/components/BriefingCard";

// ─── Briefing ───
registerComponent("Briefing", (comp: A2UIComponent, ctx: RenderContext) => {
  const props = comp as unknown as BriefingProps & { id: string };
  return (
    <div key={props.id}>
      <div className="greet" style={{ animation: "fadeUp 0.5s ease both" }}>
        {props.greeting}
      </div>
      <div className="greet-sub" style={{ animation: "fadeUp 0.5s ease 0.05s both" }}>
        {props.subtitle}
      </div>
      {props.children && renderChildren(props.children, ctx)}
    </div>
  );
});

// ─── BriefingCard ───
registerComponent("BriefingCard", (comp: A2UIComponent, ctx: RenderContext) => {
  const props = comp as unknown as BriefingCardProps & { id: string };
  const extras = ctx.extras as {
    onDismiss?: (id: string) => void;
    indexMap?: Map<string, number>;
  } | undefined;

  const index = extras?.indexMap?.get(props.id) ?? 0;

  const insight = {
    id: props.id,
    metricId: props.metricId,
    source: props.source,
    mood: props.mood,
    text: props.text,
    metric: props.metric,
    label: props.label,
    detail: props.detail,
    chart: props.chartType,
    chartData: props.chartData,
  };

  return (
    <BriefingCard
      key={props.id}
      ins={insight}
      index={index}
      onDismiss={extras?.onDismiss ? () => extras.onDismiss?.(props.id) : undefined}
    />
  );
});
