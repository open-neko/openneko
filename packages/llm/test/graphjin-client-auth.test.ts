import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { _resetSecretKeyCacheForTesting } from "@neko/secret-crypt";
import { provisionGraphjinClientAuth } from "../src/graphjin/client-auth";
import { verifyGraphjinToken } from "../src/graphjin/token";
import { ensureGraphjinGuard } from "../src/work/graphjin-guard";

let dir: string;
const prevXdg = process.env.XDG_CONFIG_HOME;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "gj-auth-"));
  process.env.XDG_CONFIG_HOME = join(dir, "deploy-xdg");
  _resetSecretKeyCacheForTesting();
});

afterAll(async () => {
  if (prevXdg === undefined) delete process.env.XDG_CONFIG_HOME;
  else process.env.XDG_CONFIG_HOME = prevXdg;
  _resetSecretKeyCacheForTesting();
  await rm(dir, { recursive: true, force: true });
});

describe("per-run GraphJin client auth (GJ4)", () => {
  it("writes a client.json the CLI reads, carrying the actor token", async () => {
    const runRoot = join(dir, "run-1");
    const auth = await provisionGraphjinClientAuth({
      runRoot,
      serverUrl: "http://localhost:8080/api/v1/mcp",
      orgId: "org-1",
      userId: "u-1",
      role: "member",
    });
    const cfg = JSON.parse(
      await readFile(join(auth.xdgConfigHome, "graphjin", "client.json"), "utf8"),
    );
    expect(cfg.server).toBe("http://localhost:8080/api/v1/mcp");
    expect(cfg.token).toBe(auth.token);
    const claims = verifyGraphjinToken(auth.token, "org-1");
    expect(claims).toMatchObject({ sub: "u-1", role: "member", org_id: "org-1" });
  });

  it("the guard pins the per-run XDG dir so the CLI sees the token", async () => {
    const runRoot = join(dir, "run-2");
    const binRoot = join(dir, "bin-2");
    await provisionGraphjinClientAuth({
      runRoot,
      serverUrl: "http://x",
      orgId: "org-1",
      userId: null,
      role: "service",
    });
    const { mkdir } = await import("node:fs/promises");
    await mkdir(binRoot, { recursive: true });
    const wrapper = await ensureGraphjinGuard(binRoot, "/usr/bin/true", {
      xdgConfigHome: join(runRoot, "gj-auth"),
    });
    const script = await readFile(wrapper, "utf8");
    expect(script).toContain(`export XDG_CONFIG_HOME='${join(runRoot, "gj-auth")}'`);
  });
});
