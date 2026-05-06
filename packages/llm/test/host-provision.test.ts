/**
 * provisionHostConfig integration test against a real Postgres + temp HOME.
 *
 * Asserts the function reads from llm_provider_config + data_source and
 * writes the right host config files (graphjin client.json + hermes
 * config.yaml + .env), including provider-name mapping (gemini, custom)
 * and secret decryption.
 */

import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { tmpdir, platform } from "node:os";
import { join } from "node:path";
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
} from "vitest";
import {
  clearProvider,
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  seedDataSource,
  seedProvider,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { db, eq, data_source, pool } from "@neko/db";
import { provisionHostConfig } from "../src/host-provision";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[host-provision] skipping: metadata Postgres unreachable. Run `docker compose up -d`.",
  );
}

function graphjinPath(home: string): string {
  if (platform() === "darwin") {
    return join(home, "Library", "Application Support", "graphjin", "client.json");
  }
  return join(home, ".config", "graphjin", "client.json");
}

const ORIGINAL_HOME = process.env.HOME;
const ORIGINAL_HERMES_HOME = process.env.HERMES_HOME;

describeIfDb("provisionHostConfig", () => {
  let orgId: string;
  let tempHome: string;
  let hermesHome: string;

  beforeAll(async () => {
    orgId = uniqueOrgId("provision");
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
  });

  beforeEach(async () => {
    tempHome = await mkdtemp(join(tmpdir(), "neko-test-home-"));
    hermesHome = join(tempHome, ".hermes");
    process.env.HOME = tempHome;
    process.env.HERMES_HOME = hermesHome;
  });

  afterEach(async () => {
    await rm(tempHome, { recursive: true, force: true });
    if (ORIGINAL_HOME) process.env.HOME = ORIGINAL_HOME;
    if (ORIGINAL_HERMES_HOME) process.env.HERMES_HOME = ORIGINAL_HERMES_HOME;
    else delete process.env.HERMES_HOME;
    await clearProvider(orgId, "agent");
    await clearProvider(orgId, "primary");
    // Reset data_source between tests.
    await db().delete(data_source).where(eq(data_source.org_id, orgId));
  });

  it("writes graphjin client.json with the server base derived from graphql_url", async () => {
    await seedDataSource(orgId, {
      graphqlUrl: "http://localhost:8080/api/v1/graphql",
    });
    await provisionHostConfig(orgId);

    const path = graphjinPath(tempHome);
    const content = JSON.parse(await readFile(path, "utf8"));
    expect(content.server).toBe("http://localhost:8080");
    expect(content.token).toBe("");
    expect(content.expires_at).toBe("0001-01-01T00:00:00Z");
  });

  it("falls back to URL origin when graphql path doesn't match the conventional suffix", async () => {
    await seedDataSource(orgId, {
      graphqlUrl: "https://api.example.com/custom-path",
    });
    await provisionHostConfig(orgId);

    const path = graphjinPath(tempHome);
    const content = JSON.parse(await readFile(path, "utf8"));
    expect(content.server).toBe("https://api.example.com");
  });

  it("writes hermes config.yaml + .env when backend=hermes", async () => {
    await seedDataSource(orgId);
    await seedProvider(orgId, {
      scope: "agent",
      provider: "hermes",
      config: { backend: "hermes" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "google-gemini",
      model: "gemini-pro-latest",
      secrets: { apiKey: "test-gemini-key" },
    });

    await provisionHostConfig(orgId);

    const yaml = await readFile(join(hermesHome, "config.yaml"), "utf8");
    expect(yaml).toContain('default: "gemini-pro-latest"');
    // Neko 'google-gemini' → Hermes 'gemini' provider name
    expect(yaml).toContain('provider: "gemini"');
    expect(yaml).toContain("max_turns:");

    const env = await readFile(join(hermesHome, ".env"), "utf8");
    expect(env).toContain("GEMINI_API_KEY=test-gemini-key");

    const envStat = await stat(join(hermesHome, ".env"));
    // Mode 0600 = owner read/write only (octal 0o600 = decimal 384).
    // Mask off file-type bits.
    expect(envStat.mode & 0o777).toBe(0o600);
  });

  it("writes anthropic key var for anthropic provider", async () => {
    await seedDataSource(orgId);
    await seedProvider(orgId, {
      scope: "agent",
      provider: "hermes",
      config: { backend: "hermes" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "claude-opus-4-7",
      secrets: { apiKey: "sk-ant-test" },
    });

    await provisionHostConfig(orgId);

    const yaml = await readFile(join(hermesHome, "config.yaml"), "utf8");
    expect(yaml).toContain('provider: "anthropic"');
    const env = await readFile(join(hermesHome, ".env"), "utf8");
    expect(env).toContain("ANTHROPIC_API_KEY=sk-ant-test");
  });

  it("skips hermes writes when backend=claude-agent (only graphjin written)", async () => {
    await seedDataSource(orgId);
    await seedProvider(orgId, {
      scope: "agent",
      provider: "claude-agent",
      config: { backend: "claude-agent" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "anthropic",
      model: "claude-opus-4-7",
      secrets: { apiKey: "sk-ant-claude-agent" },
    });

    await provisionHostConfig(orgId);

    // graphjin client.json IS written (both backends shell out to it).
    const gj = await readFile(graphjinPath(tempHome), "utf8");
    expect(gj).toContain("server");

    // hermes config.yaml and .env are NOT written.
    await expect(readFile(join(hermesHome, "config.yaml"), "utf8")).rejects.toThrow();
    await expect(readFile(join(hermesHome, ".env"), "utf8")).rejects.toThrow();
  });

  it("does not throw when primary row is missing (best-effort)", async () => {
    await seedDataSource(orgId);
    await seedProvider(orgId, {
      scope: "agent",
      provider: "hermes",
      config: { backend: "hermes" },
    });
    // No primary row.
    await expect(provisionHostConfig(orgId)).resolves.toBeUndefined();
    // graphjin still written.
    await readFile(graphjinPath(tempHome), "utf8");
  });

  it("does not throw when no data source exists (graphjin write skipped)", async () => {
    await expect(provisionHostConfig(orgId)).resolves.toBeUndefined();
  });

  it("writes empty .env when key is missing but provider row exists", async () => {
    await seedDataSource(orgId);
    await seedProvider(orgId, {
      scope: "agent",
      provider: "hermes",
      config: { backend: "hermes" },
    });
    await seedProvider(orgId, {
      scope: "primary",
      provider: "google-gemini",
      model: "gemini-pro-latest",
      secrets: {},
    });

    await provisionHostConfig(orgId);

    const env = await readFile(join(hermesHome, ".env"), "utf8");
    expect(env).toBe("");
  });
});

if (reachable) {
  afterAll(async () => {
    await pool().end();
  });
}
