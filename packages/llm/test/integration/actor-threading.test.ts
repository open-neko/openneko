import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { app_user, db, eq, pool, work_run, work_thread } from "@neko/db";
import { createWorkRun, createWorkThread } from "../../src/work/store";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[actor-threading] skipping: Postgres unreachable.");
}

// K1 — every run carries the acting principal, snapshotted at run start.
describeIfDb("K1 actor threading", () => {
  const orgId = uniqueOrgId("actor");
  let userId: string;

  beforeAll(async () => {
    await createTestOrg(orgId);
    userId = `${orgId}-user`;
    await db().insert(app_user).values({
      id: userId,
      email: "ada@example.com",
      org_id: orgId,
      role: "member",
    });
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await pool().end();
  });

  it("stamps the web actor (user id + snapshotted role)", async () => {
    const thread = await createWorkThread(orgId, "t", "web", userId);
    expect(thread.created_by_user_id).toBe(userId);

    const run = await createWorkRun(orgId, thread.id, "hermes", {
      userId,
      role: "member",
    });
    const [row] = await db()
      .select({
        actorUserId: work_run.actor_user_id,
        actorRole: work_run.actor_role,
      })
      .from(work_run)
      .where(eq(work_run.id, run.id));
    expect(row).toEqual({ actorUserId: userId, actorRole: "member" });
  });

  it("channel + service runs carry role-only identities", async () => {
    const thread = await createWorkThread(orgId, "t2", "telegram");
    expect(thread.created_by_user_id).toBeNull();

    const channelRun = await createWorkRun(orgId, thread.id, "hermes", {
      userId: null,
      role: "member",
    });
    const serviceRun = await createWorkRun(orgId, thread.id, "hermes", {
      userId: null,
      role: "service",
    });
    const rows = await db()
      .select({
        id: work_run.id,
        actorUserId: work_run.actor_user_id,
        actorRole: work_run.actor_role,
      })
      .from(work_run)
      .where(eq(work_run.thread_id, thread.id));
    const byId = new Map(rows.map((r) => [r.id, r]));
    expect(byId.get(channelRun.id)).toMatchObject({
      actorUserId: null,
      actorRole: "member",
    });
    expect(byId.get(serviceRun.id)).toMatchObject({
      actorUserId: null,
      actorRole: "service",
    });
  });

  it("legacy callers without an actor still create runs (null actor)", async () => {
    const thread = await createWorkThread(orgId);
    const run = await createWorkRun(orgId, thread.id, "hermes");
    const [row] = await db()
      .select({ actorRole: work_run.actor_role })
      .from(work_run)
      .where(eq(work_run.id, run.id));
    expect(row?.actorRole).toBeNull();
  });
});
