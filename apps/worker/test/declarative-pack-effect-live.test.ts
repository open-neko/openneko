import { createServer } from "node:http";
import { randomUUID } from "node:crypto";
import { db, eq, organization, work_thread, work_run, data_source, pack_install, pack_artifact, pack_action_definition, action_policy, pool } from "@neko/db";
import { boss, enqueue, QUEUE, type ActionExecutePayload } from "@neko/db/jobs";
import { verifyGraphjinToken } from "@neko/llm/graphjin";
import { listPackActionDescriptors } from "@neko/llm/work";
import { approveActionRequest, executeApprovedActionRequest, registerFallbackActionAdapterResolver } from "@neko/llm/workflows";
import { startAgentBroker } from "../../../packages/llm/src/work/broker";
import { inProcessControlPlane } from "../../../packages/llm/src/work/control-plane";
import { resolveDeclarativePackActionAdapter } from "../src/packs/declarative-action-runtime";
import { runActionExecute } from "../src/jobs/action-execute";
import { expect, it } from "vitest";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("executes a declarative pack effect once and fences a lost provider response", async () => {
  if (process.env.NEKO_PG_PORT !== "18119") throw Error("isolated database required");
  const orgId = `pack-effect-${randomUUID()}`;
  const threadId = randomUUID();
  const runId = randomUUID();
  const kind = "fixture.graphjin_api_change";
  let providerCalls = 0;
  let responseLoss = false;
  const provider = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    const token = String(req.headers.authorization ?? "").replace(/^Bearer /, "");
    const query = JSON.parse(body) as { query: string; variables: { call: { path: { id: string }; body: { value: number } } } };
    if (req.method !== "POST" || req.headers["x-role"] !== "pack_api_executor" ||
        verifyGraphjinToken(token, orgId)?.role !== "pack_api_executor" ||
        !query.query.includes("mutation ExecutePackAction") || !query.query.includes("fixture_update_value") ||
        query.variables.call.path.id !== "row-42") {
      res.writeHead(403).end();
      return;
    }
    providerCalls++;
    if (responseLoss) {
      res.destroy();
      return;
    }
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify({ data: { fixture_update_value: {
      ok: true, status_code: 200, operation_id: "fixtureUpdate", request_id: `provider-${providerCalls}`,
      response_json: { value: query.variables.call.body.value },
    } } }));
  });
  await new Promise<void>(resolve => provider.listen(0, "127.0.0.1", resolve));
  const endpoint = `http://127.0.0.1:${(provider.address() as { port: number }).port}/api/v1/graphql`;
  let broker: Awaited<ReturnType<typeof startAgentBroker>> | undefined;
  let queue: Awaited<ReturnType<typeof boss>> | undefined;
  const unregister = registerFallbackActionAdapterResolver(resolveDeclarativePackActionAdapter);
  try {
    await db().insert(organization).values({ id: orgId, name: "Declarative pack effect fixture" });
    await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Pack effect" });
    await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
    await pool().query("INSERT INTO harness_run_journal(org_id,run_id,fingerprint) VALUES($1,$2,$3)", [orgId, runId, "d".repeat(64)]);
    const [source] = await db().insert(data_source).values({ org_id: orgId, kind: "graphjin", graphql_url: endpoint, auth_mode: "jwt" }).returning();
    const [install] = await db().insert(pack_install).values({ org_id: orgId, pack_id: "fixture-pack", version: "1", status: "installed", manifest_hash: "fixture", config: {
      _runtime: { source: { id: source!.id, graphqlUrl: endpoint, authMode: "jwt" } },
    } }).returning();
    await db().insert(pack_artifact).values({ org_id: orgId, pack_install_id: install!.id,
      artifact_kind: "action", artifact_key: kind, target_ref: kind, desired_hash: "fixture", last_applied_hash: "fixture" });
    await db().insert(pack_action_definition).values({ org_id: orgId, kind, readiness: "ready", definition_hash: "fixture",
      definition: { kind, description: "Update a synthetic provider row", inputSchema: {
        type: "object", required: ["operation", "path", "body"], additionalProperties: false,
        properties: { operation: { type: "string", enum: ["update"] }, path: { type: "object" }, body: { type: "object" } },
      }, adapter: { kind: "graphjin_api_operation", operations: { update: {
        operationId: "fixtureUpdate", mutationRoot: "fixture_update_value",
      } } } } });
    await db().insert(action_policy).values({ org_id: orgId, name: "Pack effect approval", mode: "approval_required",
      applies_to_kinds: [kind], applies_to_scopes: ["external"] });
    expect((await listPackActionDescriptors(orgId, { forHarness: true })).map(action => action.kind)).toEqual([kind]);
    broker = await startAgentBroker({ port: 0, hostAlias: "127.0.0.1", controlPlane: inProcessControlPlane });
    queue = await boss();
    await queue.createQueue(QUEUE.ACTION_EXECUTE);
    await queue.work<ActionExecutePayload>(QUEUE.ACTION_EXECUTE, async jobs => {
      for (const job of jobs) await runActionExecute(job.data);
    });
    const token = broker.tokenFor({ orgId, runId, threadId, kind: "work", profile: "harness-governed",
      actionGrants: [{ kind, source: "pack", scope: "external" }] });
    const propose = async (operationId: number, value: number) => {
      const response = await fetch(`http://127.0.0.1:${broker!.port}/v1/harness/propose`, { method: "POST",
        headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
        body: JSON.stringify({ operationId, instruction: JSON.stringify({ action: kind,
          arguments: { operation: "update", path: { id: "row-42" }, body: { value } }, summary: `Set value to ${value}` }) }) });
      expect(response.status).toBe(200);
      return response.json() as Promise<{ id: string; status: string }>;
    };
    const first = await propose(1, 42);
    expect(first.status).toBe("pending_approval");
    expect(providerCalls).toBe(0);
    await approveActionRequest({ orgId, id: first.id, approverUserId: null, approver: { userId: null, role: "admin" } });
    await enqueue(QUEUE.ACTION_EXECUTE, { orgId, actionRequestId: first.id }, { retryLimit: 0 });
    let applied = await pool().query("SELECT status FROM action_request WHERE org_id=$1 AND id=$2", [orgId, first.id]);
    for (let attempt = 0; attempt < 100 && applied.rows[0]?.status !== "executed"; attempt++) {
      await new Promise(resolve => setTimeout(resolve, 100));
      applied = await pool().query("SELECT status FROM action_request WHERE org_id=$1 AND id=$2", [orgId, first.id]);
    }
    expect(applied.rows[0]?.status).toBe("executed");
    const restored = await executeApprovedActionRequest(orgId, first.id);
    expect(restored).toMatchObject({ ok: true, outcome: { externalRef: "provider-1", result: { operation: "update", statusCode: 200 } } });
    expect(providerCalls).toBe(1);

    responseLoss = true;
    const second = await propose(2, 43);
    await approveActionRequest({ orgId, id: second.id, approverUserId: null, approver: { userId: null, role: "admin" } });
    const unknown = await executeApprovedActionRequest(orgId, second.id);
    expect(unknown).toMatchObject({ ok: false, error: expect.stringContaining("outcome unknown") });
    expect((await executeApprovedActionRequest(orgId, second.id)).ok).toBe(false);
    expect(providerCalls).toBe(2);
    const executions = await pool().query("SELECT action_request_id,status FROM action_execution WHERE org_id=$1 ORDER BY started_at", [orgId]);
    expect(executions.rows).toHaveLength(2);
    expect(executions.rows.map(row => row.status).sort()).toEqual(["failed", "succeeded"]);
  } finally {
    await queue?.stop({ graceful: true, timeout: 5_000 });
    unregister();
    await broker?.close();
    await new Promise<void>(resolve => provider.close(() => resolve()));
    await db().delete(organization).where(eq(organization.id, orgId)).catch(() => {});
    await pool().end();
  }
}, 30_000);
