// Always session/new per turn — Hermes session/load replays history that the prompt already carries (see packages/llm/src/work/prompt.ts), double-counting context.

import { spawn, type ChildProcess } from "node:child_process";
import { writeFileSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  type AgentBackend,
  type AgentRunOptions,
  type AgentRunResult,
} from "../agent-backend";
import { registerAgentCanceller } from "../agent-shutdown";
import { hermesHomeForOrg } from "../host-provision";
import { RENDER_CARDS_DESCRIPTION } from "../work/render-catalog";
import {
  AcpProtocolError,
  createAcpClient,
  type AcpClient,
  type AcpNotification,
} from "./hermes-acp-client";
import { coerceSurfaceMessages, extractSurfaceMessages } from "./surface";

export { extractSurfaceMessages } from "./surface";

// ── Per-channel rendering (web): hand hermes a tiny stdio MCP server that
// advertises `render_cards`, so the model can render a2ui cards instead of a
// fence. The server is a sink (returns ok); the HOST emits the surface from the
// tool_call notification's rawInput. See docs/PER_CHANNEL_RENDERING.md.
const RENDER_MCP_SERVER_NAME = "neko_render";
// hermes surfaces MCP tool calls as `mcp_<server>_<tool>` (verified via spike).
const RENDER_TOOL_TITLE = `mcp_${RENDER_MCP_SERVER_NAME}_render_cards`;
const RENDER_MCP_STUB_SOURCE = `let b="";
process.stdin.on("data",c=>{b+=c;let i;while((i=b.indexOf("\\n"))>=0){
const l=b.slice(0,i).trim();b=b.slice(i+1);if(!l)continue;
let r;try{r=JSON.parse(l)}catch{continue}const{id,method}=r;
if(typeof id==="undefined")continue;
const w=o=>process.stdout.write(JSON.stringify({jsonrpc:"2.0",id,result:o})+"\\n");
if(method==="initialize")w({protocolVersion:"2024-11-05",capabilities:{tools:{}},serverInfo:{name:"neko_render",version:"1"}});
else if(method==="tools/list")w({tools:[${JSON.stringify({
  name: "render_cards",
  description: RENDER_CARDS_DESCRIPTION,
  inputSchema: {
    type: "object",
    properties: { messages: { type: "array", items: { type: "object" } } },
    required: ["messages"],
  },
})}]});
else if(method==="tools/call")w({content:[{type:"text",text:'{"ok":true}'}]});
else w({})}});
`;
let renderStubPath: string | null = null;
function ensureRenderStub(): string {
  if (!renderStubPath) {
    const p = join(tmpdir(), "neko-render-mcp-server.mjs");
    writeFileSync(p, RENDER_MCP_STUB_SOURCE);
    renderStubPath = p;
  }
  return renderStubPath;
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

const DEFAULT_TIMEOUT_MS = 5 * 60_000;

export class HermesBackend implements AgentBackend {
  readonly id = "hermes" as const;
  readonly capabilities = {
    // ACP mounts MCP servers as stdio children (session/new mcpServers) — the
    // neko servers ride a bridge process (OPENNEKO_MCP_BRIDGE) that rebuilds
    // each one over the broker control plane. Surfaces still come via fence
    // (see surface.ts).
    mcpTools: true,
    sdkStopHook: false,
    sessionResume: false,
    canUseToolGate: false,
  } as const;

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
    } = opts;

    const fullPrompt = userMessage
      ? `${prompt}\n\nCurrent user message:\n${userMessage}`
      : prompt;

    if (onEvent) {
      await onEvent({ type: "status", message: "Hermes is working…" });
    }

    const maxAttempts = onEvent ? 1 : retries + 1;
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
  mcpServerNames: string[];
  mcpBridgeEnv: Record<string, string> | undefined;
};

