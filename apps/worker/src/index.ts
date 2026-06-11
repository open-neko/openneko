import "dotenv/config";

import { createServer } from "node:http";
import type PgBoss from "pg-boss";
import {
  boss,
  enqueue,
  QUEUE,
  type ActionExecutePayload,
  type ChannelDeliverPayload,
  type ProcessingJobPayload,
  type WorkflowRunFirePayload,
  type WorkRunPayload,
} from "@neko/db/jobs";
import { createAdminHandler } from "./admin-server.js";
import {
  data_source,
  db,
  desc,
  eq,
  getOrgId,
  metric,
  processing_job,
} from "@neko/db";
import {
  cancelAllAgents,
  prefetchKnowledgeForOrg,
  provisionHostConfig,
  resolveAgentConcurrency,
  UpstreamProviderError,
  verifyAiCredentials,
} from "@neko/llm";
import {
  getDataSourceForOrg,
  getWorkflowRunChainDepth,
  dispatchExternalEvent,
  handleSourceChangeMatch,
  handleSubscriptionMatch,
  registerBuiltinAdapters,
  seedDefaultActionPolicies,
  seedPluginActionPolicies,
  startSubscriptionManager,
  type DataSourceContext,
  type PluginActionSeed,
} from "@neko/llm/workflows";
import { ensureOrgWorkspace, reportDeploymentProfile } from "@neko/llm/work";
import { ensureQueueExists } from "./pg-boss-helpers.js";
import { PluginRegistry } from "./plugins/plugin-registry.js";
import { setPluginRegistryInstance } from "./plugins/registry-instance.js";
import {
  ingestInboundWebhook,
  registerChannelOutputDelivery,
  runChannelDelivery,
} from "./channels/delivery.js";
import { startChannelInbound } from "./channels/inbound-poll.js";
import type PgBossLib from "pg-boss";
import { runBusinessProfileBuild } from "./jobs/business-profile-build.js";
import { runIndustryInsightsBuild } from "./jobs/industry-insights-build.js";
import { runBootstrapMetricsBuild } from "./jobs/bootstrap-metrics-build.js";
import { runMetricRefresh } from "./jobs/metric-refresh.js";
import { runWorkRun } from "./jobs/work-run.js";
import { runWorkflowCronSweep } from "./jobs/workflow-cron-sweep.js";
import { runWorkflowRunFire } from "./jobs/workflow-run-fire.js";
import { runWorkflowOutputTtlSweep } from "./jobs/workflow-output-ttl-sweep.js";
import { runActionExecute } from "./jobs/action-execute.js";
import {
  reconcileStaleProcessingJobs,
  reconcileStaleRuns,
} from "./reconciler.js";

const PORT: number = 4100;
const MAX_JOB_RETRIES: number = 2;

const RECONCILE_SWEEP_INTERVAL_MS: number = 60_000;
const RECONCILE_SWEEP_MIN_AGE_MS: number = 660_000;
const SCHEDULED_REFRESH_HOURS: number = 24;

const ADMIN_ORG_ID = await getOrgId();

// SEC8: state the security posture once at boot; hardened warns when
// the model host is a public cloud API (on-prem LLM is client-provided).
reportDeploymentProfile();

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

