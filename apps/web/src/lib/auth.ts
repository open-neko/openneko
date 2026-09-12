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
import { deriveSigningSecret } from "@neko/secret-crypt";
import {
  readAuthGateMarker,
  registerAuthGateCacheReset,
  resetAuthGateCaches,
  writeAuthGateMarker,
  type PersistedAuthProvider,
} from "./auth-gate-marker";
import { cookies } from "next/headers";
import {
  and,
  app_user,
  getOrCreateSoloAdmin,
  isUnclaimedSoloEmail,
  db,
  eq,
  inArray,
  isNull,
  sql,
  sso_group,
  sso_group_mapping,
  sso_group_membership,
} from "@neko/db";
import { getOrgId } from "@/lib/db";
import { upsertOperatorProfile } from "@neko/llm/work";

export const SESSION_COOKIE_NAME = "openneko_session";
export const STATE_COOKIE_NAME = "openneko_sso_state";

/** Session lifetime — 12h. Re-auths after this; refresh-on-use is a v2. */
const SESSION_TTL_SECONDS = 60 * 60 * 12;
/** State cookie lifetime — short window for the user to complete the IdP dance. */
const STATE_TTL_SECONDS = 10 * 60;

export interface AuthProviderInfo {
  pluginName: string;
  providerLabel: string;
  /**
   * "manual": the plugin's identity is self-asserted (magic link proves
   * mailbox possession only), so sign-in is restricted to users an admin
   * pre-provisioned. "automatic": IdP-attested — first sign-in may create
   * the app_user. Older workers omit the field; treat as "automatic".
   */
  provisioning?: "automatic" | "manual";
  /** Sign-in cannot start without an email (e.g. magic link). */
  loginHintRequired?: boolean;
}

/**
 * An installed auth plugin that is not live yet, and why. Admin-only
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

export interface AuthIdentity {
  sub: string;
  email: string;
  name?: string | null;
  orgId?: string | null;
  groups?: Array<string | { id: string; name?: string | null }>;
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
 * Ask the worker whether an SSO plugin is installed. Cached for one
 * second to keep the sign-in page snappy under concurrent loads —
 * the worker's hot-reload window is in seconds anyway.
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
 * Write (string) or delete (null) env values for the installed auth
 * plugin, restricted worker-side to its declared keys. Admin routes
 * only — callers must already be behind requireAdminActor.
 */
