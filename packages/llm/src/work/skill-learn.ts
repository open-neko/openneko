import { createHash } from "node:crypto";
import { join } from "node:path";
import { runWentWrong, type RunEvidence } from "./skill-learn-evidence";
import { renderKeyedRules } from "./learn-target";
import {
  applyPlaybookOperations,
  readLearnedRules,
  readRejectedRules,
  writeLearnedRules,
  type DroppedOperation,
  type LearnedRule,
  type PlaybookOperation,
} from "./skill-learn-playbook";
import { assertAdditiveLearnedBody } from "./skill-learn-scan";
import {
  fingerprintEffectiveSkill,
  readLearnedOverlay,
  writeLearnedOverlay,
  type LearnedOverlay,
} from "./skill-overlay";
import { proposeSkillLearn } from "./skill-learn-propose";
import { fingerprintSkillTree } from "./workspace";

export const DEFAULT_MIN_REPEATS = 5;
/** A run with no follow-up and no other settling event settles after this idle time. */
export const DEFAULT_SETTLEMENT_MS = 10 * 60 * 1000;
export const MAX_EVIDENCE_RUNS = 20;

export type SkillLearnDecision =
  | "applied"
  | "skipped"
  | "stale"
  | "rejected"
  | "rolled_back";

export type SkillLearnProposal = {
  rationale: string;
  operations: PlaybookOperation[];
};

export type SkillLearnTrace = {
  /** Set only when the proposer ran; the store keys idempotency on it. */
  cohortDigest?: string;
  lesson?: string;
  rationale?: string;
  evidenceRunIds?: string[];
  contentHash?: string;
  baseHash?: string;
  tokenUsage?: { input?: number; output?: number };
  skipReason?: string;
  rules?: LearnedRule[];
  applied?: PlaybookOperation[];
  dropped?: DroppedOperation[];
  outcomes?: Record<string, number>;
};

export type SkillLearnResult = {
  decision: SkillLearnDecision;
  reason: string;
  overlay?: LearnedOverlay;
  rules?: LearnedRule[];
  trace: SkillLearnTrace;
  diff?: string;
};

export type SkillUsageSnapshot = {
  runId: string;
  skillName: string;
  contentHash: string;
  createdAt: Date;
  /** When the run's outcome became final: a follow-up, completion of an unattended run, or the idle limit. */
  settledAt?: Date | null;
};

export type SkillLearnContext = {
  orgId: string;
  orgRoot: string;
  skillName: string;
  orgEnabled: boolean;
  skillEnabled: boolean;
  usages: SkillUsageSnapshot[];
  now?: Date;
  minRepeats?: number;
  settlementMs?: number;
  currentBaseHash?: string;
  currentEffectiveHash?: string;
  /** The last time the proposer ran for this skill; learning waits for minRepeats newer runs. */
  lastAttemptAt?: Date | null;
  /** Loop-safety brakes checked just before the proposer runs; a reason skips the attempt. */
  brake?: LearnBrake;
  loadEvidence?: (runIds: string[]) => Promise<RunEvidence[]>;
  propose?: (input: {
    skillName: string;
    baseHash: string;
    rules: LearnedRule[];
    evidence: RunEvidence[];
  }) => Promise<SkillLearnProposal | null>;
  llm?: import("./skill-learn-propose").SkillLearnLlm;
};

