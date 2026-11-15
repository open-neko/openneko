import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { dbReachable } from "@neko/db/test-helpers";
import { createEvalPlan, loadEval, type EvalObserver } from "@neko/evals";
import {
  CONDITION_WATCHERS,
  EvalGraphqlSource,
  POLICY_PROBES,
  conditionMatrixFrom,
  createOpenNekoLifecycleDriver,
  parseSweep,
  policyMatrixFrom,
} from "../scripts/eval-openneko-lifecycle";

const CONFIG = fileURLToPath(new URL("../../../evals/configs/openneko-lifecycle-smoke.yaml", import.meta.url));

describe("lifecycle eval GraphQL source", () => {
  it("answers data, an error and an unknown field the way GraphJin does", async () => {
    const source = await EvalGraphqlSource.start();
    try {
      source.set("eval_metric", { value: 12 });
      source.set("broken", new Error("eval transport unavailable"));
      const ask = async (query: string) =>
        (await fetch(source.url, { method: "POST", body: JSON.stringify({ query }) })).json();
      expect(await ask("{ eval_metric { value } }")).toEqual({ data: { eval_metric: { value: 12 } } });
      expect(await ask("{ broken { value } }")).toEqual({ errors: [{ message: "eval transport unavailable" }] });
      expect(await ask("{ missing }")).toEqual({ errors: [{ message: "unknown field missing" }] });
      expect(source.queries).toHaveLength(3);
      source.clear();
      expect(source.queries).toHaveLength(0);
      expect(await ask("{ eval_metric { value } }")).toEqual({ errors: [{ message: "unknown field eval_metric" }] });
    } finally {
      await source.close();
    }
  });
});

describe("lifecycle eval matrices", () => {
  const fired = (...names: string[]) => ({
    checked: CONDITION_WATCHERS.length,
    fired: names.map((name) => ({ watcherId: name, name, value: null })),
  });
  const watchers = [
    { name: "m-path-array", lastValue: 7, lastError: null },
    { name: "m-path-missing", lastValue: null, lastError: null },
    { name: "m-non-numeric", lastValue: "not-a-number", lastError: null },
  ];
  const good = {
    sweeps: [
      fired("m-path-array", "m-gt", "m-gte", "m-lt", "m-lte", "m-eq", "m-ne"),
      fired(),
      fired("m-changed"),
    ] as [ReturnType<typeof fired>, ReturnType<typeof fired>, ReturnType<typeof fired>],
    watchers,
    changedAfterFirst: 5,
  };

  it("passes the condition matrix only on the expected fires", () => {
    expect(Object.values(conditionMatrixFrom(good)).every(Boolean)).toBe(true);
    const missingFired = conditionMatrixFrom({
      ...good,
      sweeps: [fired("m-path-array", "m-path-missing"), fired(), fired("m-changed")],
    });
    expect(missingFired.pathMissing).toBe(false);
    expect(missingFired.gt).toBe(false);
    const changedEarly = conditionMatrixFrom({
      ...good,
      sweeps: [fired("m-changed"), fired("m-changed"), fired()],
    });
    expect(changedEarly).toMatchObject({ changedInitialSafe: false, changedStableSafe: false, changedFires: false });
    const erroredText = conditionMatrixFrom({
      ...good,
      watchers: [...watchers.slice(0, 2), { name: "m-non-numeric", lastValue: null, lastError: "boom" }],
    });
    expect(erroredText.nonNumericSafe).toBe(false);
  });

  it("reads the sweep JSON of openneko jobs run", () => {
    expect(parseSweep({ checked: 2, fired: [{ watcherId: "w1", name: "a", value: 12 }] })).toEqual({
      checked: 2,
      fired: [{ watcherId: "w1", name: "a", value: 12 }],
    });
    expect(parseSweep({})).toEqual({ checked: 0, fired: [] });
  });

  it("needs both the traced decision and the tool result for each policy check", () => {
    const results = {
      low: { decision: "auto_approved" },
      high: { decision: "needs_approval" },
      denied: { decision: "denied" },
      wrongTarget: { decision: "denied" },
      priority: { decision: "denied", policy: "deny-first" },
      absent: { decision: "denied" },
    };
    const decisions = {
      low: "allow",
      high: "needs_approval",
      denied: "deny",
      wrongTarget: "deny",
      priority: "deny",
      absent: "no_policy",
    };
    expect(POLICY_PROBES.map((probe) => probe.key)).toEqual(Object.keys(decisions));
    expect(Object.values(policyMatrixFrom(results, decisions)).every(Boolean)).toBe(true);
    expect(policyMatrixFrom(results, { ...decisions, low: "needs_approval" }).lowRiskAutoApproved).toBe(false);
    expect(
      policyMatrixFrom({ ...results, priority: { decision: "denied", policy: "eval-policy" } }, decisions).priorityHonored,
    ).toBe(false);
    expect(policyMatrixFrom(results, { ...decisions, absent: "deny" }).noPolicyExplicit).toBe(false);
  });
});

const reachable = await dbReachable();

describe.skipIf(!reachable)("lifecycle eval driver on the eval install", () => {
  it(
    "passes the four lifecycle cases through web, serve and the Go sweep",
    async () => {
      const loaded = await loadEval(CONFIG);
      const plan = createEvalPlan(loaded);
      const driver = createOpenNekoLifecycleDriver({ loaded, plan }, { host: { lockWaitMs: 600_000 } });
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
            runId: "vitest-lifecycle",
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
          expect(score.checks.filter((check) => !check.passed), slot.caseId).toEqual([]);
          expect(execution.measurements?.modelCalls).toBe(0);
        }
      } finally {
        await driver.close?.();
      }
      expect(observed.filter((kind) => kind === "run.end")).toHaveLength(plan.slots.length);
    },
    600_000,
  );
});
