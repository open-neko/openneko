"use client";

/**
 * Neko Component Registrations
 *
 * Registers React renderers for each A2UI component type
 * in the Neko catalog. Import this module once at app startup
 * to populate the registry.
 */

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  WORKSPACE_MARKDOWN_COMPONENTS,
  linkifyWorkspacePaths,
} from "@/lib/linkify-workspace-paths";
import { registerComponent, renderChildren } from "./renderer";
import type { RenderContext } from "./renderer";
import type { A2UIComponent } from "./types";
import type {
  BriefingCardProps,
  BriefingProps,
  ConfirmationProps,
  MarkdownProps,
} from "./catalog";
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

// ─── Confirmation ───
registerComponent("Confirmation", (comp: A2UIComponent, ctx: RenderContext) => {
  const props = comp as unknown as ConfirmationProps & { id: string };
  return (
    <div
      key={props.id}
      className="work-confirm"
      style={{ animation: "fadeUp 0.4s ease both" }}
    >
      <div className="work-confirm-head">
        <span className="work-confirm-check" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round">
            <path d="M5 13l4 4L19 7" />
          </svg>
        </span>
        <span className="work-confirm-label">{props.label}</span>
      </div>
      {props.title ? <div className="work-confirm-title">{props.title}</div> : null}
      {props.children ? (
        <div className="work-confirm-body">{renderChildren(props.children, ctx)}</div>
      ) : null}
    </div>
  );
});

// ─── Markdown ───
registerComponent("Markdown", (comp: A2UIComponent) => {
  const props = comp as unknown as MarkdownProps & { id: string };
  return (
    <div key={props.id} className="work-markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={WORKSPACE_MARKDOWN_COMPONENTS}
      >
        {linkifyWorkspacePaths(props.text)}
      </ReactMarkdown>
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