// The plugin registry is constructed below before the server starts
// taking requests; we expose its auth surface via a closure so the
// admin handler can lazily reach a freshly-installed auth plugin
// without a server restart.
let pluginRegistry: PluginRegistry | null = null;
const server = createServer(
  createAdminHandler({
    auth: {
      getAuthProvider: () => pluginRegistry?.getAuthProvider() ?? null,
      beginAuth: (params) => {
        if (!pluginRegistry) {
          throw new Error("plugin registry not initialised");
        }
        return pluginRegistry.beginAuth(params);
      },
      completeAuth: (params) => {
        if (!pluginRegistry) {
          throw new Error("plugin registry not initialised");
        }
        return pluginRegistry.completeAuth(params);
      },
    },
    plugins: {
      getRegisteredActionDescriptors: () =>
        pluginRegistry?.getRegisteredActionDescriptors() ?? [],
    },
    events: {
      dispatchExternal: async (input) => {
        const result = await dispatchExternalEvent({
          orgId: input.orgId,
          event: {
            name: input.name,
            source: input.source,
            payload: input.payload,
            ...(input.dedupeKey ? { dedupeKey: input.dedupeKey } : {}),
          },
        });
        return { matched: result.matched, enqueued: result.enqueued };
      },
    },
    installPolicy: {
      getInstallPolicy: async () => {
        const { getInstallPolicyForOrg } = await import("@neko/db");
        return getInstallPolicyForOrg(ADMIN_ORG_ID);
      },
    },
    connect: {
      getConnectProviders: () => pluginRegistry?.getConnectProviders() ?? [],
      getOperatorConnectStatus: (operatorId) =>
        pluginRegistry?.getOperatorConnectStatus(operatorId) ?? [],
      beginConnect: (pluginName, params) => {
        if (!pluginRegistry) throw new Error("plugin registry not initialised");
        return pluginRegistry.beginConnect(pluginName, params);
      },
      completeConnect: (pluginName, params) => {
        if (!pluginRegistry) throw new Error("plugin registry not initialised");
        return pluginRegistry.completeConnect(pluginName, params);
      },
      refreshConnect: (pluginName, operatorId) => {
        if (!pluginRegistry) throw new Error("plugin registry not initialised");
        return pluginRegistry.refreshConnect(pluginName, operatorId);
      },
      disconnect: (pluginName, operatorId) => {
        if (!pluginRegistry) throw new Error("plugin registry not initialised");
        return pluginRegistry.disconnect(pluginName, operatorId);
      },
    },
    channels: {
      getChannelProviders: () => pluginRegistry?.getChannelProviders() ?? [],
      deliver: (pluginName, recipient, events) => {
        if (!pluginRegistry) throw new Error("plugin registry not initialised");
        return pluginRegistry.deliverOnChannel(pluginName, recipient, events);
      },
      ingestInbound: (pluginName, headers, body) =>
        ingestInboundWebhook(ADMIN_ORG_ID, pluginName, headers, body),
    },
  }),
);

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

await seedDefaultActionPolicies(ADMIN_ORG_ID);
registerBuiltinAdapters();
// ADM3: execute approved chat-proposed plugin installs/uninstalls.
{
  const {
    registerChannelAdminAdapter,
    registerDataSourceAdminAdapter,
    registerPluginManagementAdapters,
    registerUserAdminAdapter,
  } = await import("./plugins/manage-adapters.js");
  registerUserAdminAdapter();
  registerChannelAdminAdapter();
  registerDataSourceAdminAdapter();
  registerPluginManagementAdapters({
    repoRoot: process.cwd(),
    getInstallPolicy: async () => {
      const { getInstallPolicyForOrg } = await import("@neko/db");
      return getInstallPolicyForOrg(ADMIN_ORG_ID);
    },
  });
}
console.log("[worker] action policies seeded and built-in adapters registered");

// SEC3: pick the secret residency from local config — Infisical-backed
// env bags when configured, the enc:v1 local file otherwise.
const secretsResolver = await (async () => {
  const { readLocalConfig } = await import("@neko/db");
  const cfg = readLocalConfig().secrets;
  if (cfg?.backend === "infisical" && cfg.infisical) {
    const { InfisicalSecretsResolver } = await import(
      "@open-neko/plugin-install"
    );
    console.log(
      `[worker] secrets backend: infisical (${cfg.infisical.siteUrl}, project ${cfg.infisical.projectId})`,
    );
    return new InfisicalSecretsResolver(cfg.infisical);
  }
  return undefined; // registry defaults to the file resolver
})();

