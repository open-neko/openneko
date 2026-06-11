import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { createInterface } from "node:readline";
import type { AgentEvent, AgentRunResult } from "../agent-backend";
import type { RunAgentBackendInput } from "./agent-core";
import type { RunChatTurnDeps } from "./run-chat-turn";

// Wire protocol shared with the in-image entrypoint. The agent runs in a
// separate container, so these can't share a module at runtime — they MUST
// stay identical to apps/worker/src/agent-sandbox/protocol.ts (a unit test
// asserts that). The launcher greps the exec's stdout for these markers.
const EVENT_MARKER = "__openneko_event__";
const RESULT_MARKER = "__openneko_agent_result__";

/**
 * Path of the agent entrypoint inside the agent image (the `agent` Docker
 * stage). The agent image is a `pnpm deploy` of @neko/worker, so the worker
 * package is rooted at /app (not /app/apps/worker) — entry.ts lives at
 * /app/src/agent-sandbox/entry.ts and @neko/llm is bundled at /app/node_modules.
 */
const AGENT_ENTRY = "/app/src/agent-sandbox/entry.ts";
const SANDBOX_JOB_PATH = "/sandbox/job.json";
const SANDBOX_HERMES_HOME = "/sandbox/hermes-home";

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
  /**
   * Alias the OpenShell-injected credential env var (holds the
   * `openshell:resolve:env:…` placeholder) to the env var the backend reads —
   * e.g. {from:"api_key", to:"GEMINI_API_KEY"} for hermes-gemini. The proxy
   * still substitutes the real key on egress, so the box only sees the
   * placeholder. `to` comes from the hermes provider→key map (mapHermesProvider).
   */
  keyAliases?: ReadonlyArray<{ from: string; to: string }>;
  /**
   * Host HERMES_HOME (hermesHomeForOrg) to mirror into the box KEYLESS — only
   * config.yaml travels; the `.env` is emptied so the proxy-injected key (via
   * keyAliases, process env) is what hermes uses. Set for the hermes backend.
   */
  hermesHomeHostPath?: string;
  /**
   * GJ6: path of the graphjin binary inside the agent image — the org's
   * data-source host is allowed for this binary so `graphjin cli` can
   * reach the GraphJin server from the box.
   */
  graphjinBinaryInBox?: string;
  /** Broker coords for the claude MCP-tool path (omitted for hermes). */
  brokerUrl?: string;
  /** Mint a per-run bearer token bound to {runId, orgId} (the broker forces
   *  org/run from the binding, never the request body). */
  brokerTokenFor?: (binding: { runId: string; orgId: string }) => string;
  /** Release the run's token after it finishes (called in the run's finally). */
  brokerRelease?: (runId: string) => void;
  execTimeoutMs?: number;
  onLog?: (line: string) => void;
}

type RunCore = (input: RunAgentBackendInput) => Promise<AgentRunResult>;

const SHELL_KEY_RX = /^[A-Z_][A-Z0-9_]*$/;
// Shell var names allow lowercase — the OpenShell credential is injected as `api_key`.
const SHELL_VARNAME_RX = /^[A-Za-z_][A-Za-z0-9_]*$/;

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

