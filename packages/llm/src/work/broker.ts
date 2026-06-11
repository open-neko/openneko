import {
  createServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import { randomUUID } from "node:crypto";
import type { AgentEvent } from "../agent-backend";
import { inProcessControlPlane, type AgentControlPlane } from "./control-plane";

/** What a per-run bearer token resolves to — the trust binding. */
export interface RunBinding {
  runId: string;
  orgId: string;
}

export interface AgentBrokerDeps {
  /** Host-side control plane (real DB/pg-boss access). */
  controlPlane: AgentControlPlane;
  /** Validate a bearer token → its run binding (undefined = reject). */
  resolveRun(token: string): RunBinding | undefined;
  /** Host-side event sink: scrub + persist. Scrubbing stays here so a
   *  sandboxed agent can't leak a secret it was never given. */
  onEvents(binding: RunBinding, events: AgentEvent[]): Promise<void>;
}

/**
 * Localhost HTTP/JSON broker — the ONLY channel a sandboxed agent turn has
 * back to the trusted control plane. orgId and workRunId are always taken
 * from the token binding, never from the request body, so a compromised
 * sandbox can't act cross-run or cross-org.
 */
export function createAgentBroker(deps: AgentBrokerDeps): Server {
  return createServer((req, res) => {
    void handle(deps, req, res).catch((err) =>
      send(res, 500, { error: err instanceof Error ? err.message : String(err) }),
    );
  });
}

async function handle(
  deps: AgentBrokerDeps,
  req: IncomingMessage,
  res: ServerResponse,
): Promise<void> {
  const auth = req.headers.authorization ?? "";
  const token = auth.startsWith("Bearer ") ? auth.slice(7) : "";
  const binding = deps.resolveRun(token);
  if (!binding) return send(res, 401, { error: "unauthorized" });
  if (req.method !== "POST") return send(res, 405, { error: "method_not_allowed" });

  const body = (await readJson(req)) as Record<string, unknown>;
  const path = (req.url ?? "").split("?")[0];
  const cp = deps.controlPlane;

  // SEC5: every authenticated gateway call is audited with the dual
  // identity (human principal + agent backend). Best-effort — auditing
  // must never fail the call itself.
  void auditControlPlaneCall(binding, path);

  switch (path) {
    case "/v1/policy/evaluate":
      return send(
        res,
        200,
        await cp.evaluateActionPolicy({
          ...body,
          orgId: binding.orgId,
        } as Parameters<AgentControlPlane["evaluateActionPolicy"]>[0]),
      );
    case "/v1/action/request":
      return send(
        res,
        200,
        await cp.createActionRequest({
          ...body,
          orgId: binding.orgId,
          workRunId: binding.runId,
        } as Parameters<AgentControlPlane["createActionRequest"]>[0]),
      );
    case "/v1/action/enqueue":
      await cp.enqueueActionExecute({
        orgId: binding.orgId,
        actionRequestId: String(body.actionRequestId),
      });
      return send(res, 200, { ok: true });
    case "/v1/memory/remember":
      // CV2: the memory layer is derived server-side from the bound run's
      // actor — an agent-supplied userId is never trusted.
      delete body.userId;
      return send(
        res,
        200,
        await cp.rememberWorkMemory({
          ...body,
          orgId: binding.orgId,
          runId: binding.runId,
        } as Parameters<AgentControlPlane["rememberWorkMemory"]>[0]),
      );
    case "/v1/memory/search":
      delete body.userId;
      return send(
        res,
        200,
        await cp.searchWorkMemoryByContext({
          ...body,
          orgId: binding.orgId,
          runId: binding.runId,
        } as Parameters<AgentControlPlane["searchWorkMemoryByContext"]>[0]),
      );
    case "/v1/workflow/save":
      return send(
        res,
        200,
        await cp.saveWorkflowWithTrigger({
          ...body,
          orgId: binding.orgId,
          createdByRunId: binding.runId,
        } as Parameters<AgentControlPlane["saveWorkflowWithTrigger"]>[0]),
      );
    case "/v1/workflow/list":
      return send(
        res,
        200,
        await cp.listWorkflowsWithTriggers({
          orgId: binding.orgId,
          limit: typeof body.limit === "number" ? body.limit : undefined,
        }),
      );
    case "/v1/workflow/delete":
      // orgId comes from the token binding, never the body — a sandbox
      // can't delete another org's workflow by passing its id.
      return send(
        res,
        200,
        await cp.deleteWorkflow({
          orgId: binding.orgId,
          workflowId: String(body.workflowId),
        }),
      );
    case "/v1/rule/save":
      return send(
        res,
        200,
        await cp.upsertActionPolicyByName({
          ...body,
          orgId: binding.orgId,
          createdByRunId: binding.runId,
        } as Parameters<AgentControlPlane["upsertActionPolicyByName"]>[0]),
      );
    case "/v1/rule/list":
      return send(
        res,
        200,
        await cp.listActionPolicies({
          orgId: binding.orgId,
          limit: typeof body.limit === "number" ? body.limit : undefined,
        }),
      );
    case "/v1/plugins/list":
      return send(res, 200, await cp.listPlugins({ orgId: binding.orgId }));
    case "/v1/users/list":
      return send(res, 200, await cp.listUsers({ orgId: binding.orgId }));
    case "/v1/channels/list":
      return send(res, 200, await cp.listChannels({ orgId: binding.orgId }));
    case "/v1/datasources/list":
      return send(res, 200, await cp.listDataSources({ orgId: binding.orgId }));
    case "/v1/source-graph/describe":
      return send(
        res,
        200,
        await cp.describeSourceGraph({ orgId: binding.orgId }),
      );
    case "/v1/source-secrets/names":
      return send(
        res,
        200,
        await cp.listSourceSecretNames({ orgId: binding.orgId }),
      );
    case "/v1/audit/list":
      // ADM4: the admin gate runs on the BOUND run's actor — the
      // sandbox can't claim someone else's run.
      return send(
        res,
        200,
        await cp.listAuditTrail({
          orgId: binding.orgId,
          runId: binding.runId,
          limit: typeof body.limit === "number" ? body.limit : undefined,
        }),
      );
    case "/v1/events":
      await deps.onEvents(binding, (body.events as AgentEvent[]) ?? []);
      return send(res, 200, { ok: true });
    default:
      return send(res, 404, { error: "not_found" });
  }
}

// Per-run dual-identity cache so auditing costs one DB lookup per run,
// not per call. Bounded; runs are short-lived.
const runIdentityCache = new Map<
  string,
  { userId: string | null; role: string | null; backend: string | null }
>();
const RUN_IDENTITY_CACHE_MAX = 500;

async function auditControlPlaneCall(
  binding: RunBinding,
  path: string,
): Promise<void> {
  try {
    const { control_plane_audit, db, eq, work_run } = await import("@neko/db");
    let identity = runIdentityCache.get(binding.runId);
    if (!identity) {
      const [run] = await db()
        .select({
          userId: work_run.actor_user_id,
          role: work_run.actor_role,
          backend: work_run.backend,
        })
        .from(work_run)
        .where(eq(work_run.id, binding.runId))
        .limit(1);
      identity = run ?? { userId: null, role: null, backend: null };
      if (runIdentityCache.size >= RUN_IDENTITY_CACHE_MAX) {
        runIdentityCache.clear();
      }
      runIdentityCache.set(binding.runId, identity);
    }
    await db().insert(control_plane_audit).values({
      org_id: binding.orgId,
      run_id: binding.runId,
      path,
      actor_user_id: identity.userId,
      actor_role: identity.role,
      backend: identity.backend,
    });
  } catch (err) {
    console.warn(
      `[agent-broker] audit insert failed (call proceeded): ${err instanceof Error ? err.message : err}`,
    );
  }
}

function readJson(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    let data = "";
    req.on("data", (c: Buffer) => {
      data += c.toString("utf8");
      if (data.length > 8_000_000) req.destroy();
    });
    req.on("end", () => {
      try {
        resolve(data ? JSON.parse(data) : {});
      } catch (err) {
        reject(err);
      }
    });
    req.on("error", reject);
  });
}

