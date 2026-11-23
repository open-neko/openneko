import { createHash, randomBytes, randomInt, timingSafeEqual } from "node:crypto";
import { sql } from "drizzle-orm";
import { db } from "./index";

export const DEVICE_TOKEN_PREFIX = "onk_dev";
export const DEVICE_REQUEST_TTL_SECONDS = 10 * 60;
export const DEVICE_POLL_INTERVAL_SECONDS = 5;
export const DEVICE_IDLE_DAYS = 30;
const MAX_PENDING_REQUESTS = 20;
const USER_CODE_ALPHABET = "BCDFGHJKLMNPQRSTVWXZ";
const LAST_USED_RESOLUTION_SECONDS = 60;

export type DeviceErrorCode = "invalid" | "too_many";

export class DeviceError extends Error {
  constructor(public readonly code: DeviceErrorCode, message: string) {
    super(message);
    this.name = "DeviceError";
  }
}

export type DeviceRow = {
  id: string;
  userId: string;
  userEmail: string;
  userName: string | null;
  name: string;
  tokenPrefix: string;
  createdAt: string;
  lastUsedAt: string;
  revokedAt: string | null;
  status: "active" | "idle" | "revoked";
};

export type DeviceRequestView = {
  id: string;
  name: string;
  userCode: string;
  createdAt: string;
  expiresAt: string;
};

export type RedeemResult =
  | { status: "pending" | "slow_down" | "denied" | "expired" }
  | { status: "issued"; token: string; deviceId: string; userId: string };

function digest(value: string): string {
  return createHash("sha256").update(value, "utf8").digest("hex");
}

function sameDigest(a: string, b: string): boolean {
  const left = Buffer.from(a, "hex");
  const right = Buffer.from(/^[0-9a-f]{64}$/.test(b) ? b : "0".repeat(64), "hex");
  return timingSafeEqual(left, right) && a === b;
}

export function normalizeDeviceName(raw: unknown): string {
  const name = typeof raw === "string" ? raw.replace(/\s+/g, " ").trim() : "";
  return name.slice(0, 80) || "Unnamed device";
}

export function normalizeUserCode(raw: unknown): string | null {
  if (typeof raw !== "string") return null;
  const code = raw.toUpperCase().replace(/[\s-]/g, "");
  if (code.length !== 8) return null;
  for (const ch of code) if (!USER_CODE_ALPHABET.includes(ch)) return null;
  return code;
}

export function formatUserCode(code: string): string {
  return `${code.slice(0, 4)}-${code.slice(4)}`;
}

function newUserCode(): string {
  let code = "";
  for (let i = 0; i < 8; i++) code += USER_CODE_ALPHABET[randomInt(USER_CODE_ALPHABET.length)];
  return code;
}

export function parseDeviceToken(token: string): { prefix: string } | null {
  const match = token.match(/^(onk_dev_[0-9a-f]{12})_[A-Za-z0-9_-]{43}$/);
  return match ? { prefix: match[1]! } : null;
}

export function deviceTokenFromAuthorization(header: string | null | undefined): string | null {
  const match = header?.match(/^Bearer[ \t]+(onk_dev_\S+)$/i);
  return match?.[1] ?? null;
}

export async function startDeviceRequest(
  orgId: string,
  name: unknown,
): Promise<{ deviceCode: string; userCode: string; expiresAt: Date }> {
  const deviceName = normalizeDeviceName(name);
  return db().transaction(async (tx) => {
    await tx.execute(sql`select pg_advisory_xact_lock(hashtext(${"openneko.device-request:" + orgId}))`);
    await tx.execute(sql`delete from app_device_request where org_id = ${orgId} and expires_at < now()`);
    const pending = await tx.execute<{ n: number }>(
      sql`select count(*)::int as n from app_device_request where org_id = ${orgId}`,
    );
    if ((pending.rows[0]?.n ?? 0) >= MAX_PENDING_REQUESTS) {
      throw new DeviceError("too_many", "too many devices are waiting for approval; try again in a few minutes");
    }
    const deviceCode = randomBytes(32).toString("base64url");
    for (let attempt = 0; attempt < 5; attempt++) {
      const userCode = newUserCode();
      const inserted = await tx.execute<{ expires_at: Date }>(sql`
        insert into app_device_request (org_id, device_code_verifier, user_code, name, expires_at)
        values (${orgId}, ${digest(deviceCode)}, ${userCode}, ${deviceName},
                now() + make_interval(secs => ${DEVICE_REQUEST_TTL_SECONDS}))
        on conflict (user_code) do nothing
        returning expires_at`);
      const row = inserted.rows[0];
      if (row) return { deviceCode, userCode, expiresAt: new Date(row.expires_at) };
    }
    throw new Error("could not choose a unique device code");
  });
}

export async function findDeviceRequest(
  orgId: string,
  rawUserCode: unknown,
): Promise<DeviceRequestView | null> {
  const userCode = normalizeUserCode(rawUserCode);
  if (!userCode) return null;
  const result = await db().execute<{ id: string; name: string; created_at: Date; expires_at: Date }>(sql`
    select id, name, created_at, expires_at from app_device_request
    where org_id = ${orgId} and user_code = ${userCode} and expires_at > now()
      and approved_at is null and denied_at is null`);
  const row = result.rows[0];
  if (!row) return null;
  return {
    id: row.id,
    name: row.name,
    userCode: formatUserCode(userCode),
    createdAt: new Date(row.created_at).toISOString(),
    expiresAt: new Date(row.expires_at).toISOString(),
  };
}

