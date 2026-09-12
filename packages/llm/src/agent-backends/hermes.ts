// Always session/new per turn — Hermes session/load replays history that the prompt already carries (see packages/llm/src/work/prompt.ts), double-counting context.

import { spawn, type ChildProcess } from "node:child_process";
import { readFileSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parse as parseYaml } from "yaml";
import {
  agentTurnTimeoutMs,
  type AgentBackend,
  type AgentEvent,
  type AgentModelIdentity,
  type AgentNativeDelegationPolicy,
  type AgentRunOptions,
  type AgentRunResult,
  type AgentTokenUsage,
} from "../agent-backend";
import {
  HERMES_NATIVE_DELEGATION_DISABLED,
  HERMES_NATIVE_DELEGATION_ENV,
} from "../agent-runtime-contract";
import { registerAgentCanceller } from "../agent-shutdown";
import { hermesHomeForOrg } from "../hermes-home";
import {
  A2UI_RENDER_ACP_TITLE,
  A2UI_RENDER_SERVER_NAME,
  A2UI_RENDER_TOOL_NAME,
  validateRenderCardsInput,
} from "../work/a2ui-contract";
import {
  AcpProtocolError,
  createAcpClient,
  type AcpClient,
  type AcpNotification,
  type AcpSessionUpdate,
} from "./hermes-acp-client";
import { extractSurfaceMessages } from "./surface";

export { extractSurfaceMessages } from "./surface";

export type ProviderSummarySource = "google-gemini" | "anthropic";

function canonicalAcpMcpToolName(title: unknown): string | null {
  if (typeof title !== "string") return null;
  const trimmed = title.trim();
  if (!/^mcp_+/u.test(trimmed)) return null;
  // ACP clients may render the same MCP identity as either
  // mcp_server_tool or mcp__server__tool. AgentEvent consumers need one
  // stable, precise name instead of the coarse ACP kind (`other`).
  return trimmed.replace(/_+/gu, "_");
}

/**
 * ACP calls every provider reasoning stream an `agent_thought_chunk`, so the
 * generated per-org config is the trust boundary. Only providers whose native
 * API explicitly defines these parts as user-visible summaries are exposed:
 * Gemini includeThoughts and Anthropic thinking.display="summarized".
 */
export function providerThoughtSummarySource(
  hermesHome: string | undefined,
): ProviderSummarySource | null {
  if (!hermesHome) return null;
  try {
    const config = parseYaml(
      readFileSync(join(hermesHome, "config.yaml"), "utf8"),
    ) as {
      model?: { default?: unknown; provider?: unknown };
      agent?: { reasoning_effort?: unknown };
    } | null;
    const provider = String(config?.model?.provider ?? "").trim().toLowerCase();
    const model = String(config?.model?.default ?? "").trim().toLowerCase();
    const effort = config?.agent?.reasoning_effort;
    const disabled =
      effort === false ||
      ["none", "off", "false", "no", "0"].includes(
        String(effort ?? "").trim().toLowerCase(),
      );
    if (
      provider === "gemini" &&
      model.startsWith("gemini") &&
      effort !== undefined &&
      !disabled
    ) {
      return "google-gemini";
    }
    if (
      provider === "anthropic" &&
      model.startsWith("claude-") &&
      effort !== undefined &&
      !disabled
    ) {
      return "anthropic";
    }
    return null;
  } catch {
    return null;
  }
}

/** Last error-ish lines of hermes' own agent.log — the file dies with the
 *  sandbox, so a mid-turn death must read it NOW or never. */
function hermesAgentLogTail(hermesHome: string | undefined): string {
  if (!hermesHome) return "";
  try {
    const lines = readFileSync(join(hermesHome, "logs", "agent.log"), "utf8")
      .split("\n")
      .filter(
        (l) =>
          l.trim() &&
          !l.includes("Prompt on session") &&
          !l.includes("conversation turn:"),
      );
    const errs = lines.filter((l) =>
      /ERROR|CRITICAL|Traceback|Unhandled|fatal|Killed|Segmentation/i.test(l),
    );
    return (errs.length ? errs : lines).slice(-8).join("\n").slice(-900);
  } catch {
    return "";
  }
}

/** MemAvailable from /proc/meminfo (Linux only) — distinguishes a memory-
 *  pressure kill from a crash without a debug rerun. */
