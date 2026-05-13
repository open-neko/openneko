import type { AgentChatMessage, AgentEvent } from "../agent-backend";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import {
  finishWorkRun,
  getWorkThreadBundle,
  markWorkRunRunning,
  saveAssistantWorkMessage,
} from "../work/store";
import { buildWorkflowBuilderServer } from "./builder-server";
import { buildWorkflowBuilderPrompt } from "./builder-prompt";
import { extractWorkflowSaveFence } from "./fence-parsers";
import {
  saveWorkflow as defaultSaveWorkflow,
} from "./store";
import {
  WORKFLOW_BUILDER_ALLOWED_TOOLS,
  WORKFLOW_FIXED_DENY,
  buildAllowDenyGate,
} from "./tool-defaults";

export type RunWorkflowBuilderTurnOptions = {
  orgId: string;
  threadId: string;
  runId: string;
  message: string;
  emit: (event: AgentEvent) => Promise<void>;
  signal?: AbortSignal;
};

export type RunWorkflowBuilderTurnDeps = {
  resolveAgentBackend: typeof defaultResolveAgentBackend;
  saveWorkflow: typeof defaultSaveWorkflow;
};

export type RunWorkflowBuilderTurnResult = {
  status: "completed" | "failed" | "cancelled";
  finalText: string;
  error?: string;
};

export async function runWorkflowBuilderTurn(
  opts: RunWorkflowBuilderTurnOptions,
  deps: Partial<RunWorkflowBuilderTurnDeps> = {},
): Promise<RunWorkflowBuilderTurnResult> {
  const { orgId, threadId, runId, message, emit, signal } = opts;
  const resolveAgentBackend =
    deps.resolveAgentBackend ?? defaultResolveAgentBackend;
  const saveWorkflow = deps.saveWorkflow ?? defaultSaveWorkflow;

  await markWorkRunRunning(runId);

  const bundle = await getWorkThreadBundle(orgId, threadId);
  if (!bundle) {
    const errMsg = "Thread deleted before builder run start.";
    await finishWorkRun(runId, "failed", errMsg);
    return { status: "failed", finalText: "", error: errMsg };
  }

  const backend = await resolveAgentBackend(orgId);

  let assistantText = "";
  const wrappedEmit = async (event: AgentEvent): Promise<void> => {
    if (event.type === "message" && event.role === "assistant") {
      assistantText += event.content;
    }
    await emit(event);
  };

  try {
    await wrappedEmit({ type: "status", message: "Workflow builder ready…" });

    const messages: AgentChatMessage[] = bundle.messages.map((row) => ({
      id: row.id,
      role: row.role,
      content: row.content,
      runId: row.runId,
      createdAt: row.createdAt,
    }));

    const systemPrompt = buildWorkflowBuilderPrompt({
      mcpTools: backend.capabilities.mcpTools,
    });
    const transcript = messages
      .map((m) => `${m.role.toUpperCase()}: ${m.content}`)
      .join("\n\n");
    const prompt = transcript
      ? `${systemPrompt}\n\n--- EARLIER IN THIS CONVERSATION ---\n${transcript}\n--- END HISTORY ---`
      : systemPrompt;

    const mcpServers = backend.capabilities.mcpTools
      ? {
          neko_workflow_builder: buildWorkflowBuilderServer({
            orgId,
            createdByThreadId: threadId,
            createdByRunId: runId,
          }),
        }
      : undefined;

    const canUseTool = backend.capabilities.canUseToolGate
      ? buildAllowDenyGate(
          WORKFLOW_BUILDER_ALLOWED_TOOLS,
          WORKFLOW_FIXED_DENY,
        )
      : undefined;

    const result = await backend.run({
      prompt,
      userMessage: message,
      orgId,
      onEvent: wrappedEmit,
      mcpServers,
      canUseTool,
      allowedTools: backend.capabilities.canUseToolGate
        ? WORKFLOW_BUILDER_ALLOWED_TOOLS
        : undefined,
      tag: `workflow-builder ${runId}`,
      signal,
    });

    let persistedText = result.finalText.trim() || assistantText.trim();

    if (!backend.capabilities.mcpTools && persistedText) {
      const fence = extractWorkflowSaveFence(persistedText);
      if (fence.payload) {
        await saveWorkflow({
          orgId,
          name: fence.payload.name,
          description: fence.payload.description,
          goal: fence.payload.goal,
          systemPromptOverlay: fence.payload.systemPromptOverlay,
          steps: fence.payload.steps,
          triggers: fence.payload.triggers,
          createdByThreadId: threadId,
          createdByRunId: runId,
        });
        persistedText = fence.text;
      } else if (fence.errors.length > 0) {
        const reasons = fence.errors.map((e) => e.reason).join("; ");
        await wrappedEmit({
          type: "error",
          message: `workflow save fence invalid: ${reasons}`,
        });
      }
    }

    await finishWorkRun(runId, result.status, result.error ?? null);

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
        : "Workflow builder run failed unexpectedly.";
    await wrappedEmit({ type: "error", message: errMsg });
    await finishWorkRun(runId, status, aborted ? null : errMsg);
    await wrappedEmit({ type: "done", result: { status } });
    if (!aborted) throw error;
    return { status, finalText: assistantText };
  }
}
