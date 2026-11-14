import { getOrgId, keepProcessThroughDatabaseRestarts, readLocalConfig, secretsBackendError } from "@neko/db";
import "dotenv/config";

import { createServer } from "node:http";
import { adminSocketPath, listenAdmin } from "./admin-listen.js";
import { boss, MOVED_QUEUES, RETIRED_QUEUES } from "@neko/db/jobs";
import {
  createAdminHandler,
  type SsoSetupHandlerSurface,
} from "./admin-server.js";
import {
  SsoSetupService,
  startSsoSetupPoller,
} from "./sso/sso-setup-service.js";
import { CoreSecrets } from "./secrets/core-secrets.js";
import { SignInService } from "./signin/signin-service.js";
import {
  applyGroupGrants,
  disableGroupGrants,
  enableGroupGrants,
  scheduleGroupGrantsApply,
} from "./graphjin/group-grants-service.js";
import {
  createDirectoryUser,
  directoryStatus,
  runDirectorySync,
} from "./directory/directory-service.js";
import {
  cancelAllAgents,
  provisionHostConfig,
  resolveAgentConcurrency,
  verifyAiCredentials,
} from "@neko/llm";
import {
  getDataSourceForOrg,
  getWorkflowRunChainDepth,
  createActionRequest,
  dispatchExternalEvent,
  handleSourceChangeMatch,
  handleSubscriptionMatch,
  registerActionRequestCreatedHook,
  registerBuiltinAdapters,
  seedDefaultActionPolicies,
  startSubscriptionManager,
  type DataSourceContext,
} from "@neko/llm/workflows";
import { reportDeploymentProfile, setAgentSlotLimit } from "@neko/llm/work";
import { removeRetiredQueue } from "./pg-boss-helpers.js";
import { setCoreSecretsInstance } from "./secrets/instance.js";
import { runActionExecute } from "./jobs/action-execute.js";
import { moveLibraryDocumentsToStore } from "./jobs/library-recovery.js";
import { filesEnabled } from "@neko/llm/files";
import {
  initializeWorkerTelemetry,
  shutdownWorkerTelemetry,
} from "./telemetry.js";

{
  const problem = secretsBackendError(readLocalConfig());
  if (problem) {
    console.error(`[worker] ${problem}`);
    process.exit(1);
  }
}
process.env.OPENNEKO_SANDBOX_OWNER ||= "worker";
keepProcessThroughDatabaseRestarts("worker");

const PORT: number = 4100;


if (initializeWorkerTelemetry()) {
  console.log("[telemetry] OTLP trace export enabled");
}

const ADMIN_ORG_ID = await getOrgId();
// SEC8: state the security posture once at boot; hardened warns when
// the model host is a public cloud API (on-prem LLM is client-provided).
reportDeploymentProfile();

