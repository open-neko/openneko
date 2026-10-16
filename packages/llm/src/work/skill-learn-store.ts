import { stat } from "node:fs/promises";
import { join } from "node:path";
import {
  and,
  db,
  desc,
  eq,
  gte,
  isNotNull,
  lte,
  observation,
  skill_learn_event,
  skill_learn_org,
  skill_learn_state,
  skill_usage,
  sql,
  work_run,
} from "@neko/db";
import { recordConfigChange } from "../config-vcs";
import {
  archiveIdleLearnedSkills,
  isLearnedSkill,
  runSkillDiscovery,
  SKILL_ARCHIVE_DIR,
  type SkillDiscoveryResult,
} from "./skill-discover";
import { getOrgAgentRoot, listInstalledSkills } from "./workspace";
import {
  DEFAULT_MIN_REPEATS,
  DEFAULT_SETTLEMENT_MS,
  learnedBodyHash,
  runSkillLearn,
  settledCohort,
  type SkillLearnResult,
  type SkillUsageSnapshot,
} from "./skill-learn";
import { loadRunEvidence, type RunEvidence } from "./skill-learn-evidence";
import type { LearnTarget } from "./learn-target";
import {
  afterLearn,
  guardDue,
  insertLearnEvent,
  lastLearnAttemptAt,
  learningBrake,
  learnTargetsForRun,
  notLearningThread,
  recordLearnedRuleUse,
  rollBackCitedRules,
  runNonSkillTargetLearn,
  runSettledAt,
  upsertLearnState,
  writeTargetRules,
} from "./learn-target-store";
import { recordLearningEventQuietly } from "./learn-events";
import { getLesson, recordLessons } from "./learn-lessons";
import { learnTargetDir, learnTargetKey, learnTargetVcsPath, isLearnTargetKind } from "./learn-target";
import type { RunSignal } from "./run-signal";

export { runSettledAt };
import {
  normalizeRuleText,
  readLearnedRules,
  readRulesAt,
  rejectLearnedRules,
  rejectTextsAt,
} from "./skill-learn-playbook";
import {
  recordAnswerChecks,
  runRecompute,
  scheduleRecompute,
  type VerifyExecute,
  type VerifyLlm,
} from "./learn-verify-store";
import type { SkillLearnLlm } from "./skill-learn-propose";
import { learnedVersionRegressed, scoreSkillLearnWindow } from "./skill-learn-score";
import {
  fingerprintEffectiveSkill,
  readLearnedDescription,
  readLearnedOverlay,
  writeLearnedDescription,
  writeLearnedOverlay,
} from "./skill-overlay";

export type SkillLearnOrgSettings = {
  enabled: boolean;
  source: "org" | "default";
};

export async function getSkillLearnOrgSettings(
  orgId: string,
): Promise<SkillLearnOrgSettings> {
  const [row] = await db()
    .select({ enabled: skill_learn_org.enabled })
    .from(skill_learn_org)
    .where(eq(skill_learn_org.org_id, orgId))
    .limit(1);
  if (!row) return { enabled: false, source: "default" };
  return { enabled: row.enabled, source: "org" };
}

export async function setSkillLearnOrgEnabled(input: {
  orgId: string;
  enabled: boolean;
}): Promise<SkillLearnOrgSettings> {
  await db()
    .insert(skill_learn_org)
    .values({
      org_id: input.orgId,
      enabled: input.enabled,
      updated_at: new Date(),
    })
    .onConflictDoUpdate({
      target: skill_learn_org.org_id,
      set: {
        enabled: input.enabled,
        updated_at: new Date(),
      },
    });
  return { enabled: input.enabled, source: "org" };
}

