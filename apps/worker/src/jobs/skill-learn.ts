import type { LearnVerifyPayload, SkillDiscoverPayload, SkillLearnPayload } from "@neko/db/jobs";
import { isLearnTargetKind, runLearnForTarget, runLearnVerifyForRun, runSkillDiscoveryForOrg } from "@neko/llm/work";

export async function runSkillLearnJob(
  payload: SkillLearnPayload,
): Promise<void> {
  const target = isLearnTargetKind(payload.targetKind) && payload.targetId
    ? { kind: payload.targetKind, id: payload.targetId }
    : payload.skillName
      ? { kind: "skill" as const, id: payload.skillName }
      : null;
  if (!target) {
    console.warn(`[skill_learn] org=${payload.orgId} job has no target`);
    return;
  }
  const result = await runLearnForTarget({ orgId: payload.orgId, target });
  console.log(
    `[skill_learn] org=${payload.orgId} target=${target.kind}:${target.id} decision=${result.decision} reason=${result.reason}`,
  );
}

export async function runSkillDiscoverJob(payload: SkillDiscoverPayload): Promise<void> {
  const result = await runSkillDiscoveryForOrg({ orgId: payload.orgId });
  console.log(
    `[skill_discover] org=${payload.orgId} decision=${result.decision} reason=${result.reason}${
      result.skillName ? ` skill=${result.skillName}` : ""
    }${result.archived.length ? ` archived=${result.archived.join(",")}` : ""}`,
  );
}

export async function runLearnVerifyJob(payload: LearnVerifyPayload): Promise<void> {
  const result = await runLearnVerifyForRun({ orgId: payload.orgId, runId: payload.runId });
  console.log(
    `[learn_verify] org=${payload.orgId} run=${payload.runId} verdict=${result?.verdict ?? "skipped"}`,
  );
}
