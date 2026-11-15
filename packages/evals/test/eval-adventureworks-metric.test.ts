import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { dbReachable } from "@neko/db/test-helpers";
import { EvalTaskError, createEvalPlan, loadEval, type EvalObserver, type EvalVariant } from "@neko/evals";
import {
  createAdventureWorksMetricDriver,
  metricUsageMeasurements,
  type MetricCardInput,
  type MetricClassifier,
} from "../scripts/eval-adventureworks-metric";
import { ScriptedModelServer } from "../scripts/eval-scripted-model";

const CONFIG = fileURLToPath(new URL("../../../evals/configs/adventureworks-smoke.yaml", import.meta.url));

describe("AdventureWorks eval adapter", () => {
  it("scores hidden numeric and time-window truth deterministically", async () => {
    const configPath = fileURLToPath(
      new URL("../../../evals/configs/adventureworks-smoke.yaml", import.meta.url),
    );
    const loaded = await loadEval(configPath);
    const plan = createEvalPlan(loaded);
    const driver = createAdventureWorksMetricDriver({ loaded, plan });
    const evalCase = loaded.cases.find((candidate) => candidate.id === "q01")!;
    const variant = loaded.config.variants[0]!;
    const execution = {
      output: {
        reasoning: "",
        headlineMetric: "100",
        headlineLabel: "Orders",
        insightText: "Stable",
        detailText: "",
        mood: "good",
        chartType: "kpi",
        chartData: [{ d: "Orders", v: 100, t: 90 }],
        timeWindow: {
          grain: "year",
          start: "2013-07-01",
          end: "2014-06-30",
          label: "Trailing year",
        },
      },
      measurements: { wallDurationMs: 1_000 },
    };
    const oracle = {
      expectedValue: 100,
      baselineValue: 90,
      startDate: "2013-07-01",
      anchorDate: "2014-06-30",
    };

    const passing = await driver.score({ case: evalCase, variant, oracle, execution, phase: "initial", repetition: 1 });
    expect(passing.verdict).toBe("pass");
    expect(passing.vector.groundTruth).toBe(1);
    expect(passing.coverage.method).toBe(false);
    expect(passing.coverage.safety).toBe(false);

    const failing = await driver.score({
      case: evalCase,
      variant,
      oracle,
      phase: "initial",
      repetition: 1,
      execution: {
        ...execution,
        output: {
          ...(execution.output as Record<string, unknown>),
          headlineMetric: "75",
        },
      },
    });
    expect(failing.verdict).toBe("fail");

    const badBaseline = await driver.score({
      case: evalCase,
      variant,
      oracle,
      phase: "initial",
      repetition: 1,
      execution: {
        ...execution,
        output: {
          ...(execution.output as Record<string, unknown>),
          chartData: [{ d: "Orders", v: 100, t: 1 }],
        },
      },
    });
    expect(badBaseline.verdict).toBe("fail");

    const quarterlyChart = await driver.score({
      case: evalCase,
      variant,
      oracle,
      phase: "initial",
      repetition: 1,
      execution: {
        ...execution,
        output: {
          ...(execution.output as Record<string, unknown>),
          headlineMetric: "100 orders",
          chartType: "line",
          chartData: [
            { d: "Q3", v: 20, t: 18 },
            { d: "Q4", v: 25, t: 22 },
            { d: "Q1", v: 27, t: 24 },
            { d: "Q2", v: 28, t: 26 },
          ],
          baselineValue: 90,
        },
      },
    });
    expect(quarterlyChart.verdict).toBe("pass");

    const scaledUnits = await driver.score({
      case: evalCase,
      variant,
      oracle: { ...oracle, expectedValue: 2_070_000 },
      phase: "initial",
      repetition: 1,
      execution: {
        ...execution,
        output: {
          ...(execution.output as Record<string, unknown>),
          headlineMetric: "2.07M units",
          chartData: [{ d: "Units", v: 5, t: 90 }],
        },
      },
    });
    expect(scaledUnits.checks.find((check) => check.assertionId === "headline-value")?.passed).toBe(true);
    await driver.close?.();
  });
});

