import { startupElapsedMs } from "@neko/telemetry/startup";
import type { HarnessObserver } from "@neko/telemetry";
import { observeSafely } from "@neko/telemetry";
import type { AgentEvent } from "../agent-backend";
import { normalizeGraphjinAgentUsage } from "../usage-normalization";

type ObservationStatus = "ok" | "error";

function byteLength(value: unknown): number {
  if (value === undefined) return 0;
  try {
    return Math.min(
      Buffer.byteLength(
        typeof value === "string" ? value : JSON.stringify(value),
        "utf8",
      ),
      1024 * 1024 * 1024,
    );
  } catch {
    return 0;
  }
}

function isGraphjinAgentTool(name: string): boolean {
  return name.toLocaleLowerCase().includes("neko_graphjin_agent");
}

/**
 * One metadata-only instrumentation layer for Ask and workflow agent loops.
 * It consumes AgentEvent shapes but never forwards their content-bearing
 * fields to the observer.
 */
export function createAgentEventTelemetry(input: {
  observer?: HarnessObserver;
  operationId: string;
}) {
  const stageOperationId = `${input.operationId}:agent`;
  const modelOperationId = `${input.operationId}:model:1`;
  const toolStarts = new Map<string, { name: string; startedAt: number }>();
  const modelCalls = new Map<number, {model:string; provider:string; stage:string; startedAt:number}>();
  let agentStartedAt = Date.now();
  let backendKind = "";
  let firstOutputObserved = false;
  let stageOpen = false;
  let modelOpen = false;
  let costSeen = false;
  let observationReads = 0;
  let outerUsage: Extract<AgentEvent, { type: "usage" }> | undefined;

  const observe = async (
    observation: Parameters<HarnessObserver["observe"]>[0],
  ): Promise<void> => observeSafely(input.observer, observation);

  const observeCost = async (event: Extract<AgentEvent, {type: "cost"}>): Promise<void> => {
    if (costSeen) return;
    costSeen = true;
    await observe({
      kind: "run.cost",
      operationId: `${input.operationId}:cost`,
      parentOperationId: input.operationId,
      attributes: {"openneko.cost.scope": "outer-and-graphjin", "openneko.cost.budget_micros": event.budgetMicros},
      measurements: {estimatedCostUsd: event.chargedMicros / 1_000_000, currency: "USD",
        pricingCatalogVersion: event.pricingVersion, costStatus: "estimated", costSource: "harness-admission", coverage: "complete"},
    });
  };

  const observeEvent = async (event: AgentEvent): Promise<void> => {
    if (event.type === "model_call") {
      const operationId = `${input.operationId}:model-call:${event.callId}`;
      const attributes = {"openneko.model.scope":"outer_call", "openneko.agent.stage":event.stage,
        "openneko.model.call_id":event.callId,
        "gen_ai.provider.name":event.provider,
        "gen_ai.request.model":event.model};
      if (event.phase === "started") {
        if (modelCalls.has(event.callId)) return;
        modelCalls.set(event.callId,{model:event.model,provider:event.provider,stage:event.stage,startedAt:Date.now()});
        await observe({kind:"model.request",operationId,parentOperationId:stageOperationId,attributes});
      } else {
        const prior = modelCalls.get(event.callId);
        modelCalls.delete(event.callId);
        await observe({kind:"model.response",operationId,parentOperationId:stageOperationId,
          status:event.failed ? "error" : "ok",...(event.failed ? {errorType:"model_request_failed"} : {}),
          attributes:{...attributes,"gen_ai.response.model":event.model},
          measurements:{durationMs:event.durationMs ?? (prior ? Date.now()-prior.startedAt : 0),
            ...(event.usage ?? {coverage:"unavailable" as const}),
            ...(event.chargedMicros !== undefined ? {estimatedCostUsd:event.chargedMicros/1_000_000,
              currency:"USD",costStatus:"estimated" as const,costSource:"harness-admission"} : {})}});
      }
      return;
    }
    if (
      !firstOutputObserved &&
      ((event.type === "message" && event.role === "assistant") ||
        event.type === "provisional_answer" ||
        event.type === "surface")
    ) {
      firstOutputObserved = true;
      const firstOutputMs = Date.now() - agentStartedAt;
      await observe({
        kind: "model.first_chunk",
        attributes: { "openneko.timing.basis": "agent_first_output", "openneko.timing.provider_ttft": false },
        operationId: modelOperationId,
        parentOperationId: stageOperationId,
        measurements: { firstOutputMs, coverage: "unavailable" },
      });
      await observe({
        kind: "run.first_output",
        operationId: input.operationId,
        measurements: { firstOutputMs: startupElapsedMs() ?? firstOutputMs, coverage: "unavailable" },
      });
    }
    if (event.type === "tool_start") {
      toolStarts.set(event.id, { name: event.name, startedAt: Date.now() });
      await observe({
        kind: "tool.start",
        operationId: `${input.operationId}:tool:${event.id}`,
        parentOperationId: modelOperationId,
        attributes: { "gen_ai.tool.name": event.name },
        measurements: {
          inputBytes: byteLength(event.input),
          coverage: "unavailable",
        },
      });
      if (isGraphjinAgentTool(event.name)) {
        await observe({
          kind: "delegation.start",
          operationId: `${input.operationId}:delegation:${event.id}`,
          parentOperationId: modelOperationId,
          attributes: { "openneko.delegation.target": "graphjin-agent" },
        });
        await observe({
          kind: "model.request",
          operationId: `${input.operationId}:inner-model:${event.id}`,
          parentOperationId: `${input.operationId}:delegation:${event.id}`,
          attributes: { "openneko.model.scope": "inner" },
        });
      } else if (event.name === "ax_child_agent") {
        await observe({
          kind: "delegation.start",
          operationId: `${input.operationId}:delegation:${event.id}`,
          parentOperationId: modelOperationId,
          attributes: { "openneko.delegation.target": "ax-child-agent" },
        });
      }
      return;
    }
    if (event.type === "tool_end") {
      const started = toolStarts.get(event.id);
      toolStarts.delete(event.id);
      await observe({
        kind: "tool.end",
        operationId: `${input.operationId}:tool:${event.id}`,
        parentOperationId: modelOperationId,
        status: event.error ? "error" : "ok",
        ...(event.error ? { errorType: "tool_error" } : {}),
        attributes: { "gen_ai.tool.name": started?.name ?? "unknown" },
        measurements: {
          ...(started ? { durationMs: Date.now() - started.startedAt } : {}),
          outputBytes: byteLength(event.result ?? event.error),
          coverage: "unavailable",
        },
      });
      if (started && isGraphjinAgentTool(started.name)) {
        const harnessLookup = event.id.startsWith("harness-operation-");
        const inner = harnessLookup ? undefined : normalizeGraphjinAgentUsage(event.result);
        const remote = harnessLookup ? event.remoteUsage : undefined;
        await observe({
          kind: "model.response",
          operationId: `${input.operationId}:inner-model:${event.id}`,
          parentOperationId: `${input.operationId}:delegation:${event.id}`,
          status: event.error ? "error" : "ok",
          ...(event.error ? { errorType: "graphjin_agent_error" } : {}),
          attributes: {
            "openneko.model.scope": "inner",
            ...(inner?.provider
              ? { "gen_ai.provider.name": inner.provider }
              : {}),
            ...(inner?.model ? { "gen_ai.response.model": inner.model } : {}),
          },
          measurements: remote?.reported ? {
            ...(remote.promptTokens !== undefined ? { inputTokens: remote.promptTokens } : {}),
            ...(remote.completionTokens !== undefined ? { outputTokens: remote.completionTokens } : {}),
            totalTokens: remote.totalTokens,
            coverage: remote.promptTokens !== undefined && remote.completionTokens !== undefined ? "complete" : "partial",
          } : inner?.usage ?? {
            coverage: "unavailable",
            missingReasons: ["GraphJin agent response omitted flat usage"],
          },
        });
        await observe({
          kind: "delegation.end",
          operationId: `${input.operationId}:delegation:${event.id}`,
          parentOperationId: modelOperationId,
          status: event.error ? "error" : "ok",
          ...(event.error ? { errorType: "graphjin_agent_error" } : {}),
          attributes: { "openneko.delegation.target": "graphjin-agent" },
        });
      } else if (started?.name === "ax_child_agent") {
        await observe({
          kind: "delegation.end",
          operationId: `${input.operationId}:delegation:${event.id}`,
          parentOperationId: modelOperationId,
          status: event.error ? "error" : "ok",
          ...(event.error ? { errorType: "child_agent_error" } : {}),
          attributes: { "openneko.delegation.target": "ax-child-agent" },
        });
      }
      return;
    }
    if (event.type === "usage" && event.source === "outer") {
      outerUsage = event;
      return;
    }
    if (event.type === "cost" && event.source === "harness") {
      await observeCost(event);
      return;
    }
    if (event.type === "stage_usage" && event.source === "harness") {
      await observe({
        kind: "model.stage_usage",
        operationId: `${input.operationId}:stage-usage:${event.stage}`,
        parentOperationId: modelOperationId,
        attributes: {
          "openneko.agent.stage": event.stage,
          "openneko.model.scope": "outer",
          "openneko.model.requests": event.requests,
          "openneko.model.reported_requests": event.reported,
        },
        measurements: event.usage,
      });
      return;
    }
    if (event.type === "tool_catalog_profile") {
      await observe({
        kind: "tool.catalog",
        operationId: `${input.operationId}:tool-catalog:${event.actor}`,
        parentOperationId: stageOperationId,
        attributes: {
          "openneko.tool.catalog.actor": event.actor,
          "openneko.tool.catalog.count": event.count,
          "openneko.tool.catalog.schema_bytes": event.schemaBytes,
          "openneko.tool.catalog.descriptor_bytes": event.descriptorBytes,
        },
      });
      return;
    }
    if (event.type === "tool_selection_error") {
      await observe({
        kind: "tool.selection_error",
        operationId: `${input.operationId}:tool-selection`,
        parentOperationId: stageOperationId,
        attributes: {"gen_ai.tool.name": event.name, "openneko.tool.selection_error": event.reason},
      });
      return;
    }
    if (event.type === "observation_read") {
      observationReads++;
      await observe({
        kind: "tool.observation_read",
        operationId: `${input.operationId}:observation-read:${observationReads}`,
        parentOperationId: stageOperationId,
        attributes: {
          "openneko.tool.operation_id": event.operationId,
          "openneko.observation.instruction_bytes": event.instructionBytes,
          "openneko.observation.result_bytes": event.resultBytes,
        },
      });
      return;
    }
    if (
      event.type === "status" &&
      event.message === "Hermes returned no output; retrying…"
    ) {
      await observe({
        kind: "retry",
        operationId: `${input.operationId}:retry:empty-output`,
        parentOperationId: modelOperationId,
        attributes: { "openneko.retry.reason": "empty_output" },
      });
      return;
    }
    if (event.type === "action_request_emit") {
      await observe({
        kind: "policy.decision",
        operationId: `${input.operationId}:policy:${event.action_request_id}`,
        parentOperationId: input.operationId,
        status: "ok",
        attributes: {
          "openneko.policy.decision": event.decision,
          "openneko.action.kind": event.kind,
          "openneko.action.scope": event.scope,
        },
      });
      return;
    }
    if (event.type === "action_request_result") {
      await observe({
        kind: "approval.decision",
        operationId: `${input.operationId}:action:${event.action_request_id}`,
        parentOperationId: input.operationId,
        status: event.status === "failed" || event.status === "partially_applied" || event.status === "reconcile_required" ? "error" : "ok",
        attributes: {
          "openneko.action.kind": event.kind,
          "openneko.action.status": event.status,
        },
      });
    }
  };

  const closeModelCalls = async (reason: string): Promise<void> => {
    for (const [callId, call] of modelCalls) {
      await observe({kind:"model.response",operationId:`${input.operationId}:model-call:${callId}`,
        parentOperationId:stageOperationId,status:"error",errorType:"model_receipt_missing",
        attributes:{"openneko.model.scope":"outer_call","openneko.agent.stage":call.stage,
          "openneko.model.call_id":callId,"gen_ai.provider.name":call.provider,
          "gen_ai.response.model":call.model},
        measurements:{durationMs:Date.now()-call.startedAt,coverage:"unavailable",missingReasons:[reason]}});
    }
    modelCalls.clear();
  };

  const startAgent = async (metadata: {
    backend: string;
    model?: string;
    inputBytes?: number;
  }): Promise<void> => {
    backendKind = metadata.backend;
    agentStartedAt = Date.now();
    await observe({
      kind: "stage.start",
      operationId: stageOperationId,
      parentOperationId: input.operationId,
      attributes: { "openneko.stage": "agent" },
    });
    stageOpen = true;
    if (backendKind === "harness") return;
    await observe({
      kind: "model.request",
      operationId: modelOperationId,
      parentOperationId: stageOperationId,
      attributes: {
        "openneko.model.scope": "outer",
        "openneko.backend": metadata.backend,
        ...(metadata.model ? { "gen_ai.request.model": metadata.model } : {}),
      },
      ...(metadata.inputBytes !== undefined
        ? {
            measurements: {
              inputBytes: Math.max(0, metadata.inputBytes),
              coverage: "unavailable" as const,
            },
          }
        : {}),
    });
    modelOpen = true;
  };

  const finishAgent = async (result: {
    status: ObservationStatus;
    errorType?: string;
    outputBytes?: number;
    cost?: Extract<AgentEvent, {type: "cost"}>;
  }): Promise<void> => {
    if (result.cost) await observeCost(result.cost);
    await closeModelCalls("Harness model finish receipt missing");
    if (backendKind !== "harness") await observe({
      kind: "model.response",
      operationId: modelOperationId,
      parentOperationId: stageOperationId,
      status: result.status,
      ...(result.errorType ? { errorType: result.errorType } : {}),
      attributes: {
        "openneko.model.scope": "outer",
        ...(outerUsage?.provider
          ? { "gen_ai.provider.name": outerUsage.provider }
          : {}),
        ...(outerUsage?.model
          ? { "gen_ai.response.model": outerUsage.model }
          : {}),
      },
      measurements: {
        ...(outerUsage?.usage ?? {
          coverage: "unavailable" as const,
          missingReasons: ["backend emitted no normalized usage"],
        }),
        ...(result.outputBytes !== undefined
          ? { outputBytes: Math.max(0, result.outputBytes) }
          : {}),
      },
    });
    modelOpen = false;
    await observe({
      kind: "stage.end",
      operationId: stageOperationId,
      parentOperationId: input.operationId,
      status: result.status,
      ...(result.errorType ? { errorType: result.errorType } : {}),
      attributes: { "openneko.stage": "agent" },
      measurements: {
        durationMs: Date.now() - agentStartedAt,
        ...(backendKind === "harness" ? outerUsage?.usage ?? {coverage:"unavailable" as const,
          missingReasons:["backend emitted no normalized usage"]} : {coverage:"unavailable" as const}),
      },
    });
    stageOpen = false;
  };

  const closeOpen = async (result: {
    status: ObservationStatus;
    outcome: string;
    errorType?: string;
    usageMissingReason: string;
  }): Promise<void> => {
    await closeModelCalls(result.usageMissingReason);
    for (const [toolId, tool] of toolStarts) {
      if (isGraphjinAgentTool(tool.name)) {
        await observe({
          kind: "model.response",
          operationId: `${input.operationId}:inner-model:${toolId}`,
          parentOperationId: `${input.operationId}:delegation:${toolId}`,
          status: result.status,
          ...(result.errorType ? { errorType: result.errorType } : {}),
          measurements: {
            coverage: "unavailable",
            missingReasons: [result.usageMissingReason],
          },
        });
        await observe({
          kind: "delegation.end",
          operationId: `${input.operationId}:delegation:${toolId}`,
          parentOperationId: modelOperationId,
          status: result.status,
          ...(result.errorType ? { errorType: result.errorType } : {}),
          attributes: { "openneko.delegation.target": "graphjin-agent" },
        });
      } else if (tool.name === "ax_child_agent") {
        await observe({
          kind: "delegation.end",
          operationId: `${input.operationId}:delegation:${toolId}`,
          parentOperationId: modelOperationId,
          status: result.status,
          ...(result.errorType ? { errorType: result.errorType } : {}),
          attributes: { "openneko.delegation.target": "ax-child-agent" },
        });
      }
      await observe({
        kind: "tool.end",
        operationId: `${input.operationId}:tool:${toolId}`,
        parentOperationId: modelOperationId,
        status: result.status,
        ...(result.errorType ? { errorType: result.errorType } : {}),
        attributes: { "gen_ai.tool.name": tool.name },
        measurements: {
          durationMs: Date.now() - tool.startedAt,
          coverage: "unavailable",
        },
      });
    }
    toolStarts.clear();
    if (modelOpen) {
      await observe({
        kind: "model.response",
        operationId: modelOperationId,
        parentOperationId: stageOperationId,
        status: result.status,
        ...(result.errorType ? { errorType: result.errorType } : {}),
        attributes: { "openneko.outcome": result.outcome },
        measurements: outerUsage?.usage ?? {
          coverage: "unavailable",
          missingReasons: [result.usageMissingReason],
        },
      });
      modelOpen = false;
    }
    if (stageOpen) {
      await observe({
        kind: "stage.end",
        operationId: stageOperationId,
        parentOperationId: input.operationId,
        status: result.status,
        ...(result.errorType ? { errorType: result.errorType } : {}),
        attributes: {
          "openneko.stage": "agent",
          "openneko.outcome": result.outcome,
        },
        measurements: {
          durationMs: Date.now() - agentStartedAt,
          ...(backendKind === "harness" ? outerUsage?.usage ?? {coverage:"unavailable" as const,
            missingReasons:[result.usageMissingReason]} : {coverage:"unavailable" as const}),
        },
      });
      stageOpen = false;
    }
    if (result.status === "error") {
      await observe({
        kind: "error",
        operationId: `${input.operationId}:error`,
        parentOperationId: input.operationId,
        status: "error",
        ...(result.errorType ? { errorType: result.errorType } : {}),
        attributes: { "openneko.outcome": result.outcome },
      });
    }
  };

  return { observeEvent, startAgent, finishAgent, closeOpen };
}
