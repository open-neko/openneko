// Paired M6 API short/artifact cases: independently verified output and inert redelivery.
import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, getOrgId, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { getOrgAgentRoot, shutdownAgentBroker } from "@neko/llm/work";
import { admitWorkflowApiRun, enableWorkflowApiAccess, getWorkflowApiArtifact, getWorkflowApiRunStatus, updateWorkflowApiLimits } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runWorkflowApiDispatcherTick } from "../src/workflow-api-dispatcher.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") throw Error("isolated M3 environment required");
const orgId = await getOrgId();
const queue = await boss();
const control = "http://127.0.0.1:18118/control";
const shortCase = process.env.HARNESS_BUDGET_SHORT_CASE === "1";
const model = shortCase ? "harness-budget-short-fixture" : "harness-compaction-output-fixture";
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
assert.ok(prior);
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
let workflowId: string | undefined;
try {
  assert.equal((await fetch(control, { method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({triage_choice: shortCase ? "short_answer" : "artifact_pipeline"}) })).status, 204);
  await db().update(llm_provider_config).set({ model }).where(eq(llm_provider_config.id, prior.id));
  await writeFile(configPath, `model:\n  provider: custom\n  default: ${model}\n  base_url: http://host.docker.internal:18118/v1\n`);
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [workflow] = await db().insert(workflow_definition).values({
    org_id: orgId, name: `Budget ${shortCase ? "short" : "artifact"} calibration`,
    goal: shortCase
      ? "Record one short finding and report its receipt. Never execute a change without approval."
      : "Read REF-42, create a CSV artifact, emit a file output, then verify the saved receipt. Never execute a change without approval.",
  }).returning({ id: workflow_definition.id });
  workflowId = workflow.id;
  const [org] = (await pool().query<{ solo_admin_user_id: string | null }>(
    "select solo_admin_user_id from organization where id=$1", [orgId])).rows;
  assert.ok(org);
  const actor = { userId: org.solo_admin_user_id, role: "admin" as const };
  const { token } = await enableWorkflowApiAccess({ orgId, workflowId, actor });
  await updateWorkflowApiLimits({ orgId, workflowId, actor,
    limits: { maxModelCalls: 48, maxTokensPerRun: 100_000, maxCostMicrosPerRun: 1_000_000 } });
  const clientFingerprint = `harness-budget-artifact-${orgId}`;
  const startedAt = Date.now();
  const admitted = await admitWorkflowApiRun({ workflowId, token,
    idempotencyKey: `budget-artifact-${randomUUID()}`, mode: "single",
    value: shortCase ? { question: "Record a short finding", constraint: "Never execute a change without approval" }
      : { reference: "REF-42", constraint: "Never execute a change without approval" }, clientFingerprint });
  assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
  let apiStatus: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
  for (let n = 0; n < 180; n++) {
    apiStatus = await getWorkflowApiRunStatus({ workflowId, runId: admitted.runId, token, clientFingerprint });
    if (["completed", "failed", "cancelled"].includes(apiStatus.status)) break;
    await new Promise(resolve => setTimeout(resolve, 500));
  }
  const [run] = (await pool().query<{ work_run_id: string; status: string; error: string | null }>(
    "select work_run_id,status,error from workflow_run where id=$1", [admitted.runId])).rows;
  assert.ok(run);
  const checkpointRoot = join(getOrgAgentRoot(orgId), "runs", run.work_run_id, ".harness");
  const checkpointBytes = await readFile(join(checkpointRoot, `${createHash("sha256").update(run.work_run_id).digest("hex")}.json`));
  const checkpoint = JSON.parse(checkpointBytes.toString("utf8")) as {
    spec?: { host_budget_mode?: string };
    result: { status: string; answer?: string; cost?: { charged_micros: number } };
    events: Array<{ type: string; stage?: string; data?: {profile?: string}; terminal?: { accepted: boolean } }>;
  };
  const counts = await (await fetch(control)).json() as Record<string, number>;
  const ordinaryCalls = shortCase ? counts[model] : counts[`ordinary:${model}`];
  const outputs = (await pool().query<{ id: string; kind: string; artifact_path: string | null }>(
    "select id,kind,artifact_path from workflow_output where workflow_run_id=$1", [admitted.runId])).rows;
  const expected = shortCase ? null : Buffer.from(`lead_id\n${"LEAD-42\n".repeat(6500)}`);
  const artifactPath = join(getOrgAgentRoot(orgId), "runs", run.work_run_id, "artifacts", "result.csv");
  const artifact = expected ? await readFile(artifactPath) : null;
  const [published] = (await pool().query<{ result_artifact_path: string | null; artifact_events: number }>(
    `select r.result_artifact_path,
      (select count(*)::int from work_run_event e where e.org_id=r.org_id and e.run_id=r.work_run_id and e.kind='artifact') as artifact_events
     from workflow_run r where r.id=$1`, [admitted.runId])).rows;
  console.log("M6_BUDGET_CASE_DIAGNOSTIC", JSON.stringify({taskClass: shortCase ? "short" : "artifact", status: apiStatus?.status, run,
    mode: checkpoint.spec?.host_budget_mode ?? "fixed", ordinary: ordinaryCalls,
    summaries: counts[`summary:${model}`], triage: counts["jev-fixture"], outputs: outputs.length,
    artifactBytes: artifact?.length ?? 0}));
  assert.equal(apiStatus?.status, "completed");
  assert.equal(run.status, "completed");
  assert.equal(checkpoint.result.status, "completed");
  assert.equal(checkpoint.spec?.host_budget_mode ?? "", process.env.OPENNEKO_HARNESS_BUDGET_CANARY === "1" ? "canary" : "");
  assert.match(checkpoint.result.answer ?? "", shortCase ? /short check/ : /REF-42/);
  assert.equal(ordinaryCalls, shortCase ? 3 : 9);
  assert.equal(counts[`summary:${model}`] ?? 0, shortCase ? 0 : 1);
  assert.equal(counts["jev-fixture"], 1);
  assert.equal(checkpoint.events.find(e => e.type === "budget.profile.proposed")?.data?.profile, shortCase ? "short" : "artifact");
  assert.equal(checkpoint.events.find(e => e.type === "terminal.checked")?.terminal?.accepted, true);
  if (shortCase) {
    assert.deepEqual(outputs.map(o => [o.kind, o.artifact_path]), [["finding", null]]);
    assert.equal(published.result_artifact_path, null);
    assert.equal(published.artifact_events, 0);
    await assert.rejects(getWorkflowApiArtifact({ workflowId, runId: admitted.runId, token, clientFingerprint }),
      /artifact is not available/);
    const operations = (await pool().query<{ request: {tool?: string} }>(
      "select request from harness_operation where org_id=$1 and run_id=$2", [orgId, run.work_run_id])).rows;
    assert.deepEqual(operations.map(o => o.request.tool), ["workflow_output"]);
  } else {
    assert.ok(expected && artifact);
    assert.equal(artifact.length, expected.length);
    assert.equal(createHash("sha256").update(artifact).digest("hex"), createHash("sha256").update(expected).digest("hex"));
    assert.deepEqual(outputs.map(o => [o.kind, o.artifact_path]), [["file", "result.csv"]]);
    assert.equal(published.result_artifact_path, `runs/${run.work_run_id}/artifacts/result.csv`);
    assert.equal(published.artifact_events, 1);
    const apiArtifact = await getWorkflowApiArtifact({ workflowId, runId: admitted.runId, token, clientFingerprint });
    assert.equal(apiArtifact.bytes, expected.length);
    assert.equal(apiArtifact.contentType, "text/csv; charset=utf-8");
    assert.equal(createHash("sha256").update(await readFile(apiArtifact.absolutePath)).digest("hex"),
      createHash("sha256").update(expected).digest("hex"));
  }
  if (process.env.HARNESS_M6_PUBLIC_HTTP_BASE) {
    const url = `${process.env.HARNESS_M6_PUBLIC_HTTP_BASE}/api/v1/workflows/${workflowId}/runs/${admitted.runId}/artifact`;
    const response = await fetch(url, { headers: { authorization: `Bearer ${token}` } });
    if (shortCase) {
      assert.equal(response.status, 404);
      assert.equal((await response.json() as { error: { code: string } }).error.code, "artifact_not_ready");
    } else {
      assert.ok(expected);
      assert.equal(response.status, 200);
      assert.equal(response.headers.get("content-type"), "text/csv; charset=utf-8");
      assert.equal(response.headers.get("content-length"), String(expected.length));
      assert.equal(response.headers.get("content-disposition"), `attachment; filename="workflow-${admitted.runId}.csv"`);
      assert.equal(response.headers.get("cache-control"), "no-store, private");
      assert.equal(createHash("sha256").update(Buffer.from(await response.arrayBuffer())).digest("hex"),
        createHash("sha256").update(expected).digest("hex"));
    }
    const denied = await fetch(url, { headers: { authorization: "Bearer invalid-synthetic-token" } });
    assert.equal(denied.status, 401);
    assert.equal((await denied.json() as { error: { code: string } }).error.code, "invalid_credentials");
  }
  const [admission] = (await pool().query<{ id: string; attempts: number }>(
    "select id,attempts from workflow_api_admission where workflow_run_id=$1", [admitted.runId])).rows;
  await runWorkflowRunFire({ orgId, workflowId, triggerKind: "api", apiAdmissionId: admission.id,
    workflowRunId: admitted.runId, workRunId: run.work_run_id, queueAttempt: admission.attempts });
  assert.deepEqual(await (await fetch(control)).json(), counts, "API queue redelivery repeated model work");
  assert.equal((await pool().query("select count(*)::int as n from workflow_output where workflow_run_id=$1", [admitted.runId])).rows[0].n, 1);
  if (expected) assert.equal(createHash("sha256").update(await readFile(artifactPath)).digest("hex"),
    createHash("sha256").update(expected).digest("hex"));
  if (process.env.HARNESS_BUDGET_COMPARISON_REPORT) {
    await writeFile(process.env.HARNESS_BUDGET_COMPARISON_REPORT, JSON.stringify({
      mode: checkpoint.spec?.host_budget_mode === "canary" ? "canary" : "fixed",
      verified: true, checkpointRoot, runId: run.work_run_id,
      checkpointSha256: createHash("sha256").update(checkpointBytes).digest("hex"),
      modelCalls: checkpoint.events.filter(e => e.type === "model.request.started").length,
      ordinaryCalls, triageCalls: counts["jev-fixture"],
      chargedMicros: checkpoint.result.cost?.charged_micros ?? null, wallMS: Date.now() - startedAt,
    }));
  }
  console.log(shortCase ? "M6_BUDGET_SHORT_PASS" : "M6_BUDGET_ARTIFACT_PASS", run.work_run_id);
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  if (workflowId) await db().delete(workflow_definition).where(eq(workflow_definition.id, workflowId));
  await pool().end();
}
