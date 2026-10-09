/**
 * The one source for agent run limits and budgets. Hermes (config.yaml and
 * per-run env) and Ax (the run spec) both read their values from here, so the
 * two backends cannot diverge. Each value has an environment override.
 */

export type AgentReasoningEffort = "low" | "medium" | "high";

export type AgentRunKind = "chat" | "workflow" | "agent-job";

export type AgentLimits = {
  /** Wall clock for one turn. The backend process is killed when it expires. */
  timeoutMs: number;
  /** Model turns per run: Hermes agent.max_turns, Ax max_actor_steps. */
  maxTurns: number;
  /** Tool-calling iterations per run. Undefined means only maxTurns bounds them. */
  maxToolCalls?: number;
  /** Extra turns a workflow may take after a turn times out. */
  maxContinuations: number;
  /** Undefined keeps the provider default. */
  reasoningEffort?: AgentReasoningEffort;
  delegation: {
    maxIterations: number;
    maxConcurrentChildren: number;
    maxSpawnDepth: number;
    /** 0 means no child timeout. */
    childTimeoutSeconds: number;
    orchestratorEnabled: boolean;
  };
  toolOutput: {
    /** Terminal output cap in characters. */
    maxBytes: number;
    maxLines: number;
    maxLineLength: number;
  };
  terminal: {
    /** Default command timeout. */
    timeoutSeconds: number;
  };
};

// Every concurrent job can own an OpenShell sandbox. Keep the out-of-box
// ceiling small; operators raise it after sizing their gateway and quota.
export const AGENT_DEFAULT_GLOBAL_CAP = 3;
// Two metric refreshes at a time leaves sandbox room for chat on a small host.
export const METRIC_REFRESH_DEFAULT_CAP = 2;

function readInt(name: string, fallback: number, min: number, max?: number): number {
  const raw = process.env[name]?.trim();
  if (!raw) return fallback;
  const n = Math.floor(Number(raw));
  if (!Number.isFinite(n)) return fallback;
  if (n < min) return min;
  if (max !== undefined && n > max) return max;
  return n;
}

function readBool(name: string, fallback: boolean): boolean {
  const raw = process.env[name]?.trim().toLowerCase();
  if (!raw) return fallback;
  if (["1", "true", "yes", "on"].includes(raw)) return true;
  if (["0", "false", "no", "off"].includes(raw)) return false;
  return fallback;
}

export function isReasoningEffort(value: unknown): value is AgentReasoningEffort {
  return value === "low" || value === "medium" || value === "high";
}

/**
 * Discovery-heavy asks measured 280-450 s against a live source, so a chat turn
 * gets 9 minutes. A shorter limit killed real runs mid-stream.
 */
export function agentLimits(kind: AgentRunKind = "chat"): AgentLimits {
  const shared = {
    maxTurns: readInt("OPENNEKO_AGENT_MAX_TURNS", 25, 1, 500),
    delegation: {
      maxIterations: readInt("OPENNEKO_AGENT_DELEGATION_MAX_ITERATIONS", 50, 1),
      maxConcurrentChildren: readInt("OPENNEKO_AGENT_DELEGATION_MAX_CONCURRENT_CHILDREN", 3, 1),
      maxSpawnDepth: readInt("OPENNEKO_AGENT_DELEGATION_MAX_SPAWN_DEPTH", 1, 1, 3),
      childTimeoutSeconds: readInt("OPENNEKO_AGENT_DELEGATION_CHILD_TIMEOUT_SECONDS", 0, 0),
      orchestratorEnabled: readBool("OPENNEKO_AGENT_DELEGATION_ORCHESTRATOR_ENABLED", true),
    },
    toolOutput: {
      maxBytes: readInt("OPENNEKO_AGENT_TOOL_OUTPUT_MAX_BYTES", 50_000, 1_000),
      maxLines: readInt("OPENNEKO_AGENT_TOOL_OUTPUT_MAX_LINES", 2_000, 10),
      maxLineLength: readInt("OPENNEKO_AGENT_TOOL_OUTPUT_MAX_LINE_LENGTH", 2_000, 80),
    },
    terminal: {
      timeoutSeconds: readInt("OPENNEKO_AGENT_TERMINAL_TIMEOUT_SECONDS", 180, 1, 600),
    },
  };
  if (kind === "workflow") {
    // Workflows are mechanical loops: lower effort, a tool-call cap and a longer wall clock.
    const effort = process.env.OPENNEKO_WORKFLOW_REASONING_EFFORT?.trim();
    return {
      ...shared,
      timeoutMs: readInt("OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS", 15 * 60_000, 1_000),
      maxToolCalls: readInt("OPENNEKO_WORKFLOW_MAX_TOOL_CALLS", 150, 1),
      maxContinuations: readInt("OPENNEKO_WORKFLOW_MAX_CONTINUATIONS", 2, 0),
      reasoningEffort: isReasoningEffort(effort) ? effort : "medium",
    };
  }
  return {
    ...shared,
    timeoutMs: readInt("OPENNEKO_AGENT_TURN_TIMEOUT_MS", 9 * 60_000, 1_000),
    maxContinuations: 0,
  };
}

/** Hermes config.yaml lines for the limits that Hermes reads from its config. */
export function hermesLimitConfigLines(limits: AgentLimits): string[] {
  const d = limits.delegation;
  return [
    "delegation:",
    `  max_iterations: ${d.maxIterations}`,
    `  max_concurrent_children: ${d.maxConcurrentChildren}`,
    `  max_spawn_depth: ${d.maxSpawnDepth}`,
    `  orchestrator_enabled: ${d.orchestratorEnabled ? "true" : "false"}`,
    `  child_timeout_seconds: ${d.childTimeoutSeconds}`,
    "",
    "tool_output:",
    `  max_bytes: ${limits.toolOutput.maxBytes}`,
    `  max_lines: ${limits.toolOutput.maxLines}`,
    `  max_line_length: ${limits.toolOutput.maxLineLength}`,
  ];
}

/** Per-run Hermes environment for the limits that Hermes reads from env. */
export function hermesLimitEnv(limits: AgentLimits): Record<string, string> {
  return { TERMINAL_TIMEOUT: String(limits.terminal.timeoutSeconds) };
}

export type AxRunLimits = {
  timeout_ms: number;
  max_actor_steps: number;
  max_child_steps: number;
  max_operations?: number;
  reasoning_effort?: AgentReasoningEffort;
};

/**
 * Ax run spec fields. A per-run tool-call cap replaces the turn count, as the
 * Hermes per-run iteration override does.
 */
export function axRunLimits(limits: AgentLimits): AxRunLimits {
  return {
    timeout_ms: limits.timeoutMs,
    max_actor_steps: Math.min(limits.maxToolCalls ?? limits.maxTurns, 500),
    max_child_steps: Math.min(limits.delegation.maxIterations, 500),
    ...(limits.maxToolCalls ? { max_operations: limits.maxToolCalls } : {}),
    ...(limits.reasoningEffort ? { reasoning_effort: limits.reasoningEffort } : {}),
  };
}

/** Ax Harness process environment for the limits it reads at start. */
export function axLimitEnv(limits: AgentLimits): Record<string, string> {
  return {
    OPENNEKO_HARNESS_TERMINAL_TIMEOUT_SECONDS: String(limits.terminal.timeoutSeconds),
    OPENNEKO_HARNESS_TERMINAL_MAX_OUTPUT: String(limits.toolOutput.maxBytes),
  };
}
