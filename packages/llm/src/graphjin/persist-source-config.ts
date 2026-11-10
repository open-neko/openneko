import { randomUUID } from "node:crypto";
import { open, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import {
  type Document,
  isMap,
  isScalar,
  isSeq,
  parseDocument,
  YAMLMap,
  YAMLSeq,
} from "yaml";

type SourcePatch = Record<string, unknown> & { name?: unknown };

const GRAPHJIN_CONFIG_LOCK_STALE_MS = 10 * 60 * 1_000;

/**
 * Serialize durable GraphJin config writers across web/worker processes.
 * GraphJin's catalog revision protects the live engine; this lock provides
 * the matching optimistic boundary for the shared YAML file.
 */
export async function acquireGraphjinConfigLock(input: {
  configFile: string;
}): Promise<() => Promise<void>> {
  const lockPath = join(
    /* turbopackIgnore: true */ dirname(input.configFile),
    `.${basename(input.configFile)}.openneko.lock`,
  );
  const token = randomUUID();
  const createLock = async () => {
    const handle = await open(
      /* turbopackIgnore: true */ lockPath,
      "wx",
      0o600,
    );
    try {
      await handle.writeFile(token);
      await handle.sync();
    } finally {
      await handle.close();
    }
  };
  try {
    await createLock();
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    const metadata = await stat(
      /* turbopackIgnore: true */ lockPath,
    ).catch(() => null);
    if (!metadata || Date.now() - metadata.mtimeMs <= GRAPHJIN_CONFIG_LOCK_STALE_MS) {
      throw new Error("GraphJin config has another approved change in progress");
    }
    await rm(/* turbopackIgnore: true */ lockPath, { force: true });
    await createLock().catch((retryError) => {
      if ((retryError as NodeJS.ErrnoException).code === "EEXIST") {
        throw new Error("GraphJin config has another approved change in progress");
      }
      throw retryError;
    });
  }
  return async () => {
    const currentToken = await readFile(
      /* turbopackIgnore: true */ lockPath,
      "utf8",
    ).catch(() => "");
    if (currentToken === token) {
      await rm(/* turbopackIgnore: true */ lockPath, { force: true });
    }
  };
}

function sourceName(patch: SourcePatch): string {
  const name = typeof patch.name === "string" ? patch.name.trim() : "";
  if (!name) throw new Error("GraphJin source persistence needs a source name");
  return name;
}

function secretRefSegment(value: string): string {
  return value
    .trim()
    .replaceAll("/", "_")
    .replaceAll("\\", "_")
    .replaceAll("\0", "_");
}

function isSensitiveConfigKey(key: string): boolean {
  const normalized = key.toLowerCase().replaceAll("-", "_");
  return (
    normalized.includes("password") ||
    normalized.includes("secret") ||
    normalized.includes("token") ||
    normalized.includes("passphrase") ||
    normalized.includes("private_key") ||
    normalized.includes("client_key") ||
    normalized.includes("api_key") ||
    normalized.includes("apikey") ||
    normalized.includes("authorization") ||
    normalized.includes("cookie") ||
    normalized === "connection_string" ||
    normalized === "key_value"
  );
}

function shouldReplaceWithSecretRef(value: string): boolean {
  const trimmed = value.trim();
  return Boolean(trimmed) && !trimmed.startsWith("gjsecret://") && !value.includes("${");
}

function sealSourceForDisk(
  value: unknown,
  path: string[],
  parentKey = "",
): unknown {
  if (Array.isArray(value)) {
    return value.map((item, index) => {
      const named = item && typeof item === "object" && !Array.isArray(item) &&
        typeof (item as Record<string, unknown>).name === "string"
        ? String((item as Record<string, unknown>).name)
        : String(index);
      return sealSourceForDisk(item, [...path, secretRefSegment(named)]);
    });
  }
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([key, child]) => [
        key,
        sealSourceForDisk(child, [...path, secretRefSegment(key)], key),
      ]),
    );
  }
  if (
    typeof value === "string" &&
    (isSensitiveConfigKey(parentKey) ||
      (path.length >= 2 && path.at(-2)?.toLowerCase() === "headers" && isSensitiveConfigKey(parentKey))) &&
    shouldReplaceWithSecretRef(value)
  ) {
    return `gjsecret://${path.filter(Boolean).join("/")}`;
  }
  return value;
}

