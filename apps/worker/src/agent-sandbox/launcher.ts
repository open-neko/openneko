import { spawn } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { createInterface } from "node:readline";
import type { AgentEvent, AgentRunResult } from "@neko/llm";
import type { RunAgentBackendInput } from "@neko/llm/work";
import { EVENT_MARKER, RESULT_MARKER } from "./protocol";

/** Path of the agent entrypoint inside the agent image (the `agent` Docker stage). */
const AGENT_ENTRY = "/app/apps/worker/src/agent-sandbox/entry.ts";
const SANDBOX_JOB_PATH = "/sandbox/job.json";

export interface SandboxLauncherOptions {
  /** `openshell` binary; default resolves from PATH. */
  cli?: string;
  /** Registered gateway name (mTLS) — preferred; else gatewayEndpoint; else CLI default. */
  gatewayName?: string;
  gatewayEndpoint?: string;
  /** Agent image (the Dockerfile `agent` stage), e.g. ghcr.io/open-neko/agent:<ver>. */
  agentImage: string;
  /** OpenShell provider holding the model key — the proxy injects it; never in the box. */
  modelProvider?: string;
  /** Model endpoint egress, per (host, binary) — scoped to the backend's connecting binary. */
  modelEgress?: ReadonlyArray<{ host: string; binary: string }>;
  /** Extra env exported into the exec sh-wrapper (e.g. HERMES_HOME). Values must be safe. */
  env?: Record<string, string>;
  /** Broker coords for the claude MCP-tool path (omitted for hermes). */
  brokerUrl?: string;
  brokerTokenFor?: (runId: string) => string;
  execTimeoutMs?: number;
  onLog?: (line: string) => void;
}

type RunCore = (input: RunAgentBackendInput) => Promise<AgentRunResult>;

const SHELL_KEY_RX = /^[A-Z_][A-Z0-9_]*$/;

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

/**
 * Build the `runCore` the launcher injects into runChatTurn for
 * OPENNEKO_AGENT_RUNTIME=openshell: run the agent loop in an OpenShell sandbox.
 * The host-side prologue (prompt build) and epilogue (fence handling +
 * persistence) stay in runChatTurn around this call.
 */
export function makeSandboxRunCore(opts: SandboxLauncherOptions): RunCore {
  const cli = opts.cli ?? "openshell";
  const log = opts.onLog ?? ((l: string) => console.log(`[agent-sandbox] ${l}`));

  const gatewayArgs = opts.gatewayName
    ? ["--gateway", opts.gatewayName]
    : opts.gatewayEndpoint
      ? ["--gateway-endpoint", opts.gatewayEndpoint]
      : [];

  const run = (args: string[], timeoutMs: number): Promise<string> =>
    runProcessOnce(cli, [...gatewayArgs, ...args], timeoutMs);

  return async function sandboxRunCore(
    input: RunAgentBackendInput,
  ): Promise<AgentRunResult> {
    const name = `work-${input.runId}`.toLowerCase().replace(/[^a-z0-9-]/g, "").slice(0, 60);
    const job = {
      orgId: input.orgId,
      threadId: input.threadId,
      runId: input.runId,
      message: input.userMessage,
      prompt: input.prompt,
      backendId: input.backend.id,
      backendState: input.backendState,
      pluginActions: input.pluginActions,
      workspace: input.workspace,
    };

    const stageDir = await mkdtemp(path.join(tmpdir(), "oss-agent-"));
    const jobFile = path.join(stageDir, "job.json");
    await writeFile(jobFile, JSON.stringify(job));

    try {
      await run(
        [
          "sandbox",
          "create",
          "--name",
          name,
          "--from",
          opts.agentImage,
          "--no-tty",
          "--no-auto-providers",
          ...(opts.modelProvider ? ["--provider", opts.modelProvider] : []),
          "--",
          "node",
          "--version",
        ],
        180_000,
      );

      const policyArgs = buildModelEgressArgs(name, opts.modelEgress ?? []);
      if (policyArgs) await run(policyArgs, 75_000);

      // Workspace (skills/knowledge/bin/uploads) at the same paths the prompt
      // + backend expect, then the job descriptor.
      await run(
        ["sandbox", "upload", name, input.workspace.orgRoot, input.workspace.orgRoot],
        180_000,
      );
      await run(["sandbox", "upload", name, jobFile, SANDBOX_JOB_PATH], 30_000);

      log(`agent sandbox ready: ${name} (backend=${input.backend.id})`);
      return await execAndStream(
        cli,
        gatewayArgs,
        name,
        buildInnerCommand({
          env: {
            OPENNEKO_RUN_JOB_FILE: SANDBOX_JOB_PATH,
            ...(opts.brokerUrl ? { OPENNEKO_BROKER_URL: opts.brokerUrl } : {}),
            ...(opts.brokerUrl && opts.brokerTokenFor
              ? { OPENNEKO_BROKER_TOKEN: opts.brokerTokenFor(input.runId) }
              : {}),
            ...(opts.env ?? {}),
          },
        }),
        input.emit,
        opts.execTimeoutMs ?? 600_000,
      );
    } finally {
      await run(["sandbox", "delete", name], 60_000).catch(() => {});
      await rm(stageDir, { recursive: true, force: true });
    }
  };
}

