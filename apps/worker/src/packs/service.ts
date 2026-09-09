import { packConnection, type PackConnectionContext } from "./connections.js";
import { runPackConnector } from "./connector-runner.js";
import { listUploadedPacks, loadUploadedPack, storePackUpload, snapshotUploadedPack, type AvailablePack } from "./uploads.js";
import { parse as parseYaml } from "yaml";
import { extractValueAtPath, resolveWatcherVariables } from "@neko/llm/workflows";
import { mapSavedQueryMetric } from "../jobs/deterministic-metric.js";
import { bindPackQueries, declarativeGraphjinUpdate, packVariables } from "./declarative.js";
import { createHmac, randomUUID } from "node:crypto";
import {
  cp,
  lstat,
  mkdir,
  readFile,
  readdir,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { basename, dirname, extname, join, relative, resolve, sep } from "node:path";
import {
  localConfigPath,
  action_policy,
  action_changeset,
  action_changeset_row,
  and,
  data_source,
  db,
  desc,
  eq,
  inArray,
  metric,
  magento_attribute_classification,
  magento_auto_rule,
  magento_financial_handoff,
  magento_store_control,
  pack_action_definition,
  pack_artifact,
  pack_install,
  pack_connection_client,
  pack_operation,
  processing_job,
  pool,
  watcher,
  workflow_definition,
} from "@neko/db";
import { ensureOrgWorkspace } from "@neko/llm/work";
import { graphjinQuery, graphjinSigningSecret, mintGraphjinToken, resolvePackSource, verifyPackQueryTables, graphjinConfigPatchHash, type PackSourceSelection } from "@neko/llm/graphjin";
import {
  canonicalHash,
  DEFAULT_MAGENTO_ATTRIBUTE_CLASSIFICATIONS,
  DEFAULT_MAGENTO_CAPS,
  DEFAULT_MAGENTO_DOMAIN_CONTROLS,
  loadSolutionPack,
  magentoExecutionMode,
  planPack,
  type PackArtifact,
  type PackPlan,
  type MagentoDomain,
  type MagentoRiskClass,
  type SolutionPackBundle,
} from "@neko/packs";
import {
  readSecretsStore,
  writeSecretsStore,
} from "@open-neko/plugin-install/secrets";
import { applyPackGraphjinConfig } from "./graphjin-config.js";
import {
  runMagentoPreflight,
  type MagentoPreflightResult,
} from "./magento-preflight.js";
import { magentoGraphjinTables } from "./magento-source-policy.js";
import { buildMagentoActivity, isMagentoTestRule } from "./magento-activity.js";
import {
  inspectPackArtifactCurrent,
  inspectInstalledPackArtifactCurrent,
  nativeArtifactStateHash,
  packArtifactLocator,
} from "./artifact-state.js";

const PACK_ID = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;
const PACK_SECRET_PREFIX = "pack.";

type PackInstallRequest = {
  version?: string;
  reviewHash?: string;
  inputs?: Record<string, unknown>;
  dataSourceId?: string;
  sourceBindings?: Record<string, string>;
  secrets?: Record<string, string>;
  secretRefs?: Record<string, string>;
  idempotencyKey?: string;
  actorUserId?: string | null;
};

type PackUninstallRequest = {
  idempotencyKey?: string;
  actorUserId?: string | null;
};

type PackStatus = {
  packId: string;
  version: string;
  status: string;
  readiness: Record<string, { status: string; reason: string | null }>;
  installedAt: string | null;
  lastError: string | null;
  configuration: { inputs: Record<string, unknown>; dataSourceId?: string; sourceBindings: Record<string, string> };
};

type PackDoctorResult = {
  packId: string;
  status: "ready" | "degraded" | "blocked";
  checks: Array<{
    id: string;
    status: "ready" | "optional" | "blocked";
    detail: string;
  }>;
};

type PackRuntime = {
  connectorBundleHash?: string;
  source?: PackSourceSelection;
  bindings: Record<string, string>;
  bindingHashes: Record<string, string>;
  readiness: string[];
  tables?: Array<Record<string, string>>;
};

function storedRuntime(config: Record<string, unknown>): PackRuntime | undefined {
  return config._runtime as PackRuntime | undefined;
}

function boundPackLocator(bundle: SolutionPackBundle, artifact: PackArtifact, bindings: Record<string, string>): Record<string, unknown> {
  if (bindings[artifact.key]) return { name: bindings[artifact.key] };
  if (artifact.kind === "relationships") {
    const source = bundle.artifacts.find(value => value.kind === "source" && artifactRecord(value).name === artifactRecord(artifact).source);
    if (source && bindings[source.key]) return { source: bindings[source.key], ignorePackAliases: true };
  }
  return packArtifactLocator(artifact);
}

function operatorReadinessDetail(
  reason: MagentoPreflightResult["operatorReadiness"] | null,
): string {
  switch (reason) {
    case "integration_token_missing":
      return "View only. Add a Magento API token if you later want to allow specific, approval-required changes; store insights and automations are fully available without it.";
    case "integration_token_invalid":
      return "View only because Magento rejected the saved API token. Store insights and automations are unaffected.";
    case "acl_missing":
      return "View only because the saved API token does not have the required Magento permissions. Store insights and automations are unaffected.";
    case "graphjin_version_unsupported":
      return "View only in this version. Store insights and automations are fully available.";
    case "ready":
      return "Approved Magento changes are available. Each area follows its configured approval and automation limits.";
    case "domain_disabled":
      return "This change domain is disabled by the administrator.";
    default:
      return "View-only access could not be checked because the reporting connection is unavailable.";
  }
}

function artifactRecord(artifact: PackArtifact): Record<string, unknown> {
  if (!artifact.content || typeof artifact.content !== "object" || Array.isArray(artifact.content)) {
    throw new Error(`${artifact.kind} artifact ${artifact.path} must be an object`);
  }
  return artifact.content as Record<string, unknown>;
}

function artifactLocatorFromMetadata(
  metadata: Record<string, unknown> | null | undefined,
  fallback?: PackArtifact,
): Record<string, unknown> {
  const value = metadata?.locator;
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>;
  }
  return fallback ? packArtifactLocator(fallback) : {};
}

function secretEnvKey(key: string): string {
  return key.replace(/[^A-Za-z0-9]+/g, "_").toUpperCase();
}

function graphjinEndpoint(url: string): string {
  const clean = url.replace(/\/+$/, "");
  return clean.endsWith("/api/v1/graphql") ? clean : `${clean}/api/v1/graphql`;
}

async function runMagentoAnalyticsSmoke(endpoint: string, orgId: string): Promise<void> {
  const headers = {
    authorization: `Bearer ${mintGraphjinToken({
      orgId,
      userId: "pack-health",
      role: "service",
      ttlSeconds: 60,
    })}`,
  };
  const result = await graphjinQuery<{ sales_order?: Array<{ entity_id?: string | number }> }>({
    baseUrl: endpoint,
    headers,
    query:
      'query MagentoPackAnalyticsSmoke { sales_order(where: { created_at: { gte: "1970-01-01 00:00:00" } }, limit: 1) { entity_id } }',
    signal: AbortSignal.timeout(30_000),
  });
  if (result.errors?.length || !Array.isArray(result.data?.sales_order)) {
    throw new Error(
      `Magento analytics smoke query failed: ${result.errors?.map((error) => error.message).join("; ") ?? "sales_order result unavailable"}`,
    );
  }
  const operational = await graphjinQuery<{
    sales_order?: Array<{
      customer_id?: string | number;
      customer_email?: string;
      customer_firstname?: string;
    }>;
  }>({
    baseUrl: endpoint,
    headers,
    query:
      'query MagentoPackOperationalDataCanary { sales_order(where: { created_at: { gte: "1970-01-01 00:00:00" } }, limit: 1) { customer_id customer_email customer_firstname } }',
    signal: AbortSignal.timeout(30_000),
  });
  if (operational.errors?.length || !Array.isArray(operational.data?.sales_order)) {
    throw new Error(
      `Magento operational data check failed: ${operational.errors?.map((error) => error.message).join("; ") ?? "sales_order result unavailable"}`,
    );
  }
  const secret = await graphjinQuery<{ sales_order?: Array<{ protect_code?: string }> }>({
    baseUrl: endpoint,
    headers,
    query:
      'query MagentoPackSecretColumnCanary { sales_order(where: { created_at: { gte: "1970-01-01 00:00:00" } }, limit: 1) { protect_code } }',
    signal: AbortSignal.timeout(30_000),
  });
  if (!secret.errors?.length) {
    throw new Error(
      "Magento secret-data check failed: GraphJin did not enforce the sales_order protect_code blocklist",
    );
  }
}

async function runPackReadPreflight(bundle: SolutionPackBundle, endpoint: string, orgId: string, inputs: Record<string, unknown>): Promise<void> {
  const results = new Map<string, unknown>();
  const exercised = new Set<string>();
  const now = new Date();
  const run = async (name: string, variables?: unknown): Promise<unknown> => {
    const resolved = resolveWatcherVariables(packVariables(variables, inputs), now);
    const key = canonicalHash({ name, resolved });
    if (results.has(key)) return results.get(key);
    const result = await graphjinQuery({
      baseUrl: graphjinEndpoint(endpoint), query: findSavedQuery(bundle, name), variables: resolved,
      headers: { authorization: `Bearer ${mintGraphjinToken({ orgId, userId: "pack-preflight", role: "service", ttlSeconds: 60 })}` },
      role: "service", signal: AbortSignal.timeout(30_000),
    });
    if (result.errors?.length || !result.data) throw new Error(`pack query ${name} failed preflight`);
    exercised.add(name);
    results.set(key, result.data);
    return result.data;
  };
  for (const artifact of bundle.artifacts) {
    if (artifact.kind !== "metric" && artifact.kind !== "watcher") continue;
    const value = artifactRecord(artifact);
    if (artifact.kind === "metric") {
      const execution = value.execution as Record<string, unknown>;
      const data = await run(String(execution.query), execution.variables);
      const mapping = execution.result as Record<string, unknown>;
      if (mapping.kind === "scalar" && typeof mapping.path === "string" && extractValueAtPath(data, mapping.path) == null) {
        throw new Error(`pack metric ${artifact.key} result path is missing or null`);
      }
      mapSavedQueryMetric({ definition: { ...value, execution: { ...execution, document: findSavedQuery(bundle, String(execution.query)) } }, data, baseline: null });
    } else {
      const data = await run(String(value.query), value.variables);
      if (extractValueAtPath(data, String(value.valuePath)) === undefined) throw new Error(`pack watcher ${artifact.key} result path is missing`);
    }
  }
  for (const artifact of bundle.artifacts.filter(value => value.kind === "saved_query")) {
    const name = basename(artifact.path, extname(artifact.path));
    if (!exercised.has(name)) await run(name);
  }
}

function packRoot(): string {
  return resolve(process.env.OPENNEKO_PACKS_DIR?.trim() || join(process.cwd(), "packs"));
}

function renderTemplate(value: string, inputs: Record<string, unknown>): string {
  return value.replace(/\{\{([^}]+)}}/g, (_match, key: string) => {
    const resolved = inputs[key.trim()];
    if (resolved === undefined || resolved === null) {
      throw new Error(`missing pack template input ${key.trim()}`);
    }
    return String(resolved);
  });
}

function findSavedQuery(bundle: SolutionPackBundle, name: string): string {
  const artifact = bundle.artifacts.find(
    (value) =>
      value.kind === "saved_query" && basename(value.path, extname(value.path)) === name,
  );
  if (!artifact || typeof artifact.content !== "string") {
    throw new Error(`pack saved query ${name} is missing`);
  }
  return artifact.content;
}

async function enqueuePackMetricRefreshes(
  orgId: string,
  bundle: SolutionPackBundle,
): Promise<number> {
  const { enqueue, QUEUE } = await import("@neko/db/jobs");
  let enqueued = 0;
  for (const artifact of bundle.artifacts.filter((value) => value.kind === "metric")) {
    const definition = artifactRecord(artifact);
    const [card] = await db().select({ id: metric.id }).from(metric).where(and(
      eq(metric.org_id, orgId),
      eq(metric.role, String(definition.role)),
      eq(metric.slug, artifact.targetRef),
    )).limit(1);
    if (!card) continue;
    const [job] = await db().insert(processing_job).values({
      org_id: orgId,
      kind: "metric_refresh",
      status: "queued",
      trigger: "pack_install",
      trigger_payload: { metricId: card.id },
    }).returning({ id: processing_job.id });
    if (!job) continue;
    await db().update(metric).set({
      last_refresh_status: "pending",
      last_refresh_error: null,
      last_refresh_job_id: job.id,
      updated_at: new Date(),
    }).where(eq(metric.id, card.id));
    try {
      await enqueue(QUEUE.METRIC_REFRESH, { processingJobId: job.id, orgId });
      enqueued++;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      await db().update(processing_job).set({
        status: "failed",
        error: message.slice(0, 1_000),
        finished_at: new Date(),
        updated_at: new Date(),
      }).where(eq(processing_job.id, job.id));
      await db().update(metric).set({
        last_refresh_status: "failed",
        last_refresh_error: message.slice(0, 500),
        updated_at: new Date(),
      }).where(eq(metric.id, card.id));
      console.warn(
        `[packs] could not schedule initial refresh for ${artifact.targetRef}: ${message}`,
      );
    }
  }
  return enqueued;
}

async function pathExists(path: string): Promise<boolean> {
  return lstat(path).then(() => true).catch(() => false);
}

async function writeAtomic(
  path: string,
  content: string,
  alreadyOwned: boolean,
): Promise<() => Promise<void>> {
  await mkdir(dirname(path), { recursive: true });
  const existed = await pathExists(path);
  if (existed && !alreadyOwned) {
    throw new Error(`pack file target ${path} already exists and is not owned by this pack`);
  }
  const previous = existed ? await readFile(path) : null;
  const temporary = `${path}.${randomUUID()}.pack-stage`;
  await writeFile(temporary, content, { mode: 0o644 });
  await rename(temporary, path);
  return async () => {
    if (previous) {
      const rollback = `${path}.${randomUUID()}.pack-rollback`;
      await writeFile(rollback, previous, { mode: 0o644 });
      await rename(rollback, path);
    } else {
      await rm(path, { force: true });
    }
  };
}

async function stageOwnedRemoval(path: string): Promise<{
  restore: () => Promise<void>;
  commit: () => Promise<void>;
}> {
  let target;
  try {
    target = await lstat(path);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") {
      return { restore: async () => {}, commit: async () => {} };
    }
    throw error;
  }
  if (target.isSymbolicLink()) throw new Error(`pack-owned removal target is a symlink: ${path}`);
  const backup = `${path}.${randomUUID()}.pack-remove-backup`;
  await rename(path, backup);
  return {
    restore: async () => {
      if (await pathExists(backup)) await rename(backup, path);
    },
    commit: async () => {
      await rm(backup, { recursive: target.isDirectory(), force: true });
    },
  };
}