pluginRegistry = new PluginRegistry({
  repoRoot: process.cwd(),
  pluginInstallDir: process.env.OPENNEKO_PLUGIN_INSTALL_DIR || undefined,
  workRoot: `${process.env.HOME ?? "/tmp"}/.openneko/plugins`,
  ...(secretsResolver ? { secretsResolver } : {}),
  loadInstallPolicy: async () => {
    const { getInstallPolicyForOrg } = await import("@neko/db");
    return getInstallPolicyForOrg(ADMIN_ORG_ID);
  },
  onManifestRefresh: async (entries) => {
    const seeds: PluginActionSeed[] = [];
    for (const entry of entries) {
      for (const decl of entry.capabilities.action?.kinds ?? []) {
        seeds.push({
          pluginName: entry.name,
          kind: decl.kind,
          description: decl.description,
          default_mode: decl.default_mode,
        });
      }
    }
    const { created, skipped } = await seedPluginActionPolicies(
      ADMIN_ORG_ID,
      seeds,
    );
    if (created > 0) {
      console.log(
        `[worker] seeded ${created} plugin action_policy row(s) (${skipped} already present or non-auto)`,
      );
    }
  },
});
await pluginRegistry.start();
setPluginRegistryInstance(pluginRegistry);
registerChannelOutputDelivery();
{
  const s = pluginRegistry.status();
  if (s.loaded.length > 0) {
    console.log(
      `[worker] plugin registry: ${s.loaded.length} plugin(s), ${s.kinds.length} action kind(s) registered (VMs lazy-spawn on first use)`,
    );
  } else {
    console.log(`[worker] plugin registry: no plugins installed`);
  }
  if (s.authProvider) {
    console.log(`[worker] auth provider plugin active: ${s.authProvider}`);
  }
  for (const skipped of s.skipped) {
    console.warn(`[worker] plugin skipped ${skipped.name}: ${skipped.reason}`);
  }
}

{
  const sources = await db()
    .select({ mcp_url: data_source.mcp_url })
    .from(data_source)
    .where(eq(data_source.org_id, ADMIN_ORG_ID))
    .orderBy(desc(data_source.is_default), data_source.created_at)
    .limit(1);
  const mcpUrl = sources[0]?.mcp_url;
  if (mcpUrl) {
    const workspace = await ensureOrgWorkspace(ADMIN_ORG_ID);
    const refresh = await prefetchKnowledgeForOrg(
      ADMIN_ORG_ID,
      workspace.knowledgeRoot,
    );
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

// GraphJin URL — the worker is a client of neko-graphjin, the OpenNeko
// metadata GraphJin service. Default targets localhost so `pnpm dev`
// works against a compose-up'd neko-graphjin (port 8089 exposed). The
// containerized deploy sets OPENNEKO_GRAPHJIN_URL=http://neko-graphjin:8089
// in compose.yml so service-DNS lookup wins there. Customer-data
// graphjin (used by the agent CLI path) is a separate service on
// port 8080.
// Normalize any GraphJin base (origin or full) to the full GraphQL endpoint.
// The subscription/query clients treat baseUrl as the complete
// `/api/v1/graphql` URL and never append a path, so both the env-configured
// neko-graphjin URL (often just an origin) and the data_source URLs (already
// full) must land on the same shape.
function toGraphqlEndpoint(url: string): string {
  const trimmed = url.replace(/\/+$/, "");
  return trimmed.endsWith("/api/v1/graphql")
    ? trimmed
    : `${trimmed}/api/v1/graphql`;
}

const GRAPHJIN_URL = toGraphqlEndpoint(
  process.env.OPENNEKO_GRAPHJIN_URL ?? "http://127.0.0.1:8089",
);
console.log(`[worker] neko graphjin client targeting ${GRAPHJIN_URL}`);

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
  // At boot nothing this worker tracks is running yet, so any "running" run is
  // a zombie left by a hard restart — cancel it instead of stranding it.
  const runs = await reconcileStaleRuns();
  if (runs.cancelled > 0) {
    console.log(
      `[worker] cancelled ${runs.cancelled} stale running run(s) on boot`,
    );
  }
}

for (const name of Object.values(QUEUE)) {
  await ensureQueueExists((qName, opts) => b.createQueue(qName, opts), name);
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
      channel: payload.channel,
      channelPlugin: payload.channelPlugin,
      recipient: payload.recipient,
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
  // SEC6: archive expired memories + stale pending proposals first so
  // the checkpoint below snapshots the post-TTL state.
  try {
    const { sweepExpiredWorkMemories } = await import("@neko/llm/work");
    const swept = await sweepExpiredWorkMemories();
    if (swept.archived || swept.expiredPending) {
      console.log(
        `[worker] memory TTL sweep: archived ${swept.archived}, expired ${swept.expiredPending} pending`,
      );
    }
  } catch (e) {
    console.warn(
      `[worker] memory TTL sweep failed: ${e instanceof Error ? e.message : e}`,
    );
  }
  // CV0: nightly memory checkpoint into the org config repo; CV4 also
  // checkpoints each member's personal layer onto their user/<id> ref.
  try {
    const { getOrgAgentRoot } = await import("@neko/llm/work");
    const { snapshotDurableMemories, snapshotUserConfigsForOrg } =
      await import("@neko/llm/config-vcs");
    await snapshotDurableMemories(ADMIN_ORG_ID, getOrgAgentRoot(ADMIN_ORG_ID));
    await snapshotUserConfigsForOrg(ADMIN_ORG_ID, getOrgAgentRoot(ADMIN_ORG_ID));
  } catch (e) {
    console.warn(
      `[worker] memory checkpoint failed: ${e instanceof Error ? e.message : e}`,
    );
  }
});

