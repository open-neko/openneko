/**
 * Admin HTTP handler for the worker process.
 *
 * Routes:
 *   GET  /health              → 200 ok (liveness)
 *   POST /admin/reconnect     → 202 + clean exit so the supervisor restarts
 *                               us with fresh DB credentials. Used by the
 *                               web app's /api/admin/change-password handler
 *                               after rotating the Postgres password — the
 *                               pg-boss singleton holds the old creds and
 *                               there's no clean way to re-register handlers
 *                               in-place against a fresh pool.
 *   GET  /admin/auth/status   → 200 + { provider: null | {pluginName, providerLabel} }
 *                               Tells the web app whether an SSO plugin is
 *                               installed so the sign-in page can render
 *                               the appropriate button.
 *   POST /admin/auth/begin    → 200 + { authorizationUrl }
 *                               Body: { redirectUri, state, loginHint? }
 *                               Proxies to the installed auth plugin's
 *                               begin_auth RPC.
 *   POST /admin/auth/complete → 200 + { identity }
 *                               Body: { code, redirectUri, state }
 *                               Proxies to the installed auth plugin's
 *                               complete_auth RPC.
 *
 * Extracted from index.ts so the handler can be unit-tested without
 * booting pg-boss / the agent stack.
 */

import type { IncomingMessage, ServerResponse } from "node:http";
import type {
  AuthIdentity,
  BeginConnectParams,
  CompleteConnectParams,
  ConnectorCredential,
} from "@open-neko/plugin-types";

export interface AuthHandlerSurface {
  getAuthProvider(): {
    pluginName: string;
    providerLabel: string;
  } | null;
  beginAuth(params: {
    redirectUri: string;
    state: string;
    loginHint?: string | null;
  }): Promise<{ authorizationUrl: string }>;
  completeAuth(params: {
    code: string;
    redirectUri: string;
    state: string;
  }): Promise<AuthIdentity>;
}

/**
 * Connect (per-operator OAuth) surface exposed to the web app. The
 * worker delegates each call to the matching plugin registry method;
 * credentials are persisted in the per-operator section of the secrets
 * file by the registry, not by the web.
 */
export interface ConnectHandlerSurface {
  getConnectProviders(): Array<{
    pluginId: string;
    pluginName: string;
    providerLabel: string;
    scopes: string[];
  }>;
  getOperatorConnectStatus(
    operatorId: string,
  ): Array<{ pluginName: string; connectedAt: string; scopes?: string[] }>;
  beginConnect(
    pluginName: string,
    params: BeginConnectParams,
  ): Promise<{ authorizationUrl: string }>;
  completeConnect(
    pluginName: string,
    params: CompleteConnectParams,
  ): Promise<ConnectorCredential>;
  refreshConnect(
    pluginName: string,
    operatorId: string,
  ): Promise<ConnectorCredential>;
  disconnect(pluginName: string, operatorId: string): Promise<boolean>;
}

/**
 * Channel (frontend) surface. The worker delegates `deliver` to the
 * PluginRegistry's deliver RPC and `ingestInbound` to the channel delivery
 * module (verify → parse in-VM → dispatch to the existing agent entry points).
 */
export interface ChannelHandlerSurface {
  getChannelProviders(): Array<{
    pluginId: string;
    pluginName: string;
    providerLabel: string;
    directions: string[];
    ingress: string;
  }>;
  deliver(
    pluginName: string,
    recipient: Record<string, unknown>,
    events: unknown[],
  ): Promise<{ delivered: boolean; ref?: string }>;
  ingestInbound(
    pluginName: string,
    headers: Record<string, string>,
    body: string,
  ): Promise<{ ok: boolean; dispatched: number }>;
}

/**
 * Install-policy surface exposed to the CLI (via the worker admin
 * port) for the install-time enforcement check. The CLI calls
 * `GET /admin/install-policy` before running install and refuses to
 * proceed with --unverified when the policy disallows it.
 */
