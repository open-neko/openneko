import {
  and,
  db,
  eq,
  skill_learn_event,
  skill_learn_org,
  skill_learn_state,
  skill_usage,
  sql,
} from "@neko/db";
import { getOrgAgentRoot } from "./workspace";
import { learnedBodyHash, runSkillLearn, type SkillLearnResult } from "./skill-learn";

export async function runSkillLearnForOrgSkill(input: {
  orgId: string;
  skillName: string;
}): Promise<SkillLearnResult> {
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
  const usageRows = await db()
    .select({
      runId: skill_usage.run_id,
      skillName: skill_usage.skill_name,
      contentHash: skill_usage.content_hash,
      createdAt: skill_usage.created_at,
    })
    .from(skill_usage)
    .where(
      and(
        eq(skill_usage.org_id, input.orgId),
        eq(skill_usage.skill_name, input.skillName),
      ),
    );

  return db().transaction(async (tx) => {
    await tx.execute(
      sql`select pg_advisory_xact_lock(hashtext(${input.orgId}), hashtext(${input.skillName}))`,
    );

    const result = await runSkillLearn({
      orgId: input.orgId,
      orgRoot,
      skillName: input.skillName,
      orgEnabled: orgRow?.enabled === true,
      skillEnabled: stateRow ? stateRow.enabled : true,
      usages: usageRows.map((row) => ({
        runId: row.runId,
        skillName: row.skillName,
        contentHash: row.contentHash,
        createdAt: row.createdAt,
      })),
    });

    await tx.insert(skill_learn_event).values({
      org_id: input.orgId,
      skill_name: input.skillName,
      base_hash: result.trace.baseHash ?? null,
      content_hash: result.trace.contentHash ?? null,
      run_ids: result.trace.evidenceRunIds ?? [],
      lesson: result.trace.lesson ?? null,
      rationale: result.trace.rationale ?? null,
      diff: result.diff ?? null,
      model_trace: result.trace,
      decision: result.decision,
      reason: result.reason,
    });

    if (result.decision === "applied" && result.overlay) {
      await tx
        .insert(skill_learn_state)
        .values({
          org_id: input.orgId,
          skill_name: input.skillName,
          enabled: true,
          current_base_hash: result.overlay.baseHash,
          current_learned_hash: learnedBodyHash(result.overlay.body),
        })
        .onConflictDoUpdate({
          target: [skill_learn_state.org_id, skill_learn_state.skill_name],
          set: {
            current_base_hash: result.overlay.baseHash,
            current_learned_hash: learnedBodyHash(result.overlay.body),
            updated_at: new Date(),
          },
        });
    }

    return result;
  });
}