/** One entry point for every learning target kind. */
export async function runLearnForTarget(input: {
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
  if (input.target.kind === "skill") {
    return runSkillLearnForOrgSkill({ ...input, skillName: input.target.id });
  }
  return runNonSkillTargetLearn(input);
}

export async function runSkillLearnForOrgSkill(input: {
  orgId: string;
  skillName: string;
  minRepeats?: number;
  settlementMs?: number;
  verifyTimeoutMs?: number;
  guardIntervalMs?: number;
  llm?: SkillLearnLlm;
  now?: Date;
}): Promise<SkillLearnResult> {
  const target: LearnTarget = { kind: "skill", id: input.skillName };
  const orgRoot = getOrgAgentRoot(input.orgId);
  const [orgRow] = await db()
    .select({ enabled: skill_learn_org.enabled })
    .from(skill_learn_org)
    .where(eq(skill_learn_org.org_id, input.orgId))
    .limit(1);
  const [stateRow] = await db()
    .select()
    .from(skill_learn_state)
    .where(
      and(
        eq(skill_learn_state.org_id, input.orgId),
        eq(skill_learn_state.skill_name, input.skillName),
      ),
    )
    .limit(1);
  const idleMs = input.settlementMs ?? DEFAULT_SETTLEMENT_MS;
  const usages = (
    await db()
      .select({
        runId: skill_usage.run_id,
        skillName: skill_usage.skill_name,
        contentHash: skill_usage.content_hash,
        createdAt: skill_usage.created_at,
        settledAt: runSettledAt(idleMs, input.verifyTimeoutMs),
      })
      .from(skill_usage)
      .innerJoin(work_run, eq(work_run.id, skill_usage.run_id))
      .where(
        and(
          eq(skill_usage.org_id, input.orgId),
          eq(skill_usage.skill_name, input.skillName),
          notLearningThread,
        ),
      )
  ).map((row) => ({
    runId: row.runId,
    skillName: row.skillName,
    contentHash: row.contentHash,
    createdAt: row.createdAt,
    settledAt: row.settledAt ? new Date(row.settledAt) : null,
  }));
  const lastAttemptAt = await lastLearnAttemptAt(input.orgId, target);
  const orgEnabled = orgRow?.enabled === true;
  const skillEnabled = stateRow ? stateRow.enabled : true;
  const now = input.now ?? new Date();

  const learned = await db().transaction(async (tx) => {
    await tx.execute(
      sql`select pg_advisory_xact_lock(hashtext(${input.orgId}), hashtext(${input.skillName}))`,
    );

    const guard = orgEnabled && skillEnabled && guardDue(input.orgId, target, now, input.guardIntervalMs);
    const result =
      (guard ? await rollBackSkillRules({ ...input, orgRoot, usages, now }) : null) ??
      (guard ? await rollBackRegressedVersion({ ...input, orgRoot, usages }) : null) ??
      (await runSkillLearn({
        orgId: input.orgId,
        orgRoot,
        skillName: input.skillName,
        orgEnabled,
        skillEnabled,
        usages,
        minRepeats: input.minRepeats,
        settlementMs: input.settlementMs,
        lastAttemptAt,
        now,
        brake: learningBrake(input.orgId, target, now),
        llm: input.llm,
        loadEvidence: (runIds) => loadRunEvidence(input.orgId, runIds),
      }));

    const eventId = await insertLearnEvent(tx, input.orgId, target, result);

    if (
      (result.decision === "applied" || result.decision === "rolled_back") &&
      result.overlay
    ) {
      await upsertLearnState(tx, input.orgId, target, {
        base: result.overlay.baseHash,
        learned: learnedBodyHash(result.overlay.body),
      });
      await recordConfigChange({
        workspaceRoot: orgRoot,
        paths: [`skill-overlays/${input.skillName}`],
        message:
          result.decision === "rolled_back"
            ? `Rolled back learned overlay: ${input.skillName}`
            : `Learned overlay: ${input.skillName}`,
      });
    }

    return { ...result, eventId };
  });
  await afterLearn(input.orgId, target, learned, now);
  return learned;
}

async function rollBackSkillRules(input: {
  orgId: string;
  orgRoot: string;
  skillName: string;
  usages: SkillUsageSnapshot[];
  settlementMs?: number;
  now: Date;
}): Promise<SkillLearnResult | null> {
  const skillDir = join(input.orgRoot, "skills", input.skillName);
  const currentHash = await fingerprintEffectiveSkill(input.orgRoot, input.skillName, skillDir).catch(
    () => null,
  );
  if (!currentHash) return null;
  return rollBackCitedRules({
    orgId: input.orgId,
    orgRoot: input.orgRoot,
    target: { kind: "skill", id: input.skillName },
    cohortRunIds: settledCohort(
      input.usages,
      currentHash,
      input.now,
      input.settlementMs ?? DEFAULT_SETTLEMENT_MS,
    ).map((usage) => usage.runId),
  });
}

async function rollBackRegressedVersion(input: {
  orgId: string;
  orgRoot: string;
  skillName: string;
  usages: SkillUsageSnapshot[];
  minRepeats?: number;
  settlementMs?: number;
}): Promise<SkillLearnResult | null> {
  const overlay = await readLearnedOverlay(input.orgRoot, input.skillName);
  if (!overlay || overlay.status !== "applied") return null;
  const [lastApplied] = await db()
    .select({ contentHash: skill_learn_event.content_hash })
    .from(skill_learn_event)
    .where(
      and(
        eq(skill_learn_event.org_id, input.orgId),
        eq(skill_learn_event.skill_name, input.skillName),
        eq(skill_learn_event.decision, "applied"),
      ),
    )
    .orderBy(desc(skill_learn_event.created_at))
    .limit(1);
  if (!lastApplied?.contentHash) return null;
  const skillDir = join(input.orgRoot, "skills", input.skillName);
  const currentHash = await fingerprintEffectiveSkill(
    input.orgRoot,
    input.skillName,
    skillDir,
  ).catch(() => null);
  if (!currentHash || currentHash === lastApplied.contentHash) return null;

  const minRepeats = input.minRepeats ?? DEFAULT_MIN_REPEATS;
  const settlementMs = input.settlementMs ?? DEFAULT_SETTLEMENT_MS;
  const now = Date.now();
  const settled = input.usages.filter((usage) =>
    usage.settledAt !== undefined
      ? usage.settledAt !== null && usage.settledAt.getTime() <= now
      : now - usage.createdAt.getTime() >= settlementMs,
  );
  const beforeIds = settled
    .filter((usage) => usage.contentHash === lastApplied.contentHash)
    .map((usage) => usage.runId);
  const afterIds = settled
    .filter((usage) => usage.contentHash === currentHash)
    .map((usage) => usage.runId);
  if (beforeIds.length < minRepeats || afterIds.length < minRepeats) return null;

  const [beforeRuns, afterRuns] = await Promise.all([
    loadRunEvidence(input.orgId, beforeIds),
    loadRunEvidence(input.orgId, afterIds),
  ]);
  const before = scoreSkillLearnWindow(beforeRuns.map(skillScoreSample(input.skillName)));
  const after = scoreSkillLearnWindow(afterRuns.map(skillScoreSample(input.skillName)));
  if (!learnedVersionRegressed(before, after)) return null;

  const rules = await readLearnedRules(input.orgRoot, input.skillName);
  await rejectLearnedRules(input.orgRoot, input.skillName, rules);
  const disabled = { ...overlay, status: "disabled" as const };
  await writeLearnedOverlay(input.orgRoot, disabled);
  return {
    decision: "rolled_back",
    reason: `failure rate ${before.failureRate.toFixed(2)} -> ${after.failureRate.toFixed(2)}, correction rate ${before.correctionRate.toFixed(2)} -> ${after.correctionRate.toFixed(2)}`,
    overlay: disabled,
    trace: {
      baseHash: overlay.baseHash,
      contentHash: currentHash,
      evidenceRunIds: afterIds,
      rules,
    },
  };
}

export function skillScoreSample(skillName: string) {
  return (run: RunEvidence) => ({
    skillName,
    hadCorrection: run.toolErrors > 0,
    hadRelevantFailure: run.outcome === "failed" || Boolean(run.verifiedWrong),
    tokens: 0,
    latencyMs: 0,
  });
}

/**
 * Records rule citations and answer checks, samples the run for an independent
 * recomputation, and queues every learning target the run feeds; the batch
 * trigger decides whether to call the model.
 */
export async function enqueueSkillLearnForRun(orgId: string, runId: string): Promise<void> {
  const warn = (what: string) => (error: unknown) => {
    console.warn(`[skill-learn] ${what} failed for run ${runId}: ${error instanceof Error ? error.message : error}`);
  };
  try {
    await recordLearnedRuleUse(orgId, runId).catch(warn("citation record"));
    await recordAnswerChecks(orgId, runId).catch(warn("answer checks"));
    if (!(await getSkillLearnOrgSettings(orgId)).enabled) return;
    if (await scheduleRecompute(orgId, runId).catch(warn("recompute sampling"))) {
      const { enqueue, QUEUE } = await import("@neko/db/jobs");
      await enqueue(QUEUE.LEARN_VERIFY, { orgId, runId }, { singletonKey: `learn-verify:${runId}` });
    }
    await enqueueLearnTargets(orgId, runId);
  } catch (error) {
    warn("enqueue")(error);
  }
}

/**
 * The runs a signal about one run settles: a root and its finished sub-runs.
 * A sub-run settles with its root, so a sub-run alone settles only once its
 * root has finished.
 */
export async function runsSettledWith(orgId: string, runId: string): Promise<string[]> {
  const [run] = await db()
    .select({ id: work_run.id, rootRunId: work_run.root_run_id })
    .from(work_run)
    .where(and(eq(work_run.org_id, orgId), eq(work_run.id, runId)))
    .limit(1);
  if (!run) return [];
  if (run.rootRunId && run.rootRunId !== run.id) {
    const [root] = await db()
      .select({ finishedAt: work_run.finished_at })
      .from(work_run)
      .where(eq(work_run.id, run.rootRunId))
      .limit(1);
    return root?.finishedAt ? [run.id] : [];
  }
  const subRuns = await db()
    .select({ id: work_run.id })
    .from(work_run)
    .where(and(eq(work_run.org_id, orgId), eq(work_run.root_run_id, run.id), isNotNull(work_run.finished_at)))
    .orderBy(work_run.created_at);
  return [run.id, ...subRuns.map((row) => row.id)];
}

/** The learning consumer of run signals: a finished run feeds its targets, and feedback re-queues them. */
export async function deliverLearningSignal(signal: RunSignal): Promise<string[]> {
  const runIds = await runsSettledWith(signal.orgId, signal.runId);
  for (const runId of runIds) {
    if (signal.kind === "run_finished") {
      await enqueueSkillLearnForRun(signal.orgId, runId);
    } else if ((await getSkillLearnOrgSettings(signal.orgId)).enabled) {
      await enqueueLearnTargets(signal.orgId, runId);
    }
  }
  return runIds;
}

/**
 * An admin dismisses a lesson. Its text goes on the target's blocklist, and a
 * live rule with that text leaves the store as a new rolled-back version.
 */
export async function dismissLesson(input: {
  orgId: string;
  lessonId: string;
  userId?: string | null;
}): Promise<{ dismissed: boolean; removedRule: boolean } | null> {
  const lesson = await getLesson(input.orgId, input.lessonId);
  if (!lesson) return null;
  const kind = lesson.detail.targetKind;
  if (!isLearnTargetKind(kind)) return null;
  const target: LearnTarget = { kind, id: lesson.detail.targetId };
  const orgRoot = getOrgAgentRoot(input.orgId);
  const dir = learnTargetDir(orgRoot, target);
  const normalized = normalizeRuleText(lesson.text);
  const removedRule = await db().transaction(async (tx) => {
    await tx.execute(
      sql`select pg_advisory_xact_lock(hashtext(${input.orgId}), hashtext(${learnTargetKey(target)}))`,
    );
    await rejectTextsAt(dir, [lesson.text]);
    let result: SkillLearnResult | null = null;
    if (target.kind === "skill-description") {
      const learned = await readLearnedDescription(orgRoot, target.id);
      if (learned?.status === "applied" && normalizeRuleText(learned.description) === normalized) {
        await writeLearnedDescription(orgRoot, { ...learned, status: "disabled" });
        result = {
          decision: "rolled_back",
          reason: "an admin dismissed the lesson",
          rules: [],
          trace: { contentHash: `${learnedBodyHash(learned.description)}:disabled` },
        };
      }
    } else {
      const rules = await readRulesAt(dir);
      const removed = rules.filter((rule) => normalizeRuleText(rule.text) === normalized);
      if (removed.length > 0) {
        const kept = rules.filter((rule) => !removed.includes(rule));
        const { body, overlay } = await writeTargetRules(orgRoot, target, kept);
        result = {
          decision: "rolled_back",
          reason: "an admin dismissed the lesson",
          ...(overlay ? { overlay } : {}),
          rules: kept,
          trace: {
            ...(overlay ? { baseHash: overlay.baseHash } : {}),
            contentHash: learnedBodyHash(body),
            rules: removed,
          },
        };
      }
    }
    if (result) {
      await insertLearnEvent(tx, input.orgId, target, result);
      await upsertLearnState(tx, input.orgId, target, {
        base: result.trace.baseHash ?? null,
        learned: result.trace.contentHash ?? "",
      });
    }
    await tx
      .update(observation)
      .set({
        status: "dismissed",
        consumer_user_id: input.userId ?? null,
        detail: sql`${observation.detail} || ${JSON.stringify({ dismissedBy: input.userId ?? null })}::jsonb`,
        updated_at: new Date(),
      })
      .where(and(eq(observation.org_id, input.orgId), eq(observation.id, lesson.id)));
    return Boolean(result);
  });
  await recordConfigChange({
    workspaceRoot: orgRoot,
    paths: [learnTargetVcsPath(target)],
    message: `Dismissed learned lesson for ${target.kind}: ${target.id}`,
  });
  return { dismissed: true, removedRule };
}

/** The queued recomputation for a sampled run; afterwards the run's targets learn from the verdict. */
export async function runLearnVerifyForRun(input: {
  orgId: string;
  runId: string;
  llm?: VerifyLlm;
  execute?: VerifyExecute;
}): Promise<{ verdict: string; detail: string } | null> {
  const result = await runRecompute(input);
  if (result && (await getSkillLearnOrgSettings(input.orgId)).enabled) {
    await enqueueLearnTargets(input.orgId, input.runId);
  }
  return result;
}

export async function enqueueLearnTargets(orgId: string, runId: string): Promise<void> {
  const { targets, noSkill } = await learnTargetsForRun(orgId, runId);
  if (targets.length === 0) return;
  const { enqueue, QUEUE } = await import("@neko/db/jobs");
  if (noSkill) {
    await enqueue(QUEUE.SKILL_DISCOVER, { orgId }, { singletonKey: `skill-discover:${orgId}` });
  }
  for (const target of targets) {
    await enqueue(
      QUEUE.SKILL_LEARN,
      target.kind === "skill"
        ? { orgId, skillName: target.id, targetKind: target.kind, targetId: target.id }
        : { orgId, targetKind: target.kind, targetId: target.id },
      {
        singletonKey:
          target.kind === "skill"
            ? `skill-learn:${orgId}:${target.id}`
            : `learn:${orgId}:${target.kind}:${target.id}`,
      },
    );
  }
}

export const DISCOVERY_EVENT_SKILL = "_discovery";
const DISCOVERY_WINDOW_MS = 14 * 24 * 60 * 60 * 1000;
const DISCOVERY_THROTTLE_MS = 60 * 60 * 1000;
const DISCOVERY_CANDIDATES = 40;

export async function runSkillDiscoveryForOrg(input: {
  orgId: string;
  llm?: SkillLearnLlm;
  settlementMs?: number;
  throttleMs?: number;
  now?: Date;
}): Promise<SkillDiscoveryResult & { archived: string[] }> {
  const now = input.now ?? new Date();
  const settings = await getSkillLearnOrgSettings(input.orgId);
  if (!settings.enabled) {
    return { decision: "skipped", reason: "flags_off", evidenceRunIds: [], archived: [] };
  }
  const events = await db()
    .select({
      runIds: skill_learn_event.run_ids,
      decision: skill_learn_event.decision,
      createdAt: skill_learn_event.created_at,
    })
    .from(skill_learn_event)
    .where(
      and(
        eq(skill_learn_event.org_id, input.orgId),
        eq(skill_learn_event.skill_name, DISCOVERY_EVENT_SKILL),
      ),
    )
    .orderBy(desc(skill_learn_event.created_at))
    .limit(200);
  const throttleMs = input.throttleMs ?? DISCOVERY_THROTTLE_MS;
  if (events[0] && now.getTime() - events[0].createdAt.getTime() < throttleMs) {
    return { decision: "skipped", reason: "throttled", evidenceRunIds: [], archived: [] };
  }
  const consumed = new Set(
    events
      .filter((event) => event.decision === "created" || event.decision === "extended")
      .flatMap((event) => event.runIds),
  );
  const settledBefore = new Date(now.getTime() - (input.settlementMs ?? DEFAULT_SETTLEMENT_MS));
  const recent = await db()
    .select({ id: work_run.id })
    .from(work_run)
    .where(
      and(
        eq(work_run.org_id, input.orgId),
        eq(work_run.status, "completed"),
        gte(work_run.finished_at, new Date(now.getTime() - DISCOVERY_WINDOW_MS)),
        lte(work_run.finished_at, settledBefore),
        sql`not exists (select 1 from ${skill_usage} where ${skill_usage.run_id} = ${work_run.id})`,
      ),
    )
    .orderBy(desc(work_run.finished_at))
    .limit(DISCOVERY_CANDIDATES * 2);
  const candidateIds = recent
    .map((row) => row.id)
    .filter((id) => !consumed.has(id))
    .slice(0, DISCOVERY_CANDIDATES);

  const orgRoot = getOrgAgentRoot(input.orgId);
  const skillsRoot = join(orgRoot, "skills");
  const catalog = await Promise.all(
    (await listInstalledSkills(skillsRoot)).map(async (skill) => ({
      ...skill,
      learned: await isLearnedSkill(join(skillsRoot, skill.name)),
    })),
  );
  const usageRows = await db()
    .select({ skillName: skill_usage.skill_name, last: sql<Date | null>`max(${skill_usage.created_at})` })
    .from(skill_usage)
    .where(eq(skill_usage.org_id, input.orgId))
    .groupBy(skill_usage.skill_name);
  const lastUsedAt = new Map(
    usageRows
      .filter((row) => row.last)
      .map((row) => [row.skillName, new Date(row.last as Date)]),
  );
  const createdAt = new Map<string, Date>();
  for (const skill of catalog.filter((entry) => entry.learned)) {
    const info = await stat(join(skillsRoot, skill.name, "SKILL.md")).catch(() => null);
    if (info) createdAt.set(skill.name, info.birthtime);
  }
  const archived = await archiveIdleLearnedSkills({ orgRoot, catalog, lastUsedAt, createdAt, now });
  const live = catalog.filter((skill) => !archived.includes(skill.name));

  const evidence = (await loadRunEvidence(input.orgId, candidateIds)).filter(
    (run) => run.outcome === "completed" || run.followUp,
  );
  const result = await runSkillDiscovery({
    orgRoot,
    catalog: live,
    evidence,
    llm: input.llm ?? (await defaultDiscoveryLlm(input.orgId)),
  });

  const [discoveryEvent] = await db().insert(skill_learn_event).values({
    org_id: input.orgId,
    skill_name: DISCOVERY_EVENT_SKILL,
    target_kind: "discovery",
    target_id: DISCOVERY_EVENT_SKILL,
    run_ids: result.evidenceRunIds,
    lesson: result.skillName ?? null,
    rationale: result.rationale ?? null,
    model_trace: { ...result, archived },
    decision: result.decision,
    reason: result.reason,
  }).returning({ id: skill_learn_event.id });
  if ((result.decision === "created" || result.decision === "extended") && result.skillName && result.lesson) {
    await recordLessons({
      orgId: input.orgId,
      target: { kind: "skill", id: result.skillName },
      lessons: [
        {
          text: result.lesson,
          verdict: "promoted",
          reason: result.reason,
          runIds: result.evidenceRunIds,
          quotes: [],
          ...(result.decision === "extended"
            ? { ruleId: result.rules?.find((rule) => rule.text === result.lesson)?.id }
            : {}),
        },
      ],
      decision: result.decision,
      eventId: discoveryEvent?.id ?? null,
      now,
    }).catch((error: unknown) => {
      console.warn(`[skill-discover] lesson record failed: ${error instanceof Error ? error.message : error}`);
    });
  }
  if (result.decision === "created" && result.skillName) {
    await recordLearningEventQuietly({
      orgId: input.orgId,
      kind: "skill_created",
      key: result.skillName,
      title: `OpenNeko learned a new skill: ${result.skillName}.`,
      body: `It found the same procedure in ${result.evidenceRunIds.length} settled runs and wrote it as a skill. ${result.lesson ?? ""}`.trim(),
      mood: "good",
      now,
    });
  }
  if (result.decision === "created" || result.decision === "extended" || archived.length > 0) {
    await recordConfigChange({
      workspaceRoot: orgRoot,
      paths: [
        ...(result.skillName
          ? [`skills/${result.skillName}`, `skill-overlays/${result.skillName}`]
          : []),
        ...(archived.length > 0 ? ["skills", SKILL_ARCHIVE_DIR] : []),
      ],
      message:
        result.decision === "created"
          ? `Learned skill: ${result.skillName}`
          : result.decision === "extended"
            ? `Learned rule for skill: ${result.skillName}`
            : `Archived idle learned skills: ${archived.join(", ")}`,
    });
  }
  return { ...result, archived };
}

async function defaultDiscoveryLlm(orgId: string): Promise<SkillLearnLlm> {
  const [{ ax }, { buildLlm }, { recordLearnerSpend }] = await Promise.all([
    import("@ax-llm/ax"),
    import("../llm"),
    import("./learn-spend"),
  ]);
  const llm = await buildLlm(orgId);
  const generator = ax(
    `discoveryPrompt:string "instructions, skill catalog and settled run digests" -> catalogDecision:string "a JSON object with action, rationale and evidence"`,
    {
      description:
        "You decide whether repeated runs need a new skill or a rule on an existing skill. Reply with JSON only. No tools.",
    },
  );
  return async (prompt: string) => {
    try {
      const result = await generator.forward(llm, { discoveryPrompt: prompt });
      return String(result.catalogDecision ?? "");
    } finally {
      await recordLearnerSpend(orgId, generator, "skill-discover");
    }
  };
}
