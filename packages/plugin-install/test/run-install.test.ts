import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  parseInstallSpec,
  runInstall,
  type InstallOptions,
} from "../src/run-install";
import {
  OFFICIAL_MARKETPLACE_URL,
  type Marketplace,
  type MarketplaceClient,
} from "../src/marketplace-client";
import { PLUGIN_MANIFEST_FILE, PLUGIN_MANIFEST_SCHEMA_URL } from "../src/manifest";

const INTEGRITY = "sha512-" + "a".repeat(86) + "==";

function marketplaceWith(plugins: Marketplace["plugins"]): Marketplace {
  return {
    name: "Official",
    owner: "open-neko",
    description: "test",
    plugins,
  };
}

function fakeClient(fixtures: Map<string, Marketplace>): MarketplaceClient {
  return {
    async fetch(url) {
      const m = fixtures.get(url);
      if (!m) throw new Error(`no fixture for ${url}`);
      return m;
    },
  };
}

describe("parseInstallSpec", () => {
  it("treats a leading @ as an npm scope, not a marketplace ref", () => {
    expect(parseInstallSpec("@open-neko/plugin-x")).toEqual({
      name: "@open-neko/plugin-x",
      marketplaceRef: null,
    });
  });
  it("splits on the last @ when a marketplace ref is present", () => {
    expect(parseInstallSpec("@acme/plugin@acme")).toEqual({
      name: "@acme/plugin",
      marketplaceRef: "acme",
    });
  });
  it("treats a trailing @ with no marketplace as just a name", () => {
    expect(parseInstallSpec("foo@")).toEqual({
      name: "foo@",
      marketplaceRef: null,
    });
  });
});

describe("runInstall", () => {
  let repoDir: string;
  let configDir: string;

  beforeEach(async () => {
    repoDir = await mkdtemp(path.join(tmpdir(), "pi-install-"));
    configDir = await mkdtemp(path.join(tmpdir(), "pi-config-"));
  });
  afterEach(async () => {
    await rm(repoDir, { recursive: true, force: true });
    await rm(configDir, { recursive: true, force: true });
  });

  const officialMarketplace = {
    name: "official" as const,
    url: OFFICIAL_MARKETPLACE_URL,
  };

  it("installs from the official marketplace + writes manifest entry", async () => {
    const npmCalls: Array<{ args: string[] }> = [];
    const plugin = {
      name: "@open-neko/plugin-good",
      title: "Good",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          requires_network: [],
          kinds: ["x"],
          publishedAt: "2026-05-17",
        },
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-good",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async (args) => {
        npmCalls.push({ args });
      },
      envPrompt: async () => {
        throw new Error("should not prompt");
      },
    });
    expect(result.version).toBe("0.1.0");
    expect(result.source).toBe("marketplace");
    expect(result.marketplace).toBe("official");
    expect(npmCalls[0]?.args).toEqual([
      "install",
      "@open-neko/plugin-good@0.1.0",
    ]);
    const written = JSON.parse(
      await readFile(path.join(repoDir, PLUGIN_MANIFEST_FILE), "utf8"),
    );
    expect(written.plugins[0].marketplace).toBe("official");
    expect(written.schema).toBe(PLUGIN_MANIFEST_SCHEMA_URL);
  });

  it("prompts for missing required env keys and stores them", async () => {
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
    const prompted: string[] = [];
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-slack",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async (_pkg, req) => {
        prompted.push(req.key);
        return `value-for-${req.key}`;
      },
    });
    expect(prompted).toEqual(["SLACK_BOT_TOKEN"]);
    expect(result.envSaved).toEqual(["SLACK_BOT_TOKEN"]);
    expect(result.envAlreadySet).toEqual([]);
    const stored = JSON.parse(
      await readFile(path.join(configDir, "secrets.json"), "utf8"),
    );
    expect(stored["@open-neko/plugin-slack"].SLACK_BOT_TOKEN).toBe(
      "value-for-SLACK_BOT_TOKEN",
    );
  });

  it("reports envAlreadySet without re-prompting when value is pre-set", async () => {
    await writeFile(
      path.join(configDir, "secrets.json"),
      JSON.stringify({
        "@open-neko/plugin-slack": { SLACK_BOT_TOKEN: "preexisting" },
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
            { key: "SLACK_BOT_TOKEN", required: true, secret: true, description: "x" },
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
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => {
        throw new Error("should not prompt");
      },
    });
    expect(result.envAlreadySet).toEqual(["SLACK_BOT_TOKEN"]);
    expect(result.envSaved).toEqual([]);
  });

  it("errors when envPrompt returns empty for a required key", async () => {
    const plugin = {
      name: "@open-neko/plugin-x",
      title: "x",
      description: "x",
      source: "https://github.com/x/x",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          requires_network: [],
          requires_env: [
            { key: "X_KEY", required: true, secret: true, description: "x" },
          ],
          kinds: ["x"],
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
        spec: "@open-neko/plugin-x",
        trustedMarketplaces: [officialMarketplace],
        secretsConfigDir: configDir,
        marketplaceClient: fakeClient(fixtures),
        npmRunner: async () => {},
        envPrompt: async () => "",
      } satisfies InstallOptions),
    ).rejects.toThrow(/not supplied/);
  });

  it("scoped install (<name>@<marketplace>) targets a single marketplace", async () => {
    const plugin = {
      name: "@acme/plugin",
      title: "x",
      description: "x",
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
      ["https://acme.test/marketplace.json", { ...marketplaceWith([plugin]), name: "Acme" }],
    ]);
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@acme/plugin@acme",
      trustedMarketplaces: [
        officialMarketplace,
        { name: "acme", url: "https://acme.test/marketplace.json" },
      ],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => "",
    });
    expect(result.marketplace).toBe("acme");
  });

  it("errors on conflict when a plugin appears in multiple trusted marketplaces", async () => {
    const plugin = {
      name: "@conflict/p",
      title: "x",
      description: "x",
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
      ["https://acme.test/marketplace.json", { ...marketplaceWith([plugin]), name: "Acme" }],
    ]);
    await expect(
      runInstall({
        repoRoot: repoDir,
        spec: "@conflict/p",
        trustedMarketplaces: [
          officialMarketplace,
          { name: "acme", url: "https://acme.test/marketplace.json" },
        ],
        secretsConfigDir: configDir,
        marketplaceClient: fakeClient(fixtures),
        npmRunner: async () => {},
        envPrompt: async () => "",
      }),
    ).rejects.toThrow(/multiple trusted marketplaces/);
  });
});
