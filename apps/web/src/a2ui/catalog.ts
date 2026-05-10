/**
 * A2UI Component Catalog
 *
 * Defines the custom components that agents can use to build
 * executive briefing UIs. Extends the A2UI basic catalog concepts
 * with domain-specific components for CXO dashboards.
 *
 * Components:
 *   - Briefing: Root container for a role's daily briefing
 *   - BriefingCard: Expandable card with mood, metric, detail, and chart
 *
 * All property values support A2UI data binding via { path: "/..." }
 */

export const CATALOG_ID = "urn:app:catalog:briefing:v1";

// Component type names agents can reference
export const ComponentTypes = {
  Briefing: "Briefing",
  BriefingCard: "BriefingCard",
  MetricCard: "MetricCard",
  ChatResponse: "ChatResponse",
  Markdown: "Markdown",
} as const;

// Mood enum shared across components
export type Mood = "good" | "watch" | "act";

// Chart type enum — 5 types cover 92% of exec needs
export type ChartType = "kpi" | "line" | "bar" | "area" | "donut";

// Chart data point
export interface ChartDataPoint {
  d: string;  // label (e.g. day name)
  v: number;  // primary value
  t?: number; // secondary/comparison value
}

// --- Component Property Schemas ---

export interface BriefingProps {
  component: "Briefing";
  greeting: string;
  subtitle: string;
  role: string;
  isExample?: boolean; // true when cards are the legacy ROLE_DATA mock
  children: string[]; // IDs of BriefingCard components
}

export interface MarkdownProps {
  component: "Markdown";
  text: string;
}

export interface BriefingCardProps {
  component: "BriefingCard";
  metricId: string;   // DB UUID — needed for pin/dashboard API calls
  source: string;     // "bootstrap" | "chat"
  state?: "ok" | "pending" | "failed"; // discriminator for the card chrome
  error?: string;     // populated when state="failed"
  mood: Mood;
  text: string;       // headline
  metric: string;     // e.g. "$4.7M"
  label: string;      // e.g. "Revenue MTD"
  detail: string;     // expanded explanation
  chartType: ChartType;
  chartData: ChartDataPoint[];
}

// Union of all component props
export type ComponentProps =
  | BriefingProps
  | BriefingCardProps
  | MarkdownProps;
