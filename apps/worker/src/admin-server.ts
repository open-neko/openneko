/**
 * Admin HTTP handler for the worker process.
 *
 * Routes:
 *   POST /admin/action-requests/create → persist + run worker-owned preflight
 *                               before returning an approval-card-safe id.
 *
 * Extracted from index.ts so the handler can be unit-tested without
 * booting pg-boss / the agent stack.
 */

import type { IncomingMessage, ServerResponse } from "node:http";

export type AdminHandlerOptions = {
  /** Trusted web→worker action creation so worker-owned preflight hooks run. */
  actionRequests?: ActionRequestHandlerSurface | null;
};


export interface ActionRequestHandlerSurface {
  create(input: Record<string, unknown>): Promise<{ id: string; status: string }>;
}

export function createAdminHandler(opts: AdminHandlerOptions = {}) {
  const actionRequests = opts.actionRequests ?? null;

  return function handle(req: IncomingMessage, res: ServerResponse) {
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
