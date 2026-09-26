import { randomUUID } from "node:crypto";
import { chmod, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, eq, organization, work_run, work_run_event, work_thread, workflow_definition, workflow_run } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import { ensureWorkWorkspace, shutdownAgentBroker } from "@neko/llm/work";
import { expect, it } from "vitest";
import { runHarnessBatch } from "../../src/jobs/harness-batch";

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
    "HARNESS_BATCH_BUNDLE_SHA256", "HARNESS_BATCH_WORKFLOW_NAME", "MODEL_API_KEY"] as const;
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
    OPENNEKO_AGENT_HOME: join(root, "agent"), OPENNEKO_HARNESS_BATCH_BIN: fake,
    HARNESS_OPENSHELL_BIN: fake, OPENSHELL_GATEWAY: "fixture", HARNESS_BATCH_IMAGE: "fixture",
    HARNESS_BATCH_SCRIPT: fake, HARNESS_BATCH_SCRIPT_SHA256: "fixture",
    HARNESS_BATCH_BUNDLE_DIR: root, HARNESS_BATCH_BUNDLE_SHA256: "fixture", HARNESS_BATCH_WORKFLOW_NAME: "Fixture batch workflow", MODEL_API_KEY: "host-only-secret",
  });
  await db().insert(organization).values({ id: orgId, name: "Harness batch acceptance" });
  await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Batch" });
  await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
  const [workflow] = await db().insert(workflow_definition).values({ org_id: orgId, name: "Fixture batch workflow",
    output_contract: { harnessBatch: { version: 1, executor: "query-to-file", artifactName: "leads.csv", columns: ["lead_id"] } } }).returning({ id: workflow_definition.id });
  const [workflowRun] = await db().insert(workflow_run).values({ org_id: orgId, workflow_id: workflow.id,
    thread_id: threadId, work_run_id: runId, trigger_kind: "manual", trigger_payload: { targetDay: "2026-09-15" }, status: "running" }).returning({ id: workflow_run.id });
  try {
    const payload = { orgId, threadId, runId, workflowRunId: workflowRun.id };
    await db().update(workflow_definition).set({ enabled: false }).where(eq(workflow_definition.id, workflow.id));
    await expect(runHarnessBatch(payload)).rejects.toThrow("workflow binding is invalid");
    await db().update(workflow_definition).set({ enabled: true }).where(eq(workflow_definition.id, workflow.id));
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
