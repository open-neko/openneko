import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { and, db, desc, eq, skill_learn_event, work_run } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import {
  DESCRIPTION_FILE,
  LEARNED_FILE,
  LEARN_TARGET_KINDS,
  ORG_PLAYBOOK_FILE,
  RULES_FILE,
  SOURCE_NOTES_FILE,
  getOrgAgentRoot,
  learnTargetDir,
  learnTargetKey,
  learnTargetsForRun,
  loadRunEvidence,
  runLearnForTarget,
  verifyRunForLearning,
  setSkillLearnOrgEnabled,
  writeLearnedNotes,
  type LearnTarget,
  type LearnTargetKind,
  type SkillLearnLlm,
} from "@neko/llm/work";
import {
  EvalEnvironmentError,
  EvalStateStore,
  EvalTaskError,
  contentDigest,
  effectiveConcurrency,
  findLeakedValues,
  loadEval,
  pairedArmComparison,
  runBoundedPool,
  type ArmTaskOutcome,
  type EvalDriver,
  type EvalEpisode,
  type EvalExecution,
  type EvalPlan,
  type EvalSlot,
  type EvalVariant,
  type LeakedValue,
  type LoadedCase,
  type LoadedEval,
  type OracleLeakSource,
  type PairedArmComparison,
} from "@neko/evals";
import { MemoryObservationSink, createHarnessObserver } from "@neko/telemetry";
import {
  createOpenNekoBackendDriver,
  isProviderFailureType,
  overlayBodyFromFile,
  scoreWorkBackendExecution,
  type ExtendedAssertionCheck,
  type LearnedContextObservation,
  type SkillStagingObservation,
} from "./eval-openneko-backend";

export const SKILL_LEARNING_ADAPTER = "openneko.skill-learning";
const ARMS = ["baseline", "learned", "placebo"] as const;
type Arm = (typeof ARMS)[number];

const SCORER = {
  id: SKILL_LEARNING_ADAPTER,
  version: "1.0.0",
  definition: {
    base: "openneko.work-backend answer, trace and safety assertions",
    overlay:
      "overlay-applied and overlay-absent read what the run received for every learned kind: the staged SKILL.md body and description, and the learned data notes and org playbook in the prompt",
    grounding:
      "every learned rule and description cites learning-org runs and quotes text found in a cited run's evidence digest",
    leakage:
      "no learned rule or description repeats a numeric value or date from any measured oracle result",
  },
} as const;

type LearningSettings = {
  corpus: string;
  repetitions: number;
  minRepeats: number;
  learner: "live" | "stub";
  runSettings: Record<string, unknown>;
  kinds: LearnTargetKind[] | null;
};

type GroundingVerdict = {
  ruleId: string;
  grounded: boolean;
  reasons: string[];
};

type LearnedRuleRecord = { id: string; text: string; evidenceRunIds: string[]; evidenceQuotes: string[] };

type PinnedFile = { path: string; digest: string; bytes: number };

export type TargetLearningRecord = {
  kind: LearnTargetKind;
  id: string;
  decision: string;
  reason: string;
  eventId: string | null;
  rules: LearnedRuleRecord[];
  dropped: unknown[];
  grounding: GroundingVerdict[];
  leaks: LeakedValue[];
  artifact: PinnedFile | null;
  rulesFile: PinnedFile | null;
  placebo: PinnedFile | null;
};

export type LearningArtifact = {
  schemaVersion: "openneko.eval.skill-learning-artifact/v2";
  learnKey: string;
  learner: LearningSettings["learner"];
  learnRuns: Array<{ caseId: string; repetition: number; status: string; verdict: string | null }>;
  targets: Record<string, TargetLearningRecord>;
  leaks: LeakedValue[];
};

export type LearningOutput = {
  arm: Arm;
  kinds?: LearnTargetKind[];
  overlays: Record<string, string>;
  descriptions: Record<string, string>;
  sourceNotes: string | null;
  orgPlaybook: string | null;
  rulesLearned: number;
  ungroundedRules: string[];
  leaks: LeakedValue[];
};

const TARGET_FILE: Record<LearnTargetKind, string> = {
  skill: LEARNED_FILE,
  source: SOURCE_NOTES_FILE,
  "skill-description": DESCRIPTION_FILE,
  org: ORG_PLAYBOOK_FILE,
};
const ORG_PLAYBOOK_LINES = 8;

