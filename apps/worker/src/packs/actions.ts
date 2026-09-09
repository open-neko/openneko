import { createHash, createHmac, timingSafeEqual } from "node:crypto";
import { and, db, eq, pool, pack_action_definition, action_request } from "@neko/db";
import { deriveSigningSecret } from "@neko/secret-crypt";
import { canonicalHash, connectorActionSchema, packActionPayloadSchema, packOperationResultSchema } from "@neko/packs";
import { getActionRequest, hasHumanActionApproval, evaluateActionPolicy, listEnabledPolicies, registerActionAdapter, registerActionRequestCreatedHook, updateActionRequestPayload, type ActionRequestRecord, type ActionExecutionOutcome } from "@neko/llm/workflows";
import { PackService } from "./service.js";
import { packConnection } from "./connections.js";
import { runPackConnector } from "./connector-runner.js";

const seal = (id: string, binding: string) => createHmac("sha256", deriveSigningSecret("pack-actions")).update(`${id}:${binding}`).digest("hex");
async function userOwner(orgId: string, userId: string) {
  const { rows } = await pool().query("select id from app_user where id=$1 and org_id=$2 and disabled_at is null", [userId, orgId]);
  if (!rows[0]) throw new Error("Pack action owner is not active in this organization");
  return `user:${userId}`;
}
const ownerFor = async (request: ActionRequestRecord) => {
  if (request.workRunId) {
    const { rows } = await pool().query("select actor_user_id,actor_role from work_run where id=$1 and org_id=$2", [request.workRunId, request.orgId]);
    if (!rows[0] || rows[0].actor_user_id !== request.actorUserId || rows[0].actor_role !== request.actorRole) throw new Error("Pack action actor does not match its trusted run");
  }
  if (request.actorUserId) return userOwner(request.orgId, request.actorUserId);
  if (request.actorRole === "admin") return "solo";
  if (request.actorRole === "service" && request.workflowRunId) {
    const { rows } = await pool().query("select w.owner_user_id from workflow_run r join workflow_definition w on w.id=r.workflow_id and w.org_id=r.org_id where r.id=$1 and r.org_id=$2 and r.work_run_id=$3", [request.workflowRunId, request.orgId, request.workRunId]);
    if (rows[0]?.owner_user_id) return userOwner(request.orgId, rows[0].owner_user_id);
    if (rows[0]) return `workflow:${request.workflowRunId}`;
  }
  throw new Error("Pack actions require an explicit execution owner");
};

async function definitionFor(orgId: string, kind: string) {
  const [row] = await db().select().from(pack_action_definition).where(and(eq(pack_action_definition.org_id, orgId), eq(pack_action_definition.kind, kind))).limit(1);
  if (!row?.enabled || row.readiness !== "ready") throw new Error("Pack action is not ready");
  return { row, definition: connectorActionSchema.parse(row.definition) };
}

