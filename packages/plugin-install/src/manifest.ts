// Project-local openneko.plugins.json read/write. By default lives at
// the OpenNeko repo root; in Docker / multi-process deployments the
// path is overridden via OPENNEKO_PLUGINS_MANIFEST_PATH so the manifest
// can sit on a writable named volume the worker can fs.watch.
// Tracked when at repo root; runtime state when on a volume. Pinned
// versions + integrity hashes; the schema URL is informational only.
import { existsSync } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

export const PLUGIN_MANIFEST_FILE = "openneko.plugins.json";
export const PLUGIN_MANIFEST_PATH_ENV = "OPENNEKO_PLUGINS_MANIFEST_PATH";
export const PLUGIN_MANIFEST_SCHEMA_URL =
  "https://open-neko.github.io/plugins/manifest.schema.json";

export function manifestPathFor(repoRoot: string): string {
  const override = process.env[PLUGIN_MANIFEST_PATH_ENV];
  if (override && override.length > 0) return override;
  return path.join(repoRoot, PLUGIN_MANIFEST_FILE);
}

/** Env-var requirement schema (same shape across marketplace + manifest). */
export interface ManifestEnvRequirement {
  key: string;
  required?: boolean;
  secret?: boolean;
  description: string;
}

/** What the plugin needs from the runtime — sandbox network + operator-supplied env. */
export interface ManifestPermissions {
  network: string[];
  env: ManifestEnvRequirement[];
}

/** A single declared action — kind + description, no handler. */
export interface ManifestActionDeclaration {
  kind: string;
  description: string;
}

/**
 * What the plugin contributes. The keyset declares the surfaces this
 * plugin implements; absence of a key means the plugin does not
 * contribute that surface.
 */
export interface ManifestCapabilities {
  action?: { kinds: ManifestActionDeclaration[] };
  auth?: { providerLabel?: string };
}

export interface ManifestEntry {
  name: string;
  version: string;
  integrity: string;
  /** Runtime requirements: network egress + env-var schema. */
  permissions: ManifestPermissions;
  /** Surfaces this plugin contributes (presence = declaration). */
  capabilities: ManifestCapabilities;
  /**
   * Resolved env values (operator-supplied). Worker merges these with
   * the per-user secrets store, with the per-user store winning.
   */
  env?: Record<string, string>;
  /** Display name of the marketplace this plugin came from (traceability). */
  marketplace?: string;
}

export interface Manifest {
  schema: typeof PLUGIN_MANIFEST_SCHEMA_URL;
  plugins: ManifestEntry[];
}

export function emptyManifest(): Manifest {
  return { schema: PLUGIN_MANIFEST_SCHEMA_URL, plugins: [] };
}

export async function readManifest(repoRoot: string): Promise<Manifest | null> {
  const file = manifestPathFor(repoRoot);
  if (!existsSync(file)) return null;
  const raw = await readFile(file, "utf8");
  const parsed = JSON.parse(raw) as unknown;
  if (!isManifest(parsed)) {
    throw new Error(`${file} is malformed`);
  }
  return parsed;
}

export async function writeManifest(
  repoRoot: string,
  manifest: Manifest,
): Promise<void> {
  const file = manifestPathFor(repoRoot);
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, JSON.stringify(manifest, null, 2) + "\n", "utf8");
}

export function upsertEntry(
  manifest: Manifest,
  entry: ManifestEntry,
): Manifest {
  const others = manifest.plugins.filter((p) => p.name !== entry.name);
  return { ...manifest, plugins: [...others, entry] };
}

export function removeEntry(manifest: Manifest, name: string): Manifest {
  return {
    ...manifest,
    plugins: manifest.plugins.filter((p) => p.name !== name),
  };
}

function isManifest(x: unknown): x is Manifest {
  if (typeof x !== "object" || x === null) return false;
  const o = x as { schema?: unknown; plugins?: unknown };
  return (
    typeof o.schema === "string" &&
    Array.isArray(o.plugins) &&
    o.plugins.every(isEntry)
  );
}

function isEntry(x: unknown): x is ManifestEntry {
  if (typeof x !== "object" || x === null) return false;
  const o = x as Record<string, unknown>;
  return (
    typeof o.name === "string" &&
    typeof o.version === "string" &&
    typeof o.integrity === "string"
  );
}
