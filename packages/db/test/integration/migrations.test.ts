/**
 * Migration tests against a real Postgres.
 *
 * Each test creates a fresh, empty database (via the admin connection),
 * applies the schema migrations against it, asserts behaviour, then drops it.
 * A temp database — not just a schema — is the only way the migration's
 * global pg_constraint guard runs in isolation.
 */

import { readFile } from "node:fs/promises";
import { join } from "node:path";
import pg from "pg";
import { afterAll, describe, expect, it } from "vitest";
import { dbReachable } from "./_helpers";
import { buildPoolConfig } from "../../src/connection";
import { pool } from "../../src";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[migrations] skipping: Postgres unreachable.");
}

const REPO_ROOT = join(__dirname, "..", "..", "..", "..");
const M_0001 = join(REPO_ROOT, "db", "migrations", "0001_init.sql");
const M_0002 = join(REPO_ROOT, "db", "migrations", "0002_agent_backend_and_setup.sql");
const M_0003 = join(REPO_ROOT, "db", "migrations", "0003_rename_claude_sdk_to_claude_agent.sql");
const M_0004 = join(REPO_ROOT, "db", "migrations", "0004_drop_organization_plan.sql");
const M_0005 = join(REPO_ROOT, "db", "migrations", "0005_metric_refresh_status.sql");
const M_0006 = join(REPO_ROOT, "db", "migrations", "0006_work_runtime.sql");
const M_0007 = join(REPO_ROOT, "db", "migrations", "0007_work_memory.sql");

function uniqueDbName(): string {
  return `vitest_migrations_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 6)}`;
}


async function withTempDb<T>(
  fn: (client: pg.Client) => Promise<T>,
): Promise<T> {
  const adminClient = await pool().connect();
  const dbName = uniqueDbName();
  try {
    await adminClient.query(`create database ${dbName}`);
  } finally {
    adminClient.release();
  }

  const tempClient = new pg.Client(buildPoolConfig({ database: dbName }));
  await tempClient.connect();
  try {
    return await fn(tempClient);
  } finally {
    await tempClient.end();
    const cleanup = await pool().connect();
    try {
      await cleanup.query(`drop database if exists ${dbName} with (force)`);
    } finally {
      cleanup.release();
    }
  }
}

async function applyFile(client: pg.Client, path: string) {
  const sql = await readFile(path, "utf8");
  await client.query(sql);
}

async function applyAll(client: pg.Client) {
  for (const path of [M_0001, M_0002, M_0003, M_0004, M_0005, M_0006, M_0007]) {
    await applyFile(client, path);
  }
}

