/**
 * POST /api/v1/auth/logout: clear the session cookie. An installed app that
 * sends its device token revokes that token.
 *
 * No IdP single-logout call. (Single-logout adds appreciable complexity
 * for marginal benefit; if the operator needs it, the IdP's own session
 * cleanup handles it for them.)
 */

import { NextResponse } from "next/server";
import { revokeDevice } from "@neko/db";
import { recordAuditEvent } from "@neko/llm/workflows";
import { clearSessionCookie, currentDevice } from "@/lib/auth";
import { getOrgId } from "@/lib/db";

export async function POST() {
  const device = await currentDevice().catch(() => null);
  if (device) {
    const orgId = await getOrgId();
    const revoked = await revokeDevice({ orgId, deviceId: device.deviceId, byUserId: device.userId });
    if (revoked) {
      await recordAuditEvent({
        orgId,
        entityKind: "device",
        entityId: revoked.id,
        event: "signed_out",
        payload: { userId: revoked.userId, deviceName: revoked.name },
      });
    }
    return NextResponse.json({ ok: true });
  }
  await clearSessionCookie();
  return NextResponse.json({ ok: true });
}
