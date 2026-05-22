import { describe, expect, it } from "vitest";
import {
  policySavedCard,
  workflowSavedCard,
} from "../src/workflows/builder-cards";
import type { ActionPolicyRecord } from "../src/workflows/action-store";
import type { WorkflowRecord } from "../src/workflows/store";

const workflowFixture: WorkflowRecord = {
  id: "wf-1",
  orgId: "org-1",
  name: "APAC revenue dip check",
  description: "Daily check on APAC revenue.",
  enabled: true,
  status: "active",
  goal: "Surface meaningful APAC revenue dips.",
  systemPromptOverlay: "Show INR in lakhs.",
  steps: [
    { id: "pull", description: "Pull last 7 days" },
    { id: "compare", description: "Compare against prior 7" },
    { id: "flag", description: "Flag drops over 10%" },
  ],
  cron: "0 9 * * *",
  cronTimezone: "Asia/Kolkata",
  cronEnabled: true,
  dailyRunBudget: null,
  outputContract: null,
  createdByThreadId: "t-1",
  createdByRunId: "r-1",
  createdAt: new Date("2026-05-01T00:00:00Z"),
  updatedAt: new Date("2026-05-15T00:00:00Z"),
};

const policyFixture: ActionPolicyRecord = {
  id: "pol-1",
  orgId: "org-1",
  name: "slack_revenue_alerts_autoapprove",
  description: "Auto-approve low-risk Slack alert messages.",
  appliesToKinds: ["send_message"],
  appliesToScopes: ["external"],
  mode: "auto_approve",
  riskThresholdAutoApprove: "low",
  allowedTargets: null,
  deniedTargets: null,
  limits: { daily_cap: 20 },
  approverRole: null,
  priority: 100,
  enabled: true,
  createdAt: new Date("2026-05-01T00:00:00Z"),
  updatedAt: new Date("2026-05-15T00:00:00Z"),
};

describe("workflowSavedCard", () => {
  it("emits a v0.9 createSurface + updateComponents pair", () => {
    const messages = workflowSavedCard({
      workflow: workflowFixture,
      action: "created",
    });
    expect(messages).toHaveLength(2);
    expect(messages[0]).toMatchObject({
      version: "v0.9",
      createSurface: {
        surfaceId: "workflow-save-wf-1",
        catalogId: "urn:app:catalog:briefing:v1",
      },
    });
    expect(messages[1]).toMatchObject({
      version: "v0.9",
      updateComponents: { surfaceId: "workflow-save-wf-1" },
    });
  });

  it("shows the operator-friendly cron summary and a link to the detail page", () => {
    const messages = workflowSavedCard({
      workflow: workflowFixture,
      action: "updated",
    });
    const update = messages[1].updateComponents as { components: Array<{ id: string; text?: string }> };
    const body = update.components.find((c) => c.id === "body");
    expect(body?.text).toContain("3 step(s)");
    expect(body?.text).toContain("0 9 * * *");
    expect(body?.text).toContain("Asia/Kolkata");
    expect(body?.text).toContain("[Open detail](/work/workflows/wf-1)");
  });

  it("uses 'Created' vs 'Updated' verb based on the action", () => {
    const created = workflowSavedCard({ workflow: workflowFixture, action: "created" });
    const updated = workflowSavedCard({ workflow: workflowFixture, action: "updated" });
    const createdRoot = (created[1].updateComponents as { components: Array<{ id: string; greeting?: string }> }).components.find((c) => c.id === "root");
    const updatedRoot = (updated[1].updateComponents as { components: Array<{ id: string; greeting?: string }> }).components.find((c) => c.id === "root");
    expect(createdRoot?.greeting).toBe("Created workflow");
    expect(updatedRoot?.greeting).toBe("Updated workflow");
  });

  it("falls back to placeholder text when description is empty", () => {
    const messages = workflowSavedCard({
      workflow: { ...workflowFixture, description: "" },
      action: "created",
    });
    const body = (messages[1].updateComponents as { components: Array<{ id: string; text?: string }> }).components.find((c) => c.id === "body");
    expect(body?.text).toContain("_No description._");
  });
});

describe("policySavedCard", () => {
  it("renders mode, scopes, kinds, and a link to the detail page", () => {
    const messages = policySavedCard({
      policy: policyFixture,
      action: "created",
    });
    const body = (messages[1].updateComponents as { components: Array<{ id: string; text?: string }> }).components.find((c) => c.id === "body");
    expect(body?.text).toContain("`auto_approve`");
    expect(body?.text).toContain("external");
    expect(body?.text).toContain("`send_message`");
    expect(body?.text).toContain("[Open detail](/settings/rules/pol-1)");
  });

  it("shows '(all kinds)' when appliesToKinds is empty", () => {
    const messages = policySavedCard({
      policy: { ...policyFixture, appliesToKinds: [] },
      action: "created",
    });
    const body = (messages[1].updateComponents as { components: Array<{ id: string; text?: string }> }).components.find((c) => c.id === "body");
    expect(body?.text).toContain("_(all kinds)_");
  });
});
