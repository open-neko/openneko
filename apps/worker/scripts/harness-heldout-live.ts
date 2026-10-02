// Submit one frozen M6 case through the production queue; retain a local review receipt.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { copyFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, isAbsolute, join, relative } from "node:path";
import { promisify } from "node:util";
import { db, eq, getOrgId, getOrCreateSoloAdmin, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { getOrgAgentRoot, shutdownAgentBroker } from "@neko/llm/work";
import { admitWorkflowApiRun, enableWorkflowApiAccess, getWorkflowApiArtifact,
  getWorkflowApiRunStatus, updateWorkflowApiLimits } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runWorkflowApiDispatcherTick } from "../src/workflow-api-dispatcher.js";

type Case = { id: string; prompt: string; acceptance: { kind: string } };
type Cases = { version: number; dataset: string; cases: Case[] };
const execFileAsync = promisify(execFile);
const frozenCasesSha256 = "7edca56ef2e16aa175e9b880b314112370331c8708dad221cb1250db1d57156e";

function requiredPath(name: string): string {
  const value = process.env[name];
  if (!value || !isAbsolute(value)) throw new Error(`${name} must be an absolute path`);
  return value;
}

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119") {
  throw new Error("isolated M6 environment required");
}
if (process.env.OPENNEKO_HARNESS_TRIAGE_SHADOW !== "1") {
  throw new Error("held-out evaluation requires shadow triage in both modes");
}
const mode = process.env.HARNESS_M6_MODE;
if (mode !== "fixed" && mode !== "canary") throw new Error("HARNESS_M6_MODE must be fixed or canary");
if ((process.env.OPENNEKO_HARNESS_BUDGET_CANARY === "1") !== (mode === "canary")) {
  throw new Error("Harness budget mode differs from requested mode");
}
const caseID = process.env.HARNESS_M6_CASE_ID;
const caseBytes = await readFile(requiredPath("HARNESS_M6_CASES_FILE"));
if (createHash("sha256").update(caseBytes).digest("hex") !== frozenCasesSha256) {
  throw new Error("held-out case prompts differ from the frozen version");
}
const cases = JSON.parse(caseBytes.toString("utf8")) as Cases;
if (cases.version !== 1 || cases.dataset !== "daily-lead-union-v1" || !Array.isArray(cases.cases)) {
  throw new Error("unexpected held-out case set");
}
const selected = cases.cases.find(item => item.id === caseID);
if (!selected || !selected.prompt || !["answer_fact", "answer_facts", "csv_oracle"].includes(selected.acceptance?.kind)) {
  throw new Error("unknown or invalid held-out case");
}
const reportPath = requiredPath("HARNESS_M6_RUN_REPORT");
const outputDir = dirname(reportPath);
const agentHome = requiredPath("OPENNEKO_AGENT_HOME");
if (process.env.OPENNEKO_HOST_WEB_DEV !== "1" || process.env.NODE_ENV !== "development") {
  throw new Error("held-out agent home requires isolated development-mode scoping");
}
await mkdir(outputDir, { recursive: true, mode: 0o700 });
const attestationArgs = [requiredPath("HARNESS_M6_ATTEST_SCRIPT"),
  "--url", process.env.HARNESS_M6_GJ_STATUS_URL ?? "",
  "--provider", process.env.HARNESS_M6_GJ_PROVIDER ?? "",
  "--model", process.env.HARNESS_M6_GJ_MODEL ?? "",
  "--reasoning", process.env.HARNESS_M6_GJ_REASONING ?? ""];
if (process.env.HARNESS_M6_GJ_STATUS_TOKEN_ENV) {
  attestationArgs.push("--token-env", process.env.HARNESS_M6_GJ_STATUS_TOKEN_ENV);
}
// This checks the effective server profile and frozen Postgres oracle before
// the workflow can be admitted; model-supplied configuration is never trusted.
const { stdout: attestationJSON } = await execFileAsync("python3", attestationArgs,
  { timeout: 30_000, maxBuffer: 8192 });
const graphjinEnvironment = JSON.parse(attestationJSON) as Record<string, string>;
assert.equal(graphjinEnvironment.provider, process.env.HARNESS_M6_GJ_PROVIDER);
assert.equal(graphjinEnvironment.model, process.env.HARNESS_M6_GJ_MODEL);
assert.equal(graphjinEnvironment.reasoning, process.env.HARNESS_M6_GJ_REASONING);