await b.work(QUEUE.WORKFLOW_CRON_SWEEP, async () => {
  // SEC7: behavioral thresholds ride the minute tick so a runaway agent
  // alerts within its window, not at the nightly sweep.
  try {
    const { profileBehaviorThresholds, runBehaviorSweep } = await import(
      "@neko/llm/work"
    );
    await runBehaviorSweep(ADMIN_ORG_ID, profileBehaviorThresholds());
  } catch (e) {
    console.warn(
      `[worker] behavior sweep failed: ${e instanceof Error ? e.message : e}`,
    );
  }
  await runWorkflowCronSweep();
  // OL4: condition watchers poll on the same tick (each watcher's own
  // cadence gates how often its query actually runs).
  try {
    const { sweepWatchers } = await import("@neko/llm/workflows");
    await sweepWatchers(ADMIN_ORG_ID);
  } catch (e) {
    console.warn(
      `[worker] watcher sweep failed: ${e instanceof Error ? e.message : e}`,
    );
  }
});

await b.work(
  QUEUE.WORKFLOW_RUN_FIRE,
  { batchSize: 1, pollingIntervalSeconds: 0.5 },
  async (jobs: PgBossLib.Job<WorkflowRunFirePayload>[]) => {
    for (const job of jobs) {
      try {
        await runWorkflowRunFire(job.data);
      } catch (e) {
        console.warn(
          `[workflow-run-fire] job ${job.id} failed; pg-boss may retry: ${e instanceof Error ? e.message : e}`,
        );
        throw e;
      }
    }
  },
);

await b.work(
  QUEUE.ACTION_EXECUTE,
  { batchSize: 1, pollingIntervalSeconds: 0.5 },
  async (jobs: PgBossLib.Job<ActionExecutePayload>[]) => {
    for (const job of jobs) {
      try {
        await runActionExecute(job.data);
      } catch (e) {
        console.warn(
          `[action-execute] job ${job.id} failed; pg-boss may retry: ${e instanceof Error ? e.message : e}`,
        );
        throw e;
      }
    }
  },
);

await b.work(
  QUEUE.CHANNEL_DELIVER,
  { batchSize: 1, pollingIntervalSeconds: 0.5 },
  async (jobs: PgBossLib.Job<ChannelDeliverPayload>[]) => {
    for (const job of jobs) {
      try {
        await runChannelDelivery(job.data);
      } catch (e) {
        console.warn(
          `[channel-deliver] job ${job.id} failed; pg-boss may retry: ${e instanceof Error ? e.message : e}`,
        );
        throw e;
      }
    }
  },
);

await b.schedule(QUEUE.WORKFLOW_CRON_SWEEP, "* * * * *", {}, {
  tz: "UTC",
  retryLimit: 1,
  retryDelay: 15,
});
console.log("[worker] scheduled workflow cron sweep every minute");

await b.work(QUEUE.WORKFLOW_OUTPUT_TTL_SWEEP, async () => {
  await runWorkflowOutputTtlSweep();
});

await b.schedule(QUEUE.WORKFLOW_OUTPUT_TTL_SWEEP, "0 * * * *", {}, {
  tz: "UTC",
  retryLimit: 1,
  retryDelay: 60,
});
console.log("[worker] scheduled workflow_output ttl sweep hourly");

type CachedDataSource = { ctx: DataSourceContext; expiresAt: number };
const DATA_SOURCE_CACHE_MS = 60_000;
const dataSourceCache = new Map<string, CachedDataSource>();

