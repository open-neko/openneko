/**
 * work_run job handler.
 *
 * Ports the inline `/api/work/threads/[threadId]/runs` POST flow
 * onto the pg-boss worker: build prompt, run the configured agent
 * backend, write each AgentEvent to work_run_event, finalize the
 * work_run row, kick off the auto-memory pipeline.
 *
 * The web route is now a thin enqueue + return — it inserts the
 * work_run + processing_job rows, sends WORK_RUN, returns the
 * runId. The browser opens an SSE long-poll against the /events
 * endpoint which tails work_run_event by seq until status goes
 * terminal.
 */

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

  // Re-read the bundle here (rather than passing it through the
  // payload) so the worker sees the latest backendState / message
  // history if anything else mutated them between enqueue and pickup.
  const bundle = await getWorkThreadBundle(orgId, threadId);
  if (!bundle) {
    await finishWorkRun(runId, "failed", "Thread disappeared before run start.");
    throw new Error(`work_run ${runId}: thread ${threadId} not found`);
  }

  const backend = await resolveAgentBackend(orgId);
  const workspace = await ensureWorkWorkspace(orgId, threadId, runId);

  // Refresh the on-disk knowledge pack the prompt points at. Cheap
  // local REST fetch; best-effort. If graphjin is transiently
  // unreachable we proceed with whatever's already on disk from a
  // prior run (or boot's prefetch).
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

  // Seed the seq counter from any events already written to this run
  // — covers the (rare) pg-boss retry case where the job ran once,
  // wrote some events, then failed and is being redelivered. Without
  // this, seq=1 collides with the prior attempt's seq=1.
  let seq = (await getWorkRunEvents(orgId, runId)).length;

  const emit = async (event: AgentEvent): Promise<void> => {
    seq += 1;
    await appendWorkRunEvent({ orgId, threadId, runId, seq, event });
    if (event.type === "message" && event.role === "assistant") {
      await saveAssistantWorkMessage({
        orgId,
        threadId,
        runId,
        content: event.content,
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

    if (result.status === "completed" && result.finalText.trim()) {
      // Auto-memory runs after the run is finalized so the SSE tail
      // can close on `done` without waiting for memory writes. The
      // pipeline logs its own errors; we don't want them to fail
      // the user-visible run.
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
