import { randomBytes } from "node:crypto";
import { and, eq, isNull, sql } from "drizzle-orm";
import { db } from "./index";
import { app_user, organization, work_thread, workflow_run } from "./schema";

export function isUnclaimedSoloEmail(email: string): boolean {
  return email.endsWith("@solo.openneko.invalid");
}

async function readOwner(orgId: string, runner = db()) {
  const [row] = await runner.select({
    id: app_user.id, email: app_user.email, name: app_user.name,
    role: app_user.role, disabledAt: app_user.disabled_at,
  }).from(organization).innerJoin(app_user, eq(organization.solo_admin_user_id, app_user.id))
    .where(and(eq(organization.id, orgId), eq(app_user.org_id, orgId))).limit(1);
  return row;
}

/** Call only after the auth gate confirms this is a solo installation. */
export async function getOrCreateSoloAdmin(orgId: string) {
  const existing = await readOwner(orgId);
  if (existing) return existing.role === "admin" && !existing.disabledAt ? existing : null;
  return db().transaction(async (tx) => {
    await tx.execute(sql`select pg_advisory_xact_lock(hashtext(${"openneko.app_user:" + orgId}))`);
    const [org] = await tx.select({ owner: organization.solo_admin_user_id })
      .from(organization).where(eq(organization.id, orgId)).limit(1);
    if (!org) throw new Error("Organization not found");
    let ownerId = org.owner;
    if (!ownerId) {
      const candidates = await tx.select({ id: app_user.id }).from(app_user)
        .where(and(eq(app_user.org_id, orgId), eq(app_user.role, "admin"), isNull(app_user.sub), isNull(app_user.disabled_at)))
        .limit(2);
      // Adopt an unambiguous existing local admin. Otherwise persist a dedicated
      // local operator rather than attributing their work to an arbitrary user.
      ownerId = candidates.length === 1 ? candidates[0].id : `usr_${randomBytes(9).toString("base64url")}`;
      if (candidates.length !== 1) await tx.insert(app_user).values({
        id: ownerId, org_id: orgId, role: "admin", name: "Solo administrator",
        email: `${ownerId}@solo.openneko.invalid`,
      });
      await tx.update(organization).set({ solo_admin_user_id: ownerId }).where(eq(organization.id, orgId));
      // These are exactly the unowned local chats visible to the old solo
      // principal. Move ownership atomically so assigning an ID cannot hide them.
      // Keep channel/workflow histories and already-owned chats untouched.
      await tx.update(work_thread).set({ created_by_user_id: ownerId }).where(and(
        eq(work_thread.org_id, orgId), eq(work_thread.channel, "web"),
        isNull(work_thread.created_by_user_id),
        sql`not exists (select 1 from ${workflow_run} where ${workflow_run.thread_id} = ${work_thread.id})`,
      ));
    }
    const [owner] = await tx.select({ id: app_user.id, email: app_user.email, name: app_user.name,
      role: app_user.role, disabledAt: app_user.disabled_at }).from(app_user)
      .where(and(eq(app_user.id, ownerId), eq(app_user.org_id, orgId))).limit(1);
    return owner?.role === "admin" && !owner.disabledAt ? owner : null;
  });
}

/** SSO setup must collect a real mailbox before switching this owner to sign-in. */
export async function soloAdminNeedsEmail(orgId: string): Promise<boolean> {
  const owner = await readOwner(orgId);
  return Boolean(owner && isUnclaimedSoloEmail(owner.email));
}
