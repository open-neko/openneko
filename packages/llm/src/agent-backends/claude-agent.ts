/**
 * Claude Agent backend — wraps the @anthropic-ai/claude-agent-sdk (which
 * itself spawns the Claude Code CLI as a subprocess and proxies messages
 * over stdio). Handles both Dashboard (sync) and Work (streaming) calls.
 *
 *   - sync mode: pass `prompt` as the SDK input; collect the final
 *     `result.subtype === "success"` text; return as `finalText`. No
 *     workspace, no resume, no events.
 *   - streaming mode: with `onEvent`, additionally:
 *       - cwd = workspace.claudeProjectRoot
 *       - CLAUDE_CONFIG_DIR = workspace.claudeConfigRoot
 *       - PATH prepended with workspace.binRoot (graphjin guard)
 *       - resume from backendState["claude-agent"].sessionId if present
 *       - emit tool_start/tool_end/message events from the SDK stream
 *       - install MCP servers passed in via opts.mcpServers
 *
 * Auth: pulls the Anthropic API key + Claude model from the primary
 * `llm_provider_config` row. UI enforces (and `agent-backend-resolver` re-
 * validates) that `provider === 'anthropic'` and the model starts with
 * `claude-` whenever the agent backend is `claude-agent`.
 */

import { spawnSync } from "node:child_process";
import { query } from "@anthropic-ai/claude-agent-sdk";
import {
  AgentBackendConfigError,
  type AgentBackend,
  type AgentRunOptions,
  type AgentRunResult,
} from "../agent-backend";
import { registerAgentCanceller } from "../agent-shutdown";

let _claudeOnPathChecked = false;
let _claudeOnPath = false;

function claudeBinaryAvailable(): boolean {
  if (_claudeOnPathChecked) return _claudeOnPath;
  _claudeOnPathChecked = true;
  const r = spawnSync("which", ["claude"], { stdio: "ignore" });
  _claudeOnPath = r.status === 0;
  return _claudeOnPath;
}

export type ClaudeAgentBackendConfig = {
  apiKey: string;
  model: string;
};

const DEFAULT_TIMEOUT_MS = 5 * 60_000;
const DEFAULT_RETRIES = 1;
const MAX_TURNS = 25;

export class ClaudeAgentBackend implements AgentBackend {
  readonly id = "claude-agent" as const;

  constructor(private readonly config: ClaudeAgentBackendConfig) {
    if (!config.apiKey) {
      throw new AgentBackendConfigError(
        "claude-agent backend requires an Anthropic API key on the primary provider settings.",
      );
    }
    if (!config.model || !config.model.toLowerCase().startsWith("claude-")) {
      throw new AgentBackendConfigError(
        `claude-agent backend requires a Claude model on the primary provider settings (got "${config.model || "(empty)"}").`,
      );
    }
    if (!claudeBinaryAvailable()) {
      throw new AgentBackendConfigError(
        "claude-agent backend requires the `claude` CLI on PATH. The npm package @anthropic-ai/claude-agent-sdk spawns it as a subprocess. Install Claude Code (https://claude.com/claude-code) or switch /settings/agent to Hermes.",
      );
    }
  }

  async run(opts: AgentRunOptions): Promise<AgentRunResult> {
    const {
      prompt,
      userMessage,
      timeoutMs = DEFAULT_TIMEOUT_MS,
      retries = DEFAULT_RETRIES,
      debug = false,
      tag,
      workspace,
      signal,
      onEvent,
      backendState = {},
      mcpServers,
    } = opts;

    const streaming = !!onEvent;
    // Streaming mode is one-shot per turn — Work runs commit user state
    // before invoking us, retrying would double-write events.
    const maxAttempts = streaming ? 1 : retries + 1;

    let lastErr: Error | undefined;
    let lastSessionId: string | undefined;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      try {
        const out = await this.runOnce({
          prompt,
          userMessage,
          timeoutMs,
          debug,
          tag,
          workspace,
          signal,
          onEvent,
          backendState,
          mcpServers,
        });
        if (out.sessionId) lastSessionId = out.sessionId;
        if (out.error) {
          lastErr = new Error(out.error);
          if (debug) {
            console.warn(
              `[claude-agent] attempt ${attempt + 1}/${maxAttempts} failed: ${out.error}`,
            );
          }
          continue;
        }
        if (streaming && out.finalText) {
          await onEvent!({ type: "message", role: "assistant", content: out.finalText });
        }
        return {
          finalText: out.finalText,
          status: signal?.aborted ? "cancelled" : "completed",
          backendState: nextBackendState(backendState, out.sessionId ?? lastSessionId),
        };
      } catch (e) {
        lastErr = e instanceof Error ? e : new Error(String(e));
        if (debug) {
          console.warn(
            `[claude-agent] attempt ${attempt + 1}/${maxAttempts} failed: ${lastErr.message}`,
          );
        }
      }
    }

