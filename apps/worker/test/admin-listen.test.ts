import { createServer, request, type Server } from "node:http";
import { mkdtempSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer as createNetServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { adminSocketPath, listenAdmin } from "../src/admin-listen.js";

function getHealth(socketPath: string): Promise<string> {
  return new Promise((resolve, reject) => {
    request({ socketPath, path: "/health" }, (res) => {
      let body = "";
      res.on("data", (chunk) => (body += chunk));
      res.on("end", () => resolve(body));
    }).on("error", reject).end();
  });
}

describe("worker admin listener", () => {
  let server: Server | undefined;
  let dir: string | undefined;

  afterEach(async () => {
    if (server?.listening) await new Promise((resolve) => server!.close(resolve));
    if (dir) rmSync(dir, { recursive: true, force: true });
    server = undefined;
    dir = undefined;
  });

  it("reads the socket path from OPENNEKO_WORKER_SOCKET", () => {
    expect(adminSocketPath({ OPENNEKO_WORKER_SOCKET: " /run/openneko/worker.sock " })).toBe("/run/openneko/worker.sock");
    expect(adminSocketPath({ OPENNEKO_WORKER_SOCKET: "" })).toBeUndefined();
    expect(adminSocketPath({})).toBeUndefined();
  });

  it("serves on a socket for owner and group only and replaces a stale socket", async () => {
    dir = mkdtempSync(join(tmpdir(), "wl-"));
    const socket = join(dir, "worker.sock");
    const stale = createNetServer();
    await new Promise<void>((resolve) => stale.listen(socket, resolve));
    await new Promise<void>((resolve) => {
      (stale as unknown as { _handle: { close(): void } })._handle.close();
      resolve();
    });

    server = createServer((_req, res) => res.end("ok"));
    const where = await new Promise<string>((resolve) => listenAdmin(server!, socket, 0, resolve));
    expect(where).toBe(`unix://${socket}`);
    expect(statSync(socket).mode & 0o777).toBe(0o660);
    expect(await getHealth(socket)).toBe("ok");
  });

  it("leaves a regular file at the socket path alone", async () => {
    dir = mkdtempSync(join(tmpdir(), "wl-"));
    const path = join(dir, "worker.sock");
    writeFileSync(path, "not a socket");
    server = createServer();
    const failed = new Promise<NodeJS.ErrnoException>((resolve) => server!.once("error", resolve));
    listenAdmin(server, path, 0, () => {});
    expect((await failed).code).toBe("EADDRINUSE");
  });
});
