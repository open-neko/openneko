import { NextRequest, NextResponse } from "next/server";
import { provisionHostConfig } from "@neko/llm";
import { getOrgId } from "@/lib/db";
import {
  getProviderSettingsPayload,
  saveProviderDraft,
} from "@/lib/provider-settings";

export async function GET() {
  const payload = await getProviderSettingsPayload((await getOrgId()));
  return NextResponse.json(payload);
}

export async function PUT(request: NextRequest) {
  try {
    const body = await request.json();
    const saved = await saveProviderDraft((await getOrgId()), body);
    // Saving the primary provider rewrites the Hermes config + key file so
    // the next agent run picks up the new credentials without a restart.
    if (body?.scope === "primary") {
      await provisionHostConfig((await getOrgId()));
    }
    return NextResponse.json(saved);
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    return NextResponse.json({ error: message }, { status: 400 });
  }
}
