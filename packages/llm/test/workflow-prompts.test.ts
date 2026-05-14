import { describe, expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import { buildWorkflowBuilderPrompt } from "../src/workflows/builder-prompt";
import { buildWorkflowRunnerPrompt } from "../src/workflows/runner-prompt";
import type { WorkflowRecord } from "../src/workflows/store";

const sampleWorkspace: AgentWorkspace = {
  orgRoot: "/tmp/org",
  skillsRoot: "/tmp/org/skills",
  memoryRoot: "/tmp/org/memory",
  knowledgeRoot: "/tmp/org/knowledge",
  uploadsRoot: "/tmp/org/uploads",
  runsRoot: "/tmp/org/runs",
  threadUploadsRoot: "/tmp/org/uploads/t1",
  runRoot: "/tmp/org/runs/r1",
  artifactRoot: "/tmp/org/runs/r1/artifacts",
  binRoot: "/tmp/org/runs/r1/bin",
  claudeProjectRoot: "/tmp/org",
  claudeConfigRoot: "/tmp/org/claude/config",
};

const sampleWorkflow: WorkflowRecord = {
  id: "wf1",
  orgId: "org1",
  name: "APAC revenue dip check",
  description: "Daily APAC revenue check.",
  enabled: true,
  status: "active",
  goal: "Surface revenue dips.",
  systemPromptOverlay: "Show INR in lakhs.",
  steps: [{ id: "pull", description: "Pull last 7 days of revenue" }],
  cron: null,
  cronTimezone: "UTC",
  cronEnabled: false,
  dailyRunBudget: null,
  outputContract: null,
  createdByThreadId: null,
  createdByRunId: null,
  createdAt: new Date(),
  updatedAt: new Date(),
};

describe("buildWorkflowBuilderPrompt", () => {
  it("instructs the agent to call the MCP tool when mcpTools=true", () => {
    const prompt = buildWorkflowBuilderPrompt({ mcpTools: true });
    expect(prompt).toContain("mcp__neko_workflow_builder__create_workflow");
    expect(prompt).not.toContain("```neko_workflow_save");
  });

  it("instructs the agent to emit a fence when mcpTools=false", () => {
    const prompt = buildWorkflowBuilderPrompt({ mcpTools: false });
    expect(prompt).toContain("```neko_workflow_save");
    expect(prompt).not.toContain("mcp__neko_workflow_builder__create_workflow");
  });

  it("never leaks risk_level prose in either branch", () => {
    for (const mcpTools of [true, false]) {
      const prompt = buildWorkflowBuilderPrompt({ mcpTools });
      // The builder prompt has no actions block, so risk_level shouldn't
      // appear at all.
      expect(prompt.toLowerCase()).not.toContain("risk level");
    }
  });
});

describe("buildWorkflowRunnerPrompt", () => {
  const base = {
    workflow: sampleWorkflow,
    mode: "headless" as const,
    memoryContext: "",
    backend: "hermes" as const,
    workspace: sampleWorkspace,
  };

  it("uses MCP tool names when mcpTools=true", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: true });
    expect(prompt).toContain("mcp__neko_workflow_output__emit");
    expect(prompt).toContain("mcp__neko_action__request");
    expect(prompt).not.toContain("```neko_workflow_output");
    expect(prompt).not.toContain("```neko_action_request");
  });

  it("uses fence instructions when mcpTools=false", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: false });
    expect(prompt).toContain("```neko_workflow_output");
    expect(prompt).toContain("```neko_action_request");
    expect(prompt).not.toContain("mcp__neko_workflow_output__emit");
    expect(prompt).not.toContain("mcp__neko_action__request");
  });

  it("includes the workflow's overlay and steps in both branches", () => {
    for (const mcpTools of [true, false]) {
      const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools });
      expect(prompt).toContain("Show INR in lakhs.");
      expect(prompt).toContain("Pull last 7 days of revenue");
    }
  });

  it("includes data_access guidance pointing at the knowledge pack", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: false });
    expect(prompt).toContain("<data_access>");
    expect(prompt).toContain("graphjin cli execute_graphql");
    expect(prompt).toContain(sampleWorkspace.knowledgeRoot);
  });

  it("inlines the aggregation rules so the agent doesn't have to discover them", () => {
    const prompt = buildWorkflowRunnerPrompt({ ...base, mcpTools: false });
    expect(prompt).toContain("<fn>_<column>");
    expect(prompt).toContain("distinct:");
    expect(prompt).toContain("sum(expr:");
  });
});
