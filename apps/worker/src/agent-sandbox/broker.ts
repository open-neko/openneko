import {
  createServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import type { AgentEvent } from "@neko/llm";
import type { AgentControlPlane } from "@neko/llm/work";

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
      return send(
        res,
        200,
        await cp.rememberWorkMemory({
          ...body,
          orgId: binding.orgId,
        } as Parameters<AgentControlPlane["rememberWorkMemory"]>[0]),
      );
    case "/v1/memory/search":
      return send(
        res,
        200,
        await cp.searchWorkMemoryByContext({
          ...body,
          orgId: binding.orgId,
        } as Parameters<AgentControlPlane["searchWorkMemoryByContext"]>[0]),
      );
    case "/v1/events":
      await deps.onEvents(binding, (body.events as AgentEvent[]) ?? []);
      return send(res, 200, { ok: true });
    default:
      return send(res, 404, { error: "not_found" });
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
