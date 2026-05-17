import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  addToStore,
  readStore,
  removeFromStore,
  slugify,
  writeStore,
} from "../src/marketplace-store";
import {
  OFFICIAL_MARKETPLACE_NAME,
  OFFICIAL_MARKETPLACE_URL,
} from "@open-neko/plugin-install";

describe("slugify", () => {
  it("lowercases, dash-collapses, trims", () => {
    expect(slugify("OpenNeko Official!")).toBe("openneko-official");
    expect(slugify("Acme  Plugins / Inc.")).toBe("acme-plugins-inc");
    expect(slugify("@scope/name")).toBe("scope-name");
  });
});

describe("readStore / writeStore", () => {
  let dir: string;

  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "mkt-store-"));
  });

  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("auto-creates the store with the official marketplace on first read", async () => {
    const store = await readStore(dir);
    expect(store.marketplaces).toHaveLength(1);
    expect(store.marketplaces[0]?.name).toBe(OFFICIAL_MARKETPLACE_NAME);
    expect(store.marketplaces[0]?.url).toBe(OFFICIAL_MARKETPLACE_URL);
    expect(store.marketplaces[0]?.official).toBe(true);
    const file = JSON.parse(
      await readFile(path.join(dir, "marketplaces.json"), "utf8"),
    );
    expect(file.marketplaces).toHaveLength(1);
  });

  it("re-injects the official marketplace if it's missing", async () => {
    await writeStore({ marketplaces: [] }, dir);
    const store = await readStore(dir);
    expect(
      store.marketplaces.some((m) => m.name === OFFICIAL_MARKETPLACE_NAME),
    ).toBe(true);
  });

  it("throws on malformed JSON", async () => {
    const file = path.join(dir, "marketplaces.json");
    await (await import("node:fs/promises")).writeFile(file, "not json", "utf8");
    await expect(readStore(dir)).rejects.toThrow(/invalid JSON/);
  });
});

describe("addToStore / removeFromStore", () => {
  const baseStore = {
    marketplaces: [
      {
        name: OFFICIAL_MARKETPLACE_NAME,
        url: OFFICIAL_MARKETPLACE_URL,
        addedAt: "1970-01-01",
        official: true,
      },
    ],
  };

  it("adds a new entry", () => {
    const next = addToStore(baseStore, {
      name: "acme",
      url: "https://acme.com/marketplace.json",
      addedAt: "2026-05-17",
    });
    expect(next.marketplaces).toHaveLength(2);
  });

  it("rejects a duplicate name", () => {
    expect(() =>
      addToStore(baseStore, {
        name: OFFICIAL_MARKETPLACE_NAME,
        url: "https://different.com/marketplace.json",
        addedAt: "x",
      }),
    ).toThrow(/already trusted/);
  });

  it("rejects a duplicate URL", () => {
    expect(() =>
      addToStore(baseStore, {
        name: "acme",
        url: OFFICIAL_MARKETPLACE_URL,
        addedAt: "x",
      }),
    ).toThrow(/already trusted/);
  });

  it("refuses to remove the official marketplace", () => {
    expect(() =>
      removeFromStore(baseStore, OFFICIAL_MARKETPLACE_NAME),
    ).toThrow(/refusing to remove/);
  });

  it("removes a non-official entry by name or URL", () => {
    const seeded = addToStore(baseStore, {
      name: "acme",
      url: "https://acme.com/marketplace.json",
      addedAt: "x",
    });
    const byName = removeFromStore(seeded, "acme");
    expect(byName.removed?.name).toBe("acme");
    expect(byName.store.marketplaces).toHaveLength(1);
    const byUrl = removeFromStore(seeded, "https://acme.com/marketplace.json");
    expect(byUrl.removed?.name).toBe("acme");
  });

  it("returns null when the name/URL is not in the store", () => {
    const result = removeFromStore(baseStore, "ghost");
    expect(result.removed).toBeNull();
  });
});
