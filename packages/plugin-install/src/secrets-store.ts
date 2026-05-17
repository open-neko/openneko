// Per-user plugin secrets store. The default location is
// $XDG_CONFIG_HOME/openneko/secrets.json (default
// ~/.config/openneko/secrets.json). Keyed by plugin npm name; each
// value is a flat env-var map. The OpenNeko worker reads the same
// file and injects values into each plugin's microVM at exec time.
// The CLI's `secrets {set,unset,list}` commands write through this
// module. File is written with 0600 perms so other users on the host
// can't read it.
//
// Until now this code lived in TWO places: apps/worker/src/plugins/
// load-plugins.ts (read-only worker copy) and
// open-neko/cli/src/secrets-store.ts (read/write CLI copy). They
// would have drifted. One source of truth now.
import { chmodSync, existsSync } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

export type SecretsStore = Record<string, Record<string, string>>;

const STORE_FILENAME = "secrets.json";

function configDir(): string {
  const xdg = process.env.XDG_CONFIG_HOME;
  if (xdg) return path.join(xdg, "openneko");
  return path.join(process.env.HOME ?? "/tmp", ".config", "openneko");
}

export function defaultSecretsPath(overrideDir?: string): string {
  return path.join(overrideDir ?? configDir(), STORE_FILENAME);
}

export async function readSecretsStore(
  overrideDir?: string,
): Promise<SecretsStore> {
  const file = defaultSecretsPath(overrideDir);
  if (!existsSync(file)) return {};
  const raw = await readFile(file, "utf8");
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    throw new Error(
      `secrets store at ${file} is invalid JSON: ${err instanceof Error ? err.message : err}`,
    );
  }
  if (typeof parsed !== "object" || parsed === null) {
    throw new Error(`secrets store at ${file} has unexpected shape`);
  }
  const store: SecretsStore = {};
  for (const [pkg, env] of Object.entries(parsed as Record<string, unknown>)) {
    if (typeof env !== "object" || env === null) continue;
    const map: Record<string, string> = {};
    for (const [k, v] of Object.entries(env as Record<string, unknown>)) {
      if (typeof v === "string") map[k] = v;
    }
    store[pkg] = map;
  }
  return store;
}

/**
 * Loader-friendly variant: never throws, never makes the worker boot
 * fail. Invalid JSON or unexpected shape → emit a warning via the
 * caller's logger and return an empty store. The worker uses this;
 * the CLI uses the stricter `readSecretsStore` so operators see
 * errors immediately.
 */
export async function readSecretsStoreSoft(
  overrideDir: string | undefined,
  warn: (line: string) => void = (m) => console.warn(`[plugin-install] ${m}`),
): Promise<SecretsStore> {
  try {
    return await readSecretsStore(overrideDir);
  } catch (err) {
    warn(`secrets store unreadable; treating as empty: ${err instanceof Error ? err.message : err}`);
    return {};
  }
}

export async function writeSecretsStore(
  store: SecretsStore,
  overrideDir?: string,
): Promise<void> {
  const file = defaultSecretsPath(overrideDir);
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  // Sort keys deterministically so diffs are stable across runs.
  const sortedPlugins = Object.keys(store).sort();
  const sorted: SecretsStore = {};
  for (const pkg of sortedPlugins) {
    const inner = store[pkg] ?? {};
    const sortedKeys = Object.keys(inner).sort();
    sorted[pkg] = {};
    for (const k of sortedKeys) sorted[pkg][k] = inner[k]!;
  }
  await writeFile(file, JSON.stringify(sorted, null, 2) + "\n", "utf8");
  try {
    chmodSync(file, 0o600);
  } catch {
    /* Windows etc; best-effort. */
  }
}

const ENV_KEY_RX = /^[A-Z][A-Z0-9_]*$/;

export function isValidEnvKey(key: string): boolean {
  return ENV_KEY_RX.test(key);
}

export function setSecret(
  store: SecretsStore,
  plugin: string,
  key: string,
  value: string,
): SecretsStore {
  if (!isValidEnvKey(key)) {
    throw new Error(`env key "${key}" must be UPPER_SNAKE_CASE`);
  }
  const existing = store[plugin] ?? {};
  return {
    ...store,
    [plugin]: { ...existing, [key]: value },
  };
}

export function unsetSecret(
  store: SecretsStore,
  plugin: string,
  key: string,
): { store: SecretsStore; removed: boolean } {
  const existing = store[plugin];
  if (!existing || !(key in existing)) {
    return { store, removed: false };
  }
  const { [key]: _removed, ...rest } = existing;
  void _removed;
  const next: SecretsStore = { ...store, [plugin]: rest };
  if (Object.keys(rest).length === 0) {
    const { [plugin]: _gone, ...withoutPlugin } = next;
    void _gone;
    return { store: withoutPlugin, removed: true };
  }
  return { store: next, removed: true };
}

export function listKeysForPlugin(
  store: SecretsStore,
  plugin: string,
): string[] {
  return Object.keys(store[plugin] ?? {}).sort();
}

/**
 * All distinct secret values across the store. Used by the env-scrubber
 * in `@neko/llm` to build a redaction regex applied to agent output —
 * accidentally-leaked secrets get replaced with [REDACTED] before
 * they reach work_memory, run replays, or the Briefing.
 */
export function allSecretValues(store: SecretsStore): string[] {
  const out = new Set<string>();
  for (const env of Object.values(store)) {
    for (const v of Object.values(env)) {
      if (v) out.add(v);
    }
  }
  return [...out];
}
