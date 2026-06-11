/**
 * SSO integration for the web app.
 *
 * Today OpenNeko's only auth is "share the password with whoever needs
 * in" — fine for a laptop deployment, fatal for any org with an IdP.
 * This module wires the web's sign-in flow through an installed auth
 * plugin (e.g. Scalekit) so an enterprise operator gets standard
 * OIDC SSO without the core having to know which IdP they use.
 *
 * Topology: the auth plugin lives in the worker's sandbox VM,
 * not in this Next process. We reach it through the worker's admin
 * HTTP endpoint on localhost (loopback inside the deployment, never
 * exposed). That gives us:
 *   - Secrets stay inside the worker's per-plugin VM.
 *   - The web process never needs the IdP client_secret.
 *   - Hot reload of the plugin doesn't restart the web.
 *
 * Session model: a stateless signed cookie. The session_id is the
 * app_user.id; an HMAC over `${user_id}.${expiresAt}` prevents
 * tampering and binds the cookie to OPENNEKO_SESSION_SECRET. Logging
 * a user out is a cookie delete; rotating the secret invalidates
 * every existing session globally.
 */

import { createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import { cookies } from "next/headers";
import { and, app_user, db, eq, isNull } from "@neko/db";
import { getOrgId } from "@/lib/db";

export const SESSION_COOKIE_NAME = "openneko_session";
export const STATE_COOKIE_NAME = "openneko_sso_state";

/** Session lifetime — 12h. Re-auths after this; refresh-on-use is a v2. */
const SESSION_TTL_SECONDS = 60 * 60 * 12;
/** State cookie lifetime — short window for the user to complete the IdP dance. */
const STATE_TTL_SECONDS = 10 * 60;

export interface AuthProviderInfo {
  pluginName: string;
  providerLabel: string;
}

export interface AuthIdentity {
  sub: string;
  email: string;
  name?: string | null;
  orgId?: string | null;
  groups?: string[];
}

export interface SessionPayload {
  userId: string;
  email: string;
  name: string | null;
  expiresAt: number;
}

function workerAdminBase(): string {
  const raw = process.env.WORKER_ADMIN_URL ?? "http://127.0.0.1:4100";
  return raw.replace(/\/+$/, "");
}

function sessionSecret(): string {
  // OPENNEKO_SESSION_SECRET is the HMAC key for session cookies. We
  // refuse to start an SSO flow without one — silently falling back to
  // a process-random secret would invalidate sessions on every restart
  // and (worse) let a misconfigured deployment look like it's working.
  const secret = process.env.OPENNEKO_SESSION_SECRET;
  if (!secret || secret.length < 32) {
    throw new Error(
      "OPENNEKO_SESSION_SECRET must be set to a value at least 32 characters long for SSO sessions",
    );
  }
  return secret;
}

/**
 * Ask the worker whether an SSO plugin is installed. Cached for one
 * second to keep the sign-in page snappy under concurrent loads —
 * the worker's hot-reload window is in seconds anyway.
 */
let providerCache: { value: AuthProviderInfo | null; at: number } | null = null;
const PROVIDER_CACHE_TTL_MS = 1000;

export async function getAuthProvider(): Promise<AuthProviderInfo | null> {
  const now = Date.now();
  if (providerCache && now - providerCache.at < PROVIDER_CACHE_TTL_MS) {
    return providerCache.value;
  }
  try {
    const res = await fetch(`${workerAdminBase()}/admin/auth/status`, {
      method: "GET",
      cache: "no-store",
      signal: AbortSignal.timeout(2000),
    });
    if (!res.ok) {
      providerCache = { value: null, at: now };
      return null;
    }
    const body = (await res.json()) as { provider: AuthProviderInfo | null };
    providerCache = { value: body.provider ?? null, at: now };
    return providerCache.value;
  } catch {
    // Worker unreachable — treat as "no provider". The dashboard
    // gracefully degrades to local auth in that case.
    providerCache = { value: null, at: now };
    return null;
  }
}

/** Test seam — clears the provider cache between tests. */
export function _resetAuthProviderCache() {
  providerCache = null;
}

export async function beginAuth(params: {
  redirectUri: string;
  state: string;
  loginHint?: string | null;
}): Promise<{ authorizationUrl: string }> {
  const res = await fetch(`${workerAdminBase()}/admin/auth/begin`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
    signal: AbortSignal.timeout(15_000),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(
      `auth plugin begin failed (${res.status}): ${text || res.statusText}`,
    );
  }
  return (await res.json()) as { authorizationUrl: string };
}

/**
 * Plugin action descriptors snapshot. /work hands these to the agent's
 * tool builder so it can register one MCP tool per registered plugin
 * kind. Fetched fresh per turn — the registry hot-reloads on
 * `openneko install`, and the agent should pick up new kinds without
 * a web restart. Best-effort: an unreachable worker yields an empty
 * list (the agent simply won't have plugin tools that turn).
 */
export interface PluginActionDescriptor {
  kind: string;
  description: string;
  default_mode?:
    | "auto"
    | "ask"
    | "deny"
    | {
        external?: "auto" | "ask" | "deny";
        internal?: "auto" | "ask" | "deny";
      };
  example?: Record<string, unknown>;
}

export async function getPluginActionDescriptors(): Promise<
  PluginActionDescriptor[]
> {
  try {
    const res = await fetch(
      `${workerAdminBase()}/admin/plugins/action-descriptors`,
      {
        method: "GET",
        cache: "no-store",
        signal: AbortSignal.timeout(2000),
      },
    );
    if (!res.ok) return [];
    const body = (await res.json()) as {
      descriptors?: PluginActionDescriptor[];
    };
    return body.descriptors ?? [];
  } catch {
    return [];
  }
}

export async function completeAuth(params: {
  code: string;
  redirectUri: string;
  state: string;
}): Promise<AuthIdentity> {
  const res = await fetch(`${workerAdminBase()}/admin/auth/complete`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
    signal: AbortSignal.timeout(30_000),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(
      `auth plugin complete failed (${res.status}): ${text || res.statusText}`,
    );
  }
  const body = (await res.json()) as { identity: AuthIdentity };
  return body.identity;
}

/**
 * Mint a state token: random nonce paired with the path the user was
 * trying to reach. The path is stashed in the cookie too so callback
 * can redirect back without trusting an open redirect parameter.
 */
export function newStateToken(): string {
  return randomBytes(24).toString("base64url");
}

/**
 * Upsert `app_user` for an SSO identity. Match priority:
 *   1. existing row with the same `sub` (IdP-stable);
 *   2. existing row with the same `email` (initial migration when an
 *      operator first turns SSO on against a pre-existing email-only
 *      user) — `sub` is then attached for future logins.
 * Otherwise insert a new row in the only org.
 */
export async function upsertUserFromIdentity(
  identity: AuthIdentity,
): Promise<{ id: string; email: string; name: string | null }> {
  const orgId = await getOrgId();
  // Primary lookup: sub. Anything matching wins, regardless of email
  // changes (people get married, change addresses — sub doesn't).
  const bySub = await db()
    .select({
      id: app_user.id,
      email: app_user.email,
      name: app_user.name,
    })
    .from(app_user)
    .where(and(eq(app_user.org_id, orgId), eq(app_user.sub, identity.sub)))
    .limit(1);
  if (bySub[0]) {
    await db()
      .update(app_user)
      .set({
        email: identity.email,
        name: identity.name ?? null,
        last_login_at: new Date(),
        updated_at: new Date(),
      })
      .where(eq(app_user.id, bySub[0].id));
    return {
      id: bySub[0].id,
      email: identity.email,
      name: identity.name ?? null,
    };
  }
  // Migration lookup: same email, no sub yet. Attach the sub.
  const byEmail = await db()
    .select({
      id: app_user.id,
      email: app_user.email,
      name: app_user.name,
      sub: app_user.sub,
    })
    .from(app_user)
    .where(and(eq(app_user.org_id, orgId), eq(app_user.email, identity.email)))
    .limit(1);
  if (byEmail[0] && !byEmail[0].sub) {
    await db()
      .update(app_user)
      .set({
        sub: identity.sub,
        name: identity.name ?? byEmail[0].name ?? null,
        last_login_at: new Date(),
        updated_at: new Date(),
      })
      .where(eq(app_user.id, byEmail[0].id));
    return {
      id: byEmail[0].id,
      email: identity.email,
      name: identity.name ?? byEmail[0].name ?? null,
    };
  }
  if (byEmail[0]) {
    // Same email, *different* sub already on file. Refuse rather than
    // silently take over — the operator should resolve this manually
    // (likely two IdP accounts pointing at the same mailbox).
    throw new Error(
      `app_user.email ${identity.email} is already bound to a different SSO subject; remove the row or update sub manually before re-attempting`,
    );
  }
  // Brand new user.
  const newId = `usr_${randomBytes(9).toString("base64url")}`;
  await db()
    .insert(app_user)
    .values({
      id: newId,
      sub: identity.sub,
      email: identity.email,
      name: identity.name ?? null,
      org_id: orgId,
      role: defaultRoleForGroups(identity.groups ?? []),
      last_login_at: new Date(),
    });
  return {
    id: newId,
    email: identity.email,
    name: identity.name ?? null,
  };
}

/**
 * Coarse-grained role mapping. Anyone with an `admin` or
 * `owners` group becomes admin; everyone else is `member`. Operators
 * with richer requirements override `app_user.role` directly until
 * we ship a configurable mapping screen.
 */
function defaultRoleForGroups(groups: string[]): string {
  const lower = new Set(groups.map((g) => g.toLowerCase()));
  if (lower.has("admin") || lower.has("admins") || lower.has("owners")) {
    return "admin";
  }
  return "member";
}

export function encodeSession(payload: SessionPayload): string {
  const body = `${payload.userId}.${payload.expiresAt}`;
  const mac = createHmac("sha256", sessionSecret()).update(body).digest("base64url");
  return `${body}.${mac}`;
}

export function decodeSession(value: string): SessionPayload | null {
  const parts = value.split(".");
  if (parts.length !== 3) return null;
  const [userId, expiresAtRaw, mac] = parts;
  if (!userId || !expiresAtRaw || !mac) return null;
  const body = `${userId}.${expiresAtRaw}`;
  const expected = createHmac("sha256", sessionSecret())
    .update(body)
    .digest("base64url");
  const a = Buffer.from(mac);
  const b = Buffer.from(expected);
  if (a.length !== b.length) return null;
  if (!timingSafeEqual(a, b)) return null;
  const expiresAt = Number.parseInt(expiresAtRaw, 10);
  if (!Number.isFinite(expiresAt)) return null;
  if (expiresAt < Math.floor(Date.now() / 1000)) return null;
  return { userId, expiresAt, email: "", name: null };
}

export async function writeSessionCookie(userId: string) {
  const expiresAt = Math.floor(Date.now() / 1000) + SESSION_TTL_SECONDS;
  const token = encodeSession({ userId, expiresAt, email: "", name: null });
  const jar = await cookies();
  jar.set(SESSION_COOKIE_NAME, token, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    maxAge: SESSION_TTL_SECONDS,
  });
}

