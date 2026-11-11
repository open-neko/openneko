import { NextResponse } from "next/server";
import { externalServiceStatus } from "@neko/llm/external-services";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";

export async function GET(request: Request) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const refresh = new URL(request.url).searchParams.get("refresh") === "1";
  return NextResponse.json({ services: await externalServiceStatus(await getOrgId(), { refresh }) });
}
