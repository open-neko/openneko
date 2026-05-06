import "dotenv/config";

/**
 * Worker — pg-boss consumer.
 *
 * pg-boss owns claim/dispatch, retries, per-queue concurrency, scheduled
 * refresh, and orphan recovery. The HTTP server only hosts /health for
 * liveness checks; sync LLM RPCs (classify, provider-test) used to live
 * here but moved to @neko/llm so web can call them in-process.
 */

import { createServer } from "node:http";
import type PgBoss from "pg-boss";
import {
  boss,
  enqueue,
  QUEUE,
  type ProcessingJobPayload,
} from "@neko/db/jobs";
import {
  db,
  eq,
  getOrgId,
  metric,
  processing_job,
} from "@neko/db";
import {
  provisionHostConfig,
  resolveAgentConcurrency,
  verifyAiCredentials,
} from "@neko/llm";
import { runBusinessProfileBuild } from "./jobs/business-profile-build.js";
import { runIndustryInsightsBuild } from "./jobs/industry-insights-build.js";
import { runBootstrapMetricsBuild } from "./jobs/bootstrap-metrics-build.js";
import { runMetricRefresh } from "./jobs/metric-refresh.js";

// Hardcoded transport / scheduling constants. Surface these via /settings/agent
// (or a new /settings/worker page) the day an operator needs to tune them.
// Typed as `number` to keep the literal-narrowing branches below valid.
const PORT: number = 4100;
const MAX_JOB_RETRIES: number = 2;
const SCHEDULED_REFRESH_HOURS: number = 24;

// Concurrency caps come from the DB row (scope='agent'). Single-tenant
// assumption: caps for the (only) admin org apply to the whole worker.
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

/**
 * Wrap an existing (jobId, orgId) job handler so it can be registered with
 * pg-boss. Marks processing_job running on entry, succeeded on success, and
 * failed only on the final retry — intermediate failures stay in `running`
 * because pg-boss will retry them automatically.
 */
function makeHandler<P extends ProcessingJobPayload>(
  fn: (processingJobId: string, orgId: string, payload: P) => Promise<void>,
) {
  return async (jobs: PgBoss.Job<P>[]) => {
    // Process the batch in parallel. With batchSize > 1, serial iteration
    // would defeat the point — N concurrent enqueues would run end-to-end
    // instead of side-by-side, multiplying chat-question latency by N.
    // allSettled so one failure doesn't poison the rest of the batch.
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
          console.warn(
            `[worker] job ${processingJobId} attempt failed; pg-boss may retry: ${msg}`,
          );
          throw e;
        }
      }),
    );
    // Re-surface rejections so pg-boss schedules retries for failed jobs.
    // Successes don't bubble; only the first rejection rethrows, but every
    // job's processing_job state was already updated above.
    const firstFailure = results.find((r) => r.status === "rejected");
    if (firstFailure && firstFailure.status === "rejected") {
      throw firstFailure.reason;
    }
  };
}

/**
 * Scheduled sweep: walk active metrics, enqueue a metric_refresh per card.
 * Replaces the old 60s setInterval in worker/index.ts.
 */
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

// ───────────────────────────────────────────────────────────────────────
// HTTP server — liveness only. Sync LLM RPCs moved to @neko/llm so web
// calls them in-process; nothing routes to the worker over HTTP anymore.
// ───────────────────────────────────────────────────────────────────────

const server = createServer((req, res) => {
  if (req.method === "GET" && req.url === "/health") {
    res.writeHead(200).end("ok");
    return;
  }
  res.writeHead(404).end();
});

// ───────────────────────────────────────────────────────────────────────
// Boot.
// ───────────────────────────────────────────────────────────────────────

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

const concurrency = await resolveAgentConcurrency(ADMIN_ORG_ID);
console.log(
  `[worker] concurrency: globalCap=${concurrency.globalCap} claudeAgentCap=${concurrency.claudeAgentCap} (configure in /settings/agent; restart required)`,
);

const b = await boss();

// Ensure every queue we'll consume from exists. pg-boss v10 doesn't auto-
// create on send() or work(); calling createQueue is idempotent.
for (const name of Object.values(QUEUE)) {
  await b.createQueue(name);
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

// metric_refresh is the chat-path latency-critical queue. batchSize fetches
// up to N jobs per poll and the handler runs them concurrently (see
// makeHandler). Lower polling interval so a freshly-enqueued chat question
// gets picked up sub-second instead of the 2s default.
await b.work(
  QUEUE.METRIC_REFRESH,
  {
    batchSize: concurrency.globalCap,
    pollingIntervalSeconds: 0.5,
  },
  makeHandler<ProcessingJobPayload>(async (jobId, orgId) => {
    await runMetricRefresh(jobId, orgId);
  }),
);

await b.work(QUEUE.METRIC_REFRESH_SCHEDULED_SWEEP, async () => {
  await runMetricRefreshSweep();
});

if (SCHEDULED_REFRESH_HOURS > 0) {
  // Cron expression: at minute 0 of every Nth hour. pg-boss schedule strings
  // are standard cron (5-field) so we map hours→cron.
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

server.listen(PORT, () => {
  console.log(
    `[worker] pg-boss running; /health on http://localhost:${PORT}`,
  );
});

const shutdown = async (signal: string) => {
  console.log(`[worker] received ${signal}; shutting down`);
  server.close();
  try {
    await b.stop({ graceful: true });
  } catch (e) {
    console.error("[worker] pg-boss stop error:", e);
  }
  process.exit(0);
};
process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));