/**
 * Build the `runCore` the launcher injects into runChatTurn: run the
 * agent loop in an OpenShell sandbox.
 * The host-side prologue (prompt build) and epilogue (fence handling +
 * persistence) stay in runChatTurn around this call. Shared by the worker
 * (channel runs) and the web route (interactive chat) — both are control-plane
 * hosts that launch the sandbox but never run the agent loop themselves.
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

    // The box is a separate filesystem; the host workspace path (~/.config/… or
    // /Users/…) can't be recreated under the sandbox user's home. Upload the
    // workspace under /sandbox and remap every workspace path + the prompt from
    // the host orgRoot prefix to the box prefix (all roots are under orgRoot).
    const hostOrgRoot = input.workspace.orgRoot;
    const boxOrgRoot = path.posix.join("/sandbox", path.basename(hostOrgRoot));
    const toBox = (s: string): string => s.split(hostOrgRoot).join(boxOrgRoot);
    const boxWorkspace = Object.fromEntries(
      Object.entries(input.workspace).map(([k, v]) => [
        k,
        typeof v === "string" ? toBox(v) : v,
      ]),
    ) as RunAgentBackendInput["workspace"];

    // GJ5: the box rebuilds the graphjin guard (host paths don't exist
    // there) — resolve this run's policy write grants host-side, where
    // the DB lives, and ship them in the job.
    const graphjinWriteGrants = await resolveRunGraphjinGrants(
      input.orgId,
      input.runId,
    );

    const job = {
      orgId: input.orgId,
      threadId: input.threadId,
      runId: input.runId,
      message: input.userMessage,
      prompt: toBox(input.prompt),
      backendId: input.backend.id,
      // claude-agent reconstructs in-box and validates it has a Claude model;
      // hermes reads its model from config.yaml, so this is undefined there.
      model: input.backend.model,
      backendState: input.backendState,
      pluginActions: input.pluginActions,
      // Channel render intent — gates the in-box render tool (see
      // docs/PER_CHANNEL_RENDERING.md). Default true if absent.
      wantsCards: input.wantsCards ?? true,
      workspace: boxWorkspace,
      ...(graphjinWriteGrants.length > 0 ? { graphjinWriteGrants } : {}),
    };

    const stageDir = await mkdtemp(path.join(tmpdir(), "oss-agent-"));
    const jobFile = path.join(stageDir, "job.json");
    await writeFile(jobFile, JSON.stringify(job));
    const hermesStage = opts.hermesHomeHostPath
      ? await stageKeylessHermesHome(opts.hermesHomeHostPath, stageDir)
      : null;

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

      // claude's MCP tools reach the control plane via the broker — node's
      // fetch, which routes through the egress proxy (the box sets
      // NODE_USE_ENV_PROXY=1). Allow the broker host:port for the node binary.
      const brokerEgress = opts.brokerUrl
        ? (() => {
            const u = new URL(opts.brokerUrl);
            return [
              {
                host: u.hostname,
                port: Number(u.port) || 80,
                binary: "/usr/local/bin/node",
              },
            ];
          })()
        : [];
      // GJ6: the box must reach the org's GraphJin server — allow its
      // host:port for the in-box graphjin CLI (and node, for claude's
      // MCP fetch path).
      const dataEgress = await resolveDataSourceEgress(
        input.orgId,
        opts.graphjinBinaryInBox ?? "/usr/local/bin/graphjin",
      );
      const policyArgs = buildModelEgressArgs(name, [
        ...(opts.modelEgress ?? []),
        ...brokerEgress,
        ...dataEgress,
      ]);
      if (policyArgs) await run(policyArgs, 75_000);

      // Workspace lands at /sandbox/<orgRoot-basename> (= boxOrgRoot); `upload`
      // nests a dir as DEST/basename(SRC). Then keyless HERMES_HOME + the job.
      await run(
        ["sandbox", "upload", name, input.workspace.orgRoot, path.posix.dirname(boxOrgRoot)],
        180_000,
      );
      if (hermesStage) {
        await run(["sandbox", "upload", name, hermesStage, path.dirname(SANDBOX_HERMES_HOME)], 60_000);
      }
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
              ? {
                  OPENNEKO_BROKER_TOKEN: opts.brokerTokenFor({
                    runId: input.runId,
                    orgId: input.orgId,
                  }),
                }
              : {}),
            ...(opts.env ?? {}),
            ...(hermesStage ? { HERMES_HOME: SANDBOX_HERMES_HOME } : {}),
          },
          keyAliases: opts.keyAliases,
        }),
        input.emit,
        opts.execTimeoutMs ?? 600_000,
      );
    } finally {
      opts.brokerRelease?.(input.runId);
      await run(["sandbox", "delete", name], 60_000).catch(() => {});
      await rm(stageDir, { recursive: true, force: true });
    }
  };
}

/** `policy update` adding the model endpoint(s) scoped to the backend binary. */
/** GJ6: the org's GraphJin host as a per-run egress allow entry. Best-effort. */
async function resolveDataSourceEgress(
  orgId: string,
  graphjinBinary: string,
): Promise<Array<{ host: string; binary: string; port?: number }>> {
  try {
    const { data_source, db, desc, eq } = await import("@neko/db");
    const [src] = await db()
      .select({ mcpUrl: data_source.mcp_url })
      .from(data_source)
      .where(eq(data_source.org_id, orgId))
      .orderBy(desc(data_source.is_default), data_source.created_at)
      .limit(1);
    if (!src?.mcpUrl) return [];
    const u = new URL(src.mcpUrl);
    const port = Number(u.port) || (u.protocol === "https:" ? 443 : 80);
    return [{ host: u.hostname, port, binary: graphjinBinary }];
  } catch (err) {
    console.warn(
      `[agent-sandbox] data-source egress lookup failed: ${err instanceof Error ? err.message : err}`,
    );
    return [];
  }
}