function sourceForDisk(patch: SourcePatch): SourcePatch {
  const name = sourceName(patch);
  return sealSourceForDisk(
    structuredClone(patch),
    ["sources", secretRefSegment(name)],
  ) as SourcePatch;
}

function mapByName(sources: YAMLSeq, name: string): YAMLMap | null {
  for (const item of sources.items) {
    if (isMap(item) && item.get("name") === name) return item;
  }
  return null;
}

export async function graphjinConfigHasSource(input: {
  configFile: string;
  sourceName: string;
}): Promise<boolean> {
  const raw = await readFile(
    /* turbopackIgnore: true */ input.configFile,
    "utf8",
  );
  const document = parseDocument(raw);
  if (document.errors.length > 0 || !isMap(document.contents)) {
    throw new Error("GraphJin config is unavailable for source validation");
  }
  const sources = document.get("sources", true);
  if (sources == null) return false;
  if (!isSeq(sources)) {
    throw new Error("GraphJin config sources must be a YAML list");
  }
  return mapByName(sources, input.sourceName) !== null;
}

function mergeMap(target: YAMLMap, patch: Record<string, unknown>): void {
  for (const [key, value] of Object.entries(patch)) {
    const current = target.get(key, true);
    if (
      value &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      isMap(current)
    ) {
      mergeMap(current, value as Record<string, unknown>);
    } else {
      target.set(key, value);
    }
  }
}

const TABLE_LIST_PATCHES = ["public_tables", "admin_tables", "blocked_tables"] as const;

function stringItems(node: unknown): string[] {
  return isSeq(node) ? node.items.map((item) => String(isScalar(item) ? item.value : item)) : [];
}

function withoutFold(values: string[], name: string): string[] {
  return values.filter((value) => value.trim().toLowerCase() !== name.trim().toLowerCase());
}

/** Mirrors GraphJin's classification patch: removes first, and an added table leaves the other lists. */
function applyAccessPatch(source: YAMLMap, access: Record<string, unknown>): void {
  const plain: Record<string, unknown> = {};
  const lists = new Map<string, string[]>();
  const found = source.get("access", true);
  const accessNode: YAMLMap = isMap(found) ? found : new YAMLMap();
  for (const group of TABLE_LIST_PATCHES) {
    lists.set(group, stringItems(accessNode.get(group, true)));
  }
  let touched = false;
  const removesFirst = Object.entries(access).sort(
    ([left], [right]) => Number(right.endsWith("_remove")) - Number(left.endsWith("_remove")),
  );
  for (const [key, value] of removesFirst) {
    const match = /^(public_tables|admin_tables|blocked_tables)_(add|remove)$/.exec(key);
    if (!match) {
      plain[key] = value;
      continue;
    }
    if (!Array.isArray(value)) throw new Error(`GraphJin access.${key} must be a list`);
    touched = true;
    for (const name of value.map(String)) {
      if (match[2] === "remove") {
        lists.set(match[1], withoutFold(lists.get(match[1])!, name));
        continue;
      }
      for (const group of TABLE_LIST_PATCHES) {
        lists.set(group, withoutFold(lists.get(group)!, name));
      }
      lists.get(match[1])!.push(name.trim());
    }
  }
  if (touched) {
    for (const group of TABLE_LIST_PATCHES) {
      const values = lists.get(group)!;
      if (values.length > 0 || accessNode.has(group)) plain[group] = values;
    }
  }
  if (!isMap(found)) source.set("access", accessNode);
  mergeMap(accessNode, plain);
}

function persistRoles(document: Document, roles: unknown): void {
  if (!Array.isArray(roles) || roles.length === 0) return;
  let rolesNode = document.get("roles", true);
  if (rolesNode == null) {
    rolesNode = new YAMLSeq();
    document.set("roles", rolesNode);
  }
  if (!isSeq(rolesNode)) throw new Error("GraphJin config roles must be a YAML list");
  for (const value of roles) {
    if (!value || typeof value !== "object" || Array.isArray(value)) {
      throw new Error("GraphJin roles contains an invalid role");
    }
    const role = value as Record<string, unknown>;
    const name = typeof role.name === "string" ? role.name.trim() : "";
    if (!name) throw new Error("GraphJin role persistence needs a role name");
    const index = rolesNode.items.findIndex(
      (item) =>
        isMap(item) &&
        String(item.get("name") ?? "").trim().toLowerCase() === name.toLowerCase(),
    );
    if (index >= 0) rolesNode.items[index] = document.createNode(role);
    else {
      rolesNode.flow = false;
      rolesNode.add(document.createNode(role));
    }
  }
}

