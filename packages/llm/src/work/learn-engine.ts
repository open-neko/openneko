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
  type SkillLearnLlm,
} from "./skill-learn-propose";
import {
  assertAdditiveLearnedBody,
  checkLearnedDescription,
  checkOrgRule,
  MAX_DESCRIPTION_CHARS,
  scanLearnedText,
} from "./skill-learn-scan";
import { readLearnedDescription, writeLearnedDescription } from "./skill-overlay";

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

async function learnDescription(
  ctx: TargetLearnContext,
  evidence: RunEvidence[],
  baseTrace: SkillLearnTrace,
): Promise<SkillLearnResult> {
  const skillName = ctx.target.id;
  const dir = learnTargetDir(ctx.orgRoot, ctx.target);
  const raw = await callLearnLlm(
    ctx.orgId,
    buildDescriptionLearnPrompt({
      skillName,
      description: ctx.currentDescription ?? "",
      catalog: ctx.catalog ?? [],
      evidence,
      maxChars: MAX_DESCRIPTION_CHARS,
    }),
    ctx.llm,
  );
  const proposal = parseDescriptionProposal(raw, evidence.map((run) => run.runId));
  if (!proposal) {
    return skip("no_proposal", { ...baseTrace, skipReason: "proposer kept the description" });
  }
  const description = proposal.description;
  const trace: SkillLearnTrace = {
    ...baseTrace,
    lesson: `describe ${description.slice(0, 200)}`,
    rationale: proposal.rationale,
  };
  const hits = scanLearnedText(description);
  if (hits.length > 0) {
    return { decision: "rejected", reason: hits.map((hit) => hit.detail).join("; "), trace };
  }
  const invalid = checkLearnedDescription(description);
  if (invalid) return { decision: "skipped", reason: "no_grounded_rules", trace: { ...trace, skipReason: invalid } };
  if ((await readRejectedAt(dir)).includes(normalizeRuleText(description))) {
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
  if (normalizeRuleText(description) === normalizeRuleText(ctx.currentDescription ?? "")) {
    return skip("no_change", { ...trace, skipReason: "description is unchanged" });
  }
  const before = await readLearnedDescription(ctx.orgRoot, skillName);
  await writeLearnedDescription(ctx.orgRoot, {
    skillName,
    baseHash: ctx.skillBaseHash!,
    status: "applied",
    description,
  });
  return {
    decision: "applied",
    reason: "applied grounded description",
    diff: learnedDiff(before?.description ?? ctx.currentDescription ?? "", description),
    trace: {
      ...trace,
      evidenceRunIds: grounding.runIds,
      contentHash: learnedBodyHash(description),
      applied: [{ op: "update", text: description, evidenceRunIds: grounding.runIds, evidenceQuotes: grounding.quotes }],
    },
  };
}
