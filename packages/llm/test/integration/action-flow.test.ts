import { afterAll, describe, expect, it, vi } from "vitest";
import { and, db, eq, pool } from "@neko/db";
import { action_request } from "@neko/db";
import { dbReachable, withTestOrg } from "@neko/db/test-helpers";
import {
  approveActionRequest,
  createActionPolicy,
  createActionRequest,
  executeApprovedActionRequest,
  InvalidActionStatusTransitionError,
  rejectActionRequest,
  registerActionAdapter,
  saveWorkflow,
  createWorkflowRun,
} from "../../src/workflows";
import { createWorkRun, createWorkThread } from "../../src/work/store";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[action-flow] skipping: Postgres unreachable.");
}

async function setupWorkflowRun(orgId: string) {
  const { workflow } = await saveWorkflow({
    orgId,
    name: `wf-${Math.random().toString(36).slice(2, 7)}`,
    steps: [{ id: "s1", description: "act" }],
  });
  const thread = await createWorkThread(orgId, workflow.name);
  const workRun = await createWorkRun(orgId, thread.id, "hermes");
  const wfRun = await createWorkflowRun({
    orgId,
    workflowId: workflow.id,
    threadId: thread.id,
    workRunId: workRun.id,
    triggerKind: "manual",
  });
  return { workflowId: workflow.id, workflowRunId: wfRun.id, threadId: thread.id, workRunId: workRun.id };
}

describeIfDb("action stack — approve → execute → executed", () => {
  afterAll(async () => {
    await pool().end();
  });

  it("approve transitions pending_approval → approved → executed via mock adapter", async () => {
    await withTestOrg(async (orgId) => {
      const { workflowRunId } = await setupWorkflowRun(orgId);
      const policy = await createActionPolicy({
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
      const request = await createActionRequest({
        orgId,
        workflowRunId,
        policyId: policy.id,
        scope: "external",
        kind: "send_message",
        target: "ops-channel",
        payload: { text: "hello world" },
        riskLevel: "medium",
        status: "pending_approval",
        summary: "Notify ops",
      });
      expect(request.status).toBe("pending_approval");

      const approved = await approveActionRequest({
        id: request.id,
        orgId,
        approverUserId: null,
      });
      expect(approved.status).toBe("approved");

      const result = await executeApprovedActionRequest(orgId, request.id);
      expect(result.ok).toBe(true);

      const rows = await db()
        .select()
        .from(action_request)
        .where(and(eq(action_request.org_id, orgId), eq(action_request.id, request.id)));
      expect(rows[0]?.status).toBe("executed");
    });
  });

  it("reject leaves status=rejected and execute throws", async () => {
    await withTestOrg(async (orgId) => {
      const { workflowRunId } = await setupWorkflowRun(orgId);
      const request = await createActionRequest({
        orgId,
        workflowRunId,
        scope: "external",
        kind: "send_message",
        payload: {},
        riskLevel: "low",
        status: "pending_approval",
        summary: "test",
      });
      const rejected = await rejectActionRequest({
        id: request.id,
        orgId,
        approverUserId: null,
        reason: "not now",
      });
      expect(rejected.status).toBe("rejected");
      expect(rejected.rejectionReason).toBe("not now");

      await expect(
        executeApprovedActionRequest(orgId, request.id),
      ).rejects.toThrow(/expected approved/);
    });
  });

  it("rejects approval of an already-approved request", async () => {
    await withTestOrg(async (orgId) => {
      const { workflowRunId } = await setupWorkflowRun(orgId);
      const r = await createActionRequest({
        orgId,
        workflowRunId,
        scope: "external",
        kind: "send_message",
        payload: {},
        status: "pending_approval",
        summary: "x",
      });
      await approveActionRequest({ id: r.id, orgId, approverUserId: null });
      await expect(
        approveActionRequest({ id: r.id, orgId, approverUserId: null }),
      ).rejects.toBeInstanceOf(InvalidActionStatusTransitionError);
    });
  });

  it("kind-specific adapter overrides the default mock", async () => {
    await withTestOrg(async (orgId) => {
      const adapter = vi.fn().mockResolvedValue({
        commandOrOperation: "real:slack.postMessage",
        externalRef: "slack-1234",
        result: { ts: "1234.5678" },
      });
      registerActionAdapter("test_send_message", adapter);
      try {
        const { workflowRunId } = await setupWorkflowRun(orgId);
        const r = await createActionRequest({
          orgId,
          workflowRunId,
          scope: "external",
          kind: "test_send_message",
          payload: { text: "hi" },
          status: "approved",
          summary: "x",
        });
        const result = await executeApprovedActionRequest(orgId, r.id);
        expect(result.ok).toBe(true);
        expect(adapter).toHaveBeenCalledTimes(1);
        const rows = await db()
          .select()
          .from(action_request)
          .where(eq(action_request.id, r.id));
        expect(rows[0]?.status).toBe("executed");
      } finally {
        // Clean up the test adapter so other tests aren't affected.
        registerActionAdapter("test_send_message", async () => ({
          result: { mocked: true },
        }));
      }
    });
  });
});
