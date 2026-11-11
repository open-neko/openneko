import { NextResponse } from "next/server";
import { revokeDevice } from "@neko/db";
import { recordAuditEvent } from "@neko/llm/workflows";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";

/** DELETE /api/v1/admin/devices/{deviceId}: revoke a device's token at once. */
export async function DELETE(
  _request: Request,
  context: { params: Promise<{ deviceId: string }> },
) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const { deviceId } = await context.params;
  const orgId = await getOrgId();
  const revoked = await revokeDevice({ orgId, deviceId, byUserId: actor.userId });
  if (!revoked) {
    return NextResponse.json({ error: "no active device with this id" }, { status: 404 });
  }
  await recordAuditEvent({
    orgId,
    entityKind: "device",
    entityId: revoked.id,
    event: "revoked",
    payload: { byUserId: actor.userId, userId: revoked.userId, deviceName: revoked.name },
  });
  return NextResponse.json({ ok: true });
}
