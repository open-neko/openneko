import {
  createCipheriv,
  createDecipheriv,
  createHash,
  randomBytes,
} from "node:crypto";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

/**
 * App secret used to encrypt LLM API keys at rest in `llm_provider_config`.
 *
 * Resolution: read `~/.config/openneko/secret-key` file (mode 0600) → if
 * missing, generate 32 random bytes and persist. Cached after first read.
 *
 * Local dev: nothing to configure — the file is auto-created on first run.
 * Prod: pre-populate `~/.config/openneko/secret-key` via init container /
 * secrets-manager mount before the app starts.
 *
 * No env override — same policy as the rest of app config. XDG_CONFIG_HOME
 * is respected for non-default config locations.
 *
 * Test fixtures redirect writes via process.env.HOME (or XDG_CONFIG_HOME),
 * matching the pattern used by `local-config.ts` and `host-provision.ts`.
 */

const PREFIX = "enc:v1:";

let _cachedKey: Buffer | null = null;

function appSecretKeyPath(): string {
  const xdg = process.env.XDG_CONFIG_HOME?.trim();
  const base = xdg && xdg.length > 0
    ? xdg
    : join(process.env.HOME || homedir(), ".config");
  return join(base, "openneko", "secret-key");
}

function generateAndPersist(): string {
  const path = appSecretKeyPath();
  mkdirSync(join(path, ".."), { recursive: true });
  const value = randomBytes(32).toString("base64");
  writeFileSync(path, value, { encoding: "utf8" });
  chmodSync(path, 0o600);
  return value;
}

function readSecret(): string {
  const path = appSecretKeyPath();
  try {
    const stored = readFileSync(path, "utf8").trim();
    if (stored) return stored;
  } catch {
    // file missing or unreadable — fall through to generate.
  }
  return generateAndPersist();
}

function getKey(): Buffer {
  if (_cachedKey) return _cachedKey;
  const secret = readSecret();
  _cachedKey = createHash("sha256").update(secret).digest();
  return _cachedKey;
}

/**
 * Test-only: drop the cached key so a temp HOME picks up its own file.
 * Not exported via package.json — only used by integration tests.
 */
export function _resetSecretKeyCacheForTesting(): void {
  _cachedKey = null;
}

export function maybeEncryptSecret(value: string): string {
  const key = getKey();
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  const encrypted = Buffer.concat([cipher.update(value, "utf8"), cipher.final()]);
  const tag = cipher.getAuthTag();
  return `${PREFIX}${Buffer.concat([iv, tag, encrypted]).toString("base64")}`;
}

export function maybeDecryptSecret(value: unknown): string {
  if (typeof value !== "string" || !value) return "";
  if (!value.startsWith(PREFIX)) return value;

  const key = getKey();
  const raw = Buffer.from(value.slice(PREFIX.length), "base64");
  const iv = raw.subarray(0, 12);
  const tag = raw.subarray(12, 28);
  const encrypted = raw.subarray(28);
  const decipher = createDecipheriv("aes-256-gcm", key, iv);
  decipher.setAuthTag(tag);
  return Buffer.concat([decipher.update(encrypted), decipher.final()]).toString("utf8");
}
