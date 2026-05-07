import { spawnSync } from "node:child_process";
import { query } from "@anthropic-ai/claude-agent-sdk";
import { AgentBackendConfigError, type AgentBackendId } from "../agent-backend";
import { registerAgentCanceller } from "../agent-shutdown";
import { buildWorkPrompt } from "./prompt";
import { buildRenderCardsServer, buildSkillBuilderServer } from "./tools";
import type { WorkAgentBackend, WorkRunInput, WorkRunResult } from "./types";

function claudeBinaryAvailable(): boolean {
  const r = spawnSync("which", ["claude"], { stdio: "ignore" });
  return r.status === 0;
}

export type ClaudeWorkBackendConfig = {
  apiKey: string;
  model: string;
};

export class ClaudeWorkBackend implements WorkAgentBackend {
  readonly id: AgentBackendId = "claude-agent";

  constructor(private readonly config: ClaudeWorkBackendConfig) {
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
        "claude-agent backend requires the `claude` CLI on PATH. Install Claude Code or switch /settings/agent to Hermes.",
      );
    }
  }

  async run(input: WorkRunInput): Promise<WorkRunResult> {
    const prompt = buildWorkPrompt({
      backend: this.id,
      workspace: input.workspace,
      messages: input.messages,
      currentUserMessage: input.currentUserMessage,
      supportsCardTool: true,
      supportsSkillTool: true,
    });
    const renderCardsServer = buildRenderCardsServer(input.onEvent);
    const skillBuilderServer = buildSkillBuilderServer(input.workspace.skillsRoot);
    const tag = `work run ${input.runId}`;
    const existingState = input.backendState["claude-agent"];
    const resume =
      existingState &&
      typeof existingState === "object" &&
      typeof (existingState as { sessionId?: unknown }).sessionId === "string"
        ? (existingState as { sessionId: string }).sessionId
        : undefined;
    const abortController = new AbortController();
    if (input.signal.aborted) abortController.abort();
    const onAbort = () => abortController.abort();
    input.signal.addEventListener("abort", onAbort, { once: true });
    const unregister = registerAgentCanceller(() => abortController.abort());
    let sessionId: string | undefined = resume;
    let lastAssistantText = "";
    let finalText = "";

    try {
      const stream = query({
        prompt: input.currentUserMessage,
        options: {
          model: this.config.model,
          cwd: input.workspace.claudeProjectRoot,
          permissionMode: "bypassPermissions",
          allowDangerouslySkipPermissions: true,
          tools: { type: "preset", preset: "claude_code" },
          systemPrompt: { type: "preset", preset: "claude_code", append: prompt },
          mcpServers: {
            neko_ui: renderCardsServer,
            neko_skills: skillBuilderServer,
          },
          skills: "all",
          resume,
          abortController,
          env: {
            ...process.env,
            ANTHROPIC_API_KEY: this.config.apiKey,
            CLAUDE_CONFIG_DIR: input.workspace.claudeConfigRoot,
            PATH: `${input.workspace.binRoot}:${process.env.PATH || ""}`,
          },
          stderr: input.debug
            ? (data: string) => process.stderr.write(`[claude-work ${tag}] ${data}`)
            : undefined,
        },
      });

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
            } else if (block.type === "tool_use") {
              await input.onEvent({
                type: "tool_start",
                id: String(block.id ?? ""),
                name: String(block.name ?? "unknown"),
                input: block.input,
              });
            }
          }
          continue;
        }
        if (record.type === "user") {
          const blocks =
            (record.message as { content?: unknown[] } | undefined)?.content ?? [];
          for (const block of blocks as Array<Record<string, unknown>>) {
            if (block.type !== "tool_result") continue;
            const text = extractToolResultText(block.content);
            await input.onEvent({
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
            const message = String(record.result ?? "claude-agent failed");
            await input.onEvent({ type: "error", message });
            return {
              backend: this.id,
              status: "failed",
              finalText: "",
              backendState: nextBackendState(input.backendState, sessionId),
              error: message,
            };
          }
        }
      }

      const assistantText = (finalText || lastAssistantText).trim();
      if (assistantText) {
        await input.onEvent({ type: "message", role: "assistant", content: assistantText });
      }

      return {
        backend: this.id,
        status: input.signal.aborted ? "cancelled" : "completed",
        finalText: assistantText,
        backendState: nextBackendState(input.backendState, sessionId),
      };
    } catch (error) {
      if ((error as Error)?.name === "AbortError" || input.signal.aborted) {
        return {
          backend: this.id,
          status: "cancelled",
          finalText: "",
          backendState: nextBackendState(input.backendState, sessionId),
        };
      }
      const message = error instanceof Error ? error.message : String(error);
      await input.onEvent({ type: "error", message });
      return {
        backend: this.id,
        status: "failed",
        finalText: "",
        backendState: nextBackendState(input.backendState, sessionId),
        error: message,
      };
    } finally {
      unregister();
      input.signal.removeEventListener("abort", onAbort);
    }
  }
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
