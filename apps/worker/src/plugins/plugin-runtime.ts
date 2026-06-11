import { RpcResponse } from "@open-neko/plugin-types";

/**
 * Runtime-agnostic plugin-runtime contract (SEC9). OpenShell is the
 * production runtime; SubprocessRuntime exists for plugin-author dev
 * mode. The former microsandbox runtime is gone — these abstractions
 * are what every runtime implements.
 */

/** Network policy translation for a plugin manifest's declared hosts. */
export type PluginNetworkMode = "none" | "public";

export interface PluginVmSpec {
  /** Unique handle for this VM, derived from the plugin npm name. */
  id: string;
  /** Path on host that the runtime mounts at /workspace in the VM. */
  hostWorkspacePath: string;
  /** Network policy derived from the plugin manifest's `requires_network`. */
  network: PluginNetworkMode;
  /**
   * Raw declared hosts from the manifest. OpenShellRuntime uses them
   * for per-host egress.
   */
  hosts?: readonly string[];
}

export interface PluginRuntime {
  start(spec: PluginVmSpec): Promise<void>;
  /**
   * Invokes the plugin's runner with `node /workspace/run.js <method>
   * <paramsJson>` and parses the single JSON response from stdout.
   * If `env` is provided, values are injected into the VM at exec time
   * via a shell wrapper.
   */
  callRpc(
    pluginId: string,
    method: string,
    paramsJson: string,
    options?: { timeoutMs?: number; env?: Record<string, string> },
  ): Promise<RpcResponse>;
  stop(pluginId: string): Promise<void>;
  destroyAll(): Promise<void>;
  hasPlugin(pluginId: string): boolean;
}

/**
 * Translates a manifest's declared network capability into the VM-level
 * network mode the runtime will enforce. Today: empty list → none,
 * any host → public. OpenShell additionally applies per-host egress
 * from `hosts`.
 */
export function networkModeFor(declaredHosts: readonly string[]): PluginNetworkMode {
  return declaredHosts.length === 0 ? "none" : "public";
}

/**
 * POSIX shell single-quote escape: wrap the value in single quotes and
 * replace any embedded single quote with `'\''`. Safe for all bytes.
 */
function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

const ENV_KEY_RX = /^[A-Z][A-Z0-9_]*$/;

/**
 * Returns the command + argv the runtime will invoke inside the VM.
 * Without env: a plain `node` exec, fastest path. With env: wrap in
 * `sh -c '<exports>; exec node ...'` so the env is bound for the
 * plugin process.
 *
 * Why `sh` not `bash`: the default plugin VM image ships `ash` at
 * `/bin/sh` and no `bash`. POSIX `sh` is enough for our needs (export +
 * exec + single-quote-escaped values).
 *
 * Env keys are validated to UPPER_SNAKE_CASE before they reach the
 * shell so a bad manifest entry can't smuggle a command-injection
 * substring like `FOO; rm -rf /`. Values are POSIX-quoted.
 */
export function buildExecCommand(
  method: string,
  paramsJson: string,
  env: Record<string, string>,
  runnerPath = "/workspace/run.js",
): { cmd: string; args: string[] } {
  const keys = Object.keys(env);
  if (keys.length === 0) {
    return {
      cmd: "node",
      args: [runnerPath, method, paramsJson],
    };
  }
  for (const k of keys) {
    if (!ENV_KEY_RX.test(k)) {
      throw new Error(
        `plugin env key "${k}" is not a valid UPPER_SNAKE_CASE name`,
      );
    }
  }
  const exports = keys
    .map((k) => `export ${k}=${shellQuote(env[k] ?? "")}`)
    .join("; ");
  const inner =
    `${exports}; exec node ${runnerPath} ` +
    `${shellQuote(method)} ${shellQuote(paramsJson)}`;
  return { cmd: "sh", args: ["-c", inner] };
}
