import { contentDigest } from "./canonical";
import {
  ScoreSchema,
  type EvalEpisode,
  type EvalSecurityOutcome,
  type EvalScore,
  type EvalUnsafeEffect,
} from "./schemas";

export type ScoreCheckInput = Omit<
  EvalScore["checks"][number],
  "score"
> & { score?: number };

const DIMENSIONS = [
  "ground_truth",
  "method",
  "behavior",
  "safety",
  "efficiency",
] as const;

function mean(values: readonly number[], fallback: number): number {
  return values.length
    ? values.reduce((sum, value) => sum + value, 0) / values.length
    : fallback;
}

/**
 * The assertions that apply to one phase of a case: assertions bound to the
 * phase via `phase:` plus unbound assertions, which apply to every phase.
 */
export function assertionsForPhase<T extends { phase?: string }>(
  assertions: readonly T[],
  phase: string,
): T[] {
  return assertions.filter(
    (assertion) => !assertion.phase || assertion.phase === phase,
  );
}

export function createScore(input: {
  scorerId: string;
  scorerVersion: string;
  scorerDefinition: unknown;
  checks: readonly ScoreCheckInput[];
  unsafeEffects?: readonly EvalUnsafeEffect[];
  securityOutcomes?: readonly EvalSecurityOutcome[];
}): EvalScore {
  const checks = input.checks.map((check) => ({
    ...check,
    score: check.score ?? (check.passed ? 1 : 0),
  }));
  const dimensionChecks = (name: (typeof DIMENSIONS)[number]) =>
    checks.filter((check) => check.dimension === name);
  const dimension = (name: (typeof DIMENSIONS)[number], fallback: number) =>
    mean(dimensionChecks(name).map((check) => check.score), fallback);
  return ScoreSchema.parse({
    schemaVersion: "openneko.eval.score/v1",
    scorerId: input.scorerId,
    scorerVersion: input.scorerVersion,
    scorerDigest: contentDigest({
      id: input.scorerId,
      version: input.scorerVersion,
      definition: input.scorerDefinition,
    }),
    verdict: checks.some((check) => check.gate && !check.passed) ? "fail" : "pass",
    vector: {
      groundTruth: dimension("ground_truth", 0),
      method: dimension("method", 0),
      behavior: dimension("behavior", 0),
      safety: dimension("safety", 0),
      efficiency: dimension("efficiency", 0),
    },
    coverage: {
      groundTruth: dimensionChecks("ground_truth").length > 0,
      method: dimensionChecks("method").length > 0,
      behavior: dimensionChecks("behavior").length > 0,
      safety: dimensionChecks("safety").length > 0,
      efficiency: dimensionChecks("efficiency").length > 0,
    },
    checks,
    unsafeEffects: input.unsafeEffects ?? [],
    ...(input.securityOutcomes
      ? { securityOutcomes: input.securityOutcomes }
      : {}),
  });
}

type Vector = EvalScore["vector"];

function averageVectors(vectors: readonly Vector[]): Vector {
  const average = (field: keyof Vector) =>
    mean(vectors.map((vector) => vector[field]), 0);
  return {
    groundTruth: average("groundTruth"),
    method: average("method"),
    behavior: average("behavior"),
    safety: average("safety"),
    efficiency: average("efficiency"),
  };
}

function choose(n: number, k: number): number {
  if (k < 0 || k > n) return 0;
  let result = 1;
  for (let index = 1; index <= Math.min(k, n - k); index += 1) {
    result = (result * (n - index + 1)) / index;
  }
  return result;
}

function passAtK(n: number, correct: number, k: number): number | null {
  if (n < k || k < 1) return null;
  return 1 - choose(n - correct, k) / choose(n, k);
}

function passPowerK(n: number, correct: number, k: number): number | null {
  if (n < k || k < 1) return null;
  return choose(correct, k) / choose(n, k);
}

function quantile(values: readonly number[], q: number): number {
  if (!values.length) return 0;
  const sorted = [...values].sort((left, right) => left - right);
  const index = (sorted.length - 1) * q;
  const lower = Math.floor(index);
  const upper = Math.ceil(index);
  if (lower === upper) return sorted[lower] ?? 0;
  const weight = index - lower;
  return (sorted[lower] ?? 0) * (1 - weight) + (sorted[upper] ?? 0) * weight;
}

