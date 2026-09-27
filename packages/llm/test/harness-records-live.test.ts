import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, pool, organization, work_thread, work_run } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import { runRecordsMigrations } from "@neko/db/records-migrate";
import { expect, it } from "vitest";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("runs a records-only Harness turn through OpenShell and the actor-scoped broker", async () => {
  if (process.env.NEKO_PG_PORT !== "18119" || !process.env.HARNESS_STATE) {
    throw new Error("isolated M3 environment required");
  }
  if (process.env.RECORDS_PG_PORT !== "18120") throw new Error("isolated records database required");
  await runRecordsMigrations({});
  const orgId = `harness-records-${randomUUID()}`;
  const threadId = randomUUID();
  const runId = randomUUID();
  const orgRoot = join(process.env.HARNESS_STATE, "records", runId);
  const workspace: AgentWorkspace = {
    orgRoot, skillsRoot: join(orgRoot, "skills"), memoryRoot: join(orgRoot, "memory"),
    knowledgeRoot: join(orgRoot, "knowledge"), uploadsRoot: join(orgRoot, "uploads"),
    runsRoot: join(orgRoot, "runs"), threadUploadsRoot: join(orgRoot, "uploads", threadId),
    runRoot: join(orgRoot, "runs", runId), artifactRoot: join(orgRoot, "runs", runId, "artifacts"),
    binRoot: join(orgRoot, "runs", runId, "bin"),
  };
  for (const dir of Object.values(workspace)) await mkdir(dir, { recursive: true });
  await mkdir(join(workspace.skillsRoot, "records"));
  const hermesHome = join(orgRoot, "provider-config");
  await mkdir(hermesHome, { recursive: true });
  await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-records-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await db().insert(organization).values({ id: orgId, name: "Harness records acceptance" });
  await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Records" });
  await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "admin" });
  const broker = await startAgentBroker({ port: 0, hostAlias: "host.docker.internal", controlPlane: inProcessControlPlane });
  try {
    const deniedToken = broker.tokenFor({ orgId, threadId, runId, kind: "work", profile: "harness-read-only", recordsRead: true, lookupRead: false });
    const post = (path: string, body: object) => fetch(`http://127.0.0.1:${broker.port}${path}`, {
      method: "POST", headers: { authorization: `Bearer ${deniedToken}`, "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    expect((await post("/v1/harness/lookup", {})).status).toBe(403);
    expect((await post("/v1/graphjin/agent", {})).status).toBe(403);
    expect((await post("/v1/memory/search", { query: "policy" })).status).toBe(403);
    const catalog = await post("/v1/records/catalog", {});
    expect(catalog.status).toBe(200);
    expect((await catalog.json()).apps).toEqual([]);
    broker.release(runId);
    const runCore = makeSandboxRunCore({
      cli: process.env.HARNESS_M3_CLI!, gatewayName: "harness-m2", agentImage: "harness-openneko:m3",
      modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }],
      hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url,
      brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => {},
    });
    const events: AgentEvent[] = [];
    const result = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace,
      prompt: "Browse the generated records app catalog and report what is available.",
      userMessage: "Which records apps can I use?", dataSurface: "records", pluginActions: [],
      emit: async (event) => { events.push(event); },
    });
    expect(result.status).toBe("completed");
    expect(result.finalText).toContain("no generated apps");
    expect(result.finalText).toContain("crm blueprint");
    const snapshot = JSON.parse(await readFile(join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`), "utf8"));
    expect(snapshot.spec).toMatchObject({ max_operations: 12, max_model_calls: 24 });
    expect((await pool().query("SELECT operation_limit FROM harness_run_journal WHERE org_id=$1 AND run_id=$2", [orgId, runId])).rows[0].operation_limit).toBe(12);
    expect(snapshot.operations).toMatchObject([{ tool: "mcp_neko_records_browse_catalog", finished: true }, { tool: "mcp_neko_records_browse_blueprints", finished: true }]);
    expect(snapshot.operations[1].result.content.join(" ")).toContain("crm");
    expect(snapshot.operations).toHaveLength(2);
    expect(events.some((event) => event.type === "tool_start")).toBe(true);
  } finally {
    try { await broker.close(); }
    finally { await deleteTestOrg(orgId); }
  }
}, 120_000);