export async function setAuthPluginSecrets(
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
  scope?: "external" | "internal";
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

export interface PluginStatus {
  loaded: string[];
  skipped: Array<{ name: string; reason: string }>;
  flagged: Array<{ pluginName: string; reason: string }>;
  kinds: string[];
  vmsRunning: number;
  authProvider?: string | null;
  channels: Array<{
    pluginId: string;
    providerLabel: string;
  }>;
}

const EMPTY_PLUGIN_STATUS: PluginStatus = {
  loaded: [],
  skipped: [],
  flagged: [],
  kinds: [],
  vmsRunning: 0,
  authProvider: null,
  channels: [],
};

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

export async function getPluginStatus(): Promise<PluginStatus> {
  try {
    const res = await fetch(`${workerAdminBase()}/admin/plugins/status`, {
      method: "GET",
      cache: "no-store",
      signal: AbortSignal.timeout(2000),
    });
    if (!res.ok) return EMPTY_PLUGIN_STATUS;
    const body = (await res.json()) as {
      status?: PluginStatus;
    };
    return body.status ?? EMPTY_PLUGIN_STATUS;
  } catch {
    return EMPTY_PLUGIN_STATUS;
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
 * Otherwise insert a new row in the only org — unless the provider
 * declares manual provisioning, in which case an unmatched identity is
 * rejected: possession of a mailbox must never mint an account.
 */
export async function upsertUserFromIdentity(
  identity: AuthIdentity,
): Promise<{ id: string; email: string; name: string | null }> {
  const orgId = await getOrgId();
  // Use the full gate status so the manual-provisioning backstop holds
  // even while the provider is still pending (not yet live).
  const gate = await getAuthGateStatus();
  const providerInfo = gate.provider ?? gate.pending;
  const provider = providerInfo?.pluginName ?? "oidc";
  const mapped = await resolveGroupRole(orgId, provider, identity.groups ?? []);
  // Serialize sign-ins per org. Two concurrent first sign-ins (two tabs,
  // an IdP callback retry) could each miss both lookups AND both pass the
  // has-admin check — minting duplicate identities and duplicate admins,
  // after which later sign-ins threw permanently. Under the lock the
  // second caller simply finds the first's row; the unique indexes from
  // migration 0061 are the loud backstop should the lock ever be bypassed.
  const resolved = await db().transaction(async (tx) => {
    await tx.execute(
      sql`select pg_advisory_xact_lock(hashtext(${"openneko.app_user:" + orgId}))`,
    );
    // Primary lookup: sub. Anything matching wins, regardless of email
    // changes (people get married, change addresses — sub doesn't).
    const bySub = await tx
      .select({
        id: app_user.id,
        email: app_user.email,
        name: app_user.name,
        role: app_user.role,
      })
      .from(app_user)
      .where(and(eq(app_user.org_id, orgId), eq(app_user.sub, identity.sub)))
      .limit(1);
    if (bySub[0]) {
      // A configured group mapping is authoritative on every sign-in; when
      // none is configured, leave the existing role untouched.
      const role = mapped.role ?? bySub[0].role;
      await tx
        .update(app_user)
        .set({
          email: identity.email,
          name: identity.name ?? null,
          role,
          last_login_at: new Date(),
          updated_at: new Date(),
        })
        .where(eq(app_user.id, bySub[0].id));
      return { id: bySub[0].id, name: identity.name ?? null };
    }
    // Migration lookup: same email, no sub yet. Attach the sub.
    const byEmail = await tx
      .select({
        id: app_user.id,
        email: app_user.email,
        name: app_user.name,
        sub: app_user.sub,
        role: app_user.role,
      })
      .from(app_user)
      .where(and(eq(app_user.org_id, orgId), sql`lower(${app_user.email}) = ${identity.email.trim().toLowerCase()}`))
      .limit(1);
    if (byEmail[0] && !byEmail[0].sub) {
      const role = mapped.role ?? byEmail[0].role;
      await tx
        .update(app_user)
        .set({
          sub: identity.sub,
          name: identity.name ?? byEmail[0].name ?? null,
          role,
          last_login_at: new Date(),
          updated_at: new Date(),
        })
        .where(eq(app_user.id, byEmail[0].id));
      return { id: byEmail[0].id, name: identity.name ?? byEmail[0].name ?? null };
    }
    if (byEmail[0]) {
      // Same email, *different* sub already on file. Refuse rather than
      // silently take over — the operator should resolve this manually
      // (likely two IdP accounts pointing at the same mailbox).
      throw new Error(
        `app_user.email ${identity.email} is already bound to a different SSO subject; remove the row or update sub manually before re-attempting`,
      );
    }
    if (providerInfo?.provisioning === "manual") {
      // Self-asserted identity (e.g. magic link) with no pre-provisioned
      // user. The begin route already drops unknown emails silently; this
      // is the defense-in-depth backstop.
      throw new Error(
        `no user is provisioned for ${identity.email} — ask an administrator to add you`,
      );
    }
    // Brand new user. Bootstrap the first active admin so installing an SSO
    // plugin cannot leave an org with no administrator.
    const newId = `usr_${randomBytes(9).toString("base64url")}`;
    const fallbackRole = heuristicRoleForGroups(identity.groups ?? []);
    const role = (await orgHasActiveAdmin(orgId, tx))
      ? (mapped.role ?? fallbackRole)
      : "admin";
    await tx.insert(app_user).values({
      id: newId,
      sub: identity.sub,
      email: identity.email,
      name: identity.name ?? null,
      org_id: orgId,
      role,
      last_login_at: new Date(),
    });
    return { id: newId, name: identity.name ?? null };
  });
  await syncSsoGroups({ orgId, userId: resolved.id, identity });
  await provisionPersona({ orgId, userId: resolved.id, identity, mapped });
  return {
    id: resolved.id,
    email: identity.email,
    name: resolved.name,
  };
}

async function orgHasActiveAdmin(
  orgId: string,
  runner: Pick<ReturnType<typeof db>, "select"> = db(),
): Promise<boolean> {
  const [admin] = await runner
    .select({ id: app_user.id })
    .from(app_user)
    .where(
      and(
        eq(app_user.org_id, orgId),
        eq(app_user.role, "admin"),
        isNull(app_user.disabled_at),
      ),
    )
    .limit(1);
  return Boolean(admin);
}

/**
 * Coarse-grained fallback role mapping used only when no sso_group_mapping
 * row matches. Anyone with an `admin` or `owners` group becomes admin;
 * everyone else is `member`.
 */
function heuristicRoleForGroups(
  groups: Array<string | { id: string; name?: string | null }>,
): string {
  const lower = new Set(
    groups.flatMap((group) =>
      typeof group === "string"
        ? [group.toLowerCase()]
        : [group.id.toLowerCase(), group.name?.toLowerCase()].filter(
            (value): value is string => Boolean(value),
          ),
    ),
  );
  if (lower.has("admin") || lower.has("admins") || lower.has("owners")) {
    return "admin";
  }
  return "member";
}

/**
 * Resolve a configurable role + persona template from sso_group_mapping for
 * the user's groups. `role`/`persona` are null when nothing maps — the caller
 * falls back to the heuristic and the current role, respectively.
 */
async function resolveGroupRole(
  orgId: string,
  provider: string,
  groups: Array<string | { id: string; name?: string | null }>,
): Promise<{ role: string | null; persona: string | null }> {
  const externalIds = groups
    .map((group) => (typeof group === "string" ? group : group.id).trim())
    .filter((id) => id.length > 0);
  if (externalIds.length === 0) return { role: null, persona: null };
  const rows = await db()
    .select({
      role: sso_group_mapping.role,
      persona_role_template: sso_group_mapping.persona_role_template,
      group_external_id: sso_group_mapping.group_external_id,
    })
    .from(sso_group_mapping)
    .where(
      and(
        eq(sso_group_mapping.org_id, orgId),
        eq(sso_group_mapping.provider, provider),
        inArray(sso_group_mapping.group_external_id, externalIds),
      ),
    );
  if (rows.length === 0) return { role: null, persona: null };
  const role = rows.some((r) => r.role === "admin") ? "admin" : "member";
  const personaRow = rows.find((r) => r.persona_role_template) ?? null;
  return { role, persona: personaRow?.persona_role_template ?? null };
}

/** Provision the user's per-user persona from the group mapping (idempotent). */
async function provisionPersona(input: {
  orgId: string;
  userId: string;
  identity: AuthIdentity;
  mapped: { role: string | null; persona: string | null };
}): Promise<void> {
  const roleTemplate =
    input.mapped.persona ??
    (input.mapped.role === "admin" ? "Administrator" : "");
  if (!roleTemplate) return;
  await upsertOperatorProfile({
    orgId: input.orgId,
    userId: input.userId,
    displayName: input.identity.name ?? null,
    roleTemplate,
  });
}

function normalizedIdentityGroups(identity: AuthIdentity): Array<{
  externalId: string;
  displayName: string | null;
}> {
  const groups = new Map<string, string | null>();
  for (const group of identity.groups ?? []) {
    const externalId = (typeof group === "string" ? group : group.id).trim();
    if (!externalId) continue;
    const displayName = typeof group === "string"
      ? group
      : group.name?.trim() || null;
    groups.set(externalId, displayName);
  }
  return [...groups].map(([externalId, displayName]) => ({
    externalId,
    displayName,
  }));
}

/** Sign-in claim sync fallback. SCIM writes the same tables when available. */
async function syncSsoGroups(input: {
  orgId: string;
  userId: string;
  identity: AuthIdentity;
}): Promise<void> {
  const provider = (await getAuthProvider())?.pluginName ?? "oidc";
  const tenantId = input.identity.orgId?.trim() || input.orgId;
  const groups = normalizedIdentityGroups(input.identity);
  await db().transaction(async (tx) => {
    const groupIds: string[] = [];
    for (const group of groups) {
      const [row] = await tx
        .insert(sso_group)
        .values({
          org_id: input.orgId,
          provider,
          tenant_id: tenantId,
          external_id: group.externalId,
          display_name: group.displayName,
          active: true,
          updated_at: new Date(),
        })
        .onConflictDoUpdate({
          target: [
            sso_group.org_id,
            sso_group.provider,
            sso_group.tenant_id,
            sso_group.external_id,
          ],
          set: {
            display_name: group.displayName,
            active: true,
            updated_at: new Date(),
          },
        })
        .returning({ id: sso_group.id });
      if (row) groupIds.push(row.id);
    }
    await tx
      .delete(sso_group_membership)
      .where(
        and(
          eq(sso_group_membership.org_id, input.orgId),
          eq(sso_group_membership.user_id, input.userId),
        ),
      );
    if (groupIds.length > 0) {
      await tx.insert(sso_group_membership).values(
        groupIds.map((groupId) => ({
          org_id: input.orgId,
          group_id: groupId,
          user_id: input.userId,
          synced_at: new Date(),
        })),
      );
    }
    await tx.execute(sql`
      insert into sso_group_sync_audit
        (org_id, user_id, provider, tenant_id, external_group_ids)
      values (
        ${input.orgId}, ${input.userId}, ${provider}, ${tenantId},
        (ARRAY[${sql.raw(
          groups
            .map((group) => `'${group.externalId.replace(/'/g, "''")}'`)
            .join(","),
        )}])::text[]
      )
    `);
  });
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
