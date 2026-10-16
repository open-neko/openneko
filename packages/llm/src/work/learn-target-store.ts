import { join } from "node:path";
import {
  and,
  asc,
  data_source,
  db,
  desc,
  eq,
  gte,
  inArray,
  isNotNull,
  learned_rule_use,
  ne,
  run_store_version,
  run_verification,
  skill_learn_event,
  skill_learn_org,
  skill_learn_state,
  skill_usage,
  sql,
  work_message,
  work_run,
  work_run_event,
  work_thread,
} from "@neko/db";
import { recordConfigChange } from "../config-vcs";
import { runTargetLearn } from "./learn-engine";
import { recordLearningEventQuietly, utcDay } from "./learn-events";
import { recordResultLessons } from "./learn-lessons";
import {
  GRAPHJIN_TOOL_PATTERN,
  LEARNING_THREAD_CHANNEL,
  extractRuleCitations,
  learnTargetDir,
  learnTargetKey,
  learnTargetVcsPath,
  parseRuleKey,
  readLearnedNotes,
  renderKeyedRules,
  writeLearnedNotes,
  type LearnTarget,
} from "./learn-target";
import {
  DEFAULT_MIN_REPEATS,
  DEFAULT_SETTLEMENT_MS,
  learnedBodyHash,
  settledCohort,
  type LearnBrake,
  type LearnUsage,
  type SkillLearnResult,
} from "./skill-learn";
import { loadRunEvidence, type RunEvidence } from "./skill-learn-evidence";
import {
  readRulesAt,
  rejectTextsAt,
  writeRulesAt,
  type LearnedRule,
} from "./skill-learn-playbook";
import type { SkillLearnLlm } from "./skill-learn-propose";
import { learnedVersionRegressed, scoreSkillLearnWindow } from "./skill-learn-score";
import { DEFAULT_VERIFY_TIMEOUT_MS } from "./skill-learn-verify";
import {
  readLearnedDescription,
  readLearnedOverlay,
  writeLearnedDescription,
  writeLearnedOverlay,
} from "./skill-overlay";
import { fingerprintSkillTree, getOrgAgentRoot, listInstalledSkills } from "./workspace";

export { GRAPHJIN_TOOL_PATTERN };
const EVIDENCE_WINDOW_MS = 30 * 24 * 60 * 60 * 1000;
const MAX_TARGET_RUNS = 500;
const DAY_MS = 24 * 60 * 60 * 1000;
export const DEFAULT_DAILY_PROPOSALS = 48;
export const DEFAULT_DAILY_PROMOTIONS = 12;
/** A rule needs this many citing runs before its own outcomes can roll it back. */
export const MIN_RULE_CITATIONS = 3;
const RULE_REGRESSION_MARGIN = 0.3;

const GUARD_INTERVAL_MS = 15 * 60 * 1000;
const guardRuns = new Map<string, number>();

/** Rollback guards load run evidence, so each worker runs them at most once per interval per target. */
export function guardDue(
  orgId: string,
  target: LearnTarget,
  now: Date,
  intervalMs = GUARD_INTERVAL_MS,
): boolean {
  const key = `${orgId}\0${target.kind}\0${target.id}`;
  const last = guardRuns.get(key);
  if (last !== undefined && now.getTime() - last < intervalMs) return false;
  guardRuns.set(key, now.getTime());
  return true;
}

type Tx = Parameters<Parameters<ReturnType<typeof db>["transaction"]>[0]>[0];

/**
 * A run settles at the first of: the operator's next message in the thread,
 * completion of an unattended (service) run, or the idle limit after it ends.
 * A run sampled for recomputation also waits until that job finishes or times out.
 * Unfinished runs never settle.
 */
export function runSettledAt(idleMs: number, verifyTimeoutMs = DEFAULT_VERIFY_TIMEOUT_MS) {
  // A sub-run settles with its root task.
  return sql<string | null>`case
    when "work_run"."finished_at" is null then null
    when "work_run"."root_run_id" is not null then (select ${settledAtFor("root", idleMs, verifyTimeoutMs)}
      from ${work_run} root where root.id = "work_run"."root_run_id")
    else ${settledAtFor("work_run", idleMs, verifyTimeoutMs)} end`;
}

