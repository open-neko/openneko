import { NextRequest, NextResponse } from "next/server";
import { decideDeviceRequest } from "@neko/db";
import { recordAuditEvent } from "@neko/llm/workflows";
import { currentDevice, getCurrentUser } from "@/lib/auth";
import { getOrgId } from "@/lib/db";

/**
 * POST /api/v1/auth/device/approve: the signed-in person approves or denies
 * the code their installed app shows. A device token cannot approve.
 */
export async function POST(request: NextRequest) {
  if (await currentDevice()) {
    return NextResponse.json({ error: "approve a device from a browser" }, { status: 403 });
  }
  const user = await getCurrentUser();
  if (!user) return NextResponse.json({ error: "sign in first" }, { status: 401 });
  const body = (await request.json().catch(() => null)) as
    | { userCode?: unknown; approve?: unknown }
    | null;
  if (typeof body?.approve !== "boolean") {
    return NextResponse.json({ error: "approve must be true or false" }, { status: 400 });
  }
  const orgId = await getOrgId();
  const decided = await decideDeviceRequest({
    orgId,
    userCode: body.userCode,
    userId: user.id,
    approve: body.approve,
  });
  if (!decided) {
    return NextResponse.json(
      { error: "this code is not valid or has expired; start again on the device" },
      { status: 404 },
    );
  }
  await recordAuditEvent({
    orgId,
    entityKind: "device_request",
    entityId: decided.id,
    event: body.approve ? "approved" : "denied",
    payload: { userId: user.id, deviceName: decided.name },
  });
  return NextResponse.json({ ok: true, approved: body.approve, name: decided.name });
}
