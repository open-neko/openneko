import {
  createActionPolicy,
  listEnabledPolicies as defaultListEnabledPolicies,
  type ActionPolicyMode,
  type ActionPolicyRecord,
  type ActionScope,
  type RiskLevel,
} from "./action-store";

export type PolicyRequestSubject = {
  scope: ActionScope;
  kind: string;
  target?: string | null;
  riskLevel?: RiskLevel | null;
};

export type PolicyDecision =
  | {
      decision: "allow";
      policy: ActionPolicyRecord;
      mode: "auto_approve";
      reason?: string;
    }
  | {
      decision: "needs_approval";
      policy: ActionPolicyRecord;
      mode: "approval_required" | "draft_only";
      reason?: string;
    }
  | {
      decision: "deny";
      policy: ActionPolicyRecord;
      mode: "observe_only" | "never";
      reason: string;
    }
  | {
      decision: "no_policy";
      reason: "no_matching_policy";
    };

const RISK_ORDER: Record<RiskLevel, number> = {
  low: 0,
  medium: 1,
  high: 2,
  critical: 3,
};

function riskExceedsThreshold(
  riskLevel: RiskLevel | null | undefined,
  threshold: RiskLevel | null,
): boolean {
  if (threshold === null || threshold === undefined) return false;
  if (!riskLevel) return false;
  return RISK_ORDER[riskLevel] > RISK_ORDER[threshold];
}

function targetMatches(
  target: string | null | undefined,
  patterns: Record<string, unknown> | null,
): boolean {
  if (!patterns) return false;
  if (!target) return false;
  const list = patterns.patterns;
  if (!Array.isArray(list)) return false;
  return list.some((p) => {
    if (typeof p !== "string") return false;
    if (p.endsWith("*")) return target.startsWith(p.slice(0, -1));
    return target === p;
  });
}

function policyApplies(
  policy: ActionPolicyRecord,
  request: PolicyRequestSubject,
): boolean {
  if (
    policy.appliesToScopes.length > 0 &&
    !policy.appliesToScopes.includes(request.scope)
  ) {
    return false;
  }
  if (
    policy.appliesToKinds.length > 0 &&
    !policy.appliesToKinds.includes(request.kind)
  ) {
    return false;
  }
  return true;
}

/**
 * Evaluate a proposed action against the org's enabled policies. Returns
 * the first matching policy by priority (lower number = higher priority).
 *
 * Deterministic / pure given inputs; safe to unit-test without DB.
 */
export function evaluateActionPolicy(
  request: PolicyRequestSubject,
  policies: ActionPolicyRecord[],
): PolicyDecision {
  const ordered = [...policies].sort((a, b) => a.priority - b.priority);
  for (const policy of ordered) {
    if (!policyApplies(policy, request)) continue;
    if (targetMatches(request.target, policy.deniedTargets)) {
      return {
        decision: "deny",
        policy,
        mode: policy.mode === "never" ? "never" : "observe_only",
        reason: `target "${request.target}" is in policy "${policy.name}" denied_targets`,
      };
    }
    if (
      policy.allowedTargets &&
      Array.isArray((policy.allowedTargets as { patterns?: unknown }).patterns) &&
      !targetMatches(request.target, policy.allowedTargets)
    ) {
      return {
        decision: "deny",
        policy,
        mode: "observe_only",
        reason: `target "${request.target}" is not in policy "${policy.name}" allowed_targets`,
      };
    }
    return decisionFromMode(policy, request);
  }
  return { decision: "no_policy", reason: "no_matching_policy" };
}

function decisionFromMode(
  policy: ActionPolicyRecord,
  request: PolicyRequestSubject,
): PolicyDecision {
  switch (policy.mode) {
    case "never":
      return {
        decision: "deny",
        policy,
        mode: "never",
        reason: `policy "${policy.name}" mode=never`,
      };
    case "observe_only":
      return {
        decision: "deny",
        policy,
        mode: "observe_only",
        reason: `policy "${policy.name}" mode=observe_only`,
      };
    case "draft_only":
      return {
        decision: "needs_approval",
        policy,
        mode: "draft_only",
        reason: `policy "${policy.name}" mode=draft_only — operator approves before execute`,
      };
    case "approval_required":
      return {
        decision: "needs_approval",
        policy,
        mode: "approval_required",
        reason: `policy "${policy.name}" mode=approval_required`,
      };
    case "auto_approve":
      if (riskExceedsThreshold(request.riskLevel, policy.riskThresholdAutoApprove)) {
        return {
          decision: "needs_approval",
          policy,
          mode: "approval_required",
          reason: `risk=${request.riskLevel} exceeds policy "${policy.name}" auto-approve threshold (${policy.riskThresholdAutoApprove})`,
        };
      }
      return {
        decision: "allow",
        policy,
        mode: "auto_approve",
      };
    default:
      return {
        decision: "deny",
        policy,
        mode: "never",
        reason: `policy "${policy.name}" has unknown mode "${policy.mode}"`,
      };
  }
}

/**
 * Idempotent default-policy seeder. Seeds the baseline external_default
 * policy at priority 950 so org-specific policies (priority 100..900)
 * override it naturally.
 *
 * Internal product operations (memory writes, briefing creation,
 * workflow scheduling) are not policy-gated by design — they're product
 * features. The action stack and its policies only govern external
 * state changes (outbound webhooks, CRM updates, code commits, etc.).
 */
export async function seedDefaultActionPolicies(orgId: string): Promise<void> {
  const existing = await defaultListEnabledPolicies(orgId);
  const names = new Set(existing.map((p) => p.name));
  if (!names.has("external_default")) {
    await createActionPolicy({
      orgId,
      name: "external_default",
      description:
        "Operator must approve any external state-changing operation before it executes.",
      appliesToKinds: [],
      appliesToScopes: ["external"],
      mode: "approval_required" as ActionPolicyMode,
      riskThresholdAutoApprove: null,
      allowedTargets: null,
      deniedTargets: null,
      limits: {},
      approverRole: null,
      priority: 950,
      enabled: true,
    });
  }
}