function settledAtFor(alias: string, idleMs: number, verifyTimeoutMs: number) {
  const idle = `${Math.max(0, Math.round(idleMs / 1000))} seconds`;
  const verifyTimeout = `${Math.max(0, Math.round(verifyTimeoutMs / 1000))} seconds`;
  // Drizzle leaves selected columns unqualified on a single-table select, so the
  // subqueries name the run alias explicitly instead of binding to their own tables.
  const run = (column: "id" | "thread_id" | "finished_at" | "actor_role") => sql.raw(`"${alias}"."${column}"`);
  return sql<string | null>`case when ${run("finished_at")} is null then null else greatest(
    least(
      (select min(m.created_at) from work_message m
        where m.thread_id = ${run("thread_id")} and m.role = 'user'
          and m.created_at > ${run("finished_at")}),
      case when ${run("actor_role")} = 'service' then ${run("finished_at")} end,
      ${run("finished_at")} + ${idle}::interval
    ),
    (select least(case when v.verdict <> 'pending' then v.updated_at end,
        ${run("finished_at")} + ${verifyTimeout}::interval)
      from ${run_verification} v
      where v.run_id = ${run("id")} and v.kind = 'recompute'
      limit 1)
  ) end`;
}

export const notLearningThread = sql`not exists (select 1 from ${work_thread} t
  where t.id = ${work_run.thread_id} and t.channel = ${LEARNING_THREAD_CHANNEL})`;

function targetWhere(target: LearnTarget) {
  return and(
    eq(skill_learn_event.target_kind, target.kind),
    eq(skill_learn_event.target_id, target.id),
  );
}

/** The last time the proposer ran for a target; skipped gates do not count. */
export async function lastLearnAttemptAt(orgId: string, target: LearnTarget): Promise<Date | null> {
  const [row] = await db()
    .select({ createdAt: skill_learn_event.created_at })
    .from(skill_learn_event)
    .where(
      and(
        eq(skill_learn_event.org_id, orgId),
        targetWhere(target),
        sql`(${skill_learn_event.decision} in ('applied', 'rejected', 'rolled_back')
          or ${skill_learn_event.reason} in ('no_proposal', 'no_grounded_rules', 'no_change', 'described_other_skill'))`,
      ),
    )
    .orderBy(desc(skill_learn_event.created_at))
    .limit(1);
  return row?.createdAt ?? null;
}

function envLimit(name: string, fallback: number): number {
  const value = Number(process.env[name]);
  return Number.isFinite(value) && value >= 0 ? Math.floor(value) : fallback;
}

/** Idempotency per target and cohort, a daily proposer budget and a daily promotion cap per org. */
export function learningBrake(orgId: string, target: LearnTarget, now = new Date()): LearnBrake {
  return async ({ cohortDigest }) => {
    const [seen] = await db()
      .select({ id: skill_learn_event.id })
      .from(skill_learn_event)
      .where(
        and(
          eq(skill_learn_event.org_id, orgId),
          targetWhere(target),
          eq(skill_learn_event.cohort_digest, cohortDigest),
        ),
      )
      .limit(1);
    if (seen) return "duplicate_cohort";
    const since = new Date(now.getTime() - DAY_MS);
    const [counts] = await db()
      .select({
        proposals: sql<number>`count(*) filter (where ${skill_learn_event.cohort_digest} is not null)::int`,
        promotions: sql<number>`count(*) filter (where ${skill_learn_event.decision} = 'applied')::int`,
      })
      .from(skill_learn_event)
      .where(and(eq(skill_learn_event.org_id, orgId), gte(skill_learn_event.created_at, since)));
    const hit =
      (counts?.proposals ?? 0) >= envLimit("OPENNEKO_LEARN_DAILY_PROPOSALS", DEFAULT_DAILY_PROPOSALS)
        ? "daily_learning_budget"
        : (counts?.promotions ?? 0) >= envLimit("OPENNEKO_LEARN_DAILY_PROMOTIONS", DEFAULT_DAILY_PROMOTIONS)
          ? "daily_promotion_cap"
          : null;
    if (hit) {
      await recordLearningEventQuietly({
        orgId,
        kind: "budget_hit",
        key: utcDay(now),
        title: "Learning reached its daily limit.",
        body:
          hit === "daily_learning_budget"
            ? "OpenNeko reached today's limit on rule proposals. It starts again tomorrow."
            : "OpenNeko reached today's limit on new rules. It starts again tomorrow.",
        mood: "watch",
        now,
      });
    }
    return hit;
  };
}

