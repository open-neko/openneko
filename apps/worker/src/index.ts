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
import { ensureOrgWorkspace } from "@neko/llm/work";
import { runBusinessProfileBuild } from "./jobs/business-profile-build.js";
import { runIndustryInsightsBuild } from "./jobs/industry-insights-build.js";
import { runBootstrapMetricsBuild } from "./jobs/bootstrap-metrics-build.js";
import { runMetricRefresh } from "./jobs/metric-refresh.js";
import { runWorkRun } from "./jobs/work-run.js";
import { reconcileStaleProcessingJobs } from "./reconciler.js";

// Hardcoded transport / scheduling constants. Surface these via /settings/agent
// (or a new /settings/worker page) the day an operator needs to tune them.
// Typed as `number` to keep the literal-narrowing branches below valid.
const PORT: number = 4100;
const MAX_JOB_RETRIES: number = 2;

// Periodic reconciler sweep — catches drift mid-uptime (the boot
// reconciler only fires once). minAgeMs must exceed the longest
// realistic gap between markRunning and the matching ack write so we
// don't trample a healthy in-flight handler. pg-boss expire_in is
// 600s; an extra 60s buffer is comfortable.
const RECONCILE_SWEEP_INTERVAL_MS: number = 60_000;
const RECONCILE_SWEEP_MIN_AGE_MS: number = 660_000;
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
          // Upstream provider errors (Gemini 503, etc.) are not worth
          // retrying — the same overloaded backend will reject the next
          // attempt the same way. Mark failed in our table, return
          // cleanly so pg-boss treats the job as completed and skips
          // its retry loop. The user can re-run via the card retry
          // button when ready.
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
// HTTP server — liveness + admin reconnect. Sync LLM RPCs moved to
// @neko/llm so web calls them in-process; the only thing routing to the
// worker over HTTP is the local /admin/reconnect signal that the web
// app's /api/admin/change-password handler fires after rotating the
// Postgres password (see route.ts). The worker exits cleanly so the
// supervisor (`tsx watch` in dev, Cloud Run min-instances=1 in prod)
// restarts it with fresh creds — pg-boss doesn't expose a clean way to
// re-register handlers against a fresh pool.
// ───────────────────────────────────────────────────────────────────────

const server = createServer(createAdminHandler());

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

// Prefetch the GraphJin knowledge pack onto disk under the admin
// org's workspace.knowledgeRoot. The prompts point agents at these
// files instead of inlining 30-40KB of discovery JSON every turn.
// Best-effort: if graphjin isn't reachable yet, skip — runs will
// re-fetch lazily and the agent's first attempt will see an empty
// pack until the next refresh.
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

// Reconcile non-terminal processing_job rows against pg-boss's
// authoritative state. Catches: handler crashed mid-flight, pg-boss
// expire_in fired while the handler kept running, ack write blipped,
// or the API route inserted a row but enqueue() failed afterwards.
// Each non-terminal row is mirrored to its pg-boss state — succeeded,
// failed, or reset to queued for redelivery if pg-boss is still on it.
// See apps/worker/src/reconciler.ts for the full state-mapping table.
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

// Ensure every queue we'll consume from exists. pg-boss v10 doesn't auto-
// create on send() or work(); calling createQueue is idempotent. Setting
// expireInSeconds at the queue level shortens the time pg-boss waits before
// returning a worker-killed 'active' job to 'created' for re-delivery —
// 10 min beats the 15-min default while still tolerating long agent runs.
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

// Work runs (interactive chat). Each user turn enqueues one job;
// the handler emits AgentEvent rows into work_run_event which the
// SSE `/runs/{id}/events` route long-polls. Same N-workers shape
// as metric_refresh below so a slow Hermes spawn for one thread
// doesn't head-of-line-block a freshly enqueued question on another.
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

// metric_refresh is the chat-path latency-critical queue. globalCap is a
// SHARED POOL: any task can use any free slot, regardless of what the
// other slots are doing. pg-boss v10 doesn't expose `teamSize`, so we
// realize that semantic by registering N independent workers, each with
// batchSize=1. Each worker polls on its own loop (every 0.5s) and grabs
// one job at a time, so a slow run can't head-of-line-block a freshly
// enqueued chat question — a free worker picks the new job up within
// the polling interval.
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

// Periodic reconciler sweep — catches mid-uptime drift between
// processing_job and pg-boss (handler crash, expire_in firing while
// the handler kept running, ack write failing, orphaned-by-failed-
// enqueue inserts). minAgeMs is set above pg-boss expire_in so an
// honestly in-flight handler is never touched.
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
  // Cancel every in-flight agent call up-front so jobs reject quickly
  // instead of blocking pg-boss graceful stop on long-running hermes /
  // claude calls. pg-boss returns the active jobs to created on graceful
  // stop, so the next worker boot retries them. Without this the systemd
  // unit hits TimeoutStopSec, gets SIGKILL'd, and orphan grandchildren
  // (Python venv, browser tools) survive across redeploys.
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
