import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTestOrg, dbReachable, deleteTestOrg, uniqueOrgId } from "@neko/db/test-helpers";
import { pool } from "@neko/db";
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

(reachable ? describe : describe.skip)("admin spend routes", () => {
  beforeEach(async () => {
    mocks.orgId = uniqueOrgId("admin-spend");
    mocks.role = "admin";
    await createTestOrg(mocks.orgId);
  });
  afterEach(async () => {
    await deleteTestOrg(mocks.orgId);
    vi.clearAllMocks();
  });
  afterAll(async () => {
    await pool().end();
  });

  it("reads and saves limits and a workflow override", async () => {
    const spend = await import("@/app/api/admin/spend/route");
    const override = await import("@/app/api/admin/spend/workflows/[workflowId]/route");
    const { rows } = await pool().query<{ id: string }>(
      "insert into workflow_definition (org_id, name) values ($1, 'Daily report') returning id",
      [mocks.orgId],
    );
    const workflowId = rows[0]!.id;

    const read = await callRoute(spend.GET);
    expect(read.status).toBe(200);
    expect(read.body).toMatchObject({ limits: { runCapUsd: 5, orgDailyUsd: 500 }, workflows: [{ workflowId, name: "Daily report" }] });

    const saved = await callRoute(spend.PUT, {
      method: "PUT",
      body: { runCapUsd: 3, orgHourlyUsd: 100, orgDailyUsd: 300, workflowHourlyUsd: 20, workflowDailyUsd: 60, warnPercent: 90 },
    });
    expect(saved.status).toBe(200);
    expect(saved.body).toMatchObject({ limits: { runCapUsd: 3, orgDailyUsd: 300, warnPercent: 90 } });

    const invalid = await callRoute(spend.PUT, {
      method: "PUT",
      body: { runCapUsd: 3, orgHourlyUsd: 100, orgDailyUsd: 9000, workflowHourlyUsd: 20, workflowDailyUsd: 60, warnPercent: 90 },
    });
    expect(invalid).toMatchObject({ status: 400, body: { error: "Organization daily budget cannot exceed $2000.00." } });

    const ctx = (id: string) => ({ params: Promise.resolve({ workflowId: id }) });
    const set = await callRoute((req) => override.PUT(req, ctx(workflowId)), { method: "PUT", body: { hourlyUsd: 10, dailyUsd: null } });
    expect(set.status).toBe(200);
    expect(set.body).toMatchObject({ workflows: [{ workflowId, hourlyOverrideUsd: 10, dailyOverrideUsd: null }] });

    const cleared = await callRoute((req) => override.DELETE(req, ctx(workflowId)), { method: "DELETE" });
    expect(cleared.body).toMatchObject({ workflows: [{ workflowId, hourlyOverrideUsd: null }] });

    const missing = await callRoute((req) => override.PUT(req, ctx("not-a-uuid")), { method: "PUT", body: { hourlyUsd: 1 } });
    expect(missing.status).toBe(404);
  });

  it("refuses non-administrators", async () => {
    mocks.role = "member";
    const spend = await import("@/app/api/admin/spend/route");
    expect((await callRoute(spend.GET)).status).toBe(403);
    expect((await callRoute(spend.PUT, { method: "PUT", body: {} })).status).toBe(403);
  });
});
