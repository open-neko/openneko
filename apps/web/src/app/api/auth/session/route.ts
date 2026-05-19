/**
 * GET /api/auth/session — read the current user from the session cookie.
 *
 * Returns `{ user: { id, email, name } }` when signed in,
 * `{ user: null }` otherwise. Client components poll this on mount to
 * decide whether to render dashboard chrome or a "Sign in" CTA.
 */

import { NextResponse } from "next/server";
import { getCurrentUser } from "@/lib/auth";

export async function GET() {
  const user = await getCurrentUser();
  return NextResponse.json({ user });
}
