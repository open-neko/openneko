import { afterAll, describe, expect, it } from "vitest";
import { pool } from "@neko/db";
import { withTestOrg, dbReachable } from "@neko/db/test-helpers";
import {
  createWorkflowRun,
  emitWorkflowOutput,
  finishWorkflowRun,
  getWorkflow,
  getWorkflowByOrgName,
  listCronWorkflows,
  listWorkflows,
  saveWorkflow,
} from "../../src/workflows/store";
import {
  createWorkRun,
  createWorkThread,
} from "../../src/work/store";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[workflow-store] skipping: metadata Postgres unreachable. Run `docker compose up -d` to enable.",
  );
}

describeIfDb("workflow store", () => {
  afterAll(async () => {
    await pool().end();
  });

  it("saveWorkflow inserts a new row when the name is unused", async () => {
    await withTestOrg(async (orgId) => {
      const result = await saveWorkflow({
        orgId,
        name: "Daily revenue dip checker",
        description: "Watches revenue and flags >12% drops.",
        systemPromptOverlay: "Show INR in lakhs.",
        steps: [
          { id: "s1", description: "Pull last 7 days of revenue by region" },
          { id: "s2", description: "Compare against prior 7 days" },
        ],
        triggers: { cron: "0 9 * * *", timezone: "Asia/Kolkata" },
      });
      expect(result.action).toBe("created");
      expect(result.workflow.name).toBe("Daily revenue dip checker");
      expect(result.workflow.cron).toBe("0 9 * * *");
      expect(result.workflow.cronTimezone).toBe("Asia/Kolkata");
      expect(result.workflow.steps).toHaveLength(2);

      const fetched = await getWorkflow(orgId, result.workflow.id);
      expect(fetched?.id).toBe(result.workflow.id);
    });
  });

  it("saveWorkflow upserts in place when the name is reused within an org", async () => {
    await withTestOrg(async (orgId) => {
      const first = await saveWorkflow({
        orgId,
        name: "Onboarding checker",
        description: "v1",
        steps: [{ id: "s1", description: "step a" }],
      });
      const second = await saveWorkflow({
        orgId,
        name: "Onboarding checker",
        description: "v2",
        steps: [{ id: "s1", description: "step b" }],
        triggers: { cron: "*/15 * * * *" },
      });
      expect(second.action).toBe("updated");
      expect(second.workflow.id).toBe(first.workflow.id);
      expect(second.workflow.description).toBe("v2");
      expect(second.workflow.steps[0].description).toBe("step b");
      expect(second.workflow.cron).toBe("*/15 * * * *");

      const refetched = await getWorkflowByOrgName(orgId, "Onboarding checker");
      expect(refetched?.id).toBe(first.workflow.id);
    });
  });

  it("listCronWorkflows surfaces only enabled + cron-enabled rows with a cron expression", async () => {
    await withTestOrg(async (orgId) => {
      await saveWorkflow({
        orgId,
        name: "wf-cron",
        steps: [{ id: "s1", description: "x" }],
        triggers: { cron: "0 9 * * *", enabled: true },
      });
      await saveWorkflow({
        orgId,
        name: "wf-cron-disabled",
        steps: [{ id: "s1", description: "x" }],
        triggers: { cron: "0 9 * * *", enabled: false },
      });
      await saveWorkflow({
        orgId,
        name: "wf-no-cron",
        steps: [{ id: "s1", description: "x" }],
      });

      const all = (await listCronWorkflows()).filter((w) => w.orgId === orgId);
      const names = all.map((w) => w.name).sort();
      expect(names).toEqual(["wf-cron"]);
    });
  });

  it("listWorkflows returns only the requesting org's rows", async () => {
    await withTestOrg(async (orgId) => {
      await saveWorkflow({
        orgId,
        name: "mine",
        steps: [{ id: "s1", description: "x" }],
      });
      const rows = await listWorkflows(orgId);
      const names = rows.map((w) => w.name);
      expect(names).toContain("mine");
    });
  });

  it("createWorkflowRun wraps an existing work_run and finishWorkflowRun closes it", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "runner test",
        steps: [{ id: "s1", description: "x" }],
      });
      const thread = await createWorkThread(orgId, workflow.name);
      const workRun = await createWorkRun(orgId, thread.id, "hermes");

      const wfRun = await createWorkflowRun({
        orgId,
        workflowId: workflow.id,
        threadId: thread.id,
        workRunId: workRun.id,
        triggerKind: "manual",
        triggerPayload: { source: "test" },
      });
      expect(wfRun.workRunId).toBe(workRun.id);
      expect(wfRun.status).toBe("running");
      expect(wfRun.chainDepth).toBe(0);

      await finishWorkflowRun({
        workflowRunId: wfRun.id,
        status: "completed",
        summary: "ok",
      });
    });
  });

  it("emitWorkflowOutput persists scope/topic/mood metadata", async () => {
    await withTestOrg(async (orgId) => {
      const { workflow } = await saveWorkflow({
        orgId,
        name: "output test",
        steps: [{ id: "s1", description: "x" }],
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

      const out = await emitWorkflowOutput({
        orgId,
        workflowRunId: wfRun.id,
        workRunId: workRun.id,
        kind: "finding",
        title: "APAC churn spike",
        body: "Churn rose 18% week-over-week.",
        scope: "apac_churn",
        topic: "partner_renewal",
        mood: "act",
        freshnessTtlSeconds: 7 * 24 * 3600,
      });
      expect(out.kind).toBe("finding");
      expect(out.scope).toBe("apac_churn");
      expect(out.topic).toBe("partner_renewal");
      expect(out.mood).toBe("act");
      expect(out.freshnessTtlSeconds).toBe(7 * 24 * 3600);
    });
  });
});
