import { createServer } from "node:http";
import { createHash, randomUUID } from "node:crypto";
import { mkdtemp, mkdir, writeFile, cp, rm } from "node:fs/promises";
import { tmpdir, homedir } from "node:os";
import { join } from "node:path";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { beforeAll, afterAll, describe, expect, it, vi } from "vitest";
import { stringify } from "yaml";
import { pool } from "@neko/db";
import { createActionRequest, approveActionRequest, executeApprovedActionRequest, getActionRequest, listActionExecutions } from "@neko/llm/workflows";
import { createWorkThread, createWorkRun, inProcessControlPlane, runAgentBackend } from "@neko/llm/work";
import type { AgentWorkspace } from "@neko/llm";
import { createAdminHandler } from "../src/admin-server";
import { PackService } from "../src/packs/service";
import { registerPackActionRuntime } from "../src/packs/actions";

vi.mock("../src/plugins/plugin-registry", () => { throw new Error("Pack actions must not load plugins"); });
const image = process.env.OPENNEKO_PACK_ACTIONS_TEST_IMAGE;
describe.skipIf(!image)("pack actions through Work, PostgreSQL and OpenShell", () => {
  const org = `pack-actions-${Date.now()}`;
  const user = `pack-user-${Date.now()}`;
  const operations = ["read-record", "write-record", "uncertain-write"];
  const kind = (operation: string) => `pack.action-fixture.${operation}`;
  let root: string, accountId: string, threadId: string, runId: string;
  let service: PackService;
  let unregister: () => void;
  let admin: ReturnType<typeof createServer>;
  let adminUrl: string;
  const record = { value: 0, writes: 0 };
  const provider = createServer(async (req, res) => {
    if (req.method === "POST") {
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      record.value = JSON.parse(Buffer.concat(chunks).toString()).value; record.writes++;
    }
    res.setHeader("content-type", "application/json"); res.end(JSON.stringify(record));
  });
  const manifest = {
    apiVersion: "openneko.app/v1", kind: "SolutionPack",
    metadata: { id: "action-fixture", name: "Action fixture", version: "1.0.0", publisher: "fixture", category: "operations" },
    compatibility: { openneko: ">=2.40.0", applications: [], databases: [] }, inputs: [], secrets: [],
    artifacts: { actions: "actions", policies: "policies", skills: [] },
    health: { requiredPreflight: [], postInstall: [], postWriteCanary: [], readiness: {} },
    connectors: [{ id: "fixture", image, entrypoint: "/app/connector", network: [{ host: "host.docker.internal", port: 4114, binary: "/usr/local/bin/node" }],
      auth: { label: "Fixture", authorizationOrigin: "https://provider.example", scopes: ["read", "write"], credentialVersion: "1" },
      operations: operations.map(id => ({ id, description: id, effect: id === "read-record" ? "read" : "write" })) }],
  };
  const propose = (operation: string, input: Record<string, unknown> = {}, extra: Record<string, unknown> = {}) => createActionRequest({ orgId: org, kind: kind(operation), scope: "external", status: "approved", actorUserId: user, actorRole: "admin", actorBackend: "hermes", workRunId: runId, payload: { input, accountId }, ...extra });
  const approve = (id: string) => approveActionRequest({ orgId: org, id, approverUserId: user, approver: { userId: user, role: "admin" } });
  beforeAll(async () => {
    await new Promise<void>(resolve => provider.listen(4114, "0.0.0.0", resolve));
    root = await mkdtemp(join(tmpdir(), "pack-actions-"));
    await cp(join(process.env.XDG_CONFIG_HOME || join(homedir(), ".config"), "openshell"), join(root, "config/openshell"), { recursive: true });
    vi.stubEnv("XDG_CONFIG_HOME", join(root, "config")); vi.stubEnv("WORKER_ADMIN_URL", "");
    await pool().query("insert into organization(id,name) values($1,'Pack action test')", [org]);
    await pool().query("insert into app_user(id,email,role,org_id) values($1,$2,'admin',$3)", [user, `${user}@example.test`, org]);
    threadId = (await createWorkThread(org, "Pack actions", "web")).id;
    runId = (await createWorkRun(org, threadId, "hermes", { userId: user, role: "admin" })).id;
    await mkdir(join(root, "packs/action-fixture/actions"), { recursive: true });
    await mkdir(join(root, "packs/action-fixture/policies"), { recursive: true });
    await writeFile(join(root, "packs/action-fixture/pack.yaml"), stringify(manifest));
    for (const operation of operations) await writeFile(join(root, `packs/action-fixture/actions/${operation}.yaml`), stringify({ key: `action.${operation}`, targetRef: kind(operation), kind: kind(operation), description: operation, inputSchema: { type: "object", properties: { value: { type: "number" } } }, adapter: { kind: "pack_connector", connector: "fixture", operation } }));
    await writeFile(join(root, "packs/action-fixture/policies/actions.yaml"), stringify({ key: "policy.actions", targetRef: "fixture_actions", name: "Fixture actions", description: "Test actions", appliesToKinds: operations.map(kind), appliesToScopes: ["external"], mode: "auto", priority: 1, enabled: true, allowedTargets: {}, limits: {} }));
    service = new PackService(org, join(root, "packs"));
    const review = await service.review("action-fixture");
    await service.install("action-fixture", { reviewHash: review.reviewHash });
    const call = (action: string, input: Record<string, unknown>) => service.connectAccount("action-fixture", "fixture", `user:${user}`, action, input);
    await call("configure", { clientId: "fixture", clientSecret: "test-only" });
    const redirectUri = "http://localhost/callback";
    const pending = await call("start", { redirectUri }) as { authorizationUrl: string; state: string };
    const challenge = new URL(pending.authorizationUrl).searchParams.get("code_challenge");
    accountId = (await call("callback", { redirectUri, state: pending.state, code: `${challenge}:one` }) as { accountId: string }).accountId;
    unregister = await registerPackActionRuntime(service);
    admin = createServer(createAdminHandler({ packs: service }));
    await new Promise<void>(resolve => admin.listen(0, "127.0.0.1", resolve));
    adminUrl = `http://127.0.0.1:${(admin.address() as { port: number }).port}`;
  }, 180_000);
  afterAll(async () => {
    unregister?.(); provider.close(); admin?.close();
    await pool().query("delete from organization where id=$1", [org]);
    await pool().query("delete from app_user where id=$1", [user]);
    await pool().end(); vi.unstubAllEnvs(); if (root) await rm(root, { recursive: true, force: true });
  });
  it("runs a read through the real Work MCP server and requires approval for a write", async () => {
    const callThroughWork = async (operation: string, input: Record<string, unknown>) => {
      let result: Record<string, any> = {};
      const discovery = await fetch(`${adminUrl}/admin/packs/actions`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ owner: `user:${user}` }) });
      expect(discovery.ok).toBe(true);
      const { actions } = await discovery.json();
      const controlPlane = Object.assign(Object.create(inProcessControlPlane), { enqueueActionExecute: async ({ actionRequestId }: { actionRequestId: string }) => { await executeApprovedActionRequest(org, actionRequestId); } });
      await runAgentBackend({ orgId: org, threadId, runId, prompt: "Fixture", userMessage: "Fixture", pluginActions: [], packActions: actions, controlPlane,
        workspace: Object.fromEntries(["orgRoot", "skillsRoot", "memoryRoot", "knowledgeRoot", "uploadsRoot", "runsRoot", "threadUploadsRoot", "runRoot", "artifactRoot", "binRoot"].map(key => [key, root])) as AgentWorkspace,
        emit: async () => {}, backend: { id: "hermes", capabilities: { mcpTools: true, sessionResume: false }, run: async options => {
          expect(options.mcpServers).not.toHaveProperty("neko_plugin_actions");
          const server = options.mcpServers!.neko_pack_actions as { instance: { connect: (transport: unknown) => Promise<void> } };
          const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
          await server.instance.connect(serverTransport);
          const client = new Client({ name: "pack-work-test", version: "1" }); await client.connect(clientTransport);
          const response = await client.callTool({ name: kind(operation), arguments: { intent: "Perform the fixture operation", payload: { input, accountId } } });
          expect(response.isError, JSON.stringify(response.content)).not.toBe(true);
          result = JSON.parse((response.content as { text: string }[])[0]!.text);
          await client.close(); return { status: "completed", finalText: "Fixture complete" };
        } },
      });
      return result;
    };
    const read = await callThroughWork("read-record", {});
    expect(read.status).toBe("executed");
    expect(read.outcome.result.output).toEqual({ value: 0, writes: 0 });
    const write = await callThroughWork("write-record", { value: 12 });
    expect(write.status).toBe("pending_approval"); expect(record.writes).toBe(0);
    await approve(write.action_request_id);
    const attempts = await Promise.allSettled([executeApprovedActionRequest(org, write.action_request_id), executeApprovedActionRequest(org, write.action_request_id)]);
    expect(attempts.filter(value => value.status === "fulfilled" && value.value.ok)).toHaveLength(1);
    expect(attempts.filter(value => value.status === "rejected")).toHaveLength(1);
    expect(record).toEqual({ value: 12, writes: 1 });
  }, 180_000);
  it("rejects changed payloads, attachments, policy, actor and account selection", async () => {
    const pending = await propose("write-record", { value: 13 }); await approve(pending.id);
    await pool().query("update action_request set payload=jsonb_set(payload,'{input,value}','99') where id=$1", [pending.id]);
    expect((await executeApprovedActionRequest(org, pending.id)).error).toContain("stale");
    const bytes = Buffer.from("approved attachment");
    const attachment = { name: "price.csv", mediaType: "text/csv", contentBase64: bytes.toString("base64"), sha256: createHash("sha256").update(bytes).digest("hex") };
    const attached = await propose("write-record", {}, { payload: { input: { value: 14 }, accountId, attachments: [attachment] } }); await approve(attached.id);
    const altered = Buffer.from("changed attachment");
    await pool().query("update action_request set payload=jsonb_set(payload,'{attachments}', $2::jsonb) where id=$1", [attached.id, JSON.stringify([{ ...attachment, contentBase64: altered.toString("base64"), sha256: createHash("sha256").update(altered).digest("hex") }])]);
    expect((await executeApprovedActionRequest(org, attached.id)).error).toContain("stale");
    await expect(propose("read-record", {}, { actorUserId: null, actorRole: "admin" })).rejects.toThrow("trusted run");
    await expect(propose("read-record", {}, { payload: { input: {}, accountId: randomUUID() } })).rejects.toThrow("owned");
    const disconnected = await propose("write-record", { value: 15 }); await approve(disconnected.id);
    await pool().query("update pack_account set status='reconnect_required' where id=$1", [accountId]);
    expect((await executeApprovedActionRequest(org, disconnected.id)).error).toContain("owned");
    await pool().query("update pack_account set status='connected' where id=$1", [accountId]);
    const inactive = await propose("write-record", { value: 15 }); await approve(inactive.id);
    await pool().query("update app_user set disabled_at=now() where id=$1", [user]);
    expect((await executeApprovedActionRequest(org, inactive.id)).error).toContain("not active");
    await pool().query("update app_user set disabled_at=null where id=$1", [user]);
    const stale = await propose("write-record", { value: 15 }); await approve(stale.id);
    await writeFile(join(root, "packs/action-fixture/pack.yaml"), stringify({ ...manifest, metadata: { ...manifest.metadata, version: "1.0.1" } }));
    expect((await executeApprovedActionRequest(org, stale.id)).error).toContain("contents changed");
    await writeFile(join(root, "packs/action-fixture/pack.yaml"), stringify(manifest));
    const denied = await propose("write-record", { value: 15 }); await approve(denied.id);
    await pool().query("update action_policy set mode='never' where org_id=$1", [org]);
    expect((await executeApprovedActionRequest(org, denied.id)).error).toContain("denied");
    await expect(propose("write-record", { value: 16 })).rejects.toThrow("denied");
    await pool().query("update action_policy set mode='auto_approve' where org_id=$1", [org]);
    expect(record.writes).toBe(1);
  }, 180_000);
  it("keeps an uncertain provider write and prevents a second execution attempt", async () => {
    const pending = await propose("uncertain-write", { value: 17 }); await approve(pending.id);
    const result = await executeApprovedActionRequest(org, pending.id);
    expect(result.ok).toBe(false); expect(result.outcome?.result?.status).toBe("reconcile_required");
    expect(record).toEqual({ value: 17, writes: 2 });
    const executions = await listActionExecutions(pending.id);
    expect(executions[0]?.result?.status).toBe("reconcile_required");
    await pool().query("update action_request set status='approved' where id=$1", [pending.id]);
    expect((await executeApprovedActionRequest(org, pending.id)).error).toContain("already attempted");
    expect(record.writes).toBe(2);
  }, 180_000);
  it("uses a personal workflow owner and an explicit account, then blocks queued work after uninstall", async () => {
    const workRunId = (await createWorkRun(org, threadId, "hermes", { userId: null, role: "service" })).id;
    const { rows: workflows } = await pool().query("insert into workflow_definition(org_id,name,owner_user_id) values($1,'Pack workflow',$2) returning id", [org, user]);
    const { rows: runs } = await pool().query("insert into workflow_run(org_id,workflow_id,thread_id,work_run_id,trigger_kind) values($1,$2,$3,$4,'cron') returning id", [org, workflows[0].id, threadId, workRunId]);
    const workflowRead = await propose("read-record", {}, { actorUserId: null, actorRole: "service", workRunId, workflowRunId: runs[0].id });
    expect((await executeApprovedActionRequest(org, workflowRead.id)).ok).toBe(true);
    await expect(propose("read-record", {}, { actorUserId: null, actorRole: "service", workRunId, workflowRunId: runs[0].id, payload: { input: {} } })).rejects.toThrow("owned");
    const queued = await propose("write-record", { value: 18 }); await approve(queued.id);
    await service.uninstall("action-fixture");
    expect((await executeApprovedActionRequest(org, queued.id)).ok).toBe(false);
    expect((await getActionRequest(org, queued.id))?.status).toBe("failed"); expect(record.writes).toBe(2);
  }, 180_000);
});
