import type { AgentChatMessage, AgentEvent } from "../agent-backend";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import {
  finishWorkRun,
  getWorkThreadBundle,
  markWorkRunRunning,
  saveAssistantWorkMessage,
} from "../work/store";
import {
  createActionPolicy as defaultCreateActionPolicy,
  updateActionPolicy as defaultUpdateActionPolicy,
  type ActionPolicyMode,
  type ActionScope,
  type RiskLevel,
} from "./action-store";
import { extractPolicySaveFence } from "./fence-parsers";
import { buildPolicyBuilderPrompt } from "./policy-builder-prompt";

export type RunPolicyBuilderTurnOptions = {
  orgId: string;
  threadId: string;
  runId: string;
  message: string;
  emit: (event: AgentEvent) => Promise<void>;
  /** When set, the fence is applied as an update to this existing policy
   *  via updateActionPolicy. When undefined, the fence creates a new row. */
  editingPolicyId?: string;
  signal?: AbortSignal;
};

export type RunPolicyBuilderTurnDeps = {
  resolveAgentBackend: typeof defaultResolveAgentBackend;
  createActionPolicy: typeof defaultCreateActionPolicy;
  updateActionPolicy: typeof defaultUpdateActionPolicy;
};

export type RunPolicyBuilderTurnResult = {
  status: "completed" | "failed" | "cancelled";
  finalText: string;
  error?: string;
};

export async function runPolicyBuilderTurn(
  opts: RunPolicyBuilderTurnOptions,
  deps: Partial<RunPolicyBuilderTurnDeps> = {},
): Promise<RunPolicyBuilderTurnResult> {
  const { orgId, threadId, runId, message, emit, editingPolicyId, signal } = opts;
  const resolveAgentBackend =
    deps.resolveAgentBackend ?? defaultResolveAgentBackend;
  const createActionPolicy =
    deps.createActionPolicy ?? defaultCreateActionPolicy;
  const updateActionPolicy =
    deps.updateActionPolicy ?? defaultUpdateActionPolicy;

  await markWorkRunRunning(runId);

  const bundle = await getWorkThreadBundle(orgId, threadId);
  if (!bundle) {
    const errMsg = "Thread deleted before policy-builder run start.";
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
    await wrappedEmit({ type: "status", message: "Policy builder ready…" });

    const messages: AgentChatMessage[] = bundle.messages.map((row) => ({
      id: row.id,
      role: row.role,
      content: row.content,
      runId: row.runId,
      createdAt: row.createdAt,
    }));

    const systemPrompt = buildPolicyBuilderPrompt({
      mcpTools: backend.capabilities.mcpTools,
    });
    const transcript = messages
      .map((m) => `${m.role.toUpperCase()}: ${m.content}`)
      .join("\n\n");
    const prompt = transcript
      ? `${systemPrompt}\n\n--- EARLIER IN THIS CONVERSATION ---\n${transcript}\n--- END HISTORY ---`
      : systemPrompt;

    const result = await backend.run({
      prompt,
      userMessage: message,
      orgId,
      onEvent: wrappedEmit,
      tag: `policy-builder ${runId}`,
      signal,
    });

    let persistedText = result.finalText.trim() || assistantText.trim();

    if (persistedText) {
      const fence = extractPolicySaveFence(persistedText);
      if (fence.payload) {
        const policyFields = {
          name: fence.payload.name,
          description: fence.payload.description ?? "",
          appliesToKinds: fence.payload.applies_to_kinds,
          appliesToScopes: fence.payload.applies_to_scopes as ActionScope[],
          mode: fence.payload.mode as ActionPolicyMode,
          riskThresholdAutoApprove:
            (fence.payload.risk_threshold_auto_approve as RiskLevel | undefined) ??
            null,
          allowedTargets: fence.payload.allowed_targets ?? null,
          deniedTargets: fence.payload.denied_targets ?? null,
          limits: fence.payload.limits,
          approverRole: fence.payload.approver_role ?? null,
          priority: fence.payload.priority,
          enabled: fence.payload.enabled,
        };
        if (editingPolicyId) {
          await updateActionPolicy(orgId, editingPolicyId, policyFields);
        } else {
          await createActionPolicy({ orgId, ...policyFields });
        }
        persistedText = fence.text;
      } else if (fence.errors.length > 0) {
        const reasons = fence.errors.map((e) => e.reason).join("; ");
        await wrappedEmit({
          type: "error",
          message: `policy save fence invalid: ${reasons}`,
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
        : "Policy builder run failed unexpectedly.";
    await wrappedEmit({ type: "error", message: errMsg });
    await finishWorkRun(runId, status, aborted ? null : errMsg);
    await wrappedEmit({ type: "done", result: { status } });
    if (!aborted) throw error;
    return { status, finalText: assistantText };
  }
}