export async function insertLearnEvent(
  tx: Tx,
  orgId: string,
  target: LearnTarget,
  result: SkillLearnResult,
): Promise<string> {
  const versioned = result.decision === "applied" || result.decision === "rolled_back";
  const [row] = await tx.insert(skill_learn_event).values({
    org_id: orgId,
    skill_name: learnTargetKey(target),
    target_kind: target.kind,
    target_id: target.id,
    cohort_digest: result.trace.cohortDigest ?? null,
    base_hash: result.trace.baseHash ?? null,
    content_hash: result.trace.contentHash ?? null,
    run_ids: result.trace.evidenceRunIds ?? [],
    lesson: result.trace.lesson ?? null,
    rationale: result.trace.rationale ?? null,
    diff: result.diff ?? null,
    model_trace: versioned && result.rules ? { ...result.trace, versionRules: result.rules } : result.trace,
    decision: result.decision,
    reason: result.reason,
  }).returning({ id: skill_learn_event.id });
  return row!.id;
}

export async function upsertLearnState(
  tx: Tx,
  orgId: string,
  target: LearnTarget,
  hashes: { base: string | null; learned: string },
): Promise<void> {
  await tx
    .insert(skill_learn_state)
    .values({
      org_id: orgId,
      skill_name: learnTargetKey(target),
      target_kind: target.kind,
      target_id: target.id,
      enabled: true,
      current_base_hash: hashes.base,
      current_learned_hash: hashes.learned,
    })
    .onConflictDoUpdate({
      target: [skill_learn_state.org_id, skill_learn_state.skill_name],
      set: {
        target_kind: target.kind,
        target_id: target.id,
        current_base_hash: hashes.base,
        current_learned_hash: hashes.learned,
        updated_at: new Date(),
      },
    });
}

type CitedRuleRate = { rule: LearnedRule; key: string; citedBadRate: number; otherBadRate: number };

function citedRuleRates(input: {
  target: LearnTarget;
  rules: readonly LearnedRule[];
  evidence: readonly RunEvidence[];
  citations: ReadonlyArray<{ runId: string; ruleKey: string }>;
  minCitations?: number;
}): CitedRuleRate[] {
  const minCitations = input.minCitations ?? MIN_RULE_CITATIONS;
  const bad = (run: RunEvidence) => run.outcome === "failed" || Boolean(run.followUp) || Boolean(run.verifiedWrong);
  const rate = (runs: readonly RunEvidence[]) =>
    runs.length === 0 ? 0 : runs.filter(bad).length / runs.length;
  const out: CitedRuleRate[] = [];
  for (const rule of input.rules) {
    const key = `${input.target.kind}:${input.target.id}:${rule.id}`;
    const citing = new Set(input.citations.filter((c) => c.ruleKey === key).map((c) => c.runId));
    const cited = input.evidence.filter((run) => citing.has(run.runId));
    const others = input.evidence.filter((run) => !citing.has(run.runId));
    if (cited.length < minCitations || others.length < minCitations) continue;
    out.push({ rule, key, citedBadRate: rate(cited), otherBadRate: rate(others) });
  }
  return out;
}

/** Rules whose citing runs fail or get corrected clearly more often than comparable runs that did not cite them. */
export function citedRulesToRollBack(input: Parameters<typeof citedRuleRates>[0]): CitedRuleRate[] {
  return citedRuleRates(input).filter((r) => r.citedBadRate - r.otherBadRate >= RULE_REGRESSION_MARGIN);
}

/** Rules whose citing runs go wrong clearly less often than comparable runs. */
export function citedRulesImproved(input: Parameters<typeof citedRuleRates>[0]): CitedRuleRate[] {
  return citedRuleRates(input).filter((r) => r.otherBadRate - r.citedBadRate >= RULE_REGRESSION_MARGIN);
}

/** Writes a target's kept rules and its rendered store; returns the skill overlay when the target is a skill. */
export async function writeTargetRules(
  orgRoot: string,
  target: LearnTarget,
  kept: readonly LearnedRule[],
): Promise<{ body: string; overlay?: SkillLearnResult["overlay"] }> {
  const dir = learnTargetDir(orgRoot, target);
  await writeRulesAt(dir, kept);
  const body = renderKeyedRules(target, kept);
  const status = kept.length > 0 ? ("applied" as const) : ("disabled" as const);
  if (target.kind === "skill") {
    const current = await readLearnedOverlay(orgRoot, target.id);
    if (!current) return { body };
    const overlay = { ...current, status, body: kept.length > 0 ? body : current.body };
    await writeLearnedOverlay(orgRoot, overlay);
    return { body, overlay };
  }
  await writeLearnedNotes(orgRoot, { target, status, body });
  return { body };
}

