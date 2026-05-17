// Project-local openneko.plugins.json read/write. This sits next to
// the OpenNeko repo root. Tracked. Pinned versions + integrity
// hashes; the schema URL is informational only.
import { existsSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";

export const PLUGIN_MANIFEST_FILE = "openneko.plugins.json";
export const PLUGIN_MANIFEST_SCHEMA_URL =
  "https://open-neko.github.io/plugins/manifest.schema.json";

export interface ManifestEntry {
  name: string;
  version: string;
  integrity: string;
  capabilities: { network: string[] };
  /**
   * Action kinds this plugin handles. Copied at install time from
   * the marketplace version entry so the worker can build a kind →
   * plugin map from the file alone (no VM spawn needed to know who
   * handles what — enables hot-reload).
   */
  kinds?: string[];
  env?: Record<string, string>;
  /** Display name of the marketplace this plugin came from (for traceability). */
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
  const file = path.join(repoRoot, PLUGIN_MANIFEST_FILE);
  if (!existsSync(file)) return null;
  const raw = await readFile(file, "utf8");
  const parsed = JSON.parse(raw) as unknown;
  if (!isManifest(parsed)) {
    throw new Error(`${PLUGIN_MANIFEST_FILE} is malformed`);
  }
  return parsed;
}

export async function writeManifest(
  repoRoot: string,
  manifest: Manifest,
): Promise<void> {
  const file = path.join(repoRoot, PLUGIN_MANIFEST_FILE);
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