function numericMeasurement(
  episode: EvalEpisode,
  field: string,
): number | undefined {
  const value = episode.measurements[field];
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? value
    : undefined;
}

function aggregateMeasurement(
  episodes: readonly EvalEpisode[],
  field: string,
) {
  const values = episodes.flatMap((episode) => {
    const value = numericMeasurement(episode, field);
    return value === undefined ? [] : [value];
  });
  const total = values.reduce((sum, value) => sum + value, 0);
  return {
    count: values.length,
    coverage: episodes.length ? values.length / episodes.length : 0,
    total,
    mean: values.length ? total / values.length : null,
    p50: values.length ? quantile(values, 0.5) : null,
    p95: values.length ? quantile(values, 0.95) : null,
    max: values.length ? Math.max(...values) : null,
  };
}

function seededRandom(seedText: string): () => number {
  let state = Number.parseInt(contentDigest(seedText).slice(7, 15), 16) >>> 0;
  return () => {
    state = (state * 1664525 + 1013904223) >>> 0;
    return state / 0x1_0000_0000;
  };
}

function bootstrapCI(values: readonly number[], seed: string): [number, number] {
  if (values.length < 2) return [values[0] ?? 0, values[0] ?? 0];
  const random = seededRandom(seed);
  const samples: number[] = [];
  for (let iteration = 0; iteration < 1000; iteration += 1) {
    let total = 0;
    for (let index = 0; index < values.length; index += 1) {
      total += values[Math.floor(random() * values.length)] ?? 0;
    }
    samples.push(total / values.length);
  }
  return [quantile(samples, 0.025), quantile(samples, 0.975)];
}

function bootstrapTaskSeed(
  tasks: readonly { unsafeEffects: number; [key: string]: unknown }[],
): string {
  // Unsafe-effect counts were added to the public task summary after result/v1
  // artifacts already existed. Keep the statistical seed tied to the original
  // task outcome document so adding reporting-only fields cannot rewrite a
  // historical confidence interval.
  return contentDigest(
    tasks.map(({ unsafeEffects: _unsafeEffects, ...task }) => task),
  );
}

function wilson95(successes: number, total: number): [number, number] | null {
  if (total < 1) return null;
  const z = 1.959963984540054;
  const proportion = successes / total;
  const denominator = 1 + (z * z) / total;
  const center = (proportion + (z * z) / (2 * total)) / denominator;
  const margin =
    (z / denominator) *
    Math.sqrt(
      (proportion * (1 - proportion)) / total +
        (z * z) / (4 * total * total),
    );
  return [Math.max(0, center - margin), Math.min(1, center + margin)];
}

function assertionCapabilitySummary(episodes: readonly EvalEpisode[]) {
  const capabilities = new Set(
    episodes.flatMap((episode) => [
      ...(episode.assertionTargets ?? []).flatMap(
        (target) => target.capabilities,
      ),
      ...(episode.score?.checks ?? []).flatMap(
        (check) => check.capabilities ?? [],
      ),
    ]),
  );

  return Object.fromEntries(
    [...capabilities].sort().map((capability) => {
      let attemptedEpisodes = 0;
      let completedEpisodes = 0;
      let unavailableEpisodes = 0;
      let attemptedAssertions = 0;
      let passingAssertions = 0;
      let failingAssertions = 0;
      let unavailableAssertions = 0;

      for (const episode of episodes) {
        const declared = (episode.assertionTargets ?? []).filter((target) =>
          target.capabilities.includes(capability),
        );
        const fallback = (episode.score?.checks ?? [])
          .filter((check) => check.capabilities?.includes(capability))
          .map((check) => ({ assertionId: check.assertionId }));
        const targets = declared.length ? declared : fallback;
        if (!targets.length) continue;
        attemptedEpisodes += 1;
        attemptedAssertions += targets.length;
        const checks = new Map(
          (episode.score?.checks ?? []).map((check) => [
            check.assertionId,
            check,
          ]),
        );
        let missing = 0;
        for (const target of targets) {
          const check = checks.get(target.assertionId);
          if (!check) {
            missing += 1;
            unavailableAssertions += 1;
          } else if (check.passed) {
            passingAssertions += 1;
          } else {
            failingAssertions += 1;
          }
        }
        if (missing === 0) completedEpisodes += 1;
        else unavailableEpisodes += 1;
      }

      const completedAssertions = passingAssertions + failingAssertions;
      return [
        capability,
        {
          attemptedEpisodes,
          completedEpisodes,
          unavailableEpisodes,
          attemptedAssertions,
          passingAssertions,
          failingAssertions,
          unavailableAssertions,
          unconditionalPassRate: attemptedAssertions
            ? passingAssertions / attemptedAssertions
            : 0,
          conditionalPassRate: completedAssertions
            ? passingAssertions / completedAssertions
            : null,
          coverage: attemptedAssertions
            ? completedAssertions / attemptedAssertions
            : 0,
          unconditional95CI: wilson95(passingAssertions, attemptedAssertions),
          conditional95CI: wilson95(passingAssertions, completedAssertions),
        },
      ];
    }),
  );
}

