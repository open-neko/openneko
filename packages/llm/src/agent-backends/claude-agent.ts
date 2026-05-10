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
    let partialAssistantText = "";
    let lastAssistantText = "";
    let lastEmittedAssistantText = "";
    let finalText = "";
    const toolNames = new Map<string, string>();

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
      sdkOptions.includeHookEvents = true;
      sdkOptions.includePartialMessages = true;
      sdkOptions.agentProgressSummaries = true;
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
        if (record.type === "system" && onEvent) {
          const statusMessage = describeSystemStatus(record);
          if (statusMessage) {
            await onEvent({ type: "status", message: statusMessage });
          }
          continue;
        }
        if (record.type === "tool_progress" && onEvent) {
          const toolUseId = String(record.tool_use_id ?? "");
          const toolName = String(record.tool_name ?? "tool");
          toolNames.set(toolUseId, toolName);
          await onEvent({
            type: "tool_delta",
            id: toolUseId,
            delta: {
              elapsedSeconds: Number(record.elapsed_time_seconds ?? 0),
              message: `${toolName} is running…`,
            },
          });
          continue;
        }
        if (record.type === "tool_use_summary" && onEvent) {
          const summary = typeof record.summary === "string" ? record.summary.trim() : "";
          if (summary) {
            await onEvent({ type: "status", message: summary });
          }
          continue;
        }
        if (record.type === "auth_status" && onEvent) {
          if (record.isAuthenticating) {
            await onEvent({ type: "status", message: "Authenticating Claude Agent…" });
          }
          continue;
        }
        if (record.type === "stream_event" && onEvent) {
          const deltaText = extractPartialAssistantText(record.event);
          if (deltaText) {
            partialAssistantText += deltaText;
            const streamedText = partialAssistantText.trim();
            if (streamedText && streamedText !== lastEmittedAssistantText) {
              lastAssistantText = streamedText;
              lastEmittedAssistantText = streamedText;
              await onEvent({
                type: "message",
                role: "assistant",
                content: streamedText,
              });
            }
          }
          continue;
        }
        if (record.type === "assistant") {
          const blocks =
            (record.message as { content?: unknown[] } | undefined)?.content ?? [];
          let sawText = false;
          for (const block of blocks as Array<Record<string, unknown>>) {
            if (block.type === "text" && typeof block.text === "string") {
              const text = block.text.trim();
              if (text) {
                partialAssistantText = text;
                lastAssistantText = text;
                sawText = true;
              }
            } else if (block.type === "tool_use" && onEvent) {
              const toolId = String(block.id ?? "");
              const toolName = String(block.name ?? "unknown");
              toolNames.set(toolId, toolName);
              await onEvent({
                type: "tool_start",
                id: toolId,
                name: toolName,
                input: block.input,
              });
              await onEvent({
                type: "status",
                message: `Using ${toolName}…`,
              });
            }
          }
          if (sawText && onEvent && lastAssistantText !== lastEmittedAssistantText) {
            lastEmittedAssistantText = lastAssistantText;
            await onEvent({
              type: "message",
              role: "assistant",
              content: lastAssistantText,
            });
          }
          continue;
        }
        if (record.type === "user" && onEvent) {
          const blocks =
            (record.message as { content?: unknown[] } | undefined)?.content ?? [];
          for (const block of blocks as Array<Record<string, unknown>>) {
            if (block.type !== "tool_result") continue;
            const text = extractToolResultText(block.content);
            const toolUseId = String(block.tool_use_id ?? "");
            await onEvent({
              type: "tool_end",
              id: toolUseId,
              result: text || undefined,
              error: block.is_error ? text || "Tool failed" : undefined,
            });
            const toolName = toolNames.get(toolUseId);
            if (toolName) {
              await onEvent({
                type: "status",
                message: block.is_error
                  ? `${toolName} failed.`
                  : `${toolName} finished.`,
              });
            }
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

function extractPartialAssistantText(event: unknown): string {
  if (!event || typeof event !== "object") return "";
  const payload = event as {
    type?: unknown;
    delta?: { text?: unknown };
  };
  if (payload.type !== "content_block_delta") return "";
  return typeof payload.delta?.text === "string" ? payload.delta.text : "";
}

function describeSystemStatus(record: Record<string, unknown>): string | null {
  switch (record.subtype) {
    case "status": {
      const status = record.status;
      if (status === "requesting") return "Claude is reasoning…";
      if (status === "compacting") return "Claude is compacting context…";
      if (record.compact_result === "failed" && typeof record.compact_error === "string") {
        return `Context compaction failed: ${record.compact_error}`;
      }
      return null;
    }
    case "task_started": {
      const description = typeof record.description === "string" ? record.description.trim() : "";
      return description || "Started a task…";
    }
    case "task_progress": {
      const summary = typeof record.summary === "string" ? record.summary.trim() : "";
      if (summary) return summary;
      const description = typeof record.description === "string" ? record.description.trim() : "";
      const lastToolName =
        typeof record.last_tool_name === "string" ? record.last_tool_name.trim() : "";
      if (description && lastToolName) return `${description} (${lastToolName})`;
      return description || null;
    }
    case "task_updated": {
      const patch = record.patch as { status?: unknown; error?: unknown } | undefined;
      if (patch?.status === "completed") return "Task completed.";
      if (patch?.status === "failed") {
        return typeof patch.error === "string" && patch.error
          ? `Task failed: ${patch.error}`
          : "Task failed.";
      }
      return null;
    }
    case "hook_progress":
    case "hook_response": {
      const output = typeof record.output === "string" ? record.output.trim() : "";
      if (output) return output;
      const stderr = typeof record.stderr === "string" ? record.stderr.trim() : "";
      if (stderr) return stderr;
      const stdout = typeof record.stdout === "string" ? record.stdout.trim() : "";
      if (stdout) return stdout;
      const hookName = typeof record.hook_name === "string" ? record.hook_name : "hook";
      return `${hookName} is running…`;
    }
    default:
      return null;
  }
}
