import { NextResponse } from "next/server";
import { deleteModel, getModelSettings, ModelSettingsError, saveModel } from "@neko/llm/model-routes";
import { isDenied, requireAdminActor } from "@/lib/admin-auth";
import { getOrgId } from "@/lib/db";
import { readJsonBody } from "@/lib/groups-admin";

type RouteContext = { params: Promise<{ modelId: string }> };

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function failure(error: unknown): NextResponse {
  if (error instanceof ModelSettingsError) return NextResponse.json({ error: error.message }, { status: error.status });
  throw error;
}

export async function PUT(request: Request, context: RouteContext) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const { modelId } = await context.params;
  if (!UUID.test(modelId)) return NextResponse.json({ error: "No such model." }, { status: 404 });
  const body = await readJsonBody(request);
  if (!body) return NextResponse.json({ error: "A JSON body is required." }, { status: 400 });
  const orgId = await getOrgId();
  try {
    const model = await saveModel(orgId, actor.userId, modelId, body);
    return NextResponse.json({ model, settings: await getModelSettings(orgId) });
  } catch (error) {
    return failure(error);
  }
}

export async function DELETE(_request: Request, context: RouteContext) {
  const actor = await requireAdminActor();
  if (isDenied(actor)) return actor;
  const { modelId } = await context.params;
  if (!UUID.test(modelId)) return NextResponse.json({ error: "No such model." }, { status: 404 });
  const orgId = await getOrgId();
  try {
    await deleteModel(orgId, actor.userId, modelId);
    return NextResponse.json(await getModelSettings(orgId));
  } catch (error) {
    return failure(error);
  }
}
