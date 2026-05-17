import { mkdir } from "node:fs/promises";
import { RpcResponse } from "@open-neko/plugin-types";
import {
  applyBundledRuntime,
  formatError,
  formatMicrosandboxStartError,
  stopSandbox,
  type MicrosandboxBuilder,
  type MicrosandboxFactory,
  type MicrosandboxInstance,
  type NetworkPolicyApi,
} from "./microsandbox-sdk.js";

/** Network policy translation for a plugin manifest's declared hosts. */
export type PluginNetworkMode = "none" | "public";

export interface PluginVmSpec {
  /** Unique handle for this VM, derived from the plugin npm name. */
  id: string;
  /** Path on host that the runtime mounts at /workspace in the VM. */
  hostWorkspacePath: string;
  /** Network policy derived from the plugin manifest's `requires_network`. */
  network: PluginNetworkMode;
}

export interface MicrosandboxRuntimeOptions {
  /** OCI image that hosts node + the plugin's bundle. */
  image: string;
  cpus: number;
  memoryMb: number;
  /** Optional dependency injection for tests. */
  sandboxFactory?: MicrosandboxFactory;
  networkPolicy?: NetworkPolicyApi;
  onLog?: (line: string) => void;
}

export interface PluginRuntime {
  start(spec: PluginVmSpec): Promise<void>;
  /**
   * Invokes the plugin's runner with `node /workspace/run.js <method>
   * <paramsJson>` and parses the single JSON response from stdout.
   */
  callRpc(
    pluginId: string,
    method: string,
    paramsJson: string,
    options?: { timeoutMs?: number },
  ): Promise<RpcResponse>;
  stop(pluginId: string): Promise<void>;
  destroyAll(): Promise<void>;
  hasPlugin(pluginId: string): boolean;
}

const DEFAULT_RPC_TIMEOUT_MS = 30_000;

interface VmEntry {
  sandbox: MicrosandboxInstance;
  spec: PluginVmSpec;
}

/**
 * Production plugin runtime. One microsandbox microVM per installed
 * plugin. Each `callRpc` exec'd inside the VM is one-shot: the runner
 * script returns a single JSON response on stdout and exits. This
 * matches microsandbox 0.4.x's exec model without requiring
 * long-running stdio streams.
 *
 * Network policy is per-VM, decided at start time from the plugin's
 * manifest. Today the underlying SDK only exposes none / publicOnly /
 * allowAll, so a non-empty `requires_network` list translates to
 * publicOnly (best effort at the VM boundary); the operator-visible
 * declaration is the source of truth for trust.
 */
export class MicrosandboxRuntime implements PluginRuntime {
  private readonly vms = new Map<string, VmEntry>();

  constructor(private readonly options: MicrosandboxRuntimeOptions) {}

  hasPlugin(pluginId: string): boolean {
    return this.vms.has(pluginId);
  }

  async start(spec: PluginVmSpec): Promise<void> {
    if (this.vms.has(spec.id)) return;
    await ensureHostPath(spec.hostWorkspacePath);
    const sandbox = await this.createVm(spec);
    this.vms.set(spec.id, { sandbox, spec });
    this.log(`plugin VM ready: ${spec.id} (network=${spec.network})`);
  }

  async callRpc(
    pluginId: string,
    method: string,
    paramsJson: string,
    options: { timeoutMs?: number } = {},
  ): Promise<RpcResponse> {
    const entry = this.vms.get(pluginId);
    if (!entry) {
      throw new Error(`plugin VM not started: ${pluginId}`);
    }
    const timeoutMs = options.timeoutMs ?? DEFAULT_RPC_TIMEOUT_MS;
    const output = await this.execWithTimeout(
      entry.sandbox,
      "node",
      ["/workspace/run.js", method, paramsJson],
      timeoutMs,
    );
    if (output.code !== 0 && !output.stdout.trim()) {
      throw new Error(
        `plugin ${pluginId} RPC ${method} failed before producing JSON: ` +
          `exit=${output.code} stderr=${output.stderr.slice(0, 500)}`,
      );
    }
    return parseRpcStdout(output.stdout, pluginId, method);
  }

  async stop(pluginId: string): Promise<void> {
    const entry = this.vms.get(pluginId);
    if (!entry) return;
    this.vms.delete(pluginId);
    try {
      await stopSandbox(entry.sandbox);
    } catch (err) {
      this.log(`plugin VM stop error ${pluginId}: ${formatError(err)}`);
    }
  }

  async destroyAll(): Promise<void> {
    const ids = Array.from(this.vms.keys());
    await Promise.all(ids.map((id) => this.stop(id)));
  }

