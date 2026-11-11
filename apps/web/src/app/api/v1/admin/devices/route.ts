import { NextResponse } from "next/server";
import { listDevices } from "@neko/db";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";

/** GET /api/v1/admin/devices: every installed app that signed in, newest use first. */
export async function GET() {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  return NextResponse.json({ devices: await listDevices(await getOrgId()) });
}
