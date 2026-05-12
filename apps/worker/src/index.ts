import "dotenv/config";

import { createServer } from "node:http";
import type PgBoss from "pg-boss";
import {
  boss,
  enqueue,
  QUEUE,
  type ProcessingJobPayload,
  type WorkAutoMemoryPayload,
  type WorkRunPayload,
} from "@neko/db/jobs";
import { createAdminHandler } from "./admin-server.js";
import {
  data_source,
  db,
  eq,
  getOrgId,
  metric,
  processing_job,
} from "@neko/db";
import {
  cancelAllAgents,
  discoveryUrlFromMcpUrl,
  prefetchKnowledgePack,
  provisionHostConfig,
  resolveAgentConcurrency,
  UpstreamProviderError,
  verifyAiCredentials,
} from "@neko/llm";
import { ensureOrgWorkspace, runWorkAutoMemoryPipeline } from "@neko/llm/work";
import type PgBossLib from "pg-boss";
import { runBusinessProfileBuild } from "./jobs/business-profile-build.js";
import { runIndustryInsightsBuild } from "./jobs/industry-insights-build.js";
import { runBootstrapMetricsBuild } from "./jobs/bootstrap-metrics-build.js";
import { runMetricRefresh } from "./jobs/metric-refresh.js";
import { runWorkRun } from "./jobs/work-run.js";
import { reconcileStaleProcessingJobs } from "./reconciler.js";

const PORT: number = 4100;
const MAX_JOB_RETRIES: number = 2;

const RECONCILE_SWEEP_INTERVAL_MS: number = 60_000;
const RECONCILE_SWEEP_MIN_AGE_MS: number = 660_000;
const SCHEDULED_REFRESH_HOURS: number = 24;

const ADMIN_ORG_ID = await getOrgId();

async function markRunning(processingJobId: string) {
  await db()
    .update(processing_job)
    .set({ status: "running", started_at: new Date(), updated_at: new Date() })
    .where(eq(processing_job.id, processingJobId));
}

async function markSucceeded(processingJobId: string) {
  await db()
    .update(processing_job)
    .set({
      status: "succeeded",
      finished_at: new Date(),
      error: null,
      updated_at: new Date(),
    })
    .where(eq(processing_job.id, processingJobId));
}

async function markFailed(processingJobId: string, error: string) {
  await db()
    .update(processing_job)
    .set({
      status: "failed",
      finished_at: new Date(),
      error,
      updated_at: new Date(),
    })
    .where(eq(processing_job.id, processingJobId));
}

function makeHandler<P extends ProcessingJobPayload>(
  fn: (processingJobId: string, orgId: string, payload: P) => Promise<void>,
) {
  return async (jobs: PgBoss.Job<P>[]) => {
    const results = await Promise.allSettled(
      jobs.map(async (job) => {
        const { processingJobId, orgId } = job.data;
        console.log(
          `[worker] running ${job.name} job=${processingJobId} org=${orgId}`,
        );
        await markRunning(processingJobId);
        try {
          await fn(processingJobId, orgId, job.data);
          await markSucceeded(processingJobId);
          console.log(`[worker] job ${processingJobId} succeeded`);
        } catch (e) {
          const msg = e instanceof Error ? e.message : String(e);
          await markFailed(processingJobId, msg);
          if (e instanceof UpstreamProviderError) {
            console.warn(
              `[worker] job ${processingJobId} upstream provider unavailable; skipping pg-boss retry: ${msg}`,
            );
            return;
          }
          console.warn(
            `[worker] job ${processingJobId} attempt failed; pg-boss may retry: ${msg}`,
          );
          if (e instanceof Error && e.stack) console.warn(e.stack);
          throw e;
        }
      }),
    );
    const firstFailure = results.find((r) => r.status === "rejected");
    if (firstFailure && firstFailure.status === "rejected") {
      throw firstFailure.reason;
    }
  };
}

async function runMetricRefreshSweep() {
  const cards = await db()
    .select({ id: metric.id, org_id: metric.org_id })
    .from(metric)
    .where(eq(metric.active, true));
  if (cards.length === 0) {
    console.log("[worker] scheduled sweep: no active metrics");
    return;
  }

  let enqueued = 0;
  for (const card of cards) {
    const inserted = await db()
      .insert(processing_job)
      .values({
        org_id: card.org_id,
        kind: "metric_refresh",
        status: "queued",
        trigger: "scheduled",
        trigger_payload: { metricId: card.id },
      })
      .returning({ id: processing_job.id });
    const processingJobId = inserted[0]?.id;
    if (!processingJobId) continue;
    await enqueue(QUEUE.METRIC_REFRESH, {
      processingJobId,
      orgId: card.org_id,
    });
    enqueued++;
  }
  console.log(`[worker] scheduled sweep: enqueued ${enqueued} metric_refresh job(s)`);
}

const server = createServer(createAdminHandler());

try {
  await verifyAiCredentials();
  console.log("[worker] startup credential check complete");
} catch (e) {
  console.warn(
    "[worker] startup credential check failed; continuing so settings can be configured.",
  );
  console.warn(e instanceof Error ? e.message : e);
}

await provisionHostConfig(ADMIN_ORG_ID);
console.log(
  `[worker] host config provisioned from DB (data_source + llm_provider_config)`,
);

