import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import type { AgentEvent } from "../agent-backend";
import {
  listAllPolicies,
  upsertActionPolicyByName,
  type ActionPolicyMode,
  type ActionScope,
  type RiskLevel,
} from "./action-store";
import { policySavedCard } from "./builder-cards";
import { POLICY_SAVE_SCHEMA } from "./fence-schemas";

export type RuleBuilderContext = {
  orgId: string;
  createdByThreadId?: string | null;
  createdByRunId?: string | null;
  emit?: (event: AgentEvent) => Promise<void> | void;
};

export function buildRuleBuilderServer(ctx: RuleBuilderContext) {
  const saveRuleTool = tool(
    "save_rule",
    [
      "Create or update an approval rule. Upserts by name within the org —",
      "if a rule with the same name already exists, it is updated in place.",
      "Use this when the operator asks to gate, allow, or deny specific action",
      "kinds (e.g. 'auto-approve low-risk slack posts', 'always ask before",
      "sending email externally'). After saving, the tool emits a confirmation",
      "card the operator can click to open the rule.",
    ].join(" "),
    POLICY_SAVE_SCHEMA.shape,
    async (args) => {
      const result = await upsertActionPolicyByName({
        orgId: ctx.orgId,
        name: args.name,
        description: args.description ?? "",
        appliesToKinds: args.applies_to_kinds,
        appliesToScopes: args.applies_to_scopes as ActionScope[],
        mode: args.mode as ActionPolicyMode,
        riskThresholdAutoApprove:
          (args.risk_threshold_auto_approve as RiskLevel | undefined) ?? null,
        allowedTargets: args.allowed_targets ?? null,
        deniedTargets: args.denied_targets ?? null,
        limits: args.limits,
        approverRole: args.approver_role ?? null,
        priority: args.priority,
        enabled: args.enabled,
        createdByThreadId: ctx.createdByThreadId ?? null,
        createdByRunId: ctx.createdByRunId ?? null,
      });
      if (ctx.emit) {
        await ctx.emit({
          type: "surface",
          messages: policySavedCard({
            policy: result.policy,
            action: result.action,
          }),
        });
      }
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              action: result.action,
              ruleId: result.policy.id,
              name: result.policy.name,
            }),
          },
        ],
      };
    },
  );

  const listRulesTool = tool(
    "list_rules",
    [
      "List the approval rules defined in this org so you can answer questions",
      "like 'what was that rule we set last week to auto-approve slack posts'",
      "or look up the exact name/config of a rule before updating it via",
      "`save_rule`. Returns full bodies (mode, scopes, kinds, limits), ordered",
      "by priority then name. Includes disabled rules.",
    ].join(" "),
    {
      limit: z.number().int().min(1).max(200).optional(),
    },
    async (args) => {
      const all = await listAllPolicies(ctx.orgId);
      const limit = args.limit ?? 50;
      const slice = all.slice(0, limit);
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              total: all.length,
              returned: slice.length,
              rules: slice.map((p) => ({
                id: p.id,
                name: p.name,
                description: p.description,
                mode: p.mode,
                appliesToKinds: p.appliesToKinds,
                appliesToScopes: p.appliesToScopes,
                riskThresholdAutoApprove: p.riskThresholdAutoApprove,
                allowedTargets: p.allowedTargets,
                deniedTargets: p.deniedTargets,
                limits: p.limits,
                approverRole: p.approverRole,
                priority: p.priority,
                enabled: p.enabled,
                updatedAt: p.updatedAt.toISOString(),
                createdAt: p.createdAt.toISOString(),
              })),
            }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_rule_builder",
    version: "1.0.0",
    tools: [saveRuleTool, listRulesTool],
  });
}
