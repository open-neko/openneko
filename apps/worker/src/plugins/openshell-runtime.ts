import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { RpcResponse } from "@open-neko/plugin-types";
import {
  buildEnvFile,
  buildExecCommand,
  type PluginRuntime,
  type PluginVmSpec,
} from "./plugin-runtime.js";

/**
 * Plugin runtime backed by the `openshell` CLI. One OpenShell sandbox per
 * installed plugin, with a one-shot exec model:
 * `start` boots a Ready sandbox from the shared base image and uploads the
 * plugin's `run.js`; each `callRpc` exec's the runner and parses the single
 * JSON response from stdout.
 *
 * The runner lives at /sandbox/run.js (the sandbox user's home), not
 * /workspace — OpenShell has no host bind-mount, so the bundle is uploaded.
 * Secrets ride the same `sh -c 'export K=V; exec node …'` wrapper as
 * a shell wrapper (OpenShell exec has no --env), produced by buildExecCommand.
 */
const PLUGIN_RUNNER_PATH = "/sandbox/run.js";
// Box-class secret env (manifest `inject:"box"`) is uploaded here (out of band)
// and sourced at exec — never inlined into the exec command (the gateway logs
// it). Egress secrets never reach this file; they are provider-injected.
const PLUGIN_ENV_FILE_PATH = "/sandbox/.plugin-env";
// Egress secrets are held gateway-side as a per-plugin OpenShell provider whose
// credential slots (c0, c1, …) the box receives as placeholders — one per
// declared `inject:"egress"` key, aliased to the key at exec; the proxy swaps in
// the real value on the wire. The value never enters the box.
const pluginProviderName = (pluginId: string): string => `plugin-${pluginId}`;
const pluginSecretProfileId = (pluginId: string): string => `openneko-plugin-${pluginId}`;
const credentialSlot = (i: number): string => `c${i}`;

/** A per-plugin provider profile with one credential slot per egress secret. */
function pluginSecretProfileYaml(profileId: string, count: number): string {
  const cred = (i: number): string =>
    [
      `- name: ${credentialSlot(i)}`,
      `  description: plugin egress credential ${i}`,
      `  env_vars:`,
      `  - C${i}`,
      `  required: true`,
      `  auth_style: query`,
      `  header_name: ''`,
      `  query_param: key`,
    ].join("\n");
  return [
    `id: ${profileId}`,
    `display_name: OpenNeko Plugin Secret`,
    `description: Plugin egress credentials (proxy-substituted; the box holds only placeholders)`,
    `category: agent`,
    `credentials:`,
    Array.from({ length: count }, (_, i) => cred(i)).join("\n"),
    `endpoints: []`,
    `binaries: []`,
    `inference_capable: false`,
    `discovery:`,
    `  credentials:`,
    Array.from({ length: count }, (_, i) => `  - ${credentialSlot(i)}`).join("\n"),
    ``,
  ].join("\n");
}
const DEFAULT_RPC_TIMEOUT_MS = 30_000;
const CREATE_TIMEOUT_MS = 180_000;
const UPLOAD_TIMEOUT_MS = 60_000;
const DELETE_TIMEOUT_MS = 60_000;
const POLICY_LOAD_TIMEOUT_S = 60;
const EGRESS_PORT = 443;
// The plugin's node interpreter opens the egress connection; OpenShell's proxy
// authorizes by ABSOLUTE binary path, and the plugin-base image (node:24-slim)
// ships node here. Bare "node" does not match → the CONNECT is denied (403).
const PLUGIN_NODE_BINARY = "/usr/local/bin/node";

function pluginSandboxName(id: string): string {
  if (id.length <= 19 && /^[a-z0-9][a-z0-9-]*$/u.test(id)) return id;
  return `neko-x-${createHash("sha256").update(id).digest("hex").slice(0, 12)}`;
}

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
  sandboxName: string;
  /** sha256 of the last env file uploaded to this sandbox — skip re-upload when unchanged. */
  envHash?: string;
  /** Manifest `inject:"egress"` keys — provider-injected, aliased from the
   *  placeholder at exec, and kept out of the uploaded env file. */
  egressKeys?: readonly string[];
  /** Gateway-side provider registered for this plugin's egress secret (for cleanup on stop). */
  providerName?: string;
  /** sha256 of the egress values last pushed to the provider — when it changes
   *  (token rotation) we refresh the gateway-side value, not the box. */
  egressValueHash?: string;
}

export class OpenShellRuntime implements PluginRuntime {
  private readonly entries = new Map<string, Entry>();

  constructor(private readonly options: OpenShellRuntimeOptions) {}

  hasPlugin(id: string): boolean {
    return this.entries.has(id);
  }