function memAvailable(): string {
  try {
    const m = /MemAvailable:\s+(\d+) kB/.exec(
      readFileSync("/proc/meminfo", "utf8"),
    );
    return m ? `${Math.round(Number(m[1]) / 1024)}MiB` : "";
  } catch {
    return "";
  }
}

function killProcessGroup(child: ChildProcess, signal: NodeJS.Signals): void {
  if (!child.pid) return;
  try {
    process.kill(-child.pid, signal);
  } catch {
    // ESRCH if already exited
  }
  // Group kill fails when child isn't a group leader (e.g. test mocks); send to child too — idempotent for SIGTERM/SIGKILL.
  try {
    child.kill(signal);
  } catch {
    // ignore
  }
}

const FENCE_RE = /^\s*```(?:json)?\s*([\s\S]*?)\s*```\s*$/;

const FENCE_CLOSE = "\n```";

const HERMES_RESPONSE_TRUNCATED_SENTINEL =
  "Response truncated due to output length limit";

// Fences the runtime parses out-of-band: a2ui drives surface cards,
// and the three workflow fences are the Hermes-shaped tool surfaces.
// All four are noise in the chat stream — hide them while we wait for
// the closing ``` to land.
const HIDDEN_FENCE_OPENERS = [
  "```neko_a2ui",
  "```neko_workflow_save",
  "```neko_workflow_output",
  "```neko_action_request",
  "```neko_rule_save",
] as const;

function extractMarkdownText(messages: Array<Record<string, unknown>>): string {
  const out: string[] = [];
  const visit = (node: unknown): void => {
    if (!node || typeof node !== "object") return;
    if (Array.isArray(node)) {
      for (const item of node) visit(item);
      return;
    }
    const obj = node as Record<string, unknown>;
    if (obj.component === "Markdown" && typeof obj.text === "string") {
      out.push(obj.text);
    }
    for (const value of Object.values(obj)) visit(value);
  };
  visit(messages);
  return out.join("\n\n").trim();
}

function findNextOpener(
  raw: string,
  from: number,
): { index: number; opener: string } | null {
  let best: { index: number; opener: string } | null = null;
  for (const opener of HIDDEN_FENCE_OPENERS) {
    const idx = raw.indexOf(opener, from);
    if (idx === -1) continue;
    if (!best || idx < best.index) best = { index: idx, opener };
  }
  return best;
}

export function outsideFenceText(raw: string): string {
  let out = "";
  let i = 0;
  while (i < raw.length) {
    const next = findNextOpener(raw, i);
    if (!next) {
      // No full opener visible. Hold back any tail of `raw` that matches a
      // prefix of any opener — it might complete in a later streamed chunk.
      // Without this, a partial opener like "```neko_a2" or "```neko_wo"
      // leaks into the message event stream as an empty code block.
      const tail = raw.slice(i);
      let holdBack = 0;
      for (const opener of HIDDEN_FENCE_OPENERS) {
        const maxK = Math.min(tail.length, opener.length - 1);
        for (let k = maxK; k > holdBack; k--) {
          if (tail.slice(-k) === opener.slice(0, k)) {
            holdBack = k;
            break;
          }
        }
      }
      out += tail.slice(0, tail.length - holdBack);
      break;
    }
    out += raw.slice(i, next.index);
    const close = raw.indexOf(FENCE_CLOSE, next.index + next.opener.length);
    if (close === -1) break;
    i = close + FENCE_CLOSE.length;
  }
  return out;
}

export function parseJsonFromOutput(raw: string): unknown {
  const trimmed = raw.trim();
  const fenced = trimmed.match(FENCE_RE);
  const candidate = (fenced?.[1] ?? trimmed).trim();
  try {
    return JSON.parse(candidate);
  } catch {
    const first = candidate.indexOf("{");
    const last = candidate.lastIndexOf("}");
    if (first === -1 || last === -1 || last < first) {
      throw new Error(
        `hermes output not parseable as JSON (no object braces found): ${candidate.slice(0, 200)}`,
      );
    }
    return JSON.parse(candidate.slice(first, last + 1));
  }
}

const DEFAULT_TIMEOUT_MS = agentTurnTimeoutMs();

export class HermesBackend implements AgentBackend {
  readonly id = "hermes" as const;
  readonly configuredIdentity?: AgentModelIdentity;
  readonly model?: string;
  readonly capabilities = {
    // ACP mounts MCP servers as stdio children (session/new mcpServers) — the
    // neko servers ride a bridge process (OPENNEKO_MCP_BRIDGE) that rebuilds
    // each one over the broker control plane. Surfaces still come via fence
    // (see surface.ts).
    mcpTools: true,
    sessionResume: false,
    nativeDelegation: "hermes-delegate-task",
  } as const;

