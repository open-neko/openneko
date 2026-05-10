/**
 * Hermes backend — drives `hermes acp` (ACP / JSON-RPC over stdio) to get a
 * typed event stream of message chunks, tool calls, and tool results.
 *
 *   - sync mode (no onEvent): collect all `agent_message_chunk` text into
 *     `finalText`; return raw (caller's parseJsonFromOutput / stripFences
 *     handles fences).
 *   - streaming mode (onEvent): emit incremental `message` events, tool
 *     events, and `surface` if accumulated text contains a neko_a2ui fence
 *     (fence stripped from finalText in this path).
 *
 * Each turn opens a fresh ACP session (no session/load) — the caller's
 * prompt already carries history (see packages/llm/src/work/prompt.ts:108);
 * Hermes ACP would replay that history again on session/load, double-counting
 * context. backendState round-trips unchanged.
 */

import { spawn, type ChildProcess } from "node:child_process";
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
import {
  AcpProtocolError,
  createAcpClient,
  type AcpClient,
  type AcpNotification,
} from "./hermes-acp-client";
import { extractSurfaceMessages } from "./surface";

export { extractSurfaceMessages } from "./surface";

function killProcessGroup(child: ChildProcess, signal: NodeJS.Signals): void {
  if (!child.pid) return;
  try {
    process.kill(-child.pid, signal);
  } catch {
    // ESRCH if already exited
  }
  // Also signal the child directly. process.kill(-pid) fails when the child
  // is not a process group leader — e.g. under test mocks where pid is fake.
  // Real Hermes is detached, so the group kill above hits the same target;
  // sending twice is idempotent for SIGTERM/SIGKILL.
  try {
    child.kill(signal);
  } catch {
    // ignore
  }
}

const FENCE_RE = /^\s*```(?:json)?\s*([\s\S]*?)\s*```\s*$/;

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
};

type RunOnceOutcome = {
  finalText: string;
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

  const stderrChunks: Buffer[] = [];
  let stderrLineBuffer = "";
  child.stderr?.on("data", (c: Buffer) => {
    stderrChunks.push(c);
    if (onEvent) {
      const { lines, rest } = consumeStatusLines(stderrLineBuffer + c.toString("utf8"));
      stderrLineBuffer = rest;
      for (const line of lines) {
        void onEvent({ type: "status", message: line });
      }
    }
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

    // Always start a fresh ACP session per turn. Hermes session/load replays
    // the entire conversation as session/update notifications, but the worker
    // already injects "Conversation so far:..." into the prompt
    // (packages/llm/src/work/prompt.ts:108) — using session/load on top of
    // that double-counts context.
    const fresh = await client.request<{ sessionId: string }>("session/new", {
      cwd,
      mcpServers: [],
    });
    const sessionId = fresh.sessionId;

    let accumulatedText = "";
    let lastEmittedAssistantText = "";

    client.onNotification((notif) => {
      const update = notif.update;
      switch (update.sessionUpdate) {
        case "agent_message_chunk": {
          const text = update.content?.text ?? "";
          if (!text) return;
          accumulatedText += text;
          if (onEvent) {
            const trimmed = accumulatedText.trim();
            if (trimmed && trimmed !== lastEmittedAssistantText) {
              lastEmittedAssistantText = trimmed;
              void onEvent({ type: "message", role: "assistant", content: trimmed });
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
      finalText = parsed.text;
      if (parsed.messages.length > 0) {
        await onEvent({ type: "surface", messages: parsed.messages });
      }
      if (finalText && finalText !== lastEmittedAssistantText) {
        await onEvent({ type: "message", role: "assistant", content: finalText });
      }
    }

    return { finalText: finalText.trim() };
  } catch (e) {
    if (spawnError) throw spawnError;
    throw e;
  } finally {
    client?.dispose();
    if (child.exitCode == null && !signal?.aborted) {
      killProcessGroup(child, "SIGTERM");
    }
    await closedPromise.catch(() => {});
    if (onEvent && stderrLineBuffer.trim()) {
      void onEvent({ type: "status", message: cleanStatusLine(stderrLineBuffer) });
    }
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

function consumeStatusLines(raw: string): { lines: string[]; rest: string } {
  const parts = raw.split(/\r?\n/);
  const rest = parts.pop() ?? "";
  const lines = parts.map(cleanStatusLine).filter(Boolean).slice(-5);
  return { lines, rest };
}

function cleanStatusLine(raw: string): string {
  return raw.replace(/\[[0-9;]*m/g, "").trim();
}

