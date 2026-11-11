/**
 * SSO integration for the web app.
 *
 * Without a sign-in provider OpenNeko runs in single-operator mode —
 * fine for a laptop deployment, fatal for any org with an IdP. This
 * module reads the session that `openneko web` writes at sign-in, and asks
 * the worker which provider the admin chose (Scalekit SSO or email-link
 * sign-in). The sign-in routes themselves are in Go.
 *
 * Topology: the providers run in the worker, not in this Next process.
 * We reach them through the worker's admin HTTP endpoint on localhost
 * (loopback inside the deployment, never exposed). That gives us:
 *   - Secrets stay in the worker's encrypted secrets store.
 *   - The web process never needs the IdP client_secret.
 *
 * Session model: a stateless signed cookie. The session_id is the
 * app_user.id; an HMAC over `${user_id}.${expiresAt}` prevents
 * tampering and binds the cookie to OPENNEKO_SESSION_SECRET. Logging
 * a user out is a cookie delete; rotating the secret invalidates
 * every existing session globally.
 *
 * The installed app sends a device token (`Authorization: Bearer
 * onk_dev_...`) in place of the cookie. Each request checks the token
 * against app_device, so a revoke applies at once.
 */

import { createHmac, randomBytes, timingSafeEqual } from "node:crypto";
import { deriveSigningSecret } from "@neko/secret-crypt";
import {
  readAuthGateMarker,
  registerAuthGateCacheReset,
  resetAuthGateCaches,
  writeAuthGateMarker,
  type PersistedAuthProvider,
} from "./auth-gate-marker";
import { cookies, headers } from "next/headers";
import {
  and,
  app_user,
  getOrCreateSoloAdmin,
  isUnclaimedSoloEmail,
  db,
  eq,
  isNull,
  deviceTokenFromAuthorization,
  verifyDeviceToken,
} from "@neko/db";
import { getOrgId } from "@/lib/db";
import { publicBaseUrl } from "@/lib/public-url";

export const SESSION_COOKIE_NAME = "openneko_session";

export interface AuthProviderInfo {
  pluginName: string;
  providerLabel: string;
  /**
   * "manual": the provider's identity is self-asserted (magic link proves
   * mailbox possession only), so sign-in is restricted to users an admin
   * pre-provisioned. "automatic": IdP-attested — first sign-in may create
   * the app_user. Older workers omit the field; treat as "automatic".
   */
  provisioning?: "automatic" | "manual";
  /** Sign-in cannot start without an email (e.g. magic link). */
  loginHintRequired?: boolean;
}

/**
 * The chosen sign-in provider while it is not live yet, and why. Admin-only
 * surface (setup UI); the public status route reduces it to a label.
 */
export interface PendingAuthProviderInfo extends AuthProviderInfo {
  /** Required env keys still unset — names only, never values. */
  missingEnv: string[];
  /** Manual provisioning with zero active admin users provisioned. */
  needsAdminUser: boolean;
  /** Declared env keys that currently hold a value — names only. */
  configuredEnv: string[];
}

export interface AuthGateStatus {
  provider: AuthProviderInfo | null;
  pending: PendingAuthProviderInfo | null;
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
  // OPENNEKO_SESSION_SECRET is the HMAC key for session cookies. When it
  // is not set (no compose file sets it), derive a stable per-install
  // secret from the deployment secret-key: consistent across restarts and
  // across web instances sharing the config volume — a process-random
  // fallback would invalidate sessions on every restart. An explicitly
  // set but too-short value is still a hard error: that is a
  // misconfiguration, not an absence.
  const secret = process.env.OPENNEKO_SESSION_SECRET;
  if (secret !== undefined && secret.length < 32) {
    throw new Error(
      "OPENNEKO_SESSION_SECRET must be at least 32 characters long for SSO sessions",
    );
  }
  if (secret) return secret;
  return deriveSigningSecret("session-cookie:v1").toString("base64");
}

/**
 * Ask the worker which sign-in provider is live. Cached for one second
 * to keep the sign-in page snappy under concurrent loads.
 */
let providerCache: { value: AuthGateStatus; at: number } | null = null;
const PROVIDER_CACHE_TTL_MS = 1000;

const NO_GATE: AuthGateStatus = { provider: null, pending: null };

/**
 * Full sign-in gate status: the live provider (null while setup is
 * incomplete) plus the pending provider with its missing pieces. Admin
 * surfaces consume `pending`; everything else should keep using
 * getAuthProvider().
 */
export async function getAuthGateStatus(): Promise<AuthGateStatus> {
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
      providerCache = { value: gateWhileWorkerUnavailable(), at: now };
      return providerCache.value;
    }
    const body = (await res.json()) as AuthGateStatus;
    providerCache = {
      value: { provider: body.provider ?? null, pending: body.pending ?? null },
      at: now,
    };
    writeAuthGateMarker({
      provider: (body.provider as PersistedAuthProvider | null) ?? null,
    });
    return providerCache.value;
  } catch {
    providerCache = { value: gateWhileWorkerUnavailable(), at: now };
    return providerCache.value;
  }
}

