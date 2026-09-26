import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdir, lstat, readFile } from "node:fs/promises";
import { isAbsolute, join } from "node:path";
import { promisify } from "node:util";
import { pool } from "@neko/db";
import type { HarnessBatchPayload } from "@neko/db/jobs";
import { ensureAgentBroker, ensureWorkWorkspace, getWorkRun, runEntitlementActor, runHeldItemIds } from "@neko/llm/work";

const execFileAsync = promisify(execFile);

type BatchReceipt = { artifact: string; sha256: string; rows: number; queries: number };

function batchConfig() {
  const vars = [
    "OPENNEKO_HARNESS_BATCH_BIN", "HARNESS_OPENSHELL_BIN", "OPENSHELL_GATEWAY",
    "HARNESS_BATCH_IMAGE", "HARNESS_BATCH_SCRIPT", "HARNESS_BATCH_SCRIPT_SHA256",
    "HARNESS_BATCH_BUNDLE_DIR", "HARNESS_BATCH_BUNDLE_SHA256", "HARNESS_BATCH_WORKFLOW_NAME",
  ] as const;
  const values = Object.fromEntries(vars.map((name) => [name, process.env[name] ?? ""]));
  if (vars.some((name) => !values[name]) || !isAbsolute(values.OPENNEKO_HARNESS_BATCH_BIN) ||
      !isAbsolute(values.HARNESS_OPENSHELL_BIN) || !isAbsolute(values.HARNESS_BATCH_SCRIPT) ||
      !isAbsolute(values.HARNESS_BATCH_BUNDLE_DIR) || values.HARNESS_BATCH_WORKFLOW_NAME.length > 128) {
    throw new Error("Harness batch worker is not configured");
  }
  return values;
}

/** A pg-boss retry may enter here after SIGKILL. The database lock fences hosts;
 * the Go runner then removes only a sandbox carrying this exact run label. */