function percent(rate: number): string {
  return `${Math.round(rate * 100)}%`;
}

/** Puts regressed rules on the blocklist alone; the rest of the version stays. */
export async function rollBackCitedRules(input: {
  orgId: string;
  orgRoot: string;
  target: LearnTarget;
  cohortRunIds: readonly string[];
}): Promise<SkillLearnResult | null> {
  if (input.target.kind === "skill-description" || input.cohortRunIds.length === 0) return null;
  const dir = learnTargetDir(input.orgRoot, input.target);
  const rules = await readRulesAt(dir);
  if (rules.length === 0) return null;
  const runIds = input.cohortRunIds.slice(0, 200);
  const citations = (
    await db()
      .select({ runId: learned_rule_use.run_id, ruleKey: learned_rule_use.rule_key })
      .from(learned_rule_use)
      .where(
        and(
          eq(learned_rule_use.org_id, input.orgId),
          eq(learned_rule_use.target_kind, input.target.kind),
          eq(learned_rule_use.target_id, input.target.id),
          inArray(learned_rule_use.run_id, [...runIds]),
        ),
      )
  );
  if (citations.length < MIN_RULE_CITATIONS) return null;
  const evidence = await loadRunEvidence(input.orgId, runIds);
  const culprits = citedRulesToRollBack({ target: input.target, rules, evidence, citations });
  if (culprits.length === 0) {
    for (const helped of citedRulesImproved({ target: input.target, rules, evidence, citations })) {
      await recordLearningEventQuietly({
        orgId: input.orgId,
        kind: "outcome_change",
        key: helped.key,
        title: "A learned rule is helping.",
        body: `Runs that used rule ${helped.key} went wrong ${percent(helped.citedBadRate)} of the time. Other runs went wrong ${percent(helped.otherBadRate)} of the time. The rule: ${helped.rule.text}`,
        mood: "good",
      });
    }
    return null;
  }

  const dropped = new Set(culprits.map((culprit) => culprit.rule.id));
  const kept = rules.filter((rule) => !dropped.has(rule.id));
  await rejectTextsAt(dir, culprits.map((culprit) => culprit.rule.text));
  const { body, overlay } = await writeTargetRules(input.orgRoot, input.target, kept);
  return {
    decision: "rolled_back",
    reason: culprits
      .map((c) => `rule ${c.key} bad rate ${c.citedBadRate.toFixed(2)} vs ${c.otherBadRate.toFixed(2)}`)
      .join("; "),
    ...(overlay ? { overlay } : {}),
    rules: kept,
    trace: {
      ...(overlay ? { baseHash: overlay.baseHash } : {}),
      contentHash: learnedBodyHash(body),
      evidenceRunIds: [...runIds],
      rules: culprits.map((culprit) => culprit.rule),
    },
  };
}

type VersionEvent = { id: string; createdAt: Date; decision: string };

/**
 * Each run saw the version it pinned at start. A run with no pin saw the
 * version the latest apply or rollback before its task started produced.
 */
export function assignTargetVersions<
  T extends { createdAt: Date; versionAt?: Date | null; pinnedVersion?: string | null },
>(runs: readonly T[], versions: readonly VersionEvent[]): Array<T & { contentHash: string }> {
  const ordered = [...versions].sort((a, b) => a.createdAt.getTime() - b.createdAt.getTime());
  return runs.map((run) => {
    if (run.pinnedVersion) return { ...run, contentHash: run.pinnedVersion };
    const at = (run.versionAt ?? run.createdAt).getTime();
    let version = "v0";
    for (const event of ordered) {
      if (event.createdAt.getTime() <= at) version = event.id;
      else break;
    }
    return { ...run, contentHash: version };
  });
}

async function targetVersions(orgId: string, target: LearnTarget): Promise<VersionEvent[]> {
  return db()
    .select({
      id: skill_learn_event.id,
      createdAt: skill_learn_event.created_at,
      decision: skill_learn_event.decision,
    })
    .from(skill_learn_event)
    .where(
      and(
        eq(skill_learn_event.org_id, orgId),
        targetWhere(target),
        inArray(skill_learn_event.decision, ["applied", "rolled_back"]),
      ),
    )
    .orderBy(asc(skill_learn_event.created_at));
}

