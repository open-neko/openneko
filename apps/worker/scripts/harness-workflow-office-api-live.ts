// Queued workflow API -> Harness/OpenShell process -> real Office package -> HTTP download.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";
import { db, eq, getOrgId, llm_provider_config, pool, workflow_definition } from "@neko/db";
import { boss, QUEUE, type WorkflowRunFirePayload } from "@neko/db/jobs";
import { getOrgAgentRoot, shutdownAgentBroker } from "@neko/llm/work";
import { admitWorkflowApiRun, enableWorkflowApiAccess, getWorkflowApiRunStatus, updateWorkflowApiLimits } from "@neko/llm/workflows";
import { runWorkflowRunFire } from "../src/jobs/workflow-run-fire.js";
import { runWorkflowApiDispatcherTick } from "../src/workflow-api-dispatcher.js";

if (process.env.HARNESS_M3_LIVE !== "1" || process.env.NEKO_PG_PORT !== "18119" ||
    !process.env.HARNESS_M6_PUBLIC_HTTP_BASE || !process.env.OPENNEKO_HARNESS_ROUTING) {
  throw Error("isolated M6 workflow Office API environment required");
}

const orgId = await getOrgId();
const queue = await boss();
const [prior] = await db().select().from(llm_provider_config).where(eq(llm_provider_config.org_id, orgId));
assert.ok(prior);
const configPath = join(process.env.OPENNEKO_AGENT_HERMES_HOME ?? "", "config.yaml");
const priorConfig = await readFile(configPath, "utf8");
const runCommand = promisify(execFile);
const workflows: string[] = [];
const base = process.env.HARNESS_M6_PUBLIC_HTTP_BASE;

const validate = `import sys,zipfile,xml.etree.ElementTree as ET
with zipfile.ZipFile(sys.argv[1]) as pkg:
 assert pkg.testzip() is None
 assert '[Content_Types].xml' in pkg.namelist() and '_rels/.rels' in pkg.namelist()
 name='xl/worksheets/sheet1.xml' if sys.argv[1].endswith('.xlsx') else 'word/document.xml'
 root=ET.fromstring(pkg.read(name))
 assert 'LEAD-42' in ''.join(root.itertext())
`;

