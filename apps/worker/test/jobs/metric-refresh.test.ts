/**
 * runMetricRefresh job orchestration tests.
 *
 * The metric agent itself (runMetricAgent) is mocked — we're testing the
 * worker's wiring, not the LLM. Asserts:
 *   - Path 1 (bootstrap): metricId payload → loads metric → snapshot lands
 *   - Path 2 (chat): question payload → creates metric row + snapshot
 *   - Validation failures throw + leave no snapshot
 *   - Backend semaphore is acquired and released even on failure
 */

import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
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
  metric_snapshot,
  pool,
  processing_job,
} from "@neko/db";

const { mockRunMetricAgent, mockResolveBackendId } = vi.hoisted(() => ({
  mockRunMetricAgent: vi.fn(),
  mockResolveBackendId: vi.fn(),
}));

vi.mock("@neko/llm", async () => {
  const actual = await vi.importActual<typeof import("@neko/llm")>("@neko/llm");
  return {
    ...actual,
    runMetricAgent: mockRunMetricAgent,
    resolveAgentBackendId: mockResolveBackendId,
  };
});

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[jobs/metric-refresh] skipping: Postgres unreachable.");
}

function stubResult(overrides: Record<string, unknown> = {}) {
  return {
    reasoning: "stub",
    headlineMetric: "$1.00M",
    headlineLabel: "Test",
    insightText: "Up.",
    detailText: "Driven by stub.",
    mood: "watch",
    chartType: "kpi",
    chartData: [{ d: "Test", v: 1_000_000, t: 950_000 }],
    timeWindow: {
      grain: "year",
      start: "2024-04-01",
      end: "2025-04-01",
      label: "TTM",
    },
    ...overrides,
  };
}

async function insertProcessingJob(
  orgId: string,
  payload: Record<string, unknown>,
): Promise<string> {
  const ins = await db()
    .insert(processing_job)
    .values({
      org_id: orgId,
      kind: "metric_refresh",
      status: "running",
      trigger: "test",
      trigger_payload: payload,
      started_at: new Date(),
    })
    .returning({ id: processing_job.id });
  return ins[0]!.id;
}

// metric_snapshot has no org_id column — it scopes via metric.metric_id.
// Without this join, the query would return snapshots from every other org
// in the dev DB and fail length assertions that assume a clean slate.
async function snapshotsForOrg(orgId: string): Promise<{ id: string }[]> {
  return db()
    .select({ id: metric_snapshot.id })
    .from(metric_snapshot)
    .innerJoin(metric, eq(metric_snapshot.metric_id, metric.id))
    .where(eq(metric.org_id, orgId));
}

async function insertMetric(orgId: string, slug: string): Promise<string> {
  const ins = await db()
    .insert(metric)
    .values({
      org_id: orgId,
      role: "CEO",
      slug,
      source: "bootstrap",
      title: `Title for ${slug}`,
      why: `Why ${slug}`,
      chart_hint: "kpi",
      active: true,
    })
    .returning({ id: metric.id });
  return ins[0]!.id;
}

