/**
 * Admin HTTP handler for the worker process. Every admin route moved to
 * openneko web; the handler answers 404.
 */

import type { IncomingMessage, ServerResponse } from "node:http";

export function createAdminHandler() {
  return function handle(_req: IncomingMessage, res: ServerResponse) {
    res.writeHead(404).end();
  };
}
