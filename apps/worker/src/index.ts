import { getOrgId, keepProcessThroughDatabaseRestarts, readLocalConfig, secretsBackendError } from "@neko/db";
import "dotenv/config";

import { createServer } from "node:http";
import { adminSocketPath, listenAdmin } from "./admin-listen.js";
import { boss, MOVED_QUEUES, RETIRED_QUEUES } from "@neko/db/jobs";
import { createAdminHandler } from "./admin-server.js";
import { CoreSecrets } from "./secrets/core-secrets.js";
import {
  cancelAllAgents,
  provisionHostConfig,
  resolveAgentConcurrency,
  verifyAiCredentials,
} from "@neko/llm";
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

let coreSecrets: CoreSecrets | null = null;
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

coreSecrets = new CoreSecrets();
await coreSecrets.start();
setCoreSecretsInstance(coreSecrets);
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
