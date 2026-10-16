import { createHash } from "node:crypto";

export type LearningSplit = "learn" | "verify" | "test";

export type SplitCounts = Readonly<Record<LearningSplit, number>>;

export const DEFAULT_SPLIT_COUNTS: SplitCounts = { learn: 5, verify: 2, test: 3 };

function digest(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

/** Assign each sibling to one split. The order depends only on the seed and the IDs. */
export function seededSplit(
  ids: readonly string[],
  seed: string,
  counts: SplitCounts = DEFAULT_SPLIT_COUNTS,
): Record<string, LearningSplit> {
  if (new Set(ids).size !== ids.length) {
    throw new Error("sibling IDs must be unique");
  }
  const total = counts.learn + counts.verify + counts.test;
  if (ids.length !== total) {
    throw new Error(`expected ${total} siblings, received ${ids.length}`);
  }
  const ordered = [...ids].sort(
    (left, right) =>
      digest(`${seed}/${left}`).localeCompare(digest(`${seed}/${right}`)) ||
      left.localeCompare(right),
  );
  const result: Record<string, LearningSplit> = {};
  ordered.forEach((id, index) => {
    result[id] =
      index < counts.learn
        ? "learn"
        : index < counts.learn + counts.verify
          ? "verify"
          : "test";
  });
  return result;
}

export type OracleLeakSource = {
  caseId: string;
  values: readonly number[];
  dates?: readonly string[];
};

export type LeakedValue = {
  ruleIndex: number;
  caseId: string;
  value: string;
};

const NUMBER_RE = /\d[\d,]*(?:\.\d+)?/gu;

function significantDigits(text: string): number {
  return text.replace(/[^0-9]/gu, "").replace(/^0+/u, "").length;
}

/**
 * Report every learned rule that repeats an oracle value. A number matches
 * when the oracle value rounds to it at the precision the rule wrote.
 * Numbers with fewer than three significant digits are ignored, because
 * counts such as "2 runs" carry no answer.
 */
export function findLeakedValues(
  rules: readonly string[],
  sources: readonly OracleLeakSource[],
): LeakedValue[] {
  const leaks: LeakedValue[] = [];
  rules.forEach((rule, ruleIndex) => {
    for (const source of sources) {
      for (const date of source.dates ?? []) {
        if (date && rule.includes(date)) {
          leaks.push({ ruleIndex, caseId: source.caseId, value: date });
        }
      }
    }
    for (const match of rule.matchAll(NUMBER_RE)) {
      const text = match[0].replace(/,/gu, "");
      if (significantDigits(text) < 3) continue;
      const written = Number(text);
      if (!Number.isFinite(written)) continue;
      const decimals = text.includes(".") ? text.split(".")[1]!.length : 0;
      const factor = 10 ** decimals;
      for (const source of sources) {
        for (const value of source.values) {
          if (!Number.isFinite(value)) continue;
          if (Math.round(Math.abs(value) * factor) === Math.round(written * factor)) {
            leaks.push({ ruleIndex, caseId: source.caseId, value: text });
          }
        }
      }
    }
  });
  return leaks;
}

function mulberry32(seed: number): () => number {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Percentile bootstrap 95% interval of the mean, with a fixed seed. */
export function bootstrapMeanCi(
  values: readonly number[],
  seed = "openneko-learning",
  iterations = 10_000,
): { mean: number; low: number; high: number } | null {
  if (values.length === 0) return null;
  const mean = values.reduce((sum, value) => sum + value, 0) / values.length;
  const random = mulberry32(Number.parseInt(digest(seed).slice(0, 8), 16));
  const means: number[] = [];
  for (let iteration = 0; iteration < iterations; iteration += 1) {
    let sum = 0;
    for (let index = 0; index < values.length; index += 1) {
      sum += values[Math.floor(random() * values.length)]!;
    }
    means.push(sum / values.length);
  }
  means.sort((left, right) => left - right);
  const at = (quantile: number) =>
    means[Math.min(means.length - 1, Math.max(0, Math.floor(quantile * means.length)))]!;
  return { mean, low: at(0.025), high: at(0.975) };
}

export type ArmTaskOutcome = {
  taskId: string;
  arm: string;
  repetition: number;
  passed: boolean;
};

export type PairedArmComparison = {
  treatment: string;
  control: string;
  tasks: number;
  treatmentPassRate: number;
  controlPassRate: number;
  meanDifference: number;
  ci95: { low: number; high: number } | null;
  perTask: Array<{
    taskId: string;
    treatmentPassRate: number;
    controlPassRate: number;
    difference: number;
  }>;
};

/** Pair two arms by task. Each task contributes its pass-rate difference over repetitions. */
export function pairedArmComparison(
  outcomes: readonly ArmTaskOutcome[],
  treatment: string,
  control: string,
  seed?: string,
): PairedArmComparison {
  const rates = (arm: string) => {
    const byTask = new Map<string, boolean[]>();
    for (const outcome of outcomes) {
      if (outcome.arm !== arm) continue;
      const list = byTask.get(outcome.taskId) ?? [];
      list.push(outcome.passed);
      byTask.set(outcome.taskId, list);
    }
    return new Map(
      [...byTask].map(([taskId, list]) => [
        taskId,
        list.filter(Boolean).length / list.length,
      ]),
    );
  };
  const treated = rates(treatment);
  const controlled = rates(control);
  const perTask = [...treated.keys()]
    .filter((taskId) => controlled.has(taskId))
    .sort()
    .map((taskId) => ({
      taskId,
      treatmentPassRate: treated.get(taskId)!,
      controlPassRate: controlled.get(taskId)!,
      difference: treated.get(taskId)! - controlled.get(taskId)!,
    }));
  const mean = (values: number[]) =>
    values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : 0;
  const ci = bootstrapMeanCi(
    perTask.map((task) => task.difference),
    seed ?? `${treatment}/${control}`,
  );
  return {
    treatment,
    control,
    tasks: perTask.length,
    treatmentPassRate: mean(perTask.map((task) => task.treatmentPassRate)),
    controlPassRate: mean(perTask.map((task) => task.controlPassRate)),
    meanDifference: mean(perTask.map((task) => task.difference)),
    ci95: ci ? { low: ci.low, high: ci.high } : null,
    perTask,
  };
}
