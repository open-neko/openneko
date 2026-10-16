import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { RunEvidence } from "./skill-learn-evidence";
import {
  batchGate,
  cohortDigest,
  collectEvidence,
  countOutcomes,
  DEFAULT_MIN_REPEATS,
  DEFAULT_SETTLEMENT_MS,
  learnedBodyHash,
  learnedDiff,
  settledCohort,
  skip,
  summarizeOperations,
  type LearnBrake,
  type LearnUsage,
  type SkillLearnResult,
  type SkillLearnTrace,
} from "./skill-learn";
import {
  LEARN_TARGET_POLICY,
  learnTargetDir,
  readLearnedNotes,
  renderKeyedRules,
  writeLearnedNotes,
  type LearnTarget,
} from "./learn-target";
import {
  applyPlaybookOperations,
  collectAnswerNumbers,
  groundEvidence,
  leakedNumbers,
  normalizeRuleText,
  readRejectedAt,
  readRulesAt,
  writeRulesAt,
} from "./skill-learn-playbook";
import {
  buildDescriptionLearnPrompt,
  buildTargetLearnPrompt,
  callLearnLlm,
  parseDescriptionProposal,
  parseSkillLearnProposal,
  type DescriptionCandidate,
  type SkillLearnLlm,
} from "./skill-learn-propose";
import {
  assertAdditiveLearnedBody,
  checkDescriptionSupported,
  checkLearnedDescription,
  checkOrgRule,
  descriptionKey,
  descriptionTerms,
  MAX_DESCRIPTION_CHARS,
  scanLearnedText,
  unquoteDescription,
} from "./skill-learn-scan";
import { readLearnedDescription, stripLearnedSection, writeLearnedDescription } from "./skill-overlay";
import { fingerprintSkillTree } from "./workspace";

/** Catalog siblings whose guides the description proposer also reads. */
export const DESCRIPTION_SIBLINGS = 3;

export type TargetLearnContext = {
  orgId: string;
  orgRoot: string;
  target: LearnTarget;
  /** What the proposer calls the target, such as the data source name. */
  label?: string;
  orgEnabled: boolean;
  targetEnabled: boolean;
  /** contentHash holds the target version each run saw. */
  usages: LearnUsage[];
  currentVersion: string;
  now?: Date;
  minRepeats?: number;
  settlementMs?: number;
  minIntervalMs?: number;
  lastAttemptAt?: Date | null;
  brake?: LearnBrake;
  loadEvidence?: (runIds: string[]) => Promise<RunEvidence[]>;
  llm?: SkillLearnLlm;
  /** skill-description only. */
  skillBaseHash?: string;
  currentDescription?: string;
  catalog?: ReadonlyArray<{ name: string; description: string }>;
  /** Locks another skill's description target before the engine writes it; false skips the write. */
  claimSkill?: (skillName: string) => Promise<boolean>;
};

/** One engine for the source, org and skill-description targets; skill rules use runSkillLearn. */
export async function runTargetLearn(ctx: TargetLearnContext): Promise<SkillLearnResult> {
  const now = ctx.now ?? new Date();
  const minRepeats = ctx.minRepeats ?? DEFAULT_MIN_REPEATS;
  const policy = LEARN_TARGET_POLICY[ctx.target.kind];
  if (!ctx.orgEnabled || !ctx.targetEnabled) {
    return skip("flags_off", { skipReason: "org or target learning is off" });
  }
  if (ctx.target.kind === "skill-description" && !ctx.skillBaseHash) {
    return skip("missing_skill", { skipReason: "skill directory is missing" });
  }
  const cohort = settledCohort(
    ctx.usages,
    ctx.currentVersion,
    now,
    ctx.settlementMs ?? DEFAULT_SETTLEMENT_MS,
  );
  const gated = batchGate({
    cohort,
    minRepeats,
    lastAttemptAt: ctx.lastAttemptAt,
    minIntervalMs: ctx.minIntervalMs ?? policy.minIntervalMs,
    now,
    contentHash: ctx.currentVersion,
    versionLabel: `${ctx.target.kind} version`,
  });
  if (gated) return gated;

  const evidence = await collectEvidence(ctx.loadEvidence, cohort);
  const outcomes = countOutcomes(evidence);
  if (evidence.length < minRepeats) {
    return skip("insufficient_evidence", {
      skipReason: `evidence for ${evidence.length} < ${minRepeats} settled runs`,
      evidenceRunIds: evidence.map((run) => run.runId),
      contentHash: ctx.currentVersion,
      outcomes,
    });
  }
  const digest = cohortDigest(evidence);
  const braked = await ctx.brake?.({ cohortDigest: digest });
  if (braked) {
    return skip(braked, {
      skipReason: braked,
      evidenceRunIds: evidence.map((run) => run.runId),
      contentHash: ctx.currentVersion,
    });
  }
  const baseTrace: SkillLearnTrace = {
    cohortDigest: digest,
    contentHash: ctx.currentVersion,
    ...(ctx.skillBaseHash ? { baseHash: ctx.skillBaseHash } : {}),
    evidenceRunIds: evidence.map((run) => run.runId),
    outcomes,
  };
  return ctx.target.kind === "skill-description"
    ? learnDescription(ctx, evidence, baseTrace)
    : learnRules(ctx, evidence, baseTrace);
}