export interface InstallPolicyHandlerSurface {
  getInstallPolicy(): Promise<{
    allowUnverified: boolean;
    allowGitUrlInstalls: boolean;
    allowedMarketplaces: string[];
    allowSandboxedSkillEscape: boolean;
  }>;
}

export interface PluginsHandlerSurface {
  /**
   * Flat list of every plugin's declared action kinds + seeded
   * default approval mode. Consumed by the web process's /work
   * route so the in-process runChatTurn can build the agent's MCP
   * tool surface — the web doesn't have the plugin registry
   * locally (registry + adapters live in the worker).
   */
  getRegisteredActionDescriptors(): Array<{
    kind: string;
    description: string;
    default_mode?:
      | "auto"
      | "ask"
      | "deny"
      | {
          external?: "auto" | "ask" | "deny";
          internal?: "auto" | "ask" | "deny";
        };
    example?: Record<string, unknown>;
  }>;
}

export type AdminHandlerOptions = {
  /**
   * Called from POST /admin/reconnect after responding 202. Defaults to
   * `process.exit(0)`; tests pass a spy.
   */
  exit?: (code?: number) => void;
  /**
   * Delay (ms) between sending the 202 and calling exit. Default 100ms —
   * enough for the response to flush to the caller. Set to 0 in tests.
   */
  exitDelayMs?: number;
  /**
   * Auth surface — typically wired to the PluginRegistry. Absent when
   * the plugin subsystem is disabled, in which case /admin/auth/*
   * routes return 503 with a clear message.
   */
  auth?: AuthHandlerSurface | null;
  /**
   * Plugins surface — typically wired to the PluginRegistry. Absent
   * when the plugin subsystem is disabled, in which case
   * /admin/plugins/action-descriptors returns an empty array.
   */
  plugins?: PluginsHandlerSurface | null;
  /**
   * Connect surface — typically wired to the PluginRegistry. Absent
   * when the plugin subsystem is disabled, in which case
   * /admin/connect/* routes return 503.
   */
  connect?: ConnectHandlerSurface | null;
  /**
   * Channel surface — typically wired to the PluginRegistry + channel
   * delivery module. Absent when the plugin subsystem is disabled, in which
   * case channel routes return 503 / empty.
   */
  channels?: ChannelHandlerSurface | null;
  /**
   * Install-policy reader. Absent when the plugin subsystem is
   * disabled — in that case /admin/install-policy returns a default
   * (most-restrictive) policy so the CLI errs on the side of
   * refusing privileged install paths.
   */
  installPolicy?: InstallPolicyHandlerSurface | null;
};

