import { spawn } from "node:child_process";
import { RpcResponse } from "@open-neko/plugin-types";
import {
  buildExecCommand,
  type PluginRuntime,
  type PluginVmSpec,
} from "./microsandbox-runtime.js";

/**
 * Plugin runtime backed by the `openshell` CLI. One OpenShell sandbox per
 * installed plugin, mirroring MicrosandboxRuntime's one-shot exec model:
 * `start` boots a Ready sandbox from the shared base image and uploads the
 * plugin's `run.js`; each `callRpc` exec's the runner and parses the single
 * JSON response from stdout.
 *
 * The runner lives at /sandbox/run.js (the sandbox user's home), not
 * /workspace — OpenShell has no host bind-mount, so the bundle is uploaded.
 * Secrets ride the same `sh -c 'export K=V; exec node …'` wrapper as
 * microsandbox (OpenShell exec has no --env), produced by buildExecCommand.
 */
const PLUGIN_RUNNER_PATH = "/sandbox/run.js";
const DEFAULT_RPC_TIMEOUT_MS = 30_000;
const CREATE_TIMEOUT_MS = 180_000;
const UPLOAD_TIMEOUT_MS = 60_000;
const DELETE_TIMEOUT_MS = 60_000;
const POLICY_LOAD_TIMEOUT_S = 60;
const EGRESS_PORT = 443;

export interface OpenShellRuntimeOptions {
  /** Shared lean base image: node + iproute2/nftables + a `sandbox` user. */
  image: string;
  /** `openshell` binary; defaults to resolving from PATH. */
  cli?: string;
  /**
   * Registered gateway name (`--gateway <name>`). Required for the mTLS
   * path: the CLI loads the client cert from its gateway config dir. Takes
   * precedence over gatewayEndpoint.
   */
  gatewayName?: string;
  /**
   * Direct gateway endpoint (`--gateway-endpoint <url>`). For https this is
   * edge/OIDC auth, NOT mTLS client certs — use gatewayName for mTLS. When
   * neither is set the CLI's active/OPENSHELL_GATEWAY gateway drives it.
   */
  gatewayEndpoint?: string;
  /** Host dir holding `<id>/run.js` to upload (PluginRegistry.workRoot). */
  bundleDir: string;
  onLog?: (line: string) => void;
}

interface Entry {
  spec: PluginVmSpec;
}

export class OpenShellRuntime implements PluginRuntime {
  private readonly entries = new Map<string, Entry>();

  constructor(private readonly options: OpenShellRuntimeOptions) {}

  hasPlugin(id: string): boolean {
    return this.entries.has(id);
  }

  async start(spec: PluginVmSpec): Promise<void> {
    if (this.entries.has(spec.id)) return;
    // `-- node --version` is a cheap initial command; the supervisor
    // replaces it and (without --no-keep) the sandbox stays Ready.
    await this.run([
      "sandbox",
      "create",
      "--name",
      spec.id,
      "--from",
      this.options.image,
      "--no-tty",
      "--no-auto-providers",
      "--",
      "node",
      "--version",
    ], CREATE_TIMEOUT_MS);
    await this.run([
      "sandbox",
      "upload",
      spec.id,
      `${this.options.bundleDir}/${spec.id}/run.js`,
      PLUGIN_RUNNER_PATH,
    ], UPLOAD_TIMEOUT_MS);
    const policy = buildPolicyUpdateArgs(spec.id, spec.hosts ?? []);
    if (policy) await this.run(policy, (POLICY_LOAD_TIMEOUT_S + 15) * 1000);
    this.entries.set(spec.id, { spec });
    this.log(
      `plugin sandbox ready: ${spec.id} ` +
        `(hosts=${(spec.hosts ?? []).join(",") || "none"})`,
    );
  }

