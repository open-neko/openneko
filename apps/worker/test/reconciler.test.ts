import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
} from "vitest";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import {
  db,
  eq,
  pool,
  work_run,
  work_thread,
  workflow_definition,
  workflow_run,
} from "@neko/db";
import { reconcileStaleRuns } from "../src/reconciler";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[reconciler] skipping: Postgres unreachable.");
}

describeIfDb("reconcileStaleRuns — queued runs with a dead launcher", () => {
  const orgId = uniqueOrgId("reconq");

  beforeAll(async () => {
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
  });

  async function insertRun(args: { status: string; createdAt: Date }) {
    const [thread] = await db()
      .insert(work_thread)
      .values({ org_id: orgId, title: "t" })
      .returning({ id: work_thread.id });
    const [run] = await db()
      .insert(work_run)
      .values({
        org_id: orgId,
        thread_id: thread.id,
        status: args.status,
        backend: "ax",
        created_at: args.createdAt,
        updated_at: args.createdAt,
      })
      .returning({ id: work_run.id });
    return run.id;
  }

  it("cancels queued runs older than the launcher window, keeps fresh ones", async () => {
    const oldId = await insertRun({
      status: "queued",
      createdAt: new Date(Date.now() - 10 * 60_000),
    });
    const freshId = await insertRun({ status: "queued", createdAt: new Date() });

    await reconcileStaleRuns();

    const [oldRun] = await db()
      .select({ status: work_run.status, error: work_run.error })
      .from(work_run)
      .where(eq(work_run.id, oldId));
    expect(oldRun.status).toBe("cancelled");
    expect(oldRun.error).toMatch(/Never started/);

    const [freshRun] = await db()
      .select({ status: work_run.status })
      .from(work_run)
      .where(eq(work_run.id, freshId));
    expect(freshRun.status).toBe("queued");
  });
});

describeIfDb("reconcileStaleRuns", () => {
  let orgId: string;

  beforeEach(async () => {
    orgId = uniqueOrgId("stale-runs");
    await createTestOrg(orgId);
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
  });

  async function insertRunningRun(updatedAt: Date): Promise<{
    threadId: string;
    workRunId: string;
  }> {
    const [thread] = await db()
      .insert(work_thread)
      .values({ org_id: orgId, title: "t", channel: "workflow" })
      .returning({ id: work_thread.id });
    const [run] = await db()
      .insert(work_run)
      .values({
        org_id: orgId,
        thread_id: thread!.id,
        backend: "ax",
        status: "running",
        created_at: updatedAt,
        updated_at: updatedAt,
      })
      .returning({ id: work_run.id });
    return { threadId: thread!.id, workRunId: run!.id };
  }

  it("cancels a work_run + its workflow_run stranded in running past the age threshold", async () => {
    const stale = new Date(Date.now() - 20 * 60_000);
    const { threadId, workRunId } = await insertRunningRun(stale);
    const [wf] = await db()
      .insert(workflow_definition)
      .values({ org_id: orgId, name: "zombie wf" })
      .returning({ id: workflow_definition.id });
    const [wfRun] = await db()
      .insert(workflow_run)
      .values({
        org_id: orgId,
        workflow_id: wf!.id,
        thread_id: threadId,
        work_run_id: workRunId,
        trigger_kind: "subscription",
        status: "running",
      })
      .returning({ id: workflow_run.id });

    const res = await reconcileStaleRuns({ minAgeMs: 11 * 60_000 });
    expect(res.cancelled).toBeGreaterThanOrEqual(1);

    const wr = await db()
      .select({ status: work_run.status, error: work_run.error })
      .from(work_run)
      .where(eq(work_run.id, workRunId));
    expect(wr[0]?.status).toBe("cancelled");
    expect(wr[0]?.error).toContain("Interrupted");

    const wfr = await db()
      .select({ status: workflow_run.status })
      .from(workflow_run)
      .where(eq(workflow_run.id, wfRun!.id));
    expect(wfr[0]?.status).toBe("cancelled");
  });

  it("leaves a recently-updated running run alone (respects minAgeMs)", async () => {
    const { workRunId } = await insertRunningRun(new Date());
    await reconcileStaleRuns({ minAgeMs: 11 * 60_000 });
    const wr = await db()
      .select({ status: work_run.status })
      .from(work_run)
      .where(eq(work_run.id, workRunId));
    expect(wr[0]?.status).toBe("running");
  });
});

if (reachable) {
  afterAll(async () => {
    await pool().end();
  });
}
