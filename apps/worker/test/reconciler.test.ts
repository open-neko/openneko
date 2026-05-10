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
  and,
  db,
  eq,
  metric,
  pool,
  processing_job,
  sql,
} from "@neko/db";
import { reconcileStaleProcessingJobs } from "../src/reconciler";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[reconciler] skipping: Postgres unreachable.");
}

async function ensurePgbossSchema(): Promise<boolean> {
  try {
    await db().execute(sql`SELECT 1 FROM pgboss.job LIMIT 1`);
    return true;
  } catch {
    return false;
  }
}

async function insertPgbossJob(args: {
  name: string;
  processingJobId: string;
  state: "completed" | "failed" | "cancelled" | "active" | "created" | "retry";
  outputMessage?: string;
}): Promise<void> {
  const output = args.outputMessage
    ? sql`${JSON.stringify({ value: { message: args.outputMessage } })}::jsonb`
    : sql`NULL`;
  await db().execute(sql`
    INSERT INTO pgboss.job (name, data, state, output)
    VALUES (
      ${args.name},
      ${JSON.stringify({ processingJobId: args.processingJobId, orgId: "x" })}::jsonb,
      ${args.state}::pgboss.job_state,
      ${output}
    )
  `);
}

async function insertProcessingJob(args: {
  orgId: string;
  kind: string;
  status: "queued" | "running";
}): Promise<string> {
  const ins = await db()
    .insert(processing_job)
    .values({
      org_id: args.orgId,
      kind: args.kind,
      status: args.status,
      trigger: "test",
      ...(args.status === "running" ? { started_at: new Date() } : {}),
    })
    .returning({ id: processing_job.id });
  return ins[0]!.id;
}

async function readProcessingJob(id: string) {
  const rows = await db()
    .select({
      status: processing_job.status,
      error: processing_job.error,
      finished_at: processing_job.finished_at,
    })
    .from(processing_job)
    .where(eq(processing_job.id, id))
    .limit(1);
  return rows[0]!;
}

const pgbossOk = reachable ? await ensurePgbossSchema() : false;
const describeIfReady = pgbossOk ? describeIfDb : describe.skip;

if (reachable && !pgbossOk) {
  console.warn("[reconciler] skipping: pgboss schema not initialized.");
}

