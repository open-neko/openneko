import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import { deleteRecordSavedView } from "@/lib/records";
import { recordsApiError } from "@/lib/records-api";
import { appRedirect } from "@/lib/public-url";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ app: string; object: string; id: string }>;
};

async function remove(request: Request, context: RouteContext) {
  try {
    const [{ app, object, id }, orgId] = await Promise.all([
      context.params,
      getOrgId(),
    ]);
    const deleted = await deleteRecordSavedView({
      orgId,
      appId: app,
      objectApiName: object,
      id,
    });
    if (request.method === "DELETE") {
      return deleted
        ? new NextResponse(null, { status: 204 })
        : NextResponse.json({ error: "not found" }, { status: 404 });
    }
    return appRedirect(`/a/${app}/${object}`, 303);
  } catch (error) {
    return recordsApiError(error);
  }
}

export async function DELETE(request: Request, context: RouteContext) {
  return remove(request, context);
}

export async function POST(request: Request, context: RouteContext) {
  return remove(request, context);
}
