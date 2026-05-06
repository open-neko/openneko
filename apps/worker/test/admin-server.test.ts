/**
 * Tests for the worker's /health + /admin/reconnect HTTP handler. The
 * /admin/reconnect signal is fired by the web app's change-password
 * handler after rotating the Postgres password; the worker must respond
 * 202 and exit cleanly so the supervisor (`tsx watch` / Cloud Run)
 * restarts it with fresh credentials.
 */

import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAdminHandler } from "../src/admin-server";

async function startServer(handler: ReturnType<typeof createAdminHandler>) {
  const server = createServer(handler);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const port = (server.address() as AddressInfo).port;
  return {
    port,
    close: () => new Promise<void>((resolve, reject) =>
      server.close((err) => (err ? reject(err) : resolve())),
    ),
  };
}

describe("worker admin HTTP handler", () => {
  let exit: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    exit = vi.fn();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("GET /health returns 200 ok", async () => {
    const srv = await startServer(createAdminHandler({ exit }));
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/health`);
      expect(res.status).toBe(200);
      expect(await res.text()).toBe("ok");
      expect(exit).not.toHaveBeenCalled();
    } finally {
      await srv.close();
    }
  });

  it("POST /admin/reconnect returns 202 and triggers process.exit(0)", async () => {
    const srv = await startServer(
      createAdminHandler({ exit, exitDelayMs: 0 }),
    );
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/reconnect`, {
        method: "POST",
      });
      expect(res.status).toBe(202);
      expect(await res.text()).toBe("restarting");
      // setTimeout(0) microtask — flush.
      await new Promise((r) => setTimeout(r, 5));
      expect(exit).toHaveBeenCalledTimes(1);
      expect(exit).toHaveBeenCalledWith(0);
    } finally {
      await srv.close();
    }
  });

  it("GET /admin/reconnect (wrong method) returns 404 and does not exit", async () => {
    const srv = await startServer(createAdminHandler({ exit }));
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/admin/reconnect`);
      expect(res.status).toBe(404);
      expect(exit).not.toHaveBeenCalled();
    } finally {
      await srv.close();
    }
  });

  it("unknown path returns 404", async () => {
    const srv = await startServer(createAdminHandler({ exit }));
    try {
      const res = await fetch(`http://127.0.0.1:${srv.port}/whatever`);
      expect(res.status).toBe(404);
      expect(exit).not.toHaveBeenCalled();
    } finally {
      await srv.close();
    }
  });
});
