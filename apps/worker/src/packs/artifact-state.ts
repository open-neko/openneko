import { lstat, readFile, readdir } from "node:fs/promises";
import { extname, join, relative, resolve, sep } from "node:path";
import {
  action_policy,
  and,
  db,
  eq,
  metric,
  pack_action_definition,
  watcher,
  workflow_definition,
} from "@neko/db";
import { canonicalHash, sha256, type PackArtifact } from "@neko/packs";
import { parse as parseYaml } from "yaml";

type ArtifactMetadata = Record<string, unknown>;

export type PackArtifactLocator = {
  source?: string;
  ignorePackAliases?: boolean;
  role?: string;
  slug?: string;
  name?: string;
  kind?: string;
};

const nativeStateKeys = {
  metric: [
    "role", "slug", "source", "title", "description", "why", "chart_hint", "unit",
    "direction_good", "cadence", "active", "execution_mode", "definition_json",
    "definition_version", "definition_hash",
  ],
  workflow: [
    "name", "description", "enabled", "status", "goal", "cron", "cron_timezone",
    "cron_enabled", "output_contract",
  ],
  watcher: [
    "workflow_id", "name", "description", "enabled", "query", "value_path", "op",
    "threshold", "cadence_seconds", "debounce_seconds", "cooldown_seconds", "dedupe_key",
    "activation", "variables_json", "severity",
  ],
  policy: [
    "name", "description", "applies_to_kinds", "applies_to_scopes", "mode",
    "allowed_targets", "limits", "approver_role", "priority",
  ],
  action: ["kind", "description", "definition", "definition_hash", "enabled"],
} as const;

type NativeStateKind = keyof typeof nativeStateKeys;

export function nativeArtifactStateHash(
  kind: NativeStateKind,
  value: Record<string, unknown>,
): string {
  return canonicalHash(
    Object.fromEntries(nativeStateKeys[kind].map((key) => [key, value[key] ?? null])),
  );
}

function isMissing(error: unknown): boolean {
  return (error as NodeJS.ErrnoException).code === "ENOENT";
}

async function hashMaterializedFile(path: string): Promise<string | null> {
  let stat;
  try {
    stat = await lstat(path);
  } catch (error) {
    if (isMissing(error)) return null;
    throw error;
  }
  if (stat.isSymbolicLink()) throw new Error(`pack-owned target became a symlink: ${path}`);
  if (!stat.isFile()) throw new Error(`pack-owned target is not a file: ${path}`);
  const raw = (await readFile(path, "utf8")).replace(/\r\n/g, "\n");
  const extension = extname(path).toLowerCase();
  if (extension === ".json") return canonicalHash(JSON.parse(raw));
  if (extension === ".yaml" || extension === ".yml") return canonicalHash(parseYaml(raw));
  return sha256(raw);
}

async function hashMarkdownTree(root: string, directory = root): Promise<string | null> {
  let stat;
  try {
    stat = await lstat(directory);
  } catch (error) {
    if (isMissing(error)) return null;
    throw error;
  }
  if (stat.isSymbolicLink()) throw new Error(`pack-owned skill became a symlink: ${directory}`);
  if (!stat.isDirectory()) throw new Error(`pack-owned skill is not a directory: ${directory}`);
  const entries = await readdir(directory, { withFileTypes: true });
  const values: Array<{ path: string; hash: string }> = [];
  for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
    const child = join(directory, entry.name);
    if (entry.isSymbolicLink()) throw new Error(`pack-owned skill contains a symlink: ${child}`);
    if (entry.isDirectory()) {
      const hash = await hashMarkdownTree(root, child);
      if (hash) values.push({ path: relative(root, child), hash });
      continue;
    }
    if (!entry.isFile() || extname(entry.name).toLowerCase() !== ".md") {
      throw new Error(`pack-owned skill contains a non-Markdown file: ${child}`);
    }
    values.push({ path: relative(root, child), hash: sha256((await readFile(child, "utf8")).replace(/\r\n/g, "\n")) });
  }
  return canonicalHash(values);
}

function safeMaterializedTarget(configFile: string, metadata: ArtifactMetadata): string | null {
  const target = typeof metadata.materializedTarget === "string" ? metadata.materializedTarget : null;
  if (!target) return null;
  const root = resolve(configFile, "..");
  const absolute = resolve(target);
  const path = relative(root, absolute);
  if (path === ".." || path.startsWith(`..${sep}`) || path.startsWith(sep)) {
    throw new Error(`pack materialized target escapes GraphJin config root: ${target}`);
  }
  return absolute;
}

async function graphjinConfigState(
  configFile: string,
  kind: "source" | "relationships",
  targetRef: string,
  locator: PackArtifactLocator,
): Promise<string | null> {
  let raw: string;
  try {
    raw = await readFile(configFile, "utf8");
  } catch (error) {
    if (isMissing(error)) return null;
    throw error;
  }
  const config = parseYaml(raw) as {
    sources?: Array<Record<string, unknown>>;
    tables?: Array<Record<string, unknown>>;
    relationships?: Array<Record<string, unknown>>;
  };
  if (kind === "source") {
    const source = config.sources?.find((value) => value.name === (locator.name ?? targetRef));
    return source ? canonicalHash(source) : null;
  }
  const source = locator.source ?? targetRef;
  const tables = (config.tables ?? []).filter(
    (value) => (value.source === source || value.database === source) && !(locator.ignorePackAliases && /^pack_[a-f0-9]{20}$/.test(String(value.name))),
  );
  const relationships = (config.relationships ?? []).filter(
    (value) =>
      String(value.from ?? "").startsWith(`${source}:`) ||
      String(value.to ?? "").startsWith(`${source}:`),
  );
  return tables.length || relationships.length || config.sources?.some(value => value.name === source)
    ? canonicalHash({ tables, relationships })
    : null;
}