  constructor(configuredIdentity?: AgentModelIdentity) {
    this.configuredIdentity = configuredIdentity;
    this.model = configuredIdentity?.model;
  }

  async run(opts: AgentRunOptions): Promise<AgentRunResult> {
    const {
      prompt,
      userMessage,
      timeoutMs = DEFAULT_TIMEOUT_MS,
      retries = 1,
      debug = false,
      tag,
      orgId,
      workspace,
      skills: _skills,
      signal,
      onEvent,
      wantsCards = false,
      backendState = {},
      nativeDelegation = "enabled",
    } = opts;

    const fullPrompt = userMessage
      ? `${prompt}\n\nCurrent user message:\n${userMessage}`
      : prompt;

    // Streaming turns normally cannot be retried because replaying tool and
    // message events would duplicate visible work. A completely empty ACP
    // turn is the exception: runOnce marks it retryable only when Hermes
    // emitted neither content nor tool activity.
    const maxAttempts = retries + 1;
    let lastErr: Error | undefined;

    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      try {
        const out = await runOnce({
          prompt: fullPrompt,
          timeoutMs,
          debug,
          tag,
          orgId,
          workspace,
          signal,
          onEvent,
          wantsCards,
          configuredIdentity: this.configuredIdentity,
          nativeDelegation,
          mcpServerNames: Object.keys(opts.mcpServers ?? {}),
          mcpBridgeEnv: opts.mcpBridgeEnv,
        });
        if (out.error) {
          lastErr = new Error(out.error);
          if (debug) {
            console.warn(
              `[hermes] attempt ${attempt + 1}/${maxAttempts} failed: ${out.error}`,
            );
          }
          if (onEvent && !out.retryable) break;
          if (onEvent && attempt + 1 < maxAttempts) {
            await onEvent({
              type: "status",
              message: "Hermes returned no output; retrying…",
            });
          }
          continue;
        }
        return {
          finalText: out.finalText,
          rawText: out.rawText,
          status: signal?.aborted ? "cancelled" : "completed",
          backendState,
        };
      } catch (e) {
        lastErr = e instanceof Error ? e : new Error(String(e));
        if (debug) {
          console.warn(
            `[hermes] attempt ${attempt + 1}/${maxAttempts} failed: ${lastErr.message}`,
          );
        }
        if (onEvent) break;
      }
    }

    const message = lastErr?.message ?? "hermes: unknown failure";
    if (signal?.aborted) {
      return { finalText: "", status: "cancelled", backendState };
    }
    if (onEvent) {
      await onEvent({ type: "error", message });
    }
    return { finalText: "", status: "failed", backendState, error: message };
  }
}

type RunOnceArgs = {
  prompt: string;
  timeoutMs: number;
  debug: boolean;
  tag: string | undefined;
  orgId: string | undefined;
  workspace: AgentRunOptions["workspace"];
  signal: AbortSignal | undefined;
  onEvent: AgentRunOptions["onEvent"];
  wantsCards: boolean;
  configuredIdentity: AgentModelIdentity | undefined;
  nativeDelegation: AgentNativeDelegationPolicy;
  mcpServerNames: string[];
  mcpBridgeEnv: Record<string, string> | undefined;
};

type RunOnceOutcome = {
  finalText: string;
  rawText?: string;
  error?: string;
  retryable?: boolean;
};

