import { NextResponse } from "next/server";
import { getModelSettings, ModelSettingsError, saveRoute } from "@neko/llm/model-routes";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";
import { readJsonBody } from "@/lib/groups-admin";

export async function PUT(request: Request) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const body = await readJsonBody(request);
  if (!body) return NextResponse.json({ error: "A JSON body is required." }, { status: 400 });
  const orgId = await getOrgId();
  try {
    await saveRoute(orgId, actor.userId, body);
    return NextResponse.json(await getModelSettings(orgId));
  } catch (error) {
    if (error instanceof ModelSettingsError) return NextResponse.json({ error: error.message }, { status: error.status });
    throw error;
  }
}
