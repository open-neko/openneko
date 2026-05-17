import { spawn } from "node:child_process";
import { RpcResponse } from "@open-neko/plugin-types";
import type {
  PluginRuntime,
  PluginVmSpec,
} from "./microsandbox-runtime.js";

/**
 * Plugin-author dev-mode runtime — runs `node <workspace>/run.js method
 * params` as a plain host subprocess, with no isolation. NOT for
 * operator-facing installs. See memory/feedback_plugins_sandbox_gate.md
 * for the operator policy.
 *
 * Used by:
 * - Integration tests in this repo, to exercise the full loader path
 *   without spinning up a microVM.
 * - The `openneko plugin dev` developer command (future) for hot-reload
 *   loops during plugin authoring.
 *
 * Has the same `PluginRuntime` interface as MicrosandboxRuntime so the
 * loader doesn't care which one it gets.
 */
export interface SubprocessRuntimeOptions {
  /**
   * Env vars to inject into every plugin subprocess. The future
   * microsandbox runtime will gain a parallel mechanism via the
   * manifest's `env` field; matching it here keeps tests fair.
   */
  env?: Record<string, string>;
  onLog?: (line: string) => void;
}

interface Entry {
  spec: PluginVmSpec;
}

const DEFAULT_TIMEOUT_MS = 30_000;

export class SubprocessRuntime implements PluginRuntime {
  private readonly entries = new Map<string, Entry>();

  constructor(private readonly options: SubprocessRuntimeOptions = {}) {}

  hasPlugin(id: string): boolean {
    return this.entries.has(id);
  }

  async start(spec: PluginVmSpec): Promise<void> {
    this.entries.set(spec.id, { spec });
  }

  async callRpc(
    pluginId: string,
    method: string,
    paramsJson: string,
    options: { timeoutMs?: number; env?: Record<string, string> } = {},
  ): Promise<RpcResponse> {
    const entry = this.entries.get(pluginId);
    if (!entry) {
      throw new Error(`SubprocessRuntime: plugin not started: ${pluginId}`);
    }
    const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    // Per-call env layers over the runtime-level default; per-call wins.
    const mergedEnv = { ...(this.options.env ?? {}), ...(options.env ?? {}) };
    const stdout = await runProcessOnce(
      "node",
      [`${entry.spec.hostWorkspacePath}/run.js`, method, paramsJson],
      { env: mergedEnv, timeoutMs },
    );
    return parseRpcLastLine(stdout, pluginId, method);
  }

  async stop(pluginId: string): Promise<void> {
    this.entries.delete(pluginId);
  }

  async destroyAll(): Promise<void> {
    this.entries.clear();
  }
}

interface SpawnOpts {
  env?: Record<string, string>;
  timeoutMs: number;
}

function runProcessOnce(
  cmd: string,
  args: string[],
  opts: SpawnOpts,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, {
      stdio: ["ignore", "pipe", "pipe"],
      env: {
        ...process.env,
        ...(opts.env ?? {}),
      },
    });
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
      reject(new Error(`subprocess timeout after ${opts.timeoutMs}ms`));
    }, opts.timeoutMs);
    timer.unref();
    child.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      if (code !== 0 && !stdout.trim()) {
        reject(
          new Error(
            `subprocess exited ${code} with empty stdout; stderr=${stderr.slice(0, 500)}`,
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
      `plugin ${pluginId} RPC ${method} returned non-JSON: ${
        err instanceof Error ? err.message : String(err)
      }; got: ${lastLine.slice(0, 200)}`,
    );
  }
  const result = RpcResponse.safeParse(parsed);
  if (!result.success) {
    throw new Error(
      `plugin ${pluginId} RPC ${method} returned non-RpcResponse: ${result.error.message}`,
    );
  }
  return result.data;
}
