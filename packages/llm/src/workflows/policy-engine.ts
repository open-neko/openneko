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
  // ADM1: user management from chat needs an ADMIN approval (K2 enforces).
  if (!names.has("user_management_default")) {
    await createActionPolicy({
      orgId,
      name: "user_management_default",
      description:
        "Inviting, role changes, and deactivation proposed from chat require an admin's approval.",
      appliesToKinds: ["user_admin"],
      appliesToScopes: ["internal", "external"],
      mode: "approval_required" as ActionPolicyMode,
      riskThresholdAutoApprove: null,
      allowedTargets: null,
      deniedTargets: null,
      limits: {},
      approverRole: "admin",
      priority: 90,
      enabled: true,
    });
  }
  // ADM3: chat-driven plugin management is always an explicit approval.
  // SEC8: org/hardened postures require that approval to come from an ADMIN.
  if (!names.has("plugin_management_default")) {
    const { profilePolicyDefaults } = await import("../work/deployment-profile");
    await createActionPolicy({
      orgId,
      name: "plugin_management_default",
      description:
        "Installing or removing a plugin from chat always requires operator approval.",
      appliesToKinds: ["plugin_install", "plugin_uninstall"],
      appliesToScopes: ["internal", "external"],
      mode: "approval_required" as ActionPolicyMode,
      riskThresholdAutoApprove: null,
      allowedTargets: null,
      deniedTargets: null,
      limits: {},
      approverRole: profilePolicyDefaults().pluginApproverRole,
      priority: 100,
      enabled: true,
    });
  }
  // OL6: code actions require explicit approval unless an org policy
  // says otherwise — never autonomous.
  if (!names.has("code_action_default")) {
    await createActionPolicy({
      orgId,
      name: "code_action_default",
      description:
        "Filing issues and drafting patches always require operator approval; OpenNeko never applies code changes itself.",
      appliesToKinds: ["code_create_issue", "code_draft_patch"],
      appliesToScopes: ["internal", "external"],
      mode: "approval_required" as ActionPolicyMode,
      riskThresholdAutoApprove: null,
      allowedTargets: null,
      deniedTargets: null,
      limits: {},
      approverRole: null,
      priority: 94,
      enabled: true,
    });
  }
  // ADM2: data-source registry changes from chat need an ADMIN.
  if (!names.has("data_source_management_default")) {
    await createActionPolicy({
      orgId,
      name: "data_source_management_default",
      description:
        "Registering, enabling/disabling or removing a data source proposed from chat requires an admin's approval.",
      appliesToKinds: ["data_source_admin"],
      appliesToScopes: ["internal", "external"],
      mode: "approval_required" as ActionPolicyMode,
      riskThresholdAutoApprove: null,
      allowedTargets: null,
      deniedTargets: null,
      limits: {},
      approverRole: "admin",
      priority: 96,
      enabled: true,
    });
  }
  // ADM5: linking/blocking channel identities from chat needs an ADMIN.
  if (!names.has("channel_management_default")) {
    await createActionPolicy({
      orgId,
      name: "channel_management_default",
      description:
        "Linking, unlinking or blocking a channel identity proposed from chat requires an admin's approval.",
      appliesToKinds: ["channel_admin"],
      appliesToScopes: ["internal", "external"],
      mode: "approval_required" as ActionPolicyMode,
      riskThresholdAutoApprove: null,
      allowedTargets: null,
      deniedTargets: null,
      limits: {},
      approverRole: "admin",
      priority: 95,
      enabled: true,
    });
  }
}

/**
 * Per-plugin-kind policy seeder. Reads the seeded `default_mode` each
 * plugin declares on its action kinds and creates a kind-specific
 * action_policy row when the declaration deviates from the host's
 * baseline.
 *
 * Mapping:
 *   default_mode = "auto"  → seed auto_approve at priority 900
 *                            (overrides the external_default
 *                            approval_required at 950)
 *   default_mode = "ask"   → no seed; falls through to external_default
 *                            (approval_required) — already the safe
 *                            default
 *   default_mode = "deny"  → no seed; the agent tool builder excludes
 *                            the kind from the agent's surface entirely
 *   undefined              → treated as "ask"
 *
 * Idempotent: each kind gets one policy row named
 * "plugin:<pluginName>:auto:<kind>". Re-running this function (e.g.
 * after a manifest refresh) is a no-op for kinds already seeded;
 * operators who edit or disable the row keep their changes.
 */
export interface PluginActionSeed {
  pluginName: string;
  kind: string;
  description: string;
  default_mode?:
    | "auto"
    | "ask"
    | "deny"
    | { external?: "auto" | "ask" | "deny"; internal?: "auto" | "ask" | "deny" };
}

type PluginActionScope = "external" | "internal";

function modeForScope(
  default_mode: PluginActionSeed["default_mode"],
  scope: PluginActionScope,
): "auto" | "ask" | "deny" | undefined {
  if (default_mode === undefined) return undefined;
  if (typeof default_mode === "string") return default_mode;
  return scope === "external" ? default_mode.external : default_mode.internal;
}

export async function seedPluginActionPolicies(
  orgId: string,
  seeds: readonly PluginActionSeed[],
): Promise<{ created: number; skipped: number }> {
  if (seeds.length === 0) return { created: 0, skipped: 0 };
  const existing = await defaultListEnabledPolicies(orgId);
  const existingNames = new Set(existing.map((p) => p.name));

  let created = 0;
  let skipped = 0;
  for (const seed of seeds) {
    // Walk every scope the kind cares about. Scalar default_mode is
    // applied to "external" only (the common case — every existing
    // first-party kind is external-facing). Object form lets plugin
    // authors opt into per-scope defaults.
    const scopes: PluginActionScope[] =
      seed.default_mode && typeof seed.default_mode === "object"
        ? (Object.keys(seed.default_mode) as PluginActionScope[])
        : ["external"];
    for (const scope of scopes) {
      const mode = modeForScope(seed.default_mode, scope);
      // Only auto kinds need a seeded row — ask falls through to
      // external_default (approval_required); deny is the agent
      // tool-builder's job to enforce by omission.
      if (mode !== "auto") {
        skipped++;
        continue;
      }
      const name = `plugin:${seed.pluginName}:auto:${seed.kind}:${scope}`;
      if (existingNames.has(name)) {
        skipped++;
        continue;
      }
      await createActionPolicy({
        orgId,
        name,
        description: `Auto-approve "${seed.kind}" (${scope}) — seeded from ${seed.pluginName}. ${seed.description}`,
        appliesToKinds: [seed.kind],
        appliesToScopes: [scope],
        mode: "auto_approve" as ActionPolicyMode,
        riskThresholdAutoApprove: null,
        allowedTargets: null,
        deniedTargets: null,
        limits: {},
        approverRole: null,
        priority: 900,
        enabled: true,
      });
      existingNames.add(name);
      created++;
    }
  }
  return { created, skipped };
}
