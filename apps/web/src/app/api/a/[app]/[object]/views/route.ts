import { NextResponse } from "next/server";
import { getOrgId } from "@/lib/db";
import {
  listRecordSavedViews,
  saveRecordSavedView,
} from "@/lib/records";
import { recordsApiError } from "@/lib/records-api";
import { appRedirect } from "@/lib/public-url";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ app: string; object: string }> };

export async function GET(_request: Request, context: RouteContext) {
  try {
    const [{ app, object }, orgId] = await Promise.all([context.params, getOrgId()]);
    const views = await listRecordSavedViews({
      orgId,
      appId: app,
      objectApiName: object,
    });
    return NextResponse.json({ views });
  } catch (error) {
    return recordsApiError(error);
  }
}

async function body(request: Request): Promise<{
  label: string;
  shared: boolean;
  definition: unknown;
  wantsJson: boolean;
}> {
  const contentType = request.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    const value = (await request.json()) as unknown;
    const parsed = value && typeof value === "object" && !Array.isArray(value)
      ? value as Record<string, unknown>
      : {};
    return {
      label: typeof parsed.label === "string" ? parsed.label : "",
      shared: parsed.shared === true,
      definition: parsed.definition,
      wantsJson: true,
    };
  }
  const form = await request.formData();
  const rawDefinition = form.get("definition");
  return {
    label: String(form.get("label") ?? ""),
    shared: form.get("shared") === "true",
    definition:
      typeof rawDefinition === "string" ? JSON.parse(rawDefinition) : null,
    wantsJson: false,
  };
}

export async function POST(request: Request, context: RouteContext) {
  try {
    const [{ app, object }, orgId, input] = await Promise.all([
      context.params,
      getOrgId(),
      body(request),
    ]);
    const view = await saveRecordSavedView({
      orgId,
      appId: app,
      objectApiName: object,
      label: input.label,
      shared: input.shared,
      definition: input.definition,
    });
    if (input.wantsJson) return NextResponse.json({ view }, { status: 201 });
    return appRedirect(`/a/${app}/${object}?${new URLSearchParams({ view: view.id })}`, 303);
  } catch (error) {
    if (error instanceof SyntaxError) {
      return NextResponse.json({ error: "saved view definition is malformed" }, { status: 400 });
    }
    return recordsApiError(error);
  }
}
