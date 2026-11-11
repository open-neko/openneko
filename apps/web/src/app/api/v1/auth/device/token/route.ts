import { NextRequest, NextResponse } from "next/server";
import { DeviceError, redeemDeviceRequest } from "@neko/db";
import { getOrgId } from "@/lib/db";

const OAUTH_ERRORS = {
  pending: "authorization_pending",
  slow_down: "slow_down",
  denied: "access_denied",
  expired: "expired_token",
} as const;

/**
 * POST /api/v1/auth/device/token: the installed app polls with its device
 * code. After approval it gets its token once; the server keeps a digest.
 * The error names follow RFC 8628.
 */
export async function POST(request: NextRequest) {
  const body = (await request.json().catch(() => null)) as { deviceCode?: unknown } | null;
  try {
    const result = await redeemDeviceRequest(await getOrgId(), body?.deviceCode);
    if (result.status === "issued") {
      return NextResponse.json(
        { token: result.token, deviceId: result.deviceId },
        { headers: { "Cache-Control": "no-store" } },
      );
    }
    return NextResponse.json({ error: OAUTH_ERRORS[result.status] }, { status: 400 });
  } catch (err) {
    if (err instanceof DeviceError) {
      return NextResponse.json({ error: "invalid_request", message: err.message }, { status: 400 });
    }
    throw err;
  }
}