function assertOwnedPath(root: string, target: string, label: string): string {
  const absoluteRoot = resolve(root);
  const absoluteTarget = resolve(target);
  const path = relative(absoluteRoot, absoluteTarget);
  if (!path || path === ".." || path.startsWith(`..${sep}`) || path.startsWith(sep)) {
    throw new Error(`${label} target escapes its managed root: ${target}`);
  }
  return absoluteTarget;
}

async function installGraphjinFiles(input: {
  bundle: SolutionPackBundle;
  configFile: string;
  values: Record<string, unknown>;
  ownedTargets: Set<string>;
}): Promise<{ restore: () => Promise<void>; targets: Map<string, string> }> {
  const configRoot = dirname(input.configFile);
  const restorers: Array<() => Promise<void>> = [];
  const targets = new Map<string, string>();
  try {
    for (const artifact of input.bundle.artifacts) {
      let destination: string | null = null;
      let content: string | null = null;
      if (artifact.kind === "spec") {
        destination = join(configRoot, "specs", basename(artifact.path));
        content = renderTemplate(await readFile(join(input.bundle.root, artifact.path), "utf8"), input.values);
      } else if (artifact.kind === "saved_query") {
        destination = join(
          configRoot,
          "queries",
          `${input.bundle.manifest.metadata.id.replaceAll("-", "_")}_${basename(artifact.path)}`,
        );
        content = String(artifact.content);
      }
      if (destination && content !== null) {
        restorers.push(
          await writeAtomic(destination, content, input.ownedTargets.has(destination)),
        );
        targets.set(`${artifact.kind}:${artifact.key}`, destination);
      }
    }
    return {
      targets,
      restore: async () => {
        for (const restore of restorers.reverse()) await restore();
      },
    };
  } catch (error) {
    for (const restore of restorers.reverse()) await restore().catch(() => {});
    throw error;
  }
}

async function assertNativeTargetsAvailable(input: {
  orgId: string;
  bundle: SolutionPackBundle;
  plan: PackPlan;
}): Promise<void> {
  const creates = new Set(
    input.plan.entries
      .filter((entry) => entry.action === "create")
      .map((entry) => `${entry.kind}:${entry.key}`),
  );
  for (const artifact of input.bundle.artifacts) {
    if (!creates.has(`${artifact.kind}:${artifact.key}`)) continue;
    const value = artifact.content && typeof artifact.content === "object" && !Array.isArray(artifact.content)
      ? artifact.content as Record<string, unknown>
      : null;
    let existing: { id: string } | undefined;
    if (artifact.kind === "metric" && value) {
      [existing] = await db().select({ id: metric.id }).from(metric).where(and(
        eq(metric.org_id, input.orgId),
        eq(metric.role, String(value.role)),
        eq(metric.slug, artifact.targetRef),
      )).limit(1);
    } else if (artifact.kind === "workflow" && value) {
      [existing] = await db().select({ id: workflow_definition.id }).from(workflow_definition).where(and(
        eq(workflow_definition.org_id, input.orgId),
        eq(workflow_definition.owner_user_id, ""),
        eq(workflow_definition.name, String(value.name)),
      )).limit(1);
    } else if (artifact.kind === "watcher" && value) {
      [existing] = await db().select({ id: watcher.id }).from(watcher).where(and(
        eq(watcher.org_id, input.orgId),
        eq(watcher.name, String(value.name)),
      )).limit(1);
    } else if (artifact.kind === "policy" && value) {
      [existing] = await db().select({ id: action_policy.id }).from(action_policy).where(and(
        eq(action_policy.org_id, input.orgId),
        eq(action_policy.name, String(value.name)),
      )).limit(1);
    } else if (artifact.kind === "action" && value) {
      [existing] = await db().select({ id: pack_action_definition.id }).from(pack_action_definition).where(and(
        eq(pack_action_definition.org_id, input.orgId),
        eq(pack_action_definition.kind, String(value.kind)),
      )).limit(1);
    }
    if (existing) {
      throw new Error(
        `${artifact.kind} target ${artifact.targetRef} already exists and is not owned by pack ${input.bundle.manifest.metadata.id}`,
      );
    }
  }
}

async function installSkills(input: {
  orgId: string;
  bundle: SolutionPackBundle;
  ownedTargets: Set<string>;
}): Promise<{
  targets: Map<string, string>;
  restore: () => Promise<void>;
  commit: () => Promise<void>;
}> {
  const workspace = await ensureOrgWorkspace(input.orgId);
  const restorers: Array<() => Promise<void>> = [];
  const committers: Array<() => Promise<void>> = [];
  const targets = new Map<string, string>();
  try {
    for (const artifact of input.bundle.artifacts.filter((value) => value.kind === "skill")) {
      const source = join(input.bundle.root, dirname(artifact.path));
      const target = join(workspace.skillsRoot, artifact.targetRef);
      const backup = `${target}.${randomUUID()}.pack-backup`;
      const stage = `${target}.${randomUUID()}.pack-stage`;
      const existed = await pathExists(target);
      if (existed && !input.ownedTargets.has(target)) {
        throw new Error(`skill target ${basename(target)} already exists and is not owned by this pack`);
      }
      await cp(source, stage, { recursive: true, force: false, errorOnExist: true });
      if (existed) await rename(target, backup);
      await rename(stage, target);
      targets.set(`${artifact.kind}:${artifact.key}`, target);
      restorers.push(async () => {
        await rm(target, { recursive: true, force: true });
        if (existed) await rename(backup, target);
      });
      committers.push(async () => {
        await rm(backup, { recursive: true, force: true });
      });
    }
    return {
      targets,
      restore: async () => {
        for (const restore of restorers.reverse()) await restore();
      },
      commit: async () => {
        for (const commit of committers) await commit();
      },
    };
  } catch (error) {
    for (const restore of restorers.reverse()) await restore().catch(() => {});
    throw error;
  }
}

export function resolveInputs(bundle: SolutionPackBundle, supplied: Record<string, unknown>): Record<string, unknown> {
  const declared = new Map(bundle.manifest.inputs.map((input) => [input.key, input]));
  for (const key of Object.keys(supplied)) {
    if (!declared.has(key)) throw new Error(`unknown pack input ${key}`);
  }
  const resolved: Record<string, unknown> = {};
  for (const input of bundle.manifest.inputs) {
    const value = Object.hasOwn(supplied, input.key) ? supplied[input.key] : input.default;
    if (value === undefined && input.required) throw new Error(`required pack input ${input.key} is missing`);
    if (value === undefined) continue;
    switch (input.type) {
      case "string":
      case "timezone":
      case "url": {
        if (typeof value !== "string" || (input.type !== "string" && !value.trim())) {
          throw new Error(`pack input ${input.key} must be ${input.type === "string" ? "a string" : `a non-empty ${input.type}`}`);
        }
        const normalized = value.trim();
        if (input.required && !normalized) {
          throw new Error(`required pack input ${input.key} must not be empty`);
        }
        if (input.type === "url") {
          let parsed: URL;
          try {
            parsed = new URL(normalized);
          } catch {
            throw new Error(`pack input ${input.key} must be an absolute URL`);
          }
          if (!['http:', 'https:'].includes(parsed.protocol)) {
            throw new Error(`pack input ${input.key} must use HTTP or HTTPS`);
          }
          resolved[input.key] = normalized.replace(/\/+$/, "");
        } else if (input.type === "timezone") {
          try {
            new Intl.DateTimeFormat("en-US", { timeZone: normalized }).format();
          } catch {
            throw new Error(`pack input ${input.key} must be a valid IANA timezone`);
          }
          resolved[input.key] = normalized;
        } else {
          resolved[input.key] = normalized;
        }
        break;
      }
      case "integer": {
        const number = typeof value === "number" ? value : Number(value);
        if (!Number.isInteger(number)) throw new Error(`pack input ${input.key} must be an integer`);
        resolved[input.key] = number;
        break;
      }
      case "boolean":
        if (typeof value !== "boolean") throw new Error(`pack input ${input.key} must be a boolean`);
        resolved[input.key] = value;
        break;
      case "enum":
        if (!input.values?.some((candidate) => candidate === value)) {
          throw new Error(`pack input ${input.key} must be one of: ${input.values?.join(", ")}`);
        }
        resolved[input.key] = value;
        break;
    }
  }
  const port = resolved["database.port"];
  if (port !== undefined && (typeof port !== "number" || port < 1 || port > 65535)) {
    throw new Error("database.port must be an integer from 1 to 65535");
  }
  return resolved;
}

async function resolveSecrets(
  bundle: SolutionPackBundle,
  request: PackInstallRequest,
): Promise<{
  values: Record<string, string>;
  cleared: Set<string>;
  store: Awaited<ReturnType<typeof readSecretsStore>>;
}> {
  const store = await readSecretsStore();
  const section = `${PACK_SECRET_PREFIX}${bundle.manifest.metadata.id}`;
  const current = store[section] ?? {};
  const declared = new Set(bundle.manifest.secrets.map((secret) => secret.key));
  for (const key of [...Object.keys(request.secrets ?? {}), ...Object.keys(request.secretRefs ?? {})]) {
    if (!declared.has(key)) throw new Error(`unknown pack secret ${key}`);
  }
  const values: Record<string, string> = {};
  const cleared = new Set<string>();
  for (const secret of bundle.manifest.secrets) {
    const direct = request.secrets?.[secret.key];
    const ref = request.secretRefs?.[secret.key];
    if (direct !== undefined && typeof direct !== "string") {
      throw new Error(`pack secret ${secret.key} must be a string`);
    }
    if (direct !== undefined && !direct.trim() && !secret.required) {
      cleared.add(secret.key);
      continue;
    }
    const stored = ref ? current[ref] : current[secretEnvKey(secret.key)];
    const value = direct !== undefined ? direct : stored;
    if (secret.required && (!value || !value.trim())) {
      throw new Error(`required pack secret ${secret.key} is missing`);
    }
    if (value) values[secret.key] = value;
  }
  return { values, cleared, store };
}

function graphjinRelationships(bundle: SolutionPackBundle, available: string[]): Record<string, unknown>[] {
  const artifact = bundle.artifacts.find((value) => value.kind === "relationships");
  const relationships = artifactRecord(artifact!).relationships as Array<Record<string, unknown>>;
  const tables = new Set(available);
  return relationships
    .filter((relationship) =>
      tables.has(String(relationship.left).split(".")[0]) &&
      tables.has(String(relationship.right).split(".")[0]),
    )
    .map((relationship) => ({
      from: `magento_analytics:${String(relationship.left)}`,
      to: `magento_analytics:${String(relationship.right)}`,
    }));
}

function magentoCapsFromInputs(inputs: Record<string, unknown>) {
  return {
    ...DEFAULT_MAGENTO_CAPS,
    maxRowsPerChangeset: Number(inputs["magento.max_rows_per_changeset"]),
    maxPriceDeltaPercent: Number(inputs["magento.max_price_delta_percent"]),
    maxDiscountPercent: Number(inputs["magento.max_discount_percent"]),
    maxCouponCount: Number(inputs["magento.max_coupon_count"]),
    maxProjectedExposure: Number(inputs["magento.max_projected_exposure"]),
    maxDailyAutoActions: Number(inputs["magento.max_daily_auto_actions"]),
    skuCooldownSeconds: Number(inputs["magento.sku_cooldown_seconds"]),
  };
}

function magentoV2OperationExposure(
  bundle: SolutionPackBundle,
): Record<string, unknown> {
  const operations: Record<string, unknown> = {};
  for (const artifact of bundle.artifacts.filter((value) => value.kind === "action")) {
    const content = artifactRecord(artifact);
    const adapter = content.adapter;
    if (!adapter || typeof adapter !== "object" || Array.isArray(adapter)) continue;
    const definition = adapter as Record<string, unknown>;
    if (
      definition.kind !== "magento_changeset" &&
      definition.kind !== "magento_governed_operation"
    ) continue;
    const declared = definition.operations;
    if (!declared || typeof declared !== "object" || Array.isArray(declared)) continue;
    for (const operation of Object.values(declared as Record<string, unknown>)) {
      if (!operation || typeof operation !== "object" || Array.isArray(operation)) continue;
      const value = operation as Record<string, unknown>;
      const operationId = String(value.operationId ?? "");
      const mutationRoot = String(value.mutationRoot ?? "");
      const exposeAs = mutationRoot.replace(/^magento_operator_v2_/, "");
      if (!operationId || !mutationRoot || exposeAs === mutationRoot) {
        throw new Error(`Magento operation ${operationId || "<unknown>"} has an invalid V2 mutation root`);
      }
      const next = {
        expose_mutation: true,
        allowed_roles: Number(value.defaultClass) === 2
          ? ["magento_ops_executor", "magento_sensitive_executor"]
          : ["magento_sensitive_executor"],
        expose_as: exposeAs,
      };
      const current = operations[operationId];
      if (current && canonicalHash(current) !== canonicalHash(next)) {
        throw new Error(`Magento operation ${operationId} has conflicting V2 exposure`);
      }
      operations[operationId] = next;
    }
  }
  return operations;
}

function graphjinUpdate(
  inputs: Record<string, unknown>,
  secrets: Record<string, string>,
  preflight: MagentoPreflightResult,
  bundle: SolutionPackBundle,
  retiredSourceNames: string[] = [],
): Record<string, unknown> {
  const integrationToken = secrets["magento.integration_token"];
  const writeEnabled = Boolean(integrationToken);
  const auth = integrationToken
    ? {
        auth: {
          scheme: "bearer",
          token: integrationToken,
        },
      }
    : {};
  return {
    roles: [
      {
        name: "magento_ops_executor",
        comment: "Short-lived Magento executor for approved automations",
      },
      {
        name: "magento_sensitive_executor",
        comment: "Short-lived Magento executor minted after administrator approval",
      },
    ],
    update_sources: [
      {
        name: "magento_analytics",
        kind: "database",
        default: false,
        type: preflight.databaseType,
        host: String(inputs["database.host"]),
        port: Number(inputs["database.port"]),
        dbname: String(inputs["database.name"]),
        user: secrets["database.analytics_username"],
        password: secrets["database.analytics_password"],
        read_only: true,
        analytics_mode: true,
        capabilities: {
          "data.read": true,
          "data.write": false,
          "schema.read": true,
          "schema.write": false,
        },
        access: {
          read: "authenticated",
          write: "blocked",
          delete: "blocked",
          blocked_tables: preflight.blockedTables,
        },
      },
      {
        name: "magento_operator",
        kind: "api",
        default: false,
        specs_dir: "/config/specs",
        specs: {
          "magento-operator-v1": {
            base_url: String(inputs["magento.base_url"]),
            ...auth,
            operations: {
              magentoAddInternalOrderComment: {
                expose_mutation: false,
                allowed_roles: [],
              },
            },
          },
          "magento-operator-v2": {
            base_url: String(inputs["magento.base_url"]),
            ...auth,
            operations: magentoV2OperationExposure(bundle),
          },
        },
        read_only: !writeEnabled,
        capabilities: {
          "api.read": true,
          "api.write": writeEnabled,
          "api.delete": writeEnabled,
        },
        access: {
          read: "authenticated",
          write: writeEnabled ? "authenticated" : "blocked",
          delete: writeEnabled ? "authenticated" : "blocked",
        },
      },
    ],
    ...(retiredSourceNames.length > 0
      ? {
          source_patches: retiredSourceNames.map((name) => ({
            name,
            read_only: true,
            access: { read: "blocked", write: "blocked", delete: "blocked" },
          })),
        }
      : {}),
    tables: magentoGraphjinTables(preflight.tablePrefix, preflight.availableAnalyticsTables),
    relationships: graphjinRelationships(bundle, preflight.availableAnalyticsTables),
  };
}

