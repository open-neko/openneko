import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { db, eq, organization, action_policy, llm_provider_config, processing_job, pool } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkRunPayload } from "@neko/db/jobs";
import { RPC_PROTOCOL_VERSION } from "../../plugin-types/src/index";
import { expect, it } from "vitest";
import { createWorkRun, createWorkThread, getWorkRun, shutdownAgentBroker } from "../src/work";
import { approveActionRequest, getActionRequest } from "../src/workflows/action-store";
import { executeApprovedActionRequest, registerActionAdapter, type ActionAdapter } from "../src/workflows/action-executor";
import { runWorkRun } from "../../../apps/worker/src/jobs/work-run";
import { PluginRegistry, pluginIdFromName } from "../../../apps/worker/src/plugins/plugin-registry";
import { setPluginRegistryInstance } from "../../../apps/worker/src/plugins/registry-instance";
import { OpenShellRuntime } from "../../../apps/worker/src/plugins/openshell-runtime";
import { deleteTestOrg } from "@neko/db/test-helpers";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;
const kind = "fixture_plugin_effect";
const pluginName = "@open-neko/plugin-harness-fixture";
const declaration = { kind, description: "Apply one synthetic plugin effect", default_mode: "ask" };

const runnerSource = `
const fs = require('node:fs');
const method = process.argv[2];
const params = JSON.parse(process.argv[3] || '{}');
const countPath = '/sandbox/fixture-effect-count';
const count = () => Number(fs.existsSync(countPath) ? fs.readFileSync(countPath, 'utf8') : '0');
let result;
if (method === 'register') {
  result = {protocol:${RPC_PROTOCOL_VERSION},pluginName:${JSON.stringify(pluginName)},pluginVersion:'0.1.0',
    capabilities:{action:{kinds:[${JSON.stringify(declaration)}]}}};
} else if (method === 'execute_action') {
  const request = params.request;
  if (request.kind !== ${JSON.stringify(kind)} || request.payload?.value !== 42) throw Error('approved plugin payload changed');
  fs.writeFileSync(countPath, String(count() + 1));
  result = {outcome:{result:{value:42}}};
} else if (method === 'effect_count') {
  result = {count:count()};
} else throw Error('unsupported fixture RPC');
process.stdout.write(JSON.stringify({ok:true,result}) + '\\n');
`;

