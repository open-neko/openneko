import { randomUUID } from "node:crypto";
import { readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { parse, parseDocument } from "yaml";
import {
  acquireGraphjinConfigLock,
  graphjinInputWithJsonVariables,
  persistGraphjinSourceConfigUpdate,
} from "@neko/llm/graphjin";
import {
  graphjinQuery,
  mintGraphjinToken,
  type GraphjinQueryResult,
} from "@neko/llm/graphjin";

export type AppliedGraphjinConfig = {
  catalogRevision: string | null;
  restore: () => Promise<void>;
};

function configArray(value: unknown): Record<string, unknown>[] {
  if (typeof value === "string") {
    try {
      return configArray(JSON.parse(value));
    } catch {
      return [];
    }
  }
  return Array.isArray(value)
    ? value.filter(
        (item): item is Record<string, unknown> =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

export function durablePackTables(value: unknown): Record<string, unknown>[] {
  return configArray(value).map((table) => {
    if (typeof table.source !== "string" || !table.source.trim()) return table;
    const { database: _database, ...durable } = table;
    return durable;
  });
}

function mergeByKey(
  current: Record<string, unknown>[],
  additions: Record<string, unknown>[],
  key: (value: Record<string, unknown>) => string,
): Record<string, unknown>[] {
  const merged = new Map(current.map((value) => [key(value), value]));
  for (const value of additions) merged.set(key(value), value);
  return [...merged.values()];
}

function isCatalogRevisionConflict(messages: Array<string | null | undefined>): boolean {
  return messages.some((message) =>
    Boolean(message?.includes("expected_catalog_revision") && message.includes("does not match")),
  );
}

function currentRevisionFromConflict(messages: Array<string | null | undefined>): string | null {
  for (const message of messages) {
    const match = /current catalog_revision[^a-f0-9]*([a-f0-9]{64})/i.exec(message ?? "");
    if (match?.[1]) return match[1];
  }
  return null;
}

async function persistPackSections(
  configFile: string,
  update: Record<string, unknown>,
): Promise<void> {
  if (!update.roles && !update.tables && !update.relationships) return;
  const raw = await readFile(configFile, "utf8");
  const document = parseDocument(raw);
  if (document.errors.length > 0 || !document.contents) {
    throw new Error("GraphJin config is invalid after source persistence");
  }
  if (update.tables) {
    // GraphJin 3.18 needs `database` to resolve a table against a source that
    // is being introduced in the same live patch, but rejects that legacy
    // alias on a later config-file reload. Keep it ephemeral.
    document.set("tables", durablePackTables(update.tables));
  }
  if (update.roles) document.set("roles", update.roles);
  if (update.relationships) document.set("relationships", update.relationships);
  const mode = (await stat(configFile)).mode & 0o777;
  const temporary = `${configFile}.${randomUUID()}.pack-sections`;
  await writeFile(temporary, document.toString(), { mode });
  await rename(temporary, configFile);
}

async function requestGraphjinRestart(configFile: string, endpoint: string): Promise<void> {
  const directory = dirname(configFile);
  const requestFile = join(directory, ".openneko-graphjin-restart");
  const acknowledgementFile = join(directory, ".openneko-graphjin-restart-ack");
  const token = randomUUID();
  const temporary = `${requestFile}.${token}.tmp`;
  await writeFile(temporary, token, { mode: 0o666 });
  await rename(temporary, requestFile);

  const deadline = Date.now() + 45_000;
  while (Date.now() < deadline) {
    const acknowledged = await readFile(acknowledgementFile, "utf8").catch(() => "");
    if (acknowledged.trim() === token) {
      try {
        await fetch(endpoint, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ query: "query OpenNekoRestartReady { __typename }" }),
          signal: AbortSignal.timeout(2_000),
        });
        return;
      } catch {
        // The supervisor acknowledged the replacement child; wait until its
        // listener is reachable before returning control to pack canaries.
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(
    "graphjin_restart_required: the persisted pack config was not acknowledged by the GraphJin supervisor",
  );
}

async function assertGraphjinSupervisor(configFile: string): Promise<void> {
  const directory = dirname(configFile);
  const pingFile = join(directory, ".openneko-graphjin-supervisor-ping");
  const acknowledgementFile = join(directory, ".openneko-graphjin-supervisor-ping-ack");
  const token = randomUUID();
  const temporary = `${pingFile}.${token}.tmp`;
  await writeFile(temporary, token, { mode: 0o666 });
  await rename(temporary, pingFile);
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const acknowledged = await readFile(acknowledgementFile, "utf8").catch(() => "");
    if (acknowledged.trim() === token) return;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(
    "graphjin_restart_required: this deployment has not adopted the GraphJin supervisor",
  );
}

/**
 * Apply one trusted pack update through GraphJin's preview/apply boundary and
 * persist the source portion to the shared config file. The live preview is
 * what seals plaintext connection credentials into GraphJin's keystore; the
 * durable YAML receives only gjsecret:// references.
 */
export async function applyPackGraphjinConfig(input: {
  endpoint: string;
  orgId: string;
  configFile: string;
  update: Record<string, unknown>;
  ownedSourceNames?: Set<string>;
  ownedTableNames?: Set<string>;
  sectionMode?: "merge" | "replace";
  restartAfterPersist?: boolean;
}): Promise<AppliedGraphjinConfig> {
  const release = await acquireGraphjinConfigLock({ configFile: input.configFile });
  try {
    const previous = await readFile(input.configFile);
    const previousMode = (await stat(input.configFile)).mode & 0o777;
    if (input.restartAfterPersist) {
      await assertGraphjinSupervisor(input.configFile);
    }
    const durableConfig = parse(previous.toString("utf8")) as {
      roles?: unknown;
      tables?: unknown;
      relationships?: unknown;
      secrets?: { keystore?: { path?: string } };
    };
    const configuredKeystore = durableConfig.secrets?.keystore?.path?.trim() || "secrets.enc.yml";
    const keystore = configuredKeystore.startsWith("/config/")
      ? resolve(dirname(input.configFile), configuredKeystore.slice("/config/".length))
      : resolve(dirname(input.configFile), configuredKeystore);
    const keystoreRelative = relative(dirname(input.configFile), keystore);
    if ((keystoreRelative === ".." || keystoreRelative.startsWith("../")) || isAbsolute(keystoreRelative)) throw new Error("pack credential keystore must be inside the shared GraphJin config directory");
    const readKeystore = () => readFile(keystore).catch((error: NodeJS.ErrnoException) => {
      if (error.code === "ENOENT") return null;
      throw error;
    });
    // Stable gjsecret:// references are overwritten when credentials change.
    // Restoring YAML alone would leave a failed replacement token active.
    const previousKeystore = await readKeystore();
    const headers = {
      authorization: `Bearer ${mintGraphjinToken({
        orgId: input.orgId,
        userId: "pack-installer",
        role: "admin",
        ttlSeconds: 120,
      })}`,
    };
    const requestedTables = configArray(input.update.tables);
    const requestedRelationships = configArray(input.update.relationships);
    const requestedRoles = configArray(input.update.roles);
    const requestedSources = configArray(input.update.update_sources);
    let appliedUpdate: Record<string, unknown> | null = null;
    let catalogRevision: string | null = null;
    let conflictRevision: string | null = null;
    const maxRevisionAttempts = 8;
    for (let attempt = 1; attempt <= maxRevisionAttempts; attempt++) {
      // GraphJin 3.18's response cache also covers gj_config reads. Give each
      // optimistic-concurrency attempt a unique operation name so a reload
      // cannot strand the installer on a cached catalog revision.
      const revisionOperation = `PackConfigRevision_${attempt}_${randomUUID().replaceAll("-", "")}`;
      const current = await graphjinQuery<{
        gj_config?: { catalog_revision?: string; sources?: unknown; tables?: unknown; relationships?: unknown };
      }>({
        baseUrl: input.endpoint,
        configurationOnly: true,
        headers,
        query: `query ${revisionOperation} { gj_config(id: "current") { catalog_revision sources tables relationships } }`,
      });
      const reportedRevision = current.data?.gj_config?.catalog_revision;
      if (!reportedRevision || current.errors?.length) {
        throw new Error(
          `pack GraphJin config revision unavailable: ${current.errors?.map((error) => error.message).join("; ") ?? "no revision"}`,
        );
      }
      // In GraphJin 3.18.42, filesystem discovery can invalidate the live
      // catalog before its cached gj_config row catches up. A stale-revision
      // rejection includes the authoritative current revision; carry that
      // guarded value into the next preview attempt.
      const revision = conflictRevision ?? reportedRevision;

      const currentSourceNames = new Set(
        configArray(current.data?.gj_config?.sources).map((source) => String(source.name ?? "")),
      );
      for (const source of requestedSources) {
        const name = String(source.name ?? "");
        if (name && currentSourceNames.has(name) && !input.ownedSourceNames?.has(name)) {
          throw new Error(`GraphJin source ${name} already exists and is not owned by this pack`);
        }
      }
      if (input.ownedTableNames) {
        const existingNames = new Set([...configArray(durableConfig.tables), ...configArray(current.data?.gj_config?.tables)].map(table => String(table.name)));
        for (const table of requestedTables) {
          if (existingNames.has(String(table.name)) && !input.ownedTableNames.has(String(table.name))) throw new Error(`GraphJin table ${table.name} already exists and is not owned by this pack`);
        }
      }
      const update = {
        ...input.update,
        ...(requestedRoles.length > 0
          ? {
              roles: mergeByKey(
                configArray(durableConfig.roles),
                requestedRoles,
                (value) => String(value.name ?? ""),
              ),
            }
          : {}),
        ...(input.sectionMode === "replace" && Object.hasOwn(input.update, "tables")
          ? { tables: requestedTables }
          : requestedTables.length > 0
          ? {
              tables: mergeByKey(
                configArray(durableConfig.tables),
                requestedTables,
                (value) => `${String(value.source ?? "")}:${String(value.name ?? "")}`,
              ),
            }
          : {}),
        ...(input.sectionMode === "replace" && Object.hasOwn(input.update, "relationships")
          ? { relationships: requestedRelationships }
          : requestedRelationships.length > 0
          ? {
              relationships: mergeByKey(
                configArray(durableConfig.relationships),
                requestedRelationships,
                (value) => `${String(value.from ?? "")}->${String(value.to ?? "")}`,
              ),
            }
          : {}),
      };
      const previewInput = graphjinInputWithJsonVariables({
        mode: "preview",
        expected_catalog_revision: revision,
        ...update,
      });
      let preview: GraphjinQueryResult<{
        gj_config?: { valid?: boolean; preview_id?: string; errors_json?: string };
      }>;
      try {
        preview = await graphjinQuery({
          baseUrl: input.endpoint,
        configurationOnly: true,
          headers,
          query: `mutation${previewInput.variableDefinitions} { gj_config(id: "current", update: ${previewInput.literal}) { valid preview_id errors_json } }`,
          variables: previewInput.variables,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        if (attempt < maxRevisionAttempts && isCatalogRevisionConflict([message])) {
          conflictRevision = currentRevisionFromConflict([message]);
          await new Promise((resolve) => setTimeout(resolve, 250));
          continue;
        }
        throw error;
      }
      const previewResult = preview.data?.gj_config;
      const previewMessages = [
        ...(preview.errors?.map((error) => error.message) ?? []),
        previewResult?.errors_json,
      ];
      if (preview.errors?.length || !previewResult?.valid || !previewResult.preview_id) {
        if (attempt < maxRevisionAttempts && isCatalogRevisionConflict(previewMessages)) {
          conflictRevision = currentRevisionFromConflict(previewMessages);
          await new Promise((resolve) => setTimeout(resolve, 250));
          continue;
        }
        throw new Error(
          `pack GraphJin config preview failed: ${preview.errors?.map((error) => error.message).join("; ") ?? previewResult?.errors_json ?? "invalid preview"}`,
        );
      }

      const applyInput = graphjinInputWithJsonVariables({
        mode: "apply",
        preview_id: previewResult.preview_id,
        expected_catalog_revision: revision,
        ...update,
      });
      let applied: GraphjinQueryResult<{
        gj_config?: { applied?: boolean; catalog_revision?: string; errors_json?: string };
      }>;
      try {
        applied = await graphjinQuery({
          baseUrl: input.endpoint,
        configurationOnly: true,
          headers,
          query: `mutation${applyInput.variableDefinitions} { gj_config(id: "current", update: ${applyInput.literal}) { applied catalog_revision errors_json } }`,
          variables: applyInput.variables,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        if (attempt < maxRevisionAttempts && isCatalogRevisionConflict([message])) {
          conflictRevision = currentRevisionFromConflict([message]);
          await new Promise((resolve) => setTimeout(resolve, 250));
          continue;
        }
        throw error;
      }
      const result = applied.data?.gj_config;
      const applyMessages = [
        ...(applied.errors?.map((error) => error.message) ?? []),
        result?.errors_json,
      ];
      if (applied.errors?.length || !result?.applied) {
        if (attempt < maxRevisionAttempts && isCatalogRevisionConflict(applyMessages)) {
          conflictRevision = currentRevisionFromConflict(applyMessages);
          await new Promise((resolve) => setTimeout(resolve, 250));
          continue;
        }
        throw new Error(
          `pack GraphJin config apply failed: ${applied.errors?.map((error) => error.message).join("; ") ?? result?.errors_json ?? "apply rejected"}`,
        );
      }
      appliedUpdate = update;
      catalogRevision = result.catalog_revision ?? null;
      break;
    }
    if (!appliedUpdate) throw new Error("pack GraphJin config revision did not stabilize");

    const restoreSnapshot = async () => {
      if (previousKeystore) {
        const temporaryKeystore = `${keystore}.${randomUUID()}.pack-rollback`;
        await writeFile(temporaryKeystore, previousKeystore, { mode: 0o600 });
        await rename(temporaryKeystore, keystore);
      } else await rm(keystore, { force: true });
      const temporary = `${input.configFile}.${randomUUID()}.pack-rollback`;
      await writeFile(temporary, previous, { mode: previousMode });
      await rename(temporary, input.configFile);
      if (input.restartAfterPersist) {
        await requestGraphjinRestart(input.configFile, input.endpoint);
      }
    };

    let appliedConfig: Buffer;
    let appliedKeystore: Buffer | null;
    try {
      await persistGraphjinSourceConfigUpdate({
        configFile: input.configFile,
        update: appliedUpdate,
      });
      await persistPackSections(input.configFile, appliedUpdate);
      if (input.restartAfterPersist) {
        await requestGraphjinRestart(input.configFile, input.endpoint);
      }
      appliedConfig = await readFile(input.configFile);
      appliedKeystore = await readKeystore();
    } catch (error) {
      try {
        await restoreSnapshot();
      } catch (rollbackError) {
        throw new AggregateError(
          [error, rollbackError],
          "pack GraphJin persistence failed and its live update could not be rolled back",
        );
      }
      throw error;
    }
    return {
      catalogRevision,
      restore: async () => {
        const unlock = await acquireGraphjinConfigLock({ configFile: input.configFile });
        try {
          const currentKeystore = await readKeystore();
          if (!(await readFile(input.configFile)).equals(appliedConfig) ||
              !((currentKeystore === null && appliedKeystore === null) || (currentKeystore && appliedKeystore && currentKeystore.equals(appliedKeystore)))) {
            throw new Error("GraphJin configuration changed after pack apply; rollback preserved the newer state");
          }
          await restoreSnapshot();
        } finally { await unlock(); }
      },
    };
  } finally {
    await release();
  }
}
