import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import { addLocalGroupMember, app_user, createUserGroup, db, grantItem, pool, setLocalAdministrator } from "@neko/db";
import { callRoute } from "../_helpers/route";

const mocks = vi.hoisted(() => ({ orgId: "", role: "admin" as "admin" | "member" }));

vi.mock("@/lib/db", async () => ({ ...(await vi.importActual<typeof import("@/lib/db")>("@/lib/db")), getOrgId: async () => mocks.orgId }));
vi.mock("@/lib/admin-auth", async () => {
  const { NextResponse } = await import("next/server");
  return {
    requireAdminActor: async () => (mocks.role === "admin" ? { userId: null, role: "admin" } : NextResponse.json({ error: "admin only" }, { status: 403 })),
    isDenied: (value: unknown) => value instanceof Response,
  };
});

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

  it("reports effective access", async () => {
    const access = await import("@/app/api/v1/admin/users/[userId]/access/route");
    const groupId = (await createUserGroup(mocks.orgId, { name: "Finance" })).id;
    await addLocalGroupMember(mocks.orgId, groupId, `${mocks.orgId}-ann`);
    await grantItem(mocks.orgId, { groupId, itemType: "skill", itemId: "docx" });

    const annAccess = await callRoute((req) => access.GET(req, { params: Promise.resolve({ userId: `${mocks.orgId}-ann` }) }));
    expect(annAccess.body).toMatchObject({ administrator: false, groupSlugs: ["everyone", "finance"] });
  });
});
