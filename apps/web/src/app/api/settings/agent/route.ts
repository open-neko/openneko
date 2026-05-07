import { NextRequest, NextResponse } from "next/server";
import { provisionHostConfig } from "@neko/llm";
import { getOrgId } from "@/lib/db";
import {
  getAgentSettingsPayload,
  saveAgentBackendDraft,
} from "@/lib/agent-backend-settings";

export async function GET() {
  const payload = await getAgentSettingsPayload((await getOrgId()));
  return NextResponse.json(payload);
}

export async function PUT(request: NextRequest) {
  try {
    const body = await request.json();
    const saved = await saveAgentBackendDraft((await getOrgId()), {
      backend: String(body.backend ?? ""),
      globalCap: body.globalCap,
    });
    // Backend switch may flip the primary provider (auto-coerce to Anthropic
    // for claude-agent). Reprovision so Hermes config matches the new state.
    await provisionHostConfig((await getOrgId()));
    return NextResponse.json(saved);
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    return NextResponse.json({ error: message }, { status: 400 });
  }
}