    const message = lastErr?.message ?? "claude-agent: unknown failure";
    if (signal?.aborted) {
      return {
        finalText: "",
        status: "cancelled",
        backendState: nextBackendState(backendState, lastSessionId),
      };
    }
    if (streaming) await onEvent!({ type: "error", message });
    return {
      finalText: "",
      status: "failed",
      backendState: nextBackendState(backendState, lastSessionId),
      error: message,
    };
  }

  private async runOnce(args: {
    prompt: string;
    userMessage: string | undefined;
    timeoutMs: number;
    debug: boolean;
    tag: string | undefined;
    workspace: AgentRunOptions["workspace"];
    signal: AbortSignal | undefined;
    onEvent: AgentRunOptions["onEvent"];
    backendState: Record<string, unknown>;
    mcpServers: AgentRunOptions["mcpServers"];
  }): Promise<{ finalText: string; sessionId?: string; error?: string }> {
    const {
      prompt,
      userMessage,
      timeoutMs,
      debug,
      tag,
      workspace,
      signal,
      onEvent,
      backendState,
      mcpServers,
    } = args;

    const abortController = new AbortController();
    const timer = setTimeout(() => abortController.abort(), timeoutMs);
    if (signal) {
      if (signal.aborted) abortController.abort();
      else signal.addEventListener("abort", () => abortController.abort(), { once: true });
    }
    const tagSuffix = tag ? ` tag=${tag}` : "";
    const unregister = registerAgentCanceller(() => abortController.abort());

    const resume = readSessionId(backendState["claude-agent"]);
    let sessionId = resume;
    let lastAssistantText = "";
    let finalText = "";

    // SDK input vs system prompt:
    //   - sync mode: prompt = the entire single-shot prompt → SDK prompt
    //   - streaming mode: caller already includes history/system in prompt;
    //     userMessage (when set) is the separate fresh user input. We pass
    //     userMessage as the SDK prompt so Claude's sees the latest turn,
    //     and stuff the system context via systemPrompt.append.
    const sdkPrompt = userMessage ?? prompt;
    const sdkOptions: Record<string, unknown> = {
      model: this.config.model,
      maxTurns: MAX_TURNS,
      permissionMode: "bypassPermissions",
      tools: { type: "preset", preset: "claude_code" },
      env: {
        ...process.env,
        ANTHROPIC_API_KEY: this.config.apiKey,
        ...(workspace
          ? {
              CLAUDE_CONFIG_DIR: workspace.claudeConfigRoot,
              PATH: `${workspace.binRoot}:${process.env.PATH || ""}`,
            }
          : {}),
      },
      abortController,
      stderr: debug
        ? (data: string) => process.stderr.write(`[claude-agent${tagSuffix}] ${data}`)
        : undefined,
    };
    if (workspace) {
      sdkOptions.cwd = workspace.claudeProjectRoot;
      sdkOptions.allowDangerouslySkipPermissions = true;
      sdkOptions.skills = "all";
      if (mcpServers) sdkOptions.mcpServers = mcpServers;
      if (resume) sdkOptions.resume = resume;
      if (userMessage) {
        sdkOptions.systemPrompt = { type: "preset", preset: "claude_code", append: prompt };
      }
    }

    try {
      const stream = query({ prompt: sdkPrompt, options: sdkOptions });

      for await (const message of stream) {
        const record = message as Record<string, unknown>;
        if (record.type === "system" && record.subtype === "init") {
          if (typeof record.session_id === "string") sessionId = record.session_id;
          continue;
        }
        if (record.type === "assistant") {
          const blocks =
            (record.message as { content?: unknown[] } | undefined)?.content ?? [];
          for (const block of blocks as Array<Record<string, unknown>>) {
            if (block.type === "text" && typeof block.text === "string") {
              const text = block.text.trim();
              if (text) lastAssistantText = text;
            } else if (block.type === "tool_use" && onEvent) {
              await onEvent({
                type: "tool_start",
                id: String(block.id ?? ""),
                name: String(block.name ?? "unknown"),
                input: block.input,
              });
            }
          }
          continue;
        }
        if (record.type === "user" && onEvent) {
          const blocks =
            (record.message as { content?: unknown[] } | undefined)?.content ?? [];
          for (const block of blocks as Array<Record<string, unknown>>) {
            if (block.type !== "tool_result") continue;
            const text = extractToolResultText(block.content);
            await onEvent({
              type: "tool_end",
              id: String(block.tool_use_id ?? ""),
              result: text || undefined,
              error: block.is_error ? text || "Tool failed" : undefined,
            });
          }
          continue;
        }
        if (record.type === "result") {
          if (typeof record.session_id === "string") sessionId = record.session_id;
          if (record.subtype === "success") {
            finalText = String(record.result ?? lastAssistantText ?? "").trim();
          } else {
            return {
              finalText: "",
              sessionId,
              error: String(record.result ?? `claude-agent ${record.subtype ?? "failed"}`),
            };
          }
        }
      }

      return { finalText: (finalText || lastAssistantText).trim(), sessionId };
    } finally {
      clearTimeout(timer);
      unregister();
    }
  }
}

function readSessionId(state: unknown): string | undefined {
  if (state && typeof state === "object") {
    const sid = (state as { sessionId?: unknown }).sessionId;
    if (typeof sid === "string") return sid;
  }
  return undefined;
}

function nextBackendState(
  current: Record<string, unknown>,
  sessionId: string | undefined,
): Record<string, unknown> {
  if (!sessionId) return current;
  return {
    ...current,
    "claude-agent": { sessionId },
  };
}

function extractToolResultText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((item) => {
      const block = item as Record<string, unknown>;
      return block.type === "text" && typeof block.text === "string" ? block.text : "";
    })
    .join("");
}