function send(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

/** A running broker + its per-run token registry. */
export interface AgentBrokerHandle {
  /** URL the sandboxed agent reaches the broker at (host alias + port). */
  readonly url: string;
  /** Actual listening port (resolved when port 0 / ephemeral is requested). */
  readonly port: number;
  /** Mint-or-reuse a per-run bearer token; pass the return into the sandbox. */
  tokenFor(binding: RunBinding): string;
  /** Drop a finished run's token. */
  release(runId: string): void;
  close(): Promise<void>;
}

export interface StartAgentBrokerOptions {
  controlPlane: AgentControlPlane;
  /** Host-side scrub + persist of events posted to /v1/events. Default no-op
   *  (agent events normally stream over the launcher's stdout channel). */
  onEvents?: (binding: RunBinding, events: AgentEvent[]) => Promise<void>;
  /** Host alias the sandbox uses to reach this broker. Default
   *  host.openshell.internal (the OpenShell sandbox's view of the host). */
  hostAlias?: string;
  /** Port to listen on; must match the port published to the host in compose
   *  so the sandbox can reach host.openshell.internal:<port>. */
  port: number;
}

/**
 * Start a long-lived broker bound to a host control plane, with an in-memory
 * per-run token registry. The worker/web start ONE per process; each run mints
 * a token via {@link AgentBrokerHandle.tokenFor} and releases it on completion.
 */
export async function startAgentBroker(
  opts: StartAgentBrokerOptions,
): Promise<AgentBrokerHandle> {
  const tokens = new Map<string, RunBinding>(); // token -> binding
  const byRun = new Map<string, string>(); // runId -> token

  const server = createAgentBroker({
    controlPlane: opts.controlPlane,
    resolveRun: (token) => tokens.get(token),
    onEvents: opts.onEvents ?? (async () => {}),
  });

  await new Promise<void>((resolve, reject) => {
    const onErr = (e: Error) => reject(e);
    server.once("error", onErr);
    server.listen(opts.port, "0.0.0.0", () => {
      server.removeListener("error", onErr);
      resolve();
    });
  });

  const addr = server.address();
  const port = typeof addr === "object" && addr ? addr.port : opts.port;
  const host = opts.hostAlias ?? "host.openshell.internal";

  return {
    url: `http://${host}:${port}`,
    port,
    tokenFor(binding) {
      const existing = byRun.get(binding.runId);
      if (existing) return existing;
      const token = randomUUID();
      tokens.set(token, binding);
      byRun.set(binding.runId, token);
      return token;
    },
    release(runId) {
      const token = byRun.get(runId);
      if (token) {
        tokens.delete(token);
        byRun.delete(runId);
      }
    },
    async close() {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

let brokerSingleton: AgentBrokerHandle | undefined;
let brokerStarting: Promise<AgentBrokerHandle | undefined> | undefined;

/**
 * Lazily start the per-process agent broker, bound to the in-process control
 * plane. SEC9: OpenShell is the only agent runtime, so every control-plane
 * host runs a broker. The listen port (OPENNEKO_BROKER_PORT, default 4199)
 * MUST be published to the host in compose so the sandbox can reach it at
 * host.openshell.internal:<port>. Idempotent — one broker per process,
 * shared by runWorkRun (channel runs) and the web chat route.
 */
export function ensureAgentBroker(): Promise<AgentBrokerHandle | undefined> {
  if (brokerSingleton) return Promise.resolve(brokerSingleton);
  if (!brokerStarting) {
    brokerStarting = startAgentBroker({
      controlPlane: inProcessControlPlane,
      hostAlias: process.env.OPENNEKO_BROKER_HOST_ALIAS || undefined,
      port: Number(process.env.OPENNEKO_BROKER_PORT) || 4199,
    })
      .then((h) => {
        brokerSingleton = h;
        return h;
      })
      .catch((e) => {
        brokerStarting = undefined; // allow a retry on the next run
        throw e;
      });
  }
  return brokerStarting;
}
