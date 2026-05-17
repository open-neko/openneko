import { readFileSync } from "node:fs";
import { copyFile, mkdir, readFile, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import {
  ExecuteActionParams,
  ExecuteActionResult,
  PluginManifest,
  PluginManifestEntry,
  RegisterResult,
  RPC_PROTOCOL_VERSION,
  type PluginActionRequest,
  type PluginActionOutcome,
} from "@open-neko/plugin-types";
import { registerActionAdapter } from "@neko/llm/workflows";
import type { ActionAdapter } from "@neko/llm/workflows";
import {
  MicrosandboxRuntime,
  networkModeFor,
  type PluginRuntime,
} from "./microsandbox-runtime.js";
import {
  isSupportedHost,
  platformTriple,
} from "./microsandbox-sdk.js";

export interface LoadPluginsOptions {
  /** Path to the OpenNeko repo root (where openneko.plugins.json lives). */
  repoRoot: string;
  /** Working dir for per-plugin VM bind mounts (one subdir per plugin). */
  workRoot: string;
  /** OCI image plugin VMs run from. Must include node. */
  image?: string;
  cpus?: number;
  memoryMb?: number;
  /**
   * For tests: inject a pre-built runtime + manifest entries instead of
   * reading from disk and loading microsandbox dynamically. The loader
   * is otherwise identical — same registration, same error handling.
   */
  runtime?: PluginRuntime;
  manifest?: PluginManifest;
  /** For tests: resolve a plugin package to its bundled runner script. */
  resolveRunner?: (pkg: string) => string;
}

export interface LoadedPlugin {
  name: string;
  version: string;
  actionKinds: string[];
}

export interface PluginLoadResult {
  loaded: LoadedPlugin[];
  skipped: Array<{ name: string; reason: string }>;
}

export interface PluginsHandle {
  result: PluginLoadResult;
  runtime: PluginRuntime | null;
  shutdown(): Promise<void>;
}

/**
 * Boots installed plugins into the worker process. Skips the whole
 * subsystem (with a clear log line) on hosts where microsandbox cannot
 * run — that matches the host-support matrix in the plan and the
 * sandbox-is-the-gate principle: no plugin ever runs without the VM.
 *
 * Successful flow per plugin entry:
 *   1. Resolve the plugin's installed package (npm name from manifest)
 *   2. Copy its bundled runner into a private per-plugin host workspace dir
 *   3. Start a microVM with the manifest's network capability
 *   4. Call register() — receive the action kinds the plugin handles
 *   5. Register a worker-side ActionAdapter for each kind that proxies
 *      execute_action calls back through the VM RPC
 */
export async function loadPlugins(
  options: LoadPluginsOptions,
): Promise<PluginsHandle> {
  const skipped: PluginLoadResult["skipped"] = [];
  const loaded: PluginLoadResult["loaded"] = [];

  const manifest = options.manifest ?? (await readManifestFromDisk(options.repoRoot));
  if (!manifest || manifest.plugins.length === 0) {
    const injected = options.runtime ?? null;
    return {
      result: { loaded, skipped },
      runtime: injected,
      shutdown: async () => {
        if (injected) await injected.destroyAll();
      },
    };
  }

  const runtime = options.runtime ?? (await createDefaultRuntime(options));
  if (!runtime) {
    const reason = `plugin runtime unavailable on ${platformTriple() ?? `${process.platform}-${process.arch}`}`;
    for (const entry of manifest.plugins) {
      skipped.push({ name: entry.name, reason });
    }
    return {
      result: { loaded, skipped },
      runtime: null,
      shutdown: async () => {},
    };
  }

  const resolveRunner = options.resolveRunner ?? defaultResolveRunner(options.repoRoot);

  for (const entry of manifest.plugins) {
    try {
      const result = await bootPlugin(entry, {
        runtime,
        workRoot: options.workRoot,
        resolveRunner,
      });
      loaded.push(result);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      skipped.push({ name: entry.name, reason: message });
      console.warn(
        `[plugin-loader] failed to load ${entry.name}@${entry.version}: ${message}`,
      );
    }
  }

  return {
    result: { loaded, skipped },
    runtime,
    shutdown: async () => {
      try {
        await runtime.destroyAll();
      } catch (err) {
        console.warn(
          `[plugin-loader] shutdown error: ${err instanceof Error ? err.message : err}`,
        );
      }
    },
  };
}

interface BootOptions {
  runtime: PluginRuntime;
  workRoot: string;
  resolveRunner: (pkg: string) => string;
}

async function bootPlugin(
  entry: PluginManifestEntry,
  options: BootOptions,
): Promise<LoadedPlugin> {
  const validated = PluginManifestEntry.parse(entry);
  const runnerPath = options.resolveRunner(validated.name);
  const hostWorkspacePath = path.join(
    options.workRoot,
    pluginIdFromName(validated.name),
  );
  await mkdir(hostWorkspacePath, { recursive: true });
  await copyFile(runnerPath, path.join(hostWorkspacePath, "run.js"));

  const pluginId = pluginIdFromName(validated.name);
  await options.runtime.start({
    id: pluginId,
    hostWorkspacePath,
    network: networkModeFor(validated.capabilities.network),
  });

  const response = await options.runtime.callRpc(pluginId, "register", "{}");
  if (!response.ok) {
    throw new Error(
      `register() failed: ${response.error.code} ${response.error.message}`,
    );
  }
  const registered = RegisterResult.parse(response.result);
  if (registered.protocol !== RPC_PROTOCOL_VERSION) {
    throw new Error(
      `unsupported RPC protocol ${registered.protocol} (host expects ${RPC_PROTOCOL_VERSION})`,
    );
  }
  if (registered.pluginName !== validated.name) {
    throw new Error(
      `plugin reported name ${registered.pluginName} but manifest says ${validated.name}`,
    );
  }
  if (registered.pluginVersion !== validated.version) {
    throw new Error(
      `plugin reported version ${registered.pluginVersion} but manifest pin is ${validated.version}`,
    );
  }

  for (const action of registered.actions) {
    const adapter = makeAdapterFor(options.runtime, pluginId, action.kind);
    registerActionAdapter(action.kind, adapter);
  }

  return {
    name: validated.name,
    version: validated.version,
    actionKinds: registered.actions.map((a) => a.kind),
  };
}

function makeAdapterFor(
  runtime: PluginRuntime,
  pluginId: string,
  kind: string,
): ActionAdapter {
  return async ({ request }) => {
    const params: PluginActionRequest = {
      id: request.id,
      orgId: request.orgId,
      scope: request.scope,
      kind: request.kind,
      target: request.target ?? null,
      summary: request.summary ?? null,
      payload: (request.payload ?? null) as Record<string, unknown> | null,
      riskLevel: request.riskLevel ?? null,
    };
    const response = await runtime.callRpc(
      pluginId,
      "execute_action",
      JSON.stringify(ExecuteActionParams.parse({ request: params })),
    );
    if (!response.ok) {
      throw new Error(
        `plugin ${pluginId} action ${kind} failed: ${response.error.code} ${response.error.message}`,
      );
    }
    const parsed = ExecuteActionResult.parse(response.result);
    const outcome: PluginActionOutcome = parsed.outcome;
    return {
      commandOrOperation: outcome.commandOrOperation ?? `plugin:${pluginId}:${kind}`,
      externalRef: outcome.externalRef ?? null,
      result: outcome.result ?? null,
    };
  };
}

async function readManifestFromDisk(
  repoRoot: string,
): Promise<PluginManifest | null> {
  const manifestPath = path.join(repoRoot, "openneko.plugins.json");
  let raw: string;
  try {
    raw = await readFile(manifestPath, "utf8");
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw err;
  }
  return PluginManifest.parse(JSON.parse(raw));
}

async function createDefaultRuntime(
  options: LoadPluginsOptions,
): Promise<PluginRuntime | null> {
  if (!isSupportedHost()) return null;
  let sdk: typeof import("microsandbox");
  try {
    sdk = (await import("microsandbox")) as typeof import("microsandbox");
  } catch {
    return null;
  }
  const factory = sdk.Sandbox as unknown as ConstructorParameters<
    typeof MicrosandboxRuntime
  >[0]["sandboxFactory"];
  const policy = sdk.NetworkPolicy as unknown as ConstructorParameters<
    typeof MicrosandboxRuntime
  >[0]["networkPolicy"];
  return new MicrosandboxRuntime({
    image: options.image ?? "node:20-alpine",
    cpus: options.cpus ?? 1,
    memoryMb: options.memoryMb ?? 256,
    sandboxFactory: factory,
    networkPolicy: policy,
  });
}

function defaultResolveRunner(repoRoot: string): (pkg: string) => string {
  return (pkg) => resolveRunnerForPackage(pkg, repoRoot);
}

function resolveRunnerForPackage(pkg: string, repoRoot: string): string {
  const require = createRequire(path.join(repoRoot, "noop.js"));
  let packageJsonPath: string;
  try {
    packageJsonPath = require.resolve(`${pkg}/package.json`);
  } catch (err) {
    throw new Error(
      `cannot resolve plugin package "${pkg}" from ${repoRoot}: ${err instanceof Error ? err.message : err}`,
    );
  }
  const pkgRoot = path.dirname(packageJsonPath);
  const meta = readPluginPackageMeta(packageJsonPath);
  const runner = meta?.openneko?.runner;
  if (!runner) {
    throw new Error(
      `plugin package "${pkg}" package.json must declare openneko.runner (relative path to a bundled runner script)`,
    );
  }
  return path.join(pkgRoot, runner);
}

interface PluginPackageMeta {
  openneko?: { runner?: string };
}

function readPluginPackageMeta(packageJsonPath: string): PluginPackageMeta | null {
  try {
    return JSON.parse(readFileSync(packageJsonPath, "utf8")) as PluginPackageMeta;
  } catch {
    return null;
  }
}

function pluginIdFromName(name: string): string {
  return name.replace(/^@/, "").replace(/\//g, "-");
}

export async function cleanWorkRoot(workRoot: string): Promise<void> {
  await rm(workRoot, { recursive: true, force: true });
}
