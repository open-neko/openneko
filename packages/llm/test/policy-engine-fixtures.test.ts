import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { ActionPolicyRecord } from "../src/workflows/action-store";
import { deriveActionTargets, type ActionTargetSpec } from "../src/workflows/action-targets";
import { evaluateActionPolicy, type PolicyRequestSubject } from "../src/workflows/policy-engine";

// The Go policy check (apps/openneko/internal/webfront/turnactionstore.go)
// must give the same decisions. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite
// the fixture.
const FIXTURE = join(__dirname, "../../../apps/openneko/internal/webfront/testdata/policyengine.json");

function policy(name: string, fields: Partial<ActionPolicyRecord>): ActionPolicyRecord {
  return {
    id: `id-${name}`,
    orgId: "org-1",
    name,
    description: "",
    appliesToKinds: [],
    appliesToScopes: [],
    mode: "approval_required",
    riskThresholdAutoApprove: null,
    allowedTargets: null,
    deniedTargets: null,
    limits: {},
    approverGroupId: null,
    priority: 100,
    enabled: true,
    createdByThreadId: null,
    createdByRunId: null,
    createdAt: "2026-11-13T00:00:00.000Z" as unknown as Date,
    updatedAt: "2026-11-13T00:00:00.000Z" as unknown as Date,
    ...fields,
  };
}

const DEFAULTS = [
  policy("external_default", { appliesToScopes: ["external"], priority: 950 }),
  policy("user_management_default", { appliesToKinds: ["user_admin"], appliesToScopes: ["internal", "external"], priority: 90 }),
];
const SLACK_AUTO = policy("slack_auto", { appliesToKinds: ["slack_post"], mode: "auto_approve", riskThresholdAutoApprove: "medium" });
const MAIL_LIST = policy("mail_list", {
  appliesToKinds: ["send_email"],
  mode: "auto_approve",
  allowedTargets: { patterns: ["acme.com", "*.partner.io", "https://hooks.acme.com/*"], on_miss: "next" },
  priority: 50,
});
const MAIL_STRICT = policy("mail_strict", { appliesToKinds: ["send_email"], mode: "auto_approve", allowedTargets: { patterns: ["acme.com"] }, priority: 60 });
const BLOCK = policy("block_rivals", { mode: "never", deniedTargets: { patterns: ["rival.com", "*.rival.com"] }, priority: 10 });
const BLOCK_OBSERVE = policy("block_observe", { mode: "auto_approve", deniedTargets: { patterns: ["*"] }, appliesToKinds: ["wipe"], priority: 5 });

type Case = { name: string; subject: PolicyRequestSubject; policies: ActionPolicyRecord[] };

