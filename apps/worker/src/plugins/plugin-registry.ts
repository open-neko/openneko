// Long-lived registry of installed plugins. Replaces the one-shot
// loadPlugins() from earlier — that required a worker restart to pick
// up newly-installed plugins, which a non-technical operator can't be
// expected to do.
//
// PluginRegistry:
//   - Reads openneko.plugins.json + the per-user secrets file
//   - Watches both via fs.watch, with a small debounce
//   - Maps action kind → plugin id from the manifest entries'
//     `kinds: string[]` field (populated at install time from the
//     marketplace entry)
//   - Spawns each plugin's microVM LAZILY — first execute_action for
//     that kind triggers the VM spawn; cold-start cost only paid once
//   - Owns the env-value scrubber, rebuilt on every secrets file
//     change so accidentally-leaked secrets stay redacted even when
//     the operator rotates a key mid-run
//
// What this means for the operator: after `openneko install …`, the
// new plugin's actions are usable on the next action_request. After
// `openneko secrets set …`, the new env value is in effect on the
// next execute_action. No restart anywhere.
import { type FSWatcher, existsSync, watch as fsWatch } from "node:fs";
import { copyFile, mkdir, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import {
  ExecuteActionParams,
  ExecuteActionResult,
  PluginManifest,
  PluginManifestEntry,
  RegisterResult,
  RPC_PROTOCOL_VERSION,
  type PluginActionOutcome,
  type PluginActionRequest,
} from "@open-neko/plugin-types";
import {
  allSecretValues,
  defaultSecretsPath,
  manifestPathFor,
  readSecretsStoreSoft,
  type SecretsStore,
} from "@open-neko/plugin-install";
import { createScrubber, type Scrubber } from "@neko/llm/work";
import { registerActionAdapter } from "@neko/llm/workflows";
import type { ActionAdapter } from "@neko/llm/workflows";
import {
  MicrosandboxRuntime,
  networkModeFor,
  type PluginRuntime,
} from "./microsandbox-runtime.js";
import { isSupportedHost, platformTriple } from "./microsandbox-sdk.js";

export interface PluginRegistryOptions {
  /** OpenNeko repo root — manifest lives here. */
  repoRoot: string;
  /** Per-plugin VM bind-mount root. */
  workRoot: string;
  /** Optional secrets config dir; defaults to XDG/$HOME-derived path. */
  secretsConfigDir?: string;
  image?: string;
  cpus?: number;
  memoryMb?: number;
  /** For tests: inject a runtime instead of constructing microsandbox. */
  runtime?: PluginRuntime;
  /** For tests: resolve a plugin to its bundled runner script. */
  resolveRunner?: (pkg: string) => string;
  /** ms between fs.watch fire and refresh. Default 200. */
  refreshDebounceMs?: number;
  /**
   * For tests: invoked whenever the registry registers an adapter
   * for a kind, alongside the actual registerActionAdapter call.
   * Lets tests capture the adapter closure without monkey-patching
   * the action-executor module.
   */
  onAdapter?: (kind: string, adapter: ActionAdapter) => void;
}

export interface RegistryStatus {
  loaded: string[];
  skipped: Array<{ name: string; reason: string }>;
  kinds: string[];
  vmsRunning: number;
}

interface ManifestState {
  entriesByPluginId: Map<string, PluginManifestEntry>;
  kindToPluginId: Map<string, string>;
}

const EMPTY_STATE: ManifestState = {
  entriesByPluginId: new Map(),
  kindToPluginId: new Map(),
};

const REFRESH_DEBOUNCE_MS = 200;

export class PluginRegistry {
  private runtime: PluginRuntime | null = null;
  private state: ManifestState = EMPTY_STATE;
  private secrets: SecretsStore = {};
  private scrubber: Scrubber = createScrubber([]);
  private skipped: Array<{ name: string; reason: string }> = [];
  private manifestWatcher: FSWatcher | null = null;
  private secretsWatcher: FSWatcher | null = null;
  private refreshTimer: NodeJS.Timeout | null = null;
  private refreshing = false;
  private pendingRefresh = false;
  private stopped = false;

  constructor(private readonly options: PluginRegistryOptions) {}

  /** Build runtime, take initial snapshot of manifest + secrets, install watchers. */
  async start(): Promise<void> {
    this.runtime =
      this.options.runtime ?? (await this.createDefaultRuntime());
    await this.refresh();
    this.installWatchers();
  }

  /** Stop watchers + destroy all VMs. Idempotent. */
  async stop(): Promise<void> {
    if (this.stopped) return;
    this.stopped = true;
    this.manifestWatcher?.close();
    this.secretsWatcher?.close();
    this.manifestWatcher = null;
    this.secretsWatcher = null;
    if (this.refreshTimer) clearTimeout(this.refreshTimer);
    this.refreshTimer = null;
    if (this.runtime) {
      try {
        await this.runtime.destroyAll();
      } catch (err) {
        console.warn(
          `[plugin-registry] runtime destroyAll error: ${err instanceof Error ? err.message : err}`,
        );
      }
    }
  }

  /** Current scrubber. Worker jobs call this once per agent invocation. */
  getScrubber(): Scrubber {
    return this.scrubber;
  }

  /** Human-readable snapshot for `openneko doctor` and admin endpoints. */
  status(): RegistryStatus {
    return {
      loaded: [...this.state.entriesByPluginId.values()].map((e) => e.name),
      skipped: [...this.skipped],
      kinds: [...this.state.kindToPluginId.keys()].sort(),
      vmsRunning: countRunningVms(this.runtime, this.state),
    };
  }

  /**
   * Re-read manifest + secrets, diff against current state, register
   * adapters for new kinds, stop VMs for removed plugins, rebuild the
   * scrubber from the new secret values.
   *
   * Concurrent calls coalesce: if a refresh is in flight when another
   * is requested, the second is deferred until the first finishes.
   */
  async refresh(): Promise<void> {
    if (this.refreshing) {
      this.pendingRefresh = true;
      return;
    }
    this.refreshing = true;
    try {
      const manifest = await readManifestFromDisk(this.options.repoRoot);
      const secrets = await readSecretsStoreSoft(
        this.options.secretsConfigDir,
        (line) => console.warn(`[plugin-registry] ${line}`),
      );
      this.secrets = secrets;
      this.scrubber = createScrubber(allSecretValues(secrets));

      const newState = buildState(manifest);
      const removed = diffRemoved(this.state, newState);

      // Stop VMs for plugins no longer in the manifest.
      if (this.runtime) {
        for (const pluginId of removed) {
          try {
            await this.runtime.stop(pluginId);
          } catch (err) {
            console.warn(
              `[plugin-registry] failed to stop ${pluginId}: ${err instanceof Error ? err.message : err}`,
            );
          }
        }
      }

      // Register adapters for every kind. We iterate manifest entries
      // directly (not the deduped kindToPluginId map) so we can detect
      // and surface conflicts when two plugins claim the same kind —
      // the first registration wins, the loser is recorded in skipped.
      this.skipped = [];
      const seenKinds = new Set<string>();
      for (const [pluginId, entry] of newState.entriesByPluginId) {
        for (const kind of entry.kinds ?? []) {
          if (seenKinds.has(kind)) {
            this.skipped.push({
              name: entry.name,
              reason: `kind "${kind}" already claimed by another plugin`,
            });
            continue;
          }
          seenKinds.add(kind);
          const adapter = this.makeAdapter(pluginId, kind);
          registerActionAdapter(kind, adapter);
          this.options.onAdapter?.(kind, adapter);
        }
      }

      this.state = newState;
    } finally {
      this.refreshing = false;
      if (this.pendingRefresh) {
        this.pendingRefresh = false;
        // Don't await — let the caller's await on this refresh return,
        // chase the pending one separately.
        void this.refresh().catch((err) => {
          console.warn(
            `[plugin-registry] follow-up refresh failed: ${err instanceof Error ? err.message : err}`,
          );
        });
      }
    }
  }

  private installWatchers(): void {
    const manifestPath = manifestPathFor(this.options.repoRoot);
    try {
      // fs.watch on the manifest file directly. macOS sometimes drops
      // the watcher when the file is replaced (atomic rename); the
      // {persistent: false} mode lets the process exit cleanly. On
      // ENOENT we silently skip — the file may not exist yet, and the
      // operator will create it via `openneko init`.
      if (existsSync(manifestPath)) {
        this.manifestWatcher = fsWatch(manifestPath, { persistent: false }, () =>
          this.scheduleRefresh(),
        );
      }
    } catch (err) {
      console.warn(
        `[plugin-registry] could not watch manifest: ${err instanceof Error ? err.message : err}`,
      );
    }

    const secretsFile = defaultSecretsPath(this.options.secretsConfigDir);
    try {
      // Watch the secrets file's directory rather than the file —
      // editors that "atomic save" replace the file, breaking a
      // file-level watcher. Directory-level watching with a filename
      // filter is more reliable.
      const dir = path.dirname(secretsFile);
      if (existsSync(dir)) {
        this.secretsWatcher = fsWatch(
          dir,
          { persistent: false },
          (_event, filename) => {
            if (filename === path.basename(secretsFile)) {
              this.scheduleRefresh();
            }
          },
        );
      }
    } catch (err) {
      console.warn(
        `[plugin-registry] could not watch secrets dir: ${err instanceof Error ? err.message : err}`,
      );
    }
  }

  private scheduleRefresh(): void {
    if (this.stopped || this.refreshTimer) return;
    const ms = this.options.refreshDebounceMs ?? REFRESH_DEBOUNCE_MS;
    this.refreshTimer = setTimeout(() => {
      this.refreshTimer = null;
      this.refresh().catch((err) => {
        console.warn(
          `[plugin-registry] watched refresh failed: ${err instanceof Error ? err.message : err}`,
        );
      });
    }, ms);
  }

  private makeAdapter(pluginId: string, kind: string): ActionAdapter {
    return async ({ request }) => {
      const entry = this.state.entriesByPluginId.get(pluginId);
      if (!entry) {
        throw new Error(
          `plugin-registry: ${pluginId} not in current manifest (kind=${kind}) — has the plugin been removed?`,
        );
      }
      if (!this.runtime) {
        throw new Error(
          `plugin-registry: no runtime available; plugin subsystem disabled on ${platformTriple() ?? `${process.platform}-${process.arch}`}`,
        );
      }
      await this.ensureVm(pluginId, entry);
      const env = mergeEnv(entry, this.secrets);
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
      const response = await this.runtime.callRpc(
        pluginId,
        "execute_action",
        JSON.stringify(ExecuteActionParams.parse({ request: params })),
        { env },
      );
      if (!response.ok) {
        throw new Error(
          `plugin ${pluginId} action ${kind} failed: ${response.error.code} ${response.error.message}`,
        );
      }
      const parsed = ExecuteActionResult.parse(response.result);
      const outcome: PluginActionOutcome = parsed.outcome;
      return {
        commandOrOperation:
          outcome.commandOrOperation ?? `plugin:${pluginId}:${kind}`,
        externalRef: outcome.externalRef ?? null,
        result: outcome.result ?? null,
      };
    };
  }

  private async ensureVm(
    pluginId: string,
    entry: PluginManifestEntry,
  ): Promise<void> {
    if (!this.runtime) throw new Error("plugin-registry: runtime unavailable");
    if (this.runtime.hasPlugin(pluginId)) return;

    const resolveRunner =
      this.options.resolveRunner ?? defaultResolveRunner(this.options.repoRoot);
    const runnerPath = resolveRunner(entry.name);
    const hostWorkspacePath = path.join(this.options.workRoot, pluginId);
    await mkdir(hostWorkspacePath, { recursive: true });
    await copyFile(runnerPath, path.join(hostWorkspacePath, "run.js"));

    await this.runtime.start({
      id: pluginId,
      hostWorkspacePath,
      network: networkModeFor(entry.capabilities.network),
    });

    // Sanity-check: the VM's register() must match the manifest's
    // declared kinds + name + version. If they diverge we refuse —
    // either the manifest is stale or the operator is running a
    // tampered tarball.
    const response = await this.runtime.callRpc(pluginId, "register", "{}");
    if (!response.ok) {
      await this.runtime.stop(pluginId).catch(() => {});
      throw new Error(
        `register() failed for ${entry.name}: ${response.error.code} ${response.error.message}`,
      );
    }
    const registered = RegisterResult.parse(response.result);
    if (registered.protocol !== RPC_PROTOCOL_VERSION) {
      await this.runtime.stop(pluginId).catch(() => {});
      throw new Error(
        `${entry.name}: unsupported RPC protocol ${registered.protocol} (host expects ${RPC_PROTOCOL_VERSION})`,
      );
    }
    if (registered.pluginName !== entry.name) {
      await this.runtime.stop(pluginId).catch(() => {});
      throw new Error(
        `${entry.name}: VM reports name ${registered.pluginName}`,
      );
    }
    if (registered.pluginVersion !== entry.version) {
      await this.runtime.stop(pluginId).catch(() => {});
      throw new Error(
        `${entry.name}: VM reports version ${registered.pluginVersion}, manifest pin is ${entry.version}`,
      );
    }
    if (entry.kinds) {
      const declared = new Set(entry.kinds);
      const reported = new Set(registered.actions.map((a) => a.kind));
      const missing = [...declared].filter((k) => !reported.has(k));
      if (missing.length > 0) {
        await this.runtime.stop(pluginId).catch(() => {});
        throw new Error(
          `${entry.name}: manifest declares kinds [${[...declared].join(", ")}] but VM reports [${[...reported].join(", ")}] — re-run \`openneko install\``,
        );
      }
    }
  }

  private async createDefaultRuntime(): Promise<PluginRuntime | null> {
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
      image: this.options.image ?? "node:20-alpine",
      cpus: this.options.cpus ?? 1,
      memoryMb: this.options.memoryMb ?? 256,
      sandboxFactory: factory,
      networkPolicy: policy,
    });
  }
}

