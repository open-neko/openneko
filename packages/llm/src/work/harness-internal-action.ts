import { and, app_user, db, eq, work_run } from "@neko/db";
import { z } from "zod";

const userAdminPayload = z.object({
  action:z.literal("invite"),
  email:z.email(),
  role:z.literal("member"),
}).strict();

/** Internal admin requests still require a live Work actor, a typed payload,
 * and a stable target. Only approval may dispatch the existing worker adapter. */
export async function validateHarnessInternalAction(
  scope:{orgId:string;runId:string},kind:string,payload:Record<string,unknown>,
):Promise<{definition:Record<string,unknown>;target:string}> {
  if (kind!=="user_admin") throw new Error("Internal action is not admitted");
  const input=userAdminPayload.parse(payload);
  const [run]=await db().select({userId:work_run.actor_user_id,role:work_run.actor_role})
    .from(work_run).where(and(eq(work_run.org_id,scope.orgId),eq(work_run.id,scope.runId))).limit(1);
  if (!run?.userId) throw new Error("Internal action requires a named Work actor");
  const [actor]=await db().select({disabledAt:app_user.disabled_at})
    .from(app_user).where(and(eq(app_user.org_id,scope.orgId),eq(app_user.id,run.userId))).limit(1);
  if (!actor || actor.disabledAt) throw new Error("Requesting actor is no longer active");
  const email=input.email.trim().toLowerCase();
  const [existing]=await db().select({id:app_user.id})
    .from(app_user).where(and(eq(app_user.org_id,scope.orgId),eq(app_user.email,email))).limit(1);
  return {target:email,definition:{harnessSource:"internal",kind,target:email,
    observed:{existingUserId:existing?.id??null}}};
}