/** Settled-run candidates for a target, newest first. */
export async function targetRuns(input: {
  orgId: string;
  target: LearnTarget;
  settlementMs: number;
  verifyTimeoutMs?: number;
  since?: Date;
  now?: Date;
}): Promise<
  Array<{ runId: string; createdAt: Date; settledAt: Date | null; versionAt: Date; pinnedVersion: string | null }>
> {
  const now = input.now ?? new Date();
  const conditions = [
    eq(work_run.org_id, input.orgId),
    isNotNull(work_run.finished_at),
    gte(work_run.created_at, input.since ?? new Date(now.getTime() - EVIDENCE_WINDOW_MS)),
    notLearningThread,
  ];
  if (input.target.kind === "source") {
    conditions.push(sql`exists (select 1 from ${work_run_event} e
      where e.run_id = ${work_run.id} and e.kind = 'tool_start'
        and e.payload->>'name' ~* ${GRAPHJIN_TOOL_PATTERN})`);
  }
  if (input.target.kind === "skill-description") {
    conditions.push(sql`exists (select 1 from ${skill_usage} su
      where su.run_id = ${work_run.id} and su.skill_name = ${input.target.id})`);
    conditions.push(sql`(${work_run.status} = 'failed'
      or (select count(*) from ${skill_usage} su2 where su2.run_id = ${work_run.id}) >= 2
      or exists (select 1 from ${work_run_event} e2 where e2.run_id = ${work_run.id}
        and e2.kind = 'tool_end' and e2.payload ? 'error')
      or exists (select 1 from ${work_message} m where m.thread_id = ${work_run.thread_id}
        and m.role = 'user' and m.created_at > ${work_run.finished_at})
      or exists (select 1 from ${run_verification} v where v.run_id = ${work_run.id}
        and v.verdict in ('fail', 'disagree')))`);
  }
  const rows = await db()
    .select({
      runId: work_run.id,
      createdAt: work_run.created_at,
      settledAt: runSettledAt(input.settlementMs, input.verifyTimeoutMs),
      versionAt: sql<string>`coalesce((select root.created_at from ${work_run} root
        where root.id = "work_run"."root_run_id"), "work_run"."created_at")`,
      pinnedVersion: sql<string | null>`(select v.version from ${run_store_version} v
        where v.run_id = "work_run"."id" and v.target_kind = ${input.target.kind}
          and v.target_id = ${input.target.id})`,
    })
    .from(work_run)
    .where(and(...conditions))
    .orderBy(desc(work_run.created_at))
    .limit(MAX_TARGET_RUNS);
  return rows.map((row) => ({
    runId: row.runId,
    createdAt: row.createdAt,
    settledAt: row.settledAt ? new Date(row.settledAt) : null,
    versionAt: new Date(row.versionAt),
    pinnedVersion: row.pinnedVersion ?? null,
  }));
}

export function toScoreSample(run: RunEvidence) {
  return {
    skillName: "",
    hadCorrection: Boolean(run.followUp) || run.toolErrors > 0,
    hadRelevantFailure: run.outcome === "failed" || Boolean(run.verifiedWrong),
    tokens: 0,
    latencyMs: 0,
  };
}

/** The version guard: the last apply made outcomes worse than the version before it. */
async function rollBackRegressedTarget(input: {
  orgId: string;
  orgRoot: string;
  target: LearnTarget;
  usages: LearnUsage[];
  versions: VersionEvent[];
  minRepeats: number;
  settlementMs: number;
  now: Date;
}): Promise<SkillLearnResult | null> {
  const last = input.versions.at(-1);
  if (!last || last.decision !== "applied") return null;
  const previous = input.versions.at(-2)?.id ?? "v0";
  const before = settledCohort(input.usages, previous, input.now, input.settlementMs);
  const after = settledCohort(input.usages, last.id, input.now, input.settlementMs);
  if (before.length < input.minRepeats || after.length < input.minRepeats) return null;
  const [beforeRuns, afterRuns] = await Promise.all([
    loadRunEvidence(input.orgId, before.map((usage) => usage.runId)),
    loadRunEvidence(input.orgId, after.map((usage) => usage.runId)),
  ]);
  const beforeScore = scoreSkillLearnWindow(beforeRuns.map(toScoreSample));
  const afterScore = scoreSkillLearnWindow(afterRuns.map(toScoreSample));
  if (!learnedVersionRegressed(beforeScore, afterScore)) return null;

  const dir = learnTargetDir(input.orgRoot, input.target);
  let rejected: LearnedRule[] = [];
  if (input.target.kind === "skill-description") {
    const learned = await readLearnedDescription(input.orgRoot, input.target.id);
    if (!learned || learned.status !== "applied") return null;
    await rejectTextsAt(dir, [learned.description]);
    await writeLearnedDescription(input.orgRoot, { ...learned, status: "disabled" });
  } else {
    const notes = await readLearnedNotes(input.orgRoot, input.target);
    if (!notes || notes.status !== "applied") return null;
    rejected = await readRulesAt(dir);
    await rejectTextsAt(dir, rejected.map((rule) => rule.text));
    await writeRulesAt(dir, []);
    await writeLearnedNotes(input.orgRoot, { ...notes, status: "disabled" });
  }
  return {
    decision: "rolled_back",
    reason: `failure rate ${beforeScore.failureRate.toFixed(2)} -> ${afterScore.failureRate.toFixed(2)}, correction rate ${beforeScore.correctionRate.toFixed(2)} -> ${afterScore.correctionRate.toFixed(2)}`,
    rules: [],
    trace: {
      contentHash: `${last.id}:disabled`,
      evidenceRunIds: after.map((usage) => usage.runId),
      rules: rejected,
    },
  };
}

