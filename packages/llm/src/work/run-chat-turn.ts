import { data_source, db, eq } from "@neko/db";
import type { AgentChatMessage, AgentEvent } from "../agent-backend";
import { resolveAgentBackend as defaultResolveAgentBackend } from "../agent-backend-resolver";
import { extractMemoryFences } from "../agent-backends/memory-fence";
import {
  extractActionRequestFences,
  extractAskContextFence,
  extractRuleSaveFence,
  extractValueFence,
  extractWorkflowSaveFence,
} from "../workflows/fence-parsers";
import { clampAnalysisMinutes } from "../workflows/value";
import {
  handleWorkActionRequest,
  policySavedCard,
  saveWorkflowWithTrigger,
  subscriptionSavedCard,
  upsertActionPolicyByName,
  workflowSavedCard,
  type ActionPolicyMode,
  type ActionScope,
  type RiskLevel,
} from "../workflows";
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
  setWorkRunValue,
  setWorkThreadBackendState,
} from "./store";
import type { PluginActionDescriptor } from "./tools";
import { runAgentBackend } from "./agent-core";
import type { AgentControlPlane } from "./control-plane";
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
  /**
   * Control-plane impl for the DB-touching MCP tools. Default (undefined)
   * uses the in-process plane; the agent sandbox injects a broker client.
   */
  controlPlane?: AgentControlPlane;
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
  /**
   * Runs the agent loop. Default = runAgentBackend in-process. The launcher
   * injects a sandbox-running impl for OPENNEKO_AGENT_RUNTIME=openshell: it
   * runs the core in an OpenShell sandbox, streaming events back through
   * `emit`. The DB-bound prologue/epilogue around this stay host-side.
   */
  runCore: typeof runAgentBackend;
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
  const runCore = deps.runCore ?? runAgentBackend;

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
    const supportsWorkflowTool = backend.capabilities.mcpTools;
    const supportsPolicyTool = backend.capabilities.mcpTools;

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
      supportsWorkflowTool,
      supportsPolicyTool,
      inlineTranscript: !backend.capabilities.sessionResume,
      pluginActions: opts.pluginActions ?? [],
    });

    const result = await runCore({
      backend,
      prompt,
      userMessage: message,
      orgId,
      threadId,
      runId,
      workspace,
      backendState: bundle.thread.backendState,
      pluginActions: opts.pluginActions ?? [],
      controlPlane: opts.controlPlane,
      emit: wrappedEmit,
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
    // Side-effect fences are parsed from the RAW agent output, not finalText:
    // Hermes hides builder fences from finalText (it collapses to the a2ui
    // markdown) and from the message stream, so only rawText still carries
    // them. claude-agent has no rawText and uses MCP tools, so this falls
    // back to finalText harmlessly. Each fence type is parsed independently
    // off the same source — they're distinct delimited blocks.
    const fenceSource =
      (result.rawText ?? result.finalText).trim() || assistantText.trim();
    const actionFences = extractActionRequestFences(fenceSource);
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

    // Workflow / policy save fences (Hermes path — claude-agent uses
    // the workflow_builder / policy_builder MCP tools). Same chain
    // pattern as action fences above: persist, emit a confirmation
    // surface, strip the fence body from the displayed text.
    const workflowFence = extractWorkflowSaveFence(fenceSource);
    if (workflowFence.payload) {
      try {
        const saved = await saveWorkflowWithTrigger({
          orgId,
          name: workflowFence.payload.name,
          description: workflowFence.payload.description,
          goal: workflowFence.payload.goal,
          systemPromptOverlay: workflowFence.payload.systemPromptOverlay,
          steps: workflowFence.payload.steps,
          triggers: workflowFence.payload.triggers,
          createdByThreadId: threadId,
          createdByRunId: runId,
        });
        await wrappedEmit({
          type: "surface",
          messages: workflowSavedCard({
            workflow: saved.workflow,
            action: saved.action,
          }),
        });
        if (saved.subscription) {
          await wrappedEmit({
            type: "surface",
            messages: subscriptionSavedCard({
              subscription: saved.subscription,
              workflowName: saved.workflow.name,
            }),
          });
        } else if (saved.triggerError) {
          await wrappedEmit({
            type: "error",
            message: `workflow saved, but its data trigger was not wired (${saved.triggerError.code}): ${saved.triggerError.message}`,
          });
        }
      } catch (err) {
        await wrappedEmit({
          type: "error",
          message: `workflow save failed: ${err instanceof Error ? err.message : err}`,
        });
      }
    } else if (workflowFence.errors.length > 0) {
      const reasons = workflowFence.errors.map((e) => e.reason).join("; ");
      await wrappedEmit({
        type: "error",
        message: `workflow save fence invalid: ${reasons}`,
      });
    }

    const policyFence = extractRuleSaveFence(fenceSource);
    if (policyFence.payload) {
      try {
        const saved = await upsertActionPolicyByName({
          orgId,
          name: policyFence.payload.name,
          description: policyFence.payload.description ?? "",
          appliesToKinds: policyFence.payload.applies_to_kinds,
          appliesToScopes: policyFence.payload.applies_to_scopes as ActionScope[],
          mode: policyFence.payload.mode as ActionPolicyMode,
          riskThresholdAutoApprove:
            (policyFence.payload.risk_threshold_auto_approve as
              | RiskLevel
              | undefined) ?? null,
          allowedTargets: policyFence.payload.allowed_targets ?? null,
          deniedTargets: policyFence.payload.denied_targets ?? null,
          limits: policyFence.payload.limits,
          approverRole: policyFence.payload.approver_role ?? null,
          priority: policyFence.payload.priority,
          enabled: policyFence.payload.enabled,
          createdByThreadId: threadId,
          createdByRunId: runId,
        });
        await wrappedEmit({
          type: "surface",
          messages: policySavedCard({
            policy: saved.policy,
            action: saved.action,
          }),
        });
      } catch (err) {
        await wrappedEmit({
          type: "error",
          message: `policy save failed: ${err instanceof Error ? err.message : err}`,
        });
      }
    } else if (policyFence.errors.length > 0) {
      const reasons = policyFence.errors.map((e) => e.reason).join("; ");
      await wrappedEmit({
        type: "error",
        message: `policy save fence invalid: ${reasons}`,
      });
    }

    // Per-run analysis value estimate (the human time the answer saved,
    // excluding any actions which carry their own estimate). Parsed from the
    // same raw source, server-clamped, persisted, and echoed in `done` so the
    // UI can show it live. Best-effort: a missing/invalid fence leaves it null.
    const valueFence = extractValueFence(fenceSource);
    const analysisMinutes = clampAnalysisMinutes(valueFence.payload?.minutes_saved);
    if (valueFence.payload) {
      try {
        await setWorkRunValue(runId, {
          minutes: analysisMinutes,
          basis: valueFence.payload.basis ?? null,
        });
      } catch (err) {
        console.warn(
          `[work-run] setWorkRunValue failed: ${err instanceof Error ? err.message : err}`,
        );
      }
    }

    // Ask-page right-rail context (vitals / sources / followups). Parsed from
    // the same source and emitted as an event the Ask UI lifts into the rail.
    const askContext = extractAskContextFence(fenceSource);
    if (askContext.payload) {
      await wrappedEmit({
        type: "ask_context",
        vitals: askContext.payload.vitals,
        sources: askContext.payload.sources,
        followups: askContext.payload.followups,
      });
    }

    // Pull any neko_memory fences out of the raw agent response and persist
    // them. Backend-agnostic: works for Hermes (no MCP tool registry) and is
    // harmless for claude-agent (which would have used the MCP save tool).
    const { ops: memoryOps } = extractMemoryFences(fenceSource);
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

    // The persisted assistant message is the cleaned DISPLAY text (finalText),
    // with any fence bodies that leaked into it stripped — never the raw
    // source, which still holds the a2ui block + builder fences. The a2ui card
    // and builder confirmation cards were already emitted as surfaces.
    let persistedText = extractActionRequestFences(result.finalText.trim()).text;
    persistedText = extractWorkflowSaveFence(persistedText).text;
    persistedText = extractRuleSaveFence(persistedText).text;
    persistedText = extractValueFence(persistedText).text;
    persistedText = extractAskContextFence(persistedText).text;
    persistedText = extractMemoryFences(persistedText).text;
    if (persistedText) {
      await saveAssistantWorkMessage({
        orgId,
        threadId,
        runId,
        content: persistedText,
      });
    }

    await wrappedEmit({
      type: "done",
      result: { status: result.status, minutesSaved: analysisMinutes ?? 0 },
    });

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