{
  const sources = await db()
    .select({ mcp_url: data_source.mcp_url })
    .from(data_source)
    .where(eq(data_source.org_id, ADMIN_ORG_ID))
    .limit(1);
  const mcpUrl = sources[0]?.mcp_url;
  if (mcpUrl) {
    const workspace = await ensureOrgWorkspace(ADMIN_ORG_ID);
    const refresh = await prefetchKnowledgePack({
      discoveryUrl: discoveryUrlFromMcpUrl(mcpUrl),
      destDir: workspace.knowledgeRoot,
    });
    if (refresh.ok) {
      const totalBytes = refresh.files.reduce((n, f) => n + f.bytes, 0);
      console.log(
        `[worker] knowledge pack prefetched at ${workspace.knowledgeRoot} (${refresh.files.length} files, ${totalBytes}B)`,
      );
    } else {
      console.warn(
        `[worker] knowledge pack prefetch failed (${refresh.error}); agents will refresh lazily on first run`,
      );
    }
  } else {
    console.warn(
      "[worker] no data_source.mcp_url configured; skipping knowledge pack prefetch",
    );
  }
}

const concurrency = await resolveAgentConcurrency(ADMIN_ORG_ID);
console.log(
  `[worker] concurrency: globalCap=${concurrency.globalCap} (configure in /settings/agent; restart required)`,
);

const b = await boss();

{
  const summary = await reconcileStaleProcessingJobs();
  if (
    summary.succeeded + summary.failed + summary.requeued + summary.lost >
    0
  ) {
    console.log(
      `[worker] reconciled processing_job rows on boot: succeeded=${summary.succeeded} failed=${summary.failed} requeued=${summary.requeued} lost=${summary.lost}`,
    );
  }
}

for (const name of Object.values(QUEUE)) {
  await b.createQueue(name, { name, expireInSeconds: 600 });
}

await b.work(
  QUEUE.BUSINESS_PROFILE_BUILD,
  makeHandler<ProcessingJobPayload>(async (jobId, orgId) => {
    await runBusinessProfileBuild(jobId, orgId);
  }),
);

await b.work(
  QUEUE.INDUSTRY_INSIGHTS_BUILD,
  makeHandler<ProcessingJobPayload>(async (jobId, orgId) => {
    await runIndustryInsightsBuild(jobId, orgId);
  }),
);

await b.work(
  QUEUE.BOOTSTRAP_METRICS_BUILD,
  makeHandler<ProcessingJobPayload>(async (jobId, orgId) => {
    await runBootstrapMetricsBuild(jobId, orgId);
  }),
);

const workRunHandler = makeHandler<WorkRunPayload>(
  async (jobId, orgId, payload) => {
    await runWorkRun(jobId, orgId, {
      runId: payload.runId,
      threadId: payload.threadId,
      message: payload.message,
    });
  },
);
for (let i = 0; i < concurrency.globalCap; i++) {
  await b.work(
    QUEUE.WORK_RUN,
    { batchSize: 1, pollingIntervalSeconds: 0.5 },
    workRunHandler,
  );
}

const metricRefreshHandler = makeHandler<ProcessingJobPayload>(
  async (jobId, orgId) => {
    await runMetricRefresh(jobId, orgId);
  },
);
for (let i = 0; i < concurrency.globalCap; i++) {
  await b.work(
    QUEUE.METRIC_REFRESH,
    { batchSize: 1, pollingIntervalSeconds: 0.5 },
    metricRefreshHandler,
  );
}

await b.work(QUEUE.METRIC_REFRESH_SCHEDULED_SWEEP, async () => {
  await runMetricRefreshSweep();
});

await b.work(
  QUEUE.WORK_AUTO_MEMORY,
  async (jobs: PgBossLib.Job<WorkAutoMemoryPayload>[]) => {
    for (const job of jobs) {
      try {
        await runWorkAutoMemoryPipeline(job.data);
      } catch (e) {
        console.warn(
          `[work-auto-memory] job ${job.id} failed; pg-boss may retry:`,
          e instanceof Error ? e.message : e,
        );
        throw e;
      }
    }
  },
);

if (SCHEDULED_REFRESH_HOURS > 0) {
  const cron =
    SCHEDULED_REFRESH_HOURS === 1
      ? "0 * * * *"
      : `0 */${SCHEDULED_REFRESH_HOURS} * * *`;
  await b.schedule(QUEUE.METRIC_REFRESH_SCHEDULED_SWEEP, cron, {}, {
    tz: "UTC",
    retryLimit: MAX_JOB_RETRIES,
    retryDelay: 30,
  });
  console.log(
    `[worker] scheduled metric refresh sweep: cron="${cron}" (every ${SCHEDULED_REFRESH_HOURS}h)`,
  );
}

const reconcileTimer = setInterval(() => {
  reconcileStaleProcessingJobs({ minAgeMs: RECONCILE_SWEEP_MIN_AGE_MS })
    .then((s) => {
      const total = s.succeeded + s.failed + s.requeued + s.lost;
      if (total > 0) {
        console.log(
          `[worker] reconcile sweep: succeeded=${s.succeeded} failed=${s.failed} requeued=${s.requeued} lost=${s.lost}`,
        );
      }
    })
    .catch((e) => {
      console.warn(
        `[worker] reconcile sweep failed: ${e instanceof Error ? e.message : e}`,
      );
    });
}, RECONCILE_SWEEP_INTERVAL_MS);
reconcileTimer.unref();

server.listen(PORT, () => {
  console.log(
    `[worker] pg-boss running; /health on http://localhost:${PORT}`,
  );
});

const shutdown = async (signal: string) => {
  console.log(`[worker] received ${signal}; shutting down`);
  clearInterval(reconcileTimer);
  server.close();
  const cancelled = cancelAllAgents();
  if (cancelled > 0) {
    console.log(`[worker] cancelled ${cancelled} in-flight agent call(s)`);
  }
  try {
    await b.stop({ graceful: true });
  } catch (e) {
    console.error("[worker] pg-boss stop error:", e);
  }
  process.exit(0);
};
process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));