type RunOnceOutcome = {
  finalText: string;
  rawText?: string;
  error?: string;
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
  if (orgId) {
    env.HERMES_HOME = hermesHomeForOrg(orgId);
  }

  const child = spawn("hermes", ["acp", "--accept-hooks"], {
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
      ...["OPENNEKO_BROKER_URL", "OPENNEKO_BROKER_TOKEN"]
        .map((k) => [k, process.env[k] ?? ""])
        .filter(([, v]) => v),
    ].map(([name, value]) => ({ name, value }));
    const bridgeServers =
      bridgePath && mcpBridgeEnv
        ? mcpServerNames
            .filter((n) => n !== "neko_ui")
            .map((name) => ({
              name,
              command: process.execPath,
              args: ["--import", "tsx/esm", bridgePath, name],
              env: bridgeEnv,
            }))
        : [];
    const mcpServers = [
      ...(wantsCards
        ? [{ name: RENDER_MCP_SERVER_NAME, command: process.execPath, args: [ensureRenderStub()], env: [] }]
        : []),
      ...bridgeServers,
    ];
    const fresh = await client.request<{ sessionId: string }>("session/new", {
      cwd,
      mcpServers,
    });
    const sessionId = fresh.sessionId;

    let accumulatedText = "";
    let emittedOutsideLen = 0;
    let surfaceEmittedDuringStream = false;

    client.onNotification((notif) => {
      const update = notif.update;
      switch (update.sessionUpdate) {
        case "agent_message_chunk": {
          const text = update.content?.text ?? "";
          if (!text) return;
          accumulatedText += text;
          if (onEvent) {
            const outside = outsideFenceText(accumulatedText);
            const delta = outside.slice(emittedOutsideLen);
            if (delta) {
              emittedOutsideLen = outside.length;
              void onEvent({ type: "message", role: "assistant", content: delta });
            }
            // Streaming surface emit: the fence regex requires a closing ```,
            // so a partial fence returns no messages and we wait for the next chunk.
            if (!surfaceEmittedDuringStream) {
              const parsed = extractSurfaceMessages(accumulatedText);
              if (parsed.messages.length > 0) {
                surfaceEmittedDuringStream = true;
                void onEvent({ type: "surface", messages: parsed.messages });
              }
            }
          }
          return;
        }
        case "agent_thought_chunk": {
          if (debug && onEvent) void onEvent({ type: "status", message: "Thinking…" });
          return;
        }
        case "tool_call": {
          if (!onEvent) return;
          // A render_cards call IS the answer surface, not a tool step: pull the
          // a2ui messages from the call input and emit them, skipping the pill.
          if (update.title === RENDER_TOOL_TITLE) {
            const messages = coerceSurfaceMessages(
              (update.rawInput as { messages?: unknown } | undefined)?.messages,
            );
            if (messages.length > 0) {
              surfaceEmittedDuringStream = true;
              void onEvent({ type: "surface", messages });
            }
            return;
          }
          void onEvent({
            type: "tool_start",
            id: update.toolCallId,
            name: update.kind || "tool",
            input: {
              ...(update.title ? { title: update.title } : {}),
              ...(update.locations ? { locations: update.locations } : {}),
              ...(update.rawInput !== undefined ? { rawInput: update.rawInput } : {}),
            },
          });
          return;
        }
        case "tool_call_update": {
          if (!onEvent) return;
          const id = update.toolCallId;
          const status = update.status;
          if (status === "completed" || status === "failed") {
            void onEvent({
              type: "tool_end",
              id,
              result: status === "completed" ? update.rawOutput ?? update.content : undefined,
              error: status === "failed" ? extractErrorText(update.content ?? update.rawOutput) : undefined,
            });
          } else {
            void onEvent({
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
          if (next) void onEvent({ type: "status", message: next.content });
          return;
        }
        default:
          return;
      }
    });

    let promptError: string | undefined;
    try {
      await client.request("session/prompt", {
        sessionId,
        prompt: [{ type: "text", text: prompt }],
      });
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

    let finalText = accumulatedText;
    if (onEvent) {
      const parsed = extractSurfaceMessages(accumulatedText);
      const markdownText = extractMarkdownText(parsed.messages);
      finalText = (markdownText || parsed.text).trim();
      if (parsed.messages.length > 0 && !surfaceEmittedDuringStream) {
        await onEvent({ type: "surface", messages: parsed.messages });
      }
    }

    // rawText keeps every fence (a2ui + builder); finalText above dropped the
    // builder fences when it collapsed to the a2ui markdown. run-chat-turn
    // parses action/workflow/rule/memory fences out of rawText.
    return { finalText: finalText.trim(), rawText: accumulatedText };
  } catch (e) {
    if (spawnError) throw spawnError;
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

function extractErrorText(raw: unknown): string {
  if (typeof raw === "string") return raw;
  if (raw && typeof raw === "object") return JSON.stringify(raw);
  return "Tool failed";
}

