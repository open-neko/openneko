/**
 * Admin HTTP handler for the worker process.
 *
 * Routes:
 *   POST /admin/graphjin/group-grants → apply group grants now ({ immediate: true }) or in a batch.
 *   POST /admin/action-requests/create → persist + run worker-owned preflight
 *                               before returning an approval-card-safe id.
 *
 * Extracted from index.ts so the handler can be unit-tested without
 * booting pg-boss / the agent stack.
 */

import type { IncomingMessage, ServerResponse } from "node:http";
import type { ConnectorCredential } from "@neko/secrets";
import type {
  BeginConnectParams,
  CompleteConnectParams,
} from "./signin/types.js";

/**
 * Connector OAuth surface exposed to the web app (the Scalekit workspace
 * consent today). Credentials are persisted in the secrets file by the
 * worker, not by the web.
 */
export interface ConnectHandlerSurface {
  getConnectProviders(): Array<{
    pluginId: string;
    pluginName: string;
    providerLabel: string;
    scopes: string[];
    flow: string;
    credentialScope: string;
  }>;
  getOperatorConnectStatus(
    operatorId: string,
  ): Array<{ pluginName: string; connectedAt: string; scopes?: string[] }>;
  getDeploymentConnectStatus(): Array<{
    pluginName: string;
    connectedAt: string;
    scopes?: string[];
  }>;
  beginConnect(
    pluginName: string,
    params: BeginConnectParams,
  ): Promise<{ authorizationUrl: string; oauthState?: string }>;
  completeConnect(
    pluginName: string,
    params: CompleteConnectParams,
  ): Promise<ConnectorCredential>;
  disconnect(pluginName: string, operatorId: string): Promise<boolean>;
}

export type AdminHandlerOptions = {
  /**
   * Connector surface — wired to the SignInService (Scalekit workspace).
   * Absent in tests, in which case /admin/connect/* routes return 503.
   */
  connect?: ConnectHandlerSurface | null;
  /**
   * External-event ingress (OL3): watchers and integrations POST
   * /admin/events/external to fire external_event subscriptions.
   */
  events?: ExternalEventHandlerSurface | null;
  /** Trusted web→worker action creation so worker-owned preflight hooks run. */
  actionRequests?: ActionRequestHandlerSurface | null;
  /** Writes group grants into the GraphJin config, now or batched. */
  groupGrants?: GroupGrantsHandlerSurface | null;
};

export interface GroupGrantsHandlerSurface {
  apply(): Promise<unknown>;
  schedule(): void;
  enable(actorUserId: string | null): Promise<unknown>;
  disable(actorUserId: string | null): Promise<unknown>;
  apiOperations(): Promise<string[]>;
}

export interface ActionRequestHandlerSurface {
  create(input: Record<string, unknown>): Promise<{ id: string; status: string }>;
}

export interface ExternalEventHandlerSurface {
  dispatchExternal(input: {
    orgId: string;
    name: string;
    source: string | null;
    payload: Record<string, unknown>;
    dedupeKey?: string;
  }): Promise<{ matched: number; enqueued: number }>;
}

