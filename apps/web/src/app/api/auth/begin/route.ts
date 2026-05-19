/**
 * GET /api/auth/begin — start the SSO flow.
 *
 * Mint a CSRF state token, persist it (plus the return path) in an
 * httpOnly cookie, ask the auth plugin for its IdP authorization URL,
 * and 302 the user there. The IdP eventually bounces back to
 * /api/auth/callback with `code` + `state`, where the cookie's state
 * is checked against the IdP-echoed state to defeat login-CSRF.
 *
 * Query params:
 *   ?returnTo=/some/internal/path  — optional, defaults to "/"
 *   ?loginHint=user@example.com    — forwarded to the IdP as login_hint
 */

import { NextRequest, NextResponse } from "next/server";
import {
  beginAuth,
  buildRedirectUri,
  getAuthProvider,
  newStateToken,
  writeStateCookie,
} from "@/lib/auth";

export async function GET(request: NextRequest) {
  const provider = await getAuthProvider();
  if (!provider) {
    return NextResponse.json(
      { error: "no SSO plugin installed" },
      { status: 503 },
    );
  }
  const url = new URL(request.url);
  const returnTo = url.searchParams.get("returnTo") ?? "/";
  const loginHint = url.searchParams.get("loginHint");
  const state = newStateToken();
  await writeStateCookie(state, returnTo);

  const redirectUri = buildRedirectUri(request.url);
  try {
    const { authorizationUrl } = await beginAuth({
      redirectUri,
      state,
      loginHint: loginHint && loginHint.length > 0 ? loginHint : null,
    });
    return NextResponse.redirect(authorizationUrl, { status: 302 });
  } catch (err) {
    return NextResponse.json(
      { error: err instanceof Error ? err.message : String(err) },
      { status: 502 },
    );
  }
}