  async callRpc(
    pluginId: string,
    method: string,
    paramsJson: string,
    options: { timeoutMs?: number; env?: Record<string, string> } = {},
  ): Promise<RpcResponse> {
    if (!this.entries.has(pluginId)) {
      throw new Error(`OpenShellRuntime: plugin not started: ${pluginId}`);
    }
    const timeoutMs = options.timeoutMs ?? DEFAULT_RPC_TIMEOUT_MS;
    const { cmd, args } = buildExecCommand(
      method,
      paramsJson,
      options.env ?? {},
      PLUGIN_RUNNER_PATH,
    );
    const timeoutSec = Math.max(1, Math.ceil(timeoutMs / 1000));
    // Gateway-side --timeout bounds the remote command; the host-side
    // spawn timeout is a slightly longer backstop for a hung CLI.
    const stdout = await this.run(
      [
        "sandbox",
        "exec",
        "-n",
        pluginId,
        "--no-tty",
        "--timeout",
        String(timeoutSec),
        "--",
        cmd,
        ...args,
      ],
      timeoutMs + 5_000,
    );
    return parseRpcLastLine(stdout, pluginId, method);
  }

  async stop(pluginId: string): Promise<void> {
    if (!this.entries.has(pluginId)) return;
    this.entries.delete(pluginId);
    try {
      await this.run(["sandbox", "delete", pluginId], DELETE_TIMEOUT_MS);
    } catch (err) {
      this.log(`plugin sandbox stop error ${pluginId}: ${formatError(err)}`);
    }
  }

  async destroyAll(): Promise<void> {
    const ids = Array.from(this.entries.keys());
    await Promise.all(ids.map((id) => this.stop(id)));
  }

  private run(args: string[], timeoutMs: number): Promise<string> {
    const cli = this.options.cli ?? "openshell";
    return runProcessOnce(cli, [...this.gatewayArgs(), ...args], timeoutMs);
  }

  private gatewayArgs(): string[] {
    if (this.options.gatewayName) return ["--gateway", this.options.gatewayName];
    if (this.options.gatewayEndpoint)
      return ["--gateway-endpoint", this.options.gatewayEndpoint];
    return [];
  }

  private log(line: string): void {
    if (this.options.onLog) this.options.onLog(line);
    else console.log(`[plugin-runtime] ${line}`);
  }
}

/**
 * Per-host egress, scoped to the `node` binary (the plugin's interpreter —
 * the process that actually opens the connection). Empty host list → no
 * rules added, leaving the sandbox's inherited default-deny in place.
 */
export function buildPolicyUpdateArgs(
  id: string,
  hosts: readonly string[],
): string[] | null {
  if (hosts.length === 0) return null;
  const args = ["policy", "update", id];
  for (const host of hosts) {
    args.push("--add-endpoint", `${host}:${EGRESS_PORT}:read-write:rest:enforce`);
  }
  args.push("--binary", "node");
  for (const host of hosts) {
    args.push("--add-allow", `${host}:${EGRESS_PORT}:*:/**`);
  }
  args.push("--wait", "--timeout", String(POLICY_LOAD_TIMEOUT_S));
  return args;
}

function runProcessOnce(
  cmd: string,
  args: string[],
  timeoutMs: number,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d: Buffer) => {
      stdout += d.toString("utf8");
    });
    child.stderr.on("data", (d: Buffer) => {
      stderr += d.toString("utf8");
    });
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`openshell ${args[0] ?? ""} timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    timer.unref();
    child.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      // exec exits with the remote command's code: a plugin that returns an
      // RpcErr exits non-zero but still prints JSON, so only treat an empty
      // stdout as a hard failure (matches MicrosandboxRuntime).
      if (code !== 0 && !stdout.trim()) {
        reject(
          new Error(
            `openshell ${args.join(" ").slice(0, 120)} exited ${code}; ` +
              `stderr=${stderr.slice(0, 500)}`,
          ),
        );
        return;
      }
      resolve(stdout);
    });
  });
}

function parseRpcLastLine(
  stdout: string,
  pluginId: string,
  method: string,
): RpcResponse {
  const trimmed = stdout.trim();
  if (!trimmed) {
    throw new Error(`plugin ${pluginId} RPC ${method} returned empty stdout`);
  }
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

function formatError(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