export function createAdminHandler(opts: AdminHandlerOptions = {}) {
  const connect = opts.connect ?? null;
  const events = opts.events ?? null;
  const actionRequests = opts.actionRequests ?? null;
  const groupGrants = opts.groupGrants ?? null;

  return function handle(req: IncomingMessage, res: ServerResponse) {
    if (req.url === "/admin/graphjin/api-operations" && req.method === "GET") {
      void (async () => {
        if (!groupGrants) return json(res, 503, { error: "group grants are not configured" });
        try {
          json(res, 200, { operations: await groupGrants.apiOperations() });
        } catch (err) {
          json(res, 500, { error: err instanceof Error ? err.message : String(err) });
        }
      })();
      return;
    }
    if (req.url?.startsWith("/admin/graphjin/group-grants") && req.method === "POST") {
      void (async () => {
        if (!groupGrants) return json(res, 503, { error: "group grants are not configured" });
        try {
          const body = (await readJson(req)) as { immediate?: unknown; actorUserId?: unknown };
          const actorUserId = typeof body.actorUserId === "string" ? body.actorUserId : null;
          if (req.url === "/admin/graphjin/group-grants/enable") return json(res, 200, await groupGrants.enable(actorUserId));
          if (req.url === "/admin/graphjin/group-grants/disable") return json(res, 200, await groupGrants.disable(actorUserId));
          if (req.url !== "/admin/graphjin/group-grants") return json(res, 404, { error: "not found" });
          if (body.immediate === true) return json(res, 200, await groupGrants.apply());
          groupGrants.schedule();
          json(res, 202, { scheduled: true });
        } catch (err) {
          json(res, 500, { error: err instanceof Error ? err.message : String(err) });
        }
      })();
      return;
    }
    if (req.method === "GET" && req.url === "/admin/connect/providers") {
      handleConnectProviders(res, connect);
      return;
    }
    if (req.method === "GET" && req.url === "/admin/connect/deployment/status") {
      handleDeploymentConnectStatus(res, connect);
      return;
    }
    const statusMatch = req.method === "GET" && req.url?.startsWith("/admin/connect/status/");
    if (statusMatch) {
      handleConnectStatus(res, connect, req.url!);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/connect/begin") {
      void handleConnectBegin(req, res, connect);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/connect/complete") {
      void handleConnectComplete(req, res, connect);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/connect/disconnect") {
      void handleConnectDisconnect(req, res, connect);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/events/external") {
      void handleExternalEvent(req, res, events);
      return;
    }
    if (
      req.method === "POST" &&
      req.url === "/admin/action-requests/create"
    ) {
      void handleActionRequestCreate(req, res, actionRequests);
      return;
    }
    res.writeHead(404).end();
  };
}

