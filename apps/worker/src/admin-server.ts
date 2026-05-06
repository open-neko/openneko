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
 *
 * Extracted from index.ts so the handler can be unit-tested without
 * booting pg-boss / the agent stack.
 */

import type { IncomingMessage, ServerResponse } from "node:http";

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
};

export function createAdminHandler(opts: AdminHandlerOptions = {}) {
  const exit = opts.exit ?? ((code = 0) => process.exit(code));
  const exitDelayMs = opts.exitDelayMs ?? 100;

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
    res.writeHead(404).end();
  };
}
