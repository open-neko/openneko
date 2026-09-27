// Isolated acceptance: an agent job grants its child only the server-side GraphJin agent.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
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
let interrupted: Awaited<ReturnType<typeof ensureIsolatedJobWorkspace>> | undefined;
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

  await db().update(llm_provider_config).set({ model: "harness-job-child-crash-fixture" }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, "model:\n  provider: custom\n  default: harness-job-child-crash-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: JSON.stringify({ pause_responder: true }) });
  interrupted = await ensureIsolatedJobWorkspace("harness-child-recovery-check");
  const interruptedRunId = randomUUID();
  const interruptedBackend = await sandboxAgentBackendForJob({ backend: makeAgentBackend({ id: "harness" }), orgId,
    runId: interruptedRunId, workspace: interrupted.workspace, access: { graphjinAgent: true } });
  const firstAttempt = interruptedBackend.run({ prompt: "Use one read-only child to verify the seeded reference." }).then(
    value => value, error => error);
  let paused = false;
  for (let n = 0; n < 80; n++) {
    const count = (await (await fetch("http://127.0.0.1:18118/control")).json())["harness-job-child-crash-fixture"] ?? 0;
    if (count === 5) { paused = true; break; }
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  assert.ok(paused, "child responder did not pause after its read");
  const firstOperations = (await pool().query("SELECT operation_id,request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2",
    [orgId, interruptedRunId])).rows;
  assert.equal(firstOperations.length, 1);
  assert.ok(firstOperations[0].result);
  const graphjinBefore = (await (await fetch("http://127.0.0.1:18118/control")).json())["graphjin-fixture"];
  const sandboxName = `h-${createHash("sha256").update(interruptedRunId).digest("hex").slice(0, 16)}`;
  const containers = execFileSync("docker", ["ps", "--filter", `label=openshell.ai/sandbox-name=${sandboxName}`,
    "--filter", "network=harness-m2", "--format", "{{.ID}}"], { encoding: "utf8" }).trim().split("\n").filter(Boolean);
  assert.equal(containers.length, 1);
  execFileSync("docker", ["exec", "--user", "0", containers[0], "/bin/sh", "-c",
    'killed=0; for comm in /proc/[0-9]*/comm; do read -r name < "$comm" || continue; case "$name" in harness-opennek|harness-openneko) pid=${comm#/proc/}; pid=${pid%/comm}; kill -KILL "$pid" || exit 1; killed=1;; esac; done; test "$killed" = 1'], { timeout: 15_000 });
  assert.ok((await firstAttempt) instanceof Error, "child process survived the injected crash");
  await fetch("http://127.0.0.1:18118/control", { method: "POST", body: JSON.stringify({ continue: true }) });
  const recovered = await interruptedBackend.run({ prompt: "Use one read-only child to verify the seeded reference." });
  assert.equal(recovered.status, "completed", JSON.stringify(recovered));
  assert.match(recovered.finalText, /REF-42/);
  assert.deepEqual((await pool().query("SELECT operation_id,request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2",
    [orgId, interruptedRunId])).rows, firstOperations);
  const afterRecovery = await (await fetch("http://127.0.0.1:18118/control")).json();
  assert.equal(afterRecovery["graphjin-fixture"], graphjinBefore);
  const recoveredSnapshot = JSON.parse(await readFile(join(interrupted.workspace.runRoot, ".harness",
    `${createHash("sha256").update(interruptedRunId).digest("hex")}.json`), "utf8"));
  assert.equal(recoveredSnapshot.events.filter((event: { type: string }) => event.type === "run.resumed").length, 1);
  assert.equal(recoveredSnapshot.events.filter((event: { type: string; name?: string }) => event.type === "tool.reused" && event.name === "lookup").length, 1);
  console.log("M5_AGENT_JOB_CHILD_RECOVERY_PASS", interruptedRunId);

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
  await interrupted?.cleanup();
  await modelOnly?.cleanup();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  await pool().end();
}