async function defaultSource(orgId: string, sourceId: string) {
  const [row] = await db()
    .select({ id: data_source.id, name: data_source.name, label: data_source.label, createdAt: data_source.created_at })
    .from(data_source)
    .where(and(eq(data_source.org_id, orgId), eq(data_source.id, sourceId)))
    .limit(1);
  return row ?? null;
}

/** Learning for the source, org and skill-description targets. */
export async function runNonSkillTargetLearn(input: {
  orgId: string;
  target: LearnTarget;
  minRepeats?: number;
  settlementMs?: number;
  verifyTimeoutMs?: number;
  minIntervalMs?: number;
  guardIntervalMs?: number;
  llm?: SkillLearnLlm;
  now?: Date;
}): Promise<SkillLearnResult> {
  const { orgId, target } = input;
  const now = input.now ?? new Date();
  const orgRoot = getOrgAgentRoot(orgId);
  const minRepeats = input.minRepeats ?? DEFAULT_MIN_REPEATS;
  const settlementMs = input.settlementMs ?? DEFAULT_SETTLEMENT_MS;
  const key = learnTargetKey(target);
  const [orgRow] = await db()
    .select({ enabled: skill_learn_org.enabled })
    .from(skill_learn_org)
    .where(eq(skill_learn_org.org_id, orgId))
    .limit(1);
  const [stateRow] = await db()
    .select({ enabled: skill_learn_state.enabled })
    .from(skill_learn_state)
    .where(and(eq(skill_learn_state.org_id, orgId), eq(skill_learn_state.skill_name, key)))
    .limit(1);

  let label: string | undefined;
  let since: Date | undefined;
  let skillBaseHash: string | undefined;
  let currentDescription: string | undefined;
  let catalog: Array<{ name: string; description: string }> | undefined;
  if (target.kind === "source") {
    const source = await defaultSource(orgId, target.id);
    if (!source) {
      return finish(orgId, orgRoot, target, { decision: "skipped", reason: "missing_source", trace: { skipReason: "data source is gone" } });
    }
    label = source.label || source.name;
    since = source.createdAt;
  }
  if (target.kind === "skill-description") {
    const skillsRoot = join(orgRoot, "skills");
    skillBaseHash = await fingerprintSkillTree(join(skillsRoot, target.id)).catch(() => undefined);
    catalog = await listInstalledSkills(skillsRoot);
    currentDescription = catalog.find((skill) => skill.name === target.id)?.description;
  }

  const runs = await targetRuns({ orgId, target, settlementMs, verifyTimeoutMs: input.verifyTimeoutMs, since, now });
  const versions = await targetVersions(orgId, target);
  const usages = assignTargetVersions(runs, versions);
  const currentVersion = versions.at(-1)?.id ?? "v0";
  const lastAttemptAt = await lastLearnAttemptAt(orgId, target);
  const orgEnabled = orgRow?.enabled === true;
  const targetEnabled = stateRow ? stateRow.enabled : true;

  const learned = await db().transaction(async (tx) => {
    await tx.execute(sql`select pg_advisory_xact_lock(hashtext(${orgId}), hashtext(${key}))`);
    const guarded =
      orgEnabled && targetEnabled && guardDue(orgId, target, now, input.guardIntervalMs)
        ? ((await rollBackCitedRules({
            orgId,
            orgRoot,
            target,
            cohortRunIds: settledCohort(usages, currentVersion, now, settlementMs).map((u) => u.runId),
          })) ??
          (await rollBackRegressedTarget({ orgId, orgRoot, target, usages, versions, minRepeats, settlementMs, now })))
        : null;
    const result =
      guarded ??
      (await runTargetLearn({
        orgId,
        orgRoot,
        target,
        label,
        orgEnabled,
        targetEnabled,
        usages,
        currentVersion,
        now,
        minRepeats,
        settlementMs,
        minIntervalMs: input.minIntervalMs,
        lastAttemptAt,
        brake: learningBrake(orgId, target, now),
        loadEvidence: (runIds) => loadRunEvidence(orgId, runIds),
        llm: input.llm,
        skillBaseHash,
        currentDescription,
        catalog,
        claimSkill: async (skillName) => {
          const other = learnTargetKey({ kind: "skill-description", id: skillName });
          const locked = await tx.execute<{ locked: boolean }>(
            sql`select pg_try_advisory_xact_lock(hashtext(${orgId}), hashtext(${other})) as locked`,
          );
          return locked.rows[0]?.locked === true;
        },
      }));
    if (!result.target || learnTargetKey(result.target) === key) return finish(orgId, orgRoot, target, result, tx);
    const { cohortDigest, ...changed } = result.trace;
    await finish(orgId, orgRoot, target, {
      decision: "skipped",
      reason: "described_other_skill",
      trace: { ...changed, cohortDigest, skipReason: `applied to ${result.target.id}` },
    }, tx);
    return finish(orgId, orgRoot, result.target, { ...result, trace: changed }, tx);
  });
  await afterLearn(orgId, learned.target ?? target, learned, now);
  return learned;
}

