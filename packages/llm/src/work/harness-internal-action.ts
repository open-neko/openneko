import { administratorUserIds, and, app_user, data_source, db, eq, getUserGroup, listGroupMembers, listUserGroups, work_run } from "@neko/db";
import { z } from "zod";

const userAdminPayload = z.discriminatedUnion("action",[
  z.object({
    action:z.literal("invite"),
    email:z.email(),
    role:z.literal("member"),
  }).strict(),
  z.object({action:z.literal("deactivate"),userId:z.string().trim().min(1).max(128)}).strict(),
  z.object({action:z.literal("reactivate"),userId:z.string().trim().min(1).max(128)}).strict(),
  z.object({action:z.literal("set_role"),userId:z.string().trim().min(1).max(128),role:z.literal("admin")}).strict(),
]);
const groupAdminPayload=z.discriminatedUnion("action",[
  z.object({
    action:z.literal("create_group"),
    name:z.string().trim().min(1).max(120),
    description:z.string().trim().max(500).optional(),
  }).strict(),
  z.object({
    action:z.literal("add_member"),
    groupId:z.uuid(),
    userId:z.string().trim().min(1).max(128),
  }).strict(),
]);
const dataSourceRegisterPayload=z.object({
  action:z.literal("register"),
  name:z.string().trim().min(1).max(64).regex(/^[a-z0-9][a-z0-9-]*$/),
  label:z.string().trim().max(120).optional(),
  sourceKind:z.enum(["graphjin","database","api","files","code"]).optional(),
}).strict();

/** Internal admin requests still require a live Work actor, a typed payload,
 * and a stable target. Only approval may dispatch the existing worker adapter. */
export async function validateHarnessInternalAction(
  scope:{orgId:string;runId:string},kind:string,payload:Record<string,unknown>,
):Promise<{definition:Record<string,unknown>;target:string}> {
  if (!["user_admin","group_admin","data_source_admin"].includes(kind)) throw new Error("Internal action is not admitted");
  const [run]=await db().select({userId:work_run.actor_user_id,role:work_run.actor_role})
    .from(work_run).where(and(eq(work_run.org_id,scope.orgId),eq(work_run.id,scope.runId))).limit(1);
  if (!run?.userId) throw new Error("Internal action requires a named Work actor");
  const [actor]=await db().select({disabledAt:app_user.disabled_at})
    .from(app_user).where(and(eq(app_user.org_id,scope.orgId),eq(app_user.id,run.userId))).limit(1);
  if (!actor || actor.disabledAt) throw new Error("Requesting actor is no longer active");
  if (kind==="user_admin") {
    const input=userAdminPayload.parse(payload);
    if (input.action==="invite") {
      const email=input.email.trim().toLowerCase();
      const [existing]=await db().select({id:app_user.id})
        .from(app_user).where(and(eq(app_user.org_id,scope.orgId),eq(app_user.email,email))).limit(1);
      return {target:email,definition:{harnessSource:"internal",kind,target:email,
        observed:{existingUserId:existing?.id??null}}};
    }
    if (input.userId===run.userId) throw new Error("Actor cannot change their own active state");
    const [targetUser]=await db().select({disabledAt:app_user.disabled_at})
      .from(app_user).where(and(eq(app_user.org_id,scope.orgId),eq(app_user.id,input.userId))).limit(1);
    if (!targetUser) throw new Error("Target user is no longer in this organization");
    const administrator=(await administratorUserIds(scope.orgId)).has(input.userId);
    if (input.action==="set_role") {
      if (targetUser.disabledAt) throw new Error("Target user is disabled");
      if (administrator) throw new Error("Target user is already an administrator");
      return {target:input.userId,definition:{harnessSource:"internal",kind,target:input.userId,
        observed:{disabled:false,administrator:false}}};
    }
    if (administrator) throw new Error("Administrator active state requires a separate lockout guard");
    const disabled=targetUser.disabledAt!==null;
    if (disabled !== (input.action==="reactivate")) throw new Error("Target user state changed");
    return {target:input.userId,definition:{harnessSource:"internal",kind,target:input.userId,
      observed:{disabled,administrator}}};
  }
  if (kind==="data_source_admin") {
    const input=dataSourceRegisterPayload.parse(payload);
    const [existing]=await db().select({id:data_source.id})
      .from(data_source).where(and(eq(data_source.org_id,scope.orgId),eq(data_source.name,input.name))).limit(1);
    if (existing) throw new Error("Data source name is already in use");
    return {target:input.name,definition:{harnessSource:"internal",kind,target:input.name,
      observed:{existingSourceId:null}}};
  }
  const input=groupAdminPayload.parse(payload);
  if (input.action==="create_group") {
    const name=input.name.toLowerCase();
    const existing=(await listUserGroups(scope.orgId)).find(group=>group.name.toLowerCase()===name);
    if (existing) throw new Error("Group name is already in use");
    return {target:input.name,definition:{harnessSource:"internal",kind,target:input.name,
      observed:{existingGroupId:null}}};
  }
  const group=await getUserGroup(scope.orgId,input.groupId);
  if (!group || group.kind!=="custom") throw new Error("Custom group is no longer available");
  const [member]=await db().select({id:app_user.id,disabledAt:app_user.disabled_at})
    .from(app_user).where(and(eq(app_user.org_id,scope.orgId),eq(app_user.id,input.userId))).limit(1);
  if (!member || member.disabledAt) throw new Error("Member is no longer active");
  const sources=(await listGroupMembers(scope.orgId,input.groupId))
    .find(row=>row.userId===input.userId)?.sources ?? [];
  if (sources.includes("local")) throw new Error("Member is already in this group");
  const target=`${input.groupId}:${input.userId}`;
  return {target,definition:{harnessSource:"internal",kind,target,
    observed:{groupName:group.name,memberSources:[...sources].sort()}}};
}
