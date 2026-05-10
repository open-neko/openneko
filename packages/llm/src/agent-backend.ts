export const AGENT_BACKEND_IDS = ["hermes", "claude-agent"] as const;
export type AgentBackendId = (typeof AGENT_BACKEND_IDS)[number];

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

export function shellToolName(backendId: AgentBackendId): string {
  return backendId === "claude-agent" ? "Bash" : "terminal";
}

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
  // Delta of real prose since the last message event. Backends MUST NOT emit
  // structured-output payloads (a2ui fences, tool-call JSON, etc.) here — use
  // the `surface` event for cards.
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
  prompt: string;
  userMessage?: string;
  timeoutMs?: number;
  retries?: number;
  debug?: boolean;
  tag?: string;
  orgId?: string;
  workspace?: AgentWorkspace;
  skills?: string[];
  signal?: AbortSignal;
  onEvent?: (event: AgentEvent) => Promise<void> | void;
  backendState?: Record<string, unknown>;
  mcpServers?: Record<string, unknown>;
  outputSchema?: Record<string, unknown>;
  forkSession?: boolean;
  agents?: Record<string, unknown>;
  hooks?: Record<string, unknown>;
  canUseTool?: (
    toolName: string,
    input: Record<string, unknown>,
    options: Record<string, unknown>,
  ) => Promise<Record<string, unknown>>;
  onElicitation?: (
    request: Record<string, unknown>,
    options: Record<string, unknown>,
  ) => Promise<Record<string, unknown>>;
};

export type AgentRunResult = {
  finalText: string;
  status: "completed" | "failed" | "cancelled";
  backendState?: Record<string, unknown>;
  error?: string;
};

export interface AgentBackend {
  readonly id: AgentBackendId;
  run(opts: AgentRunOptions): Promise<AgentRunResult>;
}

export class AgentBackendConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AgentBackendConfigError";
  }
}
