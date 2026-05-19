// Long-lived registry of installed plugins. Replaces the one-shot
// loadPlugins() from earlier — that required a worker restart to pick
// up newly-installed plugins, which a non-technical operator can't be
// expected to do.
//
// PluginRegistry:
//   - Reads openneko.plugins.json + the per-user secrets file
//   - Watches both via fs.watch, with a small debounce
//   - Maps action kind → plugin id from the manifest entries'
//     `capabilities.action.kinds` field (populated at install time
//     from the marketplace entry)
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
import {
  type FSWatcher,
  existsSync,
  readFileSync,
  watch as fsWatch,
} from "node:fs";
import { copyFile, mkdir, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import {
  BeginAuthParams,
  BeginAuthRpcParams,
  BeginAuthRpcResult,
  CompleteAuthParams,
  CompleteAuthRpcParams,
  CompleteAuthRpcResult,
  ExecuteActionParams,
  ExecuteActionResult,
  PluginManifest,
  PluginManifestEntry,
  RegisterResult,
  RPC_PROTOCOL_VERSION,
  type AuthIdentity,
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
  /**
   * Called after every successful manifest refresh with the parsed
   * manifest entries. Lets the worker seed action_policy rows from
   * each plugin's declared `default_mode` without the registry having
   * to know about the org_id or the policy subsystem.
   */
  onManifestRefresh?: (entries: PluginManifestEntry[]) => Promise<void> | void;
}

export interface RegistryStatus {
  loaded: string[];
  skipped: Array<{ name: string; reason: string }>;
  kinds: string[];
  vmsRunning: number;
  /** Plugin id of the installed SSO provider (if any). */
  authProvider: string | null;
}

/** Snapshot of the auth provider for the web app to render the sign-in page. */
export interface AuthProviderInfo {
  pluginId: string;
  pluginName: string;
  providerLabel: string;
}

interface ManifestState {
  entriesByPluginId: Map<string, PluginManifestEntry>;
  kindToPluginId: Map<string, string>;
  /** Plugin id chosen as the SSO provider (or null if none). */
  authPluginId: string | null;
}

const EMPTY_STATE: ManifestState = {
  entriesByPluginId: new Map(),
  kindToPluginId: new Map(),
  authPluginId: null,
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
  /**
   * Provider labels reported by each auth plugin's register() call.
   * Populated lazily — the first begin/complete RPC drives ensureVm
   * which runs register() and records the label here. The web app's
   * status endpoint falls back to a name-derived label until then.
   */
  private authProviderLabels: Map<string, string> = new Map();

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
      authProvider: this.state.authPluginId,
    };
  }

  /**
   * Snapshot of the installed SSO provider, if any. The web app's
   * `/api/auth/status` route hits this through the worker's admin
   * endpoint to decide whether to surface a "Sign in with …" button.
   * The label defaults to the package name when the manifest doesn't
   * carry one — the label is upgraded once register() runs and the
   * VM reports its providerLabel.
   */
  getAuthProvider(): AuthProviderInfo | null {
    const pluginId = this.state.authPluginId;
    if (!pluginId) return null;
    const entry = this.state.entriesByPluginId.get(pluginId);
    if (!entry) return null;
    const reported = this.authProviderLabels.get(pluginId);
    return {
      pluginId,
      pluginName: entry.name,
      providerLabel: reported ?? defaultProviderLabel(entry.name),
    };
  }

  /**
   * Drive the auth plugin's `begin_auth` over RPC. The web app calls
   * this through the worker admin endpoint; the registry handles VM
   * lifecycle + env injection exactly like an action call.
   */
  async beginAuth(
    params: BeginAuthParams,
  ): Promise<{ authorizationUrl: string }> {
    const provider = this.requireAuthProviderEntry();
    await this.ensureVm(provider.pluginId, provider.entry);
    const env = mergeEnv(provider.entry, this.secrets);
    if (!this.runtime) {
      throw new Error("plugin-registry: runtime unavailable");
    }
    const response = await this.runtime.callRpc(
      provider.pluginId,
      "begin_auth",
      JSON.stringify(BeginAuthRpcParams.parse({ params })),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `auth plugin ${provider.entry.name} begin_auth failed: ${response.error.code} ${response.error.message}`,
      );
    }
    return BeginAuthRpcResult.parse(response.result).result;
  }

  /**
   * Drive the auth plugin's `complete_auth` over RPC. Returns the
   * normalized identity assertion the web app uses to upsert the
   * `app_user` row.
   */
  async completeAuth(params: CompleteAuthParams): Promise<AuthIdentity> {
    const provider = this.requireAuthProviderEntry();
    await this.ensureVm(provider.pluginId, provider.entry);
    const env = mergeEnv(provider.entry, this.secrets);
    if (!this.runtime) {
      throw new Error("plugin-registry: runtime unavailable");
    }
    const response = await this.runtime.callRpc(
      provider.pluginId,
      "complete_auth",
      JSON.stringify(CompleteAuthRpcParams.parse({ params })),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `auth plugin ${provider.entry.name} complete_auth failed: ${response.error.code} ${response.error.message}`,
      );
    }
    return CompleteAuthRpcResult.parse(response.result).result.identity;
  }

  private requireAuthProviderEntry(): {
    pluginId: string;
    entry: PluginManifestEntry;
  } {
    const pluginId = this.state.authPluginId;
    if (!pluginId) {
      throw new Error(
        "no auth plugin installed — install one with `openneko install <name>` (e.g. @open-neko/plugin-scalekit)",
      );
    }
    const entry = this.state.entriesByPluginId.get(pluginId);
    if (!entry) {
      throw new Error(
        `plugin-registry: auth provider ${pluginId} disappeared from manifest mid-flight`,
      );
    }
    return { pluginId, entry };
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

      const { state: newState, authDuplicates } = buildState(manifest);
      const removed = diffRemoved(this.state, newState);

      // Clear cached labels for plugins no longer in the manifest so
      // the next install of a different auth plugin doesn't pick up
      // the prior plugin's label.
      for (const id of removed) {
        this.authProviderLabels.delete(id);
      }

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
      for (const name of authDuplicates) {
        this.skipped.push({
          name,
          reason:
            'auth capability claimed by another plugin (only one SSO provider supported per deployment)',
        });
      }
      const seenKinds = new Set<string>();
      for (const [pluginId, entry] of newState.entriesByPluginId) {
        for (const decl of entry.capabilities.action?.kinds ?? []) {
          const kind = decl.kind;
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

      if (this.options.onManifestRefresh) {
        try {
          await this.options.onManifestRefresh([...newState.entriesByPluginId.values()]);
        } catch (err) {
          console.warn(
            `[plugin-registry] onManifestRefresh hook failed: ${err instanceof Error ? err.message : err}`,
          );
        }
      }
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
      network: networkModeFor(entry.permissions.network),
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
    if (entry.capabilities.action) {
      const declared = new Set(
        entry.capabilities.action.kinds.map((a) => a.kind),
      );
      const reported = new Set(
        (registered.capabilities.action?.kinds ?? []).map((a) => a.kind),
      );
      const missing = [...declared].filter((k) => !reported.has(k));
      if (missing.length > 0) {
        await this.runtime.stop(pluginId).catch(() => {});
        throw new Error(
          `${entry.name}: manifest declares kinds [${[...declared].join(", ")}] but VM reports [${[...reported].join(", ")}] — re-run \`openneko install\``,
        );
      }
    }
    if (entry.capabilities.auth) {
      if (!registered.capabilities.auth) {
        await this.runtime.stop(pluginId).catch(() => {});
        throw new Error(
          `${entry.name}: manifest declares the auth capability but VM register() reports no auth provider`,
        );
      }
      if (registered.capabilities.auth.providerLabel) {
        this.authProviderLabels.set(
          pluginId,
          registered.capabilities.auth.providerLabel,
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

function buildState(manifest: PluginManifest | null): {
  state: ManifestState;
  authDuplicates: string[];
} {
  const entriesByPluginId = new Map<string, PluginManifestEntry>();
  const kindToPluginId = new Map<string, string>();
  if (!manifest) {
    return {
      state: { entriesByPluginId, kindToPluginId, authPluginId: null },
      authDuplicates: [],
    };
  }
  let authPluginId: string | null = null;
  const authDuplicates: string[] = [];
  for (const entry of manifest.plugins) {
    const pluginId = pluginIdFromName(entry.name);
    entriesByPluginId.set(pluginId, entry);
    for (const decl of entry.capabilities.action?.kinds ?? []) {
      // Last writer wins for the runtime path; refresh() emits a
      // warning via the skipped[] mechanism.
      kindToPluginId.set(decl.kind, pluginId);
    }
    if (entry.capabilities.auth) {
      if (authPluginId === null) {
        authPluginId = pluginId;
      } else {
        // Second auth-claimant — first one wins. The web app's
        // sign-in flow only handles one provider; surfacing two
        // would mean making the operator pick at the sign-in screen.
        authDuplicates.push(entry.name);
      }
    }
  }
  return {
    state: { entriesByPluginId, kindToPluginId, authPluginId },
    authDuplicates,
  };
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

/**
 * Best-effort label when the plugin's VM hasn't reported one yet.
 * `@open-neko/plugin-scalekit` → `Scalekit`,
 * `@some-org/plugin-okta` → `Okta`.
 */
function defaultProviderLabel(packageName: string): string {
  const base =
    packageName.split("/").pop() ?? packageName.replace(/^@/, "");
  const stripped = base.replace(/^plugin-/, "");
  if (!stripped) return packageName;
  return stripped.charAt(0).toUpperCase() + stripped.slice(1);
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
    return JSON.parse(
      readFileSync(packageJsonPath, "utf8"),
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
