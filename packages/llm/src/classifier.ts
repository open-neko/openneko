/**
 * Chat-question classifier — natural language → dashboard card metadata.
 *
 * Used by web's POST /api/briefing on the chat path: classify the question
 * to derive a card title, slug, rationale, and chart hint *before*
 * enqueueing the metric_refresh job. No tools, no DB lookups — pure
 * intent classification, ~2-5s LLM call.
 */

import { ax } from "@ax-llm/ax";
import { buildLlm } from "./llm";

const DESCRIPTION = `You classify a user's natural-language question about their business data into a dashboard card specification. Output a kebab-case slug for internal use, a short human-readable card title (what a CXO would see as the card heading), a one-sentence rationale explaining why this matters, and the best chart type for the answer. No database lookups — just classify the intent from the question.`;

export type ClassifiedCard = {
  slug: string;
  title: string;
  why: string;
  chartHint: string;
};

export async function classifyQuestion(
  question: string,
  role: string,
  orgId?: string,
): Promise<ClassifiedCard> {
  const llm = await buildLlm(orgId);
  const classifier = ax(
    `question:string "the user's natural language question", role:string "CXO role" -> slug:string "kebab-case identifier for internal use", title:string "short human-readable card title for the dashboard", why:string "one-sentence rationale explaining why this matters", chartHint:class "kpi, line, bar, donut, area"`,
    { description: DESCRIPTION },
  );
  const result = await classifier.forward(llm, { question, role });
  return {
    slug: String(result.slug ?? ""),
    title: String(result.title ?? ""),
    why: String(result.why ?? ""),
    chartHint: String(result.chartHint ?? "bar"),
  };
}