async function learnRules(
  ctx: TargetLearnContext,
  evidence: RunEvidence[],
  baseTrace: SkillLearnTrace,
): Promise<SkillLearnResult> {
  const kind = ctx.target.kind as "source" | "org";
  const policy = LEARN_TARGET_POLICY[kind];
  const dir = learnTargetDir(ctx.orgRoot, ctx.target);
  const current = await readRulesAt(dir);
  const raw = await callLearnLlm(
    ctx.orgId,
    buildTargetLearnPrompt({
      kind,
      label: ctx.label ?? ctx.target.id,
      rules: current,
      evidence,
      maxRules: policy.maxRules,
    }),
    ctx.llm,
  );
  const proposal = parseSkillLearnProposal(raw, evidence.map((run) => run.runId));
  if (!proposal) {
    return skip("no_proposal", { ...baseTrace, skipReason: "proposer returned no operations" });
  }
  const update = applyPlaybookOperations({
    current,
    operations: proposal.operations,
    evidence,
    rejected: await readRejectedAt(dir),
    maxRules: policy.maxRules,
    minSupport: policy.minSupport,
    ...(kind === "org" ? { check: checkOrgRule } : {}),
  });
  const trace: SkillLearnTrace = {
    ...baseTrace,
    lesson: summarizeOperations(update.applied),
    rationale: proposal.rationale,
    rules: update.rules,
    applied: update.applied,
    dropped: update.dropped,
    ...(update.unsafeOperations.length > 0 ? { unsafe: update.unsafeOperations } : {}),
  };
  if (update.unsafe.length > 0) {
    return { decision: "rejected", reason: update.unsafe.join("; "), trace };
  }
  if (update.applied.length === 0) {
    return { decision: "skipped", reason: "no_grounded_rules", trace };
  }
  const body = renderKeyedRules(ctx.target, update.rules);
  const status = update.rules.length > 0 ? "applied" : "disabled";
  if (status === "applied") {
    const scanError = assertAdditiveLearnedBody(body);
    if (scanError) return { decision: "rejected", reason: scanError, trace };
  }
  const existing = await readLearnedNotes(ctx.orgRoot, ctx.target);
  await writeLearnedNotes(ctx.orgRoot, { target: ctx.target, status, body });
  await writeRulesAt(dir, update.rules);
  return {
    decision: "applied",
    reason: "applied grounded playbook update",
    rules: update.rules,
    diff: learnedDiff(existing?.body ?? "", body),
    trace: { ...trace, contentHash: learnedBodyHash(body) },
  };
}

async function readSkillGuide(orgRoot: string, skillName: string): Promise<string> {
  const raw = await readFile(join(orgRoot, "skills", skillName, "SKILL.md"), "utf8").catch(() => "");
  return stripLearnedSection(raw.replace(/^---\s*\n[\s\S]*?\n---\s*\n?/, "")).trim();
}

function requestText(evidence: readonly RunEvidence[]): string {
  return evidence
    .flatMap((run) => run.digest.split("\n").filter((line) => line.startsWith("request: ")))
    .join("\n");
}

/** The queued skill and the catalog siblings whose descriptions share the most terms with the runs' requests. */
export function descriptionCandidateNames(input: {
  skillName: string;
  catalog: ReadonlyArray<{ name: string; description: string }>;
  requests: string;
  limit?: number;
}): string[] {
  const asked = new Set(descriptionTerms(input.requests));
  const scored = input.catalog
    .filter((skill) => skill.name !== input.skillName)
    .map((skill) => ({
      name: skill.name,
      score: new Set(descriptionTerms(`${skill.name.replace(/[-_]/g, " ")} ${skill.description}`).filter((term) => asked.has(term))).size,
    }))
    .filter((skill) => skill.score > 0)
    .sort((a, b) => b.score - a.score || a.name.localeCompare(b.name));
  return [input.skillName, ...scored.slice(0, input.limit ?? DESCRIPTION_SIBLINGS).map((skill) => skill.name)];
}

/**
 * The proposer picks the skill whose guide serves the runs' requests and
 * improves that skill's description with terms its guide supports.
 */