export async function runSkillLearn(
  ctx: SkillLearnContext,
): Promise<SkillLearnResult> {
  const now = ctx.now ?? new Date();
  const minRepeats = ctx.minRepeats ?? DEFAULT_MIN_REPEATS;
  const settlementMs = ctx.settlementMs ?? DEFAULT_SETTLEMENT_MS;
  const skillDir = join(ctx.orgRoot, "skills", ctx.skillName);

  if (!ctx.orgEnabled || !ctx.skillEnabled) {
    return skip("flags_off", { skipReason: "org or skill learning is off" });
  }

  let baseHash: string;
  let effectiveHash: string;
  try {
    baseHash = ctx.currentBaseHash ?? (await fingerprintSkillTree(skillDir));
    effectiveHash =
      ctx.currentEffectiveHash ??
      (await fingerprintEffectiveSkill(ctx.orgRoot, ctx.skillName, skillDir));
  } catch {
    return skip("missing_skill", { skipReason: "skill directory is missing" });
  }

  const cohort = settledCohort(ctx.usages, effectiveHash, now, settlementMs);
  const gated = batchGate({
    cohort,
    minRepeats,
    lastAttemptAt: ctx.lastAttemptAt,
    now,
    contentHash: effectiveHash,
    versionLabel: "skill version",
  });
  if (gated) return gated;

  const existing = await readLearnedOverlay(ctx.orgRoot, ctx.skillName);
  if (existing && existing.baseHash !== baseHash) {
    return {
      decision: "stale",
      reason: "existing overlay base_hash does not match current base",
      trace: { baseHash, contentHash: effectiveHash },
    };
  }

  const evidence = await collectEvidence(ctx.loadEvidence, cohort);
  const outcomes = countOutcomes(evidence);
  if (evidence.length < minRepeats) {
    return skip("insufficient_evidence", {
      skipReason: `evidence for ${evidence.length} < ${minRepeats} settled runs`,
      evidenceRunIds: evidence.map((run) => run.runId),
      baseHash,
      contentHash: effectiveHash,
      outcomes,
    });
  }
  const digest = cohortDigest(evidence);
  const braked = await ctx.brake?.({ cohortDigest: digest });
  if (braked) {
    return skip(braked, {
      skipReason: braked,
      evidenceRunIds: evidence.map((run) => run.runId),
      baseHash,
      contentHash: effectiveHash,
    });
  }

  const current = await readLearnedRules(ctx.orgRoot, ctx.skillName);
  const rejected = await readRejectedRules(ctx.orgRoot, ctx.skillName);
  const proposal = ctx.propose
    ? await ctx.propose({ skillName: ctx.skillName, baseHash, rules: current, evidence })
    : await proposeSkillLearn({
        orgId: ctx.orgId,
        orgRoot: ctx.orgRoot,
        skillName: ctx.skillName,
        rules: current,
        evidence,
        llm: ctx.llm,
      });
  const baseTrace: SkillLearnTrace = {
    cohortDigest: digest,
    baseHash,
    contentHash: effectiveHash,
    evidenceRunIds: evidence.map((run) => run.runId),
    outcomes,
  };
  if (!proposal) {
    return skip("no_proposal", { ...baseTrace, skipReason: "proposer returned no operations" });
  }

  const update = applyPlaybookOperations({
    current,
    operations: proposal.operations,
    evidence,
    rejected,
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

  const body = renderKeyedRules({ kind: "skill", id: ctx.skillName }, update.rules);
  const overlay: LearnedOverlay = {
    skillName: ctx.skillName,
    baseHash,
    status: update.rules.length > 0 ? "applied" : "disabled",
    body,
  };
  if (overlay.status === "applied") {
    const scanError = assertAdditiveLearnedBody(body);
    if (scanError) return { decision: "rejected", reason: scanError, trace };
  }
  await writeLearnedOverlay(ctx.orgRoot, overlay);
  await writeLearnedRules(ctx.orgRoot, ctx.skillName, update.rules);
  return {
    decision: "applied",
    reason: "applied grounded playbook update",
    overlay,
    rules: update.rules,
    diff: learnedDiff(existing?.body ?? "", body),
    trace,
  };
}

export type LearnBrake = (input: { cohortDigest: string }) => Promise<string | null>;

export type LearnUsage = {
  runId: string;
  contentHash: string;
  createdAt: Date;
  settledAt?: Date | null;
};

/** Settled runs on one version, newest first. */
export function settledCohort<T extends LearnUsage>(
  usages: readonly T[],
  version: string,
  now: Date,
  settlementMs: number,
): T[] {
  return usages
    .filter((usage) =>
      usage.settledAt !== undefined
        ? usage.settledAt !== null && usage.settledAt.getTime() <= now.getTime()
        : now.getTime() - usage.createdAt.getTime() >= settlementMs,
    )
    .filter((usage) => usage.contentHash === version)
    .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime());
}

/** The batch trigger: enough settled runs, enough new ones since the last attempt, and a quiet interval. */
export function batchGate(input: {
  cohort: readonly LearnUsage[];
  minRepeats: number;
  lastAttemptAt?: Date | null;
  minIntervalMs?: number;
  now: Date;
  contentHash: string;
  versionLabel: string;
}): SkillLearnResult | null {
  const { cohort, minRepeats } = input;
  if (cohort.length < minRepeats) {
    return skip("below_repeat_threshold", {
      skipReason: `settled ${cohort.length} < ${minRepeats} on the current ${input.versionLabel}`,
      evidenceRunIds: cohort.map((usage) => usage.runId),
      contentHash: input.contentHash,
    });
  }
  if (!input.lastAttemptAt) return null;
  const since = input.lastAttemptAt.getTime();
  const fresh = cohort.filter((usage) => usage.createdAt.getTime() > since).length;
  if (fresh < minRepeats) {
    return skip("waiting_for_new_runs", {
      skipReason: `${fresh} < ${minRepeats} new settled runs since the last attempt`,
      contentHash: input.contentHash,
    });
  }
  const interval = input.minIntervalMs ?? 0;
  if (interval > 0 && input.now.getTime() - since < interval) {
    return skip("min_interval", {
      skipReason: `last attempt was under ${Math.round(interval / 60000)} minutes ago`,
      contentHash: input.contentHash,
    });
  }
  return null;
}

export async function collectEvidence(
  loadEvidence: ((runIds: string[]) => Promise<RunEvidence[]>) | undefined,
  cohort: readonly LearnUsage[],
): Promise<RunEvidence[]> {
  const load = loadEvidence ?? (async () => []);
  return sampleEvidence(
    (await load(cohort.slice(0, MAX_EVIDENCE_RUNS * 3).map((usage) => usage.runId))).filter(
      (run) => run.outcome !== "unknown" && run.outcome !== "cancelled",
    ),
  );
}

/** Identifies the evidence a proposal would see, so one cohort never reaches the proposer twice. */
export function cohortDigest(evidence: readonly RunEvidence[]): string {
  return createHash("sha256")
    .update(evidence.map((run) => run.runId).sort().join("\n"))
    .digest("hex");
}

/** Newest first, with up to half the sample reserved for runs that went wrong. */
export function sampleEvidence(
  runs: readonly RunEvidence[],
  limit = MAX_EVIDENCE_RUNS,
): RunEvidence[] {
  const bad = runs.filter(runWentWrong).slice(0, Math.ceil(limit / 2));
  const rest = runs.filter((run) => !bad.includes(run)).slice(0, limit - bad.length);
  const picked = new Set([...bad, ...rest]);
  return runs.filter((run) => picked.has(run));
}

export function countOutcomes(evidence: readonly RunEvidence[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const run of evidence) {
    counts[run.outcome] = (counts[run.outcome] ?? 0) + 1;
    if (run.verifiedWrong) counts.verified_wrong = (counts.verified_wrong ?? 0) + 1;
  }
  return counts;
}

export function summarizeOperations(operations: readonly PlaybookOperation[]): string {
  return operations
    .map((operation) =>
      operation.op === "remove"
        ? `remove ${operation.ruleId}`
        : `${operation.op} ${(operation.text ?? "").slice(0, 80)}`,
    )
    .join("; ")
    .slice(0, 500);
}

export function learnedBodyHash(body: string): string {
  return createHash("sha256").update(body).digest("hex");
}

export function skip(reason: string, trace: SkillLearnTrace): SkillLearnResult {
  return { decision: "skipped", reason, trace };
}

export function learnedDiff(before: string, after: string): string {
  return `--- before\n${before}\n+++ after\n${after}\n`;
}
