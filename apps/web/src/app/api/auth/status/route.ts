/**
 * GET /api/auth/status — does the operator have an SSO plugin installed?
 *
 * The sign-in page calls this on mount to decide whether to render a
 * "Sign in with <provider>" button. Result is the auth plugin's
 * package name + the human-readable label its register() declared.
 * If no plugin is installed, returns `{ provider: null }` and the
 * sign-in page renders the legacy local-auth notice.
 *
 * The web process doesn't query the worker on every request — the
 * worker's /admin/auth/status is itself a cheap in-memory read, and
 * @/lib/auth wraps it with a 1-second TTL cache so concurrent loads
 * don't fan out.
 */

import { NextResponse } from "next/server";
import { getAuthProvider } from "@/lib/auth";

export async function GET() {
  const provider = await getAuthProvider();
  return NextResponse.json({ provider });
}