describeIfDb("schema migrations", () => {
  afterAll(async () => {
    await pool().end();
  });

  it("0001 creates organization + every expected sibling", async () => {
    await withTempDb(async (client) => {
      await applyFile(client, M_0001);
      const tables = await client.query<{ table_name: string }>(
        `select table_name from information_schema.tables
         where table_schema = 'public' order by table_name`,
      );
      const names = tables.rows.map((r) => r.table_name);
      for (const expected of [
        "organization",
        "data_source",
        "onboarding_wizard",
        "processing_job",
        "metric",
        "metric_snapshot",
        "llm_provider_config",
      ]) {
        expect(names, `missing ${expected}`).toContain(expected);
      }
    });
  });

  it("0002 adds setup_complete_at + scope check constraint", async () => {
    await withTempDb(async (client) => {
      await applyFile(client, M_0001);
      await applyFile(client, M_0002);

      const cols = await client.query<{ column_name: string }>(
        `select column_name from information_schema.columns
         where table_schema = 'public' and table_name = 'organization'`,
      );
      expect(cols.rows.map((r) => r.column_name)).toContain("setup_complete_at");

      const constraints = await client.query<{ conname: string }>(
        `select con.conname
         from pg_constraint con
         join pg_class t on t.oid = con.conrelid
         join pg_namespace n on n.oid = t.relnamespace
         where n.nspname = 'public'
           and t.relname = 'llm_provider_config'`,
      );
      expect(constraints.rows.map((r) => r.conname)).toContain(
        "llm_provider_config_scope_check",
      );
    });
  });

  it("0002 is idempotent (re-applying succeeds without error)", async () => {
    await withTempDb(async (client) => {
      await applyFile(client, M_0001);
      await applyFile(client, M_0002);
      await applyFile(client, M_0002); // second apply must succeed
    });
  });

  it("0002 backfills setup_complete_at for orgs with data + enabled primary", async () => {
    await withTempDb(async (client) => {
      await applyFile(client, M_0001);

      // Seed prerequisites BEFORE 0002 runs.
      await client.query(`
        insert into organization (id, name) values ('test-backfill', 'Backfill Test');
        insert into data_source (org_id, kind, graphql_url) values ('test-backfill', 'graphjin', 'http://example.com/graphql');
        insert into llm_provider_config (org_id, scope, provider, enabled) values ('test-backfill', 'primary', 'anthropic', true);
      `);

      await applyFile(client, M_0002);

      const result = await client.query<{ setup_complete_at: Date | null }>(
        `select setup_complete_at from organization where id = 'test-backfill'`,
      );
      expect(result.rows[0].setup_complete_at).not.toBeNull();
    });
  });

  it("0002 does NOT backfill orgs without prerequisites", async () => {
    await withTempDb(async (client) => {
      await applyFile(client, M_0001);
      await client.query(`insert into organization (id, name) values ('no-prereqs', 'No prereqs')`);
      await applyFile(client, M_0002);
      const result = await client.query<{ setup_complete_at: Date | null }>(
        `select setup_complete_at from organization where id = 'no-prereqs'`,
      );
      expect(result.rows[0].setup_complete_at).toBeNull();
    });
  });

  it("scope check constraint rejects unknown values", async () => {
    await withTempDb(async (client) => {
      await applyFile(client, M_0001);
      await applyFile(client, M_0002);

      await client.query(`insert into organization (id, name) values ('chk', 'Check Test')`);
      await expect(
        client.query(
          `insert into llm_provider_config (org_id, scope, provider) values ('chk', 'bogus-scope', 'anthropic')`,
        ),
      ).rejects.toThrow(/check constraint|scope/i);
    });
  });

  it("scope check accepts the three known scopes", async () => {
    await withTempDb(async (client) => {
      await applyFile(client, M_0001);
      await applyFile(client, M_0002);

      await client.query(`insert into organization (id, name) values ('chk2', 'Check Test 2')`);
      for (const scope of ["primary", "research", "agent"]) {
        await client.query(
          `insert into llm_provider_config (org_id, scope, provider) values ($1, $2, 'anthropic')`,
          ["chk2", scope],
        );
      }
    });
  });

  it("0006 creates the work runtime tables", async () => {
    await withTempDb(async (client) => {
      await applyAll(client);
      const tables = await client.query<{ table_name: string }>(
        `select table_name from information_schema.tables
         where table_schema = 'public' order by table_name`,
      );
      const names = tables.rows.map((row) => row.table_name);
      for (const expected of [
        "work_thread",
        "work_run",
        "work_message",
        "work_run_event",
      ]) {
        expect(names, `missing ${expected}`).toContain(expected);
      }
    });
  });

  it("0006 enforces one sequence slot per run event", async () => {
    await withTempDb(async (client) => {
      await applyAll(client);
      await client.query(`
        insert into organization (id, name) values ('work-org', 'Work Org');
        insert into work_thread (id, org_id, title) values ('00000000-0000-0000-0000-000000000001', 'work-org', 'Thread');
        insert into work_run (id, org_id, thread_id, backend, status)
        values ('00000000-0000-0000-0000-000000000002', 'work-org', '00000000-0000-0000-0000-000000000001', 'hermes', 'running');
        insert into work_run_event (org_id, thread_id, run_id, seq, kind, payload)
        values ('work-org', '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', 1, 'status', '{"type":"status","message":"ok"}');
      `);
      await expect(
        client.query(`
          insert into work_run_event (org_id, thread_id, run_id, seq, kind, payload)
          values ('work-org', '00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', 1, 'status', '{"type":"status","message":"dup"}')
        `),
      ).rejects.toThrow(/unique/i);
    });
  });

  it("0007 creates work memory tables", async () => {
    await withTempDb(async (client) => {
      await applyAll(client);
      const tables = await client.query<{ table_name: string }>(
        `select table_name from information_schema.tables
         where table_schema = 'public' order by table_name`,
      );
      const names = tables.rows.map((row) => row.table_name);
      for (const expected of [
        "work_memory",
        "work_memory_event",
        "work_pending_memory",
      ]) {
        expect(names, `missing ${expected}`).toContain(expected);
      }
    });
  });
});
