// Isolated acceptance: an agent job grants its child only the server-side GraphJin agent.
import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, getOrgId, llm_provider_config, pool } from "@neko/db";
import { makeAgentBackend } from "@neko/llm";
import { ensureIsolatedJobWorkspace, sandboxAgentBackendForJob, shutdownAgentBroker } from "@neko/llm/work";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw Error("isolated M3 environment required");
}
const orgId = await getOrgId();
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
if (!prior) throw Error("isolated model configuration missing");
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
const isolated = await ensureIsolatedJobWorkspace("harness-child-check");
let disabled: Awaited<ReturnType<typeof ensureIsolatedJobWorkspace>> | undefined;
let modelOnly: Awaited<ReturnType<typeof ensureIsolatedJobWorkspace>> | undefined;
const runId = randomUUID();
try {
  await db().update(llm_provider_config).set({ model: "harness-job-child-fixture" }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, "model:\n  provider: custom\n  default: harness-job-child-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: "{}" });
  const backend = await sandboxAgentBackendForJob({ backend: makeAgentBackend({ id: "harness" }), orgId, runId,
    workspace: isolated.workspace, access: { graphjinAgent: true } });
  const result = await backend.run({ prompt: "Use one read-only child to verify the seeded reference." });
  assert.equal(result.status, "completed", JSON.stringify(result));
  assert.match(result.finalText, /REF-42/);
  const operations = (await pool().query("SELECT request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2", [orgId, runId])).rows;
  assert.equal(operations.length, 1);
  assert.equal(operations[0].request.instruction, "Find the seeded reference");
  assert.equal(operations[0].result.response.status, "answered");
  const snapshot = JSON.parse(await readFile(join(isolated.workspace.runRoot, ".harness",
    `${createHash("sha256").update(runId).digest("hex")}.json`), "utf8"));
  assert.equal(snapshot.events.filter((event: { type: string }) => event.type === "child.started").length, 1);
  assert.equal(snapshot.events.filter((event: { type: string }) => event.type === "model.request.finished").length, 6);
  console.log("M5_AGENT_JOB_CHILD_PASS", runId);

  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: "{}" });
  disabled = await ensureIsolatedJobWorkspace("harness-child-disabled-check");
  const disabledRunId = randomUUID();
  const disabledBackend = await sandboxAgentBackendForJob({ backend: makeAgentBackend({ id: "harness" }), orgId,
    runId: disabledRunId, workspace: disabled.workspace, access: { graphjinAgent: true } });
  const disabledResult = await disabledBackend.run({ prompt: "Delegate the seeded reference check.", nativeDelegation: "disabled" });
  assert.equal(disabledResult.status, "failed", JSON.stringify(disabledResult));
  assert.equal((await pool().query("SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2",
    [orgId, disabledRunId])).rows[0].n, 0);
  console.log("M5_AGENT_JOB_CHILD_DISABLED_PASS", disabledRunId);

  await db().update(llm_provider_config).set({ model: "harness-job-model-only-fixture" }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, "model:\n  provider: custom\n  default: harness-job-model-only-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: "{}" });
  modelOnly = await ensureIsolatedJobWorkspace("harness-model-only-check");
  const modelOnlyRunId = randomUUID();
  const noGrant = await sandboxAgentBackendForJob({ backend: makeAgentBackend({ id: "harness" }), orgId,
    runId: modelOnlyRunId, workspace: modelOnly.workspace, access: {} });
  const noGrantResult = await noGrant.run({ prompt: "Answer without using any tool." });
  assert.equal(noGrantResult.status, "completed", JSON.stringify(noGrantResult));
  assert.equal((await pool().query("SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2",
    [orgId, modelOnlyRunId])).rows[0].n, 0);
  console.log("M5_AGENT_JOB_MODEL_ONLY_PASS", modelOnlyRunId);
} finally {
  await shutdownAgentBroker();
  await isolated.cleanup();
  await disabled?.cleanup();
  await modelOnly?.cleanup();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  await pool().end();
}
