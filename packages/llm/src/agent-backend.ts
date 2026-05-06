/**
 * Agent backend interface — abstraction over "give the agent a prompt
 * and get its final reply as a string." The metric agent (and any future
 * tool-using agent) calls `backend.run(opts)` instead of binding to a
 * specific implementation.
 *
 * Backends today:
 *   - HermesBackend     (subprocess; multi-provider via ~/.hermes/config.yaml)
 *   - ClaudeAgentBackend  (in-process; locked to Anthropic via @anthropic-ai/claude-agent-sdk)
 *
 * The contract is intentionally narrow:
 *   - input: a prompt string + transport-level knobs (timeout, retries, debug)
 *   - output: the agent's final assistant text — caller parses JSON itself
 *     (parseJsonFromOutput in hermes-runner.ts handles fence + brace-slice)
 *
 * Working directory is NOT part of the contract. Hermes uses a per-call
 * scratch dir for debug forensics (KEEP_SCRATCH=true) but the metric agent's
 * tools (`graphjin cli execute_graphql ...`) read global config from
 * ~/.config/graphjin/client.json, so cwd has no effect on correctness.
 * In-process backends like the Claude Agent simply share the worker's cwd.
 */

export const AGENT_BACKEND_IDS = ["hermes", "claude-agent"] as const;
export type AgentBackendId = (typeof AGENT_BACKEND_IDS)[number];

/**
 * Default concurrency caps for the metric agent. Overridable per-org via
 * /settings/agent (persisted in llm_provider_config scope='agent').
 *
 * - globalCap: pg-boss `batchSize` for the metric_refresh queue. Bounds
 *   how many jobs the worker pulls per poll.
 * - claudeAgentCap: in-process semaphore guarding concurrent claude-agent
 *   runs. The Hermes path is subprocess-based and doesn't need a separate
 *   cap (the global cap already bounds it). 0 disables the semaphore.
 */
export const AGENT_DEFAULT_GLOBAL_CAP = 20;
export const AGENT_DEFAULT_CLAUDE_AGENT_CAP = 8;

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

export type AgentRunOptions = {
  prompt: string;
  /** Hard cap on the run lifetime. Default 5 minutes. */
  timeoutMs?: number;
  /** Total attempts on transport-level failure (spawn/timeout/non-zero exit). Default 1 retry. */
  retries?: number;
  /** Pipe backend stderr / progress output to the parent process's stderr. */
  debug?: boolean;
  /** Identifier for correlation (e.g. processing_job UUID). Embedded in log lines and, for Hermes, the scratch dir name. */
  tag?: string;
};

export interface AgentBackend {
  readonly id: AgentBackendId;
  /** Returns the agent's final assistant text. Throws on transport-level failure. */
  run(opts: AgentRunOptions): Promise<string>;
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