async function learnDescription(
  ctx: TargetLearnContext,
  evidence: RunEvidence[],
  baseTrace: SkillLearnTrace,
): Promise<SkillLearnResult> {
  const queued = ctx.target.id;
  const catalog = ctx.catalog ?? [];
  const describe = (name: string) =>
    unquoteDescription(
      name === queued && ctx.currentDescription !== undefined
        ? ctx.currentDescription
        : (catalog.find((skill) => skill.name === name)?.description ?? ""),
    );
  const candidates: DescriptionCandidate[] = await Promise.all(
    descriptionCandidateNames({ skillName: queued, catalog, requests: requestText(evidence) }).map(async (name) => ({
      name,
      description: describe(name),
      guide: await readSkillGuide(ctx.orgRoot, name),
    })),
  );
  const raw = await callLearnLlm(
    ctx.orgId,
    buildDescriptionLearnPrompt({ skillName: queued, candidates, evidence, maxChars: MAX_DESCRIPTION_CHARS }),
    ctx.llm,
  );
  const proposal = parseDescriptionProposal(raw, evidence.map((run) => run.runId));
  if (!proposal) {
    return skip("no_proposal", { ...baseTrace, skipReason: "proposer kept the descriptions" });
  }
  const description = proposal.description;
  const trace: SkillLearnTrace = {
    ...baseTrace,
    lesson: `describe ${proposal.skill ?? queued}: ${description.slice(0, 200)}`,
    rationale: proposal.rationale,
  };
  const chosen = candidates.find((candidate) => candidate.name === (proposal.skill ?? queued));
  if (!chosen) {
    return { decision: "skipped", reason: "no_grounded_rules", trace: { ...trace, skipReason: `proposer chose ${proposal.skill}, outside the candidate skills` } };
  }
  const target: LearnTarget = { kind: "skill-description", id: chosen.name };
  const hits = scanLearnedText(description);
  if (hits.length > 0) {
    return { decision: "rejected", reason: hits.map((hit) => hit.detail).join("; "), trace };
  }
  const invalid =
    checkLearnedDescription(description) ??
    checkDescriptionSupported({ description, currentDescription: chosen.description, skillGuide: chosen.guide });
  if (invalid) return { decision: "skipped", reason: "no_grounded_rules", trace: { ...trace, skipReason: invalid } };
  if ((await readRejectedAt(learnTargetDir(ctx.orgRoot, target))).includes(normalizeRuleText(description))) {
    return { decision: "skipped", reason: "no_grounded_rules", trace: { ...trace, skipReason: "description was rolled back earlier" } };
  }
  const leaked = leakedNumbers(description, collectAnswerNumbers(evidence));
  if (leaked.length > 0) {
    return { decision: "skipped", reason: "no_grounded_rules", trace: { ...trace, skipReason: `description repeats answer values: ${leaked.join(", ")}` } };
  }
  const grounding = groundEvidence(proposal, new Map(evidence.map((run) => [run.runId, run])));
  const minSupport = LEARN_TARGET_POLICY["skill-description"].minSupport;
  if (grounding.runIds.length < minSupport) {
    return {
      decision: "skipped",
      reason: "no_grounded_rules",
      trace: { ...trace, skipReason: `grounded in ${grounding.runIds.length} run(s); needs ${minSupport}` },
    };
  }
  if (descriptionKey(description) === descriptionKey(chosen.description)) {
    return skip("no_change", { ...trace, skipReason: "description is unchanged" });
  }
  const other = chosen.name !== queued;
  const baseHash = other
    ? await fingerprintSkillTree(join(ctx.orgRoot, "skills", chosen.name)).catch(() => undefined)
    : ctx.skillBaseHash;
  if (!baseHash) return skip("missing_skill", { ...trace, skipReason: `skill ${chosen.name} is missing` });
  if (other && ctx.claimSkill && !(await ctx.claimSkill(chosen.name))) {
    return skip("target_busy", { ...trace, skipReason: `description of ${chosen.name} is being learned` });
  }
  const before = await readLearnedDescription(ctx.orgRoot, chosen.name);
  await writeLearnedDescription(ctx.orgRoot, { skillName: chosen.name, baseHash, status: "applied", description });
  return {
    decision: "applied",
    reason: "applied grounded description",
    diff: learnedDiff(before?.description ?? chosen.description, description),
    ...(other ? { target } : {}),
    trace: {
      ...trace,
      baseHash,
      evidenceRunIds: grounding.runIds,
      contentHash: learnedBodyHash(description),
      applied: [{ op: "update", text: description, evidenceRunIds: grounding.runIds, evidenceQuotes: grounding.quotes }],
    },
  };
}
