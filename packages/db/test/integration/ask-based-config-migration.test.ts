import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import pg from "pg";
import { afterAll, describe, expect, it } from "vitest";
import { buildPoolConfig } from "../../src/connection";
import { graphjinConfigSettingsFromRow, pool } from "../../src";
import { dbReachable } from "./_helpers";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

const MIGRATIONS = join(__dirname, "..", "..", "..", "..", "db", "migrations");
const TARGET = "0130_ask_based_config_default.sql";

async function withTempDb<T>(fn: (client: pg.Client) => Promise<T>): Promise<T> {
  const name = `vitest_askcfg_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 6)}`;
  const admin = await pool().connect();
  try {
    await admin.query(`create database ${name}`);
  } finally {
    admin.release();
  }
  const client = new pg.Client(buildPoolConfig({ database: name }));
  await client.connect();
  try {
    return await fn(client);
  } finally {
    await client.end();
    const cleanup = await pool().connect();
    try {
      await cleanup.query(`drop database if exists ${name}`);
    } finally {
      cleanup.release();
    }
  }
}

async function applyMigrations(client: pg.Client, filter: (file: string) => boolean) {
  const files = (await readdir(MIGRATIONS)).filter((f) => f.endsWith(".sql") && filter(f)).sort();
  for (const file of files) {
    await client.query(await readFile(join(MIGRATIONS, file), "utf8"));
  }
}

async function askBasedConfig(client: pg.Client, orgId: string): Promise<boolean> {
  const { rows } = await client.query<{ enabled: boolean; config: Record<string, unknown> | null }>(
    "select enabled, config from llm_provider_config where org_id = $1 and scope = 'graphjin-config'",
    [orgId],
  );
  return graphjinConfigSettingsFromRow(rows[0]).sourceConfigEnabled;
}

describeIfDb("0130 Ask-based config default", () => {
  afterAll(async () => {
    await pool().end();
  });

  it("is on for an organization made on a new install", async () => {
    await withTempDb(async (client) => {
      await applyMigrations(client, () => true);
      await client.query("insert into organization (id, name) values ('first', 'First')");
      expect(await askBasedConfig(client, "first")).toBe(true);
    });
  });

  it("keeps the value each organization had before the upgrade", async () => {
    await withTempDb(async (client) => {
      await applyMigrations(client, (f) => f < TARGET);
      await client.query(`
        insert into organization (id, name) values ('never-set', 'A'), ('turned-on', 'B'), ('turned-off', 'C');
        insert into llm_provider_config (org_id, scope, provider, enabled, config) values
          ('turned-on', 'graphjin-config', 'graphjin-config', true, '{"sourceConfigEnabled": true}'),
          ('turned-off', 'graphjin-config', 'graphjin-config', false, '{"sourceConfigEnabled": false}');
      `);
      const before = {
        "never-set": false,
        "turned-on": await askBasedConfig(client, "turned-on"),
        "turned-off": await askBasedConfig(client, "turned-off"),
      };
      expect(before).toEqual({ "never-set": false, "turned-on": true, "turned-off": false });

      await applyMigrations(client, (f) => f === TARGET);
      await applyMigrations(client, (f) => f === TARGET);

      for (const [orgId, value] of Object.entries(before)) {
        expect(await askBasedConfig(client, orgId)).toBe(value);
      }
      const { rows } = await client.query("select count(*)::int as n from llm_provider_config where scope = 'graphjin-config'");
      expect(rows[0].n).toBe(3);
    });
  });
});