export async function runHarnessBatch(payload: HarnessBatchPayload): Promise<void> {
  if (!/^[0-9a-f-]{36}$/i.test(payload.runId) ||
      !/^[0-9a-f-]{36}$/i.test(payload.threadId) ||
      !/^[0-9a-f-]{36}$/i.test(payload.workflowRunId)) {
    throw new Error("Invalid Harness batch job identity");
  }
  const config = batchConfig();
  const client = await pool().connect();
  const lockKey = JSON.stringify(["harness-batch", payload.orgId, payload.runId]);
  let locked = false;
  const abort = new AbortController();
  const lost = () => abort.abort();
  client.on("error", lost);
  client.on("end", lost);
  let broker: Awaited<ReturnType<typeof ensureAgentBroker>> | undefined;
  try {
    const lock = await client.query("SELECT pg_try_advisory_lock(hashtextextended($1, 0)) AS locked", [lockKey]);
    locked = lock.rows[0]?.locked === true;
    if (!locked) throw new Error("Harness batch is active on another host");
    const run = await getWorkRun(payload.orgId, payload.runId);
    if (!run || run.thread_id !== payload.threadId || run.backend !== "harness" ||
        !["queued", "running", "completed"].includes(run.status)) {
      throw new Error("Harness batch run binding is invalid");
    }
    const binding = await client.query<{ workflow_id: string; workflow_name: string; enabled: boolean;
      definition_status: string; owner_user_id: string; workflow_status: string;
      trigger_payload: { targetDay?: unknown }; output_contract: { harnessBatch?: unknown } | null }>(`
      SELECT wr.workflow_id, wd.name AS workflow_name, wd.enabled, wd.status AS definition_status,
        wd.owner_user_id, wr.status AS workflow_status, wr.trigger_payload, wd.output_contract
      FROM workflow_run wr JOIN workflow_definition wd ON wd.id=wr.workflow_id AND wd.org_id=wr.org_id
      WHERE wr.org_id=$1 AND wr.id=$2 AND wr.work_run_id=$3 AND wr.thread_id=$4`,
      [payload.orgId, payload.workflowRunId, payload.runId, payload.threadId]);
    const workflow = binding.rows[0];
    const executor = workflow?.output_contract?.harnessBatch as Record<string, unknown> | undefined;
    const columns = executor?.columns;
    const artifactName = executor?.artifactName;
    if (!workflow || !workflow.enabled || workflow.definition_status !== "active" ||
        workflow.workflow_name !== config.HARNESS_BATCH_WORKFLOW_NAME ||
        executor?.version !== 1 || executor?.executor !== "query-to-file" ||
        !Array.isArray(columns) || columns.length === 0 || columns.length > 64 ||
        columns.some((column) => typeof column !== "string" || !/^[A-Za-z_][A-Za-z0-9_]{0,79}$/.test(column)) ||
        new Set(columns).size !== columns.length ||
        typeof artifactName !== "string" || !artifactName.endsWith(".csv") ||
        !/^[A-Za-z0-9][A-Za-z0-9._-]{0,119}\.csv$/.test(artifactName) ||
        !["running", "completed"].includes(workflow.workflow_status) ||
        typeof workflow.trigger_payload?.targetDay !== "string" ||
        !/^\d{4}-\d{2}-\d{2}$/.test(workflow.trigger_payload.targetDay)) {
      throw new Error("Harness batch workflow binding is invalid");
    }
    if (!run.actor_role || (workflow.owner_user_id
      ? run.actor_user_id !== workflow.owner_user_id || run.actor_role === "service"
      : run.actor_user_id !== null || run.actor_role !== "service")) {
      throw new Error("Harness batch actor binding is invalid");
    }
    if (workflow.owner_user_id) {
      const owner = await client.query("SELECT 1 FROM app_user WHERE org_id=$1 AND id=$2 AND disabled_at IS NULL",
        [payload.orgId, workflow.owner_user_id]);
      if (!owner.rowCount) throw new Error("Harness batch workflow owner is inactive");
    }
    const actor = await runEntitlementActor(payload.orgId,
      { userId: run.actor_user_id, role: run.actor_role }, { workflowId: workflow.workflow_id });
    const allowedWorkflows = await runHeldItemIds(actor, "workflow");
    if (allowedWorkflows && !allowedWorkflows.includes(workflow.workflow_id) &&
        workflow.owner_user_id !== run.actor_user_id) {
      throw new Error("Harness batch workflow is not granted to this actor");
    }
    const workspace = await ensureWorkWorkspace(payload.orgId, payload.threadId, payload.runId);
    const artifact = join(workspace.artifactRoot, artifactName);
    const existing = await client.query(
      "SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact' AND payload->'artifact'->>'path'=$3 LIMIT 1",
      [payload.orgId, payload.runId, join("runs", payload.runId, "artifacts", artifactName)],
    );
    if (run.status === "completed") {
      if (!existing.rowCount || workflow.workflow_status !== "completed") throw new Error("Harness batch completed without workflow artifact");
      const info = await lstat(artifact);
      if (!info.isFile() || info.size > 64 * 1024 * 1024) throw new Error("Harness batch completed artifact is invalid");
      return;
    }
    await client.query(
      "UPDATE work_run SET status='running', updated_at=now() WHERE org_id=$1 AND id=$2 AND status IN ('queued','running')",
      [payload.orgId, payload.runId],
    );
    await client.query(`UPDATE workflow_run SET queue_attempts=queue_attempts+1,
      progress=jsonb_build_object('stage','executing'),updated_at=now()
      WHERE org_id=$1 AND id=$2 AND status='running'`, [payload.orgId, payload.workflowRunId]);
    const workDir = join(workspace.runRoot, "batch");
    await mkdir(workDir, { recursive: true });
    const activeBroker = await ensureAgentBroker();
    if (!activeBroker) throw new Error("Harness batch broker unavailable");
    broker = activeBroker;
    const token = activeBroker.tokenFor({ orgId: payload.orgId, threadId: payload.threadId,
      runId: payload.runId, kind: "work", profile: "harness-read-only",
      lookupRead: false, batchRead: true });
    let receipt: BatchReceipt;
    let checking = false;
    const poll = setInterval(async () => {
      if (checking || abort.signal.aborted) return;
      checking = true;
      try {
        const state = await client.query(`SELECT wr.status AS work_status, wfr.status AS workflow_status
          FROM work_run wr JOIN workflow_run wfr ON wfr.org_id=wr.org_id AND wfr.work_run_id=wr.id
          WHERE wr.org_id=$1 AND wr.id=$2 AND wfr.id=$3`,
          [payload.orgId, payload.runId, payload.workflowRunId]);
        if (state.rows[0]?.work_status !== "running" || state.rows[0]?.workflow_status !== "running") abort.abort();
      } catch { abort.abort(); }
      finally { checking = false; }
    }, 2_000);
    poll.unref();
    try {
      const { stdout } = await execFileAsync(config.OPENNEKO_HARNESS_BATCH_BIN, [workflow.trigger_payload.targetDay], {
        timeout: 22 * 60_000, maxBuffer: 16 * 1024, signal: abort.signal,
        env: {
          PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: process.env.HOME ?? "",
          ...(process.env.XDG_CONFIG_HOME ? { XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME } : {}),
          ...config, HARNESS_BATCH_RUN_ID: payload.runId,
          HARNESS_BATCH_COLUMNS_JSON: JSON.stringify(columns),
          HARNESS_BATCH_ARTIFACT_NAME: artifactName,
          HARNESS_BATCH_WORK_DIR: workDir, HARNESS_BATCH_ARTIFACT_DIR: workspace.artifactRoot,
          OPENNEKO_BROKER_URL: `http://127.0.0.1:${activeBroker.port}`,
          OPENNEKO_BROKER_TOKEN: token,
        },
      });
      receipt = JSON.parse(stdout) as BatchReceipt;
    } catch {
      throw new Error("Harness batch execution failed; retry uses saved query files");
    } finally {
      clearInterval(poll);
      activeBroker.release(payload.runId);
    }
    if (abort.signal.aborted || receipt.artifact !== artifact ||
        !/^[a-f0-9]{64}$/.test(receipt.sha256) ||
        !Number.isInteger(receipt.rows) || receipt.rows < 0 ||
        !Number.isInteger(receipt.queries) || receipt.queries < 0 || receipt.queries > 128) {
      throw new Error("Harness batch result is invalid");
    }
    const info = await lstat(artifact);
    if (!info.isFile() || info.size > 64 * 1024 * 1024) throw new Error("Harness batch artifact is invalid");
    const bytes = await readFile(artifact);
    if (createHash("sha256").update(bytes).digest("hex") !== receipt.sha256) {
      throw new Error("Harness batch artifact digest mismatch");
    }
    const relative = join("runs", payload.runId, "artifacts", artifactName).replaceAll("\\", "/");
    await client.query("BEGIN");
    try {
      const current = await client.query("SELECT status FROM work_run WHERE org_id=$1 AND id=$2 FOR UPDATE", [payload.orgId, payload.runId]);
      const workflowCurrent = await client.query("SELECT status FROM workflow_run WHERE org_id=$1 AND id=$2 FOR UPDATE", [payload.orgId, payload.workflowRunId]);
      if (current.rows[0]?.status !== "running" || workflowCurrent.rows[0]?.status !== "running" || abort.signal.aborted) {
        throw new Error("Harness batch was cancelled before publication");
      }
      await client.query(`INSERT INTO work_run_event (org_id,thread_id,run_id,kind,payload)
        SELECT $1,$2,$3,'artifact',$4::jsonb WHERE NOT EXISTS (
          SELECT 1 FROM work_run_event WHERE org_id=$1 AND run_id=$3 AND kind='artifact'
            AND payload->'artifact'->>'path'=$5)`,
      [payload.orgId, payload.threadId, payload.runId,
        JSON.stringify({ type: "artifact", artifact: { path: relative, label: artifactName, mimeType: "text/csv" } }), relative]);
      const finalText = `Processed ${receipt.rows} records into a CSV artifact with ${receipt.queries} governed queries.`;
      await client.query(`INSERT INTO work_run_event (org_id,thread_id,run_id,kind,payload)
        VALUES ($1,$2,$3,'message',$4::jsonb),($1,$2,$3,'done',$5::jsonb)`, [
        payload.orgId, payload.threadId, payload.runId,
        JSON.stringify({ type: "message", role: "assistant", content: finalText }),
        JSON.stringify({ type: "done", result: { status: "completed" } }),
      ]);
      await client.query("UPDATE work_run SET status='completed',error=NULL,finished_at=now(),updated_at=now() WHERE org_id=$1 AND id=$2", [payload.orgId, payload.runId]);
      await client.query(`UPDATE workflow_run SET status='completed',error=NULL,
        summary=$7,result_artifact_path=$3,terminal_result=$8::jsonb,
        progress=jsonb_build_object('stage','completed','rows',$4::integer,'queries',$5::integer,'artifactBytes',$6::integer),
        finished_at=now(),updated_at=now() WHERE org_id=$1 AND id=$2`,
        [payload.orgId, payload.workflowRunId, relative, receipt.rows, receipt.queries, info.size,
          finalText, JSON.stringify({ kind: "csv", rows: receipt.rows, queries: receipt.queries, columns, sha256: receipt.sha256 })]);
      await client.query("COMMIT");
      console.log(`[harness-batch] completed workflowRun=${payload.workflowRunId} run=${payload.runId} rows=${receipt.rows} queries=${receipt.queries} bytes=${info.size}`);
    } catch (error) {
      await client.query("ROLLBACK").catch(() => {});
      throw error;
    }
  } finally {
    broker?.release(payload.runId);
    let destroy = abort.signal.aborted;
    if (locked && !destroy) {
      try { await client.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [lockKey]); }
      catch { destroy = true; }
    }
    client.removeListener("error", lost);
    client.removeListener("end", lost);
    client.release(destroy);
  }
}
