/**
 * GET /api/auth/session — read the current user from the session cookie.
 *
 * Returns `{ user: { id, email, name } }` when signed in,
 * `{ user: null }` otherwise. Client components poll this on mount to
 * decide whether to render dashboard chrome or a "Sign in" CTA.
 */

import { NextResponse } from "next/server";
import { getCurrentUser, getAuthProvider } from "@/lib/auth";
import { getCurrentActor } from "@/lib/actor";

export async function GET() {
  const user = await getCurrentUser();
  const actor = user ? await getCurrentActor() : null;
  return NextResponse.json({ user, role: actor?.role ?? null, authEnabled: Boolean(await getAuthProvider()) });
}
