import { createPublicKey, verify as verifySignature } from "node:crypto";

// A real push service refuses a request whose VAPID JWT does not verify
// against the applicationServerKey the subscription was made with.
export function verifyVapidForSubscription(authorization: string, subscribedKey: string, audience: string): string {
  const m = authorization.match(/^vapid t=([^,]+), k=([A-Za-z0-9_-]+)$/);
  if (!m) throw new Error("no vapid authorization");
  const [, token, k] = m as unknown as [string, string, string];
  if (k !== subscribedKey) throw new Error("vapid key is not the subscription's applicationServerKey");
  const point = Buffer.from(k, "base64url");
  if (point.length !== 65 || point[0] !== 4) throw new Error("vapid key is not an uncompressed P-256 point");
  const key = createPublicKey({
    key: { kty: "EC", crv: "P-256", x: point.subarray(1, 33).toString("base64url"), y: point.subarray(33).toString("base64url") },
    format: "jwk",
  });
  const [h, c, sig] = token.split(".");
  if (!h || !c || !sig) throw new Error("vapid token is not a JWT");
  const ok = verifySignature("sha256", Buffer.from(`${h}.${c}`), { key, dsaEncoding: "ieee-p1363" }, Buffer.from(sig, "base64url"));
  if (!ok) throw new Error("vapid signature does not verify");
  const claims = JSON.parse(Buffer.from(c, "base64url").toString("utf8")) as { aud?: string; exp?: number };
  if (claims.aud !== audience) throw new Error(`vapid aud ${claims.aud} is not ${audience}`);
  if (!claims.exp || claims.exp * 1000 < Date.now()) throw new Error("vapid token expired");
  return k;
}
