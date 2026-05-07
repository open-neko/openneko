import type { AgentBackendId } from "../agent-backend";

export type WorkSurfaceMessage = {
  version: "v0.9";
  [key: string]: unknown;
};

export type WorkArtifact = {
  path: string;
  label: string;
  mimeType?: string;
};

export type WorkEvent =
  | { type: "message"; role: "user" | "assistant"; content: string }
  | { type: "tool_start"; id: string; name: string; input?: unknown }
  | { type: "tool_delta"; id: string; delta: unknown }
  | { type: "tool_end"; id: string; result?: unknown; error?: string }
  | { type: "surface"; messages: WorkSurfaceMessage[] }
  | { type: "artifact"; artifact: WorkArtifact }
  | { type: "status"; message: string }
  | { type: "error"; message: string }
  | { type: "done"; result?: unknown };

export type WorkTranscriptMessage = {
  id?: string;
  role: "user" | "assistant";
  content: string;
  runId?: string | null;
  createdAt?: string;
};

export type WorkAgentWorkspace = {
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
  hermesHome: string;
};

export type WorkRunInput = {
  orgId: string;
  threadId: string;
  runId: string;
  workspace: WorkAgentWorkspace;
  backendState: Record<string, unknown>;
  messages: WorkTranscriptMessage[];
  currentUserMessage: string;
  signal: AbortSignal;
  debug?: boolean;
  onEvent: (event: WorkEvent) => Promise<void> | void;
};

export type WorkRunResult = {
  backend: AgentBackendId;
  status: "completed" | "failed" | "cancelled";
  finalText: string;
  backendState?: Record<string, unknown>;
  error?: string;
};

export interface WorkAgentBackend {
  readonly id: AgentBackendId;
  run(input: WorkRunInput): Promise<WorkRunResult>;
}