async function readManifestFromDisk(
  repoRoot: string,
): Promise<PluginManifest | null> {
  const manifestPath = manifestPathFor(repoRoot);
  let raw: string;
  try {
    raw = await readFile(manifestPath, "utf8");
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw err;
  }
  try {
    return PluginManifest.parse(JSON.parse(raw));
  } catch (err) {
    console.warn(
      `[plugin-registry] ${manifestPath} is malformed; treating as empty: ${err instanceof Error ? err.message : err}`,
    );
    return null;
  }
}

function buildState(manifest: PluginManifest | null): ManifestState {
  const entriesByPluginId = new Map<string, PluginManifestEntry>();
  const kindToPluginId = new Map<string, string>();
  if (!manifest) return { entriesByPluginId, kindToPluginId };
  for (const entry of manifest.plugins) {
    const pluginId = pluginIdFromName(entry.name);
    entriesByPluginId.set(pluginId, entry);
    for (const kind of entry.kinds ?? []) {
      if (kindToPluginId.has(kind)) {
        // Last writer wins for the runtime path; refresh() emits a
        // warning when this happens via the skipped[] mechanism.
      }
      kindToPluginId.set(kind, pluginId);
    }
  }
  return { entriesByPluginId, kindToPluginId };
}

function diffRemoved(prev: ManifestState, next: ManifestState): string[] {
  const removed: string[] = [];
  for (const pid of prev.entriesByPluginId.keys()) {
    if (!next.entriesByPluginId.has(pid)) removed.push(pid);
  }
  return removed;
}

function mergeEnv(
  entry: PluginManifestEntry,
  store: SecretsStore,
): Record<string, string> {
  return {
    ...(entry.env ?? {}),
    ...(store[entry.name] ?? {}),
  };
}

function pluginIdFromName(name: string): string {
  return name.replace(/^@/, "").replace(/\//g, "-");
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
      `plugin package "${pkg}" package.json must declare openneko.runner`,
    );
  }
  return path.join(pkgRoot, runner);
}

interface PluginPackageMeta {
  openneko?: { runner?: string };
}

function readPluginPackageMeta(packageJsonPath: string): PluginPackageMeta | null {
  try {
    const fs = require("node:fs") as typeof import("node:fs");
    return JSON.parse(
      fs.readFileSync(packageJsonPath, "utf8"),
    ) as PluginPackageMeta;
  } catch {
    return null;
  }
}

function countRunningVms(
  runtime: PluginRuntime | null,
  state: ManifestState,
): number {
  if (!runtime) return 0;
  let n = 0;
  for (const pid of state.entriesByPluginId.keys()) {
    if (runtime.hasPlugin(pid)) n++;
  }
  return n;
}
