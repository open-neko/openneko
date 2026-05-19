/**
 * GET /api/auth/callback — finish the SSO flow.
 *
 * Handles the redirect from the IdP. Steps:
 *   1. Read the state cookie. If absent / forged, fail closed.
 *   2. Verify `state` query param matches the cookie (login-CSRF gate).
 *   3. Call the auth plugin's complete_auth with the code, get an
 *      identity assertion.
 *   4. Upsert app_user from the identity.
 *   5. Set the signed session cookie, redirect to the original
 *      destination.
 *
 * Errors at any step surface as a plain text 4xx/5xx — the sign-in
 * page picks them up if the user re-tries.
 */

import { NextRequest, NextResponse } from "next/server";
import {
  buildRedirectUri,
  completeAuth,
  readAndClearStateCookie,
  upsertUserFromIdentity,
  writeSessionCookie,
} from "@/lib/auth";

export async function GET(request: NextRequest) {
  const url = new URL(request.url);
  const error = url.searchParams.get("error");
  if (error) {
    // The IdP itself rejected the request (consent denied, user
    // unprovisioned, ...). Bounce back to the sign-in page with the
    // error so the operator sees the reason rather than a blank
    // dashboard.
    const description = url.searchParams.get("error_description") ?? error;
    return NextResponse.redirect(
      new URL(
        `/signin?error=${encodeURIComponent(description)}`,
        request.url,
      ),
      { status: 302 },
    );
  }

  const code = url.searchParams.get("code");
  const state = url.searchParams.get("state");
  if (!code || !state) {
    return NextResponse.redirect(
      new URL(
        "/signin?error=missing+code+or+state+on+callback",
        request.url,
      ),
      { status: 302 },
    );
  }

  const stored = await readAndClearStateCookie();
  if (!stored) {
    return NextResponse.redirect(
      new URL(
        "/signin?error=state+cookie+missing+or+expired",
        request.url,
      ),
      { status: 302 },
    );
  }
  if (stored.state !== state) {
    // Login-CSRF check: an attacker tricking the user into clicking
    // an IdP callback URL with attacker-supplied code cannot succeed
    // here without also having forced the matching state cookie.
    return NextResponse.redirect(
      new URL("/signin?error=state+mismatch", request.url),
      { status: 302 },
    );
  }

  try {
    const identity = await completeAuth({
      code,
      redirectUri: buildRedirectUri(request.url),
      state,
    });
    const user = await upsertUserFromIdentity(identity);
    await writeSessionCookie(user.id);
    return NextResponse.redirect(new URL(stored.returnPath, request.url), {
      status: 302,
    });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return NextResponse.redirect(
      new URL(`/signin?error=${encodeURIComponent(message)}`, request.url),
      { status: 302 },
    );
  }
}
