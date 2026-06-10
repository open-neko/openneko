import type {
  AgentBackend,
  AgentEvent,
  AgentRunResult,
  AgentWorkspace,
} from "../agent-backend";
import { buildRuleBuilderServer, buildWorkflowBuilderServer } from "../workflows";
import type { AgentControlPlane } from "./control-plane";
import {
  buildPluginActionServer,
  buildRenderCardsServer,
  buildSkillBuilderServer,
  buildWorkMemoryServer,
  type PluginActionDescriptor,
} from "./tools";

export interface RunAgentBackendInput {
  /** Resolved backend (hermes / claude-agent). In the sandbox it's reconstructed from config. */
  backend: AgentBackend;
  prompt: string;
  userMessage: string;
  orgId: string;
  threadId: string;
  runId: string;
  workspace: AgentWorkspace;
  backendState?: Record<string, unknown>;
  pluginActions: readonly PluginActionDescriptor[];
  /** In-process on the host; broker-backed inside the agent sandbox. */
  controlPlane?: AgentControlPlane;
  /** Whether this channel renders a2ui cards (web). Default true. Gates the
   *  neko_ui render server. See docs/PER_CHANNEL_RENDERING.md. */
  wantsCards?: boolean;
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
}

/**
 * The agent loop that is sandbox-runnable: build the MCP tool servers and run
 * the backend. Its ONLY control-plane touchpoint is `controlPlane` (the
 * broker-backed impl inside the sandbox), so nothing here needs the DB. The
 * DB-bound prologue (load bundle/knowledge/memory/skills + build the prompt)
 * and epilogue (fence handling + persistence) stay on the host in runChatTurn.
 *
 * Backends that use MCP tools (claude-agent) get the broker-backed servers
 * here; hermes (capabilities.mcpTools=false) gets none — it emits tool fences
 * that runChatTurn parses host-side after the turn.
 */
export async function runAgentBackend(
  input: RunAgentBackendInput,
): Promise<AgentRunResult> {
  const {
    backend,
    prompt,
    userMessage,
    orgId,
    threadId,
    runId,
    workspace,
    backendState,
    pluginActions,
    controlPlane,
    wantsCards = true,
    emit,
    signal,
  } = input;

  const mcp = backend.capabilities.mcpTools;
  const pluginActionServer = mcp
    ? buildPluginActionServer({
        orgId,
        threadId,
        runId,
        descriptors: pluginActions,
        emit,
        controlPlane,
      })
    : null;

  const mcpServers = mcp
    ? {
        // Rendering is per-channel: the card server only ships to web turns.
        ...(wantsCards ? { neko_ui: buildRenderCardsServer(emit) } : {}),
        neko_skills: buildSkillBuilderServer(workspace.skillsRoot),
        neko_memory: buildWorkMemoryServer({ orgId, threadId, runId }, { controlPlane }),
        neko_workflow_builder: buildWorkflowBuilderServer({
          orgId,
          createdByThreadId: threadId,
          createdByRunId: runId,
          emit,
          controlPlane,
        }),
        neko_rule_builder: buildRuleBuilderServer({
          orgId,
          createdByThreadId: threadId,
          createdByRunId: runId,
          emit,
          controlPlane,
        }),
        ...(pluginActionServer ? { neko_plugin_actions: pluginActionServer } : {}),
      }
    : undefined;

  return backend.run({
    prompt,
    userMessage,
    orgId,
    workspace,
    backendState,
    onEvent: emit,
    mcpServers,
    wantsCards,
    tag: `work ${runId}`,
    signal,
  });
}