describeIfReady("reconcileStaleProcessingJobs", () => {
  let orgId: string;

  beforeEach(async () => {
    orgId = uniqueOrgId("reconciler");
    await createTestOrg(orgId);
  });

  afterEach(async () => {
    await db().execute(sql`
      DELETE FROM pgboss.job WHERE data->>'processingJobId' IN (
        SELECT id::text FROM processing_job WHERE org_id = ${orgId}
      )
    `);
    await deleteTestOrg(orgId);
  });

  it("mirrors pg-boss completed state to processing_job=succeeded", async () => {
    const jobId = await insertProcessingJob({
      orgId,
      kind: "business_profile_build",
      status: "running",
    });
    await insertPgbossJob({
      name: "business_profile_build",
      processingJobId: jobId,
      state: "completed",
    });

    const summary = await reconcileStaleProcessingJobs();
    expect(summary.succeeded).toBeGreaterThanOrEqual(1);

    const row = await readProcessingJob(jobId);
    expect(row.status).toBe("succeeded");
    expect(row.error).toBeNull();
    expect(row.finished_at).not.toBeNull();
  });

  it("mirrors pg-boss failed state and propagates the error message", async () => {
    const jobId = await insertProcessingJob({
      orgId,
      kind: "business_profile_build",
      status: "running",
    });
    await insertPgbossJob({
      name: "business_profile_build",
      processingJobId: jobId,
      state: "failed",
      outputMessage: "agent crashed: ENOENT",
    });

    await reconcileStaleProcessingJobs();

    const row = await readProcessingJob(jobId);
    expect(row.status).toBe("failed");
    expect(row.error).toBe("agent crashed: ENOENT");
  });

  it("treats pg-boss timeout (failed state with timeout message) as failed", async () => {
    const jobId = await insertProcessingJob({
      orgId,
      kind: "business_profile_build",
      status: "running",
    });
    await insertPgbossJob({
      name: "business_profile_build",
      processingJobId: jobId,
      state: "failed",
      outputMessage: "job failed by timeout in active state",
    });

    await reconcileStaleProcessingJobs();
    const row = await readProcessingJob(jobId);
    expect(row.status).toBe("failed");
    expect(row.error).toContain("timeout");
  });

  it("propagates metric_refresh failure to metric.last_refresh_status", async () => {
    const metricInsert = await db()
      .insert(metric)
      .values({
        org_id: orgId,
        role: "CEO",
        slug: "rec-test",
        source: "bootstrap",
        title: "Reconciler test",
        why: "Validate the reconciler propagates failure to metric",
        chart_hint: "kpi",
        active: true,
        last_refresh_status: "pending",
      })
      .returning({ id: metric.id });
    const metricId = metricInsert[0]!.id;

    const jobId = await insertProcessingJob({
      orgId,
      kind: "metric_refresh",
      status: "queued",
    });
    await db()
      .update(metric)
      .set({ last_refresh_job_id: jobId })
      .where(eq(metric.id, metricId));
    await insertPgbossJob({
      name: "metric_refresh",
      processingJobId: jobId,
      state: "failed",
      outputMessage: "upstream provider unavailable",
    });

    await reconcileStaleProcessingJobs();

    const m = await db()
      .select({
        status: metric.last_refresh_status,
        err: metric.last_refresh_error,
      })
      .from(metric)
      .where(eq(metric.id, metricId))
      .limit(1);
    expect(m[0]?.status).toBe("failed");
    expect(m[0]?.err).toBe("upstream provider unavailable");
  });

  it("resets running -> queued when pg-boss is still active (handler crashed mid-flight)", async () => {
    const jobId = await insertProcessingJob({
      orgId,
      kind: "business_profile_build",
      status: "running",
    });
    await insertPgbossJob({
      name: "business_profile_build",
      processingJobId: jobId,
      state: "active",
    });

    const summary = await reconcileStaleProcessingJobs();
    expect(summary.requeued).toBeGreaterThanOrEqual(1);

    const row = await readProcessingJob(jobId);
    expect(row.status).toBe("queued");
    expect(row.error).toBeNull();
  });

  it("leaves a queued row alone when pg-boss is still active", async () => {
    const jobId = await insertProcessingJob({
      orgId,
      kind: "business_profile_build",
      status: "queued",
    });
    await insertPgbossJob({
      name: "business_profile_build",
      processingJobId: jobId,
      state: "created",
    });

    await reconcileStaleProcessingJobs();
    const row = await readProcessingJob(jobId);
    expect(row.status).toBe("queued");
  });

  it("marks orphaned (no pg-boss row) processing_job as failed", async () => {
    const jobId = await insertProcessingJob({
      orgId,
      kind: "business_profile_build",
      status: "queued",
    });

    const summary = await reconcileStaleProcessingJobs();
    expect(summary.lost).toBeGreaterThanOrEqual(1);

    const row = await readProcessingJob(jobId);
    expect(row.status).toBe("failed");
    expect(row.error).toContain("lost from queue");
  });

  it("respects minAgeMs — leaves recent rows untouched", async () => {
    const jobId = await insertProcessingJob({
      orgId,
      kind: "business_profile_build",
      status: "running",
    });
    await insertPgbossJob({
      name: "business_profile_build",
      processingJobId: jobId,
      state: "failed",
      outputMessage: "permanent",
    });

    const summary = await reconcileStaleProcessingJobs({ minAgeMs: 10_000 });
    expect(summary.failed).toBe(0);

    const row = await readProcessingJob(jobId);
    expect(row.status).toBe("running");
  });
});

if (reachable) {
  afterAll(async () => {
    await pool().end();
  });
}