/**
 * Persist the already-approved source portion of a GraphJin config update.
 * GraphJin intentionally keeps production-mode gj_config writes in memory;
 * OpenNeko owns durability for managed sources because its approval worker is
 * the trusted writer that shares the config volume.
 */
export async function persistGraphjinSourceConfigUpdate(input: {
  configFile: string;
  update: Record<string, unknown>;
}): Promise<void> {
  const raw = await readFile(
    /* turbopackIgnore: true */ input.configFile,
    "utf8",
  );
  const merged = mergeGraphjinSourceConfigUpdate(raw, input.update);
  const previousMode = (
    await stat(/* turbopackIgnore: true */ input.configFile)
  ).mode & 0o777;
  const temporary = join(
    /* turbopackIgnore: true */ dirname(input.configFile),
    `.${randomUUID()}.graphjin-config.tmp`,
  );
  await writeFile(
    /* turbopackIgnore: true */ temporary,
    merged,
    { mode: previousMode },
  );
  await rename(
    /* turbopackIgnore: true */ temporary,
    input.configFile,
  );
}

/** The config file text after the update, as the approval worker writes it. */
export function mergeGraphjinSourceConfigUpdate(
  raw: string,
  update: Record<string, unknown>,
): string {
  const document = parseDocument(raw);
  if (document.errors.length > 0) {
    throw new Error(
      `GraphJin config is not valid YAML: ${document.errors.map((error) => error.message).join("; ")}`,
    );
  }
  if (!isMap(document.contents)) {
    throw new Error("GraphJin config root must be a YAML object");
  }

  let sourcesNode = document.get("sources", true);
  const touchesSources = ["update_sources", "remove_sources", "source_patches"].some(
    (key) => Array.isArray(update[key]) && (update[key] as unknown[]).length > 0,
  );
  if (sourcesNode == null) {
    sourcesNode = new YAMLSeq();
    if (touchesSources) document.set("sources", sourcesNode);
  }
  if (!isSeq(sourcesNode)) {
    throw new Error("GraphJin config sources must be a YAML list");
  }

  const updates = update.update_sources;
  if (Array.isArray(updates)) {
    for (const value of updates) {
      if (!value || typeof value !== "object" || Array.isArray(value)) {
        throw new Error("GraphJin update_sources contains an invalid source");
      }
      const patch = sourceForDisk(value as SourcePatch);
      const name = sourceName(patch);
      const existing = mapByName(sourcesNode, name);
      if (existing) mergeMap(existing, patch);
      else {
        sourcesNode.flow = false;
        sourcesNode.add(document.createNode(patch));
      }
    }
  }

  const removals = update.remove_sources;
  if (Array.isArray(removals)) {
    const names = new Set(
      removals.map((value, index) => {
        if (typeof value !== "string" || !value.trim()) {
          throw new Error(`GraphJin remove_sources[${index}] must be a source name`);
        }
        return value.trim().toLowerCase();
      }),
    );
    sourcesNode.items = sourcesNode.items.filter(
      (item) => !isMap(item) || !names.has(String(item.get("name") ?? "").trim().toLowerCase()),
    );
  }

  const accessPatches = update.source_patches;
  if (Array.isArray(accessPatches)) {
    for (const value of accessPatches) {
      if (!value || typeof value !== "object" || Array.isArray(value)) {
        throw new Error("GraphJin source_patches contains an invalid source");
      }
      const { access, ...patch } = value as SourcePatch;
      const name = sourceName(patch);
      const existing = mapByName(sourcesNode, name);
      if (!existing) {
        throw new Error(`GraphJin source ${name} is absent from the durable config`);
      }
      mergeMap(existing, patch);
      if (access && typeof access === "object" && !Array.isArray(access)) {
        applyAccessPatch(existing, access as Record<string, unknown>);
      }
    }
  }

  persistRoles(document, update.roles);
  return document.toString();
}
