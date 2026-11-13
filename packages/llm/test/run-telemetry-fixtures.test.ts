import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  createHarnessObserver,
  HarnessRunSummaryAccumulator,
  type HarnessRunSummary,
} from "@neko/telemetry";
import { createAgentEventTelemetry } from "../src/work/agent-event-telemetry";
import type { AgentEvent } from "../src/agent-backend";

// The Go run summary (apps/openneko/internal/webfront/turntelemetry.go) keeps
// only the summary fields that the web app reads. Run with
// UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/runtelemetry.json");

type Ending = "finish" | "fail" | "needs_input" | "before_agent";

const usage = (value: Record<string, unknown>): AgentEvent =>
  ({ type: "usage", source: "outer", provider: "google-gemini", model: "m", usage: value }) as AgentEvent;

const CASES: Array<{ name: string; ending: Ending; manual?: boolean; events: AgentEvent[] }> = [
  {
    name: "complete usage",
    ending: "finish",
    events: [
      { type: "message", role: "assistant", content: "Stock is fine." } as AgentEvent,
      { type: "tool_start", id: "t1", name: "neko_graphjin", input: { q: 1 } } as AgentEvent,
      { type: "tool_end", id: "t1", result: "rows" } as AgentEvent,
      usage({
        inputTokens: 1200, outputTokens: 300, cacheReadTokens: 100, totalTokens: 1500, estimatedCostUsd: 0.0021,
        currency: "USD", pricingCatalogVersion: "standard-2026-07-09", costStatus: "estimated", costSource: "catalog",
        coverage: "complete",
      }),
    ],
  },
  {
    name: "the last outer usage wins",
    ending: "finish",
    manual: true,
    events: [
      usage({ inputTokens: 10, totalTokens: 10, estimatedCostUsd: 0.5, costStatus: "estimated", coverage: "complete" }),
      { type: "usage", source: "inner", usage: { totalTokens: 999, coverage: "complete" } } as AgentEvent,
      usage({ inputTokens: 20, outputTokens: 5, totalTokens: 25, billedCostUsd: 0.25, costStatus: "actual", coverage: "complete" }),
    ],
  },
  {
    name: "partial usage keeps its reasons",
    ending: "finish",
    events: [
      usage({
        inputTokens: 40, totalTokens: 40, coverage: "partial",
        missingReasons: ["The turn ended early; usage covers the model calls made before its last tool call."],
      }),
    ],
  },
  {
    name: "values with coverage unavailable",
    ending: "finish",
    events: [usage({ totalTokens: 7, coverage: "unavailable" })],
  },
  {
    name: "unknown cost",
    ending: "finish",
    manual: true,
    events: [usage({ totalTokens: 7, estimatedCostUsd: 0.1, costStatus: "unknown", coverage: "complete" })],
  },
  {
    name: "no values",
    ending: "finish",
    events: [usage({ coverage: "unavailable", missingReasons: ["provider sent no usage"] })],
  },
  {
    name: "no usage event",
    ending: "finish",
    manual: true,
    events: [{ type: "message", role: "assistant", content: "Done." } as AgentEvent],
  },
  {
    name: "a failed turn keeps the usage it saw",
    ending: "fail",
    events: [usage({ inputTokens: 3, totalTokens: 3, estimatedCostUsd: 0.01, costStatus: "estimated", coverage: "complete" })],
  },
  {
    name: "a failed turn with no usage",
    ending: "fail",
    manual: true,
    events: [{ type: "tool_start", id: "t1", name: "neko_graphjin" } as AgentEvent],
  },
  {
    name: "a question asked",
    ending: "needs_input",
    events: [
      usage({ outputTokens: 9, totalTokens: 9, coverage: "complete" }),
      { type: "needs_input", question: "Which store?" } as unknown as AgentEvent,
    ],
  },
  {
    name: "a failure before the agent starts",
    ending: "before_agent",
    manual: true,
    events: [],
  },
];

async function nodeSummary(runId: string, c: (typeof CASES)[number]): Promise<HarnessRunSummary> {
  const summary = new HarnessRunSummaryAccumulator(runId);
  const observer = createHarnessObserver({ runId, sinks: [summary] });
  const operationId = `${c.manual ? "workflow" : "work"}:${runId}`;
  await observer.observe({
    kind: "run.start",
    operationId,
    attributes: { "openneko.product.path": c.manual ? "workflow" : "work" },
    ...(c.manual ? { measurements: { queueDurationMs: 0, attempts: 1, coverage: "unavailable" as const } } : {}),
  });
  await observer.observe({
    kind: "stage.end",
    operationId: `${operationId}:startup`,
    attributes: { "openneko.stage": "startup.config.backend" },
    measurements: { durationMs: 2, coverage: "unavailable" },
  });
  const telemetry = createAgentEventTelemetry({ observer, operationId });
  if (c.ending !== "before_agent") {
    await telemetry.startAgent({ backend: "ax", model: "m", inputBytes: 10 });
    for (const event of c.events) await telemetry.observeEvent(event);
    if (c.ending === "finish") {
      await telemetry.finishAgent({ status: "ok", outputBytes: 5 });
    } else if (c.ending === "needs_input") {
      await telemetry.closeOpen({ status: "ok", outcome: "needs_input", usageMissingReason: "turn paused for operator input" });
    } else {
      await telemetry.closeOpen({
        status: "error",
        outcome: "failed",
        errorType: "agent_backend_error",
        errorMessage: "boom",
        usageMissingReason: "model call did not complete with normalized usage",
      });
    }
  }
  const failed = c.ending === "fail" || c.ending === "before_agent";
  await observer.observe({
    kind: "run.end",
    operationId,
    status: failed ? "error" : "ok",
    attributes: { "openneko.outcome": failed ? "failed" : "completed" },
    measurements: { durationMs: 30, ...(c.manual ? { queueDurationMs: 0 } : {}), coverage: "unavailable" },
  });
  return summary.snapshot();
}

describe("Go run telemetry fixtures", () => {
  it("matches the stored fixture", async () => {
    const cases = [];
    for (const [index, c] of CASES.entries()) {
      const runId = `run-${index + 1}`;
      const full = await nodeSummary(runId, c);
      cases.push({
        name: c.name,
        runId,
        events: c.events,
        summary: { schemaVersion: full.schemaVersion, runId: full.runId, usage: full.usage },
      });
    }
    const fixture = `${JSON.stringify({ cases }, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
