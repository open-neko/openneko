import { createHmac, timingSafeEqual } from "node:crypto";
import { deriveSigningSecret } from "@neko/secret-crypt";

/**
 * GJ4 — per-run GraphJin actor tokens. GraphJin source mode runs
 * `auth: jwt`; every run carries a short-lived HS256 JWT whose claims
 * are the K1 actor snapshot, so the data engine itself shapes what each
 * caller may see (role-aware gj_catalog, row policies). The signing
 * secret is per-org, derived from the deployment secret-key
 * (HMAC("graphjin:<orgId>")) — no extra key storage; the same value is
 * written into the GraphJin source-mode config so both sides agree.
 *
 * Lifetime: 5 minutes by default; runChatTurn mints at run start and
 * re-mints when a long run approaches expiry. Claims are a snapshot —
 * a mid-run role change does not retro-affect a running turn (K1 rule).
 */
export type GraphjinTokenInput = {
  orgId: string;
  /** K1 actor; null userId = service/org principal. */
  userId: string | null;
  role: "admin" | "member" | "service";
  ttlSeconds?: number;
  /** Test seam. */
  nowMs?: number;
};

export const GRAPHJIN_TOKEN_TTL_SECONDS = 300;

function b64url(input: Buffer | string): string {
  return Buffer.from(input).toString("base64url");
}

export function graphjinSigningSecret(orgId: string): Buffer {
  return deriveSigningSecret(`graphjin:${orgId}`);
}

/** The base64 secret to paste into the GraphJin `auth.jwt.secret` config. */
export function graphjinSigningSecretB64(orgId: string): string {
  return graphjinSigningSecret(orgId).toString("base64");
}

export function mintGraphjinToken(input: GraphjinTokenInput): string {
  const now = Math.floor((input.nowMs ?? Date.now()) / 1000);
  const header = b64url(JSON.stringify({ alg: "HS256", typ: "JWT" }));
  const payload = b64url(
    JSON.stringify({
      sub: input.userId ?? "service",
      role: input.role,
      org_id: input.orgId,
      iat: now,
      exp: now + (input.ttlSeconds ?? GRAPHJIN_TOKEN_TTL_SECONDS),
    }),
  );
  const signature = createHmac("sha256", graphjinSigningSecret(input.orgId))
    .update(`${header}.${payload}`)
    .digest("base64url");
  return `${header}.${payload}.${signature}`;
}

export type GraphjinTokenClaims = {
  sub: string;
  role: string;
  org_id: string;
  iat: number;
  exp: number;
};

/** Verify + decode (used by tests and any local enforcement point). */
export function verifyGraphjinToken(
  token: string,
  orgId: string,
  nowMs = Date.now(),
): GraphjinTokenClaims | null {
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  const [header, payload, signature] = parts as [string, string, string];
  const expected = createHmac("sha256", graphjinSigningSecret(orgId))
    .update(`${header}.${payload}`)
    .digest("base64url");
  const a = Buffer.from(signature);
  const b = Buffer.from(expected);
  if (a.length !== b.length || !timingSafeEqual(a, b)) return null;
  let claims: GraphjinTokenClaims;
  try {
    claims = JSON.parse(
      Buffer.from(payload, "base64url").toString("utf8"),
    ) as GraphjinTokenClaims;
  } catch {
    return null;
  }
  if (claims.org_id !== orgId) return null;
  if (claims.exp * 1000 <= nowMs) return null;
  return claims;
}

/** Re-mint when fewer than 60s remain — long runs call this per query. */
export function graphjinTokenNeedsRefresh(
  token: string,
  orgId: string,
  nowMs = Date.now(),
): boolean {
  const claims = verifyGraphjinToken(token, orgId, nowMs);
  if (!claims) return true;
  return claims.exp * 1000 - nowMs < 60_000;
}
