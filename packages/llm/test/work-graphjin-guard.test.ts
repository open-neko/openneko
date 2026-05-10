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

  it("blocks the documented write subcommands", () => {
    for (const sub of [
      "save_workflow",
      "update_current_config",
      "apply_schema_changes",
      "reload_schema",
      "apply_database_setup",
      "preview_schema_changes",
    ]) {
      expect(isGraphjinCommandSafe(["cli", sub, "--args", "{}"])).toBe(false);
    }
  });

  it("blocks ANY non-`cli` first argument (serve, migrate, admin, …)", () => {
    expect(isGraphjinCommandSafe(["serve"])).toBe(false);
    expect(isGraphjinCommandSafe(["migrate"])).toBe(false);
    expect(isGraphjinCommandSafe(["admin"])).toBe(false);
    expect(isGraphjinCommandSafe([])).toBe(false);
  });

  it("does NOT false-positive on identifiers inside read queries", () => {
    expect(
      isGraphjinCommandSafe([
        "cli",
        "execute_graphql",
        "--args",
        '{"query":"{ products(order_by: { newest: desc }) { config_value preserve_id } }"}',
      ]),
    ).toBe(true);
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

  it("blocks `save_workflow` under cli", () => {
    const r = spawnSync(wrapper, ["cli", "save_workflow", "--args", "{}"], {
      encoding: "utf8",
    });
    expect(r.status).toBe(2);
    expect(r.stderr).toContain("blocks GraphJin write");
  });

  it("blocks `serve` outright — it is the server, the agent never invokes it", () => {
    const r = spawnSync(wrapper, ["serve"], { encoding: "utf8" });
    expect(r.status).toBe(2);
    expect(r.stderr).toContain("only 'graphjin cli");
  });

  it("blocks bare invocations and any non-`cli` first argument", () => {
    for (const argv of [[], ["migrate"], ["admin"], ["config"], ["new"]]) {
      const r = spawnSync(wrapper, argv, { encoding: "utf8" });
      expect(r.status, `argv=${JSON.stringify(argv)} should be denied`).toBe(2);
    }
  });

  it("does NOT false-positive on column names like `newest`/`config_value`/`preserve_id`", () => {
    const r = spawnSync(wrapper, [
      "cli",
      "execute_graphql",
      "--args",
      '{"query":"{ products(order_by: { newest: desc }) { config_value preserve_id } }"}',
    ], { encoding: "utf8" });
    expect(r.status).toBe(0);
  });
});
