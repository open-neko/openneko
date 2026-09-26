import { randomUUID } from "node:crypto";
import { chmod, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, organization, work_run, work_run_event, work_thread, workflow_definition, workflow_run } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import { ensureWorkWorkspace, shutdownAgentBroker } from "@neko/llm/work";
import { admitWorkflowApiRun, enableWorkflowApiAccess, getWorkflowApiRunStatus,
  leasePendingWorkflowApiAdmissions, markWorkflowApiAdmissionEnqueued } from "@neko/llm/workflows";
import { expect, it } from "vitest";
import { runHarnessBatch } from "../../src/jobs/harness-batch";
import { runWorkflowRunFire } from "../../src/jobs/workflow-run-fire";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("publishes one host-owned batch artifact without provider credentials", async () => {
  if (process.env.NEKO_PG_PORT !== "18119" || !process.env.HARNESS_STATE) {
    throw new Error("isolated M3 environment required");
  }
  const orgId = `harness-batch-${randomUUID()}`;
  const threadId = randomUUID();
  const runId = randomUUID();
  const root = join(process.env.HARNESS_STATE, "batch-worker", runId);
  const envNames = ["OPENNEKO_AGENT_HOME", "OPENNEKO_HARNESS_BATCH_BIN", "HARNESS_OPENSHELL_BIN", "OPENSHELL_GATEWAY",
    "HARNESS_BATCH_IMAGE", "HARNESS_BATCH_SCRIPT", "HARNESS_BATCH_SCRIPT_SHA256", "HARNESS_BATCH_BUNDLE_DIR",
    "HARNESS_BATCH_BUNDLE_SHA256", "HARNESS_BATCH_WORKFLOW_ID", "MODEL_API_KEY"] as const;
  const prior = Object.fromEntries(envNames.map((name) => [name, process.env[name]]));
  await mkdir(root, { recursive: true });
  const fake = join(root, "batch-fixture");
  await writeFile(fake, `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
if (process.env.MODEL_API_KEY || !process.env.OPENNEKO_BROKER_TOKEN) process.exit(3);
const artifact = path.join(process.env.HARNESS_BATCH_ARTIFACT_DIR, process.env.HARNESS_BATCH_ARTIFACT_NAME);
const bytes = Buffer.from('lead_id\\nLEAD-42\\n');
fs.writeFileSync(artifact, bytes);
process.stdout.write(JSON.stringify({artifact,sha256:crypto.createHash('sha256').update(bytes).digest('hex'),rows:1,queries:1}));
`);
  await chmod(fake, 0o700);
  Object.assign(process.env, {
    OPENNEKO_AGENT_HOME: join(process.env.HARNESS_STATE, "agent"), OPENNEKO_HARNESS_BATCH_BIN: fake,
    HARNESS_OPENSHELL_BIN: fake, OPENSHELL_GATEWAY: "fixture", HARNESS_BATCH_IMAGE: "fixture",
    HARNESS_BATCH_SCRIPT: fake, HARNESS_BATCH_SCRIPT_SHA256: "fixture",
    HARNESS_BATCH_BUNDLE_DIR: root, HARNESS_BATCH_BUNDLE_SHA256: "fixture", MODEL_API_KEY: "host-only-secret",
  });
  await db().insert(organization).values({ id: orgId, name: "Harness batch acceptance" });
  await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Batch" });
  await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
  const contract = { version: 1, executor: "query-to-file", artifactName: "leads.csv", columns: ["lead_id"] };
  const [workflow] = await db().insert(workflow_definition).values({ id: process.env.HARNESS_M3_WORKFLOW_ID ?? randomUUID(),
    org_id: orgId, name: "Fixture batch workflow",
    output_contract: { harnessBatch: contract } }).returning({ id: workflow_definition.id });
  process.env.HARNESS_BATCH_WORKFLOW_ID = workflow.id;
  const [workflowRun] = await db().insert(workflow_run).values({ org_id: orgId, workflow_id: workflow.id,
    thread_id: threadId, work_run_id: runId, trigger_kind: "manual", trigger_payload: { targetDay: "2026-09-15" },
    executor_contract: contract, status: "running" }).returning({ id: workflow_run.id });
  try {
    const payload = { orgId, threadId, runId, workflowRunId: workflowRun.id };
    await db().update(workflow_definition).set({ enabled: false }).where(eq(workflow_definition.id, workflow.id));
    await expect(runHarnessBatch(payload)).rejects.toThrow("workflow binding is invalid");
    await db().update(workflow_definition).set({ enabled: true }).where(eq(workflow_definition.id, workflow.id));
    await db().update(workflow_definition).set({ output_contract: { harnessBatch: { ...contract, artifactName: "changed.csv", columns: ["changed"] } } }).where(eq(workflow_definition.id, workflow.id));
    await db().update(workflow_definition).set({ name: "Renamed while queued" }).where(eq(workflow_definition.id, workflow.id));
    await runHarnessBatch(payload);
    await runHarnessBatch(payload);
    const [run] = await db().select({ status: work_run.status }).from(work_run).where(eq(work_run.id, runId));
    expect(run.status).toBe("completed");
    const [finishedWorkflow] = await db().select({ status: workflow_run.status, progress: workflow_run.progress,
      result: workflow_run.terminal_result,
      attempts: workflow_run.queue_attempts }).from(workflow_run).where(eq(workflow_run.id, workflowRun.id));
    expect(finishedWorkflow.status).toBe("completed");
    expect(finishedWorkflow.attempts).toBe(1);
    expect(finishedWorkflow.progress).toMatchObject({ stage: "completed", rows: 1, queries: 1, artifactBytes: 16 });
    expect(finishedWorkflow.result).toMatchObject({ kind: "csv", rows: 1, queries: 1, columns: ["lead_id"] });
    const events = await db().select({ kind: work_run_event.kind, payload: work_run_event.payload })
      .from(work_run_event).where(eq(work_run_event.run_id, runId));
    expect(events.filter((event) => event.kind === "artifact")).toHaveLength(1);
    expect(events.filter((event) => event.kind === "message")).toHaveLength(1);
    expect(events.filter((event) => event.kind === "done")).toHaveLength(1);
    const workspace = await ensureWorkWorkspace(orgId, threadId, runId);
    expect(await readFile(join(workspace.artifactRoot, "leads.csv"), "utf8")).toBe("lead_id\nLEAD-42\n");

    await db().update(workflow_definition).set({ output_contract: { harnessBatch: contract } }).where(eq(workflow_definition.id, workflow.id));
    const { token } = await enableWorkflowApiAccess({ orgId, workflowId: workflow.id, actor: { userId: null, role: "admin" } });
    const admitted = await admitWorkflowApiRun({ workflowId: workflow.id, token, idempotencyKey: "batch-api-acceptance",
      mode: "single", value: { targetDay: "2026-09-15" }, clientFingerprint: `fixture-${orgId}` });
    const [leased] = await leasePendingWorkflowApiAdmissions();
    expect(leased.workflowRunId).toBe(admitted.runId);
    await markWorkflowApiAdmissionEnqueued(leased.id, randomUUID());
    await db().update(workflow_definition).set({ name: "Edited after API admission",
      output_contract: { harnessBatch: { ...contract, artifactName: "changed.csv", columns: ["changed"] } } })
      .where(eq(workflow_definition.id, workflow.id));
    const apiPayload = { orgId, workflowId: workflow.id, triggerKind: "api" as const,
      apiAdmissionId: leased.id, workflowRunId: admitted.runId, workRunId: leased.workRunId,
      threadId: leased.threadId, executionMode: "single" as const, queueAttempt: leased.attempts };
    await runWorkflowRunFire(apiPayload);
    await runWorkflowRunFire(apiPayload);
    const status = await getWorkflowApiRunStatus({ workflowId: workflow.id, runId: admitted.runId,
      token, clientFingerprint: `fixture-${orgId}` });
    expect(status).toMatchObject({ status: "completed", artifact: { url: expect.stringContaining("/artifact") },
      result: { kind: "csv", rows: 1, columns: ["lead_id"] } });
    const apiWorkspace = await ensureWorkWorkspace(orgId, leased.threadId, leased.workRunId);
    expect(await readFile(join(apiWorkspace.artifactRoot, "leads.csv"), "utf8")).toBe("lead_id\nLEAD-42\n");
    const apiArtifacts = await db().select({ kind: work_run_event.kind }).from(work_run_event).where(eq(work_run_event.run_id, leased.workRunId));
    expect(apiArtifacts.filter((event) => event.kind === "artifact")).toHaveLength(1);

    if (process.env.HARNESS_M3_WEB_URL) {
      const base = process.env.HARNESS_M3_WEB_URL;
      await db().update(workflow_definition).set({ output_contract: { harnessBatch: contract } }).where(eq(workflow_definition.id, workflow.id));
      const posted = await fetch(`${base}/api/v1/workflows/${workflow.id}/runs`, {
        method: "POST", headers: { "content-type": "application/json", authorization: `Bearer ${token}`,
          "idempotency-key": "batch-http-acceptance" }, body: JSON.stringify({ targetDay: "2026-09-15" }),
      });
      expect(posted.status).toBe(202);
      const accepted = await posted.json() as { runId: string; statusUrl: string };
      const [httpLease] = await leasePendingWorkflowApiAdmissions();
      expect(httpLease.workflowRunId).toBe(accepted.runId);
      await markWorkflowApiAdmissionEnqueued(httpLease.id, randomUUID());
      await db().update(workflow_definition).set({ output_contract: { harnessBatch: { ...contract, artifactName: "later.csv" } } })
        .where(eq(workflow_definition.id, workflow.id));
      await runWorkflowRunFire({ ...apiPayload, apiAdmissionId: httpLease.id, workflowRunId: accepted.runId,
        workRunId: httpLease.workRunId, threadId: httpLease.threadId, queueAttempt: httpLease.attempts });
      const headers = { authorization: `Bearer ${token}` };
      const polled = await fetch(new URL(accepted.statusUrl, base), { headers });
      expect(polled.status).toBe(200);
      const outcome = await polled.json() as { status: string; artifact: { url: string } | null };
      expect(outcome.status).toBe("completed");
      const downloaded = await fetch(new URL(outcome.artifact!.url, base), { headers });
      expect(downloaded.status).toBe(200);
      expect(downloaded.headers.get("content-type")).toContain("text/csv");
      expect(await downloaded.text()).toBe("lead_id\nLEAD-42\n");
    }
  } finally {
    await shutdownAgentBroker();
    for (const name of envNames) {
      if (prior[name] === undefined) delete process.env[name];
      else process.env[name] = prior[name];
    }
    await deleteTestOrg(orgId);
    await rm(root, { recursive: true, force: true });
  }
}, 60_000);
