import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { contentDigest } from "./canonical";
import { loadEval, type LoadedEval } from "./load";
import { createEvalPlan, type EvalPlan } from "./plan";
import { renderStoredReport, verifyResult } from "./report";
import {
  ResultLineSchema,
  ResultManifestSchema,
  type EvalResultLine,
} from "./schemas";
import {
  rescoreEvaluation,
  runEvaluation,
  type EvalDriver,
} from "./runner";

export type EvalDriverFactory = (context: {
  loaded: LoadedEval;
  plan: EvalPlan;
}) => Promise<EvalDriver> | EvalDriver;

export type EvalCliOptions = {
  cwd?: string;
  stdout?: Pick<NodeJS.WriteStream, "write">;
  stderr?: Pick<NodeJS.WriteStream, "write">;
  adapters: Readonly<Record<string, EvalDriverFactory>>;
};

function flag(args: readonly string[], name: string): string | undefined {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : undefined;
}

function has(args: readonly string[], name: string): boolean {
  return args.includes(name);
}

function requireFlag(args: readonly string[], name: string): string {
  const value = flag(args, name);
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function usage(): string {
  return `OpenNeko harness eval\n\n` +
    `Commands:\n` +
    `  validate --config <path>\n` +
    `  plan --config <path> [--json]\n` +
    `  oracles --config <path> [--json] [--out <path>]\n` +
    `  run --config <path> [--resume <run-id> | --restart] [--promote | --no-promote]\n` +
    `  resume --config <path> --run <run-id>\n` +
    `  rescore --config <path> --run <run-id> [--promote | --no-promote]\n` +
    `  verify --result <directory>\n` +
    `  report --result <directory>\n` +
    `  compare --result <directory> --baseline <directory>\n`;
}

async function loadContext(args: readonly string[], cwd: string): Promise<{
  loaded: LoadedEval;
  plan: EvalPlan;
}> {
  const loaded = await loadEval(resolve(cwd, requireFlag(args, "--config")));
  return { loaded, plan: createEvalPlan(loaded) };
}

function planDocument(loaded: LoadedEval, plan: EvalPlan): unknown {
  const derivedSemantics = loaded.config.variants.flatMap((variant) => {
    if (variant.data_path === "graphjin-direct") return ["DATA-DIRECT"];
    if (variant.data_path === "graphjin-agent") return ["DATA-DELEGATED"];
    if (variant.data_path === "planner-host-execute") {
      return ["DATA-PLANNER-EXECUTE"];
    }
    return [];
  });
  const coveredSemantics = [
    ...new Set([
      ...loaded.cases.flatMap((evalCase) => evalCase.semantics),
      ...derivedSemantics,
    ]),
  ].sort();
  const semanticCoverage = loaded.semantics
    ? {
        registryEntries: loaded.semantics.entries.length,
        covered: coveredSemantics,
        coveredCount: coveredSemantics.length,
        requiredEval: loaded.semantics.entries
          .filter((entry) => entry.disposition === "eval")
          .map((entry) => entry.id)
          .sort(),
        missingRequiredEval: loaded.semantics.entries
          .filter(
            (entry) =>
              entry.disposition === "eval" &&
              !coveredSemantics.includes(entry.id),
          )
          .map((entry) => entry.id)
          .sort(),
        dispositionCounts: Object.fromEntries(
          ["declared", "eval", "excluded", "non-eval"].map((disposition) => [
            disposition,
            loaded.semantics!.entries.filter(
              (entry) => entry.disposition === disposition,
            ).length,
          ]),
        ),
      }
    : undefined;
  return {
    schemaVersion: plan.schemaVersion,
    configId: plan.configId,
    suiteId: plan.suiteId,
    adapter: plan.adapter,
    datasets: [...loaded.datasets.values()].map((dataset) => ({
      id: dataset.id,
      version: dataset.version,
      capabilities: dataset.capabilities,
    })),
    variants: loaded.config.variants.map((variant) => ({
      id: variant.id,
      backend: variant.backend,
      provider: variant.outer_model.provider,
      model: variant.outer_model.model,
      dataPath: variant.data_path,
      innerProvider: variant.inner_model?.provider,
      innerModel: variant.inner_model?.model,
    })),
    selectedCases: loaded.cases.map((evalCase) => evalCase.id),
    repetitions: plan.repetitions,
    calls: plan.calls,
    skipped: plan.skipped,
    ...(semanticCoverage ? { semanticCoverage } : {}),
    digests: loaded.digests,
  };
}

async function compareResults(resultDir: string, baselineDir: string): Promise<unknown> {
  await verifyResult(resultDir);
  await verifyResult(baselineDir);
  const manifests = await Promise.all(
    [resultDir, baselineDir].map(async (directory) =>
      ResultManifestSchema.parse(
        JSON.parse(
          await readFile(join(resolve(directory), "manifest.json"), "utf8"),
        ),
      ),
    ),
  );
  const resultManifest = manifests[0]!;
  const baselineManifest = manifests[1]!;
  const comparableFields = [
    "suiteDigest",
    "datasetDigest",
    "oracleDigest",
    "scorerDigest",
  ] as const;
  const incompatible = comparableFields.filter(
    (field) =>
      resultManifest.compatibility[field] !==
      baselineManifest.compatibility[field],
  );
  if (incompatible.length) {
    throw new Error(
      `results are not in the same comparison cohort: ${incompatible.join(", ")}`,
    );
  }
  const summaries = await Promise.all(
    [resultDir, baselineDir].map(async (directory) =>
      JSON.parse(await readFile(join(resolve(directory), "summary.json"), "utf8")) as {
        taskPassRate: number;
        macro: Record<string, number>;
        executionFailures: number;
        safetyGateFailures: number;
        unsafeEffects?: number;
        tasks: Array<{
          key: string;
          majorityPass: boolean;
          vector: Record<string, number>;
        }>;
      },
    ),
  );
  const current = summaries[0]!;
  const baseline = summaries[1]!;
  const episodeSets = await Promise.all(
    [resultDir, baselineDir].map(async (directory) =>
      (await readFile(join(resolve(directory), "results.jsonl"), "utf8"))
        .split("\n")
        .filter(Boolean)
        .map((line) => ResultLineSchema.parse(JSON.parse(line))),
    ),
  );
  const resultEpisodes = episodeSets[0]!;
  const baselineEpisodes = episodeSets[1]!;
  const resultCandidates = new Set(
    resultEpisodes.map((episode) => episode.variantId),
  );
  const baselineCandidates = new Set(
    baselineEpisodes.map((episode) => episode.variantId),
  );
  const preserveVariantIdentity =
    resultCandidates.size > 1 || baselineCandidates.size > 1;
  if (
    preserveVariantIdentity &&
    (resultCandidates.size !== baselineCandidates.size ||
      [...resultCandidates].some(
        (variantId) => !baselineCandidates.has(variantId),
      ))
  ) {
    throw new Error(
      "multi-candidate result cohorts must contain the same variant IDs; compare separate single-candidate cohorts to match different backends",
    );
  }
  if (preserveVariantIdentity) {
    const legacyMismatches = (["configDigest", "variantDigest"] as const).filter(
      (field) =>
        resultManifest.compatibility[field] !==
        baselineManifest.compatibility[field],
    );
    if (legacyMismatches.length) {
      throw new Error(
        `multi-candidate results are not in the same comparison cohort: ${legacyMismatches.join(", ")}`,
      );
    }
  }

  const episodeIdentity = (
    episode: EvalResultLine,
    suiteId: string,
  ): { identity: string; pairKey: string; treatment: string } => {
    const pairKey =
      episode.pairKey ??
      [
        suiteId,
        episode.datasetId,
        episode.caseId,
        String(episode.repetition),
        episode.phase,
      ].join("/");
    const treatment = episode.treatment ?? "default";
    return {
      identity: JSON.stringify([
        pairKey,
        treatment,
        ...(preserveVariantIdentity ? [episode.variantId] : []),
      ]),
      pairKey,
      treatment,
    };
  };
  const indexEpisodes = (
    episodes: readonly EvalResultLine[],
    suiteId: string,
    label: string,
  ) => {
    const indexed = new Map<
      string,
      {
        episode: EvalResultLine;
        pairKey: string;
        treatment: string;
      }
    >();
    for (const episode of episodes) {
      const { identity, pairKey, treatment } = episodeIdentity(
        episode,
        suiteId,
      );
      if (indexed.has(identity)) {
        throw new Error(
          `${label} contains duplicate comparison episode ${pairKey}/${treatment}`,
        );
      }
      indexed.set(identity, { episode, pairKey, treatment });
    }
    return indexed;
  };
  const resultIndex = indexEpisodes(
    resultEpisodes,
    resultManifest.suiteId,
    "result",
  );
  const baselineIndex = indexEpisodes(
    baselineEpisodes,
    baselineManifest.suiteId,
    "baseline",
  );
  const unmatchedResult = [...resultIndex.keys()].filter(
    (identity) => !baselineIndex.has(identity),
  );
  const unmatchedBaseline = [...baselineIndex.keys()].filter(
    (identity) => !resultIndex.has(identity),
  );
  if (unmatchedResult.length || unmatchedBaseline.length) {
    throw new Error(
      `results do not have one-to-one candidate-neutral episode coverage: result-only ${unmatchedResult.length}, baseline-only ${unmatchedBaseline.length}`,
    );
  }

  const zeroVector = {
    groundTruth: 0,
    method: 0,
    behavior: 0,
    safety: 0,
    efficiency: 0,
  };
  const pairedTasks = [...resultIndex.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([identity, resultEntry]) => {
      const baselineEntry = baselineIndex.get(identity)!;
      const resultVector = resultEntry.episode.score?.vector ?? zeroVector;
      const baselineVector = baselineEntry.episode.score?.vector ?? zeroVector;
      return {
        key: `${resultEntry.pairKey}/${resultEntry.treatment}${
          preserveVariantIdentity
            ? `/${resultEntry.episode.variantId}`
            : ""
        }`,
        pairKey: resultEntry.pairKey,
        treatment: resultEntry.treatment,
        result: {
          variantId: resultEntry.episode.variantId,
          caseId: resultEntry.episode.caseId,
          repetition: resultEntry.episode.repetition,
        },
        baseline: {
          variantId: baselineEntry.episode.variantId,
          caseId: baselineEntry.episode.caseId,
          repetition: baselineEntry.episode.repetition,
        },
        verdictDelta:
          Number(resultEntry.episode.score?.verdict === "pass") -
          Number(baselineEntry.episode.score?.verdict === "pass"),
        vectorDelta: Object.fromEntries(
          Object.keys(zeroVector).map((key) => [
            key,
            (resultVector[key as keyof typeof zeroVector] ?? 0) -
              (baselineVector[key as keyof typeof zeroVector] ?? 0),
          ]),
        ),
      };
    });
  const meanPairedGroundTruthDelta = pairedTasks.length
    ? pairedTasks.reduce(
        (sum, task) => sum + Number(task.vectorDelta.groundTruth ?? 0),
        0,
      ) / pairedTasks.length
    : null;
  return {
    schemaVersion: "openneko.eval.comparison/v1",
    result: resolve(resultDir),
    baseline: resolve(baselineDir),
    delta: {
      taskPassRate: current.taskPassRate - baseline.taskPassRate,
      macro: Object.fromEntries(
        Object.keys(current.macro).map((key) => [
          key,
          (current.macro[key] ?? 0) - (baseline.macro[key] ?? 0),
        ]),
      ),
      executionFailures: current.executionFailures - baseline.executionFailures,
      safetyGateFailures: current.safetyGateFailures - baseline.safetyGateFailures,
      unsafeEffects: (current.unsafeEffects ?? 0) - (baseline.unsafeEffects ?? 0),
    },
    paired: {
      // Kept as `tasks` and `taskDeltas` for comparison/v1 compatibility;
      // entries are now repetition-specific episode pairs.
      granularity: "episode",
      episodes: pairedTasks.length,
      tasks: pairedTasks.length,
      meanGroundTruthDelta: meanPairedGroundTruthDelta,
      taskDeltas: pairedTasks,
    },
  };
}

export async function runEvalCli(
  argv: readonly string[],
  options: EvalCliOptions,
): Promise<number> {
  const stdout = options.stdout ?? process.stdout;
  const cwd = resolve(options.cwd ?? process.cwd());
  const command = argv[0];
  const args = argv.slice(1);
  if (!command || command === "help" || has(args, "--help")) {
    stdout.write(usage());
    return 0;
  }

  if (command === "validate") {
    const { loaded, plan } = await loadContext(args, cwd);
    if (!options.adapters[loaded.config.adapter]) {
      throw new Error(`unknown eval adapter ${loaded.config.adapter}`);
    }
    stdout.write(
      `valid ${loaded.config.id}: ${loaded.cases.length} cases, ${loaded.config.variants.length} variants, ${plan.calls} calls\n`,
    );
    return 0;
  }
  if (command === "plan") {
    const { loaded, plan } = await loadContext(args, cwd);
    if (!options.adapters[loaded.config.adapter]) {
      throw new Error(`unknown eval adapter ${loaded.config.adapter}`);
    }
    const document = planDocument(loaded, plan);
    if (has(args, "--json")) stdout.write(`${JSON.stringify(document, null, 2)}\n`);
    else {
      stdout.write(
        `${plan.configId}: ${loaded.cases.length} cases × ${loaded.config.variants.length} variants × ${plan.repetitions} repetitions = ${plan.calls} calls\n`,
      );
      for (const variant of loaded.config.variants) {
        stdout.write(
          `  ${variant.id}: ${variant.backend} / ${variant.outer_model.provider}:${variant.outer_model.model} / ${variant.data_path}\n`,
        );
      }
      for (const skipped of plan.skipped) {
        stdout.write(`  skipped ${skipped.variantId}/${skipped.caseId}: ${skipped.reason}\n`);
      }
    }
    return 0;
  }
  if (command === "oracles") {
    const { loaded, plan } = await loadContext(args, cwd);
    const factory = options.adapters[loaded.config.adapter];
    if (!factory) throw new Error(`unknown eval adapter ${loaded.config.adapter}`);
    const driver = await factory({ loaded, plan });
    try {
      const values: Record<string, unknown> = {};
      for (const evalCase of loaded.cases) {
        values[evalCase.id] = await driver.resolveOracle(evalCase, {
          loaded,
          plan,
        });
      }
      const document = {
        schemaVersion: "openneko.eval.oracles/v1",
        configId: plan.configId,
        suiteId: plan.suiteId,
        adapter: plan.adapter,
        datasetDigest: loaded.digests.datasets,
        digest: contentDigest(values),
        values,
      };
      const outPath = flag(args, "--out");
      if (outPath) {
        const target = resolve(cwd, outPath);
        await mkdir(dirname(target), { recursive: true });
        await writeFile(target, `${JSON.stringify(document, null, 2)}\n`);
      }
      if (has(args, "--json") || !outPath) {
        if (has(args, "--json")) {
          stdout.write(`${JSON.stringify(document, null, 2)}\n`);
        } else {
          stdout.write(
            `${plan.configId}: resolved ${loaded.cases.length} oracles (digest ${document.digest})\n`,
          );
          for (const evalCase of loaded.cases) {
            stdout.write(
              `  ${evalCase.id}: ${JSON.stringify(values[evalCase.id])}\n`,
            );
          }
        }
      } else {
        stdout.write(
          `${plan.configId}: resolved ${loaded.cases.length} oracles (digest ${document.digest}) -> ${outPath}\n`,
        );
      }
      return 0;
    } finally {
      await driver.close?.();
    }
  }
  if (command === "run" || command === "resume" || command === "rescore") {
    const { loaded, plan } = await loadContext(args, cwd);
    const factory = options.adapters[loaded.config.adapter];
    if (!factory) throw new Error(`unknown eval adapter ${loaded.config.adapter}`);
    const driver = await factory({ loaded, plan });
    const stateRoot = resolve(cwd, loaded.config.artifacts.state_dir);
    const resultsRoot = resolve(cwd, loaded.config.artifacts.results_dir);
    if (command === "rescore") {
      const result = await rescoreEvaluation({
        loaded,
        plan,
        driver,
        stateRoot,
        resultsRoot,
        runId: requireFlag(args, "--run"),
        promote: has(args, "--promote"),
      });
      stdout.write(
        `rescored without model calls: ${result.path}${
          result.resultDir ? `; result ${result.resultDir}` : ""
        }\n`,
      );
      return result.gatesPassed ? 0 : 2;
    }
    const resumeRunId =
      command === "resume" ? requireFlag(args, "--run") : flag(args, "--resume");
    const result = await runEvaluation({
      loaded,
      plan,
      driver,
      cwd,
      stateRoot,
      resultsRoot,
      ...(resumeRunId ? { resumeRunId } : {}),
      restart: has(args, "--restart"),
      ...(has(args, "--promote")
        ? { promote: true }
        : has(args, "--no-promote")
          ? { promote: false }
          : {}),
    });
    stdout.write(
      `${result.resumed ? "resumed" : "completed"} ${result.runId}; reused ${result.reusedEpisodes} episodes${
        result.resultDir ? `; result ${result.resultDir}` : ""
      }\n`,
    );
    return result.gatesPassed ? 0 : 2;
  }
  if (command === "verify") {
    const result = await verifyResult(
      resolve(cwd, requireFlag(args, "--result")),
    );
    stdout.write(
      `verified ${result.runId}: ${result.episodes} episodes (${result.digest}); gates ${result.gatesPassed ? "passed" : "failed"}\n`,
    );
    return result.gatesPassed ? 0 : 2;
  }
  if (command === "report") {
    stdout.write(
      await renderStoredReport(resolve(cwd, requireFlag(args, "--result"))),
    );
    return 0;
  }
  if (command === "compare") {
    const comparison = await compareResults(
      resolve(cwd, requireFlag(args, "--result")),
      resolve(cwd, requireFlag(args, "--baseline")),
    );
    stdout.write(`${JSON.stringify(comparison, null, 2)}\n`);
    return 0;
  }
  throw new Error(`unknown eval command ${command}\n\n${usage()}`);
}
