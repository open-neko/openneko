/**
 * Single agent backend contract — used by both Dashboard (sync metric_refresh
 * jobs) and Work (streaming chat). The backend never branches on which
 * surface called it; the difference is whether `onEvent` is provided. With
 * `onEvent`, the backend emits incremental events; without, the caller just
 * reads `finalText` from the result.
 *
 * Backends today:
 *   - HermesBackend       (subprocess; multi-provider via ~/.hermes/config.yaml)
 *   - ClaudeAgentBackend  (in-process; locked to Anthropic via @anthropic-ai/claude-agent-sdk)
 *
 * The contract:
 *   - input: prompt + optional workspace/skills/onEvent/signal/backendState
 *   - output: AgentRunResult { finalText, status, backendState?, error? }
 *   - failures return status='failed' (no thrown errors for agent-level
 *     failure). True misconfiguration still throws AgentBackendConfigError
 *     at construction time, which the resolver catches.
 *
 * LLM provider config is per-org: every run reads
 * <hermesHomeForOrg(orgId)>/config.yaml. /settings/agent →
 * provisionHostConfig writes the same path, so a chat answer and a
 * dashboard card for the same org always run on the same model. The
 * Hermes backend sets HERMES_HOME on spawn so the host's global
 * ~/.hermes/ (where the user's own `hermes` CLI sessions live) cannot
 * leak credential-pool drift into our long-running agents.
 */

export const AGENT_BACKEND_IDS = ["hermes", "claude-agent"] as const;
export type AgentBackendId = (typeof AGENT_BACKEND_IDS)[number];

/**
 * Default concurrency cap for the metric agent. Overridable per-org via
 * /settings/agent (persisted in llm_provider_config scope='agent').
 *
 * Realized as N pg-boss workers (each batchSize=1) on the metric_refresh
 * queue and as the size of the in-process semaphore on the claude-agent
 * path. Single knob, applied uniformly regardless of backend.
 */
export const AGENT_DEFAULT_GLOBAL_CAP = 20;

export const AGENT_BACKEND_OPTIONS = [
  {
    value: "hermes",
    label: "Hermes",
    description: "Subprocess agent. Works with any LLM provider.",
  },
  {
    value: "claude-agent",
    label: "Claude Agent",
    description: "In-process. Locked to Anthropic Claude models.",
  },
] as const;

export function isAgentBackendId(value: string): value is AgentBackendId {
  return (AGENT_BACKEND_IDS as readonly string[]).includes(value);
}

/**
 * The name of the shell-execution tool the agent will see in its tool
 * palette. Used by prompt builders so we instruct the agent with the
 * actual tool name it has access to, instead of a generic "Bash" that
 * doesn't exist in Hermes' palette.
 *
 *   claude-agent → "Bash"     (matches @anthropic-ai/claude-agent-sdk preset)
 *   hermes       → "terminal" (matches Hermes' built-in `terminal` toolset)
 */
export function shellToolName(backendId: AgentBackendId): string {
  return backendId === "claude-agent" ? "Bash" : "terminal";
}

/* ─── Workspace / streaming types (shared by Dashboard and Work) ─── */

export type AgentSurfaceMessage = {
  version: "v0.9";
  [key: string]: unknown;
};

export type AgentArtifact = {
  path: string;
  label: string;
  mimeType?: string;
};

export type AgentEvent =
  | { type: "message"; role: "user" | "assistant"; content: string }
  | { type: "tool_start"; id: string; name: string; input?: unknown }
  | { type: "tool_delta"; id: string; delta: unknown }
  | { type: "tool_end"; id: string; result?: unknown; error?: string }
  | { type: "surface"; messages: AgentSurfaceMessage[] }
  | { type: "artifact"; artifact: AgentArtifact }
  | { type: "status"; message: string }
  | { type: "error"; message: string }
  | { type: "done"; result?: unknown };