async function handleActionRequestCreate(
  req: IncomingMessage,
  res: ServerResponse,
  actionRequests: ActionRequestHandlerSurface | null,
): Promise<void> {
  if (!actionRequests) {
    json(res, 503, { error: "action request preflight is not ready" });
    return;
  }
  try {
    const body = await readJson(req);
    if (!body || typeof body !== "object" || Array.isArray(body)) {
      json(res, 400, { error: "request body must be a JSON object" });
      return;
    }
    json(
      res,
      200,
      await actionRequests.create(body as Record<string, unknown>),
    );
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

async function handleExternalEvent(
  req: IncomingMessage,
  res: ServerResponse,
  events: ExternalEventHandlerSurface | null,
) {
  if (!events) {
    json(res, 503, { error: "external-event ingress unavailable" });
    return;
  }
  const body = await readJson(req).catch(() => null);
  if (!body || typeof body !== "object") {
    json(res, 400, { error: "request body must be JSON" });
    return;
  }
  const { orgId, name, source, payload, dedupeKey } = body as Record<
    string,
    unknown
  >;
  if (typeof orgId !== "string" || !orgId) {
    json(res, 400, { error: "orgId (string) is required" });
    return;
  }
  if (typeof name !== "string" || !name) {
    json(res, 400, { error: "name (string) is required" });
    return;
  }
  try {
    const result = await events.dispatchExternal({
      orgId,
      name,
      source: typeof source === "string" && source ? source : null,
      payload:
        payload && typeof payload === "object" && !Array.isArray(payload)
          ? (payload as Record<string, unknown>)
          : {},
      ...(typeof dedupeKey === "string" && dedupeKey ? { dedupeKey } : {}),
    });
    json(res, 200, result);
  } catch (err) {
    json(res, 500, {
      error: err instanceof Error ? err.message : String(err),
    });
  }
}

// ─── Connect (per-operator OAuth) ──────────────────────────────────────

function handleConnectProviders(
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
) {
  if (!connect) {
    json(res, 200, { providers: [] });
    return;
  }
  json(res, 200, { providers: connect.getConnectProviders() });
}

function handleConnectStatus(
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
  url: string,
) {
  if (!connect) {
    json(res, 200, { connected: [] });
    return;
  }
  const operatorId = decodeURIComponent(
    url.slice("/admin/connect/status/".length).split(/[?#]/, 1)[0] ?? "",
  );
  if (!operatorId) {
    json(res, 400, { error: "operatorId path segment required" });
    return;
  }
  json(res, 200, { connected: connect.getOperatorConnectStatus(operatorId) });
}

function handleDeploymentConnectStatus(
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
) {
  if (!connect) {
    json(res, 200, { connected: [] });
    return;
  }
  json(res, 200, { connected: connect.getDeploymentConnectStatus() });
}

async function handleConnectBegin(
  req: IncomingMessage,
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
) {
  if (!connect) {
    json(res, 503, { error: "connectors are unavailable" });
    return;
  }
  const body = (await readJson(req).catch(() => null)) as Record<string, unknown> | null;
  if (!body || typeof body !== "object") {
    json(res, 400, { error: "request body must be JSON" });
    return;
  }
  const pluginName = body.pluginName;
  const params = body.params;
  if (typeof pluginName !== "string" || !pluginName) {
    json(res, 400, { error: "pluginName (string) is required" });
    return;
  }
  if (!params || typeof params !== "object") {
    json(res, 400, { error: "params (object) is required" });
    return;
  }
  try {
    const result = await connect.beginConnect(pluginName, params as BeginConnectParams);
    json(res, 200, {
      authorizationUrl: result.authorizationUrl,
      ...(result.oauthState ? { oauthState: result.oauthState } : {}),
    });
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

async function handleConnectComplete(
  req: IncomingMessage,
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
) {
  if (!connect) {
    json(res, 503, { error: "connectors are unavailable" });
    return;
  }
  const body = (await readJson(req).catch(() => null)) as Record<string, unknown> | null;
  if (!body || typeof body !== "object") {
    json(res, 400, { error: "request body must be JSON" });
    return;
  }
  const pluginName = body.pluginName;
  const params = body.params;
  if (typeof pluginName !== "string" || !pluginName) {
    json(res, 400, { error: "pluginName (string) is required" });
    return;
  }
  if (!params || typeof params !== "object") {
    json(res, 400, { error: "params (object) is required" });
    return;
  }
  try {
    const credential = await connect.completeConnect(
      pluginName,
      params as CompleteConnectParams,
    );
    json(res, 200, { credential });
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

async function handleConnectDisconnect(
  req: IncomingMessage,
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
) {
  if (!connect) {
    json(res, 503, { error: "connectors are unavailable" });
    return;
  }
  const body = (await readJson(req).catch(() => null)) as Record<string, unknown> | null;
  if (!body || typeof body !== "object") {
    json(res, 400, { error: "request body must be JSON" });
    return;
  }
  const pluginName = body.pluginName;
  const operatorId = body.operatorId;
  if (typeof pluginName !== "string" || !pluginName) {
    json(res, 400, { error: "pluginName (string) is required" });
    return;
  }
  if (typeof operatorId !== "string" || !operatorId) {
    json(res, 400, { error: "operatorId (string) is required" });
    return;
  }
  try {
    const removed = await connect.disconnect(pluginName, operatorId);
    json(res, 200, { removed });
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

function json(res: ServerResponse, status: number, body: unknown) {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": Buffer.byteLength(payload).toString(),
  });
  res.end(payload);
}

async function readJson(req: IncomingMessage): Promise<unknown> {
  const text = (await readBody(req, 64 * 1024)).toString("utf8");
  return text ? JSON.parse(text) : {};
}

async function readBody(req: IncomingMessage, limit: number): Promise<Buffer> {
  const chunks: Buffer[] = [];
  let size = 0;
  // Keep the response socket available for a useful 413, including chunked bodies.
  for await (const chunk of req.iterator({ destroyOnReturn: false })) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as string);
    size += bytes.length;
    if (size > limit) {
      req.resume();
      throw new Error("body too large");
    }
    chunks.push(bytes);
  }
  return Buffer.concat(chunks, size);
}
