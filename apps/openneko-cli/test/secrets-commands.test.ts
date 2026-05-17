import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  runSecretsList,
  runSecretsSet,
  runSecretsUnset,
} from "../src/commands/secrets";
import { defaultSecretsPath } from "@open-neko/plugin-install";

describe("runSecretsSet", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "sc-set-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("writes a new value and reports newKey=true", async () => {
    const r = await runSecretsSet({
      configDir: dir,
      plugin: "@x/y",
      key: "TOKEN",
      value: "abc",
    });
    expect(r).toEqual({ plugin: "@x/y", key: "TOKEN", newKey: true });
    const stored = JSON.parse(await readFile(defaultSecretsPath(dir), "utf8"));
    expect(stored["@x/y"].TOKEN).toBe("abc");
  });

  it("reports newKey=false when overwriting", async () => {
    await runSecretsSet({
      configDir: dir,
      plugin: "@x/y",
      key: "TOKEN",
      value: "abc",
    });
    const r = await runSecretsSet({
      configDir: dir,
      plugin: "@x/y",
      key: "TOKEN",
      value: "def",
    });
    expect(r.newKey).toBe(false);
  });

  it("rejects bad key names", async () => {
    await expect(
      runSecretsSet({
        configDir: dir,
        plugin: "@x/y",
        key: "lower",
        value: "v",
      }),
    ).rejects.toThrow(/UPPER_SNAKE_CASE/);
  });

  it("errors when value is undefined", async () => {
    await expect(
      runSecretsSet({
        configDir: dir,
        plugin: "@x/y",
        key: "K",
      } as Parameters<typeof runSecretsSet>[0]),
    ).rejects.toThrow(/value required/);
  });
});

describe("runSecretsList", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "sc-list-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("returns empty when no secrets stored", async () => {
    const r = await runSecretsList({ configDir: dir });
    expect(r.entries).toEqual([]);
  });

  it("returns sorted entries for all plugins", async () => {
    await runSecretsSet({ configDir: dir, plugin: "@b/p", key: "K", value: "v" });
    await runSecretsSet({ configDir: dir, plugin: "@a/p", key: "K", value: "v" });
    const r = await runSecretsList({ configDir: dir });
    expect(r.entries.map((e) => e.plugin)).toEqual(["@a/p", "@b/p"]);
  });

  it("scoped list returns just the requested plugin", async () => {
    await runSecretsSet({ configDir: dir, plugin: "@x/y", key: "K", value: "v" });
    const r = await runSecretsList({ configDir: dir, plugin: "@x/y" });
    expect(r.entries).toEqual([{ plugin: "@x/y", keys: ["K"] }]);
  });
});

describe("runSecretsUnset", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "sc-unset-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("removes a stored key", async () => {
    await runSecretsSet({ configDir: dir, plugin: "@x/y", key: "K", value: "v" });
    const r = await runSecretsUnset({ configDir: dir, plugin: "@x/y", key: "K" });
    expect(r.removed).toBe(true);
  });

  it("reports removed=false when missing", async () => {
    const r = await runSecretsUnset({
      configDir: dir,
      plugin: "@x/y",
      key: "K",
    });
    expect(r.removed).toBe(false);
  });
});
