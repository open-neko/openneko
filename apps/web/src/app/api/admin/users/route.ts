/**
 * POST /api/admin/users — provision a user ahead of their first sign-in.
 *
 * Exists for auth providers with `provisioning: "manual"` (magic link):
 * possession of a mailbox never mints an account, so an admin creates
 * the row (email + role) first and the sign-in flow only works for
 * emails found here. Works equally under SSO for pre-assigning a role
 * before a user's first login (the sub attaches on that login).
 *
 * Body: { email: string, name?: string, role: "admin" | "member" }
 */

import { randomBytes } from "node:crypto";
import { NextRequest, NextResponse } from "next/server";
import { and, app_user, db, eq, sql, organization, isUnclaimedSoloEmail } from "@neko/db";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export async function POST(request: NextRequest) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;

  let body: { email?: unknown; name?: unknown; role?: unknown; updateSoloAccount?: unknown };
  try {
    body = await request.json();
  } catch {
    return NextResponse.json({ error: "invalid JSON body" }, { status: 400 });
  }

  const email =
    typeof body.email === "string" ? body.email.trim().toLowerCase() : "";
  if (!EMAIL_RE.test(email) || email.length > 320) {
    return NextResponse.json(
      { error: "a valid email address is required" },
      { status: 400 },
    );
  }
  const role = body.role === "admin" ? "admin" : body.role === "member" ? "member" : null;
  if (!role) {
    return NextResponse.json(
      { error: "role must be admin or member" },
      { status: 400 },
    );
  }
  const name =
    typeof body.name === "string" && body.name.trim().length > 0
      ? body.name.trim().slice(0, 200)
      : null;

  const orgId = await getOrgId();
  const [existing] = await db()
    .select({ id: app_user.id })
    .from(app_user)
    .where(
      and(
        eq(app_user.org_id, orgId),
        sql`lower(${app_user.email}) = ${email}`,
      ),
    )
    .limit(1);
  if (existing) {
    return NextResponse.json(
      { error: `a user with email ${email} already exists` },
      { status: 409 },
    );
  }

  let id = `usr_${randomBytes(9).toString("base64url")}`;
  try {
    const created = await db().transaction(async (tx) => {
      await tx.execute(sql`select pg_advisory_xact_lock(hashtext(${"openneko.app_user:" + orgId}))`);
      if (body.updateSoloAccount === true) {
        const [org] = await tx.select({ owner: organization.solo_admin_user_id }).from(organization)
          .where(eq(organization.id, orgId)).limit(1);
        const [owner] = await tx.select({ email: app_user.email, sub: app_user.sub }).from(app_user)
          .where(and(eq(app_user.org_id, orgId), eq(app_user.id, actor.userId!))).limit(1);
        if (org?.owner !== actor.userId || !owner || owner.sub || !isUnclaimedSoloEmail(owner.email) || role !== "admin") return false;
        id = actor.userId!;
        await tx.update(app_user).set({ email, name, updated_at: new Date() }).where(eq(app_user.id, id));
        return true;
      }
      await tx.insert(app_user).values({
        id,
        sub: null,
        email,
        name,
        org_id: orgId,
        role,
      });
      return true;
    });
    if (!created) return NextResponse.json({ error: "This account cannot be updated here. Reload the page." }, { status: 409 });
  } catch (e) {
    // app_user_org_email_unique: a concurrent provision (double-click,
    // second admin tab) won the race between our lookup and this insert.
    const code =
      (e as { code?: string })?.code ??
      ((e as { cause?: { code?: string } })?.cause?.code);
    if (code === "23505") {
      return NextResponse.json(
        { error: `a user with email ${email} already exists` },
        { status: 409 },
      );
    }
    throw e;
  }
  return NextResponse.json(
    { user: { id, email, name, role } },
    { status: body.updateSoloAccount === true ? 200 : 201 },
  );
}
