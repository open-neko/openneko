import { createHash } from "node:crypto";

export function sha256Hex(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

/** The Go web front writes this key into the admission payload. */
export const RECKON_SEED_KEY = "openneko_reckon_webhook";

export function reckonSeedMessageFrom(payload: Record<string, unknown> | null): string | null {
  const seed = payload?.[RECKON_SEED_KEY];
  if (!seed || typeof seed !== "object") return null;
  const message = (seed as { message?: unknown }).message;
  return typeof message === "string" && message.trim() ? message : null;
}
