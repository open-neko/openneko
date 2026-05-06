/**
 * Claude Agent backend — drives Anthropic's @anthropic-ai/claude-agent-sdk
 * (which itself spawns the Claude Code CLI as a subprocess and proxies messages
 * over stdio). From our worker's perspective it's another opaque "give it a
 * prompt, get a string" backend.
 *
 * Trust boundary: full Claude Code tool preset (Bash, Read, Write, Edit,
 * Grep, Glob, etc.) — same as the Hermes backend, which by default grants
 * its agent access to file ops + shell. permissionMode='bypassPermissions'
 * runs without human-in-the-loop. The worker process is already trusted to
 * spawn arbitrary subprocesses, so this is parity, not a new surface.
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
} from "../agent-backend";
import { registerAgentCanceller } from "../agent-shutdown";

let _claudeOnPathChecked = false;
let _claudeOnPath = false;

function claudeBinaryAvailable(): boolean {
  if (_claudeOnPathChecked) return _claudeOnPath;
  _claudeOnPathChecked = true;
  // The SDK spawns the `claude` CLI under the hood. Without it on PATH
  // (or without pathToClaudeCodeExecutable pointed at a real binary),
  // every call would fail with an unhelpful spawn error. Surface a
  // typed error up-front instead.
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
// Hardcoded; surface as a setting on /settings/agent if/when there's a need.
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

  async run(opts: AgentRunOptions): Promise<string> {
    const {
      prompt,
      timeoutMs = DEFAULT_TIMEOUT_MS,
      retries = DEFAULT_RETRIES,
      debug = false,
      tag,
    } = opts;

    let lastErr: Error | undefined;
    for (let attempt = 0; attempt <= retries; attempt++) {
      try {
        return await this.runOnce(prompt, timeoutMs, debug, tag);
      } catch (e) {
        lastErr = e instanceof Error ? e : new Error(String(e));
        if (debug) {
          console.warn(
            `[claude-agent] attempt ${attempt + 1}/${retries + 1} failed: ${lastErr.message}`,
          );
        }
      }
    }
    throw lastErr ?? new Error("claude-agent: unknown failure");
  }

  private async runOnce(
    prompt: string,
    timeoutMs: number,
    debug: boolean,
    tag: string | undefined,
  ): Promise<string> {
    const abort = new AbortController();
    const timer = setTimeout(() => abort.abort(), timeoutMs);
    const tagSuffix = tag ? ` tag=${tag}` : "";
    // Register so the worker's SIGTERM handler can fail-fast every
    // in-flight Claude Agent call alongside any Hermes children.
    const unregisterCancel = registerAgentCanceller(() => abort.abort());

    try {
      const stream = query({
        prompt,
        options: {
          abortController: abort,
          model: this.config.model,
          maxTurns: MAX_TURNS,
          permissionMode: "bypassPermissions",
          // Full Claude Code tool preset (Bash, Read, Write, Edit, Grep,
          // Glob, etc.). Matches Hermes's default-tool surface so the two
          // backends compete on equal footing. The metric agent's prompt
          // tells the agent to use Bash for graphjin queries; nothing
          // obliges it to ignore other tools if they help.
          tools: { type: "preset", preset: "claude_code" },
          env: {
            ...process.env,
            ANTHROPIC_API_KEY: this.config.apiKey,
          },
          stderr: debug
            ? (data: string) => process.stderr.write(`[claude-agent${tagSuffix}] ${data}`)
            : undefined,
        },
      });

      let finalResult: string | null = null;
      let lastAssistantText = "";

      for await (const message of stream) {
        if (message.type === "assistant" && !message.error) {
          const text = extractAssistantText(message);
          if (text) lastAssistantText = text;
        } else if (message.type === "result") {
          if (message.subtype === "success") {
            finalResult = message.result;
          } else {
            throw new Error(
              `claude-agent error: ${(message as { subtype?: string }).subtype ?? "unknown"}`,
            );
          }
          break;
        }
      }

      const out = finalResult ?? lastAssistantText;
      if (!out) {
        throw new Error("claude-agent produced no assistant output");
      }
      return out;
    } finally {
      clearTimeout(timer);
      unregisterCancel();
    }
  }
}

type AssistantMessageLike = {
  message?: {
    content?: Array<{ type?: string; text?: string }> | string;
  };
};

function extractAssistantText(msg: unknown): string {
  const m = (msg as AssistantMessageLike).message;
  if (!m || !m.content) return "";
  if (typeof m.content === "string") return m.content;
  return m.content
    .filter((block) => block.type === "text" && typeof block.text === "string")
    .map((block) => block.text!)
    .join("");
}
