import { existsSync } from "node:fs";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { runInit } from "../src/commands/init";
import { runList } from "../src/commands/list";
import { parseInstallSpec, runInstall } from "../src/commands/install";
import { runRemove } from "../src/commands/remove";
import { runDoctor } from "../src/commands/doctor";
import {
  runMarketplaceAdd,
  runMarketplaceList,
  runMarketplaceRemove,
} from "../src/commands/marketplace";
import {
  OFFICIAL_MARKETPLACE_URL,
  PLUGIN_MANIFEST_FILE,
  PLUGIN_MANIFEST_SCHEMA_URL,
  type Marketplace,
  type MarketplaceClient,
  type ManifestEntry,
} from "@open-neko/plugin-install";
import { writeStore } from "../src/marketplace-store";

const INTEGRITY = "sha512-" + "a".repeat(86) + "==";

function marketplaceWith(plugins: Marketplace["plugins"]): Marketplace {
  return {
    name: "Official",
    owner: "open-neko",
    description: "test",
    plugins,
  };
}

function defaultClient(marketplaces: Map<string, Marketplace>): MarketplaceClient {
  return {
    async fetch(url: string) {
      const m = marketplaces.get(url);
      if (!m) throw new Error(`fake client: no fixture for ${url}`);
      return m;
    },
  };
}

describe("parseInstallSpec", () => {
  it("returns marketplaceRef null when only the name is given", () => {
    expect(parseInstallSpec("@open-neko/plugin-x")).toEqual({
      name: "@open-neko/plugin-x",
      marketplaceRef: null,
    });
  });

  it("treats a leading @ as an npm scope, not a marketplace ref", () => {
    expect(parseInstallSpec("@acme/plugin").marketplaceRef).toBeNull();
  });

  it("splits on the last @ when there is a marketplace ref", () => {
    expect(parseInstallSpec("@acme/plugin@acme")).toEqual({
      name: "@acme/plugin",
      marketplaceRef: "acme",
    });
    expect(parseInstallSpec("foo@bar")).toEqual({
      name: "foo",
      marketplaceRef: "bar",
    });
  });

  it("treats a trailing @ with no marketplace as just a name", () => {
    expect(parseInstallSpec("foo@")).toEqual({
      name: "foo@",
      marketplaceRef: null,
    });
  });
});

describe("init", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "cli-init-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("creates an empty manifest when none exists", async () => {
    const r = await runInit({ repoRoot: dir });
    expect(r.created).toBe(true);
    expect(existsSync(path.join(dir, PLUGIN_MANIFEST_FILE))).toBe(true);
  });

  it("does nothing when the manifest already exists", async () => {
    await runInit({ repoRoot: dir });
    const second = await runInit({ repoRoot: dir });
    expect(second.created).toBe(false);
  });
});

