/**
 * Admin HTTP handler for the worker process.
 *
 * Routes:
 *   GET  /admin/directory/status → directory provider and last sync state.
 *   POST /admin/directory/sync  → run a full directory sync now.
 *   POST /admin/directory/users → create a user in the identity provider ({ email, name? }).
 *   POST /admin/graphjin/group-grants → apply group grants now ({ immediate: true }) or in a batch.
 *   POST /admin/action-requests/create → persist + run worker-owned preflight
 *                               before returning an approval-card-safe id.
 *   POST /admin/actions/execute → run an approved action ({ orgId, actionRequestId });
 *                               the action_execute job on River calls it.
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
import { packFailurePayload, type PackErrorPhase } from "./packs/errors.js";

const PACK_FAILURE_NEXT_STEP: Record<PackErrorPhase, string> = {
  upload: "Correct the archive layout or manifest, then upload the pack again.",
  inspection: "Correct the reported manifest or artifact, then inspect the pack again.",
  review: "Correct the reported configuration or artifact, then review the pack again.",
  install: "Correct the reported configuration or artifact, then review and install the pack again.",
  configure: "Correct the reported configuration or artifact, then review and apply the change again.",
  upgrade: "Correct the reported configuration or artifact, then review and apply the upgrade again.",
  uninstall: "Check the reported pack state, then try removing the pack again.",
  oauth: "Check the OAuth client, redirect URI, and requested scopes, then connect the account again.",
  query_preflight: "Correct the reported query or source configuration, then review the pack again.",
};

function packFailure(error: unknown, phase: PackErrorPhase) {
  return packFailurePayload(error, phase, PACK_FAILURE_NEXT_STEP[phase]);
}

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

export interface PacksHandlerSurface {
  upload(bytes: Buffer, input: { actorUserId?: string | null; signal?: AbortSignal }): Promise<unknown>;
  review(packId: string, input: Record<string, unknown>, operation: "install" | "configure" | "upgrade"): Promise<unknown>;
  list(): Promise<unknown>;
  inspect(packId: string, version?: string): Promise<unknown>;
  plan(packId: string, version?: string): Promise<unknown>;
  status(packId: string): Promise<unknown>;
  doctor(packId: string): Promise<unknown>;
  install(packId: string, input: Record<string, unknown>): Promise<unknown>;
  configure(packId: string, input: Record<string, unknown>): Promise<unknown>;
  upgrade(packId: string, input: Record<string, unknown>): Promise<unknown>;
  uninstall(packId: string, input: Record<string, unknown>): Promise<unknown>;
  magentoStoreManagement(): Promise<unknown>;
  updateMagentoStoreManagement(input: Record<string, unknown>): Promise<unknown>;
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
  actions?: ActionExecuteHandlerSurface | null;
  /** Shared first-party and uploaded solution-pack lifecycle. */
  packs?: PacksHandlerSurface | null;
  /** Directory provider status and on-demand sync. */
  directory?: DirectoryHandlerSurface | null;
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

export interface DirectoryHandlerSurface {
  status(): Promise<unknown>;
  sync(): Promise<unknown>;
  createUser(input: { email: string; name: string | null }): Promise<unknown>;
}

export interface ActionRequestHandlerSurface {
  create(input: Record<string, unknown>): Promise<{ id: string; status: string }>;
}