export async function clearSessionCookie() {
  const jar = await cookies();
  jar.delete(SESSION_COOKIE_NAME);
}

export async function writeStateCookie(state: string, returnPath: string) {
  // State + intended landing path are kept server-side via a signed
  // cookie so the callback can verify both without an extra DB lookup
  // and without trusting the redirect_uri query string.
  const payload = `${state}|${encodeReturnPath(returnPath)}`;
  const mac = createHmac("sha256", sessionSecret()).update(payload).digest("base64url");
  const value = `${payload}.${mac}`;
  const jar = await cookies();
  jar.set(STATE_COOKIE_NAME, value, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "lax",
    path: "/",
    maxAge: STATE_TTL_SECONDS,
  });
}

export async function readAndClearStateCookie(): Promise<
  { state: string; returnPath: string } | null
> {
  const jar = await cookies();
  const raw = jar.get(STATE_COOKIE_NAME)?.value;
  if (!raw) return null;
  jar.delete(STATE_COOKIE_NAME);
  const split = raw.lastIndexOf(".");
  if (split <= 0) return null;
  const payload = raw.slice(0, split);
  const mac = raw.slice(split + 1);
  const expected = createHmac("sha256", sessionSecret())
    .update(payload)
    .digest("base64url");
  const a = Buffer.from(mac);
  const b = Buffer.from(expected);
  if (a.length !== b.length || !timingSafeEqual(a, b)) return null;
  const pipe = payload.indexOf("|");
  if (pipe <= 0) return null;
  const state = payload.slice(0, pipe);
  const returnPath = decodeReturnPath(payload.slice(pipe + 1));
  return { state, returnPath };
}