export class PackService {
  constructor(
    private readonly orgId: string,
    private readonly embeddedRoot = packRoot(),
    private readonly uploadedRoot?: string,
  ) {}

  private uploadsRoot(): string { return this.uploadedRoot ?? join(dirname(localConfigPath()), "agents", "orgs", encodeURIComponent(this.orgId).replaceAll(".", "%2E"), "packs"); }

  private async bundle(packId: string, version?: string): Promise<AvailablePack> {
    if (!PACK_ID.test(packId)) throw new Error("invalid pack id");
    const bundle: AvailablePack = await pathExists(join(this.embeddedRoot, packId, "pack.yaml"))
      ? await loadSolutionPack(join(this.embeddedRoot, packId))
      : await loadUploadedPack(this.uploadsRoot(), this.orgId, packId, version);
    if (packId !== "magento") {
      for (const artifact of bundle.artifacts) {
        const value = artifact.kind === "source" ? artifactRecord(artifact) : {};
        if (artifact.kind === "source" && value.kind === "database" && !value.host) {
          artifact.targetRef = `${packId}.binding.${String(value.name)}`;
        }
      }
    }
    return bundle;
  }

  private async installedBundle(installation: typeof pack_install.$inferSelect): Promise<AvailablePack> {
    const bundle = await this.bundle(installation.pack_id, installation.source === "uploaded" ? installation.version : undefined);
    if (installation.source === "uploaded") {
      const pinned = installation.config._bundle as { contentHash?: string } | undefined;
      if (!bundle.upload || bundle.upload.contentHash !== pinned?.contentHash) throw new Error("installed pack contents do not match the approved version");
    }
    return bundle;
  }