export type AgentChatMessage = {
  id?: string;
  role: "user" | "assistant";
  content: string;
  runId?: string | null;
  createdAt?: string;
};

/**
 * Per-org / per-run filesystem layout passed to streaming runs. Dashboard
 * runs leave this undefined and let the backend pick a scratch dir.
 *
 * `hermesHome` is intentionally not on this struct — it's resolved from
 * `orgId` by `hermesHomeForOrg(orgId)` in host-provision.ts, which keeps
 * the writer (provisionHostConfig) and the reader (HermesBackend.spawn)
 * agreeing on the path by construction.
 */
export type AgentWorkspace = {
  orgRoot: string;
  skillsRoot: string;
  memoryRoot: string;
  knowledgeRoot: string;
  uploadsRoot: string;
  runsRoot: string;
  threadUploadsRoot: string;
  runRoot: string;
  artifactRoot: string;
  binRoot: string;
  claudeProjectRoot: string;
  claudeConfigRoot: string;
};

export type AgentRunOptions = {
  /**
   * The full prompt (system instructions + history + current user input,
   * already assembled by the caller). Hermes passes this as `-z`. Claude
   * passes it as the SDK prompt (or as systemPrompt.append when
   * `userMessage` is also set).
   */
  prompt: string;
  /**
   * Optional separate "user input" for backends that distinguish
   * (Claude SDK uses it as the actual user message; Hermes appends it
   * to `prompt` if set).
   */
  userMessage?: string;
  /** Hard cap on the run lifetime. Default 5 minutes. */
  timeoutMs?: number;
  /** Total attempts on transport-level failure. Default 1 retry; ignored when streaming. */
  retries?: number;
  /** Pipe backend stderr / progress output to the parent process's stderr. */
  debug?: boolean;
  /** Identifier for correlation (e.g. processing_job UUID). */
  tag?: string;
  /**
   * Owning org id. The Hermes backend uses it to resolve a per-org
   * HERMES_HOME so the credential pool is isolated from the host's
   * global ~/.hermes/. Optional for backward compatibility — when
   * absent, the backend falls back to whatever HERMES_HOME the host
   * has set (or Hermes' default ~/.hermes/).
   */
  orgId?: string;
  /** Per-org / per-run filesystem layout. Streaming callers pass this; sync callers don't. */
  workspace?: AgentWorkspace;
  /** Skill names to expose (`--skills` for Hermes; SDK `skills` for Claude). */
  skills?: string[];
  /** Cancel signal. */
  signal?: AbortSignal;
  /** Event sink. Presence flips the backend into streaming mode. */
  onEvent?: (event: AgentEvent) => Promise<void> | void;
  /** Backend-keyed resume state (e.g. { "claude-agent": { sessionId } }). */
  backendState?: Record<string, unknown>;
  /**
   * MCP servers to install on the run (Claude SDK only; Hermes ignores).
   * Constructed by the caller because they typically capture per-run
   * state (onEvent closure, workspace.skillsRoot path).
   */
  mcpServers?: Record<string, unknown>;
};

export type AgentRunResult = {
  finalText: string;
  status: "completed" | "failed" | "cancelled";
  backendState?: Record<string, unknown>;
  error?: string;
};

export interface AgentBackend {
  readonly id: AgentBackendId;
  /**
   * Run the agent. Always returns an AgentRunResult — agent-level failures
   * surface as status='failed' with `error` set, never as thrown exceptions.
   * Only construction-time misconfiguration throws (AgentBackendConfigError).
   */
  run(opts: AgentRunOptions): Promise<AgentRunResult>;
}

/**
 * Misconfiguration that the user can fix in /settings (e.g. picked
 * claude-agent but no Anthropic key on the primary provider). Distinct from
 * generic Errors so callers can render a "go fix your settings" message
 * instead of a stack trace.
 */
export class AgentBackendConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AgentBackendConfigError";
  }
}
