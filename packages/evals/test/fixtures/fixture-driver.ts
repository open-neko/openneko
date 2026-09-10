import { appendFile, readFile } from "node:fs/promises";
import {
  createScore,
  type EvalDriver,
  type EvalPlan,
  type LoadedEval,
} from "../../src";

export function createFixtureDriver(input: {
  loaded: LoadedEval;
  plan: EvalPlan;
  callLog: string;
  crashOnCall?: number;
  resetLog?: string;
  counters?: { execute: number; score: number };
  estimatedCostUsd?: number;
  totalTokens?: number;
  usageCoverage?: "complete" | "partial" | "unavailable";
}): EvalDriver {
  return {
    id: "fixture",
    scorer: { id: "fixture", version: "1.0.0", definition: { exact: true } },
    async preflight() {
      return { datasetFingerprint: { fixture: "v1" } };
    },
    async resolveOracle(evalCase) {
      return { expected: Number(evalCase.input.expected) };
    },
    ...(input.resetLog
      ? {
          async reset({ slot }) {
            await appendFile(input.resetLog!, `${slot.key}\n`, "utf8");
          },
        }
      : {}),
    async execute({ slot, observer }) {
      input.counters && (input.counters.execute += 1);
      await appendFile(input.callLog, `${slot.key}\n`, "utf8");
      let calls = 0;
      try {
        calls = (await readFile(input.callLog, "utf8")).split("\n").filter(Boolean).length;
      } catch {
        // appendFile created it; this only protects unusual filesystems.
      }
      if (input.crashOnCall === calls) process.exit(91);
      await observer.observe({
        kind: "run.start",
        operationId: slot.key,
        attributes: { "openneko.backend": slot.variant.backend },
      });
      await observer.observe({
        kind: "run.end",
        operationId: slot.key,
        status: "ok",
        measurements: { durationMs: 1, coverage: "complete" },
      });
      return {
        output: { actual: Number(slot.case.input.expected) },
        measurements: {
          wallDurationMs: 1,
          ...(input.estimatedCostUsd !== undefined
            ? { estimatedCostUsd: input.estimatedCostUsd }
            : {}),
          ...(input.totalTokens !== undefined
            ? {
                totalTokens: input.totalTokens,
                usageCoverage: input.usageCoverage ?? "complete",
              }
            : { usageCoverage: "unavailable" }),
        },
      };
    },
    score({ case: evalCase, oracle, execution }) {
      input.counters && (input.counters.score += 1);
      const expected = Number((oracle as { expected: unknown }).expected);
      const actual = Number((execution.output as { actual?: unknown } | undefined)?.actual);
      return createScore({
        scorerId: "fixture",
        scorerVersion: "1.0.0",
        scorerDefinition: { exact: true },
        checks: evalCase.assertions.map((assertion) => ({
          assertionId: assertion.id,
          dimension: assertion.dimension,
          passed: actual === expected,
          gate: assertion.gate,
          ...(assertion.capabilities
            ? { capabilities: assertion.capabilities }
            : {}),
          ...(assertion.semantics ? { semantics: assertion.semantics } : {}),
        })),
      });
    },
  };
}