function encodeReturnPath(p: string): string {
  // Whitelist to internal paths only. An attacker who controls the
  // sign-in link cannot smuggle an external redirect through the
  // state cookie.
  if (!p.startsWith("/") || p.startsWith("//")) return "/";
  return Buffer.from(p).toString("base64url");
}

function decodeReturnPath(encoded: string): string {
  try {
    const raw = Buffer.from(encoded, "base64url").toString("utf8");
    if (!raw.startsWith("/") || raw.startsWith("//")) return "/";
    return raw;
  } catch {
    return "/";
  }
}

/**
 * Resolve the current session (cookie → DB lookup → user). Returns
 * null when no session, an expired session, or a session whose user
 * row has been deleted (IT deprovisioned them in the IdP and a
 * background sweep deleted the row). Pages calling this can choose
 * to redirect to /signin or render an unauthenticated view.
 */
export async function getCurrentUser(): Promise<{
  id: string;
  email: string;
  name: string | null;
} | null> {
  const jar = await cookies();
  const token = jar.get(SESSION_COOKIE_NAME)?.value;
  if (!token) return null;
  const session = decodeSession(token);
  if (!session) return null;
  const rows = await db()
    .select({
      id: app_user.id,
      email: app_user.email,
      name: app_user.name,
    })
    .from(app_user)
    // ADM1: a deactivated user's cookie is dead, not just their sign-in.
    .where(and(eq(app_user.id, session.userId), isNull(app_user.disabled_at)))
    .limit(1);
  if (!rows[0]) return null;
  return rows[0];
}

/**
 * Build the absolute callback URL the IdP will redirect to. We compute
 * it from the incoming request rather than env so dev and prod work
 * with the same code. Operators wanting to force a canonical host can
 * set OPENNEKO_PUBLIC_URL (e.g. behind a load balancer that strips
 * Host headers).
 */
export function buildRedirectUri(requestUrl: string): string {
  const override = process.env.OPENNEKO_PUBLIC_URL?.replace(/\/+$/, "");
  if (override) return `${override}/api/auth/callback`;
  const u = new URL(requestUrl);
  return `${u.protocol}//${u.host}/api/auth/callback`;
}

