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

function build(backend: "claude-agent" | "hermes"): string {
  return buildWorkPrompt({
    backend,
    workspace,
    knowledge,
    messages: [],
    currentUserMessage: "test",
    supportsCardTool: false,
    supportsSkillTool: false,
    supportsMemoryTool: false,
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
