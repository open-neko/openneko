import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import pg from "pg";
import { db, organization, work_thread, work_run, app_state } from "@neko/db";
import { buildRecordsPoolConfig, runRecordsMigrations } from "@neko/db/records-migrate";
import { deleteTestOrg } from "@neko/db/test-helpers";
import {
  loadRecordsGraphjinPolicyModel, projectRecordsGraphjinRoles,
  recordsGraphjinSigningSecret, recordsGraphjinCursorSecret,
  writeRecordsGraphjinConfig,
} from "@neko/records";
import { expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("reads populated Records through the bound broker and real GraphJin", async () => {
  if (process.env.NEKO_PG_PORT !== "18119" || process.env.RECORDS_PG_PORT !== "18120" || !process.env.HARNESS_STATE) {
    throw Error("isolated M3 records environment required");
  }
  await runRecordsMigrations({});
  const orgId = `harness-records-data-${randomUUID()}`;
  const threadId = randomUUID(), runId = randomUUID(), objectId = randomUUID();
  const tableName = `equipment__loan_${runId.replaceAll("-", "").slice(0, 8)}`;
  const root = join(process.env.HARNESS_STATE, "records-data", runId);
  const workspace: AgentWorkspace = {
    orgRoot: root, skillsRoot: join(root, "skills"), memoryRoot: join(root, "memory"),
    knowledgeRoot: join(root, "knowledge"), uploadsRoot: join(root, "uploads"),
    runsRoot: join(root, "runs"), threadUploadsRoot: join(root, "uploads", threadId),
    runRoot: join(root, "runs", runId), artifactRoot: join(root, "runs", runId, "artifacts"),
    binRoot: join(root, "runs", runId, "bin"),
  };
  const recordsPool = new pg.Pool(buildRecordsPoolConfig());
  let child: ReturnType<typeof spawn> | undefined;
  let broker: Awaited<ReturnType<typeof startAgentBroker>> | undefined;
  const priorUrl = process.env.OPENNEKO_RECORDS_GRAPHJIN_URL;
  let logs = "";
  try {
    await mkdir(root, { recursive: true });
    for (const dir of Object.values(workspace)) await mkdir(dir, { recursive: true });
    await mkdir(join(workspace.skillsRoot, "records"));
    const hermesHome = join(root, "provider-config");
    await mkdir(hermesHome, { recursive: true });
    await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-records-data-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    await db().insert(organization).values({ id: orgId, name: "Populated records fixture" });
    await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Records data" });
    await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "admin" });
    await db().insert(app_state).values({ org_id: orgId, app_id: "equipment", status: "active" });
    await recordsPool.query(`CREATE TABLE public.${tableName} (
      id text PRIMARY KEY, org_id text NOT NULL, name text NOT NULL,
      owner_user_id text, nk_deleted_at timestamptz, nk_updated_at timestamptz)`);
    await recordsPool.query("INSERT INTO engine.registry_version (org_id, revision) VALUES ($1, 1)", [orgId]);
    await recordsPool.query("INSERT INTO engine.record_app (org_id, app_id, label, status) VALUES ($1, 'equipment', 'Equipment', 'active')", [orgId]);
    await recordsPool.query(`INSERT INTO engine.record_object
      (id, org_id, app_id, api_name, label, plural_label, table_name, name_field, visibility)
      VALUES ($1, $2, 'equipment', 'loan', 'Loan', 'Loans', $3, 'name', 'org')`, [objectId, orgId, tableName]);
    await recordsPool.query(`INSERT INTO engine.record_field
      (org_id, object_id, api_name, label, kind, column_name, required)
      VALUES ($1, $2, 'name', 'Name', 'text', 'name', true)`, [orgId, objectId]);
    await recordsPool.query(`INSERT INTO engine.record_permission
      (org_id, app_id, role, object_api_name, can_read)
      VALUES ($1, 'equipment', 'admin', 'loan', true)`, [orgId]);
    await recordsPool.query(`INSERT INTO public.${tableName} (id, org_id, name) VALUES ('loan-42', $1, 'Fixture loan')`, [orgId]);
    await recordsPool.query(`INSERT INTO engine.recycle_record
      (org_id, app_id, object_api_name, visibility, record_id, record_name, deleted_at, deletion_action_request_id)
      VALUES ($1, 'equipment', 'loan', 'org', 'loan-deleted-42', 'Deleted fixture loan', now(), 'fixture-delete')`, [orgId]);
    const model = await loadRecordsGraphjinPolicyModel(recordsPool, orgId);
    const configDir = join(root, "graphjin");
    await mkdir(configDir, { recursive: true });
    const configFile = join(configDir, "dev.yml");
    const config = buildRecordsPoolConfig();
    const connection = new URL(`postgres://${config.user}:${config.password}@127.0.0.1:${config.port}/${config.database}?sslmode=disable`);
    await writeRecordsGraphjinConfig({
      configFile,
      config: {
        orgId, roles: projectRecordsGraphjinRoles(model),
        database: { connectionString: connection.toString() },
        jwt: { secret: recordsGraphjinSigningSecret(orgId) },
        secretKey: recordsGraphjinCursorSecret(orgId),
      },
      reloadRecordsGraphjin: async () => {},
    });
    await writeFile(configFile, (await readFile(configFile, "utf8")).replace("0.0.0.0:8090", "127.0.0.1:18124"));
    child = spawn(process.env.RECORDS_GRAPHJIN_BIN ?? "graphjin", ["serve", "--path", configDir], { stdio: ["ignore", "pipe", "pipe"] });
    child.stdout?.on("data", chunk => { logs = (logs + String(chunk)).slice(-2048); });
    child.stderr?.on("data", chunk => { logs = (logs + String(chunk)).slice(-2048); });
    let ready = false;
    for (let n = 0; n < 60; n++) {
      if (child.exitCode !== null || child.signalCode !== null) break;
      try { if ((await fetch("http://127.0.0.1:18124/health")).ok) { ready = true; break; } } catch { /* starting */ }
      await new Promise(resolve => setTimeout(resolve, 250));
    }
    expect(ready, logs).toBe(true);
    process.env.OPENNEKO_RECORDS_GRAPHJIN_URL = "http://127.0.0.1:18124";
    broker = await startAgentBroker({ port: 0, hostAlias: "host.docker.internal", controlPlane: inProcessControlPlane });
    const token = broker.tokenFor({ orgId, threadId, runId, kind: "work", profile: "harness-read-only", recordsRead: true, lookupRead: false });
    const post = async (path: string, body: object) => {
      const response = await fetch(`http://127.0.0.1:${broker!.port}${path}`, {
        method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
        body: JSON.stringify(body),
      });
      expect(response.status, `${path}: ${await response.clone().text()}`).toBe(200);
      return response.json();
    };
    expect(await post("/v1/records/catalog", { appId: "equipment" })).toMatchObject({ apps: [{ appId: "equipment" }] });
    expect(await post("/v1/records/find", { appId: "equipment", objectApiName: "loan", first: 5 })).toMatchObject({ rows: [{ id: "loan-42" }] });
    expect(await post("/v1/records/get", { appId: "equipment", objectApiName: "loan", recordId: "loan-42" })).toMatchObject({ row: { id: "loan-42" } });
    expect(await post("/v1/records/recycle/find", { appId: "equipment", objectApiName: "loan" })).toMatchObject({ rows: [{ recordId: "loan-deleted-42" }] });
    expect(await post("/v1/records/recycle/get", { appId: "equipment", objectApiName: "loan", recordId: "loan-deleted-42" })).toMatchObject({ row: { recordId: "loan-deleted-42" } });
    expect((await post("/v1/records/find", { appId: "equipment", objectApiName: "loan", first: 5, orgId: "forged", runId: "forged" })).rows).toHaveLength(1);
    broker.release(runId);
    const runCore = makeSandboxRunCore({
      cli: process.env.HARNESS_M3_CLI!, gatewayName: "harness-m2", agentImage: "harness-openneko:m3",
      modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }],
      hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url,
      brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => {},
    });
    const result = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace,
      prompt: "Read the equipment loan and recycle bin using Records tools.",
      userMessage: "Which loan exists and which loan was deleted?", dataSurface: "records", pluginActions: [],
      emit: async () => {},
    });
    expect(result.status).toBe("completed");
    expect(result.finalText).toContain("loan-42");
    const snapshot = JSON.parse(await readFile(join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`), "utf8"));
    expect(snapshot.operations.map((operation: { tool: string; finished: boolean }) => [operation.tool, operation.finished])).toEqual([
      ["mcp_neko_records_browse_catalog", true], ["mcp_neko_records_find_records", true],
      ["mcp_neko_records_get_record", true], ["mcp_neko_records_find_recycled_records", true],
      ["mcp_neko_records_get_recycled_record", true],
    ]);
  } finally {
    if (broker) await broker.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      child.kill("SIGTERM");
      await new Promise(resolve => child!.once("close", resolve));
    }
    if (priorUrl === undefined) delete process.env.OPENNEKO_RECORDS_GRAPHJIN_URL;
    else process.env.OPENNEKO_RECORDS_GRAPHJIN_URL = priorUrl;
    await recordsPool.query("DELETE FROM engine.record_app WHERE org_id=$1", [orgId]);
    await recordsPool.query("DELETE FROM engine.actor WHERE org_id=$1", [orgId]);
    await recordsPool.query("DELETE FROM engine.registry_version WHERE org_id=$1", [orgId]);
    await recordsPool.query(`DROP TABLE IF EXISTS public.${tableName}`);
    await recordsPool.end();
    await deleteTestOrg(orgId);
  }
}, 120_000);