async function inspectNative(
  orgId: string,
  kind: "metric" | "workflow" | "watcher" | "policy" | "action",
  targetRef: string,
  locator: PackArtifactLocator,
): Promise<string | null> {
  switch (kind) {
    case "metric": {
      const [row] = await db().select().from(metric).where(and(
        eq(metric.org_id, orgId),
        eq(metric.role, locator.role ?? ""),
        eq(metric.slug, locator.slug ?? targetRef),
      )).limit(1);
      return row ? nativeArtifactStateHash("metric", row as unknown as Record<string, unknown>) : null;
    }
    case "workflow": {
      const [row] = await db().select().from(workflow_definition).where(and(
        eq(workflow_definition.org_id, orgId),
        eq(workflow_definition.owner_user_id, ""),
        eq(workflow_definition.name, locator.name ?? targetRef),
      )).limit(1);
      return row ? nativeArtifactStateHash("workflow", row as unknown as Record<string, unknown>) : null;
    }
    case "watcher": {
      const [row] = await db().select().from(watcher).where(and(
        eq(watcher.org_id, orgId),
        eq(watcher.name, locator.name ?? targetRef),
      )).limit(1);
      return row ? nativeArtifactStateHash("watcher", row as unknown as Record<string, unknown>) : null;
    }
    case "policy": {
      const [row] = await db().select().from(action_policy).where(and(
        eq(action_policy.org_id, orgId),
        eq(action_policy.name, locator.name ?? targetRef),
      )).limit(1);
      return row ? nativeArtifactStateHash("policy", row as unknown as Record<string, unknown>) : null;
    }
    case "action": {
      const [row] = await db().select().from(pack_action_definition).where(and(
        eq(pack_action_definition.org_id, orgId),
        eq(pack_action_definition.kind, locator.kind ?? targetRef),
      )).limit(1);
      return row ? nativeArtifactStateHash("action", row as unknown as Record<string, unknown>) : null;
    }
    default:
      return null;
  }
}

export function packArtifactLocator(artifact: PackArtifact): PackArtifactLocator {
  const content = artifact.content && typeof artifact.content === "object" && !Array.isArray(artifact.content)
    ? artifact.content as Record<string, unknown>
    : {};
  switch (artifact.kind) {
    case "source":
      return { name: artifact.targetRef };
    case "relationships":
      return { source: String(content.source ?? artifact.targetRef) };
    case "metric":
      return { role: String(content.role ?? ""), slug: artifact.targetRef };
    case "workflow":
    case "watcher":
    case "policy":
      return { name: String(content.name ?? artifact.targetRef) };
    case "action":
      return { kind: String(content.kind ?? artifact.targetRef) };
    default:
      return {};
  }
}

function locatorFromMetadata(
  metadata: ArtifactMetadata,
  fallback?: PackArtifact,
): PackArtifactLocator {
  const value = metadata.locator;
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as PackArtifactLocator;
  }
  return fallback ? packArtifactLocator(fallback) : {};
}

export async function inspectInstalledPackArtifactCurrent(input: {
  orgId: string;
  kind: PackArtifact["kind"];
  targetRef: string;
  metadata: ArtifactMetadata;
  graphjinConfigFile: string;
  fallbackArtifact?: PackArtifact;
}): Promise<string | null> {
  const locator = locatorFromMetadata(input.metadata, input.fallbackArtifact);
  switch (input.kind) {
    case "source":
    case "relationships":
      return graphjinConfigState(input.graphjinConfigFile, input.kind, input.targetRef, locator);
    case "spec":
    case "saved_query": {
      const target = safeMaterializedTarget(input.graphjinConfigFile, input.metadata);
      return target ? hashMaterializedFile(target) : null;
    }
    case "skill": {
      const target = typeof input.metadata.materializedTarget === "string"
        ? input.metadata.materializedTarget
        : null;
      return target ? hashMarkdownTree(target) : null;
    }
    case "metric":
    case "workflow":
    case "watcher":
    case "policy":
    case "action":
      return inspectNative(input.orgId, input.kind, input.targetRef, locator);
  }
}

export async function inspectPackArtifactCurrent(input: {
  orgId: string;
  artifact: PackArtifact;
  metadata: ArtifactMetadata;
  graphjinConfigFile: string;
}): Promise<string | null> {
  return inspectInstalledPackArtifactCurrent({
    orgId: input.orgId,
    kind: input.artifact.kind,
    targetRef: input.artifact.targetRef,
    metadata: input.metadata,
    graphjinConfigFile: input.graphjinConfigFile,
    fallbackArtifact: input.artifact,
  });
}
