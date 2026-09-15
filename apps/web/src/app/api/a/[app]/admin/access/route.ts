import { NextResponse } from "next/server";
import { and, db, eq, inArray, sso_group, user_group } from "@neko/db";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { createActionRequest } from "@neko/llm/workflows";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";
import { readJsonBody } from "@/lib/groups-admin";
import { getWebRecordsPool } from "@/lib/records";

type Context = { params: Promise<{ app: string }> };

/** Groups with access to a records app. Grants run as approved governed actions. */
export async function GET(_request: Request, context: Context) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const { app } = await context.params;
  const orgId = await getOrgId();
  const { rows } = await getWebRecordsPool().query<{ subject_id: string }>(
    `select subject_id from engine.app_access_grant where org_id = $1 and app_id = $2 and subject_type = 'group' order by subject_id`,
    [orgId, app],
  );
  const ids = rows.map((r) => r.subject_id);
  const [groups, idpGroups] = ids.length
    ? await Promise.all([
        db().select({ id: user_group.id, name: user_group.name }).from(user_group).where(and(eq(user_group.org_id, orgId), inArray(user_group.id, ids))),
        db().select({ id: sso_group.id, name: sso_group.display_name, externalId: sso_group.external_id }).from(sso_group).where(and(eq(sso_group.org_id, orgId), inArray(sso_group.id, ids))),
      ])
    : [[], []];
  const names = new Map<string, { name: string; legacy: boolean }>();
  for (const g of groups) names.set(g.id, { name: g.name, legacy: false });
  for (const g of idpGroups) names.set(g.id, { name: g.name ?? g.externalId, legacy: true });
  return NextResponse.json({
    grants: ids.map((id) => ({ groupId: id, name: names.get(id)?.name ?? id, legacy: names.get(id)?.legacy ?? false })),
  });
}

export async function POST(request: Request, context: Context) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const { app } = await context.params;
  const body = await readJsonBody(request);
  if (!body || typeof body.groupId !== "string" || typeof body.grant !== "boolean") {
    return NextResponse.json({ error: "groupId and grant are required" }, { status: 400 });
  }
  const orgId = await getOrgId();
  const kind = body.grant ? "app_access_grant" : "app_access_revoke";
  const created = await createActionRequest({
    orgId,
    scope: "internal",
    kind,
    target: app,
    payload: { app, subject_type: "group", subject_id: body.groupId },
    status: "approved",
    summary: `${body.grant ? "Grant" : "Revoke"} ${app} access for a group`,
    intent: "Administrator changed records app access from the permissions page.",
    actorUserId: actor.userId,
    actorRole: "admin",
  });
  await enqueue(QUEUE.ACTION_EXECUTE, { orgId, actionRequestId: created.id });
  return NextResponse.json({ actionRequestId: created.id }, { status: 202 });
}
