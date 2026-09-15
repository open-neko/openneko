/**
 * PATCH /api/admin/users/[userId] — change a user's role or account status.
 *
 * Body: { role?: "admin" | "member", disabled?: boolean }
 *
 * Guards:
 *  - Demoting or disabling the last active admin is refused — an org
 *    must never end up administrator-less (mirrors the bootstrap rule
 *    in upsertUserFromIdentity).
 *  - Disabling takes effect immediately for new requests: the session
 *    reader joins on `disabled_at is null` (ADM1), so existing cookies
 *    die with the flip.
 *
 * Role changes propagate to GraphJin lazily — the records read gateway
 * re-syncs engine.actor from app_user.role on the viewer's next query.
 */

import { NextRequest, NextResponse } from "next/server";
import { and, app_user, db, eq, isNull, ne, GroupError, setLocalAdministrator } from "@neko/db";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";

export async function PATCH(
  request: NextRequest,
  { params }: { params: Promise<{ userId: string }> },
) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;

  const { userId } = await params;
  let body: { role?: unknown; disabled?: unknown };
  try {
    body = await request.json();
  } catch {
    return NextResponse.json({ error: "invalid JSON body" }, { status: 400 });
  }

  const role =
    body.role === undefined
      ? undefined
      : body.role === "admin" || body.role === "member"
        ? body.role
        : null;
  if (role === null) {
    return NextResponse.json(
      { error: "role must be admin or member" },
      { status: 400 },
    );
  }
  const disabled =
    body.disabled === undefined
      ? undefined
      : typeof body.disabled === "boolean"
        ? body.disabled
        : null;
  if (disabled === null) {
    return NextResponse.json(
      { error: "disabled must be a boolean" },
      { status: 400 },
    );
  }
  if (role === undefined && disabled === undefined) {
    return NextResponse.json(
      { error: "nothing to change — pass role and/or disabled" },
      { status: 400 },
    );
  }

  const orgId = await getOrgId();
  const [target] = await db()
    .select({
      id: app_user.id,
      role: app_user.role,
      disabledAt: app_user.disabled_at,
    })
    .from(app_user)
    .where(and(eq(app_user.org_id, orgId), eq(app_user.id, userId)))
    .limit(1);
  if (!target) {
    return NextResponse.json({ error: "user not found" }, { status: 404 });
  }

  const losesAdmin =
    target.role === "admin" &&
    !target.disabledAt &&
    (role === "member" || disabled === true);
  if (losesAdmin) {
    const [otherAdmin] = await db()
      .select({ id: app_user.id })
      .from(app_user)
      .where(
        and(
          eq(app_user.org_id, orgId),
          eq(app_user.role, "admin"),
          isNull(app_user.disabled_at),
          ne(app_user.id, target.id),
        ),
      )
      .limit(1);
    if (!otherAdmin) {
      return NextResponse.json(
        { error: "refusing to remove the last active administrator" },
        { status: 409 },
      );
    }
  }

  if (role !== undefined && role !== target.role) {
    try {
      await setLocalAdministrator(orgId, target.id, role === "admin");
    } catch (error) {
      if (error instanceof GroupError) {
        return NextResponse.json({ error: error.message }, { status: error.code === "not_found" ? 404 : 409 });
      }
      throw error;
    }
  }
  if (disabled !== undefined) {
    await db()
      .update(app_user)
      .set({ disabled_at: disabled ? new Date() : null, updated_at: new Date() })
      .where(eq(app_user.id, target.id));
  }

  return NextResponse.json({
    user: {
      id: target.id,
      role: role ?? target.role,
      disabled: disabled ?? Boolean(target.disabledAt),
    },
  });
}
