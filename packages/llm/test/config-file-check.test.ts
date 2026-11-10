import { createServer, type Server } from "node:http";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  checkGraphjinConfigCandidate,
  GRAPHJIN_CHECK_DIR,
} from "../src/graphjin/config-file-check";

const dirs: string[] = [];
const servers: Server[] = [];

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => new Promise((resolve) => server.close(resolve))));
  await Promise.all(dirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

async function fixture(reply: (candidate: string, content: string) => { status: number; body: unknown }) {
  const dir = await mkdtemp(join(tmpdir(), "gj-check-"));
  dirs.push(dir);
  const configFile = join(dir, "agentic.yml");
  await writeFile(configFile, "app_name: test\nsources: []\nroles:\n  - name: admin\n");
  const socketPath = join(dir, "check.sock");
  const seen: Array<{ candidate: string; content: string; body: string }> = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", async () => {
      const { candidate } = JSON.parse(body) as { candidate: string };
      const content = await readFile(join(dir, GRAPHJIN_CHECK_DIR, candidate, "agentic.yml"), "utf8");
      seen.push({ candidate, content, body });
      const out = reply(candidate, content);
      res.writeHead(out.status, { "content-type": "application/json" });
      res.end(JSON.stringify(out.body));
    });
  });
  servers.push(server);
  await new Promise<void>((resolve) => server.listen(socketPath, resolve));
  return { dir, configFile, socketPath, seen };
}

const update = {
  update_sources: [{ name: "shop", kind: "database", type: "mysql", password: "hunter2-secret", read_only: true }],
  roles: [{ name: "support", match: "role = 'support'" }],
};

describe("checkGraphjinConfigCandidate", () => {
  it("sends serve the id of the merged file, never its text, and deletes the file", async () => {
    const f = await fixture(() => ({ status: 200, body: { valid: true, errors: [] } }));
    const result = await checkGraphjinConfigCandidate({ configFile: f.configFile, socketPath: f.socketPath, update });
    expect(result).toEqual({ checked: true, valid: true, errors: [] });
    expect(f.seen).toHaveLength(1);
    expect(f.seen[0]!.candidate).toMatch(/^[0-9a-f-]{36}$/);
    expect(f.seen[0]!.body).not.toContain("hunter2");
    expect(f.seen[0]!.content).toContain("name: shop");
    expect(f.seen[0]!.content).not.toContain("hunter2");
    expect(f.seen[0]!.content).toContain("password: gjsecret://sources/shop/password");
    expect(f.seen[0]!.content).toContain("name: support");
    expect(await readdir(join(f.dir, GRAPHJIN_CHECK_DIR))).toEqual([]);
    expect(await readFile(f.configFile, "utf8")).toBe("app_name: test\nsources: []\nroles:\n  - name: admin\n");
  });

  it("returns the loader's errors", async () => {
    const f = await fixture(() => ({ status: 200, body: { valid: false, errors: ["sources[0]: name is required"] } }));
    expect(await checkGraphjinConfigCandidate({ configFile: f.configFile, socketPath: f.socketPath, update })).toEqual({
      checked: true,
      valid: false,
      errors: ["sources[0]: name is required"],
    });
  });

  it("fails when serve refuses, and still deletes the file", async () => {
    const f = await fixture(() => ({ status: 404, body: "no candidate" }));
    await expect(
      checkGraphjinConfigCandidate({ configFile: f.configFile, socketPath: f.socketPath, update }),
    ).rejects.toThrow(/returned 404/);
    expect(await readdir(join(f.dir, GRAPHJIN_CHECK_DIR))).toEqual([]);
  });

  it("says why it did not check", async () => {
    expect(await checkGraphjinConfigCandidate({ configFile: "", socketPath: "/x", update })).toEqual({
      checked: false,
      reason: "OPENNEKO_GRAPHJIN_CONFIG is not set",
    });
    expect(await checkGraphjinConfigCandidate({ configFile: "/x/agentic.yml", socketPath: undefined, update })).toEqual({
      checked: false,
      reason: "OPENNEKO_CONFIG_CHECK_SOCKET is not set",
    });
  });
});
