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
import type { AuthIdentity } from "@open-neko/plugin-types";

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
};

export function createAdminHandler(opts: AdminHandlerOptions = {}) {
  const exit = opts.exit ?? ((code = 0) => process.exit(code));
  const exitDelayMs = opts.exitDelayMs ?? 100;
  const auth = opts.auth ?? null;

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
    res.writeHead(404).end();
  };
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