async function runOnce(args: RunOnceArgs): Promise<RunOnceOutcome> {
  const {
    prompt,
    timeoutMs,
    debug,
    tag,
    orgId,
    workspace,
    signal,
    onEvent,
    wantsCards,
    configuredIdentity,
    nativeDelegation,
    mcpServerNames,
    mcpBridgeEnv,
  } = args;

  let cwd: string;
  let cleanupScratch: (() => Promise<void>) | undefined;
  if (workspace) {
    cwd = workspace.orgRoot;
  } else {
    const safeTag = tag?.replace(/[^a-zA-Z0-9_-]/g, "").slice(0, 64);
    cwd = safeTag
      ? await (async () => {
          const exact = join(tmpdir(), safeTag);
          try {
            await mkdir(exact);
            return exact;
          } catch (e) {
            if ((e as NodeJS.ErrnoException).code !== "EEXIST") throw e;
            return mkdtemp(join(tmpdir(), `${safeTag}-`));
          }
        })()
      : await mkdtemp(join(tmpdir(), "neko-hermes-"));
    cleanupScratch = () =>
      rm(cwd, { recursive: true, force: true }).catch(() => {});
  }

  const env: NodeJS.ProcessEnv = workspace
    ? {
        ...process.env,
        PATH: `${workspace.binRoot}:${process.env.PATH || ""}`,
      }
    : { ...process.env };
  // The model-facing Hermes process never needs the broker bearer token.
  // MCP bridge children receive it explicitly in their clean per-server env
  // below; removing it here prevents terminal tools from bypassing the
  // declared MCP capability surface with a hand-written HTTP request.
  delete env.OPENNEKO_BROKER_URL;
  delete env.OPENNEKO_BROKER_TOKEN;
  // This is a process-local policy. Clear any ambient value first so normal
  // production turns retain Hermes' native delegation unless the caller
  // explicitly disables it for this run (for example, backend parity evals).
  delete env[HERMES_NATIVE_DELEGATION_ENV];
  if (nativeDelegation === "disabled") {
    env[HERMES_NATIVE_DELEGATION_ENV] = HERMES_NATIVE_DELEGATION_DISABLED;
  }
  if (orgId) {
    env.HERMES_HOME = hermesHomeForOrg(orgId);
  }
  const providerSummarySource = providerThoughtSummarySource(
    env.HERMES_HOME,
  );
  // A hard native crash (SIGSEGV/SIGABRT in compiled deps) dies without a
  // Python traceback — faulthandler makes it dump one to stderr, which the
  // mid-turn death message below surfaces.
  env.PYTHONFAULTHANDLER = "1";

  // OpenNeko owns the safety boundary here: the agent runs with the per-run
  // graphjin guard first on PATH and any MCP tools are mounted explicitly.
  // Hermes' internal dangerous-command approval bridge is not stable across
  // releases (v2026.5 passes allow_permanent to an older ACP callback), so
  // bypass it instead of letting headless jobs hang on approval prompts.
  env.HERMES_YOLO_MODE = "1";
  const warm = process.env.OPENNEKO_HERMES_WARM === "1";
  const child = spawn(warm ? "/usr/local/uv/tools/hermes-agent/bin/python" : "hermes",
    warm ? ["/app/hermes-warm.py", "client"] : ["--yolo", "acp"], {
    stdio: ["pipe", "pipe", "pipe"],
    cwd,
    env,
    detached: true,
  });
  const tagSuffix = tag ? ` tag=${tag}` : "";

  // Hermes' ACP adapter logs at INFO to stderr — including a "Prompt on
  // session <id>: <full system prompt>" line. Forwarding that as status
  // events leaks the system prompt into the UI. Useful progress pills
  // ("Queued for…", "Loading skills…") are emitted explicitly by the API
  // route and the worker; the agent-side notifications stream covers the
  // rest. So we just buffer stderr for debug + crash dumps.
  const stderrChunks: Buffer[] = [];
  child.stderr?.on("data", (c: Buffer) => {
    stderrChunks.push(c);
    if (debug) process.stderr.write(c);
  });

  const unregister = registerAgentCanceller(() => killProcessGroup(child, "SIGKILL"));
  const onAbort = () => killProcessGroup(child, "SIGTERM");
  if (signal) {
    if (signal.aborted) onAbort();
    else signal.addEventListener("abort", onAbort, { once: true });
  }

  let timer: NodeJS.Timeout | null = null;
  let timedOut = false;
  let spawnError: Error | undefined;
  child.on("error", (e) => {
    spawnError = new Error(`hermes spawn failed: ${e.message}`);
  });
  const closedPromise = new Promise<{ code: number | null }>((resolve) => {
    child.on("close", (code) => resolve({ code }));
  });
  // The disposed-client error can outrun the child's exit event, in which
  // case exitCode/signalCode still read null — the death certificate must
  // wait for the real values (a SIGSEGV/SIGKILL is the whole diagnosis).
  const exitInfoPromise = new Promise<{
    code: number | null;
    signal: NodeJS.Signals | null;
  }>((resolve) => {
    child.on("exit", (code, sig) => resolve({ code, signal: sig }));
  });

  const cleanup = async () => {
    if (timer) clearTimeout(timer);
    if (signal) signal.removeEventListener("abort", onAbort);
    unregister();
    await cleanupScratch?.();
  };

  let client: AcpClient | undefined;
  try {
    client = createAcpClient(child);

    timer = setTimeout(() => {
      timedOut = true;
      killProcessGroup(child, "SIGTERM");
    }, timeoutMs);

    await client.request("initialize", {
      protocolVersion: 1,
      clientCapabilities: { fs: { readTextFile: false, writeTextFile: false } },
    });

    // session/new per turn — see file header for the session/load double-count rationale.
    // Web turns get the render_cards MCP server so the model renders cards.
    // The neko tool servers mount as stdio bridge children when a bridge entry
    // is available (in-box: entry.ts sets OPENNEKO_MCP_BRIDGE; broker coords
    // ride the process env down to each child).
    const bridgePath = process.env.OPENNEKO_MCP_BRIDGE;
    const bridgeEnv = [
      ...Object.entries(mcpBridgeEnv ?? {}),
      // Hermes spawns MCP children with a CLEAN env + this list — nothing
      // inherits. Without the proxy vars the child dials direct and the
      // sandbox firewall refuses; broker traffic must ride the egress proxy.
      ...[
        "OPENNEKO_BROKER_URL",
        "OPENNEKO_BROKER_TOKEN",
        "ALL_PROXY",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "NO_PROXY",
        "no_proxy",
        "NODE_USE_ENV_PROXY",
      ]
        .map((k) => [k, process.env[k] ?? ""])
        .filter(([, v]) => v),
    ].map(([name, value]) => ({ name, value }));
    const safeBridgeNames = mcpServerNames
      // names are our map keys — the regex guards the shell interpolation.
      .filter(
        (name) =>
          /^neko_[a-z0-9_]+$/.test(name) &&
          (name !== A2UI_RENDER_SERVER_NAME || wantsCards),
      );
    const bridgeServers =
      bridgePath && mcpBridgeEnv
        ? safeBridgeNames.length > 0
          ? [{
              // The bridge multiplexes all logical OpenNeko MCP servers in
              // one Node process while preserving mcp_neko_* tool names.
              name: "neko",
              // cd /app first: hermes spawns MCP children with the session
              // cwd (/sandbox/…), where `--import tsx/esm` cannot resolve.
              // A plain-JS bundle skips the tsx loader entirely.
              command: "/bin/sh",
              args: [
                "-c",
                bridgePath.endsWith(".ts")
                  ? `cd /app && exec node --import tsx/esm ${bridgePath} ${safeBridgeNames.join(",")}`
                  : `cd /app && exec node ${bridgePath} ${safeBridgeNames.join(",")}`,
              ],
              env: bridgeEnv,
            }]
          : []
        : [];
    const mcpServers = bridgeServers;
    const fresh = await client.request<{
      sessionId: string;
      models?: unknown;
    }>("session/new", {
      cwd,
      mcpServers,
    });
    const sessionId = fresh.sessionId;
    const observedIdentity = parseHermesSessionIdentity(fresh);

    let accumulatedText = "";
    let emittedOutsideLen = 0;
    let surfaceEmittedDuringStream = false;
    let toolActivityObserved = false;
    const pendingValidRenderToolCalls = new Map<string, unknown>();
    let pendingProviderSummary = "";
    let providerSummarySequence = 0;
    let eventQueue = Promise.resolve();
    let eventError: Error | undefined;
    const emitQueued = (event: AgentEvent): void => {
      if (!onEvent) return;
      eventQueue = eventQueue.then(async () => {
        try {
          await onEvent(event);
        } catch (error) {
          eventError ??= error instanceof Error ? error : new Error(String(error));
        }
      });
    };
    const flushProviderSummary = (): void => {
      const content = pendingProviderSummary.trim();
      pendingProviderSummary = "";
      if (!content || !onEvent) return;
      providerSummarySequence += 1;
      emitQueued({
        type: "progress",
        id: `${providerSummarySource}-summary-${sessionId}-${providerSummarySequence}`,
        content,
        source: "provider_summary",
        provider: providerSummarySource!,
      });
    };
    let interimSequence = 0;
    const interimMeta = (update: AcpSessionUpdate): { alreadyStreamed: boolean } | null => {
      if (update.sessionUpdate !== "agent_message_chunk") return null;
      const meta = (update.fieldMeta ?? update._meta) as { hermes?: { interim?: unknown; already_streamed?: unknown } } | undefined;
      const marker = meta?.hermes;
      return marker?.interim === true
        ? { alreadyStreamed: marker.already_streamed === true }
        : null;
    };

    client.onNotification((notif) => {
      const update = notif.update;
      switch (update.sessionUpdate) {
        case "agent_message_chunk": {
          const text = update.content?.text ?? "";
          if (!text) return;
          const interim = interimMeta(update);
          if (interim) {
            if (!interim.alreadyStreamed && onEvent) {
              interimSequence += 1;
              emitQueued({
                type: "interim",
                id: `hermes-interim-${sessionId}-${interimSequence}`,
                content: text,
                source: "hermes_interim_assistant",
              });
            }
            return;
          }
          flushProviderSummary();
          accumulatedText += text;
          if (onEvent) {
            const trimmed = accumulatedText.trim();
            if (
              trimmed.length > 0 &&
              HERMES_RESPONSE_TRUNCATED_SENTINEL.startsWith(trimmed)
            ) {
              return;
            }
            const outside = outsideFenceText(accumulatedText);
            const delta = outside.slice(emittedOutsideLen);
            if (delta) {
              emittedOutsideLen = outside.length;
              emitQueued({ type: "message", role: "assistant", content: delta });
            }
          }
          return;
        }
        case "agent_thought_chunk": {
          if (providerSummarySource) {
            const remaining = 6_000 - pendingProviderSummary.length;
            if (remaining > 0) {
              pendingProviderSummary += (update.content?.text ?? "").slice(
                0,
                remaining,
              );
            }
          }
          return;
        }
        case "tool_call": {
          toolActivityObserved = true;
          flushProviderSummary();
          if (!onEvent) return;
          const mcpToolName = canonicalAcpMcpToolName(update.title);
          // The brokered neko_ui server is the sole surface emitter. Suppress
          // a successful render's tool pill, but preserve the exact rejected
          // input as tool_start telemetry so envelope failures are diagnosable.
          if (mcpToolName === A2UI_RENDER_ACP_TITLE) {
            const validation = validateRenderCardsInput(update.rawInput);
            if (validation.success) {
              pendingValidRenderToolCalls.set(update.toolCallId, update.rawInput);
              return;
            }
            emitQueued({
              type: "tool_start",
              id: update.toolCallId,
              name: A2UI_RENDER_TOOL_NAME,
              input: {
                title: update.title,
                rawInput: update.rawInput,
                validationIssues: validation.issues,
              },
            });
            return;
          }
          emitQueued({
            type: "tool_start",
            id: update.toolCallId,
            name: mcpToolName ?? update.kind ?? "tool",
            input:
              mcpToolName && update.rawInput !== undefined
                ? update.rawInput
                : {
                    ...(update.title ? { title: update.title } : {}),
                    ...(update.locations ? { locations: update.locations } : {}),
                    ...(update.rawInput !== undefined
                      ? { rawInput: update.rawInput }
                      : {}),
                  },
          });
          return;
        }
        case "tool_call_update": {
          toolActivityObserved = true;
          if (!onEvent) return;
          const id = update.toolCallId;
          if (pendingValidRenderToolCalls.has(id)) {
            if (update.status === "completed") {
              pendingValidRenderToolCalls.delete(id);
              surfaceEmittedDuringStream = true;
              return;
            }
            if (update.status !== "failed") return;
            const rawInput = pendingValidRenderToolCalls.get(id);
            pendingValidRenderToolCalls.delete(id);
            // The input passed structural validation, but the sole render server
            // rejected execution. Surface that failure with the original input.
            emitQueued({
              type: "tool_start",
              id,
              name: A2UI_RENDER_TOOL_NAME,
              input: { title: A2UI_RENDER_ACP_TITLE, rawInput },
            });
          }
          const status = update.status;
          if (status === "completed" || status === "failed") {
            emitQueued({
              type: "tool_end",
              id,
              result: status === "completed" ? update.rawOutput ?? update.content : undefined,
              error: status === "failed" ? extractErrorText(update.content ?? update.rawOutput) : undefined,
            });
          } else {
            emitQueued({
              type: "tool_delta",
              id,
              delta: { status: status ?? "in_progress", content: update.content, rawOutput: update.rawOutput },
            });
          }
          return;
        }
        case "plan": {
          if (!onEvent) return;
          const next = update.entries.find((e) => e.status !== "completed");
          if (next) emitQueued({ type: "status", message: next.content });
          return;
        }
        default:
          return;
      }
    });

    let promptError: string | undefined;
    let promptStopReason: string | undefined;
    let promptUsage: ReturnType<typeof normalizeHermesUsage>;
    try {
      const promptResponse = await client.request<{
        usage?: unknown;
        stopReason?: string;
      }>("session/prompt", {
        sessionId,
        prompt: [{ type: "text", text: prompt }],
      });
      promptStopReason = promptResponse.stopReason;
      promptUsage = normalizeHermesUsage(promptResponse.usage);
      flushProviderSummary();
    } catch (e) {
      if (e instanceof AcpProtocolError) {
        promptError = `hermes: ${e.message}`;
      } else {
        throw e;
      }
    }

    if (timedOut) {
      throw new Error(`hermes timed out after ${timeoutMs}ms`);
    }
    if (promptError) {
      return { finalText: "", error: promptError };
    }
    await eventQueue;
    if (eventError) throw eventError;

    // ACP considers `end_turn` with no chunks a valid protocol response. It is
    // not a valid OpenNeko answer: accepting it used to persist a green run
    // with no assistant message, leaving the UI apparently dead. Retry only
    // when no tool ran, then fail with an explicit diagnostic.
    if (!accumulatedText.trim() && !surfaceEmittedDuringStream) {
      return {
        finalText: "",
        error:
          `hermes completed without assistant output or surface` +
          ` (stopReason=${promptStopReason ?? "unknown"})`,
        retryable: !toolActivityObserved,
      };
    }

    if (promptUsage || configuredIdentity || observedIdentity) {
      const modelIdentity =
        configuredIdentity || observedIdentity
          ? {
              ...(configuredIdentity ? { configured: configuredIdentity } : {}),
              ...(observedIdentity ? { observed: observedIdentity } : {}),
            }
          : undefined;
      emitQueued({
        type: "usage",
        source: "outer",
        ...(observedIdentity ?? {}),
        ...(modelIdentity ? { modelIdentity } : {}),
        usage: promptUsage ?? {
          coverage: "unavailable",
          missingReasons: ["Hermes ACP session/prompt omitted usage"],
        },
      });
      await eventQueue;
      if (eventError) throw eventError;
    }

    if (accumulatedText.trim() === HERMES_RESPONSE_TRUNCATED_SENTINEL) {
      return {
        finalText: "",
        error:
          `hermes response truncated due to output length limit` +
          ` (stopReason=${promptStopReason ?? "unknown"})`,
        retryable: !toolActivityObserved,
      };
    }

    let finalText = accumulatedText;
    if (onEvent) {
      const parsed = extractSurfaceMessages(accumulatedText);
      const markdownText = extractMarkdownText(parsed.messages);
      finalText = (markdownText || parsed.text).trim();
    }

    // rawText keeps every fence (a2ui + builder); finalText above dropped the
    // builder fences when it collapsed to the a2ui markdown. run-chat-turn
    // parses action/workflow/rule/memory fences out of rawText.
    return { finalText: finalText.trim(), rawText: accumulatedText };
  } catch (e) {
    if (spawnError) throw spawnError;
    // "ACP client disposed" alone means the hermes child died mid-turn with
    // its actual cause buried in the buffered stderr — surface the tail so
    // each occurrence is diagnosable without a debug rerun. Lines echoing
    // session prompts are dropped: they carry the system prompt.
    if (e instanceof Error && e.message.includes("ACP client disposed")) {
      // The timer's SIGTERM rejects the in-flight prompt request, so the
      // post-await timedOut check below this try never runs — without this
      // branch a plain timeout masquerades as an unexplained agent death
      // (this WAS the long-undiagnosed "hermes exited mid-turn" flake).
      if (timedOut) {
        const activity = hermesAgentLogTail(env.HERMES_HOME);
        throw new Error(
          `hermes turn exceeded its ${Math.round(timeoutMs / 1000)}s budget and was terminated ` +
            `(OPENNEKO_AGENT_TURN_TIMEOUT_MS overrides)` +
            (activity ? `; agent activity: ${activity}` : ""),
          { cause: e },
        );
      }
      const exit = await Promise.race([
        exitInfoPromise,
        new Promise<null>((r) => setTimeout(r, 3000)),
      ]);
      const tail = Buffer.concat(stderrChunks)
        .toString("utf8")
        .split("\n")
        .filter((l) => l.trim() && !l.includes("Prompt on session"))
        .slice(-6)
        .join("\n")
        .slice(-600);
      const agentLog = hermesAgentLogTail(env.HERMES_HOME);
      const mem = memAvailable();
      throw new Error(
        `hermes exited mid-turn (code=${exit ? exit.code ?? "null" : "?"} signal=${
          exit ? exit.signal ?? "null" : "?"
        }${mem ? ` mem-available=${mem}` : ""})${
          tail ? `; last stderr: ${tail}` : ""
        }${agentLog ? `; agent.log tail: ${agentLog}` : ""}`,
        { cause: e },
      );
    }
    throw e;
  } finally {
    client?.dispose();
    if (child.exitCode == null && !signal?.aborted) {
      killProcessGroup(child, "SIGTERM");
    }
    await closedPromise.catch(() => {});
    if (debug) {
      const stderr = Buffer.concat(stderrChunks).toString("utf8");
      if (stderr) process.stderr.write(stderr);
    }
    await cleanup();
  }
}