/**
 * Worker unreachable (booting, restarting, or slow): the last persisted
 * live answer decides. An install where SSO was ever live fails CLOSED —
 * synthesizing the persisted provider keeps requireAdminActor denying
 * session-less requests while valid session cookies verify locally. An
 * install that never configured SSO keeps the solo-operator behavior.
 */
function gateWhileWorkerUnavailable(): AuthGateStatus {
  const marker = readAuthGateMarker();
  if (marker?.provider) {
    return {
      provider: marker.provider as unknown as AuthProviderInfo,
      pending: null,
    };
  }
  return NO_GATE;
}

export async function getAuthProvider(): Promise<AuthProviderInfo | null> {
  return (await getAuthGateStatus()).provider;
}

registerAuthGateCacheReset(() => {
  providerCache = null;
});

/**
 * Clears every auth-gate cache (this module's, the proxy's, and the
 * persisted-marker copy) — SSO settings changes must be visible on the
 * very next request in all of them.
 */
export function _resetAuthProviderCache() {
  resetAuthGateCaches();
}

/**
 * Choose the sign-in provider ("scalekit" or "magic-link"); null turns
 * sign-in off. Admin routes only — callers must already be behind
 * requireAdminActor.
 */
export async function selectSignInProvider(
  provider: "scalekit" | "magic-link" | null,
): Promise<void> {
  const res = await fetch(`${workerAdminBase()}/admin/auth/provider`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider }),
    signal: AbortSignal.timeout(10_000),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `sign-in provider change failed (${res.status})`);
  }
  _resetAuthProviderCache();
}

/**
 * Write (string) or delete (null) values of the chosen sign-in provider,
 * restricted worker-side to its declared keys. Admin routes only —
 * callers must already be behind requireAdminActor.
 */
export async function setSignInSecrets(
  values: Record<string, string | null>,
): Promise<void> {
  const res = await fetch(`${workerAdminBase()}/admin/auth/secrets`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ values }),
    signal: AbortSignal.timeout(10_000),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as {
      error?: string;
    } | null;
    throw new Error(
      body?.error ?? `auth secrets write failed (${res.status})`,
    );
  }
  // The gate may have just flipped — drop the 1s cache so the next
  // status read reflects it immediately.
  _resetAuthProviderCache();
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
    // Sending the email-link can take a while on a slow provider. Giving
    // up early sends no email at all, which reads to the person as silence.
    signal: AbortSignal.timeout(45_000),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(
      `sign-in begin failed (${res.status}): ${text || res.statusText}`,
    );
  }
  return (await res.json()) as { authorizationUrl: string };
}

/**
 * Mint a state token: random nonce paired with the path the user was
 * trying to reach. The path is stashed in the cookie too so callback
 * can redirect back without trusting an open redirect parameter.
 */
export function newStateToken(): string {
  return randomBytes(24).toString("base64url");
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

/**
 * Resolve the local solo account, or the SSO session (cookie → DB lookup → user). Returns
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
  // Solo authentication still resolves to a real account. Never infer a
  // another user as the operator once SSO is live.
  if (!(await getAuthProvider())) {
    const owner = await getOrCreateSoloAdmin(await getOrgId());
    return owner ? { id: owner.id, email: isUnclaimedSoloEmail(owner.email) ? "" : owner.email, name: owner.name } : null;
  }
  const userId = await sessionOrDeviceUserId();
  if (!userId) return null;
  const rows = await db()
    .select({
      id: app_user.id,
      email: app_user.email,
      name: app_user.name,
    })
    .from(app_user)
    // ADM1: a deactivated user's cookie is dead, not just their sign-in.
    .where(and(eq(app_user.id, userId), isNull(app_user.disabled_at)))
    .limit(1);
  if (!rows[0]) return null;
  return rows[0];
}

async function sessionOrDeviceUserId(): Promise<string | null> {
  const jar = await cookies();
  const cookie = jar.get(SESSION_COOKIE_NAME)?.value;
  if (cookie) return decodeSession(cookie)?.userId ?? null;
  const device = await currentDevice();
  return device?.userId ?? null;
}

/** The installed app's device behind this request's bearer token, if any. */
export async function currentDevice(): Promise<{ deviceId: string; userId: string } | null> {
  const token = deviceTokenFromAuthorization((await headers()).get("authorization"));
  if (!token) return null;
  return verifyDeviceToken(await getOrgId(), token);
}

/**
 * Build the absolute callback URL the IdP will redirect to. We compute
 * it from the incoming request rather than env so dev and prod work
 * with the same code. Operators wanting to force a canonical host can
 * set OPENNEKO_PUBLIC_URL (e.g. behind a load balancer that strips
 * Host headers).
 */
export function buildRedirectUri(requestUrl: string): string {
  return `${publicBaseUrl(requestUrl)}/api/auth/callback`;
}
