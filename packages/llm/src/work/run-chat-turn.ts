import { data_source, db, eq } from "@neko/db";
import {
  enqueue as defaultEnqueue,
  QUEUE,
  type QueueName,
} from "@neko/db/jobs";
import type { AgentChatMessage, AgentEvent } from "../agent-backend";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import {
  discoveryUrlFromMcpUrl,
  knowledgePackPaths,
  prefetchKnowledgePack as defaultPrefetchKnowledgePack,
  readKnowledgePack,
} from "../knowledge-pack";
import { runWorkAutoMemoryPipeline } from "./auto-memory";
import { makeAutoMemoryStopHook } from "./auto-memory-hook";
import {
  ensureGraphjinGuard as defaultEnsureGraphjinGuard,
  resolveBinaryOnPath as defaultResolveBinaryOnPath,
} from "./graphjin-guard";
import { formatWorkMemoryPromptContext as defaultFormatWorkMemoryPromptContext } from "./memory";
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
  enqueue: <T extends object>(queue: QueueName, data: T) => Promise<string | null>;
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
  const enqueue = deps.enqueue ?? defaultEnqueue;
  // runWorkAutoMemoryPipeline is referenced so its export stays live for
  // backends that still import it directly. Not used here — the auto-memory
  // classifier is enqueued as a pg-boss job (Hermes) or fired from the SDK
  // Stop hook (claude-agent).
  void runWorkAutoMemoryPipeline;

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

    const memoryContext = await formatWorkMemoryPromptContext({
      orgId,
      threadId,
      runId,
    });

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
    });

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
        }
      : undefined;

    const autoMemoryHook = backend.capabilities.sdkStopHook
      ? makeAutoMemoryStopHook({
          orgId,
          threadId,
          runId,
          userMessage: message,
        })
      : null;

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
      ...(autoMemoryHook
        ? { hooks: { Stop: [{ hooks: [autoMemoryHook] }] } }
        : {}),
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

    // Backends without an SDK Stop hook need a post-completion fallback to
    // schedule the memory classifier.
    if (
      !backend.capabilities.sdkStopHook &&
      result.status === "completed" &&
      result.finalText.trim()
    ) {
      await enqueue(QUEUE.WORK_AUTO_MEMORY, {
        orgId,
        threadId,
        runId,
        userMessage: message,
        agentAnswer: result.finalText,
      }).catch((err) => {
        console.error("[work-auto-memory] hermes enqueue failed:", err);
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