describe("install", () => {
  let repoDir: string;
  let configDir: string;

  beforeEach(async () => {
    repoDir = await mkdtemp(path.join(tmpdir(), "cli-install-"));
    configDir = await mkdtemp(path.join(tmpdir(), "cli-config-"));
  });

  afterEach(async () => {
    await rm(repoDir, { recursive: true, force: true });
    await rm(configDir, { recursive: true, force: true });
  });

  it("installs from the official marketplace and writes the manifest entry with marketplace=official", async () => {
    const npmCalls: Array<{ args: string[] }> = [];
    const market = marketplaceWith([
      {
        name: "@open-neko/plugin-parallel-search",
        title: "Parallel.ai Search",
        description: "...",
        source: "https://github.com/open-neko/plugins",
        versions: [
          {
            version: "0.2.0",
            integrity: INTEGRITY,
            requires_network: ["search.parallel.ai"],
            kinds: ["web_search", "web_fetch"],
            publishedAt: "2026-05-17",
          },
        ],
      },
    ]);
    const fixtures = new Map([[OFFICIAL_MARKETPLACE_URL, market]]);

    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-parallel-search",
      configDir,
      marketplaceClient: defaultClient(fixtures),
      npmRunner: async (args) => {
        npmCalls.push({ args });
      },
    });

    expect(result.version).toBe("0.2.0");
    expect(result.source).toBe("marketplace");
    expect(result.marketplace).toBe("official");
    expect(npmCalls[0]?.args).toEqual([
      "install",
      "@open-neko/plugin-parallel-search@0.2.0",
    ]);
    const written = JSON.parse(
      await readFile(path.join(repoDir, PLUGIN_MANIFEST_FILE), "utf8"),
    ) as { plugins: ManifestEntry[] };
    expect(written.plugins[0]?.marketplace).toBe("official");
    expect(written.plugins[0]?.capabilities.network).toEqual([
      "search.parallel.ai",
    ]);
  });

  it("errors when the plugin isn't in any trusted marketplace", async () => {
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([])],
    ]);
    await expect(
      runInstall({
        repoRoot: repoDir,
        spec: "@open-neko/plugin-missing",
        configDir,
        marketplaceClient: defaultClient(fixtures),
        npmRunner: async () => {},
      }),
    ).rejects.toThrow(/not found in any trusted marketplace/);
  });

  it("errors when the same plugin appears in multiple trusted marketplaces", async () => {
    // Trust a second marketplace alongside the official one.
    await writeStore(
      {
        marketplaces: [
          {
            name: "official",
            url: OFFICIAL_MARKETPLACE_URL,
            addedAt: "1970-01-01",
            official: true,
          },
          {
            name: "acme",
            url: "https://acme.com/marketplace.json",
            addedAt: "2026-05-17",
          },
        ],
      },
      configDir,
    );
    const plugin = {
      name: "@conflict/plugin",
      title: "x",
      description: "y",
      source: "https://github.com/x/y",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          requires_network: [],
          kinds: ["k"],
          publishedAt: "2026-05-17",
        },
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
      ["https://acme.com/marketplace.json", { ...marketplaceWith([plugin]), name: "Acme" }],
    ]);
    await expect(
      runInstall({
        repoRoot: repoDir,
        spec: "@conflict/plugin",
        configDir,
        marketplaceClient: defaultClient(fixtures),
        npmRunner: async () => {},
      }),
    ).rejects.toThrow(/multiple trusted marketplaces/);
  });

  it("scoped install (<name>@<marketplace>) targets exactly one marketplace", async () => {
    await writeStore(
      {
        marketplaces: [
          {
            name: "official",
            url: OFFICIAL_MARKETPLACE_URL,
            addedAt: "1970-01-01",
            official: true,
          },
          {
            name: "acme",
            url: "https://acme.com/marketplace.json",
            addedAt: "2026-05-17",
          },
        ],
      },
      configDir,
    );
    const plugin = {
      name: "@acme/plugin",
      title: "x",
      description: "y",
      source: "https://github.com/acme/x",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          requires_network: [],
          kinds: ["k"],
          publishedAt: "2026-05-17",
        },
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([])],
      ["https://acme.com/marketplace.json", { ...marketplaceWith([plugin]), name: "Acme" }],
    ]);
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@acme/plugin@acme",
      configDir,
      marketplaceClient: defaultClient(fixtures),
      npmRunner: async () => {},
    });
    expect(result.marketplace).toBe("acme");
  });

  it("scoped install errors when the marketplace ref isn't trusted", async () => {
    await expect(
      runInstall({
        repoRoot: repoDir,
        spec: "@acme/plugin@unknown",
        configDir,
        marketplaceClient: defaultClient(new Map()),
        npmRunner: async () => {},
      }),
    ).rejects.toThrow(/not trusted/);
  });

  it("install prompts for required env keys not already in the secrets store", async () => {
    const plugin = {
      name: "@open-neko/plugin-slack",
      title: "Slack",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          requires_network: ["slack.com"],
          requires_env: [
            {
              key: "SLACK_BOT_TOKEN",
              required: true,
              secret: true,
              description: "xoxb- token",
            },
            {
              key: "SLACK_DEFAULT_CHANNEL",
              required: false,
              secret: false,
              description: "default channel",
            },
          ],
          kinds: ["send_slack_message"],
          publishedAt: "2026-05-17",
        },
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    const prompted: string[] = [];
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-slack",
      configDir,
      marketplaceClient: defaultClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async (_pkg, req) => {
        prompted.push(req.key);
        return `value-for-${req.key}`;
      },
    });
    // Only the required env (SLACK_BOT_TOKEN) gets prompted for.
    expect(prompted).toEqual(["SLACK_BOT_TOKEN"]);
    expect(result.envSaved).toEqual(["SLACK_BOT_TOKEN"]);
    expect(result.envAlreadySet).toEqual([]);
    // The secret landed in the per-user store.
    const stored = JSON.parse(
      await readFile(path.join(configDir, "secrets.json"), "utf8"),
    );
    expect(stored["@open-neko/plugin-slack"].SLACK_BOT_TOKEN).toBe(
      "value-for-SLACK_BOT_TOKEN",
    );
  });

  it("install reports envAlreadySet for env already present in the store", async () => {
    await writeFile(
      path.join(configDir, "secrets.json"),
      JSON.stringify({
        "@open-neko/plugin-slack": {
          SLACK_BOT_TOKEN: "preexisting",
        },
      }),
      "utf8",
    );
    const plugin = {
      name: "@open-neko/plugin-slack",
      title: "Slack",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          requires_network: ["slack.com"],
          requires_env: [
            {
              key: "SLACK_BOT_TOKEN",
              required: true,
              secret: true,
              description: "xoxb-",
            },
          ],
          kinds: ["send_slack_message"],
          publishedAt: "2026-05-17",
        },
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-slack",
      configDir,
      marketplaceClient: defaultClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => {
        throw new Error("should not prompt");
      },
    });
    expect(result.envAlreadySet).toEqual(["SLACK_BOT_TOKEN"]);
    expect(result.envSaved).toEqual([]);
  });

  it("install errors when envPrompt returns an empty string for a required key", async () => {
    const plugin = {
      name: "@open-neko/plugin-slack",
      title: "Slack",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          requires_network: ["slack.com"],
          requires_env: [
            {
              key: "SLACK_BOT_TOKEN",
              required: true,
              secret: true,
              description: "xoxb-",
            },
          ],
          kinds: ["send_slack_message"],
          publishedAt: "2026-05-17",
        },
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    await expect(
      runInstall({
        repoRoot: repoDir,
        spec: "@open-neko/plugin-slack",
        configDir,
        marketplaceClient: defaultClient(fixtures),
        npmRunner: async () => {},
        envPrompt: async () => "",
      }),
    ).rejects.toThrow(/not supplied/);
  });
});