try {
  await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
  await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async jobs => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
  });
  const [org] = (await pool().query<{ solo_admin_user_id: string | null }>(
    "select solo_admin_user_id from organization where id=$1", [orgId])).rows;
  assert.ok(org);
  const actor = { userId: org.solo_admin_user_id, role: "admin" as const };

  for (const [name, mime] of [
    ["leads.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"],
    ["summary.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"],
  ] as const) {
    const model = "harness-workflow-office-fixture";
    assert.equal((await fetch("http://127.0.0.1:18118/control", { method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ office_output: name.endsWith("xlsx") ? "xlsx" : "docx" }),
    })).status, 204);
    await db().update(llm_provider_config).set({ model }).where(eq(llm_provider_config.id, prior.id));
    await writeFile(configPath, `model:\n  provider: custom\n  default: ${model}\n  base_url: http://host.docker.internal:18118/v1\n`);

    const [workflow] = await db().insert(workflow_definition).values({
      org_id: orgId, name: `Office API ${name}`,
      goal: `Create ${name} as a file output from the synthetic LEAD-42 reference. No external effects.`,
    }).returning({ id: workflow_definition.id });
    workflows.push(workflow.id);
    const { token } = await enableWorkflowApiAccess({ orgId, workflowId: workflow.id, actor });
    await updateWorkflowApiLimits({ orgId, workflowId: workflow.id, actor,
      limits: { maxModelCalls: 48, maxTokensPerRun: 100_000, maxCostMicrosPerRun: 1_000_000 } });
    const fingerprint = `office-api-${name}-${orgId}`;
    const admitted = await admitWorkflowApiRun({ workflowId: workflow.id, token,
      idempotencyKey: `office-api-${name}`, mode: "single", value: { requested: name },
      clientFingerprint: fingerprint });
    assert.equal((await runWorkflowApiDispatcherTick()).dispatched, 1);
    let status: Awaited<ReturnType<typeof getWorkflowApiRunStatus>> | undefined;
    for (let attempt = 0; attempt < 180; attempt++) {
      status = await getWorkflowApiRunStatus({ workflowId: workflow.id, runId: admitted.runId,
        token, clientFingerprint: fingerprint });
      if (["completed", "failed", "cancelled"].includes(status.status)) break;
      await new Promise(resolve => setTimeout(resolve, 500));
    }
    const [run] = (await pool().query<{ status: string; error: string | null;
      work_run_id: string; result_artifact_path: string | null }>(
      "select status,error,work_run_id,result_artifact_path from workflow_run where id=$1", [admitted.runId])).rows;
    assert.ok(run);
    if (status?.status !== "completed") {
      const operations = (await pool().query<{tool: string; ok: boolean | null}>(
        "select request->>'tool' as tool,(result->>'ok')::boolean as ok from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
        [orgId, run.work_run_id])).rows;
      const [work] = (await pool().query<{ error: string | null }>(
        "select error from work_run where org_id=$1 and id=$2", [orgId, run.work_run_id])).rows;
      console.error("M6_WORKFLOW_OFFICE_DIAGNOSTIC", JSON.stringify({ name, status: status?.status,
        error: status?.error?.code ?? run.error, workError: work?.error, operations }));
    }
    assert.equal(status?.status, "completed", run.error ?? "workflow API did not finish");
    assert.equal(run.status, "completed");
    if (!run.result_artifact_path) {
      const diagnostic = await pool().query<{
        operation_id: number; tool: string; ok: boolean | null; error: string | null;
      }>(`select operation_id,request->>'tool' as tool,(result->>'ok')::boolean as ok,
          left(coalesce(result->>'error',''),180) as error
         from harness_operation where org_id=$1 and run_id=$2 order by operation_id`, [orgId, run.work_run_id]);
      const outputs = await pool().query<{ kind: string; artifact_path: string | null }>(
        "select kind,artifact_path from workflow_output where workflow_run_id=$1", [admitted.runId]);
      const events = await pool().query<{ path: string | null }>(
        "select payload->'artifact'->>'path' as path from work_run_event where org_id=$1 and run_id=$2 and kind='artifact'",
        [orgId, run.work_run_id]);
      console.error("M6_WORKFLOW_OFFICE_DIAGNOSTIC", JSON.stringify({ name, operations: diagnostic.rows,
        outputs: outputs.rows, artifactEvents: events.rows }));
    }
    assert.equal(run.result_artifact_path, `runs/${run.work_run_id}/artifacts/process-1/${name}`);
    const path = join(getOrgAgentRoot(orgId), "runs", run.work_run_id, "artifacts", "process-1", name);
    const bytes = await readFile(path);
    await runCommand("python3", ["-c", validate, path], { timeout: 10_000 });
    const outputs = (await pool().query<{kind: string; artifact_path: string | null}>(
      "select kind,artifact_path from workflow_output where workflow_run_id=$1", [admitted.runId])).rows;
    assert.deepEqual(outputs, [{ kind: "file", artifact_path: `process-1/${name}` }]);
    const operations = (await pool().query<{tool: string}>(
      "select request->>'tool' as tool from harness_operation where org_id=$1 and run_id=$2 order by operation_id",
      [orgId, run.work_run_id])).rows;
    assert.deepEqual(operations.map(operation => operation.tool), ["process_run", "workflow_output"]);

    const publicUrl = `${base}/api/v1/workflows/${workflow.id}/runs/${admitted.runId}/artifact`;
    const operatorUrl = `${base}/api/workflow-runs/${admitted.runId}/artifact`;
    for (const [surface, url, headers] of [
      ["public", publicUrl, { authorization: `Bearer ${token}` }],
      ["operator", operatorUrl, {}],
    ] as const) {
      const response = await fetch(url, { headers });
      assert.equal(response.status, 200, `${surface} ${name}`);
      assert.equal(response.headers.get("content-type"), mime);
      assert.equal(response.headers.get("content-length"), String(bytes.length));
      assert.equal(response.headers.get("content-disposition"),
        `attachment; filename="workflow-${admitted.runId}.${name.split(".").at(-1)}"`);
      assert.deepEqual(Buffer.from(await response.arrayBuffer()), bytes);
    }
    const [admission] = (await pool().query<{ id: string; attempts: number }>(
      "select id,attempts from workflow_api_admission where workflow_run_id=$1", [admitted.runId])).rows;
    await runWorkflowRunFire({ orgId, workflowId: workflow.id, triggerKind: "api",
      apiAdmissionId: admission.id, workflowRunId: admitted.runId,
      workRunId: run.work_run_id, queueAttempt: admission.attempts });
    assert.equal((await pool().query("select count(*)::int as n from workflow_output where workflow_run_id=$1",
      [admitted.runId])).rows[0].n, 1);
    console.log("M6_WORKFLOW_OFFICE_API_PASS", JSON.stringify({ kind: name, bytes: bytes.length, runId: admitted.runId }));
  }
} finally {
  await queue.stop({ graceful: true, timeout: 5_000 });
  await shutdownAgentBroker();
  await writeFile(configPath, priorConfig);
  await db().update(llm_provider_config).set({ model: prior.model }).where(eq(llm_provider_config.id, prior.id));
  for (const workflowId of workflows) await db().delete(workflow_definition).where(eq(workflow_definition.id, workflowId));
  await pool().end();
}