/** Audit after a learner result commits: lessons, and an event for a rollback. */
export async function afterLearn(orgId: string, target: LearnTarget, result: SkillLearnResult, now = new Date()): Promise<void> {
  await recordResultLessons(orgId, target, result, now);
  if (result.decision === "rolled_back") {
    const what = target.kind === "skill-description" ? `routing description for ${target.id}` : `${target.kind} ${target.id}`;
    await recordLearningEventQuietly({
      orgId,
      kind: "rolled_back",
      key: `${target.kind}:${target.id}:${utcDay(now)}`,
      title: `OpenNeko rolled back learned guidance for ${what}.`,
      body: `Runs that used it went wrong more often. ${result.reason}.`,
      mood: "watch",
      now,
    });
  }
}

async function finish(
  orgId: string,
  orgRoot: string,
  target: LearnTarget,
  result: SkillLearnResult,
  tx?: Tx,
): Promise<SkillLearnResult> {
  let eventId = "";
  const write = async (t: Tx) => {
    eventId = await insertLearnEvent(t, orgId, target, result);
    if (result.decision === "applied" || result.decision === "rolled_back") {
      await upsertLearnState(t, orgId, target, {
        base: result.trace.baseHash ?? null,
        learned: result.trace.contentHash ?? "",
      });
      await recordConfigChange({
        workspaceRoot: orgRoot,
        paths: [learnTargetVcsPath(target)],
        message: `${result.decision === "rolled_back" ? "Rolled back learned" : "Learned"} ${target.kind}: ${target.id}`,
      });
    }
  };
  if (tx) await write(tx);
  else await db().transaction(write);
  return { ...result, eventId };
}

