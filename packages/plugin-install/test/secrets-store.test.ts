import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  allSecretValues,
  defaultSecretsPath,
  isValidEnvKey,
  listKeysForPlugin,
  readSecretsStore,
  readSecretsStoreSoft,
  setSecret,
  unsetSecret,
  writeSecretsStore,
  type SecretsStore,
} from "../src/secrets-store";

describe("isValidEnvKey", () => {
  it("accepts UPPER_SNAKE_CASE", () => {
    expect(isValidEnvKey("SLACK_BOT_TOKEN")).toBe(true);
    expect(isValidEnvKey("X")).toBe(true);
  });
  it("rejects bad inputs", () => {
    expect(isValidEnvKey("slack")).toBe(false);
    expect(isValidEnvKey("1KEY")).toBe(false);
    expect(isValidEnvKey("FOO; rm")).toBe(false);
  });
});

describe("setSecret / unsetSecret / listKeysForPlugin", () => {
  it("setSecret writes a new value", () => {
    expect(setSecret({}, "@x/y", "K", "v")["@x/y"]?.K).toBe("v");
  });
  it("setSecret refuses bad keys", () => {
    expect(() => setSecret({}, "@x/y", "bad-key", "v")).toThrow(/UPPER_SNAKE_CASE/);
  });
  it("setSecret overwrites existing keys", () => {
    let s: SecretsStore = setSecret({}, "@x/y", "K", "v1");
    s = setSecret(s, "@x/y", "K", "v2");
    expect(s["@x/y"]?.K).toBe("v2");
  });
  it("unsetSecret removes a key + reports removal", () => {
    const s = setSecret({}, "@x/y", "K", "v");
    const { store, removed } = unsetSecret(s, "@x/y", "K");
    expect(removed).toBe(true);
    expect(store).toEqual({});
  });
  it("unsetSecret drops the plugin entry when no keys remain", () => {
    let s: SecretsStore = setSecret({}, "@x/y", "A", "1");
    s = setSecret(s, "@x/y", "B", "2");
    const { store } = unsetSecret(s, "@x/y", "A");
    expect(store["@x/y"]).toEqual({ B: "2" });
    const after = unsetSecret(store, "@x/y", "B");
    expect(after.store).toEqual({});
  });
  it("unsetSecret no-ops for missing keys", () => {
    expect(unsetSecret({}, "@x/y", "K").removed).toBe(false);
  });
  it("listKeysForPlugin returns sorted keys", () => {
    let s: SecretsStore = {};
    s = setSecret(s, "@x/y", "BANANA", "1");
    s = setSecret(s, "@x/y", "APPLE", "2");
    expect(listKeysForPlugin(s, "@x/y")).toEqual(["APPLE", "BANANA"]);
  });
});

describe("read/writeSecretsStore", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "pi-secrets-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("readSecretsStore returns empty when file missing", async () => {
    expect(await readSecretsStore(dir)).toEqual({});
  });

  it("writeSecretsStore creates the file with 0600 perms", async () => {
    await writeSecretsStore({ "@x/y": { K: "v" } }, dir);
    const file = defaultSecretsPath(dir);
    const s = await stat(file);
    if (process.platform !== "win32") {
      expect(s.mode & 0o777).toBe(0o600);
    }
    expect((await readSecretsStore(dir))["@x/y"]?.K).toBe("v");
  });

  it("writeSecretsStore sorts plugins + keys deterministically", async () => {
    await writeSecretsStore(
      {
        "@b/p": { Z: "1", A: "2" },
        "@a/p": { B: "1", A: "2" },
      },
      dir,
    );
    const raw = await readFile(defaultSecretsPath(dir), "utf8");
    expect(raw.indexOf("@a/p")).toBeLessThan(raw.indexOf("@b/p"));
    const aBlock = raw.slice(raw.indexOf("@a/p"), raw.indexOf("@b/p"));
    expect(aBlock.indexOf("\"A\"")).toBeLessThan(aBlock.indexOf("\"B\""));
  });

  it("readSecretsStore throws on invalid JSON", async () => {
    await writeFile(defaultSecretsPath(dir), "not json", "utf8");
    await expect(readSecretsStore(dir)).rejects.toThrow(/invalid JSON/);
  });

  it("readSecretsStoreSoft returns empty + warns on invalid JSON", async () => {
    await writeFile(defaultSecretsPath(dir), "not json", "utf8");
    const warnings: string[] = [];
    const out = await readSecretsStoreSoft(dir, (m) => warnings.push(m));
    expect(out).toEqual({});
    expect(warnings.join("\n")).toMatch(/invalid JSON/);
  });

  it("readSecretsStore tolerates malformed plugin entries", async () => {
    await writeFile(
      defaultSecretsPath(dir),
      JSON.stringify({
        "@good/p": { K: "v" },
        "@bad/p": 42,
        "@other/p": { K: 42, GOOD: "yes" },
      }),
      "utf8",
    );
    const back = await readSecretsStore(dir);
    expect(back["@good/p"]?.K).toBe("v");
    expect(back["@bad/p"]).toBeUndefined();
    expect(back["@other/p"]).toEqual({ GOOD: "yes" });
  });
});

describe("allSecretValues", () => {
  it("returns the union of every value, deduplicated", () => {
    const store = {
      "@x/a": { K: "alpha", L: "beta" },
      "@x/b": { K: "alpha", M: "gamma" },
    };
    const values = allSecretValues(store);
    expect(values.sort()).toEqual(["alpha", "beta", "gamma"]);
  });
  it("ignores empty strings", () => {
    expect(allSecretValues({ "@x/y": { K: "" } })).toEqual([]);
  });
});

describe("defaultSecretsPath", () => {
  it("returns the secrets.json under the provided dir", () => {
    expect(defaultSecretsPath("/tmp/x")).toBe("/tmp/x/secrets.json");
  });
});
