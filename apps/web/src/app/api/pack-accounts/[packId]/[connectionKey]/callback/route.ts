import { NextRequest, NextResponse } from "next/server";
import { packOAuthCallbackUri, readAndClearPackOAuthState } from "@/lib/pack-oauth";
import { requestPackWorker } from "@/lib/solution-packs";

export async function GET(request: NextRequest, context: { params: Promise<{ packId: string; connectionKey: string }> }) {
  const { packId, connectionKey } = await context.params;
  const saved = await readAndClearPackOAuthState();
  const code = request.nextUrl.searchParams.get("code") ?? "";
  const state = request.nextUrl.searchParams.get("state") ?? "";
  if (!saved || saved.packId !== packId || saved.connectionKey !== connectionKey || saved.state !== state || !code) {
    return NextResponse.json({ error: "OAuth callback state is missing or invalid" }, { status: 400 });
  }
  const result = await requestPackWorker(`/admin/packs/${encodeURIComponent(packId)}/oauth/${encodeURIComponent(connectionKey)}/complete`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code, codeVerifier: saved.codeVerifier, redirectUri: packOAuthCallbackUri(request, packId, connectionKey) }),
  });
  if (result.status !== 200) return NextResponse.json(result.body, { status: result.status });
  const target = new URL(saved.returnPath, packOAuthCallbackUri(request, packId, connectionKey));
  target.searchParams.set("pack", packId);
  target.searchParams.set("connected", connectionKey);
  return NextResponse.redirect(target);
}
