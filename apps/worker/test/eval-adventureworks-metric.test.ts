import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { createEvalPlan, loadEval } from "@neko/evals";
import { createAdventureWorksMetricDriver } from "../scripts/eval-adventureworks-metric";

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
    await driver.close?.();
  });
});