// The secrets view and sign-in service are constructed below, after the
// server starts taking requests; the admin handler reaches them through
// closures and reports "not ready" until then.
let coreSecrets: CoreSecrets | null = null;
let ssoSetupService: SsoSetupService | null = null;
let signInService: SignInService | null = null;
function signIn(): SignInService {
  if (!signInService) throw new Error("sign-in service not ready");
  return signInService;
}
let actionRequestPreflightReady = false;
let actionAdaptersReady = false;
function ssoSetupSurface(): SsoSetupHandlerSurface {
  return {
    getStatus: (orgId) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.getStatus(orgId);
    },
    setScalekitIds: (orgId, input) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.setScalekitIds(orgId, input);
    },
    listEnvironments: (orgId) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.listEnvironments(orgId);
    },
    listOrganizations: (orgId, environmentId) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.listOrganizations(orgId, environmentId);
    },
    getEnvironmentCredentials: (orgId, environmentId) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.getEnvironmentCredentials(orgId, environmentId);
    },
    setSignInCredentials: (orgId, input) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.setSignInCredentials(orgId, input);
    },
    generatePortalLink: (orgId) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.generatePortalLink(orgId);
    },
    checkConnection: (orgId) => {
      if (!ssoSetupService) throw new Error("SSO setup service not ready");
      return ssoSetupService.checkConnection(orgId);
    },
  };
}
const server = createServer(
  createAdminHandler({
    actions: {
      ready: () => actionAdaptersReady,
      execute: (input) => runActionExecute(input),
    },
    actionRequests: {
      create: async (input) => {
        if (!actionRequestPreflightReady) {
          throw new Error("action request preflight is not ready");
        }
        const request = await createActionRequest(
          input as Parameters<typeof createActionRequest>[0],
        );
        return { id: request.id, status: request.status };
      },
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
    groupGrants: {
      apply: async () => applyGroupGrants(await getOrgId()),
      schedule: () => {
        void getOrgId().then((orgId) => scheduleGroupGrantsApply(orgId));
      },
      enable: async (actorUserId) => enableGroupGrants(await getOrgId(), actorUserId),
      disable: async (actorUserId) => disableGroupGrants(await getOrgId(), actorUserId),
      apiOperations: async () => {
        const configFile = process.env.OPENNEKO_GRAPHJIN_CONFIG?.trim();
        if (!configFile) return [];
        const { readFile } = await import("node:fs/promises");
        const { listConfigApiOperations } = await import("@neko/llm/graphjin");
        return listConfigApiOperations(await readFile(configFile, "utf8"));
      },
    },
    directory: {
      status: async () => directoryStatus(signInService, await getOrgId()),
      sync: async () => runDirectorySync(signIn(), await getOrgId()),
      createUser: async (input) => createDirectoryUser(signIn(), input),
    },
    connect: {
      getConnectProviders: () => signInService?.getConnectProviders() ?? [],
      getOperatorConnectStatus: (operatorId) =>
        signInService?.getOperatorConnectStatus(operatorId) ?? [],
      getDeploymentConnectStatus: () =>
        signInService?.getDeploymentConnectStatus() ?? [],
      beginConnect: (name, params) => signIn().beginConnect(name, params),
      completeConnect: (name, params) => signIn().completeConnect(name, params),
      disconnect: (name, operatorId) => signIn().disconnect(name, operatorId),
    },
    ssoSetup: ssoSetupSurface(),
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
// Only now may the web process create action requests through this worker:
// every worker-owned preflight hook is registered and will finish before the
// request id is returned for an approval card.
actionRequestPreflightReady = true;
// ADM: execute approved chat-proposed user, group, channel and source admin changes.
{
  const {
    registerChannelAdminAdapter,
    registerDataSourceAdminAdapter,
    registerSourceConfigAdminAdapter,
    registerUserAdminAdapter,
    registerGroupAdminAdapter,
  } = await import("./admin-actions/adapters.js");
  registerUserAdminAdapter();
  registerGroupAdminAdapter((orgId) => scheduleGroupGrantsApply(orgId));
  registerChannelAdminAdapter();
  registerDataSourceAdminAdapter();
  registerSourceConfigAdminAdapter();
}
actionAdaptersReady = true;
console.log("[worker] action policies seeded and built-in adapters registered");

coreSecrets = new CoreSecrets();
await coreSecrets.start();
setCoreSecretsInstance(coreSecrets);
signInService = new SignInService(coreSecrets);
ssoSetupService = new SsoSetupService(signInService);
startSsoSetupPoller({ service: ssoSetupService, orgId: ADMIN_ORG_ID });
const concurrency = await resolveAgentConcurrency(ADMIN_ORG_ID);
// Every agent job on this host, from any queue, shares one cap.
setAgentSlotLimit(concurrency.globalCap);
console.log(
  `[worker] concurrency: globalCap=${concurrency.globalCap} (configure in /admin/settings/agent; restart required)`,
);

// The subscriptions GraphJin in openneko serve serves workflow outputs as
// its read-only subscriptions role. Compose sets
// OPENNEKO_GRAPHJIN_URL=http://openneko-serve:8089; `pnpm dev` reaches the
// port the dev stack publishes.
// Normalize any GraphJin base (origin or full) to the full GraphQL endpoint.
// The subscription/query clients treat baseUrl as the complete
// `/api/v1/graphql` URL and never append a path, so both the env-configured
// subscriptions URL (often just an origin) and the data_source URLs (already
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
console.log(`[worker] subscriptions GraphJin client targeting ${GRAPHJIN_URL}`);

if (process.env.OPENNEKO_SERVE_SOCKET) {
}
const b = await boss();

// Removing a retired or moved queue also drops its jobs and schedule.
// `openneko migrate` has already copied a moved queue's pending jobs to River.
for (const name of [...RETIRED_QUEUES, ...MOVED_QUEUES]) {
  try {
    await removeRetiredQueue(b, name);
  } catch (e) {
    console.warn(
      `[worker] could not remove retired queue ${name}: ${e instanceof Error ? e.message : e}`,
    );
  }
}

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


if (filesEnabled()) {
  moveLibraryDocumentsToStore()
    .then(({ moved, missing }) => {
      if (moved > 0 || missing > 0) {
        console.log(`[library-move] moved ${moved} document(s) into openneko serve; ${missing} failed`);
      }
    })
    .catch((error) => {
      console.warn(
        `[library-move] failed: ${error instanceof Error ? error.message : error}`,
      );
    });
}

listenAdmin(server, adminSocketPath(), PORT, (where) => {
  console.log(`[worker] pg-boss running; admin socket on ${where}`);
});


const shutdown = async (signal: string) => {
  console.log(`[worker] received ${signal}; shutting down`);
  coreSecrets?.stop();
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
  setCoreSecretsInstance(null);
  try {
    await b.stop({ graceful: true });
  } catch (e) {
    console.error("[worker] pg-boss stop error:", e);
  }
  await shutdownWorkerTelemetry();
  process.exit(0);
};
process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));
