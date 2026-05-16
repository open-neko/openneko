import { describe, expect, it, vi } from "vitest";
import {
  applyMigration,
  shouldRunInTransaction,
} from "../src/migrate-helpers.mjs";

describe("shouldRunInTransaction", () => {
  it("returns true for ordinary migration filenames", () => {
    expect(shouldRunInTransaction("0001_init.sql")).toBe(true);
    expect(shouldRunInTransaction("0014_work_memory_embedding.sql")).toBe(true);
  });

  it("returns false for files ending with `_no_tx.sql`", () => {
    expect(
      shouldRunInTransaction("0016_reindex_after_pgvector_swap_no_tx.sql"),
    ).toBe(false);
    expect(shouldRunInTransaction("anything_no_tx.sql")).toBe(false);
  });

  it("is case-sensitive on the suffix (matching filesystem semantics)", () => {
    // _NO_TX.sql is NOT a recognized opt-out — only the lowercase form.
    expect(shouldRunInTransaction("file_NO_TX.sql")).toBe(true);
  });

  it("falls back to true when given garbage input", () => {
    expect(shouldRunInTransaction("")).toBe(true);
    // @ts-expect-error — defensive runtime check
    expect(shouldRunInTransaction(undefined)).toBe(true);
    // @ts-expect-error — defensive runtime check
    expect(shouldRunInTransaction(null)).toBe(true);
  });
});

describe("applyMigration", () => {
  it("wraps regular migrations in BEGIN/COMMIT and stamps schema_migrations", async () => {
    const calls: string[] = [];
    const client = {
      query: vi.fn(async (sqlOrCmd: string, _args?: unknown[]) => {
        calls.push(sqlOrCmd);
        return { rows: [] };
      }),
    };
    await applyMigration(client, "0001_init.sql", "CREATE TABLE t (id int);");
    expect(calls).toEqual([
      "BEGIN",
      "CREATE TABLE t (id int);",
      "INSERT INTO schema_migrations(name) VALUES($1)",
      "COMMIT",
    ]);
  });

  it("rolls back the transaction when the migration SQL throws", async () => {
    const calls: string[] = [];
    const failingErr = new Error("syntax error");
    const client = {
      query: vi.fn(async (sqlOrCmd: string, _args?: unknown[]) => {
        calls.push(sqlOrCmd);
        if (sqlOrCmd === "BAD SQL") throw failingErr;
        return { rows: [] };
      }),
    };
    await expect(
      applyMigration(client, "0002_bad.sql", "BAD SQL"),
    ).rejects.toBe(failingErr);
    expect(calls).toEqual(["BEGIN", "BAD SQL", "ROLLBACK"]);
  });

  it("does NOT wrap _no_tx.sql migrations and still stamps schema_migrations", async () => {
    const calls: string[] = [];
    const client = {
      query: vi.fn(async (sqlOrCmd: string, _args?: unknown[]) => {
        calls.push(sqlOrCmd);
        return { rows: [] };
      }),
    };
    await applyMigration(
      client,
      "0016_reindex_after_pgvector_swap_no_tx.sql",
      "REINDEX DATABASE neko;",
    );
    expect(calls).toEqual([
      "REINDEX DATABASE neko;",
      "INSERT INTO schema_migrations(name) VALUES($1)",
    ]);
    expect(calls).not.toContain("BEGIN");
    expect(calls).not.toContain("COMMIT");
    expect(calls).not.toContain("ROLLBACK");
  });

  it("rethrows on _no_tx.sql failures without attempting a ROLLBACK", async () => {
    const calls: string[] = [];
    const failingErr = new Error("REINDEX failed");
    const client = {
      query: vi.fn(async (sqlOrCmd: string) => {
        calls.push(sqlOrCmd);
        throw failingErr;
      }),
    };
    await expect(
      applyMigration(client, "9999_thing_no_tx.sql", "REINDEX DATABASE x;"),
    ).rejects.toBe(failingErr);
    expect(calls).toEqual(["REINDEX DATABASE x;"]);
  });

  it("passes the filename as the bind value when stamping schema_migrations", async () => {
    const bindCalls: unknown[][] = [];
    const client = {
      query: vi.fn(async (sqlOrCmd: string, args?: unknown[]) => {
        if (args) bindCalls.push(args);
        return { rows: [] };
      }),
    };
    await applyMigration(client, "0099_special.sql", "SELECT 1;");
    expect(bindCalls).toEqual([["0099_special.sql"]]);
  });
});
