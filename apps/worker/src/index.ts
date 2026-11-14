import { getOrgId, keepProcessThroughDatabaseRestarts, readLocalConfig, secretsBackendError } from "@neko/db";
import "dotenv/config";

import { createServer } from "node:http";
import { adminSocketPath, listenAdmin } from "./admin-listen.js";
import { boss, MOVED_QUEUES, RETIRED_QUEUES } from "@neko/db/jobs";
import { createAdminHandler } from "./admin-server.js";
import { CoreSecrets } from "./secrets/core-secrets.js";
import { SignInService } from "./signin/signin-service.js";
import {
  applyGroupGrants,
  disableGroupGrants,
  enableGroupGrants,
  scheduleGroupGrantsApply,
} from "./graphjin/group-grants-service.js";
import {
  cancelAllAgents,
  provisionHostConfig,
  resolveAgentConcurrency,
  verifyAiCredentials,
} from "@neko/llm";
import {
  createActionRequest,
  dispatchExternalEvent,
  registerActionRequestCreatedHook,
  seedDefaultActionPolicies,
} from "@neko/llm/workflows";
import { reportDeploymentProfile, setAgentSlotLimit } from "@neko/llm/work";
import { removeRetiredQueue } from "./pg-boss-helpers.js";
import { setCoreSecretsInstance } from "./secrets/instance.js";
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
let signInService: SignInService | null = null;
function signIn(): SignInService {
  if (!signInService) throw new Error("sign-in service not ready");
  return signInService;
}
let actionRequestPreflightReady = false;
const server = createServer(
  createAdminHandler({
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
// Only now may the web process create action requests through this worker:
// every worker-owned preflight hook is registered and will finish before the
// request id is returned for an approval card.
actionRequestPreflightReady = true;
console.log("[worker] action policies seeded");

coreSecrets = new CoreSecrets();
await coreSecrets.start();
setCoreSecretsInstance(coreSecrets);
signInService = new SignInService(coreSecrets);
const concurrency = await resolveAgentConcurrency(ADMIN_ORG_ID);
// Every agent job on this host, from any queue, shares one cap.
setAgentSlotLimit(concurrency.globalCap);
console.log(
  `[worker] concurrency: globalCap=${concurrency.globalCap} (configure in /admin/settings/agent; restart required)`,
);

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