live("queued Harness Work run approves and executes an installed plugin once", async () => {
  if (process.env.NEKO_PG_PORT !== "18119" || !process.env.HARNESS_STATE || !process.env.OPENNEKO_AGENT_HERMES_HOME) {
    throw Error("isolated Harness plugin environment required");
  }
  const orgId = `harness-plugin-${randomUUID()}`;
  const root = await mkdtemp(join(tmpdir(), "harness-installed-plugin-"));
  const pluginId = pluginIdFromName(pluginName);
  const runtime = new OpenShellRuntime({ image: process.env.OPENNEKO_PLUGIN_BASE_IMAGE ?? "ghcr.io/open-neko/plugin-base:v3.5.6",
    cli: process.env.HARNESS_M3_CLI!, gatewayName: "harness-m2", bundleDir: join(root, "work"), onLog: () => {} });
  let registry: PluginRegistry | undefined;
  let queue: Awaited<ReturnType<typeof boss>> | undefined;
  let adapterError = "";
  try {
    await mkdir(join(root, "repo"));
    await mkdir(join(root, "work"));
    await mkdir(join(root, "secrets"));
    const runnerPath = join(root, "runner.js");
    await writeFile(runnerPath, runnerSource);
    const manifestPath = join(root, "repo", "openneko.plugins.json");
    const manifest = {
      schema: "https://open-neko.github.io/plugins/manifest.schema.json",
      plugins: [{ name: pluginName, version: "0.1.0", integrity: `sha512-${"a".repeat(86)}==`,
        permissions: { network: [], env: [] }, capabilities: { action: { kinds: [declaration] } } }],
    };
    await writeFile(manifestPath, JSON.stringify(manifest));
    registry = new PluginRegistry({ repoRoot: join(root, "repo"), workRoot: join(root, "work"),
      secretsConfigDir: join(root, "secrets"), runtime, resolveRunner: () => runnerPath,
      onAdapter: (registeredKind, adapter: ActionAdapter) => registerActionAdapter(registeredKind, async input => {
        try { return await adapter(input); }
        catch (error) { adapterError = error instanceof Error ? error.message : String(error); throw error; }
      }, "plugin") });
    await registry.start();
    expect(registry.getRegisteredActionDescriptors()).toMatchObject([{ kind, pluginName }]);
    setPluginRegistryInstance(registry);

    await db().insert(organization).values({ id: orgId, name: "Installed plugin fixture", setup_complete_at: new Date() });
    await db().insert(llm_provider_config).values({ org_id: orgId, scope: "primary", provider: "ollama",
      model: "harness-installed-plugin-fixture", config: { url: "http://host.docker.internal:18118" } });
    await db().insert(action_policy).values({ org_id: orgId, name: "Fixture plugin approval", mode: "approval_required",
      applies_to_kinds: [kind], applies_to_scopes: ["external"] });
    await writeFile(join(process.env.OPENNEKO_AGENT_HERMES_HOME, "config.yaml"),
      "model:\n  provider: custom\n  default: harness-installed-plugin-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const thread = await createWorkThread(orgId, "Installed plugin action", "web", null);
    const run = await createWorkRun(orgId, thread.id, "harness", { userId: null, role: "service" });
    queue = await boss();
    await queue.createQueue(QUEUE.WORK_RUN);
    await queue.work<WorkRunPayload>(QUEUE.WORK_RUN, async jobs => {
      for (const job of jobs) {
        const { processingJobId, orgId: jobOrgId, ...payload } = job.data;
        await db().update(processing_job).set({ status: "running" }).where(eq(processing_job.id, processingJobId));
        try {
          await runWorkRun(processingJobId, jobOrgId, { ...payload, channel: "web" });
          await db().update(processing_job).set({ status: "succeeded" }).where(eq(processing_job.id, processingJobId));
        } catch (error) {
          await db().update(processing_job).set({ status: "failed" }).where(eq(processing_job.id, processingJobId));
          throw error;
        }
      }
    });
    const deliver = async () => {
      const [job] = await db().insert(processing_job).values({ org_id: orgId, kind: QUEUE.WORK_RUN,
        trigger: "installed-plugin-fixture" }).returning();
      await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId, runId: run.id,
        threadId: thread.id, message: "Request approval to apply the installed plugin effect with value 42." }, { retryLimit: 0 });
      for (let n = 0; n < 90; n++) {
        const current = await getWorkRun(orgId, run.id);
        const [processing] = await db().select().from(processing_job).where(eq(processing_job.id, job.id));
        if (current?.status === "completed" && processing?.status === "succeeded") return;
        if (current?.status === "failed" || processing?.status === "failed") {
          const counts = await (await fetch("http://127.0.0.1:18118/control")).json();
          throw Error(`Installed plugin run failed: ${current?.error ?? "worker error"}; model=${JSON.stringify(counts)}`);
        }
        await new Promise(resolve => setTimeout(resolve, 500));
      }
      throw Error("Installed plugin run did not finish");
    };
    await deliver();
    const rows = (await pool().query("SELECT id,status FROM action_request WHERE org_id=$1 AND work_run_id=$2", [orgId, run.id])).rows;
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("pending_approval");
    expect(runtime.hasPlugin(pluginId)).toBe(false);
    const before = (await (await fetch("http://127.0.0.1:18118/control")).json())["harness-installed-plugin-fixture"];
    await deliver();
    expect((await (await fetch("http://127.0.0.1:18118/control")).json())["harness-installed-plugin-fixture"]).toBe(before);
    expect((await pool().query("SELECT count(*)::int AS n FROM action_request WHERE org_id=$1 AND work_run_id=$2", [orgId, run.id])).rows[0].n).toBe(1);
    await approveActionRequest({ orgId, id: rows[0].id, approverUserId: null,
      approver: { userId: null, role: "admin" } });
    expect((await getActionRequest(orgId, rows[0].id))?.status).toBe("approved");
    await writeFile(manifestPath, JSON.stringify({ ...manifest, plugins: [] }));
    await registry.refresh();
    await expect(executeApprovedActionRequest(orgId, rows[0].id)).rejects.toThrow("no longer available");
    expect((await pool().query("SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1 AND action_request_id=$2", [orgId, rows[0].id])).rows[0].n).toBe(0);
    expect(runtime.hasPlugin(pluginId)).toBe(false);
    await writeFile(manifestPath, JSON.stringify({ ...manifest, plugins: [{ ...manifest.plugins[0], name: "@open-neko/plugin-replacement" }] }));
    await registry.refresh();
    await expect(executeApprovedActionRequest(orgId, rows[0].id)).rejects.toThrow("no longer available");
    await writeFile(manifestPath, JSON.stringify({ ...manifest, plugins: [{ ...manifest.plugins[0], version: "0.1.1" }] }));
    await registry.refresh();
    await expect(executeApprovedActionRequest(orgId, rows[0].id)).rejects.toThrow("no longer available");
    await writeFile(manifestPath, JSON.stringify({ ...manifest, plugins: [{ ...manifest.plugins[0], integrity: `sha512-${"b".repeat(86)}==` }] }));
    await registry.refresh();
    await expect(executeApprovedActionRequest(orgId, rows[0].id)).rejects.toThrow("no longer available");
    await writeFile(manifestPath, JSON.stringify({ ...manifest, plugins: [{ ...manifest.plugins[0],
      capabilities: { action: { kinds: [{ ...declaration, default_mode: "deny" }] } } }] }));
    await registry.refresh();
    await expect(executeApprovedActionRequest(orgId, rows[0].id)).rejects.toThrow("no longer available");
    expect((await pool().query("SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1 AND action_request_id=$2", [orgId, rows[0].id])).rows[0].n).toBe(0);
    await writeFile(manifestPath, JSON.stringify(manifest));
    await registry.refresh();
    const execution = await executeApprovedActionRequest(orgId, rows[0].id);
    if (!execution.ok) {
      const names = spawnSync("docker", ["ps", "-a", "--format", "{{.Names}}"], {encoding:"utf8"}).stdout
        .split("\n").filter(name => name.startsWith(`openshell-default--${pluginId}-`));
      const logResult = names.length ? spawnSync("docker", ["logs", "--tail", "80", names[0]], {encoding:"utf8"}) : null;
      const logs = `${logResult?.stdout ?? ""}${logResult?.stderr ?? ""}`;
      throw Error(`Plugin execution failed: ${adapterError}; sandbox logs: ${logs.slice(-2500)}`);
    }
    expect((await executeApprovedActionRequest(orgId, rows[0].id)).ok).toBe(true);
    expect(runtime.hasPlugin(pluginId)).toBe(true);
    expect(await runtime.callRpc(pluginId, "effect_count", "{}")).toMatchObject({ok:true,result:{count:1}});
  } finally {
    setPluginRegistryInstance(null);
    if (queue) await queue.offWork(QUEUE.WORK_RUN);
    await shutdownAgentBroker();
    await registry?.stop();
    await deleteTestOrg(orgId);
    await rm(root, { recursive: true, force: true });
  }
}, 180_000);