/** GJ5 host-side grant resolution for the in-box guard rebuild. Best-effort. */
async function resolveRunGraphjinGrants(
  orgId: string,
  runId: string,
): Promise<string[]> {
  try {
    const { getWorkRunActor } = await import("./personas");
    const { resolveGraphjinWriteGrants } = await import(
      "./graphjin-actor-guard"
    );
    const actor = await getWorkRunActor(runId);
    return await resolveGraphjinWriteGrants(orgId, actor);
  } catch {
    return [];
  }
}

export function buildModelEgressArgs(
  name: string,
  egress: ReadonlyArray<{ host: string; binary: string; port?: number }>,
): string[] | null {
  if (egress.length === 0) return null;
  const args = ["policy", "update", name];
  for (const { host, port } of egress) {
    args.push("--add-endpoint", `${host}:${port ?? 443}:read-write:rest:enforce`);
  }
  for (const { binary } of egress) args.push("--binary", binary);
  for (const { host, port } of egress)
    args.push("--add-allow", `${host}:${port ?? 443}:*:/**`);
  args.push("--wait", "--timeout", "60");
  return args;
}

/**
 * Build the OpenShell runtime deps for runChatTurn from env. SEC9: OpenShell
 * is the only agent runtime — every production host injects this runCore;
 * tests inject their own in-process runCore via deps. Env-wired for now;
 * per-org auto-sync (provider/egress/key-var from the org row) is a follow-up.
 *
 * `broker` (optional) wires the claude MCP-tool path: the caller starts a host
 * broker (startAgentBroker) bound to its control plane and passes the handle so
 * the sandbox can reach the control plane mid-turn. Omit for the hermes-only
 * path (hermes emits fences parsed host-side; it needs no broker).
 */
export function agentRuntimeDepsFromEnv(broker?: {
  url: string;
  tokenFor: (binding: { runId: string; orgId: string }) => string;
  release?: (runId: string) => void;
}): Pick<Partial<RunChatTurnDeps>, "runCore"> {
  // Comma-separated: the model endpoint AND any resolution hosts (hermes needs
  // models.dev), all scoped to the backend's one connecting binary.
  const hosts = (process.env.OPENNEKO_AGENT_MODEL_HOST ?? "")
    .split(",")
    .map((h) => h.trim())
    .filter(Boolean);
  const binary = process.env.OPENNEKO_AGENT_MODEL_BINARY;
  // OpenShell injects the credential under the credential NAME (default
  // `api_key`); alias it to the env var the backend reads (the hermes
  // provider→key map, e.g. GEMINI_API_KEY). The proxy swaps in the real key on
  // egress, so the box only ever holds the placeholder.
  const keyEnv = process.env.OPENNEKO_AGENT_MODEL_KEY_ENV;
  const credName = process.env.OPENNEKO_AGENT_MODEL_CREDENTIAL || "api_key";
  return {
    runCore: makeSandboxRunCore({
      agentImage: process.env.OPENNEKO_AGENT_IMAGE ?? "ghcr.io/open-neko/agent:latest",
      gatewayName: process.env.OPENSHELL_GATEWAY || undefined,
      gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
      modelProvider: process.env.OPENNEKO_AGENT_MODEL_PROVIDER || undefined,
      modelEgress: binary ? hosts.map((host) => ({ host, binary })) : [],
      keyAliases: keyEnv ? [{ from: credName, to: keyEnv }] : undefined,
      hermesHomeHostPath: process.env.OPENNEKO_AGENT_HERMES_HOME || undefined,
      graphjinBinaryInBox:
        process.env.OPENNEKO_AGENT_GRAPHJIN_BINARY || undefined,
      brokerUrl: broker?.url,
      brokerTokenFor: broker?.tokenFor,
      brokerRelease: broker?.release,
    }),
  };
}

