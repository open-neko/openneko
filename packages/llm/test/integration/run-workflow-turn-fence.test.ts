import { afterAll, describe, expect, it } from "vitest";
import {
  action_request,
  and,
  db,
  eq,
  pool,
  workflow_output,
} from "@neko/db";
import { dbReachable, withTestOrg } from "@neko/db/test-helpers";
import type {
  AgentBackend,
  AgentEvent,
  AgentRunOptions,
  AgentRunResult,
} from "../../src/agent-backend";
import {
  createActionPolicy,
  prepareWorkflowRun,
  runWorkflowTurn,
  saveWorkflow,
} from "../../src/workflows";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[run-workflow-turn-fence] skipping: metadata Postgres unreachable.",
  );
}

function hermesShapedBackend(
  onRun: (opts: AgentRunOptions) => Promise<AgentRunResult>,
): AgentBackend {
  return {
    id: "hermes",
    capabilities: {
      mcpTools: false,
      sdkStopHook: false,
      sessionResume: false,
      canUseToolGate: false,
    },
    run: onRun,
  };
}

describeIfDb("runWorkflowTurn — fence-fallback path (Hermes-shape)", () => {
  afterAll(async () => {
    await pool().end();
  });

  it("persists a workflow_output when the agent emits a neko_workflow_output fence", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "fence output test",
        steps: [{ id: "s1", description: "emit observation" }],
      });

      const backend = hermesShapedBackend(async (opts) => {
        const text = [
          "APAC revenue dipped 14% WoW.",
          "",
          "```neko_workflow_output",
          JSON.stringify({
            kind: "observation",
            title: "APAC revenue dipped 14% WoW",
            body: "Revenue fell from 3.2L to 2.75L.",
            scope: "apac_revenue",
            mood: "watch",
          }),
          "```",
        ].join("\n");
        await opts.onEvent?.({ type: "message", role: "assistant", content: text });
        return { status: "completed", finalText: text };
      });

      const events: AgentEvent[] = [];
      const prepared = await prepareWorkflowRun(
        { orgId, workflowId: workflow.id, triggerKind: "manual" },
        { resolveAgentBackend: async () => backend },
      );
      const result = await runWorkflowTurn(
        {
          prepared,
          mode: "headless",
          emit: async (e) => {
            events.push(e);
          },
        },
        {
          resolveAgentBackend: async () => backend,
          formatGlobalMemoryPromptContext: async () => "",
        },
      );

      expect(result.status).toBe("completed");
      // Fence is stripped from the persisted prose.
      expect(result.finalText).toContain("APAC revenue dipped 14% WoW.");
      expect(result.finalText).not.toContain("```neko_workflow_output");

      const rows = await db()
        .select()
        .from(workflow_output)
        .where(eq(workflow_output.workflow_run_id, prepared.workflowRun.id));
      expect(rows).toHaveLength(1);
      expect(rows[0]?.kind).toBe("observation");
      expect(rows[0]?.scope).toBe("apac_revenue");
      expect(rows[0]?.mood).toBe("watch");

      expect(events.some((e) => e.type === "output_emit")).toBe(true);
    });
  });

  it("creates an action_request via policy when the agent emits a neko_action_request fence", async () => {
    await withTestOrg(async (orgId) => {
      await createActionPolicy({
        orgId,
        name: "external_test",
        description: "",
        appliesToKinds: [],
        appliesToScopes: ["external"],
        mode: "approval_required",
        riskThresholdAutoApprove: null,
        allowedTargets: null,
        deniedTargets: null,
        limits: {},
        approverRole: null,
        priority: 100,
        enabled: true,
      });

      const { workflow } = await saveWorkflow({
        orgId,
        name: "fence action test",
        steps: [{ id: "s1", description: "propose action" }],
      });

      const backend = hermesShapedBackend(async (opts) => {
        const text = [
          "Proposing one action.",
          "",
          "```neko_action_request",
          JSON.stringify({
            scope: "external",
            kind: "send_message",
            target: "slack:#growth",
            payload: { text: "APAC dip alert" },
            risk_level: "low",
            summary: "Post APAC dip alert to #growth.",
          }),
          "```",
        ].join("\n");
        await opts.onEvent?.({ type: "message", role: "assistant", content: text });
        return { status: "completed", finalText: text };
      });

      const events: AgentEvent[] = [];
      const prepared = await prepareWorkflowRun(
        { orgId, workflowId: workflow.id, triggerKind: "manual" },
        { resolveAgentBackend: async () => backend },
      );
      const result = await runWorkflowTurn(
        {
          prepared,
          mode: "headless",
          emit: async (e) => {
            events.push(e);
          },
        },
        {
          resolveAgentBackend: async () => backend,
          formatGlobalMemoryPromptContext: async () => "",
        },
      );

      expect(result.status).toBe("completed");
      expect(result.finalText).toContain("Proposing one action.");
      expect(result.finalText).not.toContain("```neko_action_request");

      const rows = await db()
        .select()
        .from(action_request)
        .where(
          and(
            eq(action_request.org_id, orgId),
            eq(action_request.workflow_run_id, prepared.workflowRun.id),
          ),
        );
      expect(rows).toHaveLength(1);
      expect(rows[0]?.kind).toBe("send_message");
      expect(rows[0]?.status).toBe("pending_approval");
      expect(rows[0]?.summary).toContain("APAC");

      expect(events.some((e) => e.type === "action_request_emit")).toBe(true);
    });
  });

  it("emits an error event but still completes when a fence body is invalid JSON", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "fence error test",
        steps: [{ id: "s1", description: "try" }],
      });

      const backend = hermesShapedBackend(async (opts) => {
        const text = [
          "Here's my output.",
          "```neko_workflow_output",
          "{ not valid",
          "```",
        ].join("\n");
        await opts.onEvent?.({ type: "message", role: "assistant", content: text });
        return { status: "completed", finalText: text };
      });

      const events: AgentEvent[] = [];
      const prepared = await prepareWorkflowRun(
        { orgId, workflowId: workflow.id, triggerKind: "manual" },
        { resolveAgentBackend: async () => backend },
      );
      const result = await runWorkflowTurn(
        {
          prepared,
          mode: "headless",
          emit: async (e) => {
            events.push(e);
          },
        },
        {
          resolveAgentBackend: async () => backend,
          formatGlobalMemoryPromptContext: async () => "",
        },
      );

      expect(result.status).toBe("completed");
      expect(events.some((e) => e.type === "error")).toBe(true);

      const rows = await db()
        .select()
        .from(workflow_output)
        .where(eq(workflow_output.workflow_run_id, prepared.workflowRun.id));
      expect(rows).toHaveLength(0);
    });
  });
});
