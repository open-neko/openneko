import {
  discoveryUrlFromMcpUrl,
  prefetchKnowledgePack,
  resolveAgentBackend,
  type AgentChatMessage,
  type AgentEvent,
} from "@neko/llm";
import {
  appendWorkRunEvent,
  buildRenderCardsServer,
  buildSkillBuilderServer,
  buildWorkMemoryServer,
  buildWorkPrompt,
  ensureGraphjinGuard,
  ensureWorkWorkspace,
  finishWorkRun,
  formatWorkMemoryPromptContext,
  getWorkRun,
  getWorkRunEvents,
  getWorkThreadBundle,
  markWorkRunRunning,
  resolveBinaryOnPath,
  runWorkAutoMemoryPipeline,
  saveAssistantWorkMessage,
  setWorkThreadBackendState,
} from "@neko/llm/work";
import { data_source, db, eq } from "@neko/db";

function backendLabel(id: string): string {
  return id === "claude-agent" ? "Claude Agent" : "Hermes";
}

export async function runWorkRun(
  _jobId: string,
  orgId: string,
  payload: { runId: string; threadId: string; message: string },
): Promise<void> {
  const { runId, threadId, message } = payload;

  const run = await getWorkRun(orgId, runId);
  if (!run) {
    console.warn(
      `[work-run] run ${runId} not found for thread ${threadId}; skipping stale job`,
    );
    return;
  }

  await markWorkRunRunning(runId);

  const bundle = await getWorkThreadBundle(orgId, threadId);
  if (!bundle) {
    // Thread was deleted between enqueue and dispatch (common when users
    // cancel + delete a thread mid-run). Mark the run failed and return
    // cleanly — throwing makes pg-boss retry forever.
    await finishWorkRun(runId, "failed", "Thread deleted before run start.");
    console.warn(
      `[work-run] thread ${threadId} not found for run ${runId}; marking failed and skipping`,
    );
    return;
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

  let seq = (await getWorkRunEvents(orgId, runId)).length;
  let assistantText = "";

  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({ orgId, threadId, runId, seq, event });
    if (event.type === "message" && event.role === "assistant") {
      assistantText += event.content;
    }
  };

  const graphjinBinary = await resolveBinaryOnPath("graphjin");
  if (!graphjinBinary) {
    const errMsg = "graphjin CLI is not installed on PATH.";
    await emit({ type: "error", message: errMsg });
    await finishWorkRun(runId, "failed", errMsg);
    await emit({ type: "done", result: { status: "failed" } });
    throw new Error(errMsg);
  }
  await ensureGraphjinGuard(workspace.binRoot, graphjinBinary);

  try {
    await emit({
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

    await emit({
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
            ...(supportsCardTool ? { neko_ui: buildRenderCardsServer(emit) } : {}),
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
      onEvent: emit,
      mcpServers,
      tag: `work ${runId}`,
    });

    if (result.backendState && result.backendState !== bundle.thread.backendState) {
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

    await emit({ type: "done", result: { status: result.status } });
  } catch (error) {
    const errMsg =
      error instanceof Error ? error.message : "Work run failed unexpectedly.";
    await emit({ type: "error", message: errMsg });
    await finishWorkRun(runId, "failed", errMsg);
    await emit({ type: "done", result: { status: "failed" } });
    throw error;
  }
}