// Generic provider profile: holds just the model credential. The proxy
// substitutes the injected `openshell:resolve:env:…` placeholder wherever the
// agent puts it (gemini's ?key= query, claude's x-api-key header — both work,
// verified), so endpoints/binaries aren't needed here; egress is applied
// per-run by the launcher (modelEgress). One profile covers every provider.
const OPENNEKO_AGENT_PROFILE_ID = "openneko-agent";
const OPENNEKO_AGENT_PROFILE_YAML = `id: ${OPENNEKO_AGENT_PROFILE_ID}
display_name: OpenNeko Agent
description: OpenNeko agent model credential (generic; egress applied per-run)
category: agent
credentials:
- name: api_key
  description: model API key — proxy substitutes the placeholder on egress
  env_vars:
  - MODEL_API_KEY
  required: true
  auth_style: query
  header_name: ''
  query_param: key
endpoints: []
binaries: []
inference_capable: false
discovery:
  credentials:
  - api_key
`;

/**
 * Ensure a gateway-side OpenShell provider exists holding the org's model key,
 * so the egress proxy can inject it (the key never enters the box). Idempotent:
 * registers the generic profile, then creates-or-updates the named provider.
 * Run at worker startup (provisionHostConfig) — replaces the manual
 * `openshell provider create` step. Egress + the key-env alias stay with the
 * launcher; this only owns the credential.
 */
export async function ensureOpenShellProvider(opts: {
  providerName: string;
  apiKey: string;
  cli?: string;
  gatewayName?: string;
  gatewayEndpoint?: string;
}): Promise<void> {
  const cli = opts.cli ?? "openshell";
  const gatewayArgs = opts.gatewayName
    ? ["--gateway", opts.gatewayName]
    : opts.gatewayEndpoint
      ? ["--gateway-endpoint", opts.gatewayEndpoint]
      : [];
  const run = (args: string[]): Promise<string> =>
    runProcessOnce(cli, [...gatewayArgs, ...args], 60_000);

  const dir = await mkdtemp(path.join(tmpdir(), "oss-profile-"));
  try {
    const file = path.join(dir, `${OPENNEKO_AGENT_PROFILE_ID}.yaml`);
    await writeFile(file, OPENNEKO_AGENT_PROFILE_YAML);
    // Import is upsert-ish; tolerate "already registered".
    await run(["provider", "profile", "import", "--file", file]).catch(() => {});
  } finally {
    await rm(dir, { recursive: true, force: true });
  }

  const credential = `api_key=${opts.apiKey}`;
  // create on first run; update (refresh key) when it already exists.
  await run([
    "provider",
    "create",
    "--name",
    opts.providerName,
    "--type",
    OPENNEKO_AGENT_PROFILE_ID,
    "--credential",
    credential,
  ]).catch(() => run(["provider", "update", opts.providerName, "--credential", credential]));
}

/**
 * Mirror a host HERMES_HOME into `stageDir/hermes-home` KEYLESS: copy
 * config.yaml, write an empty `.env`. hermes reads `.env` before the process
 * env, so an empty `.env` lets the proxy-injected key (keyAliases) win — and
 * the real key never lands in the box.
 */
async function stageKeylessHermesHome(
  hostPath: string,
  stageDir: string,
): Promise<string> {
  const dest = path.join(stageDir, "hermes-home");
  await mkdir(dest, { recursive: true });
  const config = await readFile(path.join(hostPath, "config.yaml"), "utf8");
  await writeFile(path.join(dest, "config.yaml"), config);
  await writeFile(path.join(dest, ".env"), "");
  return dest;
}

function buildInnerCommand(o: {
  env: Record<string, string>;
  keyAliases?: ReadonlyArray<{ from: string; to: string }>;
}): string {
  const exports = Object.entries(o.env)
    .map(([k, v]) => {
      if (!SHELL_KEY_RX.test(k)) throw new Error(`bad env key ${k}`);
      return `export ${k}=${shellQuote(v)}`;
    })
    .join("; ");
  // Reference the OpenShell-injected var at runtime ("$from") — never a value.
  const aliases = (o.keyAliases ?? [])
    .map(({ from, to }) => {
      if (!SHELL_VARNAME_RX.test(from) || !SHELL_VARNAME_RX.test(to)) {
        throw new Error(`bad key alias ${from}->${to}`);
      }
      return `export ${to}="$${from}"`;
    })
    .join("; ");
  // cd into /app so `node --import tsx/esm` resolves tsx from /app/node_modules
  // (the image WORKDIR is the sandbox home /sandbox, which has no node_modules).
  // entry.ts reads its job by absolute path and the backend sets hermes's cwd
  // itself, so the node process cwd is free to be /app.
  const parts = [exports, aliases, "cd /app", `exec node --import tsx/esm ${AGENT_ENTRY}`];
  return parts.filter(Boolean).join("; ");
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