const orgId = await getOrgId();
const orgRoot = getOrgAgentRoot(orgId);
if (relative(agentHome, orgRoot).startsWith("..") || !relative(agentHome, orgRoot)) {
  throw new Error("held-out agent root escaped the isolated output directory");
}
const modelName = process.env.HARNESS_M6_MODEL_NAME;
if (!modelName) throw new Error("HARNESS_M6_MODEL_NAME is required");
const queue = await boss();
const startedAt = Date.now();
let workflowId: string | undefined;
try {
  const [provider] = await db().select().from(llm_provider_config)
    .where(eq(llm_provider_config.org_id, orgId));
  assert.ok(provider, "isolated organization has no model configuration");
  await db().update(llm_provider_config).set({ model: modelName })
    .where(eq(llm_provider_config.id, provider.id));
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [workflow] = await db().insert(workflow_definition).values({
    org_id: orgId, name: `M6 held-out ${selected.id}`, goal: selected.prompt,
  }).returning({ id: workflow_definition.id });
  workflowId = workflow.id;
  const soloAdmin = await getOrCreateSoloAdmin(orgId);
  assert.ok(soloAdmin, "isolated organization has no active solo admin");
  const actor = { userId: soloAdmin.id, role: "admin" as const };
  const { token } = await enableWorkflowApiAccess({ orgId, workflowId, actor });
  await updateWorkflowApiLimits({ orgId, workflowId, actor,
    limits: { maxModelCalls: 64, maxTokensPerRun: 250_000,
      maxCostMicrosPerRun: 5_000_000, maxArtifactBytes: 16 << 20 } });
  const clientFingerprint = `harness-m6-heldout-${orgId}`;
  const admitted = await admitWorkflowApiRun({ workflowId, token,
    idempotencyKey: `m6-${selected.id}-${randomUUID()}`, mode: "single",
    value: { task: selected.id }, clientFingerprint });
  assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
  let status: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
  for (let attempt = 0; attempt < 900; attempt++) {
    status = await getWorkflowApiRunStatus({ workflowId, runId: admitted.runId, token, clientFingerprint });
    if (["completed", "failed", "cancelled"].includes(status.status)) break;
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
  if (!status || !["completed", "failed", "cancelled"].includes(status.status)) {
    throw new Error("held-out workflow did not reach a terminal API state within 15 minutes");
  }
  const [run] = (await pool().query<{ work_run_id: string }>(
    "select work_run_id from workflow_run where id=$1", [admitted.runId])).rows;
  assert.ok(run?.work_run_id);
  const workRunId = run.work_run_id;
  const checkpointRoot = join(orgRoot, "runs", workRunId, ".harness");
  const checkpointPath = join(checkpointRoot,
    `${createHash("sha256").update(workRunId).digest("hex")}.json`);
  const checkpointBytes = await readFile(checkpointPath).catch(async (error: unknown) => {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    await writeFile(reportPath, JSON.stringify({
      version: 1, dataset: cases.dataset, cases_sha256: frozenCasesSha256,
      case_id: selected.id, mode, workflow_run_id: admitted.runId, run_id: workRunId,
      api_status: status.status, api_error_code: status.error?.code ?? null,
      checkpoint_missing: true, graphjin_environment: graphjinEnvironment,
      wall_ms: Date.now() - startedAt,
    }, null, 2) + "\n", { mode: 0o600, flag: "wx" });
    throw new Error(`terminal ${status.status} workflow has no Harness checkpoint; see ${reportPath}`);
  });
  const checkpoint = JSON.parse(checkpointBytes.toString("utf8")) as {
    result?: { status?: string; code?: string; answer?: string }; spec?: { host_budget_mode?: string };
  };
  assert.equal(checkpoint.spec?.host_budget_mode ?? "fixed", mode);
  let artifactPath: string | undefined;
  if (status.artifact) {
    const artifact = await getWorkflowApiArtifact({ workflowId, runId: admitted.runId, token, clientFingerprint });
    artifactPath = join(outputDir, `${selected.id}-${mode}-${workRunId}.csv`);
    await copyFile(artifact.absolutePath, artifactPath);
  }
  let artifactVerified: boolean | null = null;
  if (selected.acceptance.kind === "csv_oracle") {
    artifactVerified = false;
    if (artifactPath) {
      try {
        const { stdout } = await execFileAsync("python3",
          [requiredPath("HARNESS_M6_VERIFY_SCRIPT"), "--artifact", artifactPath],
          { timeout: 30_000, maxBuffer: 8192 });
        const verification = JSON.parse(stdout) as { artifact_verified?: boolean; data_snapshot_sha256?: string };
        artifactVerified = verification.artifact_verified === true &&
          verification.data_snapshot_sha256 === graphjinEnvironment.data_snapshot_sha256;
      } catch {
        artifactVerified = false;
      }
    }
  }
  const receipt = {
    version: 1, dataset: cases.dataset, cases_sha256: frozenCasesSha256,
    case_id: selected.id, mode,
    workflow_run_id: admitted.runId, run_id: workRunId, api_status: status.status,
    api_error_code: status.error?.code ?? null,
    checkpoint_status: checkpoint.result?.status ?? null,
    checkpoint_code: checkpoint.result?.code ?? null, root: checkpointRoot,
    checkpoint_sha256: createHash("sha256").update(checkpointBytes).digest("hex"),
    graphjin_environment: graphjinEnvironment,
    wall_ms: Date.now() - startedAt, artifact_path: artifactPath ?? null,
    artifact_verified: artifactVerified,
    // Local review material. The comparison manifest must contain outcome only.
    answer: checkpoint.result?.answer ?? null,
  };
  await writeFile(reportPath, JSON.stringify(receipt, null, 2) + "\n", { mode: 0o600, flag: "wx" });
  console.log("M6_HELDOUT_RUN_RECORDED", JSON.stringify({ case_id: selected.id, mode,
    api_status: status.status, run_id: workRunId, report: reportPath }));
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  await shutdownAgentBroker();
  if (workflowId) await db().delete(workflow_definition).where(eq(workflow_definition.id, workflowId));
  await pool().end();
}
