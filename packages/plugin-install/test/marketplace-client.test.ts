import { describe, expect, it } from "vitest";
import {
  createMarketplaceClient,
  findPlugin,
  pickInstallVersion,
  semverCompare,
  type Marketplace,
} from "../src/marketplace-client";

const INTEGRITY = "sha512-" + "a".repeat(86) + "==";

function marketplace(): Marketplace {
  return {
    name: "Test",
    owner: "test",
    description: "Test marketplace used by the plugin-install suite.",
    plugins: [
      {
        name: "@test/plugin-a",
        title: "A",
        description: "A",
        source: "https://github.com/test/a",
        versions: [
          {
            version: "0.1.0",
            integrity: INTEGRITY,
            requires_network: [],
            kinds: ["a"],
            publishedAt: "2026-05-17",
          },
          {
            version: "0.2.0",
            integrity: INTEGRITY,
            requires_network: ["api.example.com"],
            kinds: ["a"],
            publishedAt: "2026-05-18",
          },
        ],
      },
    ],
  };
}

function fakeFetch(body: unknown, status = 200): typeof fetch {
  return (async () =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })) as unknown as typeof fetch;
}

describe("createMarketplaceClient.fetch", () => {
  it("returns a parsed marketplace on 200", async () => {
    const client = createMarketplaceClient({ fetchImpl: fakeFetch(marketplace()) });
    const m = await client.fetch("https://example.test/marketplace.json");
    expect(m.name).toBe("Test");
    expect(m.plugins).toHaveLength(1);
  });

  it("throws on non-2xx", async () => {
    const client = createMarketplaceClient({ fetchImpl: fakeFetch({}, 404) });
    await expect(client.fetch("https://x")).rejects.toThrow(/404/);
  });

  it("throws when body does not match marketplace shape", async () => {
    const client = createMarketplaceClient({
      fetchImpl: fakeFetch({ wrong: "shape" }),
    });
    await expect(client.fetch("https://x")).rejects.toThrow(/expected shape/);
  });
});

describe("findPlugin", () => {
  it("returns matching plugin or null", () => {
    const m = marketplace();
    expect(findPlugin(m, "@test/plugin-a")?.name).toBe("@test/plugin-a");
    expect(findPlugin(m, "@test/missing")).toBeNull();
  });
});

describe("pickInstallVersion", () => {
  it("picks the latest non-yanked version by default", () => {
    const plugin = marketplace().plugins[0]!;
    expect(pickInstallVersion(plugin).version).toBe("0.2.0");
  });
  it("honours an explicit version request", () => {
    const plugin = marketplace().plugins[0]!;
    expect(pickInstallVersion(plugin, "0.1.0").version).toBe("0.1.0");
  });
  it("throws when the requested version is yanked", () => {
    const m = marketplace();
    const plugin = m.plugins[0]!;
    plugin.versions[1]!.yanked = true;
    expect(() => pickInstallVersion(plugin, "0.2.0")).toThrow(/no published non-yanked/);
  });
  it("throws when every version is yanked", () => {
    const m = marketplace();
    const plugin = m.plugins[0]!;
    plugin.versions.forEach((v) => (v.yanked = true));
    expect(() => pickInstallVersion(plugin)).toThrow(/yanked/);
  });
});

describe("semverCompare", () => {
  it("orders by major, minor, patch", () => {
    expect(semverCompare("1.0.0", "0.9.9") > 0).toBe(true);
    expect(semverCompare("0.2.0", "0.1.99") > 0).toBe(true);
    expect(semverCompare("0.1.1", "0.1.0") > 0).toBe(true);
  });
  it("treats pre-release < release", () => {
    expect(semverCompare("1.0.0", "1.0.0-rc1") > 0).toBe(true);
  });
});
