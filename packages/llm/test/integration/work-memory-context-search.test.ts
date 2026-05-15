// Integration tests for the pgvector-backed memory functions.
//
// We mock embedText so the suite doesn't pull a 22MB model in CI, but
// hit a real Postgres so Drizzle column types and the SQL shape are
// actually exercised — that's the bug class that bit us twice in a row
// (rows.map / row.created_at.toISOString) on the first wiring.

import { afterAll, beforeEach, describe, expect, it, vi } from "vitest";
import { pool, db, eq, work_memory } from "@neko/db";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";

// Mock the embedding service. The mock returns a deterministic 384-dim
// unit-ish vector so we can stuff multiple rows with controllable
// "distances" without loading the real model.
vi.mock("../../src/embedding", async () => {
  const EMBEDDING_DIM = 384;
  return {
    EMBEDDING_DIM,
    embedText: vi.fn(async (text: string) => {
      // Deterministic per-text vector: hash the text to a small set of
      // anchor vectors so similarity comparisons are predictable.
      const seed = text
        .split("")
        .reduce((acc, c) => (acc * 31 + c.charCodeAt(0)) >>> 0, 7);
      const v = new Array<number>(EMBEDDING_DIM);
      for (let i = 0; i < EMBEDDING_DIM; i++) {
        v[i] = Math.sin(seed + i) * 0.1;
      }
      return v;
    }),
    vectorLiteral: (vec: number[]) => `[${vec.join(",")}]`,
  };
});

import {
  formatWorkMemoryPromptContext,
  rememberWorkMemory,
  searchWorkMemoryByContext,
} from "../../src/work/memory";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[work-memory-context-search] skipping: Postgres unreachable.");
}

describeIfDb("work memory context search (pgvector)", () => {
  afterAll(async () => {
    await pool().end();
  });

  let orgId: string;

  beforeEach(async () => {
    orgId = uniqueOrgId("wm-ctx");
    await createTestOrg(orgId, "WM Ctx");
  });

  it("returns [] when no memories exist for the org", async () => {
    try {
      const results = await searchWorkMemoryByContext({
        orgId,
        query: "anything goes here",
        limit: 5,
      });
      expect(results).toEqual([]);
    } finally {
      await deleteTestOrg(orgId);
    }
  });

  it("returns [] when query is empty / whitespace-only", async () => {
    try {
      await rememberWorkMemory({
        orgId,
        text: "Some saved rule",
        kind: "business_rule",
        scope: "global",
      });
      expect(
        await searchWorkMemoryByContext({ orgId, query: "" }),
      ).toEqual([]);
      expect(
        await searchWorkMemoryByContext({ orgId, query: "   \n\t" }),
      ).toEqual([]);
    } finally {
      await deleteTestOrg(orgId);
    }
  });

  it("populates the embedding column on insert and finds the row by context", async () => {
    try {
      const memory = await rememberWorkMemory({
        orgId,
        text: "Always cite the table name in your reasoning",
        kind: "business_rule",
        scope: "global",
      });

      // Verify the embedding column got populated as a non-null pgvector.
      const stored = await db()
        .select({
          id: work_memory.id,
          embedding: work_memory.embedding,
        })
        .from(work_memory)
        .where(eq(work_memory.id, memory.id))
        .limit(1);
      expect(stored[0]?.embedding).toBeTruthy();

      const results = await searchWorkMemoryByContext({
        orgId,
        query: "what should I include in my reasoning",
        limit: 5,
      });

      expect(results).toHaveLength(1);
      expect(results[0].memory.id).toBe(memory.id);
      expect(results[0].memory.text).toBe(
        "Always cite the table name in your reasoning",
      );
      // Scalar score returned, finite number.
      expect(typeof results[0].score).toBe("number");
      expect(Number.isFinite(results[0].score)).toBe(true);
    } finally {
      await deleteTestOrg(orgId);
    }
  });

  it("returns Date objects for timestamp columns (regression: row.created_at.toISOString)", async () => {
    try {
      const memory = await rememberWorkMemory({
        orgId,
        text: "Date columns must be Date instances after search",
        kind: "business_rule",
        scope: "global",
      });

      const results = await searchWorkMemoryByContext({
        orgId,
        query: "date column shape check",
        limit: 5,
      });

      expect(results).toHaveLength(1);
      const m = results[0].memory;
      // The memory shape stringifies dates to ISO; this proves the SQL
      // path returned proper Date objects (the Drizzle column-mode path
      // would crash with .toISOString is not a function on raw strings).
      expect(typeof m.createdAt).toBe("string");
      expect(() => new Date(m.createdAt).toISOString()).not.toThrow();
      expect(typeof m.updatedAt).toBe("string");
      expect(memory.id).toBe(m.id);
    } finally {
      await deleteTestOrg(orgId);
    }
  });

  it("never returns memories from another org (org isolation)", async () => {
    const otherOrg = uniqueOrgId("wm-ctx-other");
    await createTestOrg(otherOrg, "Other org");
    try {
      await rememberWorkMemory({
        orgId: otherOrg,
        text: "Secret rule that must not leak",
        kind: "business_rule",
        scope: "global",
      });
      const results = await searchWorkMemoryByContext({
        orgId,
        query: "secret rule",
        limit: 5,
      });
      expect(results).toEqual([]);
    } finally {
      await deleteTestOrg(otherOrg);
      await deleteTestOrg(orgId);
    }
  });

  it("excludes archived memories from search results", async () => {
    try {
      // Insert two memories, then archive one directly via SQL update.
      const live = await rememberWorkMemory({
        orgId,
        text: "Live memory still available",
        kind: "business_rule",
        scope: "global",
      });
      const archived = await rememberWorkMemory({
        orgId,
        text: "Archived memory should not appear",
        kind: "business_rule",
        scope: "global",
      });
      await db()
        .update(work_memory)
        .set({ archived_at: new Date() })
        .where(eq(work_memory.id, archived.id));

      const results = await searchWorkMemoryByContext({
        orgId,
        query: "memory results",
        limit: 5,
      });
      expect(results.map((r) => r.memory.id)).toEqual([live.id]);
    } finally {
      await deleteTestOrg(orgId);
    }
  });

  it("formatWorkMemoryPromptContext merges context-relevant + core memories without duplication", async () => {
    try {
      const m = await rememberWorkMemory({
        orgId,
        text: "Treat this as durable context",
        kind: "business_rule",
        scope: "global",
        pinned: true,
      });
      const out = await formatWorkMemoryPromptContext(
        { orgId },
        { contextQuery: "find me a context-relevant memory", contextLimit: 5 },
      );
      // Memory text should appear exactly once in the merged section.
      const matches = out.split(m.text).length - 1;
      expect(matches).toBe(1);
      expect(out).toContain("durable context");
    } finally {
      await deleteTestOrg(orgId);
    }
  });
});