async function loadDataSourceContext(orgId: string): Promise<DataSourceContext> {
  const now = Date.now();
  const cached = dataSourceCache.get(orgId);
  if (cached && cached.expiresAt > now) return cached.ctx;
  const ctx = await getDataSourceForOrg(orgId);
  if (!ctx) throw new Error(`no data_source configured for org ${orgId}`);
  dataSourceCache.set(orgId, { ctx, expiresAt: now + DATA_SOURCE_CACHE_MS });
  return ctx;
}

const subscriptionManager = startSubscriptionManager({
  resolveTransport: async (sub) => {
    if (sub.sourceKind === "source_change") {
      const ctx = await loadDataSourceContext(sub.orgId);
      // GraphJin serves subscriptions on the same endpoint as queries, so
      // drive off graphql_url (the URL that's kept reachable per deploy mode).
      // subscription_url drifts — it isn't rewritten alongside graphql_url
      // when the host is provisioned (e.g. compose service-DNS vs host
      // localhost), which left source_change subs pointing at an
      // unresolvable host.
      return { baseUrl: toGraphqlEndpoint(ctx.graphqlUrl) };
    }
    return { baseUrl: GRAPHJIN_URL };
  },
  refreshIntervalMs: 60_000,
  onMatch: async (event) => {
    if (event.kind === "workflow_output") {
      const decision = await handleSubscriptionMatch({
        subscription: event.subscription,
        output: event.output,
        resolveProducingRunChainDepth: getWorkflowRunChainDepth,
      });
      if (decision.action === "dropped") {
        console.log(
          `[subscription-manager] dropped match sub=${event.subscription.id} output=${event.output.id}: ${decision.reason}`,
        );
      } else {
        console.log(
          `[subscription-manager] enqueued sub=${event.subscription.id} output=${event.output.id} obs=${decision.observationId}`,
        );
      }
      return;
    }
    if (event.kind === "source_change") {
      const ctx = await loadDataSourceContext(event.subscription.orgId);
      const decision = await handleSourceChangeMatch({
        subscription: event.subscription,
        match: event.match,
        dataSourceId: ctx.id,
      });
      const pk = JSON.stringify(event.match.primary_key);
      if (decision.action === "dropped") {
        console.log(
          `[subscription-manager] dropped source_change sub=${event.subscription.id} ${event.match.table}:${pk}: ${decision.reason}`,
        );
      } else {
        console.log(
          `[subscription-manager] enqueued source_change sub=${event.subscription.id} ${event.match.table}:${pk} obs=${decision.observationId}`,
        );
      }
      return;
    }
  },
  onError: (err, sub) => {
    console.warn(
      `[subscription-manager] error${sub ? ` sub=${sub.id}` : ""}: ${err.message}`,
    );
  },
});

subscriptionManager.ready
  .then(() => {
    console.log(
      `[worker] subscription manager ready (${subscriptionManager.activeSubscriptionIds().length} active)`,
    );
  })
  .catch((err) => {
    console.warn(
      `[subscription-manager] initial connect failed: ${err instanceof Error ? err.message : err}`,
    );
  });


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
  reconcileStaleRuns({ minAgeMs: RECONCILE_SWEEP_MIN_AGE_MS })
    .then((s) => {
      if (s.cancelled > 0) {
        console.log(
          `[worker] reconcile sweep: cancelled ${s.cancelled} stale running run(s)`,
        );
      }
    })
    .catch((e) => {
      console.warn(
        `[worker] stale-run sweep failed: ${e instanceof Error ? e.message : e}`,
      );
    });
}, RECONCILE_SWEEP_INTERVAL_MS);
reconcileTimer.unref();

server.listen(PORT, () => {
  console.log(
    `[worker] pg-boss running; /health on http://localhost:${PORT}`,
  );
});

const channelInbound = startChannelInbound(ADMIN_ORG_ID);

const shutdown = async (signal: string) => {
  console.log(`[worker] received ${signal}; shutting down`);
  clearInterval(reconcileTimer);
  channelInbound.stop();
  server.close();
  const cancelled = cancelAllAgents();
  if (cancelled > 0) {
    console.log(`[worker] cancelled ${cancelled} in-flight agent call(s)`);
  }
  try {
    await subscriptionManager.stop();
  } catch (e) {
    console.error("[worker] subscription manager stop error:", e);
  }
  try {
    setPluginRegistryInstance(null);
    if (pluginRegistry) {
      await pluginRegistry.stop();
    }
  } catch (e) {
    console.error("[worker] plugin shutdown error:", e);
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
