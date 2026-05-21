/**
 * GET /api/integrations/connect/[plugin]/callback
 *
 * Finish the OAuth dance. Steps:
 *   1. Read the state cookie. If absent / forged, fail closed.
 *   2. Verify the plugin in the URL matches the plugin in the cookie.
 *   3. Verify the state query param matches the cookie's state.
 *   4. Call completeConnect with code + redirect_uri + state + verifier;
 *      worker stores the credential under the operator's slot.
 *   5. Redirect back to the originating returnPath with ?connected=<plugin>.
 */

import { NextRequest, NextResponse } from "next/server";
import {
  buildConnectCallbackUri,
  completeConnect,
  listConnectProviders,
  readAndClearConnectStateCookie,
} from "@/lib/integrations";

export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ plugin: string }> },
) {
  const url = new URL(request.url);
  const error = url.searchParams.get("error");
  if (error) {
    const description = url.searchParams.get("error_description") ?? error;
    return NextResponse.redirect(
      new URL(`/integrations?error=${encodeURIComponent(description)}`, request.url),
      { status: 302 },
    );
  }
  const { plugin } = await params;
  const pluginName = decodeURIComponent(plugin);
  const code = url.searchParams.get("code");
  const state = url.searchParams.get("state");
  if (!code || !state) {
    return NextResponse.redirect(
      new URL("/integrations?error=missing+code+or+state", request.url),
      { status: 302 },
    );
  }
  const stored = await readAndClearConnectStateCookie();
  if (!stored) {
    return NextResponse.redirect(
      new URL("/integrations?error=state+cookie+missing+or+expired", request.url),
      { status: 302 },
    );
  }
  if (stored.pluginName !== pluginName) {
    return NextResponse.redirect(
      new URL("/integrations?error=plugin+mismatch", request.url),
      { status: 302 },
    );
  }
  if (stored.state !== state) {
    return NextResponse.redirect(
      new URL("/integrations?error=state+mismatch", request.url),
      { status: 302 },
    );
  }
  // Re-verify the plugin is still installed (rare race; operator might've
  // removed it during the IdP dance).
  const providers = await listConnectProviders();
  const provider = providers.find((p) => p.pluginName === pluginName);
  if (!provider) {
    return NextResponse.redirect(
      new URL(`/integrations?error=plugin+removed`, request.url),
      { status: 302 },
    );
  }
  try {
    await completeConnect(pluginName, {
      operatorId: stored.operatorId,
      code,
      redirectUri: buildConnectCallbackUri(request.url, pluginName),
      state,
      codeVerifier: stored.codeVerifier,
      scopes: provider.scopes,
    });
    return NextResponse.redirect(
      new URL(`${stored.returnPath}?connected=${encodeURIComponent(pluginName)}`, request.url),
      { status: 302 },
    );
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return NextResponse.redirect(
      new URL(`/integrations?error=${encodeURIComponent(message)}`, request.url),
      { status: 302 },
    );
  }
}
