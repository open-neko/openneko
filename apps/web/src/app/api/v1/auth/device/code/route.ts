import { NextRequest, NextResponse } from "next/server";
import {
  DEVICE_POLL_INTERVAL_SECONDS,
  DeviceError,
  formatUserCode,
  startDeviceRequest,
} from "@neko/db";
import { getOrgId } from "@/lib/db";
import { publicBaseUrl } from "@/lib/public-url";

/**
 * POST /api/v1/auth/device/code: the installed app asks to sign in. The
 * person opens `verificationUri` in a signed-in browser and approves the
 * code; the app polls /auth/device/token until then.
 */
export async function POST(request: NextRequest) {
  const body = (await request.json().catch(() => ({}))) as { name?: unknown } | null;
  try {
    const started = await startDeviceRequest(await getOrgId(), body?.name);
    const userCode = formatUserCode(started.userCode);
    const verificationUri = `${publicBaseUrl(request.url)}/device`;
    return NextResponse.json({
      deviceCode: started.deviceCode,
      userCode,
      verificationUri,
      verificationUriComplete: `${verificationUri}?code=${userCode}`,
      expiresIn: Math.max(0, Math.round((started.expiresAt.getTime() - Date.now()) / 1000)),
      interval: DEVICE_POLL_INTERVAL_SECONDS,
    });
  } catch (err) {
    if (err instanceof DeviceError && err.code === "too_many") {
      return NextResponse.json({ error: err.message }, { status: 429 });
    }
    throw err;
  }
}
