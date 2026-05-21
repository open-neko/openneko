/**
 * GET /api/integrations/connect/[plugin]/start
 *
 * Begin the OAuth flow for the current operator against the named
 * connect-capable plugin. Mints CSRF state + PKCE code_verifier,
 * stashes both in a signed cookie, asks the plugin to build the
 * authorization URL (via the worker admin port), then 302s the
 * browser there. The matching /callback finishes the dance.
 */

import { NextRequest, NextResponse } from "next/server";
import { getCurrentUser } from "@/lib/auth";
import {
  beginConnect,
  buildConnectCallbackUri,
  listConnectProviders,
  newPkceVerifier,
  newStateToken,
  writeConnectStateCookie,
} from "@/lib/integrations";

export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ plugin: string }> },
) {
  const user = await getCurrentUser();
  if (!user) {
    return NextResponse.json({ error: "not signed in" }, { status: 401 });
  }
  const { plugin } = await params;
  const pluginName = decodeURIComponent(plugin);
  if (!pluginName) {
    return NextResponse.json({ error: "plugin param required" }, { status: 400 });
  }
  // Verify the plugin is installed + declares connect; gives the operator
  // a clear 404 instead of a worker-side error when they hit a stale link.
  const providers = await listConnectProviders();
  const provider = providers.find((p) => p.pluginName === pluginName);
  if (!provider) {
    return NextResponse.json(
      { error: `connect plugin "${pluginName}" not installed` },
      { status: 404 },
    );
  }
  const url = new URL(request.url);
  const returnTo = url.searchParams.get("returnTo") ?? "/integrations";
  const state = newStateToken();
  const codeVerifier = newPkceVerifier();
  await writeConnectStateCookie({
    pluginName,
    operatorId: user.id,
    state,
    codeVerifier,
    returnPath: returnTo,
  });
  const redirectUri = buildConnectCallbackUri(request.url, pluginName);
  try {
    const { authorizationUrl } = await beginConnect(pluginName, {
      operatorId: user.id,
      redirectUri,
      state,
      scopes: provider.scopes,
      codeVerifier,
    });
    return NextResponse.redirect(authorizationUrl, { status: 302 });
  } catch (err) {
    return NextResponse.json(
      { error: err instanceof Error ? err.message : String(err) },
      { status: 502 },
    );
  }
}