/** Records the learned rules a run cited, for per-rule credit. Keys that name no live rule are ignored. */
export async function recordLearnedRuleUse(orgId: string, runId: string): Promise<string[]> {
  const [run] = await db()
    .select({ threadId: work_run.thread_id, channel: work_thread.channel })
    .from(work_run)
    .innerJoin(work_thread, eq(work_thread.id, work_run.thread_id))
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  if (!run || run.channel === LEARNING_THREAD_CHANNEL) return [];
  const [messages, events] = await Promise.all([
    db()
      .select({ content: work_message.content })
      .from(work_message)
      .where(and(eq(work_message.run_id, runId), eq(work_message.role, "assistant"))),
    db()
      .select({ payload: work_run_event.payload })
      .from(work_run_event)
      .where(and(eq(work_run_event.run_id, runId), inArray(work_run_event.kind, ["interim", "message"]))),
  ]);
  const text = [
    ...messages.map((m) => m.content),
    ...events.map((e) => String((e.payload as { content?: unknown })?.content ?? "")),
  ].join("\n");
  const orgRoot = getOrgAgentRoot(orgId);
  const rows: Array<typeof learned_rule_use.$inferInsert> = [];
  const rulesByDir = new Map<string, LearnedRule[]>();
  for (const key of extractRuleCitations(text)) {
    const parsed = parseRuleKey(key);
    if (!parsed) continue;
    const target = { kind: parsed.kind, id: parsed.id };
    const dir = learnTargetDir(orgRoot, target);
    const rules = rulesByDir.get(dir) ?? (await readRulesAt(dir));
    rulesByDir.set(dir, rules);
    if (!rules.some((rule) => rule.id === parsed.ruleId)) continue;
    rows.push({
      org_id: orgId,
      run_id: runId,
      rule_key: key,
      target_kind: target.kind,
      target_id: target.id,
      version: learnedBodyHash(renderKeyedRules(target, rules)),
    });
  }
  if (rows.length === 0) return [];
  const inserted = await db()
    .insert(learned_rule_use)
    .values(rows)
    .onConflictDoNothing()
    .returning({ ruleKey: learned_rule_use.rule_key });
  const ruleKeys = inserted.map((row) => row.ruleKey);
  if (ruleKeys.length > 0) {
    const { appendWorkRunEvent } = await import("./store");
    await appendWorkRunEvent({
      orgId,
      threadId: run.threadId,
      runId,
      event: { type: "learned_rule_use", ruleKeys },
    });
  }
  return ruleKeys;
}

/** The targets a finished run feeds: its skills, the source it queried, the org playbook, and routing. */
export async function learnTargetsForRun(orgId: string, runId: string): Promise<{
  targets: LearnTarget[];
  noSkill: boolean;
}> {
  const [run] = await db()
    .select({
      threadId: work_run.thread_id,
      status: work_run.status,
      createdAt: work_run.created_at,
      channel: work_thread.channel,
    })
    .from(work_run)
    .innerJoin(work_thread, eq(work_thread.id, work_run.thread_id))
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  if (!run || run.channel === LEARNING_THREAD_CHANNEL) return { targets: [], noSkill: false };
  const skills = (
    await db().select({ name: skill_usage.skill_name }).from(skill_usage).where(eq(skill_usage.run_id, runId))
  ).map((row) => row.name);
  const targets: LearnTarget[] = skills.map((name) => ({ kind: "skill", id: name }));

  const [queried] = await db()
    .select({ id: work_run_event.id })
    .from(work_run_event)
    .where(
      and(
        eq(work_run_event.run_id, runId),
        eq(work_run_event.kind, "tool_start"),
        sql`${work_run_event.payload}->>'name' ~* ${GRAPHJIN_TOOL_PATTERN}`,
      ),
    )
    .limit(1);
  if (queried) {
    const [source] = await db()
      .select({ id: data_source.id })
      .from(data_source)
      .where(and(eq(data_source.org_id, orgId), eq(data_source.enabled, true)))
      .orderBy(desc(data_source.is_default), data_source.created_at)
      .limit(1);
    if (source) targets.push({ kind: "source", id: source.id });
  }
  targets.push({ kind: "org", id: "org" });

  const [toolError] = await db()
    .select({ id: work_run_event.id })
    .from(work_run_event)
    .where(and(eq(work_run_event.run_id, runId), eq(work_run_event.kind, "tool_end"), sql`${work_run_event.payload} ? 'error'`))
    .limit(1);
  const [verifiedWrong] = await db()
    .select({ id: run_verification.id })
    .from(run_verification)
    .where(and(eq(run_verification.run_id, runId), inArray(run_verification.verdict, ["fail", "disagree"])))
    .limit(1);
  const routing = new Set<string>();
  if (run.status === "failed" || toolError || verifiedWrong || skills.length >= 2) skills.forEach((name) => routing.add(name));
  // This run's request may correct the previous run in the thread.
  const [previous] = await db()
    .select({ id: work_run.id })
    .from(work_run)
    .where(and(eq(work_run.thread_id, run.threadId), ne(work_run.id, runId), sql`${work_run.created_at} < ${run.createdAt}`))
    .orderBy(desc(work_run.created_at))
    .limit(1);
  if (previous) {
    const prior = await db().select({ name: skill_usage.skill_name }).from(skill_usage).where(eq(skill_usage.run_id, previous.id));
    prior.forEach((row) => routing.add(row.name));
  }
  for (const name of routing) targets.push({ kind: "skill-description", id: name });
  return { targets, noSkill: skills.length === 0 };
}