export function createAdminHandler(opts: AdminHandlerOptions = {}) {
  const exit = opts.exit ?? ((code = 0) => process.exit(code));
  const exitDelayMs = opts.exitDelayMs ?? 100;
  const auth = opts.auth ?? null;
  const plugins = opts.plugins ?? null;
  const connect = opts.connect ?? null;
  const channels = opts.channels ?? null;
  const installPolicy = opts.installPolicy ?? null;

  return function handle(req: IncomingMessage, res: ServerResponse) {
    if (req.method === "GET" && req.url === "/health") {
      res.writeHead(200).end("ok");
      return;
    }
    if (req.method === "POST" && req.url === "/admin/reconnect") {
      res.writeHead(202).end("restarting");
      console.log(
        "[worker] /admin/reconnect received — exiting for clean restart",
      );
      setTimeout(() => exit(0), exitDelayMs);
      return;
    }
    if (req.method === "GET" && req.url === "/admin/auth/status") {
      handleAuthStatus(res, auth);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/auth/begin") {
      void handleAuthBegin(req, res, auth);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/auth/complete") {
      void handleAuthComplete(req, res, auth);
      return;
    }
    if (
      req.method === "GET" &&
      req.url === "/admin/plugins/action-descriptors"
    ) {
      handlePluginActionDescriptors(res, plugins);
      return;
    }
    if (req.method === "GET" && req.url === "/admin/connect/providers") {
      handleConnectProviders(res, connect);
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
    if (req.method === "POST" && req.url === "/admin/connect/refresh") {
      void handleConnectRefresh(req, res, connect);
      return;
    }
    if (req.method === "POST" && req.url === "/admin/connect/disconnect") {
      void handleConnectDisconnect(req, res, connect);
      return;
    }
    if (req.method === "GET" && req.url === "/admin/install-policy") {
      void handleInstallPolicy(res, installPolicy);
      return;
    }
    if (req.method === "GET" && req.url === "/admin/channels/providers") {
      handleChannelProviders(res, channels);
      return;
    }
    if (req.method === "POST" && req.url?.startsWith("/admin/channels/")) {
      const m = /^\/admin\/channels\/([^/]+)\/deliver(?:[?#]|$)/.exec(req.url);
      if (m) {
        void handleChannelDeliver(req, res, decodeURIComponent(m[1]!), channels);
        return;
      }
    }
    if (req.method === "POST" && req.url?.startsWith("/channels/")) {
      const m = /^\/channels\/([^/]+)\/inbound(?:[?#]|$)/.exec(req.url);
      if (m) {
        void handleChannelInbound(req, res, decodeURIComponent(m[1]!), channels);
        return;
      }
    }
    res.writeHead(404).end();
  };
}

async function handleInstallPolicy(
  res: ServerResponse,
  installPolicy: InstallPolicyHandlerSurface | null,
) {
  if (!installPolicy) {
    // No reader wired → return defaults (most-restrictive). The CLI
    // will treat this as "no privileged install paths allowed".
    json(res, 200, {
      policy: {
        allowUnverified: false,
        allowGitUrlInstalls: false,
        allowedMarketplaces: [
          "https://open-neko.github.io/plugins/marketplace.json",
        ],
        allowSandboxedSkillEscape: false,
      },
      source: "default",
    });
    return;
  }
  try {
    const policy = await installPolicy.getInstallPolicy();
    json(res, 200, { policy, source: "org" });
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

function handlePluginActionDescriptors(
  res: ServerResponse,
  plugins: PluginsHandlerSurface | null,
) {
  const descriptors = plugins?.getRegisteredActionDescriptors() ?? [];
  json(res, 200, { descriptors });
}

function handleAuthStatus(res: ServerResponse, auth: AuthHandlerSurface | null) {
  if (!auth) {
    json(res, 200, { provider: null });
    return;
  }
  const provider = auth.getAuthProvider();
  json(res, 200, {
    provider: provider
      ? {
          pluginName: provider.pluginName,
          providerLabel: provider.providerLabel,
        }
      : null,
  });
}

async function handleAuthBegin(
  req: IncomingMessage,
  res: ServerResponse,
  auth: AuthHandlerSurface | null,
) {
  if (!auth) {
    json(res, 503, { error: "plugin subsystem disabled" });
    return;
  }
  const body = await readJson(req).catch(() => null);
  if (!body || typeof body !== "object") {
    json(res, 400, { error: "request body must be JSON" });
    return;
  }
  const { redirectUri, state, loginHint } = body as Record<string, unknown>;
  if (typeof redirectUri !== "string" || !redirectUri) {
    json(res, 400, { error: "redirectUri (string) is required" });
    return;
  }
  if (typeof state !== "string" || !state) {
    json(res, 400, { error: "state (string) is required" });
    return;
  }
  try {
    const result = await auth.beginAuth({
      redirectUri,
      state,
      loginHint:
        typeof loginHint === "string" && loginHint.length > 0
          ? loginHint
          : null,
    });
    json(res, 200, { authorizationUrl: result.authorizationUrl });
  } catch (err) {
    json(res, 500, {
      error: err instanceof Error ? err.message : String(err),
    });
  }
}

async function handleAuthComplete(
  req: IncomingMessage,
  res: ServerResponse,
  auth: AuthHandlerSurface | null,
) {
  if (!auth) {
    json(res, 503, { error: "plugin subsystem disabled" });
    return;
  }
  const body = await readJson(req).catch(() => null);
  if (!body || typeof body !== "object") {
    json(res, 400, { error: "request body must be JSON" });
    return;
  }
  const { code, redirectUri, state } = body as Record<string, unknown>;
  if (typeof code !== "string" || !code) {
    json(res, 400, { error: "code (string) is required" });
    return;
  }
  if (typeof redirectUri !== "string" || !redirectUri) {
    json(res, 400, { error: "redirectUri (string) is required" });
    return;
  }
  if (typeof state !== "string" || !state) {
    json(res, 400, { error: "state (string) is required" });
    return;
  }
  try {
    const identity = await auth.completeAuth({ code, redirectUri, state });
    json(res, 200, { identity });
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

async function handleConnectBegin(
  req: IncomingMessage,
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
) {
  if (!connect) {
    json(res, 503, { error: "plugin subsystem disabled" });
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
    json(res, 200, { authorizationUrl: result.authorizationUrl });
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
    json(res, 503, { error: "plugin subsystem disabled" });
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

async function handleConnectRefresh(
  req: IncomingMessage,
  res: ServerResponse,
  connect: ConnectHandlerSurface | null,
) {
  if (!connect) {
    json(res, 503, { error: "plugin subsystem disabled" });
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
    const credential = await connect.refreshConnect(pluginName, operatorId);
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
    json(res, 503, { error: "plugin subsystem disabled" });
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

// ─── Channel (frontend) ────────────────────────────────────────────────

function handleChannelProviders(
  res: ServerResponse,
  channels: ChannelHandlerSurface | null,
) {
  json(res, 200, { providers: channels?.getChannelProviders() ?? [] });
}

async function handleChannelDeliver(
  req: IncomingMessage,
  res: ServerResponse,
  pluginName: string,
  channels: ChannelHandlerSurface | null,
) {
  if (!channels) {
    json(res, 503, { error: "channel subsystem disabled" });
    return;
  }
  const body = (await readJson(req).catch(() => null)) as Record<string, unknown> | null;
  if (!body || typeof body !== "object") {
    json(res, 400, { error: "request body must be JSON" });
    return;
  }
  if (!body.recipient || typeof body.recipient !== "object") {
    json(res, 400, { error: "recipient (object) is required" });
    return;
  }
  if (!Array.isArray(body.events)) {
    json(res, 400, { error: "events (array) is required" });
    return;
  }
  try {
    const result = await channels.deliver(
      pluginName,
      body.recipient as Record<string, unknown>,
      body.events,
    );
    json(res, 200, result);
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

async function handleChannelInbound(
  req: IncomingMessage,
  res: ServerResponse,
  pluginName: string,
  channels: ChannelHandlerSurface | null,
) {
  if (!channels) {
    json(res, 503, { error: "channel subsystem disabled" });
    return;
  }
  const rawBody = await readText(req).catch(() => null);
  if (rawBody == null) {
    json(res, 400, { error: "could not read request body" });
    return;
  }
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(req.headers)) {
    if (typeof v === "string") headers[k] = v;
    else if (Array.isArray(v) && v.length > 0) headers[k] = v[0]!;
  }
  try {
    const result = await channels.ingestInbound(pluginName, headers, rawBody);
    json(res, 200, result);
  } catch (err) {
    json(res, 500, { error: err instanceof Error ? err.message : String(err) });
  }
}

async function readText(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as string));
    if (Buffer.concat(chunks).length > 256 * 1024) {
      throw new Error("body too large");
    }
  }
  return Buffer.concat(chunks).toString("utf8");
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
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as string));
    // Cap body size to defend against runaway uploads against the
    // worker admin port (loopback, but still).
    if (Buffer.concat(chunks).length > 64 * 1024) {
      throw new Error("body too large");
    }
  }
  const text = Buffer.concat(chunks).toString("utf8");
  if (!text) return {};
  return JSON.parse(text);
}