const CASES: Case[] = [
  { name: "no rules", subject: { scope: "external", kind: "slack_post" }, policies: [] },
  { name: "external default asks", subject: { scope: "external", kind: "slack_post", target: "C1" }, policies: DEFAULTS },
  { name: "internal has no rule", subject: { scope: "internal", kind: "slack_post" }, policies: DEFAULTS },
  { name: "kind rule wins by priority", subject: { scope: "external", kind: "slack_post", riskLevel: "low" }, policies: [...DEFAULTS, SLACK_AUTO] },
  { name: "risk over threshold", subject: { scope: "external", kind: "slack_post", riskLevel: "high" }, policies: [...DEFAULTS, SLACK_AUTO] },
  { name: "risk at threshold", subject: { scope: "external", kind: "slack_post", riskLevel: "medium" }, policies: [SLACK_AUTO] },
  { name: "unknown risk", subject: { scope: "external", kind: "slack_post", riskLevel: "extreme" as never }, policies: [SLACK_AUTO] },
  { name: "user admin rule", subject: { scope: "internal", kind: "user_admin" }, policies: DEFAULTS },
  { name: "denied target", subject: { scope: "external", kind: "send_email", target: "a@rival.com", targets: ["rival.com"] }, policies: [...DEFAULTS, BLOCK, MAIL_LIST] },
  { name: "denied subdomain", subject: { scope: "external", kind: "send_email", targets: ["eu.rival.com", "acme.com"] }, policies: [BLOCK, MAIL_LIST] },
  { name: "denied by observe rule", subject: { scope: "internal", kind: "wipe", target: "anything" }, policies: [BLOCK_OBSERVE] },
  { name: "deny pattern needs a target", subject: { scope: "internal", kind: "wipe" }, policies: [BLOCK_OBSERVE, ...DEFAULTS] },
  { name: "allowed targets all match", subject: { scope: "external", kind: "send_email", targets: ["acme.com", "eu.partner.io"] }, policies: [...DEFAULTS, MAIL_LIST] },
  { name: "allowed miss falls to next", subject: { scope: "external", kind: "send_email", targets: ["acme.com", "other.org"] }, policies: [...DEFAULTS, MAIL_LIST] },
  { name: "allowed miss denies", subject: { scope: "external", kind: "send_email", targets: ["other.org"] }, policies: [...DEFAULTS, MAIL_STRICT] },
  { name: "allowed with no targets", subject: { scope: "external", kind: "send_email", targets: [] }, policies: [MAIL_STRICT, ...DEFAULTS] },
  { name: "allowed prefix pattern", subject: { scope: "external", kind: "send_email", target: "https://hooks.acme.com/x" }, policies: [MAIL_LIST] },
  { name: "bare subdomain pattern", subject: { scope: "external", kind: "send_email", target: "partner.io" }, policies: [MAIL_LIST, MAIL_STRICT] },
  { name: "patterns not a list", subject: { scope: "external", kind: "x", target: "t" }, policies: [policy("odd", { mode: "auto_approve", allowedTargets: { patterns: "t" }, deniedTargets: { patterns: [1, "t"] } })] },
  { name: "draft only", subject: { scope: "external", kind: "x" }, policies: [policy("drafts", { mode: "draft_only" })] },
  { name: "observe only", subject: { scope: "external", kind: "x" }, policies: [policy("watch", { mode: "observe_only" })] },
  { name: "unknown mode", subject: { scope: "external", kind: "x" }, policies: [policy("odd", { mode: "sometimes" as never })] },
  { name: "same priority keeps order", subject: { scope: "external", kind: "x" }, policies: [policy("first", { mode: "never" }), policy("second", { mode: "auto_approve" })] },
  { name: "scope filter", subject: { scope: "internal", kind: "x" }, policies: [policy("ext", { appliesToScopes: ["external"], mode: "never" }), policy("any", { mode: "auto_approve", priority: 200 })] },
];

type TargetCase = { name: string; spec: ActionTargetSpec; payload: Record<string, unknown> | null };

const TARGETS: TargetCase[] = [
  { name: "webhook url", spec: { fields: ["url"], as: "value" }, payload: { url: "  https://hooks.acme.com/x  " } },
  { name: "no payload", spec: { fields: ["url"], as: "value" }, payload: null },
  { name: "email list", spec: { fields: ["to", "cc", "body.bcc"], as: "email_domain" }, payload: { to: "Ann <ann@Acme.COM>, bob@partner.io.", cc: ["c@x.org", "d@x.org", ""], body: { bcc: "nobody", extra: 1 } } },
  { name: "bad addresses", spec: { fields: ["to"], as: "email_domain" }, payload: { to: ["@acme.com", "ann@", "<>", "a@b@c.com"] } },
  { name: "non text values", spec: { fields: ["ids", "flag", "none"], as: "value" }, payload: { ids: [3, { a: 1 }, "  "], flag: true, none: null } },
  { name: "path through array", spec: { fields: ["rows.0.to"], as: "value" }, payload: { rows: [{ to: "x" }] } },
  { name: "sorted and unique", spec: { fields: ["a", "b"], as: "value" }, payload: { a: ["zeta", "Alpha", "beta", "Ä"], b: "zeta" } },
];

describe("policy engine fixtures for the Go policy check", () => {
  it("matches the fixture the Go test reads", () => {
    const fixture = `${JSON.stringify(
      {
        decisions: CASES.map((c) => ({ ...c, decision: evaluateActionPolicy(c.subject, c.policies) })),
        targets: TARGETS.map((c) => ({ ...c, targets: deriveActionTargets(c.spec, c.payload) })),
      },
      null,
      1,
    )}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(readFileSync(FIXTURE, "utf8"));
  });
});
