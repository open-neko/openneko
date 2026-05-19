import { data_source, db, eq } from "@neko/db";
import type { AgentChatMessage, AgentEvent } from "../agent-backend";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import { extractMemoryFences } from "../agent-backends/memory-fence";
import { extractActionRequestFences } from "../workflows/fence-parsers";
import { handleWorkActionRequest } from "../workflows";
import {
  discoveryUrlFromMcpUrl,
  knowledgePackPaths,
  prefetchKnowledgePack as defaultPrefetchKnowledgePack,
  readKnowledgePack,
} from "../knowledge-pack";
import {
  ensureGraphjinGuard as defaultEnsureGraphjinGuard,
  resolveBinaryOnPath as defaultResolveBinaryOnPath,
} from "./graphjin-guard";
import {
  formatWorkMemoryPromptContext as defaultFormatWorkMemoryPromptContext,
  rememberWorkMemory,
} from "./memory";
import { buildWorkPrompt } from "./prompt";
import {
  finishWorkRun,
  getWorkThreadBundle,
  markWorkRunRunning,
  saveAssistantWorkMessage,
  setWorkThreadBackendState,
} from "./store";
import {
  buildPluginActionServer,
  buildRenderCardsServer,
  buildSkillBuilderServer,
  buildWorkMemoryServer,
  type PluginActionDescriptor,
} from "./tools";
import {
  ensureWorkWorkspace as defaultEnsureWorkWorkspace,
  listInstalledSkills as defaultListInstalledSkills,
} from "./workspace";

export type RunChatTurnOptions = {
  orgId: string;
  threadId: string;
  runId: string;
  message: string;
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
  /**
   * Plugin action kinds to surface to the agent as MCP tools, one
   * per kind. The worker passes its plugin registry snapshot here;
   * tests pass an empty array to keep the agent's surface stable.
   * Only honored when the backend supports MCP tools (claude-agent).
   */
  pluginActions?: readonly PluginActionDescriptor[];
};

// Tests can substitute any of these without touching the call site. Production
// callers pass nothing and get the real implementations.
export type RunChatTurnDeps = {
  resolveAgentBackend: typeof defaultResolveAgentBackend;
  ensureWorkWorkspace: typeof defaultEnsureWorkWorkspace;
  resolveBinaryOnPath: typeof defaultResolveBinaryOnPath;
  ensureGraphjinGuard: typeof defaultEnsureGraphjinGuard;
  formatWorkMemoryPromptContext: typeof defaultFormatWorkMemoryPromptContext;
  prefetchKnowledgePack: typeof defaultPrefetchKnowledgePack;
  listInstalledSkills: typeof defaultListInstalledSkills;
};

export type RunChatTurnResult = {
  status: "completed" | "failed" | "cancelled";
  finalText: string;
  error?: string;
};

function backendLabel(id: string): string {
  return id === "claude-agent" ? "Claude Agent" : "Hermes";
}