export interface ActionExecuteHandlerSurface {
  ready(): boolean;
  execute(input: { orgId: string; actionRequestId: string }): Promise<void>;
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
  const actions = opts.actions ?? null;
  const packs = opts.packs ?? null;
  const directory = opts.directory ?? null;
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
    if (req.url === "/admin/directory/status" || req.url === "/admin/directory/sync" || req.url === "/admin/directory/users") {
      void handleDirectory(req, res, directory);
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
    if (req.method === "POST" && req.url === "/admin/actions/execute") {
      void handleActionExecute(req, res, actions);
      return;
    }
    if (req.method === "GET" && req.url === "/admin/packs") {
      void handlePacksList(res, packs);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/packs/upload") {
      void handlePackUpload(req, res, packs);
      return;
    }
    if (req.method === "GET" && req.url === "/admin/packs/magento/store-management") {
      void handleMagentoStoreManagementRead(res, packs);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/packs/magento/store-management") {
      void handleMagentoStoreManagementWrite(req, res, packs);
      return;
    }
    const packPath = (req.url ?? "").split(/[?#]/, 1)[0] ?? "";
    const packRoute = /^\/admin\/packs\/([^/]+)(?:\/(inspect|plan|status|doctor|review|install|configure|upgrade|uninstall))?$/.exec(
      packPath,
    );
    if (packRoute) {
      let packId: string;
      try {
        packId = decodeURIComponent(packRoute[1]!);
      } catch {
        json(res, 400, { error: "pack id contains invalid URL encoding" });
        return;
      }
      const action = packRoute[2] ?? "inspect";
      if (req.method === "GET" && !["review", "install", "configure", "upgrade", "uninstall"].includes(action)) {
        void handlePackRead(res, packs, packId, action as "inspect" | "plan" | "status" | "doctor", new URL(req.url!, "http://localhost").searchParams.get("version") ?? undefined);
        return;
      }
      if (req.method === "POST" && ["review", "install", "configure", "upgrade", "uninstall"].includes(action)) {
        void handlePackApply(
          req,
          res,
          packs,
          packId,
          action as "review" | "install" | "configure" | "upgrade" | "uninstall",
        );
        return;
      }
    }
    res.writeHead(404).end();
  };
}

async function handlePackUpload(req: IncomingMessage, res: ServerResponse, packs: PacksHandlerSurface | null): Promise<void> {
  if (!packs) { json(res, 503, { error: "solution-pack service unavailable" }); return; }
  if (req.headers["content-type"]?.split(";", 1)[0]?.trim() !== "application/zip") {
    json(res, 415, { error: "upload a ZIP with Content-Type application/zip" }); return;
  }
  const controller = new AbortController();
  const abort = () => { if (!res.writableEnded) controller.abort(); };
  res.once("close", abort);
  try {
    const bytes = await readBody(req, 16 * 1024 * 1024);
    const actor = req.headers["x-openneko-actor"];
    if (Array.isArray(actor)) throw new Error("invalid upload actor");
    json(res, 200, await packs.upload(bytes, { actorUserId: actor ? decodeURIComponent(actor) : null, signal: controller.signal }));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    json(res, message === "body too large" ? 413 : 400, packFailure(error, "upload"));
  } finally { res.off("close", abort); }
}

async function handlePacksList(
  res: ServerResponse,
  packs: PacksHandlerSurface | null,
): Promise<void> {
  if (!packs) {
    json(res, 503, { error: "solution-pack service unavailable" });
    return;
  }
  try {
    json(res, 200, { packs: await packs.list() });
  } catch (error) {
    json(res, 500, { error: error instanceof Error ? error.message : String(error) });
  }
}

async function handleMagentoStoreManagementRead(
  res: ServerResponse,
  packs: PacksHandlerSurface | null,
): Promise<void> {
  if (!packs) {
    json(res, 503, { error: "solution-pack service unavailable" });
    return;
  }
  try {
    json(res, 200, await packs.magentoStoreManagement());
  } catch (error) {
    json(res, 400, packFailure(error, "inspection"));
  }
}

async function handleMagentoStoreManagementWrite(
  req: IncomingMessage,
  res: ServerResponse,
  packs: PacksHandlerSurface | null,
): Promise<void> {
  if (!packs) {
    json(res, 503, { error: "solution-pack service unavailable" });
    return;
  }
  const body = await readJson(req).catch(() => null);
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    json(res, 400, { error: "request body must be a JSON object" });
    return;
  }
  try {
    json(res, 200, await packs.updateMagentoStoreManagement(body as Record<string, unknown>));
  } catch (error) {
    json(res, 400, packFailure(error, "inspection"));
  }
}

async function handlePackRead(
  res: ServerResponse,
  packs: PacksHandlerSurface | null,
  packId: string,
  action: "inspect" | "plan" | "status" | "doctor",
  version?: string,
): Promise<void> {
  if (!packs) {
    json(res, 503, { error: "solution-pack service unavailable" });
    return;
  }
  try {
    const result = version && (action === "inspect" || action === "plan") ? await packs[action](packId, version) : await packs[action](packId);
    if (action === "status" && result === null) {
      json(res, 404, { error: `pack ${packId} is not installed` });
      return;
    }
    json(res, 200, result);
  } catch (error) {
    json(res, 400, packFailure(error, "inspection"));
  }
}

async function handlePackApply(
  req: IncomingMessage,
  res: ServerResponse,
  packs: PacksHandlerSurface | null,
  packId: string,
  action: "review" | "install" | "configure" | "upgrade" | "uninstall",
): Promise<void> {
  if (!packs) {
    json(res, 503, { error: "solution-pack service unavailable" });
    return;
  }
  const body = await readJson(req).catch(() => null);
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    json(res, 400, { error: "request body must be a JSON object" });
    return;
  }
  try {
    const input = body as Record<string, unknown>;
    if (action === "review") {
      const operation = input.operation ?? "install";
      if (operation !== "install" && operation !== "configure" && operation !== "upgrade") throw new Error("invalid pack review operation");
      json(res, 200, await packs.review(packId, input, operation));
    } else json(res, 200, await packs[action](packId, input));
  } catch (error) {
    json(res, 400, packFailure(error, action === "review" ? "review" : action));
  }
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

async function handleActionExecute(
  req: IncomingMessage,
  res: ServerResponse,
  actions: ActionExecuteHandlerSurface | null,
): Promise<void> {
  if (!actions?.ready()) {
    json(res, 503, { error: "action adapters are not ready" });
    return;
  }
  try {
    const body = (await readJson(req).catch(() => null)) as { orgId?: unknown; actionRequestId?: unknown } | null;
    const orgId = typeof body?.orgId === "string" ? body.orgId : "";
    const actionRequestId = typeof body?.actionRequestId === "string" ? body.actionRequestId : "";
    if (!orgId || !actionRequestId) {
      json(res, 400, { error: "orgId and actionRequestId are required" });
      return;
    }
    await actions.execute({ orgId, actionRequestId });
    json(res, 200, { ok: true });
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

async function handleDirectory(
  req: IncomingMessage,
  res: ServerResponse,
  directory: DirectoryHandlerSurface | null,
) {
  if (!directory) {
    json(res, 503, { error: "directory is not configured" });
    return;
  }
  const sync = req.url === "/admin/directory/sync";
  const users = req.url === "/admin/directory/users";
  if (req.method !== (sync || users ? "POST" : "GET")) {
    json(res, 405, { error: "method not allowed" });
    return;
  }
  try {
    if (users) {
      const body = (await readJson(req).catch(() => null)) as { email?: unknown; name?: unknown } | null;
      const email = typeof body?.email === "string" ? body.email.trim().toLowerCase() : "";
      if (!email.includes("@")) {
        json(res, 400, { error: "a valid email address is required" });
        return;
      }
      const name = typeof body?.name === "string" && body.name.trim() ? body.name.trim() : null;
      json(res, 200, await directory.createUser({ email, name }));
      return;
    }
    json(res, 200, sync ? { stats: await directory.sync() } : await directory.status());
  } catch (err) {
    const code = (err as { code?: string }).code;
    const status = code === "no_provider" ? 404 : code === "running" || code === "lockout" ? 409 : code === "unsupported" ? 400 : 500;
    json(res, status, { error: err instanceof Error ? err.message : String(err), code: code ?? null });
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
