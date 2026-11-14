import { createServer, type Server } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { workerAdminFetch, workerAdminTarget } from "../src/worker-admin";

describe("worker admin address", () => {
  let server: Server | undefined;
  let dir: string | undefined;

  afterEach(async () => {
    if (server) await new Promise((resolve) => server!.close(resolve));
    if (dir) rmSync(dir, { recursive: true, force: true });
    server = undefined;
    dir = undefined;
  });

  it("keeps an http address and defaults to the loopback port", () => {
    expect(workerAdminTarget("http://worker:4100/")).toEqual({ origin: "http://worker:4100" });
    expect(workerAdminTarget(undefined)).toEqual({ origin: "http://127.0.0.1:4100" });
    expect(workerAdminTarget("unix:///run/openneko/worker.sock")).toEqual({
      origin: "http://worker",
      socketPath: "/run/openneko/worker.sock",
    });
    expect(() => workerAdminTarget("unix:run/worker.sock")).toThrow(/absolute/);
  });

  it("sends a request over a unix socket", async () => {
    dir = mkdtempSync(join(tmpdir(), "wa-"));
    const socket = join(dir, "worker.sock");
    server = createServer((req, res) => {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ path: req.url, method: req.method }));
    });
    await new Promise<void>((resolve) => server!.listen(socket, resolve));
    const res = await workerAdminFetch("/admin/auth/status", { method: "POST", cache: "no-store" }, `unix://${socket}`);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ path: "/admin/auth/status", method: "POST" });
  });
});