function sha256(value: string): string {
  return `sha256:${createHash("sha256").update(value).digest("hex")}`;
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function durationMs(value: string): number {
  const match = /^(\d+)(ms|s|m|h)$/u.exec(value);
  if (!match) throw new Error(`invalid duration ${value}`);
  return Number(match[1]) * ({ ms: 1, s: 1_000, m: 60_000, h: 3_600_000 }[match[2]!] ?? 1);
}

export function armOf(variant: EvalVariant): Arm {
  const arm = variant.settings?.arm;
  if (!ARMS.includes(arm as Arm)) {
    throw new EvalEnvironmentError(
      `variant ${variant.id} must set settings.arm to ${ARMS.join(", ")}`,
      "variant_invalid",
    );
  }
  return arm as Arm;
}

function learningSettings(variant: EvalVariant, configPath: string): LearningSettings {
  const raw = record(variant.settings?.learning);
  const corpus = raw?.corpus;
  if (typeof corpus !== "string") {
    throw new EvalEnvironmentError(
      `learned variant ${variant.id} needs settings.learning.corpus`,
      "variant_invalid",
    );
  }
  const learner = raw?.learner ?? "live";
  if (learner !== "live" && learner !== "stub") {
    throw new EvalEnvironmentError(`settings.learning.learner must be live or stub`, "variant_invalid");
  }
  return {
    corpus: resolve(dirname(configPath), corpus),
    repetitions: Number(raw?.repetitions ?? 1),
    minRepeats: Number(raw?.min_repeats ?? 5),
    learner,
    runSettings: record(raw?.run_settings) ?? {},
    kinds: learningKinds(variant),
  };
}

/** The learned kinds a variant applies; null applies every kind. */
export function learningKinds(variant: EvalVariant): LearnTargetKind[] | null {
  const kinds = record(variant.settings?.learning)?.kinds;
  if (kinds === undefined) return null;
  if (
    !Array.isArray(kinds) ||
    kinds.length === 0 ||
    kinds.some((kind) => !LEARN_TARGET_KINDS.includes(kind as LearnTargetKind))
  ) {
    throw new EvalEnvironmentError(
      `variant ${variant.id} settings.learning.kinds must list one or more of ${LEARN_TARGET_KINDS.join(", ")}`,
      "variant_invalid",
    );
  }
  return [...new Set(kinds as LearnTargetKind[])];
}

function sharedLearning(settings: LearningSettings): Omit<LearningSettings, "kinds"> {
  const { kinds: _kinds, ...shared } = settings;
  return shared;
}

function promptRuns(prompt: string): Array<{ runId: string; text: string }> {
  const runs: Array<{ runId: string; text: string }> = [];
  for (const line of prompt.split("\n")) {
    const start = /^run (\S+): outcome /u.exec(line);
    if (start) runs.push({ runId: start[1]!, text: line });
    else if (runs.length > 0) runs[runs.length - 1]!.text += `\n${line}`;
  }
  return runs;
}

function addRule(rationale: string, text: string, runIds: string[], quotes: string[]): string {
  return JSON.stringify({
    rationale,
    operations: [{ op: "add", text, evidenceRunIds: runIds, evidenceQuotes: quotes }],
  });
}

/** Provider-free learner: one generic rule or description per target, grounded in text each run holds. */
export const stubSkillLearnLlm: SkillLearnLlm = async (prompt) => {
  const runs = promptRuns(prompt);
  const runIds = runs.map((run) => run.runId);
  const skill = /^skill: (\S+)$/mu.exec(prompt)?.[1] ?? "";
  const description = /^current description: (.*)$/mu.exec(prompt)?.[1];
  if (description !== undefined) {
    const current = description.trim().replace(/^"(.*)"$/u, "$1").replace(/\.$/u, "");
    return JSON.stringify({
      rationale: `The settled runs opened ${skill} together with a sibling skill.`,
      description: `${current}; requests about neighbouring measures belong to the sibling skills.`,
      evidenceRunIds: runIds,
      evidenceQuotes: [`skills/${skill}/SKILL.md`],
    });
  }
  if (/^data source: /mu.test(prompt)) {
    const tools = [
      ...new Set(runs.flatMap((run) => /^step \d+: (\S*graphjin\S*?)\(/imu.exec(run.text)?.[1] ?? [])),
    ];
    return addRule(
      "Every settled run queried the source through its GraphJin tool.",
      "Find the latest date in the source first, then count each day window back from it with both endpoints included.",
      runIds,
      tools,
    );
  }
  if (/^organization: /mu.test(prompt)) {
    return addRule(
      "Every settled run reported its window next to its values.",
      "Put the time window you used on its own line, next to the current and comparison values.",
      runIds,
      ["Current window:"],
    );
  }
  return addRule(
    `The settled runs all start from the ${skill} skill.`,
    "Open the named skill first, then follow its window and comparison steps in order before the first query.",
    runIds,
    [`skills/${skill}/SKILL.md`],
  );
};

/** The union of targets that the learn runs feed, in learner order. */
export async function collectLearnTargets(
  runIds: readonly string[],
  targetsFor: (runId: string) => Promise<{ targets: LearnTarget[] }>,
): Promise<LearnTarget[]> {
  const targets = new Map<string, LearnTarget>();
  for (const runId of runIds) {
    for (const target of (await targetsFor(runId)).targets) targets.set(learnTargetKey(target), target);
  }
  return [...targets.values()].sort(
    (a, b) =>
      LEARN_TARGET_KINDS.indexOf(a.kind) - LEARN_TARGET_KINDS.indexOf(b.kind) || a.id.localeCompare(b.id),
  );
}

function normalize(text: string): string {
  return text.toLowerCase().replace(/\s+/gu, " ").trim();
}

export function checkRuleGrounding(
  rules: readonly LearnedRuleRecord[],
  evidence: ReadonlyMap<string, string>,
): GroundingVerdict[] {
  return rules.map((rule) => {
    const reasons: string[] = [];
    if (rule.evidenceRunIds.length === 0) reasons.push("no evidence run");
    for (const runId of rule.evidenceRunIds) {
      const digest = evidence.get(runId);
      if (digest === undefined) {
        reasons.push(`run ${runId} is not a learn-split run`);
        continue;
      }
      if (!rule.evidenceQuotes.some((quote) => normalize(digest).includes(normalize(quote)))) {
        reasons.push(`no quote appears in run ${runId}`);
      }
    }
    return { ruleId: rule.id, grounded: reasons.length === 0, reasons };
  });
}

export function placeboBody(rulesMarkdown: string, targetLength: number, maxLines = Infinity): string {
  const lines = rulesMarkdown.split("\n").map((line) => line.trim()).filter((line) => line.startsWith("- "));
  const chosen: string[] = [];
  for (const line of lines) {
    if (chosen.length >= maxLines) break;
    if (chosen.length > 0 && chosen.join("\n").length >= targetLength) break;
    chosen.push(line);
  }
  return chosen.join("\n");
}

/** Variant settings for each arm: learned files for the chosen kinds, length-matched generic text for the placebo. */
export function armInstallPlan(
  artifact: Pick<LearningArtifact, "targets"> | undefined,
  arm: Arm,
  resolvePath: (path: string) => string = (path) => path,
  kinds: readonly LearnTargetKind[] | null = null,
): {
  skill_overlays: Record<string, string>;
  skill_descriptions: Record<string, string>;
  learned_notes: Record<string, string>;
} {
  const plan = {
    skill_overlays: {} as Record<string, string>,
    skill_descriptions: {} as Record<string, string>,
    learned_notes: {} as Record<string, string>,
  };
  if (arm === "baseline" || !artifact) return plan;
  for (const entry of Object.values(artifact.targets)) {
    if (kinds && !kinds.includes(entry.kind)) continue;
    const file = arm === "learned" ? entry.artifact : entry.placebo;
    if (!file) continue;
    const path = resolvePath(file.path);
    if (entry.kind === "skill") plan.skill_overlays[entry.id] = path;
    if (entry.kind === "skill-description") plan.skill_descriptions[entry.id] = path;
    if (entry.kind === "source" || entry.kind === "org") plan.learned_notes[entry.kind] = path;
  }
  return plan;
}

export type CitationSummary = {
  rules: Record<string, { runs: number; passed: number }>;
  citing: { runs: number; passed: number };
  nonCiting: { runs: number; passed: number };
};

/** Per-rule citation counts and the pass rate of citing against non-citing runs. */
export function summarizeCitations(runs: ReadonlyArray<{ passed: boolean; cited: readonly string[] }>): CitationSummary {
  const summary: CitationSummary = { rules: {}, citing: { runs: 0, passed: 0 }, nonCiting: { runs: 0, passed: 0 } };
  for (const run of runs) {
    const group = run.cited.length > 0 ? summary.citing : summary.nonCiting;
    group.runs += 1;
    if (run.passed) group.passed += 1;
    for (const key of new Set(run.cited)) {
      const entry = (summary.rules[key] ??= { runs: 0, passed: 0 });
      entry.runs += 1;
      if (run.passed) entry.passed += 1;
    }
  }
  return summary;
}

export type KindDelivery = { kind: LearnTargetKind; passed: boolean; diagnostic: string };

/** Checks each learned kind against what the run received; baseline checks that nothing learned arrived. */
export function verifyLearnedDelivery(input: {
  learning: LearningOutput;
  staging?: SkillStagingObservation;
  context?: LearnedContextObservation;
}): KindDelivery[] {
  const { learning, staging, context } = input;
  const out: KindDelivery[] = [];
  const result = (kind: LearnTargetKind, failure: string | null) =>
    out.push({ kind, passed: failure === null, diagnostic: failure ?? "ok" });
  const absent = (kind: LearnTargetKind) =>
    learning.arm === "baseline" || (learning.kinds !== undefined && !learning.kinds.includes(kind));
  if (staging) {
    if (absent("skill")) result("skill", staging.hasLearnedSection ? "staged_skill_has_learned_section" : null);
    if (absent("skill-description")) {
      result(
        "skill-description",
        staging.stagedDescriptionDigest === staging.baseDescriptionDigest ? null : "staged_description_differs_from_base",
      );
    }
  }
  if (context) {
    if (absent("source")) result("source", context.sourceNotesBlock ? "prompt_has_learned_data_notes" : null);
    if (absent("org")) result("org", context.orgPlaybookBlock ? "prompt_has_org_playbook" : null);
  }
  if (learning.arm === "baseline") return out;
  if (staging) {
    const overlay = learning.overlays[staging.skill];
    if (overlay) {
      result(
        "skill",
        staging.overlayDigest !== overlay
          ? "installed_overlay_differs_from_arm_artifact"
          : staging.stagedDigest === staging.composedDigest
            ? null
            : staging.hasLearnedSection
              ? "staged_learned_section_differs"
              : "overlay_not_staged",
      );
    }
    const description = learning.descriptions[staging.skill];
    if (description) {
      result(
        "skill-description",
        staging.descriptionDigest !== description
          ? "installed_description_differs_from_arm_artifact"
          : staging.stagedDescriptionDigest === description
            ? null
            : "description_not_staged",
      );
    } else if (learning.arm === "placebo") {
      result(
        "skill-description",
        staging.stagedDescriptionDigest === staging.baseDescriptionDigest ? null : "staged_description_differs_from_base",
      );
    }
  }
  const notes = (kind: "source" | "org", expected: string | null) => {
    if (!expected) return;
    const observed = kind === "source" ? context?.sourceNotes : context?.orgPlaybook;
    result(
      kind,
      !context
        ? "no_prompt_observation"
        : !observed
          ? "notes_not_installed"
          : observed.digest !== expected
            ? "installed_notes_differ_from_arm_artifact"
            : observed.inPrompt
              ? null
              : "notes_not_in_prompt",
    );
  };
  notes("source", learning.sourceNotes);
  notes("org", learning.orgPlaybook);
  return out;
}

function oracleLeakSource(caseId: string, oracle: unknown): OracleLeakSource {
  const value = record(oracle) ?? {};
  return {
    caseId,
    values: [value.expectedValue, value.baselineValue].map(Number).filter(Number.isFinite),
    dates: [value.anchorDate, value.startDate].filter((date): date is string => typeof date === "string"),
  };
}

async function pinFile(from: string, to: string, artifactDir: string, rules = false): Promise<PinnedFile | null> {
  const raw = await readFile(from, "utf8").catch(() => null);
  if (raw === null) return null;
  await mkdir(dirname(to), { recursive: true });
  await writeFile(to, raw, "utf8");
  const content = rules ? raw.trim() : overlayBodyFromFile(raw);
  return { path: relative(artifactDir, to), digest: sha256(content), bytes: Buffer.byteLength(content, "utf8") };
}

async function writePinned(path: string, raw: string, artifactDir: string): Promise<PinnedFile> {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, raw, "utf8");
  const body = overlayBodyFromFile(raw);
  return { path: relative(artifactDir, path), digest: sha256(body), bytes: Buffer.byteLength(body, "utf8") };
}

function appliedFile(raw: string | null): boolean {
  return raw !== null && /^status:\s*applied\s*$/mu.test(/^---\s*\n([\s\S]*?)\n---/u.exec(raw)?.[1] ?? "");
}

function rulesOf(target: LearnTarget, trace: Record<string, unknown>, fallback: unknown): LearnedRuleRecord[] {
  if (target.kind === "skill-description") {
    const applied = Array.isArray(trace.applied) ? (trace.applied as LearnedRuleRecord[]) : [];
    return applied.map((entry, index) => ({
      id: `D${index + 1}`,
      text: entry.text,
      evidenceRunIds: entry.evidenceRunIds ?? [],
      evidenceQuotes: entry.evidenceQuotes ?? [],
    }));
  }
  return (Array.isArray(trace.rules) ? trace.rules : Array.isArray(fallback) ? fallback : []) as LearnedRuleRecord[];
}

async function loadCorpusCases(loaded: LoadedEval, variant: EvalVariant, corpus: string): Promise<LoadedCase[]> {
  const datasets = [...loaded.datasetPaths.entries()].map(([id, path]) => ({
    ref: path,
    snapshot: loaded.datasetSnapshots.get(id)!,
  }));
  const temp = await mkdtemp(join(tmpdir(), "openneko-learning-corpus-"));
  try {
    const configPath = join(temp, "corpus.yaml");
    await writeFile(
      configPath,
      JSON.stringify({
        schema_version: "openneko.eval/v1",
        id: `${loaded.config.id}-corpus`,
        adapter: loaded.config.adapter,
        suite: { ref: corpus },
        datasets,
        defaults: loaded.config.defaults,
        variants: [variant],
        artifacts: loaded.config.artifacts,
      }),
      "utf8",
    );
    return [...(await loadEval(configPath)).cases];
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
}

/** One shared start: every caller awaits the same learning promise. */
export function learningGate(start: () => Promise<void>): () => Promise<void> {
  let pending: Promise<void> | undefined;
  return () => (pending ??= start());
}

/** Learn-split runs go through a bounded pool; the learner starts after every run has settled. */
export async function runLearnStage<J, R>(input: {
  jobs: readonly J[];
  concurrency: number;
  run: (job: J) => Promise<R>;
  learn: (results: R[]) => Promise<void>;
}): Promise<void> {
  const results: R[] = new Array(input.jobs.length);
  await runBoundedPool({
    items: input.jobs,
    concurrency: input.concurrency,
    run: async (job, index) => {
      results[index] = await input.run(job);
    },
  });
  await input.learn(results);
}

export type LearningOutcome = "answer" | "routing" | "task";
const OUTCOMES: readonly LearningOutcome[] = ["answer", "routing", "task"];
const OUTCOME_LABEL: Record<LearningOutcome, string> = {
  answer: "Answer correct",
  routing: "Routing",
  task: "Full task pass",
};
const SPLITS = ["test", "holdout", "control"] as const;
const DECOY_CHECK = /^decoy-\d+-not-loaded$/u;
export const DEFAULT_ROUTING_NOISE = 0.05;

export type EpisodeOutcome = Record<LearningOutcome, boolean> & {
  /** Provider fault type; the episode is left out of rates and pairs. */
  infra: string | null;
  /** Error type of an agent failure that is not a provider fault. */
  failure: string | null;
};

/** Answer: every ground-truth check. Routing: target loaded and every decoy avoided. Task: the full gating verdict. */
export function episodeOutcome(
  episode: Pick<EvalEpisode, "status" | "errorType" | "score">,
): EpisodeOutcome {
  const completed = episode.status === "completed";
  const infra = !completed && isProviderFailureType(episode.errorType) ? episode.errorType! : null;
  const checks = episode.score?.checks ?? [];
  const passed = (id: string) => checks.some((check) => check.assertionId === id && check.passed);
  return {
    answer: completed && checks.filter((check) => check.dimension === "ground_truth").every((check) => check.passed),
    routing:
      completed &&
      passed("skill-loaded") &&
      checks.filter((check) => DECOY_CHECK.test(check.assertionId)).every((check) => check.passed),
    task: completed && episode.score?.verdict === "pass",
    infra,
    failure: completed || infra ? null : (episode.errorType ?? episode.status),
  };
}

export type LearningGate = {
  id: string;
  hypothesis: string;
  rule: string;
  observed: string;
  passed: boolean | null;
};

/** The routing noise floor: the widest bound of the A/A routing CI, else 5 points. */
export function routingNoise(aa?: PairedArmComparison): { value: number; source: string } {
  return aa?.ci95
    ? { value: Math.max(Math.abs(aa.ci95.low), Math.abs(aa.ci95.high)), source: `A/A ${aa.treatment} vs ${aa.control}` }
    : { value: DEFAULT_ROUTING_NOISE, source: "default 5 points (no A/A arm)" };
}

function pctText(value: number): string {
  return `${(100 * value).toFixed(1)}%`;
}

function ciText(comparison?: PairedArmComparison): string {
  return comparison?.ci95
    ? `${pctText(comparison.meanDifference)} [${pctText(comparison.ci95.low)}, ${pctText(comparison.ci95.high)}]`
    : "n/a";
}

/** Shipping gates H1, H2, routing non-inferiority and H5, on the test split. */
export function learningGates(input: {
  answerVsBaseline?: PairedArmComparison;
  routingVsBaseline?: PairedArmComparison;
  answerVsPlacebo?: PairedArmComparison;
  noise: { value: number; source: string };
  leaks: number;
  safetyFailures: number;
}): LearningGate[] {
  const above = (comparison?: PairedArmComparison) => (comparison?.ci95 ? comparison.ci95.low > 0 : null);
  return [
    {
      id: "answer-vs-baseline",
      hypothesis: "H1",
      rule: "learned beats baseline on answer correctness; CI lower bound > 0",
      observed: ciText(input.answerVsBaseline),
      passed: above(input.answerVsBaseline),
    },
    {
      id: "routing-not-worse",
      hypothesis: "routing guard",
      rule: `learned is not worse than baseline on routing; CI lower bound >= -${pctText(input.noise.value)} (${input.noise.source})`,
      observed: ciText(input.routingVsBaseline),
      passed: input.routingVsBaseline?.ci95 ? input.routingVsBaseline.ci95.low >= -input.noise.value - 1e-9 : null,
    },
    {
      id: "answer-vs-placebo",
      hypothesis: "H2",
      rule: "learned beats placebo on answer correctness; CI lower bound > 0",
      observed: ciText(input.answerVsPlacebo),
      passed: above(input.answerVsPlacebo),
    },
    {
      id: "no-leak-no-safety-failure",
      hypothesis: "H5",
      rule: "zero leaked values and zero safety failures in the learned arms",
      observed: `${input.leaks} leaks, ${input.safetyFailures} safety failures`,
      passed: input.leaks === 0 && input.safetyFailures === 0,
    },
  ];
}

export function createSkillLearningDriver(context: { loaded: LoadedEval; plan: EvalPlan }): EvalDriver {
  const { loaded } = context;
  const workspaceRoot = process.env.OPENNEKO_WORKSPACE_ROOT || process.cwd();
  const rawRoot = resolve(workspaceRoot, loaded.config.artifacts.raw_dir);
  const stateRoot = resolve(workspaceRoot, loaded.config.artifacts.state_dir);
  const learningOrgs = new Map<string, string>();
  const inner = createOpenNekoBackendDriver(context, {
    sharedOrgFor: (slot) => learningOrgs.get(slot.key),
  });
  const learning = learningGate(() => (learnedVariant ? runLearning(learnedVariant) : Promise.resolve()));
  let artifact: LearningArtifact | undefined;
  let artifactDir: string | undefined;
  let observedRunId: string | undefined;
  const learnedVariants = loaded.config.variants.filter((variant) => variant.settings?.arm === "learned");
  const learnedVariant =
    learnedVariants.find((variant) => record(variant.settings?.learning)?.kinds === undefined) ?? learnedVariants[0];

  function innerVariant(variant: EvalVariant): EvalVariant {
    const {
      arm: _arm,
      learning: _learning,
      placebo_rules: _placebo,
      placebo_notes: _notes,
      placebo_playbook: _playbook,
      ...settings
    } = variant.settings ?? {};
    return {
      ...variant,
      settings: {
        ...settings,
        ...armInstallPlan(artifact, armOf(variant), (path) => resolve(artifactDir!, path), learningKinds(variant)),
      },
    };
  }

  async function runLearning(variant: EvalVariant): Promise<void> {
    const settings = learningSettings(variant, loaded.configPath);
    const corpusCases = await loadCorpusCases(loaded, variant, settings.corpus);
    const learnCases = corpusCases.filter((evalCase) => evalCase.input.split === "learn");
    if (learnCases.length === 0) {
      throw new EvalEnvironmentError("the learning corpus has no learn-split cases", "corpus_invalid");
    }
    const baselineVariant = innerVariant({ ...variant, settings: { ...variant.settings, arm: "baseline" } });
    const learnerVariant = { ...baselineVariant, settings: { ...baselineVariant.settings, ...settings.runSettings } };
    const placeboVariant = loaded.config.variants.find((candidate) => candidate.settings?.arm === "placebo");
    const fixturePath = (key: string, fallback: string) =>
      resolve(dirname(loaded.configPath), String(placeboVariant?.settings?.[key] ?? `../datasets/openneko-learning/fixtures/${fallback}`));
    const placeboPaths = {
      skill: fixturePath("placebo_rules", "placebo-rules.md"),
      source: fixturePath("placebo_notes", "placebo-data-notes.md"),
      org: fixturePath("placebo_playbook", "placebo-playbook.md"),
    };
    const learnKey = contentDigest({
      artifact: "openneko.eval.skill-learning-artifact/v2",
      cases: learnCases.map((evalCase) => evalCase.contentId),
      measured: loaded.cases.map((evalCase) => evalCase.contentId),
      variant: { backend: variant.backend, model: variant.outer_model, settings: learnerVariant.settings },
      settings: { ...sharedLearning(settings), corpus: relative(dirname(loaded.configPath), settings.corpus) },
      placebo: await Promise.all(Object.values(placeboPaths).map((path) => readFile(path, "utf8"))),
    });
    artifactDir = join(rawRoot, "skill-learning", loaded.config.id, learnKey.slice(7, 23));
    const manifestPath = join(artifactDir, "learning.json");
    const existing = await readFile(manifestPath, "utf8").catch(() => null);
    if (existing) {
      artifact = JSON.parse(existing) as LearningArtifact;
      return;
    }
    await rm(artifactDir, { recursive: true, force: true });

    const orgId = `eval-learn-${learnKey.slice(7, 19)}`;
    const cleanupOrg = async () => {
      await deleteTestOrg(orgId).catch(() => {});
      await rm(getOrgAgentRoot(orgId), { recursive: true, force: true });
    };
    await cleanupOrg();
    const runId = `learn-${learnKey.slice(7, 19)}`;
    const learnRuns: LearningArtifact["learnRuns"] = [];
    try {
      const jobs: Array<{ evalCase: LoadedCase; oracle: unknown; repetition: number }> = [];
      for (const evalCase of learnCases) {
        const oracle = await inner.resolveOracle(evalCase, context);
        for (let repetition = 1; repetition <= settings.repetitions; repetition += 1) {
          jobs.push({ evalCase, oracle, repetition });
        }
      }
      await runLearnStage({
        jobs,
        concurrency: effectiveConcurrency(loaded.config.defaults.concurrency, inner.maxConcurrency),
        run: async ({ evalCase, oracle, repetition }) => {
          const slot: EvalSlot = {
            key: `learn/${evalCase.id}/${repetition}`,
            pairKey: `learn/${evalCase.id}/${repetition}`,
            treatment: "default",
            suiteId: "learn",
            datasetId: evalCase.dataset,
            variantId: "learn",
            caseId: evalCase.id,
            repetition,
            phase: "initial",
            case: evalCase,
            dataset: loaded.datasets.get(evalCase.dataset)!,
            variant: learnerVariant,
          };
          learningOrgs.set(slot.key, orgId);
          const sink = new MemoryObservationSink();
          const observer = createHarnessObserver({ runId, sinks: [sink] });
          let status = "completed";
          let verdict: string | null = null;
          try {
            for (let attempt = 1; ; attempt += 1) {
              try {
                const execution = await inner.execute({
                  ...context,
                  runId,
                  slot,
                  oracle,
                  observer,
                  signal: AbortSignal.timeout(durationMs(evalCase.timeout ?? loaded.config.defaults.timeout)),
                });
                verdict = scoreWorkBackendExecution({ evalCase, oracle: oracle as never, execution, phase: "initial" }).verdict;
                break;
              } catch (cause) {
                if (cause instanceof EvalTaskError) {
                  status = cause.errorType;
                  break;
                }
                if (attempt >= loaded.config.defaults.max_attempts) throw cause;
              }
            }
          } finally {
            await observer.shutdown();
          }
          console.error(`[skill-learning] learn ${evalCase.id}#${repetition}: ${status}${verdict ? ` ${verdict}` : ""}`);
          return { caseId: evalCase.id, repetition, status, verdict };
        },
        learn: async (runs) => {
          learnRuns.push(...runs);
        },
      });

      process.env.OPENNEKO_LEARN_DAILY_PROPOSALS ??= "100000";
      process.env.OPENNEKO_LEARN_DAILY_PROMOTIONS ??= "100000";
      await setSkillLearnOrgEnabled({ orgId, enabled: true });
      const measuredOracles = await Promise.all(
        loaded.cases.map(async (evalCase) =>
          oracleLeakSource(evalCase.id, await inner.resolveOracle(evalCase, context)),
        ),
      );
      const runIds = (
        await db().select({ id: work_run.id }).from(work_run).where(eq(work_run.org_id, orgId)).orderBy(work_run.created_at)
      ).map((row) => row.id);
      let checkFails = 0;
      let disagreed = 0;
      let recomputed = 0;
      for (let i = 0; i < runIds.length; i += 4) {
        const batch = await Promise.all(
          runIds.slice(i, i + 4).map((runId) =>
            verifyRunForLearning({
              orgId,
              runId,
              ...(settings.learner === "stub" ? { llm: async () => null } : {}),
            }),
          ),
        );
        for (const verified of batch) {
          if (verified.checks.some((check) => check.verdict === "fail")) checkFails += 1;
          if (verified.recompute && verified.recompute.verdict !== "unknown") recomputed += 1;
          if (verified.recompute?.verdict === "disagree") disagreed += 1;
        }
      }
      console.log(
        `[skill-learning] verification: ${checkFails}/${runIds.length} runs failed a check; recompute disagreed on ${disagreed}/${recomputed} comparable runs`,
      );
      const targets = await collectLearnTargets(runIds, (id) => learnTargetsForRun(orgId, id));
      const orgRoot = getOrgAgentRoot(orgId);
      const learnedRoot = join(artifactDir, "learned");
      const placeboRoot = join(artifactDir, "placebo");
      const records: LearningArtifact["targets"] = {};
      for (const target of targets) {
        const key = learnTargetKey(target);
        const result = await runLearnForTarget({
          orgId,
          target,
          minRepeats: settings.minRepeats,
          settlementMs: 0,
          verifyTimeoutMs: 0,
          minIntervalMs: 0,
          guardIntervalMs: 0,
          ...(settings.learner === "stub" ? { llm: stubSkillLearnLlm } : {}),
        });
        const [event] = await db()
          .select({ id: skill_learn_event.id, trace: skill_learn_event.model_trace })
          .from(skill_learn_event)
          .where(and(eq(skill_learn_event.org_id, orgId), eq(skill_learn_event.skill_name, key)))
          .orderBy(desc(skill_learn_event.created_at))
          .limit(1);
        const trace = record(event?.trace) ?? {};
        const rules = rulesOf(target, trace, result.rules);
        const evidence = await loadRunEvidence(orgId, [...new Set(rules.flatMap((rule) => rule.evidenceRunIds))]);
        records[key] = {
          kind: target.kind,
          id: target.id,
          decision: result.decision,
          reason: result.reason,
          eventId: event?.id ?? null,
          rules,
          dropped: Array.isArray(trace.dropped) ? trace.dropped : [],
          grounding: checkRuleGrounding(rules, new Map(evidence.map((run) => [run.runId, run.digest]))),
          leaks: findLeakedValues(rules.map((rule) => rule.text), measuredOracles),
          artifact: null,
          rulesFile: null,
          placebo: null,
        };
        console.error(`[skill-learning] learner ${key}: ${result.decision} (${result.reason}); rules ${rules.length}`);
      }
      const placeboText = Object.fromEntries(
        await Promise.all(Object.entries(placeboPaths).map(async ([kind, path]) => [kind, await readFile(path, "utf8")])),
      ) as Record<"skill" | "source" | "org", string>;
      for (const entry of Object.values(records)) {
        const target: LearnTarget = { kind: entry.kind, id: entry.id };
        const file = TARGET_FILE[target.kind];
        const source = join(learnTargetDir(orgRoot, target), file);
        if (!appliedFile(await readFile(source, "utf8").catch(() => null))) continue;
        entry.artifact = await pinFile(source, join(learnTargetDir(learnedRoot, target), file), artifactDir);
        if (target.kind !== "skill-description") {
          entry.rulesFile = await pinFile(
            join(learnTargetDir(orgRoot, target), RULES_FILE),
            join(learnTargetDir(learnedRoot, target), RULES_FILE),
            artifactDir,
            true,
          );
        }
        if (!entry.artifact || target.kind === "skill-description") continue;
        const length = entry.artifact.bytes;
        if (target.kind === "skill") {
          entry.placebo = await writePinned(
            join(learnTargetDir(placeboRoot, target), file),
            `${placeboBody(placeboText.skill, length)}\n`,
            artifactDir,
          );
        } else {
          const body = placeboBody(placeboText[target.kind], length, target.kind === "org" ? ORG_PLAYBOOK_LINES : Infinity);
          await writeLearnedNotes(placeboRoot, { target, status: "applied", body });
          entry.placebo = await pinFile(join(learnTargetDir(placeboRoot, target), file), join(learnTargetDir(placeboRoot, target), file), artifactDir);
        }
      }
      artifact = {
        schemaVersion: "openneko.eval.skill-learning-artifact/v2",
        learnKey,
        learner: settings.learner,
        learnRuns,
        targets: records,
        leaks: Object.values(records).flatMap((entry) => entry.leaks),
      };
      await writeFile(manifestPath, `${JSON.stringify(artifact, null, 2)}\n`, "utf8");
    } finally {
      for (const key of [...learningOrgs.keys()]) learningOrgs.delete(key);
      await cleanupOrg();
    }
  }

  function learningOutput(variant: EvalVariant): LearningOutput {
    const arm = armOf(variant);
    const kinds = learningKinds(variant);
    const entries = Object.values(artifact?.targets ?? {});
    const digest = (kind: LearnTargetKind) =>
      Object.fromEntries(
        entries.flatMap((entry) => {
          if (entry.kind !== kind || (kinds && !kinds.includes(kind))) return [];
          const file = arm === "learned" ? entry.artifact : arm === "placebo" ? entry.placebo : null;
          return file ? [[entry.id, file.digest]] : [];
        }),
      ) as Record<string, string>;
    return {
      arm,
      ...(kinds ? { kinds } : {}),
      overlays: digest("skill"),
      descriptions: digest("skill-description"),
      sourceNotes: Object.values(digest("source"))[0] ?? null,
      orgPlaybook: Object.values(digest("org"))[0] ?? null,
      rulesLearned: entries.reduce((total, entry) => total + entry.rules.length, 0),
      ungroundedRules: entries.flatMap((entry) =>
        entry.grounding.filter((verdict) => !verdict.grounded).map((verdict) => `${learnTargetKey(entry)}:${verdict.ruleId}`),
      ),
      leaks: artifact?.leaks ?? [],
    };
  }

  function extendedCheck(
    assertion: LoadedCase["assertions"][number],
    output: { skillStaging?: SkillStagingObservation; learnedContext?: LearnedContextObservation; learning?: LearningOutput },
    loadedSkill: boolean,
  ): ExtendedAssertionCheck | null {
    const learning = output.learning;
    const arms = Array.isArray(assertion.params?.arms) ? assertion.params.arms.map(String) : [...ARMS];
    if (!learning || !arms.includes(learning.arm)) return null;
    const staging = output.skillStaging;
    if (assertion.kind === "learning.overlay-applied" || assertion.kind === "learning.overlay-absent") {
      if (!staging) return { passed: false, diagnostic: "no_staged_skill_observation" };
      if (!loadedSkill) return { passed: false, diagnostic: "run_did_not_load_skill" };
      if (staging.readDigests.length > 0 && !staging.readDigests.includes(staging.stagedDigest)) {
        return { passed: false, diagnostic: "run_read_content_differs_from_staged_skill" };
      }
      const deliveries = verifyLearnedDelivery({ learning, staging, ...(output.learnedContext ? { context: output.learnedContext } : {}) });
      if (deliveries.length === 0) return null;
      const failed = deliveries.filter((delivery) => !delivery.passed);
      return failed.length === 0
        ? { passed: true, diagnostic: `ok kinds=${deliveries.map((delivery) => delivery.kind).join(",")}` }
        : { passed: false, diagnostic: failed.map((delivery) => `${delivery.kind}:${delivery.diagnostic}`).join(",") };
    }
    if (assertion.kind === "learning.rule-grounded") {
      if (learning.rulesLearned === 0) return { passed: true, diagnostic: "no_learned_rules" };
      return learning.ungroundedRules.length === 0
        ? { passed: true, diagnostic: "ok" }
        : { passed: false, diagnostic: `ungrounded_rules=${learning.ungroundedRules.join(",")}` };
    }
    if (assertion.kind === "learning.no-leak") {
      return learning.leaks.length === 0
        ? { passed: true, diagnostic: learning.rulesLearned === 0 ? "no_learned_rules" : "ok" }
        : { passed: false, diagnostic: `leaked_values=${learning.leaks.map((leak) => leak.value).join(",")}` };
    }
    throw new Error(`unknown learning assertion ${assertion.kind}`);
  }

  async function writeReport(runId: string): Promise<void> {
    const store = new EvalStateStore(stateRoot);
    const variantIds = loaded.config.variants.map((variant) => variant.id);
    const variantArm = new Map(loaded.config.variants.map((variant) => [variant.id, armOf(variant)]));
    const baselineIds = variantIds.filter((id) => variantArm.get(id) === "baseline");
    const baselineId = baselineIds[0];
    const aaId = baselineIds[1];
    const learnedId = learnedVariant?.id;
    const placeboId = variantIds.find((id) => variantArm.get(id) === "placebo");
    const rows: Record<LearningOutcome, ArmTaskOutcome[]> = { answer: [], routing: [], task: [] };
    const counts: Record<string, Record<string, Record<LearningOutcome, { pass: number; total: number }>>> = {};
    const infra: Record<string, Record<string, number>> = {};
    const failures: Record<string, Record<string, number>> = {};
    const safety: Record<string, number> = {};
    const tallies: Record<string, Record<string, { pass: number; total: number }>> = {};
    const delivery: Record<string, Record<string, { pass: number; total: number }>> = {};
    const citationRuns: Array<{ passed: boolean; cited: string[] }> = [];
    const bump = (table: Record<string, Record<string, number>>, variant: string, key: string) => {
      const entry = (table[variant] ??= {});
      entry[key] = (entry[key] ?? 0) + 1;
    };
    const tally = (variant: string, name: string, passed: boolean) => {
      const entry = ((tallies[variant] ??= {})[name] ??= { pass: 0, total: 0 });
      entry.total += 1;
      if (passed) entry.pass += 1;
    };
    for (const slot of context.plan.slots) {
      const episode = await store.readEpisode(runId, slot.key).catch(() => null);
      if (!episode) continue;
      const variant = slot.variantId;
      const split = String(slot.case.input.split ?? "");
      const outcome = episodeOutcome(episode);
      if (outcome.infra) {
        bump(infra, variant, outcome.infra);
        continue;
      }
      if (outcome.failure) bump(failures, variant, outcome.failure);
      const taskId = `${split}/${slot.caseId}`;
      for (const name of OUTCOMES) {
        rows[name].push({ taskId, arm: variant, repetition: slot.repetition, passed: outcome[name] });
        const entry = (((counts[variant] ??= {})[split] ??= {
          answer: { pass: 0, total: 0 },
          routing: { pass: 0, total: 0 },
          task: { pass: 0, total: 0 },
        })[name]);
        entry.total += 1;
        if (outcome[name]) entry.pass += 1;
      }
      const checks = episode.score?.checks ?? [];
      const passed = (id: string) => checks.some((check) => check.assertionId === id && check.passed);
      safety[variant] ??= 0;
      safety[variant] += checks.filter(
        (check) => check.dimension === "safety" && !check.passed && !DECOY_CHECK.test(check.assertionId),
      ).length;
      const discovery = slot.case.input.discovery !== false;
      tally(variant, discovery ? "discovery" : "mention-load", passed("skill-loaded"));
      if (discovery) {
        tally(variant, "decoy-avoided", checks.filter((check) => DECOY_CHECK.test(check.assertionId)).every((check) => check.passed));
      }
      for (const id of ["overlay-applied", "overlay-absent"]) {
        const check = checks.find((candidate) => candidate.assertionId === id);
        if (check) tally(variant, "overlay", check.passed);
      }
      const output = (record(episode.output) ?? {}) as {
        skillStaging?: SkillStagingObservation;
        learnedContext?: LearnedContextObservation;
        learning?: LearningOutput;
      };
      if (episode.status === "completed" && output.learning) {
        for (const result of verifyLearnedDelivery({
          learning: output.learning,
          ...(output.skillStaging ? { staging: output.skillStaging } : {}),
          ...(output.learnedContext ? { context: output.learnedContext } : {}),
        })) {
          const entry = ((delivery[variant] ??= {})[result.kind] ??= { pass: 0, total: 0 });
          entry.total += 1;
          if (result.passed) entry.pass += 1;
        }
      }
      if (variant === learnedId) {
        citationRuns.push({
          passed: outcome.task,
          cited: (output.learnedContext?.citedRuleKeys ?? []).map((key) =>
            key.replace(/^source:[^:]+:/u, "source:default:"),
          ),
        });
      }
    }
    const citations = summarizeCitations(citationRuns);
    const compare = (split: string, name: LearningOutcome, treatment: string, control: string) =>
      pairedArmComparison(
        rows[name].filter((row) => row.taskId.startsWith(`${split}/`)),
        treatment,
        control,
        `${split}/${name}/${treatment}/${control}`,
      );
    const comparisons: Record<string, Record<string, Record<LearningOutcome, PairedArmComparison>>> = {};
    const pairs: Array<[string, string]> = [
      ...variantIds.filter((id) => baselineId && id !== baselineId).map((id): [string, string] => [id, baselineId!]),
      ...(learnedId && placeboId ? [[learnedId, placeboId] as [string, string]] : []),
    ];
    for (const [treatment, control] of pairs) {
      const key = `${treatment} vs ${control}`;
      comparisons[key] = Object.fromEntries(
        SPLITS.map((split) => [
          split,
          Object.fromEntries(OUTCOMES.map((name) => [name, compare(split, name, treatment, control)])),
        ]),
      ) as Record<string, Record<LearningOutcome, PairedArmComparison>>;
    }
    const versus = (treatment?: string, control?: string) =>
      treatment && control ? comparisons[`${treatment} vs ${control}`]?.test : undefined;
    const noise = routingNoise(versus(aaId, baselineId)?.routing);
    const learnedIds = variantIds.filter((id) => variantArm.get(id) === "learned");
    const gates = learningGates({
      answerVsBaseline: versus(learnedId, baselineId)?.answer,
      routingVsBaseline: versus(learnedId, baselineId)?.routing,
      answerVsPlacebo: versus(learnedId, placeboId)?.answer,
      noise,
      leaks: artifact?.leaks.length ?? 0,
      safetyFailures: learnedIds.reduce((total, id) => total + (safety[id] ?? 0), 0),
    });
    const shipped = gates.every((gate) => gate.passed === true);
    const report = {
      schemaVersion: "openneko.eval.skill-learning-report/v3",
      runId,
      configId: loaded.config.id,
      learnKey: artifact?.learnKey ?? null,
      arms: Object.fromEntries(
        loaded.config.variants.map((variant) => [
          variant.id,
          { arm: armOf(variant), kinds: learningKinds(variant) ?? (armOf(variant) === "learned" ? "all" : null) },
        ]),
      ),
      gates: { verdict: shipped ? "pass" : "fail", gates },
      outcomes: counts,
      infraFailures: infra,
      agentFailures: failures,
      safetyFailures: safety,
      learnStage: artifact
        ? {
            runs: artifact.learnRuns.length,
            passed: artifact.learnRuns.filter((run) => run.verdict === "pass").length,
            targets: Object.fromEntries(
              Object.entries(artifact.targets).map(([key, entry]) => [
                key,
                {
                  kind: entry.kind,
                  id: entry.id,
                  decision: entry.decision,
                  reason: entry.reason,
                  rules: entry.rules.length,
                  dropped: entry.dropped.length,
                  grounded: entry.grounding.filter((verdict) => verdict.grounded).length,
                  leaks: entry.leaks.length,
                  artifactDigest: entry.artifact?.digest ?? null,
                },
              ]),
            ),
            leaks: artifact.leaks.length,
            leaksByKind: Object.fromEntries(
              LEARN_TARGET_KINDS.map((kind) => [
                kind,
                Object.values(artifact!.targets)
                  .filter((entry) => entry.kind === kind)
                  .reduce((total, entry) => total + entry.leaks.length, 0),
              ]),
            ),
          }
        : null,
      rates: tallies,
      delivery,
      citations,
      comparisons,
    };
    const pct = pctText;
    const rate = (entry?: { pass: number; total: number }) =>
      entry && entry.total ? `${entry.pass}/${entry.total} (${pct(entry.pass / entry.total)})` : "n/a";
    const line = (label: string, comparison: PairedArmComparison) =>
      `| ${label} | ${comparison.tasks} | ${pct(comparison.treatmentPassRate)} | ${pct(comparison.controlPassRate)} | ${pct(comparison.meanDifference)} | ${
        comparison.ci95 ? `${pct(comparison.ci95.low)} to ${pct(comparison.ci95.high)}` : "n/a"
      } |`;
    const failureText = (entry?: Record<string, number>) =>
      entry && Object.keys(entry).length
        ? Object.entries(entry)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([type, count]) => `${type} ${count}`)
            .join(", ")
        : "none";
    const learnedVsBaseline = versus(learnedId, baselineId);
    const markdown = [
      `# Skill learning report: ${loaded.config.id}`,
      "",
      `Run ${runId}. Learn key ${artifact?.learnKey ?? "none"}.`,
      "",
      `## Shipping gates: ${shipped ? "PASS" : "FAIL"}`,
      "",
      `Test split, ${learnedId ?? "learned"} against ${baselineId ?? "baseline"} and ${placeboId ?? "placebo"}. Each cell is the mean paired difference with its bootstrap 95% CI.`,
      "",
      "| Gate | Rule | Observed | Result |",
      "| --- | --- | --- | --- |",
      ...gates.map(
        (gate) =>
          `| ${gate.hypothesis} ${gate.id} | ${gate.rule} | ${gate.observed} | ${gate.passed === null ? "n/a" : gate.passed ? "PASS" : "FAIL"} |`,
      ),
      "",
      "Answer correct: every ground-truth check passes. Routing: the target skill loaded and every decoy stayed unloaded. Full task pass: every gating check, including the answer contract and the query path.",
      "",
      "Provider faults (provider_http_*, provider_blocked) count as missing data. They leave both the rates and the pairs; a task stays in a pair while each arm keeps one valid repetition. Agent failures count as fails on all three outcomes.",
      "",
      "## Outcomes per arm, test split",
      "",
      "| Arm | Kinds | Answer correct | Routing | Full task pass | Provider faults | Agent failures | Safety failures |",
      "| --- | --- | --- | --- | --- | --- | --- | ---: |",
      ...variantIds.map((id) => {
        const test = counts[id]?.test;
        return `| ${id} | ${report.arms[id]?.kinds ?? "-"} | ${rate(test?.answer)} | ${rate(test?.routing)} | ${rate(test?.task)} | ${failureText(infra[id])} | ${failureText(failures[id])} | ${safety[id] ?? 0} |`;
      }),
      "",
      "## Paired comparisons",
      "",
      "| Comparison | Split | Outcome | Tasks | Treatment | Control | Mean paired difference | Bootstrap 95% CI |",
      "| --- | --- | --- | ---: | ---: | ---: | ---: | --- |",
      ...Object.entries(comparisons).flatMap(([key, bySplit]) =>
        SPLITS.flatMap((split) =>
          OUTCOMES.flatMap((name) =>
            bySplit[split]![name].tasks > 0 ? [line(`${key} | ${split} | ${OUTCOME_LABEL[name]}`, bySplit[split]![name])] : [],
          ),
        ),
      ),
      "",
      "| Arm | Discovery (target skill loaded) | Decoys avoided | Skill loaded when named | Overlay check |",
      "| --- | --- | --- | --- | --- |",
      ...Object.entries(tallies).map(
        ([arm, entry]) =>
          `| ${arm} | ${rate(entry.discovery)} | ${rate(entry["decoy-avoided"])} | ${rate(entry["mention-load"])} | ${rate(entry.overlay)} |`,
      ),
      "",
      ...(report.learnStage
        ? [
            `Learn stage: ${report.learnStage.passed}/${report.learnStage.runs} runs passed. Leaked values: ${report.learnStage.leaks}.`,
            "",
            "| Kind | Target | Decision | Rules | Grounded | Dropped | Leaks |",
            "| --- | --- | --- | ---: | ---: | ---: | ---: |",
            ...Object.values(report.learnStage.targets).map(
              (entry) =>
                `| ${entry.kind} | ${entry.id} | ${entry.decision}${entry.decision === "applied" ? "" : ` (${entry.reason})`} | ${entry.rules} | ${entry.grounded} | ${entry.dropped} | ${entry.leaks} |`,
            ),
            "",
            `Leaked values by kind: ${Object.entries(report.learnStage.leaksByKind).map(([kind, count]) => `${kind} ${count}`).join(", ")}.`,
            "",
          ]
        : []),
      "Learned content that reached the run, per kind (staged skill body and description, prompt notes and playbook). Kinds an arm leaves out are checked for absence:",
      "",
      "| Arm | Skill rules | Description | Data notes | Org playbook |",
      "| --- | --- | --- | --- | --- |",
      ...Object.entries(delivery).map(
        ([arm, entry]) =>
          `| ${arm} | ${rate(entry.skill)} | ${rate(entry["skill-description"])} | ${rate(entry.source)} | ${rate(entry.org)} |`,
      ),
      "",
      `Learned-arm rule citations: ${citations.citing.runs} citing runs pass ${rate({ pass: citations.citing.passed, total: citations.citing.runs })}; ${citations.nonCiting.runs} non-citing runs pass ${rate({ pass: citations.nonCiting.passed, total: citations.nonCiting.runs })}.`,
      "",
      ...(Object.keys(citations.rules).length
        ? [
            "| Rule | Citing runs | Passed |",
            "| --- | ---: | ---: |",
            ...Object.entries(citations.rules)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([key, entry]) => `| ${key} | ${entry.runs} | ${entry.passed} |`),
            "",
          ]
        : []),
      ...(learnedVsBaseline
        ? [
            `Per task, test split, ${learnedId} against ${baselineId} (answer, routing, full task):`,
            "",
            ...learnedVsBaseline.answer.perTask.map((task) => {
              const of = (name: LearningOutcome) => learnedVsBaseline[name].perTask.find((entry) => entry.taskId === task.taskId);
              const cell = (name: LearningOutcome) => {
                const entry = of(name);
                return entry ? `${pct(entry.treatmentPassRate)}/${pct(entry.controlPassRate)}` : "n/a";
              };
              return `- ${task.taskId}: answer ${cell("answer")}, routing ${cell("routing")}, task ${cell("task")}`;
            }),
            "",
          ]
        : []),
    ].join("\n");
    const directory = store.runDir(runId);
    await mkdir(directory, { recursive: true });
    await writeFile(join(directory, "learning-report.json"), `${JSON.stringify(report, null, 2)}\n`, "utf8");
    await writeFile(join(directory, "learning-report.md"), markdown, "utf8");
    console.error(`[skill-learning] report ${join(directory, "learning-report.md")}`);
  }

  return {
    id: SKILL_LEARNING_ADAPTER,
    scorer: SCORER,
    ...(inner.maxConcurrency !== undefined ? { maxConcurrency: inner.maxConcurrency } : {}),
    async preflight(driverContext) {
      for (const variant of loaded.config.variants) armOf(variant);
      if (loaded.cases.some((evalCase) => !["test", "holdout", "control"].includes(String(evalCase.input.split)))) {
        throw new EvalEnvironmentError(
          "the measured suite may contain only test, holdout and control siblings; learn and verify siblings run inside the learning stage",
          "suite_invalid",
        );
      }
      if (loaded.config.variants.some((variant) => armOf(variant) !== "baseline") && !learnedVariant) {
        throw new EvalEnvironmentError("learned and placebo arms need one learned variant", "variant_invalid");
      }
      if (learnedVariant) {
        const shared = contentDigest(sharedLearning(learningSettings(learnedVariant, loaded.configPath)));
        for (const variant of learnedVariants) {
          if (contentDigest(sharedLearning(learningSettings(variant, loaded.configPath))) !== shared) {
            throw new EvalEnvironmentError(
              `learned variant ${variant.id} must share the learning settings of ${learnedVariant.id}; only kinds may differ`,
              "variant_invalid",
            );
          }
        }
      }
      const preflight = await inner.preflight(driverContext);
      await learning();
      return {
        datasetFingerprint: preflight.datasetFingerprint,
        resolvedVariants: Object.fromEntries(
          loaded.config.variants.map((variant) => [
            variant.id,
            {
              ...(record(preflight.resolvedVariants?.[variant.id]) ?? {}),
              arm: armOf(variant),
              learnKey: artifact?.learnKey ?? null,
              learned: (({ overlays, descriptions, sourceNotes, orgPlaybook }) => ({
                overlays,
                descriptions,
                sourceNotes,
                orgPlaybook,
              }))(learningOutput(variant)),
            },
          ]),
        ),
      };
    },
    resolveOracle(evalCase, driverContext) {
      return inner.resolveOracle(evalCase, driverContext);
    },
    async execute(slotContext) {
      observedRunId = slotContext.runId;
      const arm = armOf(slotContext.slot.variant);
      if (arm !== "baseline") await learning();
      const execution = await inner.execute({
        ...slotContext,
        slot: { ...slotContext.slot, variant: innerVariant(slotContext.slot.variant) },
      });
      const output = record(execution.output) ?? {};
      return {
        ...execution,
        output: {
          ...output,
          learning: learningOutput(slotContext.slot.variant),
        },
      } satisfies EvalExecution;
    },
    score({ case: evalCase, oracle, execution, phase }) {
      const output = (record(execution.output) ?? {}) as {
        skillStaging?: SkillStagingObservation;
        learnedContext?: LearnedContextObservation;
        learning?: LearningOutput;
      };
      const skill = String(evalCase.input.skill ?? "");
      const loadedSkill = (execution.semanticEvidence?.events ?? []).some(
        (event) =>
          event.operation === "skill.loaded" &&
          event.status === "ok" &&
          record(event.evidence)?.id === skill,
      );
      const score = scoreWorkBackendExecution({
        evalCase,
        oracle: oracle as never,
        execution,
        phase,
        extend: (assertion) => extendedCheck(assertion, output, loadedSkill),
      });
      return { ...score, scorerId: SCORER.id, scorerVersion: SCORER.version, scorerDigest: contentDigest(SCORER) };
    },
    async close() {
      try {
        if (observedRunId) await writeReport(observedRunId);
      } finally {
        await inner.close?.();
      }
    },
  };
}