describe("metric usage from the classifier and spend_ledger", () => {
  it("is complete only when every ledger row has tokens and a priced cost", async () => {
    const loaded = await loadEval(CONFIG);
    const variant = loaded.config.variants[0]!;
    const classifier = { coverage: "complete" as const, inputTokens: 6, outputTokens: 4, totalTokens: 10, billedCostUsd: 0.001 };
    const complete = metricUsageMeasurements({
      variant,
      catalog: loaded.pricing,
      classifier,
      ledger: [{ tokens: 90, costMicros: 2_000, priced: "estimated" }],
    });
    expect(complete).toMatchObject({ usageCoverage: "complete", totalTokens: 100, costCoverage: "complete" });
    expect(complete.estimatedCostUsd).toBeCloseTo(0.003, 9);
    expect(complete.usageMissingReasons).toBeUndefined();

    const fallback = metricUsageMeasurements({
      variant,
      catalog: loaded.pricing,
      classifier,
      ledger: [{ tokens: null, costMicros: 5_000_000, priced: "fallback" }],
    });
    expect(fallback).toMatchObject({ usageCoverage: "partial", totalTokens: 10, costCoverage: "partial" });
    expect(fallback.estimatedCostUsd).toBeCloseTo(0.001, 9);
    expect(fallback.usageMissingReasons).toEqual([
      "metric_refresh: a spend_ledger row has no tokens",
      "metric_refresh: a spend_ledger row has the fallback price",
    ]);

    const none = metricUsageMeasurements({
      variant,
      catalog: loaded.pricing,
      classifier: { coverage: "unavailable", missingReasons: ["no usage"] },
      ledger: [],
    });
    expect(none).toMatchObject({ usageCoverage: "unavailable", costCoverage: "unavailable" });
    expect(none.totalTokens).toBeUndefined();
  });
});

const reachable = await dbReachable();

const SCRIPTED_RESULT = {
  reasoning: "Counted the orders of the trailing year.",
  headlineMetric: "100",
  headlineLabel: "Orders",
  insightText: "Stable",
  detailText: "",
  mood: "good",
  chartType: "kpi",
  chartData: [{ d: "Orders", v: 100, t: 90 }],
  baselineValue: 90,
  timeWindow: { grain: "year", start: "2013-07-01", end: "2014-06-30", label: "Trailing year" },
};

describe.skipIf(!reachable)("metric eval driver on the eval install", () => {
  it(
    "runs the Go metric_refresh job and reads its snapshot and spend_ledger rows",
    async () => {
      const loaded = await loadEval(CONFIG);
      const plan = createEvalPlan(loaded);
      const model = await ScriptedModelServer.start();
      const cards: MetricCardInput[] = [];
      let reply: unknown = SCRIPTED_RESULT;
      const classify: MetricClassifier = async (_question, _role, _orgId, options) => {
        options?.onDiagnostics?.({ durationMs: 5, usage: { coverage: "complete", inputTokens: 6, outputTokens: 4, totalTokens: 10 } });
        return { slug: "trailing-year-orders", title: "Trailing year orders", why: "Order volume", chartHint: "kpi" };
      };
      const driver = createAdventureWorksMetricDriver(
        { loaded, plan },
        {
          host: { lockWaitMs: 600_000 },
          model,
          classify,
          program: (card) => {
            cards.push(card);
            return () => JSON.stringify(reply);
          },
        },
      );
      const observed: Array<{ kind: string; status?: string }> = [];
      const observer: EvalObserver = {
        async observe(event) {
          observed.push({ kind: event.kind, status: (event as { status?: string }).status });
        },
      };
      const scripted: EvalVariant = {
        ...loaded.config.variants[0]!,
        id: "scripted-metric",
        backend: "scripted-metric",
        outer_model: { provider: "openai-compatible", model: "scripted-metric" },
      };
      const base = plan.slots.find((slot) => slot.caseId === "q01")!;
      const slot = { ...base, variant: scripted, variantId: scripted.id };
      const run = (runId: string) =>
        driver.execute({
          runId,
          slot,
          oracle: {},
          observer,
          signal: new AbortController().signal,
        } as unknown as Parameters<typeof driver.execute>[0]);
      try {
        const execution = await run("vitest-metric");
        expect(cards[0]).toMatchObject({ slug: "trailing-year-orders", chartHint: "kpi", role: expect.any(String) });
        expect(execution.output).toMatchObject({ headlineMetric: "100", chartData: [{ d: "Orders", v: 100, t: 90 }] });
        const m = execution.measurements!;
        expect(Number(m.totalTokens)).toBeGreaterThan(10);
        expect(m.usageCoverage).toBe("complete");
        expect(m.costCoverage).toBe("unavailable");
        expect(m.usageMissingReasons).toContain("metric_refresh: a spend_ledger row has the fallback price");
        expect(Number(m.metricJobDurationMs)).toBeGreaterThan(0);
        expect(observed).toContainEqual({ kind: "output.contract", status: "ok" });
        const score = await driver.score({
          case: slot.case,
          variant: scripted,
          oracle: { expectedValue: 100, baselineValue: 90, startDate: "2013-07-01", anchorDate: "2014-06-30" },
          execution,
          phase: "initial",
          repetition: 1,
        });
        expect(score.checks.find((check) => check.assertionId === "headline-value")?.passed).toBe(true);

        reply = { ...SCRIPTED_RESULT, headlineMetric: "N/A" };
        const failure = await run("vitest-metric-fail").catch((cause: unknown) => cause);
        expect(failure).toBeInstanceOf(EvalTaskError);
        expect((failure as EvalTaskError).message).toMatch(/error sentinel/);
      } finally {
        await driver.close?.();
        await model.close();
      }
    },
    900_000,
  );
});
