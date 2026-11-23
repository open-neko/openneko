import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { dbReachable } from "@neko/db/test-helpers";
import { createEvalPlan, loadEval, type EvalObserver } from "@neko/evals";
import { createOpenNekoWorkDriver, searchOutputFrom, type MemoryHit } from "../scripts/eval-openneko-work";

const CONFIG = fileURLToPath(new URL("../../../evals/configs/openneko-work-memory-smoke.yaml", import.meta.url));

describe("OpenNeko work-memory eval adapter", () => {
  it("resolves phase-keyed bundles and scores each phase against its own oracle", async () => {
    const loaded = await loadEval(CONFIG);
    const plan = createEvalPlan(loaded);
    // Three cases expand to 3 + 2 + 1 phase slots.
    expect(plan.calls).toBe(6);
    const driver = createOpenNekoWorkDriver({ loaded, plan });
    const retrieval = loaded.cases.find(
      (candidate) => candidate.id === "m01-memory-retrieval",
    )!;
    const variant = loaded.config.variants[0]!;

    const oracle = await driver.resolveOracle(retrieval, { loaded, plan });
    expect(Object.keys(oracle as Record<string, unknown>).sort()).toEqual([
      "isolate",
      "search",
      "seed",
    ]);

    const searchOutput = {
      plantedFound: true,
      plantedRankedFirst: true,
      decoyOutranked: true,
      missWeakerThanHit: true,
      archivesEmpty: true,
      usageTouched: true,
    };
    const passing = await driver.score({
      case: retrieval,
      variant,
      oracle,
      execution: { output: searchOutput, measurements: {} },
      phase: "search",
      repetition: 1,
    });
    expect(passing.verdict).toBe("pass");
    // Phase-bound assertions only: the seed/isolate checks must not run
    // against a search-phase output.
    expect(passing.checks).toHaveLength(6);
    expect(passing.coverage.safety).toBe(false);

    const failing = await driver.score({
      case: retrieval,
      variant,
      oracle,
      execution: {
        output: { ...searchOutput, decoyOutranked: false },
        measurements: {},
      },
      phase: "search",
      repetition: 1,
    });
    expect(failing.verdict).toBe("fail");

    const isolate = await driver.score({
      case: retrieval,
      variant,
      oracle,
      execution: {
        output: {
          crossOrgHidden: true,
          archiveAccepted: true,
          archivedExcluded: true,
        },
        measurements: {},
      },
      phase: "isolate",
      repetition: 1,
    });
    expect(isolate.verdict).toBe("pass");
    expect(isolate.coverage.safety).toBe(true);
  });
});

describe("work-memory search output", () => {
  const seeded = { planted: "p", definition: "d", decoy: "x" };
  const hit = (id: string, text: string, score: number, source = "saved_memory"): MemoryHit => ({
    source,
    score,
    memory: { id, text },
  });
  const planted = hit("p", "Revenue alert SLA: flag any sales territory when its weekly order value drops below 18500 USD.", 0.4);

  it("passes a ranked hit with an empty miss and a touched planted row", () => {
    expect(
      Object.values(searchOutputFrom({ seeded, hit: [planted], miss: [], plantedUseCount: 1 })).every(Boolean),
    ).toBe(true);
  });

  it("fails each check on the output that breaks it", () => {
    const decoyFirst = searchOutputFrom({ seeded, hit: [hit("x", "lunch", 0.9), planted], miss: [], plantedUseCount: 1 });
    expect(decoyFirst.plantedRankedFirst).toBe(false);
    expect(decoyFirst.decoyOutranked).toBe(false);
    const strongMiss = searchOutputFrom({ seeded, hit: [planted], miss: [hit("y", "other", 0.5)], plantedUseCount: 1 });
    expect(strongMiss.missWeakerThanHit).toBe(false);
    const archive = searchOutputFrom({ seeded, hit: [planted], miss: [hit("t", "thread", 0.1, "archive")], plantedUseCount: 1 });
    expect(archive.archivesEmpty).toBe(false);
    expect(searchOutputFrom({ seeded, hit: [planted], miss: [], plantedUseCount: 0 }).usageTouched).toBe(false);
    expect(searchOutputFrom({ seeded, hit: [], miss: [], plantedUseCount: 1 }).plantedFound).toBe(false);
  });
});

const reachable = await dbReachable();

describe.skipIf(!reachable)("work-memory eval driver on the eval install", () => {
  it(
    "passes every work slot through scripted turns on web and serve",
    async () => {
      const loaded = await loadEval(CONFIG);
      const plan = createEvalPlan(loaded);
      const driver = createOpenNekoWorkDriver({ loaded, plan }, { host: { lockWaitMs: 600_000 } });
      const observed: string[] = [];
      const observer: EvalObserver = {
        async observe(event) {
          observed.push(event.kind);
        },
      };
      try {
        await driver.preflight({ loaded, plan });
        for (const slot of plan.slots) {
          const oracle = await driver.resolveOracle(slot.case, { loaded, plan });
          const execution = await driver.execute({
            runId: "vitest-work",
            slot,
            oracle,
            observer,
            signal: new AbortController().signal,
          } as Parameters<typeof driver.execute>[0]);
          const score = await driver.score({
            case: slot.case,
            variant: slot.variant,
            oracle,
            execution,
            phase: slot.phase,
            repetition: slot.repetition,
          });
          expect(score.checks.filter((check) => !check.passed), `${slot.caseId}/${slot.phase}`).toEqual([]);
          expect(execution.measurements?.modelCalls).toBe(0);
          expect(execution.measurements?.scriptedTurns).toBeGreaterThan(0);
        }
      } finally {
        await driver.close?.();
      }
      expect(observed.filter((kind) => kind === "run.end")).toHaveLength(plan.slots.length);
      expect(observed).toContain("memory.start");
    },
    900_000,
  );
});
