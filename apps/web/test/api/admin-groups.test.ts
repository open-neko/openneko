import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import { addLocalGroupMember, app_user, createUserGroup, db, pool, setLocalAdministrator } from "@neko/db";
import { callRoute } from "../_helpers/route";

const mocks = vi.hoisted(() => ({ orgId: "", role: "admin" as "admin" | "member", worker: vi.fn() }));

vi.mock("@/lib/db", async () => ({ ...(await vi.importActual<typeof import("@/lib/db")>("@/lib/db")), getOrgId: async () => mocks.orgId }));
vi.mock("@/lib/admin-auth", async () => {
  const { NextResponse } = await import("next/server");
  return {
    requireAdminActor: async () => (mocks.role === "admin" ? { userId: null, role: "admin" } : NextResponse.json({ error: "admin only" }, { status: 403 })),
    isDenied: (value: unknown) => value instanceof Response,
  };
});
vi.mock("@/lib/groups-admin", async () => ({
  ...(await vi.importActual<typeof import("@/lib/groups-admin")>("@/lib/groups-admin")),
  scheduleGroupGrantsApply: mocks.worker,
}));

const reachable = await dbReachable();

(reachable ? describe : describe.skip)("admin group routes", () => {
  beforeEach(async () => {
    mocks.orgId = uniqueOrgId("admin-groups");
    mocks.role = "admin";
    await createTestOrg(mocks.orgId);
    await db().insert(app_user).values([
      { id: `${mocks.orgId}-owner`, org_id: mocks.orgId, email: "owner@example.test" },
      { id: `${mocks.orgId}-ann`, org_id: mocks.orgId, email: "ann@example.test" },
    ]);
    await setLocalAdministrator(mocks.orgId, `${mocks.orgId}-owner`, true);
  });
  afterEach(async () => {
    await deleteTestOrg(mocks.orgId);
    vi.clearAllMocks();
  });
  afterAll(async () => {
    await pool().end();
  });

  it("manages grants and reports effective access", async () => {
    const grants = await import("@/app/api/v1/admin/item-grants/route");
    const access = await import("@/app/api/v1/admin/users/[userId]/access/route");
    const groupId = (await createUserGroup(mocks.orgId, { name: "Finance" })).id;
    await addLocalGroupMember(mocks.orgId, groupId, `${mocks.orgId}-ann`);

    expect((await callRoute(grants.POST, { method: "POST", body: { groupId, itemType: "skill", itemId: "docx" } })).body).toEqual({ created: true });
    expect((await callRoute(grants.POST, { method: "POST", body: { groupId, itemType: "data_source", itemId: "shop" } })).status).toBe(200);
    expect(mocks.worker).toHaveBeenCalledTimes(1);

    const holders = await callRoute(grants.GET, { query: { itemType: "skill", itemId: "docx" } });
    expect((holders.body as { holders: Array<{ name: string }> }).holders.map((h) => h.name)).toEqual(["Administrators", "Everyone", "Finance"]);

    const annAccess = await callRoute((req) => access.GET(req, { params: Promise.resolve({ userId: `${mocks.orgId}-ann` }) }));
    expect(annAccess.body).toMatchObject({ administrator: false, groupSlugs: ["everyone", "finance"] });

    expect((await callRoute(grants.DELETE, { method: "DELETE", body: { groupId, itemType: "skill", itemId: "docx" } })).body).toEqual({ removed: true });
  });

  it("validates data access rules and row filters", async () => {
    const rules = await import("@/app/api/v1/admin/data-access/route");
    const ruleRoute = await import("@/app/api/v1/admin/data-access/[ruleId]/route");
    const groupId = (await createUserGroup(mocks.orgId, { name: "Sales" })).id;

    const bad = await callRoute(rules.POST, { method: "POST", body: { groupId, source: "shop", tableName: "orders", columns: ["id"], rowFilter: { column: "id", op: "like", value: 1 } } });
    expect(bad.status).toBe(400);
    expect((bad.body as { error: string }).error).toContain("op must be one of");

    const saved = await callRoute(rules.POST, { method: "POST", body: { groupId, source: "shop", tableName: "orders", columns: ["id", "amount"], rowFilter: { column: "region", op: "eq", value: "emea" } } });
    expect(saved.status).toBe(201);
    const ruleId = (saved.body as { rule: { id: string } }).rule.id;
    expect((await callRoute(rules.GET, { query: { groupId } })).body).toMatchObject({ enabled: false, rules: [{ tableName: "orders", columns: ["id", "amount"] }] });
    expect((await callRoute((req) => ruleRoute.DELETE(req, { params: Promise.resolve({ ruleId }) }), { method: "DELETE" })).status).toBe(200);
    expect(mocks.worker).toHaveBeenCalledTimes(2);
  });
});
