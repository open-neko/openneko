import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import {
  enqueue as defaultEnqueue,
  QUEUE,
} from "@neko/db/jobs";
import type { AgentEvent } from "../agent-backend";
import {
  createActionRequest,
  listEnabledPolicies as defaultListEnabledPolicies,
  type ActionRequestRecord,
  type ActionScope,
  type RiskLevel,
} from "./action-store";
import {
  evaluateActionPolicy,
  type PolicyDecision,
} from "./policy-engine";

export type WorkflowActionContext = {
  orgId: string;
  workflowRunId: string;
  workRunId: string;
  triggeredByObservationId?: string | null;
  emit: (event: AgentEvent) => Promise<void> | void;
  /** DI for tests. */
  listPolicies?: typeof defaultListEnabledPolicies;
  enqueue?: typeof defaultEnqueue;
};

const RISK_LEVELS = ["low", "medium", "high", "critical"] as const;
const SCOPES = ["internal", "external"] as const;

const REQUEST_DESCRIPTION = [
  "Propose a state-changing operation (a real action that mutates the",
  "world or a policy-governed internal write). Workflows decide;",
  "actions mutate. NEVER perform state-changing operations directly —",
  "always go through this tool so policy can gate them.",
  "",
  "Inputs:",
  "  scope: 'external' for outbound mutations (send_message,",
  "    mutate_record, open_pr, run_command, ...). 'internal' for",
  "    policy-governed writes inside OpenNeko itself (memory_write,",
  "    briefing_create, schedule_workflow). At scale these need policy",
  "    gating too, even though they look routine.",
  "  kind: short identifier of the operation. Free-form; the executor",
  "    registry routes by this string.",
  "  target: resource identifier (account id, slack channel, repo name,",
  "    memory id, etc.). Used by policies for allow/deny matching.",
  "  payload: full operation payload — exactly what the executor needs.",
  "  risk_level: your honest assessment of blast radius.",
  "  summary: one-sentence human-readable description for the approval",
  "    queue. Include WHAT will change and WHY.",
  "",
  "The tool returns one of:",
  "  ok: true, status: 'queued_for_execution'   — auto-approved",
  "  ok: true, status: 'pending_approval'       — operator must approve",
  "  ok: false, reason: ...                      — denied by policy",
  "Do not retry after a denial. Surface the reason to the operator and",
  "ask how to proceed.",
].join("\n");

export function buildWorkflowActionServer(ctx: WorkflowActionContext) {
  const requestAction = tool(
    "request",
    REQUEST_DESCRIPTION,
    {
      scope: z.enum(SCOPES),
      kind: z.string().trim().min(1).max(120),
      target: z.string().trim().min(1).max(1024).optional(),
      payload: z.record(z.string(), z.unknown()).optional(),
      risk_level: z.enum(RISK_LEVELS).optional(),
      summary: z.string().trim().min(1).max(2000),
    },
    async (args) => {
      const listPolicies = ctx.listPolicies ?? defaultListEnabledPolicies;
      const enqueue = ctx.enqueue ?? defaultEnqueue;

      const policies = await listPolicies(ctx.orgId);
      const decision = evaluateActionPolicy(
        {
          scope: args.scope as ActionScope,
          kind: args.kind,
          target: args.target ?? null,
          riskLevel: (args.risk_level as RiskLevel | undefined) ?? null,
        },
        policies,
      );

      if (decision.decision === "deny") {
        return jsonOk({
          ok: false,
          decision: "denied",
          reason: decision.reason,
          policy: decision.policy.name,
        });
      }
      if (decision.decision === "no_policy") {
        return jsonOk({
          ok: false,
          decision: "denied",
          reason:
            "no policy matches this scope/kind — refuse for safety. Ask the operator to define a policy first.",
        });
      }

      const status =
        decision.decision === "allow" ? "approved" : "pending_approval";

      const request = await createActionRequest({
        orgId: ctx.orgId,
        workflowRunId: ctx.workflowRunId,
        requestedByRunId: ctx.workflowRunId,
        triggeredByObservationId: ctx.triggeredByObservationId ?? null,
        policyId: decision.policy.id,
        scope: args.scope as ActionScope,
        kind: args.kind,
        target: args.target ?? null,
        payload: args.payload ?? {},
        riskLevel: (args.risk_level as RiskLevel | undefined) ?? null,
        summary: args.summary,
        status,
      });

      await emitActionRequestEvent(ctx, request, decision);

      if (status === "approved") {
        await enqueue(QUEUE.ACTION_EXECUTE, {
          orgId: ctx.orgId,
          actionRequestId: request.id,
        });
        return jsonOk({
          ok: true,
          decision: "auto_approved",
          status: "queued_for_execution",
          actionRequestId: request.id,
          policy: decision.policy.name,
        });
      }

      return jsonOk({
        ok: true,
        decision: "needs_approval",
        status: "pending_approval",
        actionRequestId: request.id,
        policy: decision.policy.name,
        reason: decision.reason,
      });
    },
  );

  return createSdkMcpServer({
    name: "neko_action",
    version: "1.0.0",
    tools: [requestAction],
  });
}

async function emitActionRequestEvent(
  ctx: WorkflowActionContext,
  request: ActionRequestRecord,
  decision: PolicyDecision,
): Promise<void> {
  if (decision.decision === "deny" || decision.decision === "no_policy") return;
  await ctx.emit({
    type: "action_request_emit",
    action_request_id: request.id,
    kind: request.kind,
    scope: request.scope,
    risk_level: request.riskLevel ?? undefined,
  });
}

function jsonOk(payload: Record<string, unknown>) {
  return {
    content: [
      {
        type: "text" as const,
        text: JSON.stringify(payload),
      },
    ],
  };
}
