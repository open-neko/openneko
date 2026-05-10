/**
 * Claude Agent backend — wraps `@anthropic-ai/claude-agent-sdk` (which spawns
 * the Claude Code CLI as a subprocess and proxies messages over stdio).
 *
 *   - sync mode: pass `prompt` as the SDK input; collect `result.subtype === "success"`
 *     text; return as `finalText`. No workspace, no resume, no events.
 *   - streaming mode (`onEvent`): cwd = workspace.claudeProjectRoot,
 *     CLAUDE_CONFIG_DIR = workspace.claudeConfigRoot, PATH prepended with
 *     workspace.binRoot (graphjin guard), resume from
 *     backendState["claude-agent"].sessionId, emit tool_start/tool_end/message
 *     events from the SDK stream, install MCP servers passed in via
 *     opts.mcpServers, surface (neko_a2ui) extraction.
 *
 * Auth: pulls the Anthropic API key + Claude model from the primary
 * `llm_provider_config` row. UI enforces (and `agent-backend-resolver` re-
 * validates) `provider === 'anthropic'` and a `claude-` model.
 *
 * Caller-extensible SDK features (all optional, all forwarded verbatim):
 *   skills, outputSchema, forkSession, agents, hooks, canUseTool,
 *   onElicitation. When `canUseTool` is set, `bypassPermissions` is dropped —
 *   the SDK semantics short-circuit canUseTool under bypass.
 *
 * Internal hooks: a PostToolUse hook captures `duration_ms` so `tool_end`
 * events carry timing. Caller-supplied hooks are merged on top.
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
import { extractSurfaceMessages } from "./surface";

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
      timeoutMs = DEFAULT_TIMEOUT_MS,
      retries = DEFAULT_RETRIES,
      debug = false,
      signal,
      onEvent,
      backendState = {},
    } = opts;

    const streaming = !!onEvent;
    const maxAttempts = streaming ? 1 : retries + 1;

    let lastErr: Error | undefined;
    let lastSessionId: string | undefined;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      try {
        const out = await this.runOnce(opts, timeoutMs);
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
        if (streaming && out.surfaceMessages.length > 0) {
          await onEvent!({ type: "surface", messages: out.surfaceMessages });
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

  private async runOnce(
    opts: AgentRunOptions,
    timeoutMs: number,
  ): Promise<{
    finalText: string;
    surfaceMessages: import("../agent-backend").AgentSurfaceMessage[];
    sessionId?: string;
    error?: string;
  }> {
    const {
      prompt,
      userMessage,
      debug = false,
      tag,
      workspace,
      signal,
      onEvent,
      backendState = {},
      mcpServers,
      skills,
      outputSchema,
      forkSession,
      agents,
      hooks: callerHooks,
      canUseTool,
      onElicitation,
    } = opts;

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
    let accumulatedText = "";
    let lastEmittedAssistantText = "";
    let finalText = "";
    let surfaceMessages: import("../agent-backend").AgentSurfaceMessage[] = [];
    const toolDurations = new Map<string, number>();

    const sdkPrompt = userMessage ?? prompt;
    const sdkOptions: Record<string, unknown> = {
      model: this.config.model,
      maxTurns: MAX_TURNS,
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

    if (canUseTool) {
      sdkOptions.canUseTool = canUseTool;
    } else {
      sdkOptions.permissionMode = "bypassPermissions";
    }
    if (outputSchema) {
      sdkOptions.outputFormat = { type: "json_schema", schema: outputSchema };
    }
    if (onElicitation) sdkOptions.onElicitation = onElicitation;

    if (workspace) {
      sdkOptions.cwd = workspace.claudeProjectRoot;
      if (!canUseTool) sdkOptions.allowDangerouslySkipPermissions = true;
      sdkOptions.skills = skills && skills.length > 0 ? skills : "all";
      sdkOptions.includeHookEvents = true;
      sdkOptions.includePartialMessages = true;
      sdkOptions.agentProgressSummaries = true;
      if (mcpServers) sdkOptions.mcpServers = mcpServers;
      if (resume) sdkOptions.resume = resume;
      if (forkSession) sdkOptions.forkSession = forkSession;
      if (agents) sdkOptions.agents = agents;
      if (userMessage) {
        sdkOptions.systemPrompt = { type: "preset", preset: "claude_code", append: prompt };
      }
    }

    sdkOptions.hooks = mergeHooks(callerHooks, {
      PostToolUse: [
        {
          hooks: [
            async (input: Record<string, unknown>) => {
              const id = String(input.tool_use_id ?? "");
              const ms = typeof input.duration_ms === "number" ? input.duration_ms : undefined;
              if (id && ms !== undefined) toolDurations.set(id, ms);
              return { continue: true };
            },
          ],
        },
      ],
    });

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
          if (statusMessage) await onEvent({ type: "status", message: statusMessage });
          continue;
        }
        if (record.type === "tool_progress" && onEvent) {
          const toolUseId = String(record.tool_use_id ?? "");
          await onEvent({
            type: "tool_delta",
            id: toolUseId,
            delta: { elapsedSeconds: Number(record.elapsed_time_seconds ?? 0) },
          });
          continue;
        }
        if (record.type === "tool_use_summary" && onEvent) {
          const summary = typeof record.summary === "string" ? record.summary.trim() : "";
          const ids = Array.isArray(record.preceding_tool_use_ids)
            ? (record.preceding_tool_use_ids as unknown[]).map(String)
            : [];
          if (summary && ids.length > 0) {
            for (const id of ids) {
              await onEvent({ type: "tool_delta", id, delta: { summary } });
            }
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
            accumulatedText += deltaText;
            const streamedText = accumulatedText.trim();
            if (streamedText && streamedText !== lastEmittedAssistantText) {
              lastEmittedAssistantText = streamedText;
              await onEvent({ type: "message", role: "assistant", content: streamedText });
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
                accumulatedText = text;
                sawText = true;
              }
            } else if (block.type === "tool_use" && onEvent) {
              await onEvent({
                type: "tool_start",
                id: String(block.id ?? ""),
                name: String(block.name ?? "unknown"),
                input: block.input,
              });
            }
          }
          if (sawText && onEvent && accumulatedText !== lastEmittedAssistantText) {
            lastEmittedAssistantText = accumulatedText;
            await onEvent({ type: "message", role: "assistant", content: accumulatedText });
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
            const durationMs = toolDurations.get(toolUseId);
            if (durationMs !== undefined) {
              await onEvent({
                type: "tool_delta",
                id: toolUseId,
                delta: { durationMs },
              });
            }
            await onEvent({
              type: "tool_end",
              id: toolUseId,
              result: text || undefined,
              error: block.is_error ? text || "Tool failed" : undefined,
            });
          }
          continue;
        }
        if (record.type === "result") {
          if (typeof record.session_id === "string") sessionId = record.session_id;
          if (record.subtype === "success") {
            const raw = String(record.result ?? accumulatedText ?? "").trim();
            if (onEvent) {
              const parsed = extractSurfaceMessages(raw);
              finalText = parsed.text;
              surfaceMessages = parsed.messages;
            } else {
              finalText = raw;
            }
          } else {
            return {
              finalText: "",
              surfaceMessages: [],
              sessionId,
              error: String(record.result ?? `claude-agent ${record.subtype ?? "failed"}`),
            };
          }
        }
      }

      const resolved = (finalText || accumulatedText).trim();
      return { finalText: resolved, surfaceMessages, sessionId };
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

type HookMatcher = { matcher?: string; hooks: unknown[]; timeout?: number };

export function mergeHooks(
  caller: Record<string, unknown> | undefined,
  internal: Record<string, HookMatcher[]>,
): Record<string, HookMatcher[]> {
  const merged: Record<string, HookMatcher[]> = {};
  for (const [event, matchers] of Object.entries(internal)) {
    merged[event] = [...matchers];
  }
  if (!caller) return merged;
  for (const [event, matchers] of Object.entries(caller)) {
    if (!Array.isArray(matchers)) continue;
    merged[event] = [...(merged[event] ?? []), ...(matchers as HookMatcher[])];
  }
  return merged;
}
