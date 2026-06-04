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
  statSync,
  watch as fsWatch,
} from "node:fs";
import { copyFile, mkdir, readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import {
  BeginAuthParams,
  BeginAuthRpcParams,
  BeginAuthRpcResult,
  BeginConnectParams,
  BeginConnectRpcParams,
  BeginConnectRpcResult,
  CompleteAuthParams,
  CompleteAuthRpcParams,
  CompleteAuthRpcResult,
  CompleteConnectParams,
  CompleteConnectRpcParams,
  CompleteConnectRpcResult,
  ExecuteActionParams,
  ExecuteActionResult,
  PluginManifest,
  PluginManifestEntry,
  RefreshConnectRpcParams,
  RefreshConnectRpcResult,
  RegisterResult,
  RPC_PROTOCOL_VERSION,
  type AuthIdentity,
  type ConnectorCredential,
  type PluginActionOutcome,
  type PluginActionRequest,
} from "@open-neko/plugin-types";
import {
  allSecretValuesFull,
  defaultSecretsPath,
  getOperatorCredential,
  manifestPathFor,
  readFullSecretsFileSoft,
  setOperatorCredential,
  unsetOperatorCredential,
  writeFullSecretsFile,
  type FullSecretsFile,
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

/**
 * Snapshot of the install policy + a per-entry flag, surfaced via
 * status() so the /integrations admin UI can render yellow rows for
 * entries that don't match the current policy. We grandfather (not
 * yank) — the policy change blocks NEW installs from that source,
 * existing ones keep running until the admin removes them manually.
 */
export interface FlaggedEntry {
  pluginName: string;
  reason: string;
}

export interface PluginRegistryOptions {
  /** OpenNeko repo root — manifest lives here. */
  repoRoot: string;
  /**
   * Directory containing the installed plugin npm packages (a node_modules
   * dir gets created here by `openneko install`). When unset, defaults to
   * repoRoot — the source-build dev workflow keeps using /app's node_modules.
   * In the docker-distributed worker image OPENNEKO_PLUGIN_INSTALL_DIR points
   * to /var/lib/openneko/plugins/ so plugin installs don't fight the worker's
   * own pnpm-managed node_modules.
   */
  pluginInstallDir?: string;
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
  /**
   * Fetch the current install policy. Called once per refresh; result
   * is used to flag installed entries whose source no longer matches.
   * Omit to disable policy-flagging (tests that don't care). The
   * worker wires this up to `getInstallPolicyForOrg(orgId)` from
   * @neko/db; tests pass a stub.
   */
  loadInstallPolicy?: () => Promise<{
    allowUnverified: boolean;
    allowGitUrlInstalls: boolean;
    allowedMarketplaces: string[];
  } | null>;
}

export interface RegistryStatus {
  loaded: string[];
  skipped: Array<{ name: string; reason: string }>;
  /**
   * Entries that are still loaded but whose install source no longer
   * matches the current install policy. Admin should remove them
   * manually via `openneko remove <pkg>` or /integrations.
   */
  flagged: FlaggedEntry[];
  kinds: string[];
  vmsRunning: number;
  /** Plugin id of the installed SSO provider (if any). */
  authProvider: string | null;
  /** Installed channel plugins (frontends): pluginId + provider label. */
  channels: Array<{ pluginId: string; providerLabel: string }>;
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
  /**
   * Per-operator credentials produced by `connect` capability OAuth
   * dances. Refreshed every refresh() pass from disk; persisted back
   * by completeConnect/refreshConnect. Kept in memory so per-call
   * envelope injection doesn't hit the disk on every action call.
   */
  private operators: Record<string, Record<string, ConnectorCredential>> = {};
  private scrubber: Scrubber = createScrubber([]);
  private skipped: Array<{ name: string; reason: string }> = [];
  private flagged: FlaggedEntry[] = [];
  private manifestWatcher: FSWatcher | null = null;
  private secretsWatcher: FSWatcher | null = null;
  // Poll fallback: fs.watch misses file events that come from outside the
  // process tree (e.g. a `docker exec` writing into a bind-mounted volume
  // doesn't reliably propagate inotify to a long-lived container process,
  // and on first-create the watcher hasn't been armed yet). A cheap stat
  // every few seconds catches these cases. The watcher path stays as the
  // fast path on supported FS-event setups.
  private pollTimer: NodeJS.Timeout | null = null;
  private lastManifestMtimeMs = 0;
  private lastSecretsMtimeMs = 0;
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
  private stopPolling(): void {
    if (this.pollTimer) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  async stop(): Promise<void> {
    if (this.stopped) return;
    this.stopped = true;
    this.manifestWatcher?.close();
    this.secretsWatcher?.close();
    this.manifestWatcher = null;
    this.secretsWatcher = null;
    this.stopPolling();
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

  /**
   * Snapshot of every installed plugin's declared action kinds plus
   * the seeded approval-mode hint. Consumed by the /work agent's tool
   * builder to build one MCP tool per kind. Returns an empty array
   * when no action-capable plugins are installed (auth-only plugins
   * don't contribute anything here).
   *
   * `default_mode` is passed through as-declared — scalar OR
   * per-scope object. The seeder + tool builder both understand both
   * shapes.
   */
  getRegisteredActionDescriptors(): Array<{
    kind: string;
    description: string;
    default_mode?:
      | "auto"
      | "ask"
      | "deny"
      | {
          external?: "auto" | "ask" | "deny";
          internal?: "auto" | "ask" | "deny";
        };
    example?: Record<string, unknown>;
  }> {
    const out: Array<{
      kind: string;
      description: string;
      default_mode?:
        | "auto"
        | "ask"
        | "deny"
        | {
            external?: "auto" | "ask" | "deny";
            internal?: "auto" | "ask" | "deny";
          };
      example?: Record<string, unknown>;
    }> = [];
    for (const entry of this.state.entriesByPluginId.values()) {
      for (const decl of entry.capabilities.action?.kinds ?? []) {
        out.push({
          kind: decl.kind,
          description: decl.description,
          default_mode: decl.default_mode,
          example: decl.example,
        });
      }
    }
    return out;
  }

  /** Human-readable snapshot for `openneko doctor` and admin endpoints. */
  status(): RegistryStatus {
    return {
      loaded: [...this.state.entriesByPluginId.values()].map((e) => e.name),
      skipped: [...this.skipped],
      flagged: [...this.flagged],
      kinds: [...this.state.kindToPluginId.keys()].sort(),
      vmsRunning: countRunningVms(this.runtime, this.state),
      authProvider: this.state.authPluginId,
      channels: this.getChannelProviders().map((c) => ({
        pluginId: c.pluginId,
        providerLabel: c.providerLabel,
      })),
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

  // ─── connect capability (per-operator OAuth) ─────────────────────────

  /**
   * List installed plugins that declare a `connect` capability. Used by
   * the `/integrations` web page to render one Connect button per
   * available connector.
   */
  getConnectProviders(): Array<{
    pluginId: string;
    pluginName: string;
    providerLabel: string;
    scopes: string[];
  }> {
    const out: Array<{
      pluginId: string;
      pluginName: string;
      providerLabel: string;
      scopes: string[];
    }> = [];
    for (const [pluginId, entry] of this.state.entriesByPluginId) {
      const decl = entry.capabilities.connect;
      if (!decl) continue;
      out.push({
        pluginId,
        pluginName: entry.name,
        providerLabel: decl.providerLabel,
        scopes: decl.scopes,
      });
    }
    return out;
  }

  // ─── channel capability (frontends) ─────────────────────────────────

  /**
   * Installed plugins that declare a `channel` capability. Multi-cardinality
   * like connect — an operator can run several frontends (web + Telegram + …)
   * at once.
   */
  getChannelProviders(): Array<{
    pluginId: string;
    pluginName: string;
    providerLabel: string;
    directions: string[];
    ingress: string;
  }> {
    const out: Array<{
      pluginId: string;
      pluginName: string;
      providerLabel: string;
      directions: string[];
      ingress: string;
    }> = [];
    for (const [pluginId, entry] of this.state.entriesByPluginId) {
      const decl = entry.capabilities.channel;
      if (!decl) continue;
      out.push({
        pluginId,
        pluginName: entry.name,
        providerLabel: decl.providerLabel,
        directions: decl.directions,
        ingress: decl.ingress,
      });
    }
    return out;
  }

  /**
   * Merged env (manifest defaults + per-plugin secrets) for a plugin by name.
   * Used by the worker-side inbound poller to read a channel's bot token for
   * getUpdates — the doc's thin-worker-ingress path for poll/socket channels.
   */
  getPluginEnv(pluginName: string): Record<string, string> | null {
    for (const [, entry] of this.state.entriesByPluginId) {
      if (entry.name === pluginName) return mergeEnv(entry, this.secrets);
    }
    return null;
  }

  private requireChannelPluginEntry(pluginName: string): {
    pluginId: string;
    entry: PluginManifestEntry;
  } {
    for (const [pluginId, entry] of this.state.entriesByPluginId) {
      if (entry.name === pluginName) {
        if (!entry.capabilities.channel) {
          throw new Error(
            `${pluginName}: installed but does not declare a channel capability`,
          );
        }
        return { pluginId, entry };
      }
    }
    throw new Error(
      `channel plugin ${pluginName} not installed — run \`openneko install ${pluginName}\``,
    );
  }

  /** Project + send InteractionEvents to a recipient via the plugin's deliver RPC. */
  async deliverOnChannel(
    pluginName: string,
    recipient: Record<string, unknown>,
    events: unknown[],
  ): Promise<{ delivered: boolean; ref?: string }> {
    const { pluginId, entry } = this.requireChannelPluginEntry(pluginName);
    await this.ensureVm(pluginId, entry);
    const env = mergeEnv(entry, this.secrets);
    if (!this.runtime) throw new Error("plugin-registry: runtime unavailable");
    const response = await this.runtime.callRpc(
      pluginId,
      "deliver",
      JSON.stringify({
        recipient,
        events,
        profile: entry.capabilities.channel?.profile,
      }),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `channel ${entry.name} deliver failed: ${response.error.code} ${response.error.message}`,
      );
    }
    return response.result as { delivered: boolean; ref?: string };
  }

  /**
   * Normalize a raw inbound substrate payload to IntentEvents via parse_inbound,
   * plus the sender's channel-native recipient (so the worker can auto-bind
   * delivery on first contact).
   */
  async parseInbound(
    pluginName: string,
    raw: unknown,
  ): Promise<{ intents: unknown[]; recipient?: Record<string, unknown> }> {
    const { pluginId, entry } = this.requireChannelPluginEntry(pluginName);
    await this.ensureVm(pluginId, entry);
    const env = mergeEnv(entry, this.secrets);
    if (!this.runtime) throw new Error("plugin-registry: runtime unavailable");
    const response = await this.runtime.callRpc(
      pluginId,
      "parse_inbound",
      JSON.stringify({ raw }),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `channel ${entry.name} parse_inbound failed: ${response.error.code} ${response.error.message}`,
      );
    }
    const result = response.result as {
      intents?: unknown[];
      recipient?: Record<string, unknown>;
    };
    return {
      intents: result.intents ?? [],
      ...(result.recipient ? { recipient: result.recipient } : {}),
    };
  }

  /** Verify a webhook signature in-VM via verify_inbound (secret stays in the VM). */
  async verifyInbound(
    pluginName: string,
    headers: Record<string, string>,
    body: string,
  ): Promise<boolean> {
    const { pluginId, entry } = this.requireChannelPluginEntry(pluginName);
    await this.ensureVm(pluginId, entry);
    const env = mergeEnv(entry, this.secrets);
    if (!this.runtime) throw new Error("plugin-registry: runtime unavailable");
    const response = await this.runtime.callRpc(
      pluginId,
      "verify_inbound",
      JSON.stringify({ headers, body }),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `channel ${entry.name} verify_inbound failed: ${response.error.code} ${response.error.message}`,
      );
    }
    return (response.result as { ok: boolean }).ok;
  }

  /** Pull the next batch of inbound updates via poll_inbound — for hosts without a public webhook URL. */
  async pollInbound(
    pluginName: string,
    cursor?: string,
  ): Promise<{ updates: unknown[]; cursor?: string }> {
    const { pluginId, entry } = this.requireChannelPluginEntry(pluginName);
    await this.ensureVm(pluginId, entry);
    const env = mergeEnv(entry, this.secrets);
    if (!this.runtime) throw new Error("plugin-registry: runtime unavailable");
    const response = await this.runtime.callRpc(
      pluginId,
      "poll_inbound",
      JSON.stringify(cursor ? { cursor } : {}),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `channel ${entry.name} poll_inbound failed: ${response.error.code} ${response.error.message}`,
      );
    }
    const result = response.result as { updates?: unknown[]; cursor?: string };
    return {
      updates: result.updates ?? [],
      ...(result.cursor ? { cursor: result.cursor } : {}),
    };
  }

  /**
   * Whether a given operator has an active credential for the named
   * plugin. The web app's `/integrations` page uses this to decide
   * whether to show "Connect" vs "Disconnect".
   */
  isOperatorConnected(operatorId: string, pluginName: string): boolean {
    return this.operators[operatorId]?.[pluginName] != null;
  }

  /**
   * Per-operator connect status across every installed connector. Lets
   * the web app render the integrations page in one query.
   */
  getOperatorConnectStatus(
    operatorId: string,
  ): Array<{ pluginName: string; connectedAt: string; scopes?: string[] }> {
    const byPlugin = this.operators[operatorId] ?? {};
    return Object.entries(byPlugin)
      .map(([pluginName, cred]) => ({
        pluginName,
        connectedAt: cred.connectedAt,
        scopes: cred.scopes,
      }))
      .sort((a, b) => a.pluginName.localeCompare(b.pluginName));
  }

  /**
   * Drive the connect plugin's `begin_connect` RPC. Returns the
   * authorization URL the browser should redirect to. The web app
   * mints `state` (CSRF) and `codeVerifier` (PKCE) and threads them
   * through so the matching `complete_connect` call can bind them.
   */
  async beginConnect(
    pluginName: string,
    params: BeginConnectParams,
  ): Promise<{ authorizationUrl: string }> {
    const { pluginId, entry } = this.requireConnectPluginEntry(pluginName);
    await this.ensureVm(pluginId, entry);
    const env = mergeEnv(entry, this.secrets);
    if (!this.runtime) {
      throw new Error("plugin-registry: runtime unavailable");
    }
    const response = await this.runtime.callRpc(
      pluginId,
      "begin_connect",
      JSON.stringify(BeginConnectRpcParams.parse({ params })),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `connect plugin ${entry.name} begin_connect failed: ${response.error.code} ${response.error.message}`,
      );
    }
    return BeginConnectRpcResult.parse(response.result).result;
  }

  /**
   * Drive the connect plugin's `complete_connect` RPC, then persist
   * the returned credential under the operator's slot in the secrets
   * file. The worker remains the only writer to secrets.json — the
   * plugin never touches disk directly.
   */
  async completeConnect(
    pluginName: string,
    params: CompleteConnectParams,
  ): Promise<ConnectorCredential> {
    const { pluginId, entry } = this.requireConnectPluginEntry(pluginName);
    await this.ensureVm(pluginId, entry);
    const env = mergeEnv(entry, this.secrets);
    if (!this.runtime) {
      throw new Error("plugin-registry: runtime unavailable");
    }
    const response = await this.runtime.callRpc(
      pluginId,
      "complete_connect",
      JSON.stringify(CompleteConnectRpcParams.parse({ params })),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `connect plugin ${entry.name} complete_connect failed: ${response.error.code} ${response.error.message}`,
      );
    }
    const credential = CompleteConnectRpcResult.parse(response.result).result.credential;
    await this.persistOperatorCredential(params.operatorId, entry.name, credential);
    return credential;
  }

  /**
   * Drive the plugin's `refresh_connect` to rotate an expiring access
   * token, persist the new credential. Errors if the plugin doesn't
   * declare a refresh handler — most OAuth providers expire tokens
   * after an hour, so this is virtually always required.
   */
  async refreshConnect(
    pluginName: string,
    operatorId: string,
  ): Promise<ConnectorCredential> {
    const { pluginId, entry } = this.requireConnectPluginEntry(pluginName);
    const current = getOperatorCredential(
      { env: this.secrets, operators: this.operators },
      operatorId,
      entry.name,
    );
    if (!current) {
      throw new Error(
        `connect plugin ${entry.name}: no credential to refresh for operator ${operatorId}`,
      );
    }
    await this.ensureVm(pluginId, entry);
    const env = mergeEnv(entry, this.secrets);
    if (!this.runtime) {
      throw new Error("plugin-registry: runtime unavailable");
    }
    const response = await this.runtime.callRpc(
      pluginId,
      "refresh_connect",
      JSON.stringify(RefreshConnectRpcParams.parse({ params: { operatorId, current } })),
      { env },
    );
    if (!response.ok) {
      throw new Error(
        `connect plugin ${entry.name} refresh_connect failed: ${response.error.code} ${response.error.message}`,
      );
    }
    const credential = RefreshConnectRpcResult.parse(response.result).result.credential;
    const stamped: ConnectorCredential = {
      ...credential,
      refreshedAt: credential.refreshedAt ?? new Date().toISOString(),
    };
    await this.persistOperatorCredential(operatorId, entry.name, stamped);
    return stamped;
  }

  /**
   * Disconnect an operator from a plugin — deletes the credential
   * locally. Token revocation against the upstream provider is the
   * plugin's concern via an action call beforehand if it cares.
   */
  async disconnect(pluginName: string, operatorId: string): Promise<boolean> {
    // Don't require the plugin to still be installed — a removed
    // plugin's orphaned credentials should still be cleanable.
    const before: FullSecretsFile = { env: this.secrets, operators: this.operators };
    const { store, removed } = unsetOperatorCredential(before, operatorId, pluginName);
    if (!removed) return false;
    this.operators = store.operators;
    await writeFullSecretsFile(store, this.options.secretsConfigDir);
    // Re-derive scrubber so the removed credential's tokens stop
    // showing up in the redaction set after a delay.
    this.scrubber = createScrubber(allSecretValuesFull(store));
    return true;
  }

  private async persistOperatorCredential(
    operatorId: string,
    pluginName: string,
    credential: ConnectorCredential,
  ): Promise<void> {
    const before: FullSecretsFile = { env: this.secrets, operators: this.operators };
    const after = setOperatorCredential(before, operatorId, pluginName, credential);
    this.operators = after.operators;
    await writeFullSecretsFile(after, this.options.secretsConfigDir);
    // Newly-stored tokens become part of the redaction set immediately.
    this.scrubber = createScrubber(allSecretValuesFull(after));
  }

  /**
   * Compare each entry's installSource against the current policy and
   * surface every mismatch as a flag. Entries with no installSource
   * (pre-feature legacy installs) are never flagged — we can't know
   * where they came from. Entries whose source IS allowed are clean.
   */
  private async computeFlagged(state: ManifestState): Promise<FlaggedEntry[]> {
    if (!this.options.loadInstallPolicy) return [];
    let policy: Awaited<ReturnType<typeof this.options.loadInstallPolicy>>;
    try {
      policy = await this.options.loadInstallPolicy();
    } catch (err) {
      console.warn(
        `[plugin-registry] loadInstallPolicy failed; skipping flag eval: ${err instanceof Error ? err.message : err}`,
      );
      return [];
    }
    if (!policy) return [];
    const flagged: FlaggedEntry[] = [];
    for (const entry of state.entriesByPluginId.values()) {
      const source = entry.installSource;
      if (!source) continue; // pre-feature legacy: unknowable, don't flag
      if (source === "marketplace") {
        // Marketplace entries carry the URL on entry.marketplace? Currently
        // a name like "official" — for now, treat marketplace installs as
        // grandfathered. Once the marketplace URL flows through, gate by
        // policy.allowedMarketplaces.includes(url).
        continue;
      }
      if (source === "unverified" && !policy.allowUnverified) {
        flagged.push({
          pluginName: entry.name,
          reason: "installed via --unverified; current policy disallows new unverified installs",
        });
      }
      if (source === "git-url" && !policy.allowGitUrlInstalls) {
        flagged.push({
          pluginName: entry.name,
          reason: "installed via git URL; current policy disallows new git-URL installs",
        });
      }
    }
    return flagged;
  }

  private requireConnectPluginEntry(pluginName: string): {
    pluginId: string;
    entry: PluginManifestEntry;
  } {
    for (const [pluginId, entry] of this.state.entriesByPluginId) {
      if (entry.name === pluginName) {
        if (!entry.capabilities.connect) {
          throw new Error(
            `${pluginName}: installed but does not declare a connect capability`,
          );
        }
        return { pluginId, entry };
      }
    }
    throw new Error(
      `connect plugin ${pluginName} not installed — run \`openneko install ${pluginName}\``,
    );
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
      await enrichExamplesFromPackages(
        manifest,
        this.options.pluginInstallDir ?? this.options.repoRoot,
      );
      const full = await readFullSecretsFileSoft(
        this.options.secretsConfigDir,
        (line) => console.warn(`[plugin-registry] ${line}`),
      );
      this.secrets = full.env;
      this.operators = full.operators;
      // Scrubber walks both env values AND tokens nested inside
      // per-operator credentials, so an accidentally-leaked
      // refresh_token gets redacted from agent output too.
      this.scrubber = createScrubber(allSecretValuesFull(full));

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

      // Re-evaluate the install-policy flag for every entry. Grandfather
      // (don't unregister) — operators told us to flag, not yank.
      this.flagged = await this.computeFlagged(newState);

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

    // Poll fallback — fires the same scheduleRefresh path on mtime change.
    // 3 s cadence is fine; plugin operations are infrequent and the
    // debounce in scheduleRefresh coalesces with watcher hits.
    this.lastManifestMtimeMs = mtimeOrZero(manifestPath);
    this.lastSecretsMtimeMs = mtimeOrZero(secretsFile);
    this.pollTimer = setInterval(() => {
      if (this.stopped) return;
      const m = mtimeOrZero(manifestPath);
      const s = mtimeOrZero(secretsFile);
      if (m !== this.lastManifestMtimeMs || s !== this.lastSecretsMtimeMs) {
        this.lastManifestMtimeMs = m;
        this.lastSecretsMtimeMs = s;
        this.scheduleRefresh();
      }
    }, 3000);
    // Don't keep the event loop alive just for the poll timer.
    this.pollTimer.unref?.();
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
      let env = mergeEnv(entry, this.secrets);
      // For connect-capable plugins, inject the calling operator's
      // credential as OPENNEKO_CONNECTOR_CREDENTIAL_TOKENS. The plugin's
      // action handler reads + parses this env var to make the API call.
      // Absent operator → no credential → action handler errors clearly
      // (the operator must Connect via /integrations first).
      const actorId = (request as { actorId?: string | null }).actorId ?? null;
      if (entry.capabilities.connect && actorId) {
        const credential = getOperatorCredential(
          { env: this.secrets, operators: this.operators },
          actorId,
          entry.name,
        );
        if (credential) {
          env = {
            ...env,
            OPENNEKO_CONNECTOR_CREDENTIAL_TOKENS: JSON.stringify(credential.tokens),
            OPENNEKO_OPERATOR_ID: actorId,
          };
        }
      }
      const params: PluginActionRequest = {
        id: request.id,
        orgId: request.orgId,
        actorId,
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
      this.options.resolveRunner ??
      defaultResolveRunner(
        this.options.pluginInstallDir ?? this.options.repoRoot,
      );
    const runnerPath = resolveRunner(entry.name);
    const hostWorkspacePath = path.join(this.options.workRoot, pluginId);
    await mkdir(hostWorkspacePath, { recursive: true });
    await copyFile(runnerPath, path.join(hostWorkspacePath, "run.js"));

    await this.runtime.start({
      id: pluginId,
      hostWorkspacePath,
      network: networkModeFor(entry.permissions.network),
      hosts: entry.permissions.network,
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
    if (entry.capabilities.connect) {
      if (!registered.capabilities.connect) {
        await this.runtime.stop(pluginId).catch(() => {});
        throw new Error(
          `${entry.name}: manifest declares the connect capability but VM register() reports no connect handler`,
        );
      }
      const declaredScopes = new Set(entry.capabilities.connect.scopes);
      const reportedScopes = new Set(registered.capabilities.connect.scopes);
      const missing = [...declaredScopes].filter((s) => !reportedScopes.has(s));
      if (missing.length > 0) {
        await this.runtime.stop(pluginId).catch(() => {});
        throw new Error(
          `${entry.name}: manifest declares connect scopes [${[...declaredScopes].join(", ")}] but VM reports [${[...reportedScopes].join(", ")}]`,
        );
      }
    }
    if (entry.capabilities.channel) {
      if (!registered.capabilities.channel) {
        await this.runtime.stop(pluginId).catch(() => {});
        throw new Error(
          `${entry.name}: manifest declares the channel capability but VM register() reports none`,
        );
      }
    }
  }

  private async createDefaultRuntime(): Promise<PluginRuntime | null> {
    const kind = (
      process.env.OPENNEKO_PLUGIN_RUNTIME ?? "microsandbox"
    ).toLowerCase();
    if (kind === "openshell") {
      const { OpenShellRuntime } = await import("./openshell-runtime.js");
      return new OpenShellRuntime({
        image:
          this.options.image ??
          process.env.OPENNEKO_PLUGIN_BASE_IMAGE ??
          "ghcr.io/open-neko/plugin-base:node20",
        cli: process.env.OPENSHELL_CLI || undefined,
        gatewayName: process.env.OPENSHELL_GATEWAY || undefined,
        gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
        bundleDir: this.options.workRoot,
      });
    }
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

/**
 * The marketplace schema doesn't carry action `example` payloads, and the Go
 * install path drops them — so a marketplace-installed manifest has none. The
 * agent prompt relies on examples to get payload shapes right (small models
 * otherwise invent wrong shapes), so read them back from each installed
 * package's own package.json (which always carries them) and fill any kind
 * whose manifest entry is missing one. No-ops for the source-build dev
 * manifest, which already carries examples.
 */
async function enrichExamplesFromPackages(
  manifest: PluginManifest | null,
  installDir: string,
): Promise<void> {
  if (!manifest) return;
  for (const entry of manifest.plugins) {
    const kinds = entry.capabilities.action?.kinds;
    if (!kinds?.length || kinds.every((k) => k.example !== undefined)) continue;
    let byKind: Map<string, Record<string, unknown>>;
    try {
      const pkgPath = path.join(installDir, "node_modules", entry.name, "package.json");
      const pkg = JSON.parse(await readFile(pkgPath, "utf8")) as {
        openneko?: {
          capabilities?: { action?: { kinds?: Array<{ kind?: string; example?: unknown }> } };
        };
      };
      byKind = new Map(
        (pkg.openneko?.capabilities?.action?.kinds ?? [])
          .filter(
            (k): k is { kind: string; example: Record<string, unknown> } =>
              typeof k.kind === "string" && !!k.example && typeof k.example === "object",
          )
          .map((k) => [k.kind, k.example]),
      );
    } catch {
      continue; // package unreadable → leave manifest examples untouched
    }
    for (const decl of kinds) {
      if (decl.example === undefined) {
        const ex = byKind.get(decl.kind);
        if (ex) decl.example = ex;
      }
    }
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

/** stat.mtimeMs, or 0 if the file doesn't exist. The poll loop uses 0 as
 *  "file is absent" — a transition from 0 → non-zero (file appeared) or
 *  vice-versa (file removed) also triggers refresh. */
function mtimeOrZero(filePath: string): number {
  try {
    return statSync(filePath).mtimeMs;
  } catch {
    return 0;
  }
}

function defaultResolveRunner(resolverRoot: string): (pkg: string) => string {
  return (pkg) => resolveRunnerForPackage(pkg, resolverRoot);
}

function resolveRunnerForPackage(pkg: string, resolverRoot: string): string {
  const require = createRequire(path.join(resolverRoot, "noop.js"));
  let packageJsonPath: string;
  try {
    packageJsonPath = require.resolve(`${pkg}/package.json`);
  } catch (err) {
    throw new Error(
      `cannot resolve plugin package "${pkg}" from ${resolverRoot}: ${err instanceof Error ? err.message : err}`,
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
