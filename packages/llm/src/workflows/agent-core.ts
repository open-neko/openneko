import type {
  AgentBackend,
  AgentEvent,
  AgentRunResult,
  AgentWorkspace,
} from "../agent-backend";
import type { AgentControlPlane } from "../work/control-plane";
import {
  buildGraphjinMcpServer,
  buildLibraryServer,
  buildWorkMemoryServer,
} from "../work/tools";
import { buildWorkflowActionServer } from "./action-tool-server";
import { buildWorkflowOutputServer } from "./output-tool-server";

export interface RunWorkflowAgentBackendInput {
  /** Hermes backend. In the sandbox it is reconstructed from config. */
  backend: AgentBackend;
  prompt: string;
  userMessage: string;
  orgId: string;
  threadId: string;
  /** The underlying work_run id that owns the sandbox and broker token. */
  runId: string;
  workflowRunId: string;
  mode: "live" | "headless";
  networkHosts: string[];
  triggeredByObservationId?: string | null;
  workspace: AgentWorkspace;
  /** In-process on the host; broker-backed inside the agent sandbox. */
  controlPlane: AgentControlPlane;
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
  tag?: string;
}

/**
 * Sandbox-runnable workflow agent loop. The DB-bound prologue/epilogue stays in
 * runWorkflowTurn; this core only rebuilds workflow MCP tools and calls the
 * backend. Inside OpenShell, tools reach persistence through the broker-backed
 * control plane.
 */
export async function runWorkflowAgentBackend(
  input: RunWorkflowAgentBackendInput,
): Promise<AgentRunResult> {
  const {
    backend,
    prompt,
    userMessage,
    orgId,
    threadId,
    runId,
    workflowRunId,
    mode,
    triggeredByObservationId,
    workspace,
    controlPlane,
    emit,
    signal,
    tag,
  } = input;

  const mcp = backend.capabilities.mcpTools;
  const mcpServers = mcp
    ? {
        neko_graphjin: buildGraphjinMcpServer({
          orgId,
          runId,
          controlPlane,
        }),
        neko_workflow_output: buildWorkflowOutputServer({
          orgId,
          workflowRunId,
          workRunId: runId,
          emit,
          controlPlane,
        }),
        neko_action: buildWorkflowActionServer({
          orgId,
          workflowRunId,
          workRunId: runId,
          triggeredByObservationId,
          emit,
          controlPlane,
        }),
        neko_memory: buildWorkMemoryServer(
          {
            orgId,
            threadId,
            runId,
          },
          { controlPlane },
        ),
        // Search-only: workflows consult the document library the same
        // way chat turns do (layering resolved from the run binding).
        neko_library: buildLibraryServer(
          { orgId, threadId, runId },
          { controlPlane },
        ),
      }
    : undefined;

  void mode;

  return backend.run({
    prompt,
    userMessage,
    orgId,
    workspace,
    onEvent: emit,
    mcpServers,
    mcpBridgeEnv: mcp
      ? {
          OPENNEKO_MCP_MODE: "workflow",
          OPENNEKO_MCP_ORG_ID: orgId,
          OPENNEKO_MCP_THREAD_ID: threadId,
          OPENNEKO_MCP_RUN_ID: runId,
          OPENNEKO_MCP_SKILLS_ROOT: workspace.skillsRoot,
          OPENNEKO_MCP_WORKFLOW_RUN_ID: workflowRunId,
          ...(triggeredByObservationId
            ? { OPENNEKO_MCP_TRIGGERED_BY_OBSERVATION_ID: triggeredByObservationId }
            : {}),
        }
      : undefined,
    tag,
    signal,
  });
}