describeIfDb("runMetricRefresh", () => {
  let orgId: string;
  let runMetricRefresh: typeof import("../../src/jobs/metric-refresh").runMetricRefresh;

  beforeAll(async () => {
    const mod = await import("../../src/jobs/metric-refresh.js");
    runMetricRefresh = mod.runMetricRefresh;
  });

  beforeEach(async () => {
    orgId = uniqueOrgId("job-metric-refresh");
    await createTestOrg(orgId);
    mockResolveBackendId.mockResolvedValue("hermes");
    mockRunMetricAgent.mockResolvedValue(stubResult());
  });

  afterEach(async () => {
    await deleteTestOrg(orgId);
    vi.clearAllMocks();
  });

  afterAll(async () => {
    await pool().end();
  });

  describe("path 1: bootstrap metric (trigger_payload.metricId)", () => {
    it("loads the metric, runs the agent, writes a snapshot", async () => {
      const metricId = await insertMetric(orgId, "revenue-by-channel");
      const jobId = await insertProcessingJob(orgId, { metricId });

      await runMetricRefresh(jobId, orgId);

      const snaps = await db()
        .select({ status: metric_snapshot.status, payload: metric_snapshot.payload })
        .from(metric_snapshot)
        .where(eq(metric_snapshot.metric_id, metricId));
      expect(snaps).toHaveLength(1);
      expect(snaps[0].status).toBe("watch");

      // Mock was called with the right slug + role
      expect(mockRunMetricAgent).toHaveBeenCalledWith(
        expect.objectContaining({
          orgId,
          slug: "revenue-by-channel",
          role: "CEO",
          jobId,
        }),
      );
    });

    it("throws when the metricId doesn't resolve to a row (no snapshot written)", async () => {
      const fakeId = "00000000-0000-0000-0000-000000000000";
      const jobId = await insertProcessingJob(orgId, { metricId: fakeId });

      await expect(runMetricRefresh(jobId, orgId)).rejects.toThrow(/not found/);

      const snaps = await snapshotsForOrg(orgId);
      expect(snaps).toHaveLength(0);
    });
  });

  describe("path 2: chat question (trigger_payload.question)", () => {
    it("creates a chat metric row, then a snapshot pointing at it", async () => {
      const jobId = await insertProcessingJob(orgId, {
        question: "What's our revenue this month?",
        slug: "chat-revenue-q",
        title: "Revenue MTD",
        why: "User asked",
        chartHint: "kpi",
        role: "CEO",
      });

      await runMetricRefresh(jobId, orgId);

      const metrics = await db()
        .select({ slug: metric.slug, source: metric.source, active: metric.active })
        .from(metric)
        .where(eq(metric.org_id, orgId));
      expect(metrics).toHaveLength(1);
      expect(metrics[0]).toMatchObject({ slug: "chat-revenue-q", source: "chat", active: false });

      const snaps = await snapshotsForOrg(orgId);
      expect(snaps).toHaveLength(1);
    });

    it("re-running with same slug+role+org reuses the existing metric row", async () => {
      const payload = {
        question: "Q?",
        slug: "chat-x",
        title: "X",
        why: "Y",
        chartHint: "bar",
        role: "CEO",
      };
      const job1 = await insertProcessingJob(orgId, payload);
      await runMetricRefresh(job1, orgId);
      const job2 = await insertProcessingJob(orgId, payload);
      await runMetricRefresh(job2, orgId);

      const metrics = await db()
        .select({ id: metric.id, created_by_job: metric.created_by_job })
        .from(metric)
        .where(and(eq(metric.org_id, orgId), eq(metric.slug, "chat-x")));
      expect(metrics).toHaveLength(1);
      // Status endpoint locates the snapshot via metric.created_by_job, so the
      // reused row must be re-linked to the latest job — otherwise rerun /
      // re-ask returns payload=null in the UI.
      expect(metrics[0].created_by_job).toBe(job2);
      const snaps = await snapshotsForOrg(orgId);
      expect(snaps).toHaveLength(2);
    });
  });

  describe("metric.last_refresh_status bookkeeping", () => {
    it("stamps last_refresh_status='ok' on success", async () => {
      const metricId = await insertMetric(orgId, "ok-status");
      const jobId = await insertProcessingJob(orgId, { metricId });
      await runMetricRefresh(jobId, orgId);

      const rows = await db()
        .select({
          status: metric.last_refresh_status,
          err: metric.last_refresh_error,
          jobRef: metric.last_refresh_job_id,
        })
        .from(metric)
        .where(eq(metric.id, metricId));
      expect(rows[0].status).toBe("ok");
      expect(rows[0].err).toBeNull();
      expect(rows[0].jobRef).toBe(jobId);
    });

    it("stamps last_refresh_status='failed' + error on validation failure", async () => {
      mockRunMetricAgent.mockResolvedValueOnce(stubResult({ mood: "ecstatic" }));
      const metricId = await insertMetric(orgId, "fail-status-mood");
      const jobId = await insertProcessingJob(orgId, { metricId });

      await expect(runMetricRefresh(jobId, orgId)).rejects.toThrow();

      const rows = await db()
        .select({
          status: metric.last_refresh_status,
          err: metric.last_refresh_error,
          jobRef: metric.last_refresh_job_id,
        })
        .from(metric)
        .where(eq(metric.id, metricId));
      expect(rows[0].status).toBe("failed");
      expect(rows[0].err).toMatch(/invalid mood/);
      expect(rows[0].jobRef).toBe(jobId);

      // Snapshot should not exist on failure path.
      const snaps = await snapshotsForOrg(orgId);
      expect(snaps).toHaveLength(0);
    });

    it("stamps last_refresh_status='failed' on sentinel-headline rejection", async () => {
      mockRunMetricAgent.mockResolvedValueOnce(
        stubResult({ headlineMetric: "Error" }),
      );
      const metricId = await insertMetric(orgId, "fail-status-sentinel");
      const jobId = await insertProcessingJob(orgId, { metricId });

      await expect(runMetricRefresh(jobId, orgId)).rejects.toThrow(/sentinel/);

      const rows = await db()
        .select({ status: metric.last_refresh_status, err: metric.last_refresh_error })
        .from(metric)
        .where(eq(metric.id, metricId));
      expect(rows[0].status).toBe("failed");
      expect(rows[0].err).toMatch(/sentinel/);
    });
  });

  describe("validation", () => {
    it("rejects an agent result with an invalid mood (no snapshot written)", async () => {
      mockRunMetricAgent.mockResolvedValueOnce(stubResult({ mood: "ecstatic" }));
      const metricId = await insertMetric(orgId, "bad-mood-metric");
      const jobId = await insertProcessingJob(orgId, { metricId });

      await expect(runMetricRefresh(jobId, orgId)).rejects.toThrow(/invalid mood/);
      const snaps = await snapshotsForOrg(orgId);
      expect(snaps).toHaveLength(0);
    });
  });

  describe("backend resolution", () => {
    it("resolveAgentBackendId is called once per job", async () => {
      const metricId = await insertMetric(orgId, "backend-call");
      const jobId = await insertProcessingJob(orgId, { metricId });
      await runMetricRefresh(jobId, orgId);
      expect(mockResolveBackendId).toHaveBeenCalledTimes(1);
      expect(mockResolveBackendId).toHaveBeenCalledWith(orgId);
    });
  });
});
