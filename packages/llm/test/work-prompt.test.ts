import { describe, expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import type { KnowledgePackContents } from "../src/knowledge-pack";
import { buildWorkPrompt } from "../src/work/prompt";

const workspace: AgentWorkspace = {
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

const knowledge: KnowledgePackContents = {
  tables: "{}",
  namespaces: "{}",
  insights: "{}",
  syntax: "{}",
};

function build(
  backend: "claude-agent" | "hermes",
  overrides: {
    supportsCardTool?: boolean;
    supportsSkillTool?: boolean;
    supportsMemoryTool?: boolean;
    supportsWorkflowTool?: boolean;
    supportsPolicyTool?: boolean;
  } = {},
): string {
  return buildWorkPrompt({
    backend,
    workspace,
    knowledge,
    messages: [],
    currentUserMessage: "test",
    supportsCardTool: overrides.supportsCardTool ?? false,
    supportsSkillTool: overrides.supportsSkillTool ?? false,
    supportsMemoryTool: overrides.supportsMemoryTool ?? false,
    supportsWorkflowTool: overrides.supportsWorkflowTool ?? false,
    supportsPolicyTool: overrides.supportsPolicyTool ?? false,
    inlineTranscript: false,
  });
}

describe("buildWorkPrompt attachments guidance", () => {
  it("tells the claude-agent how to read uploads and which path shape to expect", () => {
    const prompt = build("claude-agent");
    expect(prompt).toContain("<attachments>");
    expect(prompt).toContain("uploads/<threadId>/<filename>");
    expect(prompt).toContain("`Read` tool");
    // Path is relative to cwd, which is orgRoot.
    expect(prompt).toContain(workspace.orgRoot);
  });

  it("references the hermes shell tool when running under hermes", () => {
    const prompt = build("hermes");
    expect(prompt).toContain("<attachments>");
    // Hermes' shell tool is `terminal`, claude-agent's is `Bash`. The
    // attachments block names whichever one is wired up so the agent picks
    // the right tool for non-text formats.
    expect(prompt).toContain("`terminal`");
  });

  it("no longer dismisses uploaded files as 'auxiliary'", () => {
    const prompt = build("claude-agent");
    // The old wording told the model uploaded files were auxiliary, which it
    // routinely took as permission to ignore them. The new framing must
    // explicitly say to read them.
    expect(prompt).not.toMatch(/Uploaded files are auxiliary/);
    expect(prompt).toMatch(/read the file/i);
  });
});

describe("buildWorkPrompt workflow + policy management", () => {
  it("advertises workflow tools when supportsWorkflowTool is true", () => {
    const prompt = build("claude-agent", { supportsWorkflowTool: true });
    expect(prompt).toContain("mcp__neko_workflow_builder__list_workflows");
    expect(prompt).toContain("mcp__neko_workflow_builder__create_workflow");
    // Operators are not developers — should warn against showing cron syntax.
    expect(prompt).toMatch(/never show them cron syntax/i);
  });

  it("falls back to the workflow save fence when MCP tools unavailable", () => {
    const prompt = build("hermes", { supportsWorkflowTool: false });
    expect(prompt).toContain("neko_workflow_save");
    expect(prompt).not.toContain("mcp__neko_workflow_builder__");
  });

  it("advertises policy tools when supportsPolicyTool is true", () => {
    const prompt = build("claude-agent", { supportsPolicyTool: true });
    expect(prompt).toContain("mcp__neko_policy_builder__list_policies");
    expect(prompt).toContain("mcp__neko_policy_builder__save_policy");
  });

  it("falls back to the policy save fence when MCP tools unavailable", () => {
    const prompt = build("hermes", { supportsPolicyTool: false });
    expect(prompt).toContain("neko_policy_save");
    expect(prompt).not.toContain("mcp__neko_policy_builder__");
  });

  it("frames /work as the single chat surface for everything", () => {
    const prompt = build("claude-agent");
    expect(prompt).toMatch(/only chat surface/i);
  });
});