describe("list", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "cli-list-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("reports no manifest when absent", async () => {
    expect((await runList({ repoRoot: dir })).hadManifest).toBe(false);
  });

  it("returns the entries from the manifest", async () => {
    await writeFile(
      path.join(dir, PLUGIN_MANIFEST_FILE),
      JSON.stringify({
        schema: PLUGIN_MANIFEST_SCHEMA_URL,
        plugins: [
          {
            name: "@open-neko/plugin-parallel-search",
            version: "0.2.0",
            integrity: INTEGRITY,
            capabilities: { network: ["search.parallel.ai"] },
            marketplace: "official",
          },
        ],
      }),
      "utf8",
    );
    const r = await runList({ repoRoot: dir });
    expect(r.entries).toHaveLength(1);
    expect(r.entries[0]?.marketplace).toBe("official");
  });
});

describe("remove", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "cli-rm-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("removes an entry by name", async () => {
    await writeFile(
      path.join(dir, PLUGIN_MANIFEST_FILE),
      JSON.stringify({
        schema: PLUGIN_MANIFEST_SCHEMA_URL,
        plugins: [
          {
            name: "@open-neko/plugin-x",
            version: "0.1.0",
            integrity: INTEGRITY,
            capabilities: { network: [] },
          },
        ],
      }),
      "utf8",
    );
    const r = await runRemove({ repoRoot: dir, name: "@open-neko/plugin-x" });
    expect(r.removed).toBe(true);
  });
});

describe("doctor", () => {
  let dir: string;
  beforeEach(async () => {
    dir = await mkdtemp(path.join(tmpdir(), "cli-doc-"));
  });
  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("reports host triple and manifest state", async () => {
    const r = await runDoctor({ repoRoot: dir });
    expect(r.host.triple).toMatch(/-/);
    expect(r.manifest.present).toBe(false);
  });
});

describe("marketplace add / list / remove", () => {
  let configDir: string;

  beforeEach(async () => {
    configDir = await mkdtemp(path.join(tmpdir(), "cli-mkt-"));
  });

  afterEach(async () => {
    await rm(configDir, { recursive: true, force: true });
  });

  function fakeClient(url: string, market: Marketplace): MarketplaceClient {
    return {
      async fetch(u) {
        if (u !== url) throw new Error(`unexpected ${u}`);
        return market;
      },
    };
  }

  it("list initially returns just the official marketplace", async () => {
    const r = await runMarketplaceList({ configDir });
    expect(r.marketplaces).toHaveLength(1);
    expect(r.marketplaces[0]?.official).toBe(true);
  });

  it("add fetches the marketplace, derives a slug, and records it", async () => {
    const url = "https://acme.com/marketplace.json";
    const market = marketplaceWith([]);
    market.name = "Acme Marketplace";
    const result = await runMarketplaceAdd({
      url,
      configDir,
      client: fakeClient(url, market),
    });
    expect(result.added.name).toBe("acme-marketplace");
    expect(result.added.url).toBe(url);
    expect(result.pluginCount).toBe(0);
    const after = await runMarketplaceList({ configDir });
    expect(after.marketplaces.map((m) => m.name)).toContain("acme-marketplace");
  });

  it("add rejects a duplicate", async () => {
    const url = "https://acme.com/marketplace.json";
    const market = marketplaceWith([]);
    market.name = "Acme";
    const client = fakeClient(url, market);
    await runMarketplaceAdd({ url, configDir, client });
    await expect(
      runMarketplaceAdd({ url, configDir, client }),
    ).rejects.toThrow(/already trusted/);
  });

  it("remove deletes a non-official marketplace", async () => {
    const url = "https://acme.com/marketplace.json";
    const market = marketplaceWith([]);
    market.name = "Acme";
    await runMarketplaceAdd({
      url,
      configDir,
      client: fakeClient(url, market),
    });
    const r = await runMarketplaceRemove({
      nameOrUrl: "acme",
      configDir,
    });
    expect(r.removed?.name).toBe("acme");
  });

  it("remove refuses the official marketplace", async () => {
    await expect(
      runMarketplaceRemove({
        nameOrUrl: "official",
        configDir,
      }),
    ).rejects.toThrow(/refusing to remove/);
  });
});
