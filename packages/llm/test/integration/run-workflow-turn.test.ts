import { afterAll, describe, expect, it } from "vitest";
import { db, eq, pool, work_thread, workflow_output, workflow_run } from "@neko/db";
import { withTestOrg, dbReachable } from "@neko/db/test-helpers";
import { createWorkThread, listWorkThreads } from "../../src/work/store";
import type {
  AgentBackend,
  AgentEvent,
  AgentRunOptions,
  AgentRunResult,
} from "../../src/agent-backend";
import {
  prepareWorkflowRun,
  runWorkflowTurn,
  saveWorkflow,
} from "../../src/workflows";
import { emitWorkflowOutput } from "../../src/workflows/store";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[run-workflow-turn] skipping: metadata Postgres unreachable.",
  );
}

function fakeBackend(
  onRun: (opts: AgentRunOptions) => Promise<AgentRunResult>,
): AgentBackend {
  return {
    id: "hermes",
    capabilities: {
      mcpTools: true,
      sdkStopHook: false,
      sessionResume: false,
      canUseToolGate: true,
    },
    run: onRun,
  };
}

describeIfDb("runWorkflowTurn", () => {
  afterAll(async () => {
    await pool().end();
  });

  it("completes a workflow_run end-to-end, persists summary, and emits message events", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "smoke test workflow",
        steps: [{ id: "s1", description: "do nothing" }],
      });

      const events: AgentEvent[] = [];
      const emit = async (ev: AgentEvent) => {
        events.push(ev);
      };

      const backend = fakeBackend(async (opts) => {
        await opts.onEvent?.({
          type: "message",
          role: "assistant",
          content: "All clear.",
        });
        return {
          status: "completed",
          finalText: "All clear.",
        };
      });

      const prepared = await prepareWorkflowRun(
        {
          orgId,
          workflowId: workflow.id,
          triggerKind: "manual",
        },
        { resolveAgentBackend: async () => backend },
      );

      const result = await runWorkflowTurn(
        {
          prepared,
          mode: "live",
          emit,
        },
        {
          resolveAgentBackend: async () => backend,
          formatGlobalMemoryPromptContext: async () => "",
        },
      );

      expect(result.status).toBe("completed");
      expect(result.finalText).toContain("All clear");

      const rows = await db()
        .select()
        .from(workflow_run)
        .where(eq(workflow_run.id, prepared.workflowRun.id));
      expect(rows[0]?.status).toBe("completed");
      expect(rows[0]?.summary).toContain("All clear");

      const eventTypes = events.map((e) => e.type);
      expect(eventTypes).toContain("status");
      expect(eventTypes).toContain("message");
      expect(eventTypes).toContain("done");
    });
  });

  it("marks the run failed and surfaces the error when the backend throws", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "fail test workflow",
        steps: [{ id: "s1", description: "fail" }],
      });

      const backend = fakeBackend(async () => {
        throw new Error("upstream provider down");
      });

      const prepared = await prepareWorkflowRun(
        {
          orgId,
          workflowId: workflow.id,
          triggerKind: "manual",
        },
        { resolveAgentBackend: async () => backend },
      );

      await expect(
        runWorkflowTurn(
          {
            prepared,
            mode: "live",
            emit: async () => {},
          },
          {
            resolveAgentBackend: async () => backend,
            formatGlobalMemoryPromptContext: async () => "",
          },
        ),
      ).rejects.toThrow(/upstream provider down/);

      const rows = await db()
        .select()
        .from(workflow_run)
        .where(eq(workflow_run.id, prepared.workflowRun.id));
      expect(rows[0]?.status).toBe("failed");
      expect(rows[0]?.error).toContain("upstream provider down");
    });
  });

  it("persists a workflow_output row when emitWorkflowOutput is called inside the turn", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "output test workflow",
        steps: [{ id: "s1", description: "emit a finding" }],
      });

      const prepared = await prepareWorkflowRun(
        {
          orgId,
          workflowId: workflow.id,
          triggerKind: "manual",
        },
        {
          resolveAgentBackend: async () =>
            fakeBackend(async () => ({ status: "completed", finalText: "" })),
        },
      );

      // Simulate the runner emitting an output via the MCP tool — the
      // mechanism the tool exposes is just emitWorkflowOutput.
      const out = await emitWorkflowOutput({
        orgId,
        workflowRunId: prepared.workflowRun.id,
        workRunId: prepared.workRunId,
        kind: "finding",
        title: "fake finding",
        scope: "test_scope",
        mood: "watch",
      });

      const rows = await db()
        .select()
        .from(workflow_output)
        .where(eq(workflow_output.id, out.id));
      expect(rows[0]?.kind).toBe("finding");
      expect(rows[0]?.scope).toBe("test_scope");
      expect(rows[0]?.mood).toBe("watch");
    });
  });

  // A trigger run reuses the work_thread plumbing for its transcript, but it is
  // not a human chat — its thread must never appear in the web Ask sidebar.
  it("creates its thread on the \"workflow\" channel, never \"web\"", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "low stock slack alert",
        steps: [{ id: "s1", description: "alert on slack" }],
      });

      const prepared = await prepareWorkflowRun(
        { orgId, workflowId: workflow.id, triggerKind: "subscription" },
        {
          resolveAgentBackend: async () =>
            fakeBackend(async () => ({ status: "completed", finalText: "" })),
        },
      );

      const [thread] = await db()
        .select()
        .from(work_thread)
        .where(eq(work_thread.id, prepared.threadId));
      expect(thread?.channel).toBe("workflow");
    });
  });

  it("keeps an orphaned trigger thread (no workflow_run) out of the Ask sidebar", async () => {
    await withTestOrg(async (orgId) => {
      // A genuine human Ask thread, plus a trigger thread whose workflow_run
      // never persisted (a run-row insert that failed mid-trigger) — the leak
      // the web sidebar's NOT EXISTS(workflow_run) filter cannot catch alone.
      const ask = await createWorkThread(orgId, "what were our top products?", "web");
      const orphan = await createWorkThread(orgId, "low stock slack alert", "workflow");

      const webIds = (await listWorkThreads(orgId, "web")).map((t) => t.id);
      expect(webIds).toContain(ask.id);
      expect(webIds).not.toContain(orphan.id);
    });
  });
});