export async function runPackAction(request: ActionRequestRecord, executionId?: string, service = new PackService(request.orgId)): Promise<ActionExecutionOutcome | ActionRequestRecord> {
  const { definition } = await definitionFor(request.orgId, request.kind);
  const packId = request.kind.split(".")[1]!;
  return service.withConnector(packId, definition.adapter.connector, async ctx => {
    const currentDefinition = await definitionFor(request.orgId, request.kind);
    if (canonicalHash(currentDefinition.definition) !== canonicalHash(definition)) throw new Error("Pack action changed during dispatch");
    const artifact = ctx.bundle.artifacts.find(value => value.kind === "action" && value.targetRef === request.kind);
    if (!artifact || artifact.hash !== currentDefinition.row.definition_hash || canonicalHash(artifact.content) !== canonicalHash(currentDefinition.definition)) throw new Error("Installed pack action definition changed");
    const operation = ctx.connector.operations.find(value => value.id === definition.adapter.operation);
    if (!operation) throw new Error("Pack operation is no longer declared");
    const policy = evaluateActionPolicy({ scope: request.scope, kind: request.kind, target: request.target, riskLevel: request.riskLevel }, await listEnabledPolicies(request.orgId));
    if (policy.decision === "deny" || policy.decision === "no_policy") throw new Error("Pack action is denied by current policy");
    const payload = packActionPayloadSchema.parse(request.payload);
    const { _pack, ...data } = payload;
    if (Object.hasOwn(data.input, "attachments")) throw new Error("Pack attachments must use the content-bound attachment field");
    if (Buffer.byteLength(JSON.stringify(data)) > 512 * 1024) throw new Error("Pack action payload exceeds 512 KiB");
    for (const attachment of data.attachments ?? []) {
      const bytes = Buffer.from(attachment.contentBase64, "base64");
      if (bytes.toString("base64") !== attachment.contentBase64 || createHash("sha256").update(bytes).digest("hex") !== attachment.sha256) throw new Error("Pack attachment content does not match its digest");
    }
    const owner = await ownerFor(request);
    if (ctx.connector.auth) {
      const accounts = await packConnection({ ...ctx, owner }, "list", {}) as { configured: boolean; accounts: { id: string; status: string }[] };
      if (!accounts.configured || !data.accountId || !accounts.accounts.some(value => value.id === data.accountId && value.status === "connected")) throw new Error("Select a connected pack account owned by the execution user");
    } else if (data.accountId) throw new Error("This pack operation does not use an account");
    const binding = canonicalHash({ installationId: ctx.installationId, bundle: ctx.bundle.bundleHash,
      definition: currentDefinition.row.definition_hash, connector: ctx.connector, owner, kind: request.kind, scope: request.scope,
      target: request.target, summary: request.summary, intent: request.intent, risk: request.riskLevel, data });
    if (!executionId) {
      if (request.scope !== "external") throw new Error("Pack operations use the external policy scope");
      if ((operation.effect === "write" || policy.decision !== "allow") && request.status === "approved") await db().update(action_request).set({ status: "pending_approval" }).where(eq(action_request.id, request.id));
      return updateActionRequestPayload({ id: request.id, orgId: request.orgId, payload: { ...data, _pack: { binding, seal: seal(request.id, binding) } } });
    }
    const actual = _pack?.seal ? Buffer.from(_pack.seal, "hex") : Buffer.alloc(0);
    const expected = Buffer.from(seal(request.id, binding), "hex");
    if (_pack?.binding !== binding || actual.length !== expected.length || !timingSafeEqual(actual, expected)) throw new Error("Pack action approval is stale or changed; submit a new request");
    const latest = await getActionRequest(request.orgId, request.id);
    if (latest?.status !== "approved" || canonicalHash(latest.payload) !== canonicalHash(request.payload)) throw new Error("Pack action is no longer approved");
    if ((operation.effect === "write" || policy.decision !== "allow") && !await hasHumanActionApproval(latest)) throw new Error("Pack writes require human approval");
    // The executor records an attempt before calling the adapter. Under the pack
    // lock, only the earliest attempt may reach the provider, even after a crash.
    const { rows: attempts } = await ctx.sql.query("select id from action_execution where org_id=$1 and action_request_id=$2 order by created_at,id limit 1", [request.orgId, request.id]);
    if (attempts[0]?.id !== executionId) throw new Error("Pack action was already attempted; reconcile its saved result before creating another request");
    const credential = ctx.connector.auth ? await packConnection({ ...ctx, owner }, "credential", { accountId: data.accountId }) : undefined;
    let result;
    try {
      result = packOperationResultSchema.parse(await runPackConnector(ctx.connector, { operation: operation.id, action: { requestId: request.id, executionId }, input: { ...data.input, ...(data.attachments ? { attachments: data.attachments } : {}) }, ...(credential ? { credential } : {}) }, operation.effect === "write"));
    } catch {
      result = { status: "reconcile_required" as const, receipt: {}, output: { message: "Connector result is uncertain. Do not repeat this operation." } };
    }
    return { result, commandOrOperation: `${packId}/${ctx.connector.id}/${operation.id}`, ...(result.status !== "succeeded" ? { error: `Pack action ${result.status}; inspect the saved provider receipt` } : {}) };
  });
}

export async function registerPackActionRuntime(service?: PackService) {
  const adapter = async ({ request, executionId }: { request: ActionRequestRecord; executionId?: string }) => {
    if (!executionId) throw new Error("Pack action requires a recorded execution attempt");
    return await runPackAction(request, executionId, service) as ActionExecutionOutcome;
  };
  const rows = await db().select({ kind: pack_action_definition.kind, definition: pack_action_definition.definition }).from(pack_action_definition);
  for (const row of rows) if (connectorActionSchema.safeParse(row.definition).success) registerActionAdapter(row.kind, adapter);
  return registerActionRequestCreatedHook(async request => {
    if (!request.kind.startsWith("pack.")) return;
    const prepared = await runPackAction(request, undefined, service) as ActionRequestRecord;
    registerActionAdapter(request.kind, adapter);
    return prepared;
  });
}


/** Pack-only discovery. It does not read or register plugins. */
export async function packActionDescriptors(orgId: string, owner: string, service = new PackService(orgId)) {
  const rows = await db().select().from(pack_action_definition).where(and(eq(pack_action_definition.org_id, orgId), eq(pack_action_definition.enabled, true), eq(pack_action_definition.readiness, "ready")));
  const descriptors = [];
  for (const row of rows) {
    const parsed = connectorActionSchema.safeParse(row.definition);
    if (!parsed.success) continue;
    const definition = parsed.data;
    const descriptor = await service.withConnector(row.kind.split(".")[1]!, definition.adapter.connector, async ctx => {
      const artifact = ctx.bundle.artifacts.find(value => value.kind === "action" && value.targetRef === row.kind);
      if (!artifact || artifact.hash !== row.definition_hash || canonicalHash(artifact.content) !== canonicalHash(definition)) throw new Error("Pack action definition changed");
      const operation = ctx.connector.operations.find(value => value.id === definition.adapter.operation);
      if (!operation) throw new Error("Pack operation is not declared");
      const accounts = ctx.connector.auth ? await packConnection({ ...ctx, owner }, "list", {}) : undefined;
      return { kind: row.kind, scope: "external" as const, default_mode: operation.effect === "write" ? "ask" as const : "auto" as const,
        description: `${definition.description} Payload: {input, accountId?, attachments?}. Input schema: ${JSON.stringify(definition.inputSchema)}.${accounts ? ` Select an account owned by the execution user: ${JSON.stringify(accounts)}.` : ""}`,
        ...(definition.example ? { example: definition.example } : {}) };
    });
    descriptors.push(descriptor);
  }
  return descriptors;
}