  private async createVm(spec: PluginVmSpec): Promise<MicrosandboxInstance> {
    const factory = this.factory();
    if (!factory?.builder) {
      throw new Error(
        "microsandbox SDK does not expose Sandbox.builder(...). " +
          "Install the `microsandbox` npm package.",
      );
    }
    try {
      let builder = factory
        .builder(spec.id)
        .image(this.options.image)
        .cpus(this.options.cpus)
        .memory(this.options.memoryMb)
        .replace();
      builder = applyBundledRuntime(builder);
      builder = applyNetwork(builder, spec.network, this.networkPolicy());
      builder = builder.volume("/workspace", (v) =>
        v.bind(spec.hostWorkspacePath),
      );
      return await builder.create();
    } catch (error) {
      throw new Error(
        `Failed to start plugin VM ${spec.id}: ${formatMicrosandboxStartError(error)}`,
      );
    }
  }

  private async execWithTimeout(
    sandbox: MicrosandboxInstance,
    cmd: string,
    args: string[],
    timeoutMs: number,
  ): Promise<{
    code: number;
    stdout: string;
    stderr: string;
    timedOut: boolean;
  }> {
    let timedOut = false;
    const timeout = new Promise<never>((_, reject) => {
      setTimeout(() => {
        timedOut = true;
        reject(new Error(`plugin RPC timed out after ${timeoutMs}ms`));
      }, timeoutMs).unref();
    });
    try {
      const output = await Promise.race([sandbox.exec(cmd, args), timeout]);
      return {
        code: output.code ?? 0,
        stdout: output.stdout(),
        stderr: output.stderr(),
        timedOut: false,
      };
    } catch (err) {
      if (!timedOut) throw err;
      return { code: 124, stdout: "", stderr: formatError(err), timedOut: true };
    }
  }

  private factory(): MicrosandboxFactory {
    if (this.options.sandboxFactory) return this.options.sandboxFactory;
    throw new Error(
      "MicrosandboxRuntime: no sandboxFactory injected. Pass " +
        "`Sandbox` from the `microsandbox` npm package in production.",
    );
  }

  private networkPolicy(): NetworkPolicyApi {
    if (this.options.networkPolicy) return this.options.networkPolicy;
    throw new Error(
      "MicrosandboxRuntime: no networkPolicy injected. Pass " +
        "`NetworkPolicy` from the `microsandbox` npm package in production.",
    );
  }

  private log(line: string): void {
    if (this.options.onLog) this.options.onLog(line);
    else console.log(`[plugin-runtime] ${line}`);
  }
}

function applyNetwork(
  builder: MicrosandboxBuilder,
  mode: PluginNetworkMode,
  api: NetworkPolicyApi,
): MicrosandboxBuilder {
  if (mode === "none") {
    return builder.network((n) => n.policy(api.none()));
  }
  return builder.network((n) => n.policy(api.publicOnly()));
}

async function ensureHostPath(hostPath: string): Promise<void> {
  await mkdir(hostPath, { recursive: true });
}

function parseRpcStdout(
  stdout: string,
  pluginId: string,
  method: string,
): RpcResponse {
  const trimmed = stdout.trim();
  if (!trimmed) {
    throw new Error(
      `plugin ${pluginId} RPC ${method} returned empty stdout`,
    );
  }
  // The plugin runner may emit logs alongside the JSON response; we keep
  // the contract simple by requiring the response to be the LAST line.
  const lastLine = trimmed.split("\n").pop() ?? "";
  let parsed: unknown;
  try {
    parsed = JSON.parse(lastLine);
  } catch (err) {
    throw new Error(
      `plugin ${pluginId} RPC ${method} returned non-JSON stdout: ` +
        `${formatError(err)}; got: ${lastLine.slice(0, 200)}`,
    );
  }
  const result = RpcResponse.safeParse(parsed);
  if (!result.success) {
    throw new Error(
      `plugin ${pluginId} RPC ${method} returned non-RpcResponse shape: ` +
        `${result.error.message}`,
    );
  }
  return result.data;
}

/**
 * Translates a manifest's declared network capability into the VM-level
 * network mode the runtime will enforce. Today: empty list → none,
 * any host → public. Per-host filtering is a documented v2 follow-up
 * that depends on richer microsandbox NetworkPolicy support.
 */
export function networkModeFor(declaredHosts: readonly string[]): PluginNetworkMode {
  return declaredHosts.length === 0 ? "none" : "public";
}

export {
  applyBundledRuntime,
  isSupportedHost,
  platformTriple,
  type MicrosandboxFactory,
  type NetworkPolicyApi,
} from "./microsandbox-sdk.js";