/** `policy update` adding the model endpoint(s) scoped to the backend binary. */
export function buildModelEgressArgs(
  name: string,
  egress: ReadonlyArray<{ host: string; binary: string }>,
): string[] | null {
  if (egress.length === 0) return null;
  const args = ["policy", "update", name];
  for (const { host } of egress) {
    args.push("--add-endpoint", `${host}:443:read-write:rest:enforce`);
  }
  for (const { binary } of egress) args.push("--binary", binary);
  for (const { host } of egress) args.push("--add-allow", `${host}:443:*:/**`);
  args.push("--wait", "--timeout", "60");
  return args;
}

function buildInnerCommand(o: { env: Record<string, string> }): string {
  const exports = Object.entries(o.env)
    .map(([k, v]) => {
      if (!SHELL_KEY_RX.test(k)) throw new Error(`bad env key ${k}`);
      return `export ${k}=${shellQuote(v)}`;
    })
    .join("; ");
  return `${exports}; exec node --import tsx/esm ${AGENT_ENTRY}`;
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
    child.stdout.on("data", (d: Buffer) => (stdout += d.toString("utf8")));
    child.stderr.on("data", (d: Buffer) => (stderr += d.toString("utf8")));
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
      if (code !== 0 && !stdout.trim()) {
        reject(
          new Error(
            `openshell ${args.join(" ").slice(0, 120)} exited ${code}; stderr=${stderr.slice(0, 400)}`,
          ),
        );
        return;
      }
      resolve(stdout);
    });
  });
}

/**
 * exec entry.ts and stream its tagged stdout: EVENT_MARKER lines → emit (host
 * scrubs + persists → web UI), the RESULT_MARKER line → the AgentRunResult.
 */
function execAndStream(
  cli: string,
  gatewayArgs: string[],
  name: string,
  innerCmd: string,
  emit: (event: AgentEvent) => Promise<void>,
  timeoutMs: number,
): Promise<AgentRunResult> {
  return new Promise((resolve, reject) => {
    const timeoutSec = Math.max(1, Math.ceil(timeoutMs / 1000));
    const child = spawn(
      cli,
      [
        ...gatewayArgs,
        "sandbox",
        "exec",
        "-n",
        name,
        "--no-tty",
        "--timeout",
        String(timeoutSec),
        "--",
        "sh",
        "-c",
        innerCmd,
      ],
      { stdio: ["ignore", "pipe", "pipe"] },
    );
    let result: AgentRunResult | undefined;
    let stderr = "";
    const rl = createInterface({ input: child.stdout });
    rl.on("line", (line: string) => {
      const ev = line.indexOf(EVENT_MARKER);
      if (ev >= 0) {
        try {
          void emit(JSON.parse(line.slice(ev + EVENT_MARKER.length)) as AgentEvent);
        } catch {
          /* ignore a partial/garbled event line */
        }
        return;
      }
      const rs = line.indexOf(RESULT_MARKER);
      if (rs >= 0) {
        try {
          result = JSON.parse(line.slice(rs + RESULT_MARKER.length)) as AgentRunResult;
        } catch {
          /* ignore */
        }
      }
    });
    child.stderr.on("data", (d: Buffer) => (stderr += d.toString("utf8")));
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`agent sandbox exec timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    timer.unref();
    child.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      if (result) return resolve(result);
      reject(
        new Error(
          `agent sandbox exited ${code} without a result line; stderr=${stderr.slice(0, 500)}`,
        ),
      );
    });
  });
}
