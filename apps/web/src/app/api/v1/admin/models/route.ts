import { NextResponse } from "next/server";
import { getModelSettings, ModelSettingsError, saveModel } from "@neko/llm/model-routes";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";
import { readJsonBody } from "@/lib/groups-admin";

export async function GET() {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  return NextResponse.json(await getModelSettings(await getOrgId()));
}

export async function POST(request: Request) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const body = await readJsonBody(request);
  if (!body) return NextResponse.json({ error: "A JSON body is required." }, { status: 400 });
  const orgId = await getOrgId();
  try {
    const model = await saveModel(orgId, actor.userId, undefined, body);
    return NextResponse.json({ model, settings: await getModelSettings(orgId) }, { status: 201 });
  } catch (error) {
    if (error instanceof ModelSettingsError) return NextResponse.json({ error: error.message }, { status: error.status });
    throw error;
  }
}