  private async withConnector<T>(packId: string, connectorId: string, fn: (ctx: Omit<PackConnectionContext, "owner">) => Promise<T>) {
    const client = await pool().connect();
    try {
      await client.query("SELECT pg_advisory_lock(hashtextextended($1, 0))", [`pack:${this.orgId}:${packId}`]);
      const [installation] = await db().select().from(pack_install).where(and(
        eq(pack_install.org_id, this.orgId), eq(pack_install.pack_id, packId), eq(pack_install.status, "installed"),
      )).limit(1);
      if (!installation) throw new Error("Pack is not installed");
      const bundle = await this.installedBundle(installation);
      if (storedRuntime(installation.config)?.connectorBundleHash !== bundle.bundleHash) throw new Error("Pack connector contents changed; review and upgrade the pack");
      const connector = bundle.manifest.connectors?.find(value => value.id === connectorId);
      if (!connector) throw new Error("Pack connector is not declared");
      return await fn({ sql: client, installationId: installation.id, connector });
    } finally {
      await client.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [`pack:${this.orgId}:${packId}`]).catch(() => {});
      client.release();
    }
  }

  async accountProviders(owner: string) {
    const installations = await db().select().from(pack_install).where(and(eq(pack_install.org_id, this.orgId), eq(pack_install.status, "installed")));
    const providers = [];
    for (const installation of installations) {
      const bundle = await this.installedBundle(installation);
      for (const connector of bundle.manifest.connectors ?? []) {
        if (connector.auth) providers.push({ packId: installation.pack_id, connectorId: connector.id, name: connector.auth.label, scopes: connector.auth.scopes, ...await this.connectAccount(installation.pack_id, connector.id, owner, "list", {}) });
      }
    }
    return providers;
  }

  async connectAccount(packId: string, connectorId: string, owner: string, action: string, input: Record<string, unknown>) {
    if (!["configure", "list", "start", "callback", "disconnect"].includes(action)) throw new Error("Unknown pack connection action");
    return this.withConnector(packId, connectorId, ctx => packConnection({ ...ctx, owner }, action, input));
  }

  async runConnector(packId: string, connectorId: string, operation: string, input: Record<string, unknown>, binding?: { ownerId: string; accountId: string }) {
    return this.withConnector(packId, connectorId, async ctx => {
      if (ctx.connector.auth && (!binding?.ownerId || !binding.accountId)) throw new Error("Select a pack account owned by the execution user");
      const credential = ctx.connector.auth ? await packConnection({ ...ctx, owner: binding!.ownerId }, "credential", { accountId: binding!.accountId }) : undefined;
      return runPackConnector(ctx.connector, { operation, input, ...(credential ? { credential } : {}) });
    });
  }

  async upload(bytes: Buffer, request: { actorUserId?: string | null; signal?: AbortSignal } = {}) {
    const embedded = await readdir(this.embeddedRoot, { withFileTypes: true }).catch((error: NodeJS.ErrnoException) => { if (error.code === "ENOENT") return []; throw error; });
    const reservedIds = ["magento", ...embedded.filter(entry => entry.isDirectory() && PACK_ID.test(entry.name)).map(entry => entry.name)];
    return storePackUpload({ root: this.uploadsRoot(), orgId: this.orgId, bytes, reservedIds, actorUserId: request.actorUserId, signal: request.signal });
  }

  private reviewHash(bundle: AvailablePack, plan: PackPlan, operation: string, request: PackInstallRequest, inputs: Record<string, unknown>, secrets: Record<string, string>, runtime: PackRuntime): string {
    return createHmac("sha256", graphjinSigningSecret(this.orgId)).update("pack-review/v1:").update(canonicalHash({
      orgId: this.orgId, actor: request.actorUserId ?? null, operation, plan,
      contentHash: bundle.upload?.contentHash ?? bundle.bundleHash, inputs, secrets, runtime,
      secretRefs: request.secretRefs ?? {},
    })).digest("hex");
  }

  async review(packId: string, request: PackInstallRequest = {}, operation: "install" | "configure" | "upgrade" = "install") {
    const [existing] = await db().select().from(pack_install).where(and(eq(pack_install.org_id, this.orgId), eq(pack_install.pack_id, packId))).orderBy(desc(pack_install.created_at)).limit(1);
    if (operation !== "install" && existing?.status !== "installed") throw new Error("pack must be installed before reviewing this operation");
    if (operation === "configure" && request.version && request.version !== existing?.version) throw new Error("configure cannot change the installed pack version");
    const bundle = operation === "configure" && existing ? await this.installedBundle(existing) : await this.bundle(packId, request.version);
    const prior = Object.fromEntries(Object.entries(existing?.config ?? {}).filter(([key]) => bundle.manifest.inputs.some(input => input.key === key)));
    const inputs = resolveInputs(bundle, { ...prior, ...request.inputs });
    const secrets = await resolveSecrets(bundle, request);
    const runtime = await this.runtime(bundle, request, storedRuntime(existing?.config ?? {}));
    if (packId !== "magento") {
      const bound = bindPackQueries(bundle, runtime.bindings);
      runtime.tables = [...new Map([...(storedRuntime(existing?.config ?? {})?.tables ?? []), ...bound.tables].map(table => [table.name, table])).values()];
      declarativeGraphjinUpdate(bound.bundle, inputs, secrets.values, [], runtime.bindings);
    }
    for (const connector of bundle.manifest.connectors ?? []) await runPackConnector(connector);
    const plan = await this.planBundle(bundle);
    await assertNativeTargetsAvailable({ orgId: this.orgId, bundle, plan });
    return { ...await this.inspect(packId, bundle.manifest.metadata.version), operation, inputs, runtime, plan,
      secrets: Object.keys(secrets.values), reviewHash: this.reviewHash(bundle, plan, operation, request, inputs, secrets.values, runtime) };
  }

  private async runtime(bundle: SolutionPackBundle, request: PackInstallRequest, prior?: PackRuntime): Promise<PackRuntime> {
    const connectorIdentity = bundle.manifest.connectors?.length ? { connectorBundleHash: bundle.bundleHash } : {};
    if (!bundle.manifest.artifacts.graphjin) {
      if (request.dataSourceId || Object.keys(request.sourceBindings ?? {}).length) throw new Error("this pack does not use a data source");
      if (prior?.source) throw new Error("removing GraphJin artifacts requires uninstall first");
      return { ...connectorIdentity, bindings: {}, bindingHashes: {}, readiness: Object.keys(bundle.manifest.health.readiness) };
    }
    await verifyPackQueryTables(prior?.tables);
    let source: PackSourceSelection;
    if (!request.dataSourceId && prior?.source) source = await resolvePackSource(this.orgId, prior.source);
    else {
      if (!request.dataSourceId && bundle.manifest.metadata.id !== "magento") throw new Error("select an enabled organization dataSourceId before installing a custom pack");
      const [selected] = await db().select({ id: data_source.id, graphqlUrl: data_source.graphql_url, authMode: data_source.auth_mode }).from(data_source)
        .where(and(eq(data_source.org_id, this.orgId), eq(data_source.enabled, true), ...(request.dataSourceId ? [eq(data_source.id, request.dataSourceId)] : [])))
        .orderBy(desc(data_source.is_default), data_source.created_at).limit(1);
      if (!selected) throw new Error("selected pack data source is unavailable in this organization");
      source = selected;
    }
    const bindingHashes: Record<string, string> = {};
    const references = bundle.artifacts.filter(artifact => artifact.kind === "source" && artifact.targetRef.startsWith(`${bundle.manifest.metadata.id}.binding.`));
    const bindings = request.sourceBindings ?? Object.fromEntries(Object.entries(prior?.bindings ?? {}).filter(([key]) => references.some(artifact => artifact.key === key)));
    if (Object.keys(bindings).some(key => !references.some(artifact => artifact.key === key))) throw new Error("unknown pack source binding");
    if (references.length) {
      const configFile = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim();
      if (!configFile) throw new Error("GraphJin configuration is unavailable");
      const config = parseYaml(await readFile(configFile, "utf8")) as { sources?: Array<Record<string, unknown>> };
      const live = await graphjinQuery<{ gj_config?: { sources?: Array<Record<string, unknown>> } }>({
        baseUrl: graphjinEndpoint(source.graphqlUrl), headers: { authorization: `Bearer ${mintGraphjinToken({ orgId: this.orgId, userId: "pack-binding", role: "admin", ttlSeconds: 60 })}` },
        query: "query { gj_config(id: \"current\") { sources } }", signal: AbortSignal.timeout(30_000),
      });
      if (live.errors?.length || !Array.isArray(live.data?.gj_config?.sources)) throw new Error("cannot verify selected GraphJin sources");
      for (const artifact of references) {
        const name = bindings[artifact.key];
        const current = config.sources?.find(value => value.name === name);
        const running = live.data!.gj_config!.sources!.find(value => value.name === name);
        if (!name || !current || !running || current.kind !== "database" || current.read_only !== true || running.kind !== "database" || running.read_only !== true) throw new Error(`binding ${artifact.key} requires an existing read-only database source`);
        for (const key of ["name", "kind", "type", "host", "port", "dbname"]) {
          if (String(current[key] ?? "") !== String(running[key] ?? "")) throw new Error(`binding ${artifact.key} differs from the selected live source`);
        }
        const engines = bundle.manifest.compatibility.databases.map(value => value.engine);
        if (engines.length && !engines.includes(current.type as "postgres")) throw new Error(`binding ${artifact.key} database engine is incompatible`);
        bindingHashes[artifact.key] = graphjinConfigPatchHash(current);
        if (!request.sourceBindings && prior?.bindingHashes[artifact.key] && prior.bindingHashes[artifact.key] !== bindingHashes[artifact.key]) throw new Error(`binding ${artifact.key} changed; explicitly reconfigure it`);
      }
    }
    return { ...connectorIdentity, source, bindings, bindingHashes, readiness: Object.keys(bundle.manifest.health.readiness) };
  }

  private async replayIdempotentOperation(
    packId: string,
    idempotencyKey: string | undefined,
  ): Promise<PackStatus | null> {
    if (!idempotencyKey) return null;
    const [prior] = await db()
      .select({
        packId: pack_install.pack_id,
        status: pack_operation.status,
        error: pack_operation.error,
      })
      .from(pack_operation)
      .innerJoin(pack_install, eq(pack_install.id, pack_operation.pack_install_id))
      .where(
        and(
          eq(pack_operation.org_id, this.orgId),
          eq(pack_operation.idempotency_key, idempotencyKey),
        ),
      )
      .limit(1);
    if (!prior) return null;
    if (prior.packId !== packId) {
      throw new Error(`idempotency key was already used for pack ${prior.packId}`);
    }
    if (prior.status === "succeeded") {
      const status = await this.status(packId);
      if (!status) throw new Error(`pack ${packId} status is missing for completed operation`);
      return status;
    }
    if (prior.status === "failed") {
      throw new Error(prior.error || `the previous ${packId} operation failed`);
    }
    throw new Error(`the ${packId} operation for this idempotency key is still ${prior.status}`);
  }

  async list(): Promise<Array<{ id: string; name: string; version: string; installed: boolean }>> {
    const entries = await readdir(this.embeddedRoot, { withFileTypes: true }).catch((error: NodeJS.ErrnoException) => { if (error.code === "ENOENT") return []; throw error; });
    const installed = await db()
      .select({ packId: pack_install.pack_id })
      .from(pack_install)
      .where(and(eq(pack_install.org_id, this.orgId), eq(pack_install.status, "installed")));
    const installedIds = new Set(installed.map((row) => row.packId));
    const packDirectories: string[] = [];
    for (const entry of entries) {
      if (!entry.isDirectory() || !PACK_ID.test(entry.name)) continue;
      try {
        const manifest = await lstat(join(this.embeddedRoot, entry.name, "pack.yaml"));
        if (manifest.isFile() || manifest.isSymbolicLink()) packDirectories.push(entry.name);
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      }
    }
    const bundles = [...await Promise.all(packDirectories.map((packId) => this.bundle(packId))), ...await listUploadedPacks(this.uploadsRoot(), this.orgId)];
    return bundles.map((bundle) => ({
      id: bundle.manifest.metadata.id,
      name: bundle.manifest.metadata.name,
      version: bundle.manifest.metadata.version,
      installed: installedIds.has(bundle.manifest.metadata.id),
    }));
  }

  async inspect(packId: string, version?: string): Promise<Record<string, unknown>> {
    const bundle = await this.bundle(packId, version);
    return {
      source: bundle.upload ? "uploaded" : "embedded",
      ...(bundle.upload ? { upload: bundle.upload } : {}),
      manifest: bundle.manifest,
      manifestHash: bundle.manifestHash,
      bundleHash: bundle.bundleHash,
      bindingRequirements: bundle.artifacts.filter(artifact => artifact.kind === "source" && artifactRecord(artifact).kind === "database" && !artifactRecord(artifact).host).map(artifact => ({ key: artifact.key, name: String(artifactRecord(artifact).name) })),
      connectors: bundle.manifest.connectors ?? [],
      permissions: { ...(packId !== "magento" ? { database: bundle.manifest.artifacts.graphjin ? "read-only" : "none", apiWrite: "blocked" } : {
        database: "view-only reporting",
        apiWrite: "specific Magento changes require approval and must be enabled individually",
        customerPii: "available to authenticated read-only queries",
        paymentData: "operational fields available; credentials and raw gateway payloads blocked",
      }), ...(bundle.manifest.connectors?.length ? { connector_access: bundle.manifest.connectors.map(connector => `${connector.id}: ${connector.operations.map(operation => `${operation.id} (${operation.effect})`).join(", ")}; network: ${connector.network.map(rule => `${rule.host}:${rule.port}`).join(", ") || "none"}`).join("; ") } : {}) },
    };
  }

  async plan(packId: string, version?: string): Promise<PackPlan> {
    return this.planBundle(await this.bundle(packId, version));
  }

  private async planBundle(bundle: SolutionPackBundle): Promise<PackPlan> {
    const packId = bundle.manifest.metadata.id;
    const [installation] = await db()
      .select({ id: pack_install.id })
      .from(pack_install)
      .where(
        and(
          eq(pack_install.org_id, this.orgId),
          eq(pack_install.pack_id, packId),
        ),
      )
      .orderBy(desc(pack_install.created_at))
      .limit(1);
    const artifacts = installation
      ? await db().select().from(pack_artifact).where(eq(pack_artifact.pack_install_id, installation.id))
      : [];
    const desiredByIdentity = new Map(
      bundle.artifacts.map((artifact) => [`${artifact.kind}:${artifact.key}`, artifact]),
    );
    const configFile = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim() ?? "";
    const installed = await Promise.all(artifacts.map(async (artifact) => {
      const desired = desiredByIdentity.get(`${artifact.artifact_kind}:${artifact.artifact_key}`);
      const metadata = artifact.metadata ?? {};
      const needsGraphjinConfig = ["source", "relationships", "spec", "saved_query"].includes(
        artifact.artifact_kind,
      );
      const inspected = configFile || !needsGraphjinConfig
        ? await inspectInstalledPackArtifactCurrent({
            orgId: this.orgId,
            kind: artifact.artifact_kind as PackArtifact["kind"],
            targetRef: artifact.target_ref,
            metadata,
            graphjinConfigFile: configFile,
            ...(desired ? { fallbackArtifact: desired } : {}),
          })
        : null;
      const baseline = metadata.stateHashVersion === 1
        ? artifact.last_applied_hash
        : inspected ?? artifact.last_applied_hash;
      return {
        kind: artifact.artifact_kind as PackArtifact["kind"],
        key: artifact.artifact_key,
        targetRef: artifact.target_ref,
        desiredHash: artifact.desired_hash,
        lastAppliedHash: baseline,
        currentHash: inspected,
        ownership: artifact.ownership as "managed" | "modified" | "detached" | "retired",
      };
    }));
    return planPack(bundle, installed);
  }

  private async persistLegacyStateBaselines(
    installationId: string,
    plan: PackPlan,
    bundle: SolutionPackBundle,
  ): Promise<void> {
    const rows = await db().select().from(pack_artifact)
      .where(eq(pack_artifact.pack_install_id, installationId));
    const entries = new Map(plan.entries.map((entry) => [`${entry.kind}:${entry.key}`, entry]));
    const desired = new Map(bundle.artifacts.map((artifact) => [`${artifact.kind}:${artifact.key}`, artifact]));
    await db().transaction(async (tx) => {
      for (const row of rows) {
        if (row.metadata?.stateHashVersion === 1) continue;
        const entry = entries.get(`${row.artifact_kind}:${row.artifact_key}`);
        if (entry?.action !== "noop" || !entry.currentHash) continue;
        await tx.update(pack_artifact).set({
          last_applied_hash: entry.currentHash,
          metadata: {
            ...(row.metadata ?? {}),
            ...(desired.get(`${row.artifact_kind}:${row.artifact_key}`)
              ? { locator: packArtifactLocator(desired.get(`${row.artifact_kind}:${row.artifact_key}`)!) }
              : {}),
            stateHashVersion: 1,
          },
          updated_at: new Date(),
        }).where(eq(pack_artifact.id, row.id));
      }
    });
  }

  async status(packId: string): Promise<PackStatus | null> {
    const [installation] = await db()
      .select()
      .from(pack_install)
      .where(and(eq(pack_install.org_id, this.orgId), eq(pack_install.pack_id, packId)))
      .orderBy(desc(pack_install.created_at))
      .limit(1);
    if (!installation) return null;
    const artifacts = await db()
      .select({ readiness: pack_artifact.readiness, reason: pack_artifact.readiness_reason })
      .from(pack_artifact)
      .where(eq(pack_artifact.pack_install_id, installation.id));
    const readiness: PackStatus["readiness"] = {};
    for (const artifact of artifacts) {
      const operatorMatch = /^operator:([^:]+):(.*)$/.exec(artifact.reason ?? "");
      const capability = operatorMatch?.[1] ?? (artifact.reason?.startsWith("operator:") ? "operator" : "analytics");
      if (!readiness[capability] || artifact.readiness === "blocked") {
        readiness[capability] = {
          status: artifact.readiness,
          reason: operatorMatch?.[2] ?? artifact.reason?.replace(/^operator:/, "") ?? null,
        };
      }
    }
    for (const capability of storedRuntime(installation.config)?.readiness ?? []) {
      readiness[capability] ??= { status: installation.status === "installed" ? "ready" : "blocked", reason: installation.status === "installed" ? null : "pack_not_active" };
    }
    return {
      packId,
      version: installation.version,
      status: installation.status,
      readiness,
      installedAt: installation.installed_at?.toISOString() ?? null,
      lastError: installation.last_error,
      configuration: {
        inputs: Object.fromEntries(Object.entries(installation.config).filter(([key]) => !key.startsWith("_"))),
        dataSourceId: storedRuntime(installation.config)?.source?.id,
        sourceBindings: storedRuntime(installation.config)?.bindings ?? {},
      },
    };
  }

  async doctor(packId: string): Promise<PackDoctorResult> {
    const [installation] = await db()
      .select()
      .from(pack_install)
      .where(and(eq(pack_install.org_id, this.orgId), eq(pack_install.pack_id, packId)))
      .orderBy(desc(pack_install.created_at))
      .limit(1);
    if (!installation || installation.status === "removed") {
      return {
        packId,
        status: "blocked",
        checks: [{ id: "installation", status: "blocked", detail: "pack is not installed" }],
      };
    }

    const bundle = await this.installedBundle(installation);
    if (packId !== "magento") {
      try {
        const inputs = resolveInputs(bundle, Object.fromEntries(Object.entries(installation.config).filter(([key]) => bundle.manifest.inputs.some(input => input.key === key))));
        const secrets = await resolveSecrets(bundle, {});
        const runtime = await this.runtime(bundle, {}, storedRuntime(installation.config));
        declarativeGraphjinUpdate(bundle, inputs, secrets.values, [], runtime.bindings);
        const source = runtime.source;
        if (source) await runPackReadPreflight(bindPackQueries(bundle, runtime.bindings).bundle, source.graphqlUrl, this.orgId, inputs);
        return { packId, status: "ready", checks: [{ id: source ? "queries" : "configuration", status: "ready", detail: source ? "Pack queries and response mappings passed." : "Pack configuration passed. No data connection is required." }] };
      } catch {
        return { packId, status: "blocked", checks: [{ id: "queries", status: "blocked", detail: "Pack configuration or query preflight failed." }] };
      }
    }

    const checks: PackDoctorResult["checks"] = [];
    const declaredInputs = new Set(bundle.manifest.inputs.map((input) => input.key));
    const storedInputs = Object.fromEntries(
      Object.entries(installation.config as Record<string, unknown>)
        .filter(([key]) => declaredInputs.has(key)),
    );
    let preflight: MagentoPreflightResult | null = null;
    try {
      const inputs = resolveInputs(bundle, storedInputs);
      const resolvedSecrets = await resolveSecrets(bundle, {});
      preflight = await runMagentoPreflight({
        host: String(inputs["database.host"]),
        port: Number(inputs["database.port"]),
        database: String(inputs["database.name"]),
        username: resolvedSecrets.values["database.analytics_username"]!,
        password: resolvedSecrets.values["database.analytics_password"]!,
        tablePrefix: String(inputs["magento.table_prefix"] ?? ""),
        baseUrl: String(inputs["magento.base_url"]),
        storeCode: String(inputs["magento.store_code"] ?? "all"),
        integrationToken: resolvedSecrets.values["magento.integration_token"] ?? null,
        customersEnabled: Boolean(inputs["magento.customers_enabled"]),
      });
      checks.push({
        id: "analytics",
        status: "ready",
        detail: `${preflight.databaseType} ${preflight.databaseVersion}; SELECT-only grants verified`,
      });
      checks.push({
        id: "magento",
        status: "ready",
        detail: `${preflight.magentoVersion}; store IDs ${preflight.storeIds.join(", ")}`,
      });
    } catch (error) {
      checks.push({
        id: "analytics",
        status: "blocked",
        detail: error instanceof Error ? error.message : String(error),
      });
    }

    const selection = storedRuntime(installation.config)?.source;
    const bound = selection ? await resolvePackSource(this.orgId, selection) : null;
    const [fallback] = bound || !bundle.manifest.artifacts.graphjin ? [] : await db()
      .select({ graphqlUrl: data_source.graphql_url })
      .from(data_source)
      .where(and(eq(data_source.org_id, this.orgId), eq(data_source.enabled, true)))
      .orderBy(desc(data_source.is_default), data_source.created_at)
      .limit(1);
    const source = bound ?? fallback;
    const configFile = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim();
    if (!source?.graphqlUrl || !configFile || !(await pathExists(configFile))) {
      checks.push({
        id: "graphjin",
        status: "blocked",
        detail: "customer GraphJin endpoint/config volume is unavailable",
      });
    } else {
      try {
        const result = await graphjinQuery<{ gj_config?: { catalog_revision?: string } }>({
          baseUrl: graphjinEndpoint(source.graphqlUrl),
          headers: {
            authorization: `Bearer ${mintGraphjinToken({
              orgId: this.orgId,
              userId: "pack-doctor",
              role: "admin",
              ttlSeconds: 60,
            })}`,
          },
          query: 'query { gj_config(id: "current") { catalog_revision } }',
        });
        if (result.errors?.length || !result.data?.gj_config?.catalog_revision) {
          throw new Error(result.errors?.map((value) => value.message).join("; ") || "catalog revision unavailable");
        }
        await runMagentoAnalyticsSmoke(graphjinEndpoint(source.graphqlUrl), this.orgId);
        checks.push({
          id: "graphjin",
          status: "ready",
          detail: `catalog ${result.data.gj_config.catalog_revision}`,
        });
        checks.push({
          id: "analytics-query",
          status: "ready",
          detail: "Magento sales_order smoke query succeeded",
        });
      } catch (error) {
        checks.push({
          id: "graphjin",
          status: "blocked",
          detail: error instanceof Error ? error.message : String(error),
        });
      }
    }

    if (preflight) {
      for (const domain of ["catalog", "inventory", "orders", "promotions", "content", "customers"] as const) {
        const reason = preflight.operatorDomains[domain];
        checks.push({
          id: `changes-${domain}`,
          status: reason === "ready" ? "ready" : reason === "domain_disabled" ? "optional" : "blocked",
          detail: operatorReadinessDetail(reason),
        });
      }
      checks.push({
        id: "bulk-consumers",
        status: preflight.bulkConsumerReadiness === "ready" ? "ready" : "blocked",
        detail: preflight.bulkConsumerReadiness === "ready"
          ? "Magento async bulk consumers completed recently."
          : "Magento async bulk consumers are not reporting recent successful work.",
      });
    } else {
      checks.push({
        id: "changes",
        status: "optional",
        detail: operatorReadinessDetail(null),
      });
    }
    const requiredBlocked = checks.some(
      (check) => !check.id.startsWith("changes") && check.id !== "bulk-consumers" && check.status === "blocked",
    );
    const operatorBlocked = checks.some(
      (check) => (check.id.startsWith("changes-") || check.id === "bulk-consumers") && check.status === "blocked",
    );
    return {
      packId,
      status: requiredBlocked ? "blocked" : operatorBlocked ? "degraded" : "ready",
      checks,
    };
  }

  async magentoStoreManagement(): Promise<Record<string, unknown>> {
    const controls = await db()
      .select()
      .from(magento_store_control)
      .where(eq(magento_store_control.org_id, this.orgId))
      .orderBy(magento_store_control.domain);
    const rules = await db()
      .select()
      .from(magento_auto_rule)
      .where(eq(magento_auto_rule.org_id, this.orgId))
      .orderBy(desc(magento_auto_rule.updated_at));
    const changesets = await db()
      .select({
        id: action_changeset.id,
        domain: action_changeset.domain,
        operationId: action_changeset.operation_id,
        riskClass: action_changeset.risk_class,
        status: action_changeset.status,
        summary: action_changeset.summary,
        bulkUuid: action_changeset.bulk_uuid,
        projectedExposure: action_changeset.projected_exposure,
        inverseOfId: action_changeset.inverse_of_id,
        scope: action_changeset.scope,
        capSnapshot: action_changeset.cap_snapshot,
        createdAt: action_changeset.created_at,
        reconciledAt: action_changeset.reconciled_at,
      })
      .from(action_changeset)
      .where(eq(action_changeset.org_id, this.orgId))
      .orderBy(desc(action_changeset.created_at))
      .limit(20);
    const changesetRows = changesets.length === 0
      ? []
      : await db()
        .select({
          changesetId: action_changeset_row.changeset_id,
          entityRef: action_changeset_row.entity_ref,
          beforeImage: action_changeset_row.before_image,
          afterImage: action_changeset_row.after_image,
        })
        .from(action_changeset_row)
        .where(inArray(action_changeset_row.changeset_id, changesets.map((changeset) => changeset.id)));
    const handoffs = await db()
      .select({
        id: magento_financial_handoff.id,
        kind: magento_financial_handoff.kind,
        entityRef: magento_financial_handoff.entity_ref,
        status: magento_financial_handoff.status,
        draft: magento_financial_handoff.draft,
        evidence: magento_financial_handoff.evidence,
        createdAt: magento_financial_handoff.created_at,
        completedAt: magento_financial_handoff.completed_at,
      })
      .from(magento_financial_handoff)
      .where(eq(magento_financial_handoff.org_id, this.orgId))
      .orderBy(desc(magento_financial_handoff.created_at))
      .limit(20);
    const actionDefinitions = await db()
      .select({
        definition: pack_action_definition.definition,
        readiness: pack_action_definition.readiness,
        reason: pack_action_definition.readiness_reason,
      })
      .from(pack_action_definition)
      .where(
        and(
          eq(pack_action_definition.org_id, this.orgId),
          eq(pack_action_definition.enabled, true),
        ),
      );
    const readinessByDomain = new Map<string, { readiness: string; reason: string | null }>();
    for (const action of actionDefinitions) {
      const adapter = action.definition.adapter;
      if (!adapter || typeof adapter !== "object" || Array.isArray(adapter)) continue;
      if ((adapter as Record<string, unknown>).kind === "magento_financial_handoff") continue;
      const domain = String(action.definition.domain ?? "");
      const current = readinessByDomain.get(domain);
      if (!current || action.readiness === "blocked") {
        readinessByDomain.set(domain, { readiness: action.readiness, reason: action.reason });
      }
    }
    const operations = actionDefinitions.flatMap(({ definition }) => {
      const domain = String(definition.domain ?? "");
      const adapter = definition.adapter;
      if (!adapter || typeof adapter !== "object" || Array.isArray(adapter)) return [];
      const declared = (adapter as Record<string, unknown>).operations;
      if (!declared || typeof declared !== "object" || Array.isArray(declared)) return [];
      return Object.entries(declared as Record<string, unknown>).flatMap(([name, value]) => {
        if (!value || typeof value !== "object" || Array.isArray(value)) return [];
        const operation = value as Record<string, unknown>;
        return [{
          name,
          domain,
          operationId: String(operation.operationId ?? ""),
          executionMode: magentoExecutionMode(
            Number(operation.defaultClass) as MagentoRiskClass,
          ),
          reversible: Boolean(operation.reversible),
          resultMode: String(operation.resultMode ?? "sync"),
        }];
      });
    });
    const changesetsWithMode = changesets.map(({ riskClass, ...changeset }) => ({
      ...changeset,
      executionMode: magentoExecutionMode(riskClass as MagentoRiskClass),
    }));
    const rowsByChangeset = new Map<string, typeof changesetRows>();
    for (const row of changesetRows) {
      const rows = rowsByChangeset.get(row.changesetId) ?? [];
      rows.push(row);
      rowsByChangeset.set(row.changesetId, rows);
    }
    return {
      controls: controls.map((control) => {
        const domainReadiness = readinessByDomain.get(control.domain);
        return {
          domain: control.domain,
          automationEligible: control.risk_class === 2,
          enabled: control.enabled,
          autoExecute: control.auto_execute,
          caps: control.caps,
          scope: control.scope,
          readiness: domainReadiness?.readiness ?? "blocked",
          readinessReason: domainReadiness ? domainReadiness.reason : "change_access_unavailable",
          readinessMessage: domainReadiness
            ? operatorReadinessDetail(
              domainReadiness.reason as MagentoPreflightResult["operatorReadiness"],
            )
            : "View-only access could not be checked because the reporting connection is unavailable.",
          updatedAt: control.updated_at,
        };
      }),
      rules: rules.map((rule) => {
        const compiledPolicy = rule.compiled_policy;
        return {
          id: rule.id,
          name: rule.name,
          instruction: rule.instruction,
          domain: rule.domain,
          actionKind: rule.action_kind,
          compiledPolicy,
          dailyCap: rule.daily_cap,
          cooldownSeconds: rule.cooldown_seconds,
          enabled: rule.enabled,
          suspendedReason: rule.suspended_reason,
          lastFiredAt: rule.last_fired_at,
          isTest: isMagentoTestRule({ name: rule.name, compiledPolicy }),
        };
      }),
      changesets: changesetsWithMode,
      handoffs,
      activity: buildMagentoActivity({
        changesets: changesetsWithMode.map((changeset) => ({
          ...changeset,
          rows: rowsByChangeset.get(changeset.id) ?? [],
        })),
        handoffs,
      }),
      operations,
      handoffOnly: {
        executePath: false,
        handoffKinds: [
          "online_refund",
          "return_approval",
          "financial_configuration",
          "store_credit_over_cap",
        ],
      },
    };
  }

  async updateMagentoStoreManagement(
    input: Record<string, unknown>,
  ): Promise<Record<string, unknown>> {
    const action = String(input.action ?? "");
    const actorUserId = typeof input.actorUserId === "string" ? input.actorUserId : null;
    if (action === "update_domain") {
      const domain = String(input.domain ?? "");
      if (!["catalog", "inventory", "orders", "promotions", "content", "customers"].includes(domain)) {
        throw new Error("unknown Magento change domain");
      }
      const [current] = await db()
        .select()
        .from(magento_store_control)
        .where(
          and(
            eq(magento_store_control.org_id, this.orgId),
            eq(magento_store_control.domain, domain),
          ),
        )
        .limit(1);
      if (!current) throw new Error(`Magento ${domain} control is not installed`);
      const set: Record<string, unknown> = {
        updated_by_user_id: actorUserId,
        updated_at: new Date(),
      };
      if (typeof input.enabled === "boolean") set.enabled = input.enabled;
      if (typeof input.autoExecute === "boolean") {
        if (input.autoExecute && current.risk_class !== 2) {
          throw new Error("Automatic execution is not available for this Magento domain");
        }
        set.auto_execute = input.autoExecute;
      }
      if (input.caps !== undefined) {
        if (!input.caps || typeof input.caps !== "object" || Array.isArray(input.caps)) {
          throw new Error("Magento caps must be an object");
        }
        const allowed = new Set([
          "maxRowsPerChangeset",
          "maxPriceDeltaPercent",
          "maxDiscountPercent",
          "maxCouponCount",
          "maxProjectedExposure",
          "maxDailyAutoActions",
          "maxStoreCredit",
          "minPromotionDays",
          "skuCooldownSeconds",
        ]);
        const caps = { ...(current.caps as Record<string, unknown>) };
        for (const [key, value] of Object.entries(input.caps as Record<string, unknown>)) {
          if (!allowed.has(key)) throw new Error(`unknown Magento cap ${key}`);
          const number = Number(value);
          if (!Number.isFinite(number) || number < 0) throw new Error(`Magento cap ${key} must be non-negative`);
          caps[key] = number;
        }
        set.caps = caps;
      }
      await db().update(magento_store_control).set(set).where(
        and(
          eq(magento_store_control.org_id, this.orgId),
          eq(magento_store_control.domain, domain),
        ),
      );
    } else if (action === "create_rule") {
      const name = typeof input.name === "string" ? input.name.trim() : "";
      const instruction = typeof input.instruction === "string" ? input.instruction.trim() : "";
      const domain = String(input.domain ?? "");
      const actionKind = typeof input.actionKind === "string" ? input.actionKind.trim() : "";
      const policySource = input.source === "acceptance_test"
        ? "acceptance_test"
        : "admin_plain_language";
      const dailyCap = Number(input.dailyCap);
      const cooldownSeconds = Number(input.cooldownSeconds ?? 0);
      if (!name || name.length > 120 || !instruction || instruction.length > 1000) {
        throw new Error("Magento automatic rule needs a concise name and instruction");
      }
      if (!Number.isInteger(dailyCap) || dailyCap < 1 || !Number.isInteger(cooldownSeconds) || cooldownSeconds < 0) {
        throw new Error("Magento automatic rule caps are invalid");
      }
      const [control] = await db().select().from(magento_store_control).where(and(
        eq(magento_store_control.org_id, this.orgId),
        eq(magento_store_control.domain, domain),
      )).limit(1);
      if (!control || !control.enabled || !control.auto_execute || control.risk_class !== 2) {
        throw new Error(`Magento ${domain} automatic execution is not enabled`);
      }
      const [definition] = await db().select({ definition: pack_action_definition.definition })
        .from(pack_action_definition)
        .where(and(
          eq(pack_action_definition.org_id, this.orgId),
          eq(pack_action_definition.kind, actionKind),
          eq(pack_action_definition.enabled, true),
        )).limit(1);
      if (!definition || String(definition.definition.domain ?? "") !== domain) {
        throw new Error("Magento automatic rule action does not belong to this domain");
      }
      const controlDailyCap = Number((control.caps as Record<string, unknown>).maxDailyAutoActions ?? 0);
      if (controlDailyCap > 0 && dailyCap > controlDailyCap) {
        throw new Error(`Rule daily cap exceeds the domain ceiling of ${controlDailyCap}`);
      }
      await db().insert(magento_auto_rule).values({
        org_id: this.orgId,
        name,
        instruction,
        domain,
        action_kind: actionKind,
        compiled_policy: {
          version: 1,
          source: policySource,
          condition: "watcher_finding",
          dailyCap,
          cooldownSeconds,
        },
        daily_cap: dailyCap,
        cooldown_seconds: cooldownSeconds,
        enabled: Boolean(input.enabled),
        created_by_user_id: actorUserId,
      }).onConflictDoUpdate({
        target: [magento_auto_rule.org_id, magento_auto_rule.name],
        set: {
          instruction,
          domain,
          action_kind: actionKind,
          compiled_policy: {
            version: 1,
            source: policySource,
            condition: "watcher_finding",
            dailyCap,
            cooldownSeconds,
          },
          daily_cap: dailyCap,
          cooldown_seconds: cooldownSeconds,
          enabled: Boolean(input.enabled),
          suspended_reason: null,
          updated_at: new Date(),
        },
      });
    } else if (action === "set_rule_status") {
      const ruleId = typeof input.ruleId === "string" ? input.ruleId : "";
      if (!ruleId || typeof input.enabled !== "boolean") {
        throw new Error("Magento rule status needs ruleId and enabled");
      }
      await db().update(magento_auto_rule).set({
        enabled: input.enabled,
        suspended_reason: input.enabled ? null : "suspended_by_admin",
        updated_at: new Date(),
      }).where(and(eq(magento_auto_rule.id, ruleId), eq(magento_auto_rule.org_id, this.orgId)));
    } else {
      throw new Error("unknown Magento store-management action");
    }
    return this.magentoStoreManagement();
  }

  async install(packId: string, request: PackInstallRequest = {}): Promise<PackStatus> {
    return this.apply(packId, request, "install");
  }

  async configure(packId: string, request: PackInstallRequest = {}): Promise<PackStatus> {
    const current = await this.status(packId);
    if (!current || current.status !== "installed") {
      throw new Error(`pack ${packId} must be installed before it can be configured`);
    }
    return this.apply(packId, request, "configure");
  }

  async upgrade(packId: string, request: PackInstallRequest = {}): Promise<PackStatus> {
    const current = await this.status(packId);
    if (!current || current.status !== "installed") {
      throw new Error(`pack ${packId} must be installed before it can be upgraded`);
    }
    return this.apply(packId, request, "upgrade");
  }

  async uninstall(packId: string, request: PackUninstallRequest = {}): Promise<PackStatus> {
    const replay = await this.replayIdempotentOperation(packId, request.idempotencyKey);
    if (replay) return replay;
    let [installation] = await db().select().from(pack_install).where(and(
      eq(pack_install.org_id, this.orgId),
      eq(pack_install.pack_id, packId),
    )).orderBy(desc(pack_install.created_at)).limit(1);
    if (!installation) throw new Error(`pack ${packId} is not installed`);
    if (installation.status === "removed") {
      const removed = await this.status(packId);
      if (!removed) throw new Error(`pack ${packId} removal status is missing`);
      return removed;
    }

    const bundle = await this.installedBundle(installation);
    const usesGraphjin = Boolean(bundle.manifest.artifacts.graphjin || storedRuntime(installation.config)?.source);
    const client = await pool().connect();
    let operationId: string | null = null;
    let graphjinRestore: (() => Promise<void>) | null = null;
    let secretsRestore: (() => Promise<void>) | null = null;
    const stagedRemovals: Array<{
      restore: () => Promise<void>;
      commit: () => Promise<void>;
    }> = [];
    try {
      await client.query("SELECT pg_advisory_lock(hashtextextended($1, 0))", [`pack:${this.orgId}:${packId}`]);
      const lockedReplay = await this.replayIdempotentOperation(packId, request.idempotencyKey);
      if (lockedReplay) return lockedReplay;
      [installation] = await db().select().from(pack_install).where(and(
        eq(pack_install.org_id, this.orgId),
        eq(pack_install.pack_id, packId),
      )).orderBy(desc(pack_install.created_at)).limit(1);
      if (!installation) throw new Error(`pack ${packId} is not installed`);
      if (installation.status === "removed") {
        const removed = await this.status(packId);
        if (!removed) throw new Error(`pack ${packId} removal status is missing`);
        return removed;
      }
      if (["installing", "upgrading", "removing"].includes(installation.status)) {
        throw new Error(`pack ${packId} already has an operation in progress`);
      }
      const plan = await this.planBundle(bundle);
      const conflicts = plan.entries.filter((entry) => entry.action === "conflict");
      if (conflicts.length > 0) {
        const conflictKeys = new Set(conflicts.map((entry) => `${entry.kind}:${entry.key}`));
        const rows = await db().select().from(pack_artifact)
          .where(eq(pack_artifact.pack_install_id, installation.id));
        await db().transaction(async (tx) => {
          for (const row of rows) {
            if (!conflictKeys.has(`${row.artifact_kind}:${row.artifact_key}`)) continue;
            await tx.update(pack_artifact).set({ ownership: "modified", updated_at: new Date() })
              .where(eq(pack_artifact.id, row.id));
          }
        });
        throw new Error(`pack uninstall has ${conflicts.length} drift conflict(s); modified artifacts were preserved`);
      }
      await db().update(pack_install).set({
        status: "removing",
        last_error: null,
        updated_at: new Date(),
      }).where(eq(pack_install.id, installation.id));
      const [operation] = await db().insert(pack_operation).values({
        pack_install_id: installation.id,
        org_id: this.orgId,
        operation_type: "uninstall",
        actor_user_id: request.actorUserId ?? null,
        status: "running",
        requested_version: installation.version,
        idempotency_key: request.idempotencyKey ?? randomUUID(),
        plan: plan as unknown as Record<string, unknown>,
        plan_hash: plan.hash,
        phase: "removing_graphjin",
        started_at: new Date(),
      }).returning({ id: pack_operation.id });
      operationId = operation!.id;
      await db().update(pack_install).set({ operation_id: operationId })
        .where(eq(pack_install.id, installation.id));

      await verifyPackQueryTables(storedRuntime(installation.config)?.tables);
      const selection = storedRuntime(installation.config)?.source;
      const bound = selection ? await resolvePackSource(this.orgId, selection) : null;
      const [fallback] = bound || !usesGraphjin ? [] : await db().select({ graphqlUrl: data_source.graphql_url })
        .from(data_source)
        .where(and(eq(data_source.org_id, this.orgId), eq(data_source.enabled, true)))
        .orderBy(desc(data_source.is_default), data_source.created_at)
        .limit(1);
      const source = bound ?? fallback;
      const configFile = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim() ?? "";
      if (usesGraphjin && (!source?.graphqlUrl || !configFile)) {
        throw new Error("customer GraphJin endpoint/config volume is unavailable");
      }
      const provenance = await db().select().from(pack_artifact)
        .where(eq(pack_artifact.pack_install_id, installation.id));
      const desiredByIdentity = new Map(
        bundle.artifacts.map((artifact) => [`${artifact.kind}:${artifact.key}`, artifact]),
      );
      const sourceNames = provenance
        .filter((artifact) => artifact.artifact_kind === "source" && !artifact.metadata?.borrowedSource)
        .map((artifact) => artifact.target_ref);
      // GraphJin 3.18 cannot remove a source while tables still reference it,
      // does not expose remove_tables through gj_config, and treats tables: []
      // as a no-op. Revoke every source capability instead. The disabled
      // source/table metadata is retained so uninstall is fail closed and a
      // later pack install can safely reclaim it.
      const revoked = usesGraphjin ? await applyPackGraphjinConfig({
        endpoint: graphjinEndpoint(source!.graphqlUrl),
        orgId: this.orgId,
        configFile,
        update: {
          source_patches: sourceNames.map((name) => ({
            name,
            read_only: true,
            access: {
              read: "blocked",
              write: "blocked",
              delete: "blocked",
            },
          })),
        },
        ownedSourceNames: new Set(sourceNames),
        restartAfterPersist: true,
      }) : { restore: async () => {} };
      graphjinRestore = revoked.restore;

      const configRoot = dirname(configFile);
      const workspace = await ensureOrgWorkspace(this.orgId);
      for (const artifact of provenance) {
        const target = artifact.metadata?.materializedTarget;
        if (typeof target !== "string") continue;
        if (artifact.artifact_kind === "spec" || artifact.artifact_kind === "saved_query") {
          stagedRemovals.push(
            await stageOwnedRemoval(assertOwnedPath(configRoot, target, "GraphJin pack file")),
          );
        } else if (artifact.artifact_kind === "skill") {
          stagedRemovals.push(
            await stageOwnedRemoval(assertOwnedPath(workspace.skillsRoot, target, "pack skill")),
          );
        }
      }

      const currentSecrets = await readSecretsStore();
      const secretSection = `${PACK_SECRET_PREFIX}${packId}`;
      const nextSecrets = { ...currentSecrets };
      delete nextSecrets[secretSection];
      await writeSecretsStore(nextSecrets);
      secretsRestore = () => writeSecretsStore(currentSecrets);

      const retiredHashes = new Map<string, string>();
      for (const artifact of provenance.filter(
        (value) => value.artifact_kind === "source" || value.artifact_kind === "relationships",
      )) {
        const identity = `${artifact.artifact_kind}:${artifact.artifact_key}`;
        const current = await inspectInstalledPackArtifactCurrent({
          orgId: this.orgId,
          kind: artifact.artifact_kind as "source" | "relationships",
          targetRef: artifact.target_ref,
          metadata: artifact.metadata ?? {},
          graphjinConfigFile: configFile,
          ...(desiredByIdentity.get(identity)
            ? { fallbackArtifact: desiredByIdentity.get(identity)! }
            : {}),
        });
        if (!current) throw new Error(`pack GraphJin artifact ${identity} disappeared during uninstall`);
        retiredHashes.set(identity, current);
      }
      await db().transaction(async (tx) => {
        for (const artifact of provenance) {
          const identity = `${artifact.artifact_kind}:${artifact.artifact_key}`;
          const locator = artifactLocatorFromMetadata(
            artifact.metadata,
            desiredByIdentity.get(identity),
          );
          if (artifact.artifact_kind === "metric") {
            const role = String(locator.role ?? "");
            if (!role) throw new Error(`pack metric ${artifact.artifact_key} has no uninstall locator`);
            const [row] = await tx.select().from(metric).where(and(
              eq(metric.org_id, this.orgId),
              eq(metric.role, role),
              eq(metric.slug, String(locator.slug ?? artifact.target_ref)),
            )).limit(1);
            if (!row) throw new Error(`pack metric ${artifact.target_ref} disappeared during uninstall`);
            await tx.update(metric).set({ active: false, updated_at: new Date() }).where(eq(metric.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("metric", { ...row, active: false } as unknown as Record<string, unknown>));
          } else if (artifact.artifact_kind === "watcher") {
            const [row] = await tx.select().from(watcher).where(and(
              eq(watcher.org_id, this.orgId),
              eq(watcher.name, String(locator.name ?? artifact.target_ref)),
            )).limit(1);
            if (!row) throw new Error(`pack watcher ${artifact.target_ref} disappeared during uninstall`);
            await tx.update(watcher).set({ enabled: false, updated_at: new Date() }).where(eq(watcher.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("watcher", { ...row, enabled: false } as unknown as Record<string, unknown>));
          } else if (artifact.artifact_kind === "workflow") {
            const [row] = await tx.select().from(workflow_definition).where(and(
              eq(workflow_definition.org_id, this.orgId),
              eq(workflow_definition.owner_user_id, ""),
              eq(workflow_definition.name, String(locator.name ?? artifact.target_ref)),
            )).limit(1);
            if (!row) throw new Error(`pack workflow ${artifact.target_ref} disappeared during uninstall`);
            await tx.update(workflow_definition).set({
              enabled: false,
              cron_enabled: false,
              updated_at: new Date(),
            }).where(eq(workflow_definition.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("workflow", {
              ...row,
              enabled: false,
              cron_enabled: false,
            } as unknown as Record<string, unknown>));
          } else if (artifact.artifact_kind === "policy") {
            const [row] = await tx.select().from(action_policy).where(and(
              eq(action_policy.org_id, this.orgId),
              eq(action_policy.name, String(locator.name ?? artifact.target_ref)),
            )).limit(1);
            if (!row) throw new Error(`pack policy ${artifact.target_ref} disappeared during uninstall`);
            await tx.update(action_policy).set({ enabled: false, updated_at: new Date() })
              .where(eq(action_policy.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("policy", { ...row, enabled: false } as unknown as Record<string, unknown>));
          } else if (artifact.artifact_kind === "action") {
            const [row] = await tx.select().from(pack_action_definition).where(and(
              eq(pack_action_definition.org_id, this.orgId),
              eq(pack_action_definition.kind, String(locator.kind ?? artifact.target_ref)),
            )).limit(1);
            if (!row) throw new Error(`pack action ${artifact.target_ref} disappeared during uninstall`);
            await tx.update(pack_action_definition).set({
              enabled: false,
              readiness: "blocked",
              readiness_reason: "pack_uninstalled",
              updated_at: new Date(),
            }).where(eq(pack_action_definition.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("action", { ...row, enabled: false } as unknown as Record<string, unknown>));
          }

          const materializedHash = retiredHashes.get(identity) ?? canonicalHash({ retired: true, identity });
          await tx.update(pack_artifact).set({
            last_applied_hash: materializedHash,
            ownership: "retired",
            readiness: "not_applicable",
            readiness_reason: null,
            metadata: {
              ...(artifact.metadata ?? {}),
              stateHashVersion: 1,
              retiredMissing: !retiredHashes.has(identity),
              ...(
                artifact.artifact_kind === "source" || artifact.artifact_kind === "relationships"
                  ? { retainedForGraphjinCompatibility: true }
                  : {}
              ),
            },
            updated_at: new Date(),
          }).where(eq(pack_artifact.id, artifact.id));
        }
        await tx.delete(pack_connection_client).where(eq(pack_connection_client.pack_install_id, installation.id));
        await tx.update(pack_install).set({
          status: "removed",
          removed_at: new Date(),
          updated_at: new Date(),
        }).where(eq(pack_install.id, installation.id));
        await tx.update(pack_operation).set({
          status: "succeeded",
          phase: "complete",
          compensation_status: "not_required",
          completed_at: new Date(),
          updated_at: new Date(),
        }).where(eq(pack_operation.id, operationId!));
      });

      for (const removal of stagedRemovals) {
        await removal.commit().catch((error) => {
          console.warn(`[packs] uninstall cleanup warning: ${error instanceof Error ? error.message : String(error)}`);
        });
      }
      const removed = await this.status(packId);
      if (!removed) throw new Error("pack status missing after uninstall");
      return removed;
    } catch (error) {
      let compensationFailed = false;
      if (secretsRestore) await secretsRestore().catch(() => { compensationFailed = true; });
      for (const removal of stagedRemovals.reverse()) {
        await removal.restore().catch(() => { compensationFailed = true; });
      }
      if (graphjinRestore) await graphjinRestore().catch(() => { compensationFailed = true; });
      const message = (error instanceof Error ? error.message : String(error)).slice(0, 1000);
      if (operationId) {
        await db().update(pack_operation).set({
          status: "failed",
          failure_phase: "uninstall",
          error: message,
          compensation_status: compensationFailed ? "failed" : "succeeded",
          completed_at: new Date(),
          updated_at: new Date(),
        }).where(eq(pack_operation.id, operationId)).catch(() => {});
      }
      await db().update(pack_install).set({
        status: compensationFailed ? "failed" : installation.status,
        operation_id: installation.operation_id,
        last_error: message,
        updated_at: new Date(),
      }).where(eq(pack_install.id, installation.id)).catch(() => {});
      throw error;
    } finally {
      await client.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [`pack:${this.orgId}:${packId}`]).catch(() => {});
      client.release();
    }
  }

  private async apply(
    packId: string,
    request: PackInstallRequest,
    operationType: "install" | "configure" | "upgrade",
  ): Promise<PackStatus> {
    const replay = await this.replayIdempotentOperation(packId, request.idempotencyKey);
    if (replay) return replay;
    const firstPartyMagento = packId === "magento";
    const secretSection = `${PACK_SECRET_PREFIX}${packId}`;
    const client = await pool().connect();
    let cleanupBundle: (() => Promise<void>) | undefined;
    let priorInstallation: typeof pack_install.$inferSelect | undefined;
    let installationId: string | null = null;
    let operationId: string | null = null;
    let graphjinRestore: (() => Promise<void>) | null = null;
    let filesRestore: (() => Promise<void>) | null = null;
    let skillRestore: (() => Promise<void>) | null = null;
    let skillCommit: (() => Promise<void>) | null = null;
    let retiredRestore: (() => Promise<void>) | null = null;
    let retiredCommit: (() => Promise<void>) | null = null;
    let secretsRestore: (() => Promise<void>) | null = null;
    try {
      await client.query("SELECT pg_advisory_lock(hashtextextended($1, 0))", [`pack:${this.orgId}:${packId}`]);
      const lockedReplay = await this.replayIdempotentOperation(packId, request.idempotencyKey);
      if (lockedReplay) return lockedReplay;
      const [existing] = await db()
        .select()
        .from(pack_install)
        .where(and(eq(pack_install.org_id, this.orgId), eq(pack_install.pack_id, packId)))
        .orderBy(desc(pack_install.created_at))
        .limit(1);
      priorInstallation = existing;
      if (operationType !== "install" && existing?.status !== "installed") {
        throw new Error(`pack ${packId} must be installed before it can be ${operationType}d`);
      }
      if (existing && ["installing", "upgrading", "removing"].includes(existing.status)) {
        throw new Error(`pack ${packId} already has an operation in progress`);
      }

      if (operationType === "configure" && request.version && request.version !== existing?.version) throw new Error("configure cannot change the installed pack version; review an upgrade instead");
      let authoredBundle = operationType === "configure" && existing ? await this.installedBundle(existing) : await this.bundle(packId, request.version);
      if (authoredBundle.upload) {
        const snapshot = await snapshotUploadedPack(this.uploadsRoot(), authoredBundle);
        // Keep normalized binding targets from the validated catalog bundle.
        snapshot.bundle.artifacts.forEach(artifact => { artifact.targetRef = authoredBundle.artifacts.find(value => value.kind === artifact.kind && value.key === artifact.key)!.targetRef; });
        authoredBundle = snapshot.bundle;
        cleanupBundle = snapshot.cleanup;
      }
      let bundle: SolutionPackBundle = authoredBundle;
      const runtime = await this.runtime(bundle, request, storedRuntime(existing?.config ?? {}));
      if (!firstPartyMagento) {
        const bound = bindPackQueries(bundle, runtime.bindings);
        bundle = bound.bundle;
        // Retain alias ownership across upgrades, like the existing source/table metadata.
        runtime.tables = [...new Map([...(storedRuntime(existing?.config ?? {})?.tables ?? []), ...bound.tables].map(table => [table.name, table])).values()];
      }
      const source = runtime.source;
      const declaredInputs = new Set(bundle.manifest.inputs.map((input) => input.key));
      const priorInputs = Object.fromEntries(
        Object.entries((existing?.config ?? {}) as Record<string, unknown>)
          .filter(([key]) => declaredInputs.has(key)),
      );
      const inputs = resolveInputs(bundle, { ...priorInputs, ...(request.inputs ?? {}) });
      const resolvedSecrets = await resolveSecrets(bundle, request);
      const plan = await this.planBundle(authoredBundle);
      const reviewHash = this.reviewHash(authoredBundle, plan, operationType, request, inputs, resolvedSecrets.values, runtime);
      if ((authoredBundle.upload || authoredBundle.manifest.connectors?.length || request.reviewHash) && request.reviewHash !== reviewHash) throw new Error("pack review is missing or stale; review the exact bundle, configuration, and plan before installing");
      const preflight = firstPartyMagento ? await runMagentoPreflight({
        host: String(inputs["database.host"]),
        port: Number(inputs["database.port"]),
        database: String(inputs["database.name"]),
        username: resolvedSecrets.values["database.analytics_username"]!,
        password: resolvedSecrets.values["database.analytics_password"]!,
        tablePrefix: String(inputs["magento.table_prefix"] ?? ""),
        baseUrl: String(inputs["magento.base_url"]),
        storeCode: String(inputs["magento.store_code"] ?? "all"),
        integrationToken: resolvedSecrets.values["magento.integration_token"] ?? null,
        customersEnabled: Boolean(inputs["magento.customers_enabled"]),
      }) : null;
      if (preflight) {
        inputs["database.type"] = preflight.databaseType;
        inputs["magento.table_prefix"] = preflight.tablePrefix;
        inputs["magento.base_currency"] = inputs["magento.base_currency"] ?? preflight.baseCurrency;
        inputs["magento.timezone"] = inputs["magento.timezone"] ?? preflight.timezone;
      }
      // Validate declarations before writing installation or secret state.
      if (!preflight) declarativeGraphjinUpdate(bundle, inputs, resolvedSecrets.values, [], runtime.bindings);

      const conflicts = plan.entries.filter((entry) => entry.action === "conflict");
      if (conflicts.length > 0) {
        throw new Error(`pack install has ${conflicts.length} drift conflict(s); run pack plan for details`);
      }
      const storedSecrets = resolvedSecrets.store[secretSection] ?? {};
      const secretsChanged = Object.entries(resolvedSecrets.values).some(
        ([key, value]) => storedSecrets[secretEnvKey(key)] !== value,
      ) || [...resolvedSecrets.cleared].some((key) => storedSecrets[secretEnvKey(key)] !== undefined);
      const priorNormalizedInputs = existing ? resolveInputs(bundle, priorInputs) : null;
      const configChanged = !priorNormalizedInputs ||
        canonicalHash(priorNormalizedInputs) !== canonicalHash(inputs) ||
        canonicalHash(storedRuntime(existing?.config ?? {}) ?? null) !== canonicalHash(runtime);
      if (
        existing?.status === "installed" &&
        existing.version === bundle.manifest.metadata.version &&
        existing.manifest_hash === bundle.manifestHash &&
        plan.entries.every((entry) => entry.action === "noop") &&
        !configChanged &&
        !secretsChanged
      ) {
        await this.persistLegacyStateBaselines(existing.id, plan, bundle);
        const current = await this.status(packId);
        if (!current) throw new Error(`pack ${packId} status is missing after no-op plan`);
        return current;
      }
      const priorArtifacts = existing
        ? await db().select({
            id: pack_artifact.id,
            kind: pack_artifact.artifact_kind,
            key: pack_artifact.artifact_key,
            targetRef: pack_artifact.target_ref,
            metadata: pack_artifact.metadata,
          }).from(pack_artifact).where(eq(pack_artifact.pack_install_id, existing.id))
        : [];
      const retireIdentities = new Set(
        plan.entries
          .filter((entry) => entry.action === "retire")
          .map((entry) => `${entry.kind}:${entry.key}`),
      );
      const retiredArtifacts = priorArtifacts.filter((artifact) =>
        retireIdentities.has(`${artifact.kind}:${artifact.key}`),
      );
      await assertNativeTargetsAvailable({ orgId: this.orgId, bundle, plan });
      const ownedFileTargets = new Set(
        priorArtifacts.filter((artifact) => artifact.kind !== "skill").flatMap((artifact) => {
          const target = artifact.metadata?.materializedTarget;
          return typeof target === "string" ? [target] : [];
        }),
      );
      const ownedSkillTargets = new Set(
        priorArtifacts.filter((artifact) => artifact.kind === "skill").flatMap((artifact) => {
          const target = artifact.metadata?.materializedTarget;
          return typeof target === "string" ? [target] : [];
        }),
      );
      const ownedSourceNames = new Set(
        priorArtifacts
          .filter((artifact) => artifact.kind === "source")
          .map((artifact) => artifact.targetRef),
      );
      if (existing) {
        installationId = existing.id;
        await db().update(pack_install).set({
          version: bundle.manifest.metadata.version,
          status: operationType === "upgrade" ? "upgrading" : "installing",
          manifest_hash: bundle.manifestHash,
          source: authoredBundle.upload ? "uploaded" : "embedded",
          config: { ...inputs, _runtime: runtime, ...(authoredBundle.upload ? { _bundle: authoredBundle.upload } : {}) },
          last_error: null,
          removed_at: null,
          updated_at: new Date(),
        }).where(eq(pack_install.id, existing.id));
      } else {
        const [created] = await db().insert(pack_install).values({
          org_id: this.orgId,
          pack_id: packId,
          version: bundle.manifest.metadata.version,
          status: "installing",
          manifest_hash: bundle.manifestHash,
          source: authoredBundle.upload ? "uploaded" : "embedded",
          config: { ...inputs, _runtime: runtime, ...(authoredBundle.upload ? { _bundle: authoredBundle.upload } : {}) },
          installed_by_user_id: request.actorUserId ?? null,
        }).returning({ id: pack_install.id });
        installationId = created!.id;
      }
      const [operation] = await db().insert(pack_operation).values({
        pack_install_id: installationId,
        org_id: this.orgId,
        operation_type: operationType,
        actor_user_id: request.actorUserId ?? null,
        status: "running",
        requested_version: bundle.manifest.metadata.version,
        idempotency_key: request.idempotencyKey ?? randomUUID(),
        plan: { ...plan, inputs, runtime, reviewHash },
        plan_hash: canonicalHash({ ...plan, inputs, runtime, reviewHash }),
        phase: "preflight_complete",
        started_at: new Date(),
      }).returning({ id: pack_operation.id });
      operationId = operation!.id;
      await db().update(pack_install).set({ operation_id: operationId }).where(eq(pack_install.id, installationId));

      const nextPackSecrets = { ...storedSecrets };
      for (const key of resolvedSecrets.cleared) delete nextPackSecrets[secretEnvKey(key)];
      for (const [key, value] of Object.entries(resolvedSecrets.values)) {
        nextPackSecrets[secretEnvKey(key)] = value;
      }
      const nextSecrets = {
        ...resolvedSecrets.store,
        [secretSection]: nextPackSecrets,
      };
      await writeSecretsStore(nextSecrets);
      secretsRestore = () => writeSecretsStore(resolvedSecrets.store);

      const configFile = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim() ?? "";
      if (bundle.manifest.artifacts.graphjin && (!source?.graphqlUrl || !configFile)) {
        throw new Error("customer GraphJin endpoint/config volume is unavailable");
      }
      const retiredRemovals: Array<{
        restore: () => Promise<void>;
        commit: () => Promise<void>;
      }> = [];
      const configRoot = dirname(configFile);
      const workspace = retiredArtifacts.some((artifact) => artifact.kind === "skill")
        ? await ensureOrgWorkspace(this.orgId)
        : null;
      try {
        for (const artifact of retiredArtifacts) {
          const target = artifact.metadata?.materializedTarget;
          if (typeof target !== "string") continue;
          if (artifact.kind === "spec" || artifact.kind === "saved_query") {
            retiredRemovals.push(
              await stageOwnedRemoval(assertOwnedPath(configRoot, target, "GraphJin pack file")),
            );
          } else if (artifact.kind === "skill" && workspace) {
            retiredRemovals.push(
              await stageOwnedRemoval(assertOwnedPath(workspace.skillsRoot, target, "pack skill")),
            );
          }
        }
      } catch (error) {
        for (const removal of retiredRemovals.reverse()) await removal.restore().catch(() => {});
        throw error;
      }
      retiredRestore = async () => {
        for (const removal of retiredRemovals.reverse()) await removal.restore();
      };
      retiredCommit = async () => {
        for (const removal of retiredRemovals) await removal.commit();
      };
      const desiredSourceNames = new Set(
        bundle.artifacts.filter((artifact) => artifact.kind === "source").map((artifact) => artifact.targetRef),
      );
      const retiredSourceNames = retiredArtifacts
        .filter((artifact) => artifact.kind === "source" && !artifact.metadata?.borrowedSource && !desiredSourceNames.has(artifact.targetRef))
        .map((artifact) => artifact.targetRef);
      await db().update(pack_operation).set({ phase: "graphjin_files", updated_at: new Date() }).where(eq(pack_operation.id, operationId));
      const files = await installGraphjinFiles({
        bundle,
        configFile,
        values: inputs,
        ownedTargets: ownedFileTargets,
      });
      filesRestore = files.restore;

      await this.runtime(authoredBundle, {}, { ...runtime, tables: storedRuntime(existing?.config ?? {})?.tables });
      const applied = bundle.manifest.artifacts.graphjin ? await applyPackGraphjinConfig({
        endpoint: graphjinEndpoint(source!.graphqlUrl),
        orgId: this.orgId,
        configFile,
        update: preflight
          ? graphjinUpdate(inputs, resolvedSecrets.values, preflight, bundle, retiredSourceNames)
          : { ...declarativeGraphjinUpdate(bundle, inputs, resolvedSecrets.values, retiredSourceNames, runtime.bindings), tables: (runtime.tables ?? []).map(table => ({ ...table, database: table.source })) },
        ownedSourceNames,
        ...(!preflight ? { ownedTableNames: new Set((storedRuntime(existing?.config ?? {})?.tables ?? []).map(table => table.name!)) } : {}),
        restartAfterPersist: true,
      }) : { restore: async () => {} };
      graphjinRestore = applied.restore;
      if (preflight) await runMagentoAnalyticsSmoke(graphjinEndpoint(source!.graphqlUrl), this.orgId);
      else if (source) await runPackReadPreflight(bundle, source!.graphqlUrl, this.orgId, inputs);

      const retiredHashes = new Map<string, string>();
      for (const artifact of retiredArtifacts.filter(
        (value) => value.kind === "source" || value.kind === "relationships",
      )) {
        const current = await inspectInstalledPackArtifactCurrent({
          orgId: this.orgId,
          kind: artifact.kind as "source" | "relationships",
          targetRef: artifact.targetRef,
          metadata: artifact.metadata ?? {},
          graphjinConfigFile: configFile,
        });
        if (current) retiredHashes.set(`${artifact.kind}:${artifact.key}`, current);
      }

      const skills = await installSkills({ orgId: this.orgId, bundle, ownedTargets: ownedSkillTargets });
      skillRestore = skills.restore;
      skillCommit = skills.commit;

      const materializedHashes = new Map<string, string>();
      for (const artifact of bundle.artifacts.filter((value) =>
        value.kind === "source" ||
        value.kind === "relationships" ||
        value.kind === "spec" ||
        value.kind === "saved_query" ||
        value.kind === "skill"
      )) {
        const target = files.targets.get(`${artifact.kind}:${artifact.key}`) ??
          skills.targets.get(`${artifact.kind}:${artifact.key}`) ?? artifact.targetRef;
        const currentHash = await inspectPackArtifactCurrent({
          orgId: this.orgId,
          artifact,
          metadata: { materializedTarget: target, locator: boundPackLocator(bundle, artifact, runtime.bindings), borrowedSource: Boolean(runtime.bindings[artifact.key]) },
          graphjinConfigFile: configFile,
        });
        if (!currentHash) throw new Error(`pack artifact ${artifact.key} was not materialized`);
        materializedHashes.set(`${artifact.kind}:${artifact.key}`, currentHash);
      }

      await this.runtime(authoredBundle, {}, runtime);
      await db().transaction(async (tx) => {
        for (const artifact of retiredArtifacts) {
          const identity = `${artifact.kind}:${artifact.key}`;
          const locatorValue = artifact.metadata?.locator;
          const locator = locatorValue && typeof locatorValue === "object" && !Array.isArray(locatorValue)
            ? locatorValue as Record<string, unknown>
            : {};
          if (artifact.kind === "metric") {
            const role = String(locator.role ?? "");
            if (!role) throw new Error(`pack metric ${artifact.key} has no retirement locator`);
            const [row] = await tx.select().from(metric).where(and(
              eq(metric.org_id, this.orgId),
              eq(metric.role, role),
              eq(metric.slug, String(locator.slug ?? artifact.targetRef)),
            )).limit(1);
            if (!row) throw new Error(`pack metric ${artifact.targetRef} disappeared during upgrade`);
            await tx.update(metric).set({ active: false, updated_at: new Date() }).where(eq(metric.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("metric", { ...row, active: false } as unknown as Record<string, unknown>));
          } else if (artifact.kind === "workflow") {
            const [row] = await tx.select().from(workflow_definition).where(and(
              eq(workflow_definition.org_id, this.orgId),
              eq(workflow_definition.owner_user_id, ""),
              eq(workflow_definition.name, String(locator.name ?? artifact.targetRef)),
            )).limit(1);
            if (!row) throw new Error(`pack workflow ${artifact.targetRef} disappeared during upgrade`);
            await tx.update(workflow_definition).set({
              enabled: false,
              cron_enabled: false,
              updated_at: new Date(),
            }).where(eq(workflow_definition.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("workflow", {
              ...row,
              enabled: false,
              cron_enabled: false,
            } as unknown as Record<string, unknown>));
          } else if (artifact.kind === "watcher") {
            const [row] = await tx.select().from(watcher).where(and(
              eq(watcher.org_id, this.orgId),
              eq(watcher.name, String(locator.name ?? artifact.targetRef)),
            )).limit(1);
            if (!row) throw new Error(`pack watcher ${artifact.targetRef} disappeared during upgrade`);
            await tx.update(watcher).set({ enabled: false, updated_at: new Date() }).where(eq(watcher.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("watcher", { ...row, enabled: false } as unknown as Record<string, unknown>));
          } else if (artifact.kind === "policy") {
            const [row] = await tx.select().from(action_policy).where(and(
              eq(action_policy.org_id, this.orgId),
              eq(action_policy.name, String(locator.name ?? artifact.targetRef)),
            )).limit(1);
            if (!row) throw new Error(`pack policy ${artifact.targetRef} disappeared during upgrade`);
            await tx.update(action_policy).set({ enabled: false, updated_at: new Date() }).where(eq(action_policy.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("policy", { ...row, enabled: false } as unknown as Record<string, unknown>));
          } else if (artifact.kind === "action") {
            const [row] = await tx.select().from(pack_action_definition).where(and(
              eq(pack_action_definition.org_id, this.orgId),
              eq(pack_action_definition.kind, String(locator.kind ?? artifact.targetRef)),
            )).limit(1);
            if (!row) throw new Error(`pack action ${artifact.targetRef} disappeared during upgrade`);
            await tx.update(pack_action_definition).set({
              enabled: false,
              readiness: "blocked",
              readiness_reason: "removed_from_pack",
              updated_at: new Date(),
            }).where(eq(pack_action_definition.id, row.id));
            retiredHashes.set(identity, nativeArtifactStateHash("action", { ...row, enabled: false } as unknown as Record<string, unknown>));
          }

          const retiredHash = retiredHashes.get(identity) ?? canonicalHash({ retired: true, identity });
          await tx.update(pack_artifact).set({
            last_applied_hash: retiredHash,
            ownership: "retired",
            readiness: "not_applicable",
            readiness_reason: null,
            metadata: {
              ...(artifact.metadata ?? {}),
              stateHashVersion: 1,
              retiredMissing: !retiredHashes.has(identity),
              ...(
                artifact.kind === "source" || artifact.kind === "relationships"
                  ? { retainedForGraphjinCompatibility: true }
                  : {}
              ),
            },
            updated_at: new Date(),
          }).where(eq(pack_artifact.id, artifact.id));
        }

        if (preflight) {
          const caps = magentoCapsFromInputs(inputs);
          for (const control of DEFAULT_MAGENTO_DOMAIN_CONTROLS) {
            const enabled = control.domain === "customers"
              ? Boolean(inputs["magento.customers_enabled"])
              : control.enabled;
            await tx.insert(magento_store_control).values({
              org_id: this.orgId,
              domain: control.domain,
              risk_class: control.riskClass,
              enabled,
              auto_execute: false,
              caps,
              scope: { stores: preflight.scopes },
            }).onConflictDoUpdate({
              target: [magento_store_control.org_id, magento_store_control.domain],
              set: {
                enabled,
                caps,
                scope: { stores: preflight.scopes },
                updated_at: new Date(),
              },
            });
          }
          for (const classification of DEFAULT_MAGENTO_ATTRIBUTE_CLASSIFICATIONS) {
            await tx.insert(magento_attribute_classification).values({
              org_id: this.orgId,
              domain: classification.domain,
              entity_type: classification.entityType,
              attribute: classification.attribute,
              risk_class: classification.riskClass,
              category: classification.category,
              rationale: classification.rationale,
              reviewed: true,
              pack_default: true,
            }).onConflictDoUpdate({
              target: [
                magento_attribute_classification.org_id,
                magento_attribute_classification.domain,
                magento_attribute_classification.entity_type,
                magento_attribute_classification.attribute,
              ],
              set: {
                risk_class: classification.riskClass,
                category: classification.category,
                rationale: classification.rationale,
                reviewed: true,
                pack_default: true,
                updated_at: new Date(),
              },
            });
          }

        }

        const workflows = new Map<string, string>();
        for (const artifact of bundle.artifacts.filter((value) => value.kind === "workflow")) {
          const value = artifactRecord(artifact);
          const name = String(value.name);
          const [existingWorkflow] = await tx.select({ id: workflow_definition.id }).from(workflow_definition)
            .where(and(eq(workflow_definition.org_id, this.orgId), eq(workflow_definition.owner_user_id, ""), eq(workflow_definition.name, name))).limit(1);
          const schedule = value.schedule as Record<string, unknown> | null;
          const rowValues = {
            description: String(value.description ?? ""),
            enabled: Boolean(value.enabled),
            status: String(value.status),
            goal: String(value.goal),
            cron: schedule ? String(schedule.cron) : null,
            cron_timezone: String(inputs[String(schedule?.timezoneInput)] ?? schedule?.timezoneInput ?? "UTC"),
            cron_enabled: Boolean(schedule?.enabled),
            output_contract: value.outputContract as Record<string, unknown>,
            updated_at: new Date(),
          };
          const [row] = existingWorkflow
            ? await tx.update(workflow_definition).set(rowValues).where(eq(workflow_definition.id, existingWorkflow.id)).returning({ id: workflow_definition.id })
            : await tx.insert(workflow_definition).values({ org_id: this.orgId, owner_user_id: "", name, ...rowValues }).returning({ id: workflow_definition.id });
          workflows.set(artifact.key, row!.id);
          materializedHashes.set(
            `${artifact.kind}:${artifact.key}`,
            nativeArtifactStateHash("workflow", { name, ...rowValues }),
          );
        }

        for (const artifact of bundle.artifacts.filter((value) => value.kind === "metric")) {
          const value = artifactRecord(artifact);
          const execution = value.execution as Record<string, unknown>;
          const definition = {
            ...value,
            execution: {
              ...execution,
              document: findSavedQuery(bundle, String(execution.query)),
              ...(!preflight ? { variables: packVariables(execution.variables, inputs) } : {}),
              runtime: preflight ? {
                storeIds: preflight.storeIds,
                windowDays: 30,
                staleAfterSeconds: 2 * 60 * 60,
                agedAfterSeconds: 2 * 24 * 60 * 60,
                stockThreshold: 5,
                currency: String(inputs["magento.base_currency"] ?? "USD"),
                timezone: String(inputs["magento.timezone"] ?? "UTC"),
              } : {},
            },
          };
          const set = {
            source: `pack:${packId}`,
            title: String(value.title),
            description: String(value.description),
            why: String(value.calculationNote),
            chart_hint: String(value.chartHint),
            unit: String(value.unit),
            direction_good: String(value.directionGood),
            cadence: String(value.cadence),
            active: true,
            execution_mode: "saved_query",
            definition_json: definition,
            definition_version: 1,
            definition_hash: canonicalHash(definition),
            updated_at: new Date(),
          };
          await tx.insert(metric).values({
            org_id: this.orgId,
            role: String(value.role),
            slug: String(value.targetRef),
            ...set,
          }).onConflictDoUpdate({
            target: [metric.org_id, metric.role, metric.slug],
            set,
          });
          materializedHashes.set(
            `${artifact.kind}:${artifact.key}`,
            nativeArtifactStateHash("metric", { role: String(value.role), slug: String(value.targetRef), ...set }),
          );
        }

        for (const artifact of bundle.artifacts.filter((value) => value.kind === "watcher")) {
          const value = artifactRecord(artifact);
          const workflowId = workflows.get(String(value.workflow));
          if (!workflowId) throw new Error(`watcher ${artifact.key} references missing workflow ${String(value.workflow)}`);
          const query = findSavedQuery(bundle, String(value.query));
          const set = {
            workflow_id: workflowId,
            description: String(value.description),
            enabled: Boolean(value.enabled),
            query,
            value_path: String(value.valuePath),
            op: String(value.operator),
            threshold: value.threshold,
            cadence_seconds: Number(value.cadenceSeconds),
            debounce_seconds: Number(value.debounceSeconds),
            cooldown_seconds: Number(value.cooldownSeconds),
            dedupe_key: String(value.dedupeKey),
            activation: String(value.activation),
            variables_json: preflight ? {
              from: { kind: "seconds_ago", seconds: 30 * 24 * 60 * 60 },
              to: { kind: "now" },
              staleBefore: { kind: "seconds_ago", seconds: 2 * 60 * 60 },
              olderThan: { kind: "seconds_ago", seconds: 2 * 24 * 60 * 60 },
              storeIds: { kind: "literal", value: preflight.storeIds },
              threshold: { kind: "literal", value: 5 },
            } : packVariables(value.variables, inputs),
            severity: String(value.severity),
            updated_at: new Date(),
          };
          await tx.insert(watcher).values({ org_id: this.orgId, name: String(value.name), ...set })
            .onConflictDoUpdate({ target: [watcher.org_id, watcher.name], set });
          materializedHashes.set(
            `${artifact.kind}:${artifact.key}`,
            nativeArtifactStateHash("watcher", { name: String(value.name), ...set }),
          );
        }

        for (const artifact of bundle.artifacts.filter((value) => value.kind === "policy")) {
          const value = artifactRecord(artifact);
          const [existingPolicy] = await tx.select({ id: action_policy.id }).from(action_policy)
            .where(and(eq(action_policy.org_id, this.orgId), eq(action_policy.name, String(value.name)))).limit(1);
          const set = {
            description: String(value.description),
            applies_to_kinds: value.appliesToKinds as string[],
            applies_to_scopes: value.appliesToScopes as string[],
            mode: value.mode === "ask"
              ? "approval_required"
              : value.mode === "auto"
                ? "auto_approve"
                : value.mode === "draft"
                  ? "draft_only"
                  : String(value.mode),
            allowed_targets: value.allowedTargets as Record<string, unknown>,
            limits: value.limits as Record<string, unknown>,
            approver_role: value.approverRole ? String(value.approverRole) : null,
            priority: Number(value.priority),
            enabled: Boolean(value.enabled),
            updated_at: new Date(),
          };
          if (existingPolicy) await tx.update(action_policy).set(set).where(eq(action_policy.id, existingPolicy.id));
          else await tx.insert(action_policy).values({ org_id: this.orgId, name: String(value.name), ...set });
          materializedHashes.set(
            `${artifact.kind}:${artifact.key}`,
            nativeArtifactStateHash("policy", { name: String(value.name), ...set }),
          );
        }

        for (const artifact of bundle.artifacts.filter((value) => value.kind === "action")) {
          const value = artifactRecord(artifact);
          const readinessValue = value.readiness as Record<string, unknown> | undefined;
          const domain = String(readinessValue?.domain ?? "") as MagentoDomain;
          const adapter = value.adapter as Record<string, unknown> | undefined;
          const reason = adapter?.kind === "magento_financial_handoff"
            ? "ready"
            : preflight?.operatorDomains[domain] ?? preflight?.operatorReadiness ?? "unsupported_adapter";
          const actionReady = reason === "ready";
          await tx.insert(pack_action_definition).values({
            org_id: this.orgId,
            kind: String(value.kind),
            description: String(value.description),
            definition: value,
            definition_hash: artifact.hash,
            readiness: actionReady ? "ready" : "blocked",
            readiness_reason: actionReady ? null : reason,
            enabled: true,
          }).onConflictDoUpdate({
            target: [pack_action_definition.org_id, pack_action_definition.kind],
            set: {
              description: String(value.description),
              definition: value,
              definition_hash: artifact.hash,
              readiness: actionReady ? "ready" : "blocked",
              readiness_reason: actionReady ? null : reason,
              enabled: true,
              updated_at: new Date(),
            },
          });
          materializedHashes.set(
            `${artifact.kind}:${artifact.key}`,
            nativeArtifactStateHash("action", {
              kind: String(value.kind),
              description: String(value.description),
              definition: value,
              definition_hash: artifact.hash,
              enabled: true,
            }),
          );
        }

        for (const artifact of bundle.artifacts) {
          const operatorArtifact = firstPartyMagento && (
            artifact.key === "source.magento_operator" ||
            artifact.kind === "spec" ||
            artifact.kind === "action" ||
            (artifact.kind === "saved_query" && basename(artifact.path).startsWith("action_")));
          const value = artifact.kind === "action" ? artifactRecord(artifact) : null;
          const actionDomain = String(
            (value?.readiness as Record<string, unknown> | undefined)?.domain ?? "",
          ) as MagentoDomain;
          const actionAdapter = value?.adapter as Record<string, unknown> | undefined;
          const operatorReason = actionAdapter?.kind === "magento_financial_handoff"
            ? "ready"
            : actionDomain
              ? preflight?.operatorDomains[actionDomain]
              : preflight?.operatorReadiness;
          const readiness = operatorArtifact && operatorReason !== "ready" ? "blocked" : "ready";
          const reason = operatorArtifact
            ? `operator:${actionDomain || "operator"}:${operatorReason}`
            : null;
          const target = files.targets.get(`${artifact.kind}:${artifact.key}`) ??
            skills.targets.get(`${artifact.kind}:${artifact.key}`) ?? artifact.targetRef;
          const materializedHash = materializedHashes.get(`${artifact.kind}:${artifact.key}`);
          if (!materializedHash) throw new Error(`pack artifact ${artifact.key} has no materialized state hash`);
          await tx.insert(pack_artifact).values({
            pack_install_id: installationId!,
            org_id: this.orgId,
            artifact_kind: artifact.kind,
            artifact_key: artifact.key,
            target_ref: artifact.targetRef,
            desired_hash: artifact.hash,
            last_applied_hash: materializedHash,
            ownership: "managed",
            readiness,
            readiness_reason: reason,
            metadata: {
              path: artifact.path,
              materializedTarget: target,
              locator: boundPackLocator(bundle, artifact, runtime.bindings), borrowedSource: Boolean(runtime.bindings[artifact.key]),
              stateHashVersion: 1,
            },
          }).onConflictDoUpdate({
            target: [pack_artifact.org_id, pack_artifact.artifact_kind, pack_artifact.target_ref],
            set: {
              pack_install_id: installationId!,
              artifact_key: artifact.key,
              desired_hash: artifact.hash,
              last_applied_hash: materializedHash,
              ownership: "managed",
              readiness,
              readiness_reason: reason,
              metadata: {
                path: artifact.path,
                materializedTarget: target,
                locator: boundPackLocator(bundle, artifact, runtime.bindings), borrowedSource: Boolean(runtime.bindings[artifact.key]),
                stateHashVersion: 1,
              },
              updated_at: new Date(),
            },
          });
        }
        // Keep compatible accounts across image upgrades. Changed OAuth contracts
        // require new client setup and consent; cleanup shares this transaction.
        const clients = await tx.select().from(pack_connection_client).where(eq(pack_connection_client.pack_install_id, installationId!));
        for (const client of clients) {
          const auth = authoredBundle.manifest.connectors?.find(value => value.id === client.connector_id)?.auth;
          if (!auth || canonicalHash(auth) !== client.auth_hash) await tx.delete(pack_connection_client).where(and(eq(pack_connection_client.pack_install_id, installationId!), eq(pack_connection_client.connector_id, client.connector_id)));
        }
        await tx.update(pack_install).set({
          status: "installed",
          config: { ...inputs, _runtime: runtime, ...(authoredBundle.upload ? { _bundle: authoredBundle.upload } : {}), ...(preflight ? { magentoVersion: preflight.magentoVersion } : {}) },
          installed_at: new Date(),
          updated_at: new Date(),
        }).where(eq(pack_install.id, installationId!));
        await tx.update(pack_operation).set({
          status: "succeeded",
          phase: "complete",
          compensation_status: "not_required",
          completed_at: new Date(),
          updated_at: new Date(),
        }).where(eq(pack_operation.id, operationId!));
      });

      await skillCommit?.().catch((error) => {
        console.warn(
          `[packs] installed ${packId}, but could not remove the skill backup: ${error instanceof Error ? error.message : String(error)}`,
        );
      });
      await retiredCommit?.().catch((error) => {
        console.warn(
          `[packs] installed ${packId}, but could not remove a retired-artifact backup: ${error instanceof Error ? error.message : String(error)}`,
        );
      });
      await enqueuePackMetricRefreshes(this.orgId, bundle).catch((error) => {
        console.warn(`[packs] initial metric refresh setup failed: ${error instanceof Error ? error.message : String(error)}`);
      });
      const status = await this.status(packId);
      if (!status) throw new Error("pack status missing after install");
      return status;
    } catch (error) {
      let compensationFailed = false;
      for (const restore of [skillRestore, graphjinRestore, filesRestore, retiredRestore, secretsRestore]) {
        if (!restore) continue;
        await restore().catch(() => {
          compensationFailed = true;
        });
      }
      const message = (error instanceof Error ? error.message : String(error)).slice(0, 1000);
      if (operationId) {
        await db().update(pack_operation).set({
          status: "failed",
          failure_phase: "apply",
          error: message,
          compensation_status: compensationFailed ? "failed" : "succeeded",
          completed_at: new Date(),
          updated_at: new Date(),
        }).where(eq(pack_operation.id, operationId)).catch(() => {});
      }
      if (installationId) {
        const restorePrior =
          (priorInstallation?.status === "installed" || priorInstallation?.status === "removed") &&
          !compensationFailed;
        await db().update(pack_install).set(restorePrior ? {
          status: priorInstallation!.status,
          version: priorInstallation!.version,
          source: priorInstallation!.source,
          manifest_hash: priorInstallation!.manifest_hash,
          config: priorInstallation!.config,
          operation_id: priorInstallation!.operation_id,
          installed_at: priorInstallation!.installed_at,
          removed_at: priorInstallation!.removed_at,
          last_error: message,
          updated_at: new Date(),
        } : { status: "failed", last_error: message, updated_at: new Date() })
          .where(eq(pack_install.id, installationId)).catch(() => {});
      }
      throw error;
    } finally {
      await client.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [`pack:${this.orgId}:${packId}`]).catch(() => {});
      client.release();
      await cleanupBundle?.();
    }
  }
}