function countBy(
  values: readonly string[],
): Record<string, number> {
  return Object.fromEntries(
    [...new Set(values)].sort().map((value) => [
      value,
      values.filter((candidate) => candidate === value).length,
    ]),
  );
}

export type EvalSummary = ReturnType<typeof summarizeEpisodes>;

export function summarizeEpisodes(episodes: readonly EvalEpisode[]) {
  const scored = episodes.filter(
    (episode): episode is EvalEpisode & { score: EvalScore } => Boolean(episode.score),
  );
  const byTask = new Map<string, EvalEpisode[]>();
  for (const episode of episodes) {
    const key = `${episode.variantId}/${episode.caseId}/${episode.phase}`;
    const group = byTask.get(key) ?? [];
    group.push(episode);
    byTask.set(key, group);
  }
  const tasks = [...byTask.entries()].map(([key, group]) => {
    const passes = group.filter(
      (episode) => episode.score?.verdict === "pass",
    ).length;
    const majorityPass = passes > group.length / 2;
    const unsafeEffects = group.reduce((total, episode) => {
      const typed = episode.score?.securityOutcomes;
      return (
        total +
        (typed
          ? typed.filter((outcome) => outcome.outcome === "completed").length
          : (episode.score?.unsafeEffects?.length ?? 0))
      );
    }, 0);
    return {
      key,
      variantId: group[0]?.variantId ?? "unknown",
      caseId: group[0]?.caseId ?? "unknown",
      datasetId: group[0]?.datasetId ?? "unknown",
      phase: group[0]?.phase ?? "unknown",
      family: group[0]?.family ?? "contract",
      productPath: group[0]?.productPath ?? "unknown",
      difficulty: group[0]?.difficulty ?? "unknown",
      capabilityTags: group[0]?.capabilityTags ?? [],
      semantics: group[0]?.semantics ?? [],
      repetitions: group.length,
      passes,
      majorityPass,
      consistency: group.length ? Math.max(passes, group.length - passes) / group.length : 0,
      unsafeEffects,
      // Execution/environment failures are real failed attempts, not missing
      // data. Counting them as zero prevents a broken variant from looking
      // perfect merely because its failed tasks had no score document.
      vector: averageVectors(
        group.map(
          (episode) =>
            episode.score?.vector ?? {
              groundTruth: 0,
              method: 0,
              behavior: 0,
              safety: 0,
              efficiency: 0,
            },
        ),
      ),
      passAtK: Object.fromEntries(
        [1, 3, 5].map((k) => [String(k), passAtK(group.length, passes, k)]),
      ),
      passPowerK: Object.fromEntries(
        [1, 3, 5].map((k) => [String(k), passPowerK(group.length, passes, k)]),
      ),
    };
  });
  const macroVector = averageVectors(tasks.map((task) => task.vector));
  const microVector = averageVectors(
    episodes.map(
      (episode) =>
        episode.score?.vector ?? {
          groundTruth: 0,
          method: 0,
          behavior: 0,
          safety: 0,
          efficiency: 0,
        },
    ),
  );
  const macroGroundTruth = tasks.map((task) => task.vector.groundTruth);
  const failures = episodes.filter((episode) => episode.status !== "completed");
  const grouped = (
    values: readonly string[],
    includes: (task: (typeof tasks)[number], value: string) => boolean,
  ) =>
    Object.fromEntries(
      [...new Set(values)].sort().map((value) => {
        const members = tasks.filter((task) => includes(task, value));
        const passed = members.filter((task) => task.majorityPass).length;
        return [
          value,
          {
            tasks: members.length,
            passed,
            passRate: members.length ? passed / members.length : 0,
            vector: averageVectors(members.map((task) => task.vector)),
          },
        ];
      }),
    );
  const difficulties = tasks.map((task) => task.difficulty);
  const families = tasks.map((task) => task.family);
  const capabilities = tasks.flatMap((task) => task.capabilityTags);
  const semantics = tasks.flatMap((task) => task.semantics);
  const variants = tasks.map((task) => task.variantId);
  const datasets = tasks.map((task) => task.datasetId);
  const productPaths = tasks.map((task) => task.productPath);
  const usageCoverage = episodes.map((episode) => {
    const value = episode.measurements.usageCoverage;
    return value === "complete" || value === "partial" ? value : "unavailable";
  });
  const costCoverage = episodes.map((episode) => {
    const value = episode.measurements.costCoverage;
    return value === "complete" || value === "partial" ? value : "unavailable";
  });
  const securityOutcomes = scored.flatMap(
    (episode) => episode.score.securityOutcomes ?? [],
  );
  // Security outcomes supersede the legacy unsafeEffects list per episode,
  // not per result. This keeps mixed old/new journals correct during an
  // interrupted upgrade: a typed outcome in one episode must not hide a
  // legacy completed effect recorded by another episode.
  const completedUnsafeEffectKinds = scored.flatMap<string>((episode) => {
    const typed = episode.score.securityOutcomes;
    return typed
      ? typed
          .filter((outcome) => outcome.outcome === "completed")
          .map((outcome) => outcome.kind)
      : (episode.score.unsafeEffects ?? []).map((effect) => effect.kind);
  });
  const assertionCapabilities = assertionCapabilitySummary(episodes);
  const coverage = Object.fromEntries(
    ["groundTruth", "method", "behavior", "safety", "efficiency"].map((field) => [
      field,
      scored.length
        ? scored.filter(
            (episode) =>
              episode.score.coverage[field as keyof EvalScore["coverage"]],
          ).length / scored.length
        : 0,
    ]),
  ) as Record<keyof EvalScore["coverage"], number>;
  return {
    schemaVersion: "openneko.eval.summary/v1" as const,
    expectedEpisodes: episodes.length,
    scoredEpisodes: scored.length,
    executionFailures: failures.length,
    taskCount: tasks.length,
    passedTasks: tasks.filter((task) => task.majorityPass).length,
    taskPassRate: tasks.length
      ? tasks.filter((task) => task.majorityPass).length / tasks.length
      : 0,
    meanConsistency: mean(tasks.map((task) => task.consistency), 0),
    macro: macroVector,
    micro: microVector,
    coverage,
    measurements: {
      wallDurationMs: aggregateMeasurement(episodes, "wallDurationMs"),
      firstOutputMs: aggregateMeasurement(episodes, "firstOutputMs"),
      toolCalls: aggregateMeasurement(episodes, "toolCalls"),
      repeatedToolCalls: aggregateMeasurement(episodes, "repeatedToolCalls"),
      maxToolCalls: aggregateMeasurement(episodes, "maxToolCalls"),
      inputTokens: aggregateMeasurement(episodes, "inputTokens"),
      outputTokens: aggregateMeasurement(episodes, "outputTokens"),
      cacheReadTokens: aggregateMeasurement(episodes, "cacheReadTokens"),
      cacheWriteTokens: aggregateMeasurement(episodes, "cacheWriteTokens"),
      reasoningTokens: aggregateMeasurement(episodes, "reasoningTokens"),
      totalTokens: aggregateMeasurement(episodes, "totalTokens"),
      estimatedCostUsd: aggregateMeasurement(episodes, "estimatedCostUsd"),
      billedCostUsd: aggregateMeasurement(episodes, "billedCostUsd"),
      usageCoverage: {
        completeEpisodes: usageCoverage.filter((value) => value === "complete").length,
        partialEpisodes: usageCoverage.filter((value) => value === "partial").length,
        unavailableEpisodes: usageCoverage.filter(
          (value) => value === "unavailable",
        ).length,
        completeRate: usageCoverage.length
          ? usageCoverage.filter((value) => value === "complete").length /
            usageCoverage.length
          : 0,
        availableRate: usageCoverage.length
          ? usageCoverage.filter((value) => value !== "unavailable").length /
            usageCoverage.length
          : 0,
      },
      costCoverage: {
        completeEpisodes: costCoverage.filter((value) => value === "complete").length,
        partialEpisodes: costCoverage.filter((value) => value === "partial").length,
        unavailableEpisodes: costCoverage.filter(
          (value) => value === "unavailable",
        ).length,
        completeRate: costCoverage.length
          ? costCoverage.filter((value) => value === "complete").length /
            costCoverage.length
          : 0,
        availableRate: costCoverage.length
          ? costCoverage.filter((value) => value !== "unavailable").length /
            costCoverage.length
          : 0,
      },
    },
    macroGroundTruth95CI: bootstrapCI(
      macroGroundTruth,
      bootstrapTaskSeed(tasks),
    ),
    safetyGateFailures: scored.flatMap((episode) => episode.score.checks).filter(
      (check) => check.dimension === "safety" && check.gate && !check.passed,
    ).length,
    behaviorGateFailures: scored.flatMap((episode) => episode.score.checks).filter(
      (check) => check.dimension === "behavior" && check.gate && !check.passed,
    ).length,
    unsafeEffects: completedUnsafeEffectKinds.length,
    unsafeEffectEpisodes: scored.filter(
      (episode) =>
        episode.score.securityOutcomes
          ? episode.score.securityOutcomes.some(
              (outcome) => outcome.outcome === "completed",
            )
          : (episode.score.unsafeEffects?.length ?? 0) > 0,
    ).length,
    unsafeEffectsByKind: countBy(completedUnsafeEffectKinds),
    assertionCapabilities,
    securityOutcomes: {
      total: securityOutcomes.length,
      episodes: scored.filter(
        (episode) => (episode.score.securityOutcomes?.length ?? 0) > 0,
      ).length,
      byOutcome: countBy(securityOutcomes.map((outcome) => outcome.outcome)),
      byKind: countBy(securityOutcomes.map((outcome) => outcome.kind)),
      bySeverity: countBy(
        securityOutcomes.map((outcome) => outcome.severity),
      ),
      byOutcomeSeverity: countBy(
        securityOutcomes.map(
          (outcome) => `${outcome.outcome}:${outcome.severity}`,
        ),
      ),
      byOutcomeKind: countBy(
        securityOutcomes.map(
          (outcome) => `${outcome.outcome}:${outcome.kind}`,
        ),
      ),
    },
    tasks,
    byDifficulty: grouped(
      difficulties,
      (task, value) => task.difficulty === value,
    ),
    byFamily: grouped(families, (task, value) => task.family === value),
    byCapability: grouped(
      capabilities,
      (task, value) => task.capabilityTags.includes(value),
    ),
    bySemantic: grouped(
      semantics,
      (task, value) => task.semantics.includes(value),
    ),
    byDataset: grouped(datasets, (task, value) => task.datasetId === value),
    byProductPath: grouped(
      productPaths,
      (task, value) => task.productPath === value,
    ),
    byVariant: grouped(variants, (task, value) => task.variantId === value),
    failureTypes: Object.fromEntries(
      [...new Set(failures.map((episode) => episode.errorType ?? "unknown"))]
        .sort()
        .map((type) => [
          type,
          failures.filter((episode) => (episode.errorType ?? "unknown") === type).length,
        ]),
    ),
    failureDetails: [
      ...new Set(failures.map((episode) => episode.errorType ?? "unknown")),
    ]
      .sort()
      .map((type) => ({
        type,
        episodes: failures.filter(
          (episode) => (episode.errorType ?? "unknown") === type,
        ).length,
        taskIds: [
          ...new Set(
            failures
              .filter(
                (episode) => (episode.errorType ?? "unknown") === type,
              )
              .map((episode) => episode.caseId),
          ),
        ].sort(),
      })),
  };
}
