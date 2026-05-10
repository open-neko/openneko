import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { ensureGraphjinGuard, isGraphjinCommandSafe } from "../src/work/graphjin-guard";

describe("isGraphjinCommandSafe", () => {
  it("allows read-style graphjin queries", () => {
    expect(
      isGraphjinCommandSafe([
        "cli",
        "execute_graphql",
        "--args",
        '{"query":"query Revenue { revenue { total } }"}',
      ]),
    ).toBe(true);
  });

  it("blocks mutations", () => {
    expect(
      isGraphjinCommandSafe([
        "cli",
        "execute_graphql",
        "--args",
        '{"query":"mutation Dangerous { delete_user(id: 1) }"}',
      ]),
    ).toBe(false);
  });

  it("blocks config-changing commands", () => {
    expect(isGraphjinCommandSafe(["config", "set", "admin_secret", "x"])).toBe(false);
  });
});

describe("ensureGraphjinGuard wrapper script", () => {
  let dir: string;
  let wrapper: string;
  beforeAll(async () => {
    dir = await mkdtemp(join(tmpdir(), "neko-guard-"));
    wrapper = await ensureGraphjinGuard(dir, "/bin/echo");
  });
  afterAll(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("is syntactically valid bash", () => {
    const r = spawnSync("bash", ["-n", wrapper], { encoding: "utf8" });
    expect(r.status, `bash -n stderr: ${r.stderr}`).toBe(0);
  });

  it("execs the underlying binary for read queries", () => {
    const r = spawnSync(wrapper, [
      "cli",
      "execute_graphql",
      "--args",
      '{"query":"{ revenue { total } }"}',
    ], { encoding: "utf8" });
    expect(r.status).toBe(0);
    expect(r.stdout).toContain("execute_graphql");
  });

  it("blocks mutations in --args payloads (substring match)", () => {
    const r = spawnSync(wrapper, [
      "cli",
      "execute_graphql",
      "--args",
      '{"query":"mutation Bad { delete_user(id: 1) }"}',
    ], { encoding: "utf8" });
    expect(r.status).toBe(2);
    expect(r.stderr).toContain("blocks GraphJin mutations");
  });

  it("blocks `serve` as a whole-word subcommand", () => {
    const r = spawnSync(wrapper, ["serve"], { encoding: "utf8" });
    expect(r.status).toBe(2);
  });

  it("does NOT false-positive on whole-word `preserve` or `newest`", () => {
    const r = spawnSync(wrapper, [
      "cli",
      "execute_graphql",
      "--args",
      '{"query":"{ products(order_by: { newest: desc }) { id } }"}',
    ], { encoding: "utf8" });
    expect(r.status).toBe(0);
  });
});