export async function decideDeviceRequest(input: {
  orgId: string;
  userCode: unknown;
  userId: string;
  approve: boolean;
}): Promise<DeviceRequestView | null> {
  const userCode = normalizeUserCode(input.userCode);
  if (!userCode) return null;
  const result = await db().execute<{ id: string; name: string; created_at: Date; expires_at: Date }>(sql`
    update app_device_request
    set approved_by_user_id = ${input.approve ? input.userId : null},
        approved_at = ${input.approve ? sql`now()` : null},
        denied_at = ${input.approve ? null : sql`now()`}
    where org_id = ${input.orgId} and user_code = ${userCode} and expires_at > now()
      and approved_at is null and denied_at is null
    returning id, name, created_at, expires_at`);
  const row = result.rows[0];
  if (!row) return null;
  return {
    id: row.id,
    name: row.name,
    userCode: formatUserCode(userCode),
    createdAt: new Date(row.created_at).toISOString(),
    expiresAt: new Date(row.expires_at).toISOString(),
  };
}

export async function redeemDeviceRequest(orgId: string, deviceCode: unknown): Promise<RedeemResult> {
  if (typeof deviceCode !== "string" || deviceCode.length === 0 || deviceCode.length > 128) {
    throw new DeviceError("invalid", "deviceCode is required");
  }
  return db().transaction(async (tx) => {
    const found = await tx.execute<{
      id: string;
      name: string;
      approved_by_user_id: string | null;
      approved_at: Date | null;
      denied_at: Date | null;
      expired: boolean;
      too_soon: boolean;
    }>(sql`
      select id, name, approved_by_user_id, approved_at, denied_at,
             expires_at <= now() as expired,
             coalesce(last_polled_at > now() - make_interval(secs => ${DEVICE_POLL_INTERVAL_SECONDS - 1}), false) as too_soon
      from app_device_request
      where org_id = ${orgId} and device_code_verifier = ${digest(deviceCode)}
      for update`);
    const request = found.rows[0];
    if (!request || request.expired) return { status: "expired" };
    if (request.denied_at) {
      await tx.execute(sql`delete from app_device_request where id = ${request.id}`);
      return { status: "denied" };
    }
    if (!request.approved_at || !request.approved_by_user_id) {
      await tx.execute(sql`update app_device_request set last_polled_at = now() where id = ${request.id}`);
      return { status: request.too_soon ? "slow_down" : "pending" };
    }
    const fingerprint = randomBytes(6).toString("hex");
    const prefix = `${DEVICE_TOKEN_PREFIX}_${fingerprint}`;
    const token = `${prefix}_${randomBytes(32).toString("base64url")}`;
    const inserted = await tx.execute<{ id: string }>(sql`
      insert into app_device (org_id, user_id, name, token_prefix, token_verifier)
      values (${orgId}, ${request.approved_by_user_id}, ${request.name}, ${prefix}, ${digest(token)})
      returning id`);
    await tx.execute(sql`delete from app_device_request where id = ${request.id}`);
    return {
      status: "issued",
      token,
      deviceId: inserted.rows[0]!.id,
      userId: request.approved_by_user_id,
    };
  });
}

export async function verifyDeviceToken(
  orgId: string,
  token: string | null | undefined,
): Promise<{ deviceId: string; userId: string } | null> {
  const parsed = token ? parseDeviceToken(token) : null;
  if (!token || !parsed) return null;
  const result = await db().execute<{ id: string; user_id: string; token_verifier: string; stale: boolean }>(sql`
    select d.id, d.user_id, d.token_verifier,
           d.last_used_at < now() - make_interval(secs => ${LAST_USED_RESOLUTION_SECONDS}) as stale
    from app_device d
    join app_user u on u.id = d.user_id and u.disabled_at is null
    where d.org_id = ${orgId} and d.token_prefix = ${parsed.prefix} and d.revoked_at is null
      and d.last_used_at > now() - make_interval(days => ${DEVICE_IDLE_DAYS})`);
  const row = result.rows[0];
  if (!sameDigest(digest(token), row?.token_verifier ?? "") || !row) return null;
  if (row.stale) {
    await db().execute(sql`update app_device set last_used_at = now() where id = ${row.id}`);
  }
  return { deviceId: row.id, userId: row.user_id };
}

export async function listDevices(orgId: string): Promise<DeviceRow[]> {
  const result = await db().execute<{
    id: string;
    user_id: string;
    email: string;
    user_name: string | null;
    name: string;
    token_prefix: string;
    created_at: Date;
    last_used_at: Date;
    revoked_at: Date | null;
    idle: boolean;
  }>(sql`
    select d.id, d.user_id, u.email, u.name as user_name, d.name, d.token_prefix,
           d.created_at, d.last_used_at, d.revoked_at,
           d.last_used_at <= now() - make_interval(days => ${DEVICE_IDLE_DAYS}) as idle
    from app_device d join app_user u on u.id = d.user_id
    where d.org_id = ${orgId}
    order by d.revoked_at is not null, d.last_used_at desc`);
  return result.rows.map((row) => ({
    id: row.id,
    userId: row.user_id,
    userEmail: row.email,
    userName: row.user_name,
    name: row.name,
    tokenPrefix: row.token_prefix,
    createdAt: new Date(row.created_at).toISOString(),
    lastUsedAt: new Date(row.last_used_at).toISOString(),
    revokedAt: row.revoked_at ? new Date(row.revoked_at).toISOString() : null,
    status: row.revoked_at ? "revoked" : row.idle ? "idle" : "active",
  }));
}
