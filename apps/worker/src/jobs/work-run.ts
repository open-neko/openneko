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

  await markWorkRunRunning(runId);

  const bundle = await getWorkThreadBundle(orgId, threadId);
  if (!bundle) {
    await finishWorkRun(runId, "failed", "Thread disappeared before run start.");
    throw new Error(`work_run ${runId}: thread ${threadId} not found`);
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
  // Backends now emit `message` events as deltas, not snapshots — accumulate
  // them so the persisted assistant message contains the full text.
  let assistantText = "";

  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({ orgId, threadId, runId, seq, event });
    if (event.type === "message" && event.role === "assistant") {
      assistantText += event.content;
      await saveAssistantWorkMessage({
        orgId,
        threadId,
        runId,
        content: assistantText,
      });
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

    // Backends may post-process the streamed text (e.g. strip a2ui fences)
    // and return a cleaned `finalText`. If it differs from what was streamed,
    // overwrite the persisted assistant message with the cleaned version so
    // memory/title pipelines and reload-after-run see the canonical text.
    if (
      result.status === "completed" &&
      result.finalText.trim() &&
      result.finalText.trim() !== assistantText.trim()
    ) {
      await saveAssistantWorkMessage({
        orgId,
        threadId,
        runId,
        content: result.finalText,
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
