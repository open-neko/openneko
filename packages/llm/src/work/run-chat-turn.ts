import { data_source, db, eq } from "@neko/db";
import type { AgentChatMessage, AgentEvent } from "../agent-backend";
import { resolveAgentBackend } from "../agent-backend-resolver";
import {
  discoveryUrlFromMcpUrl,
  prefetchKnowledgePack,
} from "../knowledge-pack";
import { runWorkAutoMemoryPipeline } from "./auto-memory";
import { ensureGraphjinGuard, resolveBinaryOnPath } from "./graphjin-guard";
import { formatWorkMemoryPromptContext } from "./memory";
import { buildWorkPrompt } from "./prompt";
import {
  finishWorkRun,
  getWorkThreadBundle,
  markWorkRunRunning,
  saveAssistantWorkMessage,
  setWorkThreadBackendState,
} from "./store";
import {
  buildRenderCardsServer,
  buildSkillBuilderServer,
  buildWorkMemoryServer,
} from "./tools";
import { ensureWorkWorkspace } from "./workspace";

export type RunChatTurnOptions = {
  orgId: string;
  threadId: string;
  runId: string;
  message: string;
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
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
): Promise<RunChatTurnResult> {
  const { orgId, threadId, runId, message, emit, signal } = opts;

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

    const supportsCardTool = backend.id === "claude-agent";
    const supportsSkillTool = backend.id === "claude-agent";
    const supportsMemoryTool = backend.id === "claude-agent";

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

    const memoryContext = await formatWorkMemoryPromptContext({
      orgId,
      threadId,
      runId,
    });

    const prompt = buildWorkPrompt({
      backend: backend.id,
      workspace,
      messages,
      currentUserMessage: message,
      memoryContext,
      supportsCardTool,
      supportsSkillTool,
      supportsMemoryTool,
    });

    const mcpServers =
      backend.id === "claude-agent"
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

    const persistedText = result.finalText.trim() || assistantText.trim();
    if (persistedText) {
      await saveAssistantWorkMessage({
        orgId,
        threadId,
        runId,
        content: persistedText,
      });
    }

    if (result.status === "completed" && result.finalText.trim()) {
      void runWorkAutoMemoryPipeline({
        orgId,
        threadId,
        runId,
        userMessage: message,
        agentAnswer: result.finalText,
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
