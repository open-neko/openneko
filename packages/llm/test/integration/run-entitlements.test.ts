import { describe, expect, it } from "vitest";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import {
  addLocalGroupMember,
  app_user,
  builtinGroupId,
  createUserGroup,
  db,
  grantItem,
  revokeItem,
} from "@neko/db";
import { inProcessControlPlane } from "../../src/work/control-plane";
import { entitlementActorForRun, runHeldItemIds } from "../../src/work/entitlement-scope";
import { createWorkRun, createWorkThread } from "../../src/work/store";
import { saveWorkflow } from "../../src/workflows/store";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

async function withOrg(fn: (orgId: string) => Promise<void>) {
  const orgId = uniqueOrgId("run-ent");
  await createTestOrg(orgId);
  try {
    await fn(orgId);
  } finally {
    await deleteTestOrg(orgId);
  }
}

describeIfDb("run entitlements", () => {
  it("limits workflow tools and skills to what the run's user holds", async () => {
    await withOrg(async (orgId) => {
      const everyone = await builtinGroupId(orgId, "everyone");
      await revokeItem(orgId, { groupId: everyone, itemType: "workflow", itemId: "*" });
      await revokeItem(orgId, { groupId: everyone, itemType: "skill", itemId: "*" });
      const finance = await createUserGroup(orgId, { name: "Finance" });
      await db().insert(app_user).values([
        { id: `${orgId}-ann`, org_id: orgId, role: "member", email: "ann@example.test" },
        { id: `${orgId}-boss`, org_id: orgId, role: "admin", email: "boss@example.test" },
      ]);
      await addLocalGroupMember(orgId, finance.id, `${orgId}-ann`);

      const revenue = (await saveWorkflow({ orgId, name: "Daily revenue check", steps: [] })).workflow;
      const promos = (await saveWorkflow({ orgId, name: "Promotions", steps: [] })).workflow;
      const personal = (await saveWorkflow({ orgId, name: "Mine", steps: [], ownerUserId: `${orgId}-ann` })).workflow;
      await grantItem(orgId, { groupId: finance.id, itemType: "workflow", itemId: revenue.id });
      await grantItem(orgId, { groupId: finance.id, itemType: "skill", itemId: "docx" });

      const thread = await createWorkThread(orgId, "t");
      const annRun = await createWorkRun(orgId, thread.id, "hermes", { userId: `${orgId}-ann`, role: "member" });
      const bossRun = await createWorkRun(orgId, thread.id, "hermes", { userId: `${orgId}-boss`, role: "admin" });

      const annList = await inProcessControlPlane.listWorkflowsWithTriggers({ orgId, runId: annRun.id });
      expect(annList.workflows.map((w) => w.name).sort()).toEqual(["Daily revenue check", "Mine"]);
      expect((await inProcessControlPlane.listWorkflowsWithTriggers({ orgId, runId: bossRun.id })).total).toBe(3);

      expect(await inProcessControlPlane.deleteWorkflow({ orgId, workflowId: promos.id, runId: annRun.id })).toEqual({ found: false, name: null });
      await expect(
        inProcessControlPlane.saveWorkflowWithTrigger({ orgId, name: "Promotions", steps: [], createdByRunId: annRun.id }),
      ).rejects.toThrow("cannot change it");
      expect((await inProcessControlPlane.deleteWorkflow({ orgId, workflowId: personal.id, runId: annRun.id })).found).toBe(true);

      const annActor = await entitlementActorForRun(orgId, annRun.id);
      expect(await runHeldItemIds(annActor!, "skill")).toEqual(["docx"]);
      expect(await runHeldItemIds((await entitlementActorForRun(orgId, bossRun.id))!, "skill")).toBeUndefined();
      expect(await entitlementActorForRun(orgId, "00000000-0000-0000-0000-000000000000")).toBeNull();
    });
  });
});