export async function runChatTurn(
  opts: RunChatTurnOptions,
  deps: Partial<RunChatTurnDeps> = {},
): Promise<RunChatTurnResult> {
  const { orgId, threadId, runId, message, emit, signal } = opts;

  const resolveAgentBackend = deps.resolveAgentBackend ?? defaultResolveAgentBackend;
  const ensureWorkWorkspace = deps.ensureWorkWorkspace ?? defaultEnsureWorkWorkspace;
  const resolveBinaryOnPath = deps.resolveBinaryOnPath ?? defaultResolveBinaryOnPath;
  const ensureGraphjinGuard = deps.ensureGraphjinGuard ?? defaultEnsureGraphjinGuard;
  const formatWorkMemoryPromptContext =
    deps.formatWorkMemoryPromptContext ?? defaultFormatWorkMemoryPromptContext;
  const prefetchKnowledgePack =
    deps.prefetchKnowledgePack ?? defaultPrefetchKnowledgePack;
  const listInstalledSkills =
    deps.listInstalledSkills ?? defaultListInstalledSkills;

  await markWorkRunRunning(runId);

  const bundle = await getWorkThreadBundle(orgId, threadId);
  if (!bundle) {
    const errMsg = "Thread deleted before run start.";
    await finishWorkRun(runId, "failed", errMsg);
    console.warn(
      `[work-run] thread ${threadId} not found for run ${runId}; marking failed and skipping`,
    );
    return { status: "failed", finalText: "", error: errMsg };
  }

  const backend = await resolveAgentBackend(orgId);
  const workspace = await ensureWorkWorkspace(orgId, threadId, runId);

  const sources = await db()
    .select({ mcp_url: data_source.mcp_url })
    .from(data_source)
    .where(eq(data_source.org_id, orgId))
    .limit(1);
  const mcpUrl = sources[0]?.mcp_url;
  if (mcpUrl) {
    const refresh = await prefetchKnowledgePack({
      discoveryUrl: discoveryUrlFromMcpUrl(mcpUrl),
      destDir: workspace.knowledgeRoot,
    });
    if (!refresh.ok) {
      console.warn(
        `[work-run] org=${orgId} knowledge refresh failed (${refresh.error}); proceeding with on-disk pack`,
      );
    }
  }
  const knowledge = await readKnowledgePack(
    knowledgePackPaths(workspace.knowledgeRoot),
  );

  let assistantText = "";
  const wrappedEmit = async (event: AgentEvent): Promise<void> => {
    if (event.type === "message" && event.role === "assistant") {
      assistantText += event.content;
    }
    await emit(event);
  };

  const graphjinBinary = await resolveBinaryOnPath("graphjin");
  if (!graphjinBinary) {
    const errMsg = "graphjin CLI is not installed on PATH.";
    await wrappedEmit({ type: "error", message: errMsg });
    await finishWorkRun(runId, "failed", errMsg);
    await wrappedEmit({ type: "done", result: { status: "failed" } });
    throw new Error(errMsg);
  }
  await ensureGraphjinGuard(workspace.binRoot, graphjinBinary);

  try {
    await wrappedEmit({
      type: "status",
      message: `Starting ${backendLabel(backend.id)}…`,
    });

    const supportsCardTool = backend.capabilities.mcpTools;
    const supportsSkillTool = backend.capabilities.mcpTools;
    const supportsMemoryTool = backend.capabilities.mcpTools;

    const messages: AgentChatMessage[] = bundle.messages.map((row) => ({
      id: row.id,
      role: row.role,
      content: row.content,
      runId: row.runId,
      createdAt: row.createdAt,
    }));

    await wrappedEmit({
      type: "status",
      message: "Loading shared skills and memory…",
    });

    const memoryContext = await formatWorkMemoryPromptContext(
      { orgId, threadId, runId },
      // Use the latest user message as the retrieval query so we pull
      // memories semantically close to what the operator just asked.
      { contextQuery: message, contextLimit: 5 },
    );

    const installedSkills = await listInstalledSkills(workspace.skillsRoot);

    const prompt = buildWorkPrompt({
      backend: backend.id,
      workspace,
      knowledge,
      messages,
      currentUserMessage: message,
      memoryContext,
      installedSkills,
      supportsCardTool,
      supportsSkillTool,
      supportsMemoryTool,
      inlineTranscript: !backend.capabilities.sessionResume,
      pluginActions: opts.pluginActions ?? [],
    });

    const pluginActions = opts.pluginActions ?? [];
    const pluginActionServer = backend.capabilities.mcpTools
      ? buildPluginActionServer({
          orgId,
          threadId,
          runId,
          descriptors: pluginActions,
          emit: wrappedEmit,
        })
      : null;

    const mcpServers = backend.capabilities.mcpTools
      ? {
          ...(supportsCardTool
            ? { neko_ui: buildRenderCardsServer(wrappedEmit) }
            : {}),
          ...(supportsSkillTool
            ? { neko_skills: buildSkillBuilderServer(workspace.skillsRoot) }
            : {}),
          ...(supportsMemoryTool
            ? {
                neko_memory: buildWorkMemoryServer({
                  orgId,
                  threadId,
                  runId,
                }),
              }
            : {}),
          ...(pluginActionServer
            ? { neko_plugin_actions: pluginActionServer }
            : {}),
        }
      : undefined;

    const result = await backend.run({
      prompt,
      userMessage: message,
      orgId,
      workspace,
      backendState: bundle.thread.backendState,
      onEvent: wrappedEmit,
      mcpServers,
      tag: `work ${runId}`,
      signal,
    });

    if (
      result.backendState &&
      result.backendState !== bundle.thread.backendState
    ) {
      await setWorkThreadBackendState(threadId, result.backendState);
    }

    await finishWorkRun(runId, result.status, result.error ?? null);

    // Hermes /work emits plugin action calls as `neko_action_request`
    // fences (no MCP tool registry to use). Parse them out and route
    // each through the same policy + DB + emit path the MCP tools
    // use, so the agent's tool surface is identical across backends
    // from the user's perspective.
    const rawTextForActions = result.finalText.trim() || assistantText.trim();
    const actionFences = extractActionRequestFences(rawTextForActions);
    for (const payload of actionFences.payloads) {
      try {
        await handleWorkActionRequest(
          {
            orgId,
            workRunId: runId,
            threadId,
            emit: wrappedEmit,
          },
          payload,
          payload.summary,
        );
      } catch (err) {
        console.warn(
          `[work-run] handleWorkActionRequest failed: ${err instanceof Error ? err.message : err}`,
        );
      }
    }

    // Pull any neko_memory fences out of the agent response, persist
    // them, and strip from the user-facing text. Backend-agnostic: works
    // for Hermes (which has no MCP tool registry) and is harmless for
    // claude-agent (which would have used the MCP save tool, but we
    // accept either path).
    const rawText = actionFences.text;
    const { text: persistedText, ops: memoryOps } = extractMemoryFences(rawText);
    for (const op of memoryOps) {
      try {
        await rememberWorkMemory({
          orgId,
          threadId,
          runId,
          text: op.text,
          kind: "business_rule",
          scope: op.scope ?? "global",
          pinned: op.pinned ?? true,
        });
      } catch (err) {
        console.error(
          "[work-memory] fence-driven save failed:",
          err instanceof Error ? err.message : err,
        );
      }
    }
    if (persistedText) {
      await saveAssistantWorkMessage({
        orgId,
        threadId,
        runId,
        content: persistedText,
      });
    }

    await wrappedEmit({ type: "done", result: { status: result.status } });

    return {
      status: result.status,
      finalText: result.finalText,
      error: result.error,
    };
  } catch (error) {
    const aborted =
      signal?.aborted ||
      (error instanceof Error &&
        (error.name === "AbortError" || error.message.includes("aborted")));
    const status: "failed" | "cancelled" = aborted ? "cancelled" : "failed";
    const errMsg = aborted
      ? "Cancelled by user."
      : error instanceof Error
        ? error.message
        : "Work run failed unexpectedly.";
    await wrappedEmit({ type: "error", message: errMsg });
    await finishWorkRun(runId, status, aborted ? null : errMsg);
    await wrappedEmit({ type: "done", result: { status } });
    if (!aborted) throw error;
    return { status, finalText: assistantText };
  }
}