  async start(spec: PluginVmSpec): Promise<void> {
    if (this.entries.has(spec.id)) return;
    const sandboxName = pluginSandboxName(spec.id);
    const egress = spec.egressSecrets ?? [];
    // Hold the egress secrets gateway-side first (one credential slot each), so
    // the box that references them only ever sees the placeholders.
    const providerName = egress.length > 0 ? pluginProviderName(spec.id) : undefined;
    if (providerName) {
      await this.ensurePluginProvider(spec.id, providerName, egress.map((e) => e.value));
    }
    try {
      await this.createSandbox(spec, providerName);
    } catch (err) {
      // A failed or orphaned start (image pull error, worker restart) leaves
      // the name registered on the gateway, so every retry collides forever —
      // replace the stale sandbox instead.
      if (!formatError(err).includes("already exists")) throw err;
      await this.run(["sandbox", "delete", sandboxName], DELETE_TIMEOUT_MS);
      await this.createSandbox(spec, providerName);
    }
    await this.run([
      "sandbox",
      "upload",
      sandboxName,
      `${this.options.bundleDir}/${spec.id}/run.js`,
      PLUGIN_RUNNER_PATH,
    ], UPLOAD_TIMEOUT_MS);
    for (const command of buildPolicyUpdateCommands(sandboxName, spec.hosts ?? [])) {
      await this.run(command, (POLICY_LOAD_TIMEOUT_S + 15) * 1000);
    }
    this.entries.set(spec.id, {
      spec,
      sandboxName,
      ...(egress.length > 0
        ? { egressKeys: egress.map((e) => e.key), egressValueHash: hashEgress(egress) }
        : {}),
      ...(providerName ? { providerName } : {}),
    });
    this.log(
      `plugin sandbox ready: ${spec.id} ` +
        `(hosts=${(spec.hosts ?? []).join(",") || "none"}, ` +
        `egress=${egress.map((e) => e.key).join(",") || "none"})`,
    );
  }