/**
 * Hermes reports the live session identity as `<provider>:<model>` in
 * session/new.models.currentModelId. This is process-observed evidence, not a
 * copy of OpenNeko's configured identity. Split only on the first colon so
 * provider-qualified model ids remain intact.
 */
export function parseHermesSessionIdentity(
  response: unknown,
): AgentModelIdentity | undefined {
  if (!response || typeof response !== "object" || Array.isArray(response)) {
    return undefined;
  }
  const models = (response as { models?: unknown }).models;
  if (!models || typeof models !== "object" || Array.isArray(models)) {
    return undefined;
  }
  const modelState = models as Record<string, unknown>;
  const raw = modelState.currentModelId ?? modelState.current_model_id;
  if (typeof raw !== "string") return undefined;
  const separator = raw.indexOf(":");
  if (separator <= 0 || separator >= raw.length - 1) return undefined;
  const provider = raw.slice(0, separator).trim();
  const model = raw.slice(separator + 1).trim();
  if (!provider || !model) return undefined;
  return { provider, model };
}

export function normalizeHermesUsage(value: unknown): AgentTokenUsage | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const usage = value as Record<string, unknown>;
  const number = (...keys: string[]): number | undefined => {
    for (const key of keys) {
      const parsed = Number(usage[key]);
      if (Number.isFinite(parsed) && parsed >= 0) return parsed;
    }
    return undefined;
  };
  const inputTokens = number("inputTokens", "input_tokens", "promptTokens", "prompt_tokens");
  const outputTokens = number(
    "outputTokens",
    "output_tokens",
    "completionTokens",
    "completion_tokens",
  );
  const cacheReadTokens = number(
    "cacheReadTokens",
    "cache_read_tokens",
    "cachedReadTokens",
    "cached_read_tokens",
  );
  const cacheWriteTokens = number(
    "cacheWriteTokens",
    "cache_write_tokens",
    "cachedWriteTokens",
    "cached_write_tokens",
  );
  const reasoningTokens = number(
    "reasoningTokens",
    "reasoning_tokens",
    "thoughtTokens",
    "thought_tokens",
  );
  const reportedTotal = number("totalTokens", "total_tokens");
  const billedCostUsd = number("costUsd", "cost_usd", "billedCostUsd", "billed_cost_usd");
  if (
    inputTokens === undefined &&
    outputTokens === undefined &&
    reportedTotal === undefined &&
    billedCostUsd === undefined
  ) {
    return undefined;
  }
  const complete = inputTokens !== undefined && outputTokens !== undefined;
  return {
    ...(inputTokens !== undefined ? { inputTokens } : {}),
    ...(outputTokens !== undefined ? { outputTokens } : {}),
    ...(cacheReadTokens !== undefined ? { cacheReadTokens } : {}),
    ...(cacheWriteTokens !== undefined ? { cacheWriteTokens } : {}),
    ...(reasoningTokens !== undefined ? { reasoningTokens } : {}),
    ...(reportedTotal !== undefined
      ? { totalTokens: reportedTotal }
      : complete
        ? { totalTokens: inputTokens + outputTokens }
        : {}),
    ...(billedCostUsd !== undefined ? { billedCostUsd, currency: "USD" } : {}),
    coverage: complete ? "complete" : "partial",
    ...(!complete
      ? { missingReasons: ["Hermes ACP usage omitted input or output token counts"] }
      : {}),
  };
}

function extractErrorText(raw: unknown): string {
  if (typeof raw === "string") return raw;
  if (raw && typeof raw === "object") return JSON.stringify(raw);
  return "Tool failed";
}
