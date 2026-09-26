import { describe, expect, it } from "vitest";
import type { AgentBackend, AgentRunOptions, AgentWorkspace } from "../src/agent-backend";
import type { AgentControlPlane } from "../src/work/control-plane";
import { runWorkflowAgentBackend } from "../src/workflows/agent-core";

const workspace: AgentWorkspace = {
  orgRoot: "/tmp/org",
  skillsRoot: "/tmp/org/skills",
  memoryRoot: "/tmp/org/memory",
  knowledgeRoot: "/tmp/org/knowledge",
  uploadsRoot: "/tmp/org/uploads",
  runsRoot: "/tmp/org/runs",
  threadUploadsRoot: "/tmp/org/uploads/thread-1",
  runRoot: "/tmp/org/runs/run-1",
  artifactRoot: "/tmp/org/runs/run-1/artifacts",
  binRoot: "/tmp/org/runs/run-1/bin",
};

// This test only inspects the MCP server wiring; no tool handler is invoked.
const controlPlane = {} as AgentControlPlane;

describe("runWorkflowAgentBackend", () => {
  it("threads workflow MCP bridge env needed by the in-box bridge", async () => {
    let captured: AgentRunOptions | undefined;
    const backend: AgentBackend = {
      id: "hermes",
      capabilities: {
        mcpTools: true,
        sessionResume: false,
        nativeDelegation: "hermes-delegate-task",
      },
      async run(opts) {
        captured = opts;
        return { status: "completed", finalText: "done" };
      },
    };

    await runWorkflowAgentBackend({
      backend,
      prompt: "prompt",
      userMessage: "begin",
      orgId: "org-1",
      threadId: "thread-1",
      runId: "run-1",
      workflowRunId: "workflow-run-1",
      mode: "headless",
      triggeredByObservationId: "obs-1",
      workspace,
      controlPlane,
      emit: async () => {},
      timeoutMs: 1_800_000,
    });

    expect(captured?.mcpBridgeEnv).toMatchObject({
      OPENNEKO_MCP_MODE: "workflow",
      OPENNEKO_MCP_ORG_ID: "org-1",
      OPENNEKO_MCP_THREAD_ID: "thread-1",
      OPENNEKO_MCP_RUN_ID: "run-1",
      OPENNEKO_MCP_SKILLS_ROOT: "/tmp/org/skills",
      OPENNEKO_MCP_WORKFLOW_RUN_ID: "workflow-run-1",
      OPENNEKO_MCP_TRIGGERED_BY_OBSERVATION_ID: "obs-1",
    });
    expect(captured?.timeoutMs).toBe(1_800_000);
    expect(captured?.mcpServers).toEqual(
      expect.objectContaining({
        neko_action: expect.anything(),
        neko_graphjin: expect.anything(),
        neko_memory: expect.anything(),
        neko_workflow_output: expect.anything(),
      }),
    );
  });
});