  /**
   * Register (or refresh) the gateway-side provider holding this plugin's egress
   * secrets — one credential slot per value. Idempotent: imports the per-plugin
   * profile, then creates-or-updates the provider. The values ride the create
   * command host→gateway (the trusted control plane), never into a sandbox.
   */
  private async ensurePluginProvider(
    pluginId: string,
    providerName: string,
    values: readonly string[],
  ): Promise<void> {
    const profileId = pluginSecretProfileId(pluginId);
    const dir = await mkdtemp(path.join(tmpdir(), "oss-plugin-profile-"));
    try {
      const file = path.join(dir, `${profileId}.yaml`);
      await writeFile(file, pluginSecretProfileYaml(profileId, values.length), { mode: 0o600 });
      await this.run(["provider", "profile", "import", "--file", file], UPLOAD_TIMEOUT_MS).catch(
        () => {},
      );
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
    const credArgs = values.flatMap((v, i) => ["--credential", `${credentialSlot(i)}=${v}`]);
    await this.run(
      ["provider", "create", "--name", providerName, "--type", profileId, ...credArgs],
      DELETE_TIMEOUT_MS,
    ).catch(() => this.run(["provider", "update", providerName, ...credArgs], DELETE_TIMEOUT_MS));
  }

  private createSandbox(spec: PluginVmSpec, providerName?: string): Promise<string> {
    // OpenShell 0.1.x completes a sandbox when its main process exits. Keep
    // the canonical process alive until stop() deletes the sandbox.
    return this.run([
      "sandbox",
      "create",
      "--name",
      pluginSandboxName(spec.id),
      "--from",
      this.options.image,
      "--detach",
      "--no-tty",
      "--no-auto-providers",
      ...(providerName ? ["--provider", providerName] : []),
      "--",
      "sleep",
      "infinity",
    ], CREATE_TIMEOUT_MS);
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
    const entry = this.entries.get(pluginId);
    const egressKeys = entry?.egressKeys ?? [];
    // Rotation: if the egress value changed since the VM started, refresh the
    // gateway-side provider. The box keeps the same placeholder; the proxy just
    // resolves the new value — no re-upload, no VM restart.
    if (entry?.providerName && egressKeys.length > 0) {
      const values = egressKeys.map((k) => (options.env ?? {})[k] ?? "");
      const hash = hashEgress(egressKeys.map((k, i) => ({ key: k, value: values[i] ?? "" })));
      if (hash !== entry.egressValueHash) {
        await this.ensurePluginProvider(pluginId, entry.providerName, values);
        entry.egressValueHash = hash;
      }
    }
    // Egress secrets are provider-injected — the box holds only the placeholder.
    // Keep their values out of the uploaded env file; box-class secrets still
    // ride the file (cached by hash, sourced at exec, never on the command line).
    const boxEnv = Object.fromEntries(
      Object.entries(options.env ?? {}).filter(([k]) => !egressKeys.includes(k)),
    );
    const hasBoxEnv = Object.keys(boxEnv).length > 0;
    if (hasBoxEnv) await this.ensureEnvFile(pluginId, boxEnv);
    // Alias each provider-injected placeholder ($c0, $c1, …) to the env var the
    // plugin reads (e.g. TELEGRAM_BOT_TOKEN) — a reference, never a value.
    const aliases = egressKeys.map((to, i) => ({ from: credentialSlot(i), to }));
    const { cmd, args } = buildExecCommand(method, paramsJson, {
      runnerPath: PLUGIN_RUNNER_PATH,
      ...(hasBoxEnv ? { envFilePath: PLUGIN_ENV_FILE_PATH } : {}),
      ...(aliases.length ? { aliases } : {}),
    });
    const timeoutSec = Math.max(1, Math.ceil(timeoutMs / 1000));
    // Gateway-side --timeout bounds the remote command; the host-side
    // spawn timeout is a slightly longer backstop for a hung CLI.
    const stdout = await this.run(
      [
        "sandbox",
        "exec",
        "-n",
        entry!.sandboxName,
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

  /**
   * Write the plugin's secret env to a host temp file (mode 0600), upload it to
   * the sandbox, and remember its hash so an unchanged env skips the round-trip.
   * The upload command carries the temp PATH + dest path, never the values, so
   * the secret never reaches the gateway's command log.
   */
  private async ensureEnvFile(pluginId: string, env: Record<string, string>): Promise<void> {
    const entry = this.entries.get(pluginId);
    if (!entry) return;
    const content = buildEnvFile(env);
    const hash = createHash("sha256").update(content).digest("hex");
    if (entry.envHash === hash) return;
    const dir = await mkdtemp(path.join(tmpdir(), "oss-plugin-env-"));
    try {
      const file = path.join(dir, "plugin-env");
      await writeFile(file, content, { mode: 0o600 });
      await this.run(
        ["sandbox", "upload", entry.sandboxName, file, PLUGIN_ENV_FILE_PATH],
        UPLOAD_TIMEOUT_MS,
      );
      entry.envHash = hash;
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  }

  async stop(pluginId: string): Promise<void> {
    const entry = this.entries.get(pluginId);
    if (!entry) return;
    this.entries.delete(pluginId);
    try {
      await this.run(["sandbox", "delete", entry.sandboxName], DELETE_TIMEOUT_MS);
    } catch (err) {
      this.log(`plugin sandbox stop error ${pluginId}: ${formatError(err)}`);
    }
    // Drop the gateway-side egress-secret provider too (best-effort).
    if (entry.providerName) {
      await this.run(["provider", "delete", entry.providerName], DELETE_TIMEOUT_MS).catch(() => {});
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
 * Per-host egress, scoped to the plugin's node interpreter (the process that
 * actually opens the connection) by ABSOLUTE path — the proxy matches the full
 * binary path, so bare "node" is denied. Empty host list → no rules added,
 * leaving the sandbox's inherited default-deny in place.
 */
export function buildPolicyUpdateCommands(
  id: string,
  hosts: readonly string[],
): string[][] {
  // OpenShell 0.1.x requires endpoint and L7 allow updates separately. Each
  // allow update names the exact endpoint rule and its complete binary list.
  return hosts.flatMap((host) => {
    const rule = `plugin_${createHash("sha256").update(host).digest("hex").slice(0, 12)}`;
    const base = ["policy", "update", id, "--rule-name", rule, "--binary", PLUGIN_NODE_BINARY];
    const wait = ["--wait", "--timeout", String(POLICY_LOAD_TIMEOUT_S)];
    return [
      [...base, "--add-endpoint", `${host}:${EGRESS_PORT}:read-write:rest:enforce`, ...wait],
      [...base, "--add-allow", `${host}:${EGRESS_PORT}:*:/**`, ...wait],
    ];
  });
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
      // stdout as a hard failure.
      if (code !== 0 && !stdout.trim()) {
        // Redact any `--credential <slot>=<value>` so a failed provider command
        // can't leak the secret into console logs.
        const shown = args
          .map((a, i) => (args[i - 1] === "--credential" ? a.replace(/=.*/su, "=[redacted]") : a))
          .join(" ");
        reject(
          new Error(`openshell ${shown.slice(0, 160)} exited ${code}; stderr=${stderr.slice(0, 500)}`),
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

function hashEgress(
  secrets: ReadonlyArray<{ key: string; value: string }>,
): string {
  return createHash("sha256").update(JSON.stringify(secrets)).digest("hex");
}
