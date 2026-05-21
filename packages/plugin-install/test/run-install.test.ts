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
  type MarketplaceVersion,
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

function actionVersion(
  overrides: Partial<MarketplaceVersion> & { kinds: Array<{ kind: string; description: string }> },
): MarketplaceVersion {
  const { kinds, ...rest } = overrides;
  return {
    version: "0.1.0",
    integrity: INTEGRITY,
    permissions: { network: [], env: [] },
    capabilities: { action: { kinds } },
    publishedAt: "2026-05-17",
    ...rest,
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
      versions: [actionVersion({ kinds: [{ kind: "x", description: "x" }] })],
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
    expect(written.plugins[0].capabilities.action.kinds[0].kind).toBe("x");
    expect(written.schema).toBe(PLUGIN_MANIFEST_SCHEMA_URL);
  });

  it("prompts for missing required env keys and stores them", async () => {
    const plugin = {
      name: "@open-neko/plugin-slack",
      title: "Slack",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [
        actionVersion({
          permissions: {
            network: ["slack.com"],
            env: [
              {
                key: "SLACK_BOT_TOKEN",
                required: true,
                secret: true,
                description: "xoxb-",
              },
            ],
          },
          kinds: [{ kind: "send_slack_message", description: "send" }],
        }),
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
        actionVersion({
          permissions: {
            network: ["slack.com"],
            env: [
              { key: "SLACK_BOT_TOKEN", required: true, secret: true, description: "x" },
            ],
          },
          kinds: [{ kind: "send_slack_message", description: "send" }],
        }),
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
        actionVersion({
          permissions: {
            network: [],
            env: [
              { key: "X_KEY", required: true, secret: true, description: "x" },
            ],
          },
          kinds: [{ kind: "x", description: "x" }],
        }),
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
      versions: [actionVersion({ kinds: [{ kind: "k", description: "k" }] })],
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

  it("copies the auth capability into the manifest entry when the marketplace version declares it", async () => {
    const plugin = {
      name: "@open-neko/plugin-scalekit",
      title: "Scalekit SSO",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [
        {
          version: "0.1.0",
          integrity: INTEGRITY,
          permissions: { network: ["*.scalekit.com"], env: [] },
          capabilities: { auth: { providerLabel: "Scalekit" } },
          publishedAt: "2026-05-17",
        } satisfies MarketplaceVersion,
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-scalekit",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => "",
    });
    const written = JSON.parse(
      await readFile(path.join(repoDir, PLUGIN_MANIFEST_FILE), "utf8"),
    );
    expect(written.plugins[0].capabilities.auth.providerLabel).toBe("Scalekit");
    expect(written.plugins[0].capabilities.action).toBeUndefined();
  });

  it("omits the auth capability from the manifest when the version doesn't declare one", async () => {
    const plugin = {
      name: "@open-neko/plugin-noauth",
      title: "noauth",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [
        actionVersion({
          kinds: [{ kind: "do_something", description: "do" }],
        }),
      ],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-noauth",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => "",
    });
    const written = JSON.parse(
      await readFile(path.join(repoDir, PLUGIN_MANIFEST_FILE), "utf8"),
    );
    expect(written.plugins[0].capabilities.auth).toBeUndefined();
    expect(written.plugins[0].capabilities.action.kinds[0].kind).toBe(
      "do_something",
    );
  });

  it("errors on conflict when a plugin appears in multiple trusted marketplaces", async () => {
    const plugin = {
      name: "@conflict/p",
      title: "x",
      description: "x",
      source: "https://github.com/x/y",
      versions: [actionVersion({ kinds: [{ kind: "k", description: "k" }] })],
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

  // ─── Install receipt: installSource + installedAt + policySnapshot ─────

  it("marketplace install records installSource='marketplace', installedAt, and policySnapshot", async () => {
    const plugin = {
      name: "@open-neko/plugin-good",
      title: "Good",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [actionVersion({ kinds: [{ kind: "x", description: "x" }] })],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    const before = Date.now();
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-good",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => "",
      policySnapshot: {
        allowUnverified: false,
        allowGitUrlInstalls: false,
        allowSandboxedSkillEscape: false,
        allowedMarketplaces: [OFFICIAL_MARKETPLACE_URL],
      },
    });
    const after = Date.now();
    expect(result.source).toBe("marketplace");
    expect(result.installedAt).toBeTruthy();
    const ts = Date.parse(result.installedAt);
    expect(ts).toBeGreaterThanOrEqual(before);
    expect(ts).toBeLessThanOrEqual(after);

    const written = JSON.parse(
      await readFile(path.join(repoDir, PLUGIN_MANIFEST_FILE), "utf8"),
    );
    const entry = written.plugins[0];
    expect(entry.installSource).toBe("marketplace");
    expect(entry.installedAt).toBe(result.installedAt);
    expect(entry.policySnapshot).toEqual({
      allowUnverified: false,
      allowGitUrlInstalls: false,
      allowSandboxedSkillEscape: false,
      allowedMarketplaces: [OFFICIAL_MARKETPLACE_URL],
    });
  });

  it("unverified install records installSource='unverified' on the manifest entry", async () => {
    // Stub the npm install + the package.json read by writing what the
    // post-install path expects to find under node_modules.
    const pkgRoot = path.join(repoDir, "node_modules", "@x", "y");
    await import("node:fs/promises").then((fs) =>
      fs.mkdir(pkgRoot, { recursive: true }),
    );
    await writeFile(
      path.join(pkgRoot, "package.json"),
      JSON.stringify({
        version: "0.2.0",
        _integrity: "sha512-localpnpmtest",
        openneko: {
          permissions: { network: ["acme.com"], env: [] },
          capabilities: { action: { kinds: [{ kind: "x", description: "x" }] } },
        },
      }),
      "utf8",
    );
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@x/y",
      unverified: true,
      trustedMarketplaces: [],
      secretsConfigDir: configDir,
      npmRunner: async () => {},
      envPrompt: async () => "",
      policySnapshot: {
        allowUnverified: true, // operator opted in
        allowGitUrlInstalls: false,
        allowSandboxedSkillEscape: false,
        allowedMarketplaces: [OFFICIAL_MARKETPLACE_URL],
      },
    });
    expect(result.source).toBe("unverified");
    const written = JSON.parse(
      await readFile(path.join(repoDir, PLUGIN_MANIFEST_FILE), "utf8"),
    );
    const entry = written.plugins[0];
    expect(entry.installSource).toBe("unverified");
    expect(entry.policySnapshot.allowUnverified).toBe(true);
  });

  it("copies a bundled skill folder under skillsInstallDir when openneko.skill is declared", async () => {
    // The CLI runs the npm install first, which leaves the package on
    // disk under node_modules/<name>/. Stub that by writing the
    // expected layout BEFORE runInstall and using a no-op npmRunner.
    const skillsDir = await import("node:fs/promises").then((fs) =>
      fs.mkdtemp(path.join(tmpdir(), "pi-skills-")),
    );
    const pkgRoot = path.join(repoDir, "node_modules", "@vendor", "connector-x");
    await import("node:fs/promises").then((fs) =>
      fs.mkdir(pkgRoot, { recursive: true }),
    );
    await writeFile(
      path.join(pkgRoot, "package.json"),
      JSON.stringify({
        version: "0.1.0",
        _integrity: "sha512-skillpkgtest",
        openneko: {
          runner: "./dist/run.js",
          skill: "./skill",
          permissions: { network: ["api.example.com"], env: [] },
          capabilities: {
            action: { kinds: [{ kind: "do_x", description: "x" }] },
          },
        },
      }),
      "utf8",
    );
    await import("node:fs/promises").then((fs) =>
      fs.mkdir(path.join(pkgRoot, "skill"), { recursive: true }),
    );
    await writeFile(
      path.join(pkgRoot, "skill", "SKILL.md"),
      `---\nname: vendor-x\ndescription: Operate against Vendor X.\n---\nbody`,
      "utf8",
    );

    const plugin = {
      name: "@vendor/connector-x",
      title: "Connector X",
      description: "...",
      source: "https://github.com/vendor/plugins",
      versions: [actionVersion({ kinds: [{ kind: "do_x", description: "x" }] })],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@vendor/connector-x",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {
        // No-op — the test already wrote the package's layout above.
      },
      envPrompt: async () => "",
      skillsInstallDir: skillsDir,
    });

    expect(result.skillInstalledAt).toBeTruthy();
    expect(result.skillInstalledAt).toBe(path.join(skillsDir, "vendor-x"));
    const skillCopy = path.join(skillsDir, "vendor-x", "SKILL.md");
    const body = await readFile(skillCopy, "utf8");
    expect(body).toContain("name: vendor-x");
  });

  it("does not error when the package declares no skill half", async () => {
    const plugin = {
      name: "@open-neko/plugin-plain",
      title: "Plain",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [actionVersion({ kinds: [{ kind: "x", description: "x" }] })],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-plain",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => "",
    });
    expect(result.skillInstalledAt).toBeUndefined();
  });

  it("install without policySnapshot records null (pre-feature compat)", async () => {
    const plugin = {
      name: "@open-neko/plugin-legacy",
      title: "Legacy",
      description: "...",
      source: "https://github.com/open-neko/plugins",
      versions: [actionVersion({ kinds: [{ kind: "x", description: "x" }] })],
    };
    const fixtures = new Map([
      [OFFICIAL_MARKETPLACE_URL, marketplaceWith([plugin])],
    ]);
    const result = await runInstall({
      repoRoot: repoDir,
      spec: "@open-neko/plugin-legacy",
      trustedMarketplaces: [officialMarketplace],
      secretsConfigDir: configDir,
      marketplaceClient: fakeClient(fixtures),
      npmRunner: async () => {},
      envPrompt: async () => "",
      // no policySnapshot
    });
    expect(result.installedAt).toBeTruthy();
    const written = JSON.parse(
      await readFile(path.join(repoDir, PLUGIN_MANIFEST_FILE), "utf8"),
    );
    expect(written.plugins[0].policySnapshot).toBeNull();
  });
});
