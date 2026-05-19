/**
 * Next.js 16 Proxy (formerly middleware) — auth gate for all routes.
 *
 * Two-mode model:
 *
 *   1. No SSO plugin installed → the app runs fully open. Every browser
 *      user gets the full app, no sign-in required. This is the
 *      single-operator / laptop deployment.
 *
 *   2. An SSO plugin is installed (worker reports a provider on
 *      /admin/auth/status) → every route is gated. A request without a
 *      valid signed session cookie is 302'd to /signin with returnTo
 *      pointing at the original URL.
 *
 * The mode is detected per-request (cached 1s) by asking the worker,
 * so installing or removing the plugin takes effect on the next cache
 * miss without a web restart — same hot-reload model as the rest of
 * the plugin system.
 *
 * Paths exempt from the gate:
 *   - /signin                — the sign-in page itself
 *   - /api/auth/*            — the SSO flow endpoints
 *   - _next/static, _next/image, favicon.ico — static assets
 *
 * Session verification here is the HMAC check ONLY. We do not hit the
 * database — the per-route handler (or `getCurrentUser`) does the
 * follow-up DB lookup if it needs the user row. Per the Next.js docs,
 * proxy should not be the sole authorisation layer; we use it for
 * optimistic redirects.
 *
 * Note: Server Functions appear as POSTs to the page they're invoked
 * from, so the matcher's page-level gate covers them. If a future
 * refactor moves a Server Function to a route the matcher excludes,
 * the gate is silently lost — re-verify auth inside any Server
 * Function that performs sensitive work.
 */

import { NextResponse, type NextRequest } from "next/server";
import { createHmac, timingSafeEqual } from "node:crypto";

const SESSION_COOKIE_NAME = "openneko_session";
const PROVIDER_CACHE_TTL_MS = 1_000;
const WORKER_STATUS_TIMEOUT_MS = 1_500;

interface ProviderProbe {
  installed: boolean;
  at: number;
}

let providerCache: ProviderProbe | null = null;

function workerAdminBase(): string {
  return (process.env.WORKER_ADMIN_URL ?? "http://127.0.0.1:4100").replace(
    /\/+$/,
    "",
  );
}

/**
 * Ask the worker whether an SSO plugin is installed. Cached for one
 * second to keep proxy overhead negligible under load while still
 * picking up `openneko install` within a second of the manifest write.
 *
 * Fails OPEN (treats as "no plugin") when the worker is unreachable —
 * a dev environment with no worker running shouldn't be locked out.
 * If an operator has actually configured SSO and their worker is down,
 * action execution is already broken and that's where they'll notice.
 */
export async function isAuthPluginInstalled(): Promise<boolean> {
  const now = Date.now();
  if (providerCache && now - providerCache.at < PROVIDER_CACHE_TTL_MS) {
    return providerCache.installed;
  }
  try {
    const res = await fetch(`${workerAdminBase()}/admin/auth/status`, {
      method: "GET",
      signal: AbortSignal.timeout(WORKER_STATUS_TIMEOUT_MS),
    });
    if (!res.ok) {
      providerCache = { installed: false, at: now };
      return false;
    }
    const body = (await res.json()) as { provider: { pluginName: string } | null };
    const installed = body.provider != null;
    providerCache = { installed, at: now };
    return installed;
  } catch {
    providerCache = { installed: false, at: now };
    return false;
  }
}

/** Test seam — clears the provider cache between tests. */
export function _resetProviderCache(): void {
  providerCache = null;
}

/**
 * HMAC-verify the signed session cookie. Mirrors `decodeSession` in
 * @/lib/auth but doesn't throw on missing/short secret — the proxy
 * treats that as "no valid session" so a misconfigured deployment
 * redirects to /signin rather than 500ing every page load.
 */
export function verifySessionCookie(value: string | undefined): boolean {
  if (!value) return false;
  const secret = process.env.OPENNEKO_SESSION_SECRET;
  if (!secret || secret.length < 32) return false;
  const parts = value.split(".");
  if (parts.length !== 3) return false;
  const [userId, expiresAtRaw, mac] = parts;
  if (!userId || !expiresAtRaw || !mac) return false;
  const body = `${userId}.${expiresAtRaw}`;
  const expected = createHmac("sha256", secret).update(body).digest("base64url");
  const a = Buffer.from(mac);
  const b = Buffer.from(expected);
  if (a.length !== b.length) return false;
  if (!timingSafeEqual(a, b)) return false;
  const expiresAt = Number.parseInt(expiresAtRaw, 10);
  if (!Number.isFinite(expiresAt)) return false;
  if (expiresAt < Math.floor(Date.now() / 1000)) return false;
  return true;
}

export async function proxy(request: NextRequest): Promise<NextResponse> {
  if (!(await isAuthPluginInstalled())) {
    return NextResponse.next();
  }
  const cookie = request.cookies.get(SESSION_COOKIE_NAME)?.value;
  if (verifySessionCookie(cookie)) {
    return NextResponse.next();
  }
  const returnTo = request.nextUrl.pathname + request.nextUrl.search;
  const url = request.nextUrl.clone();
  url.pathname = "/signin";
  url.search = `?returnTo=${encodeURIComponent(returnTo)}`;
  return NextResponse.redirect(url, { status: 302 });
}

export const config = {
  // Match everything except the SSO flow surfaces and static assets.
  // Negative lookahead is a constant here so Next can statically
  // analyse it at build time (per the proxy.md API reference).
  matcher: [
    "/((?!signin|api/auth/|_next/static/|_next/image/|favicon\\.ico).*)",
  ],
};
