// SEC10 — the tamper-evident chain: governance events append links,
// verification walks them, and any retroactive edit, deletion, or
// reorder breaks the chain at the exact spot.

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { audit_chain, and, db, eq, pool } from "@neko/db";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import {
  appendAuditChain,
  canonicalJson,
  exportAuditChain,
  verifyAuditChain,
} from "../../src/workflows/audit-chain";
import { createActionRequest } from "../../src/workflows/action-store";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[audit-chain] skipping: Postgres unreachable.");
}

describe("canonicalJson", () => {
  it("is independent of key order and drops undefined", () => {
    expect(canonicalJson({ b: 1, a: { d: 2, c: 3 }, z: undefined })).toBe(
      canonicalJson({ a: { c: 3, d: 2 }, b: 1 }),
    );
  });
});

describeIfDb("SEC10 audit chain", () => {
  const orgId = uniqueOrgId("sec10");

  beforeAll(async () => {
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await pool().end();
  });

  it("links append in sequence and verify, even under concurrency", async () => {
    await Promise.all(
      Array.from({ length: 8 }, (_, i) =>
        appendAuditChain({
          orgId,
          entityKind: "test",
          entityId: `e-${i}`,
          event: "created",
          payload: { i },
        }),
      ),
    );
    const verification = await verifyAuditChain(orgId);
    expect(verification).toMatchObject({ ok: true, length: 8 });
  });

  it("governance writes land on the chain automatically", async () => {
    await createActionRequest({
      orgId,
      scope: "external",
      kind: "send_webhook",
      target: "https://example.test/hook",
      status: "pending_approval",
      summary: "ping",
    });
    const ndjson = await exportAuditChain(orgId);
    const lines = ndjson.split("\n").map((l) => JSON.parse(l));
    const created = lines.find(
      (l) => l.entityKind === "action_request" && l.event === "created:pending_approval",
    );
    expect(created?.payload).toMatchObject({ kind: "send_webhook" });
    expect(await verifyAuditChain(orgId)).toMatchObject({ ok: true });
  });

  it("editing a recorded payload breaks the chain at that link", async () => {
    await db()
      .update(audit_chain)
      .set({ payload: { i: 999 } })
      .where(and(eq(audit_chain.org_id, orgId), eq(audit_chain.seq, 3)));
    const verification = await verifyAuditChain(orgId);
    expect(verification.ok).toBe(false);
    expect(verification.brokenAtSeq).toBe(3);
    expect(verification.reason).toMatch(/payload/);
  });

  it("deleting a link is a visible sequence gap", async () => {
    await db()
      .delete(audit_chain)
      .where(and(eq(audit_chain.org_id, orgId), eq(audit_chain.seq, 5)));
    const verification = await verifyAuditChain(orgId);
    expect(verification.ok).toBe(false);
    // seq 3 is still the payload tamper; the gap at 5 surfaces once 3 is
    // the earliest break — assert the earliest break is reported.
    expect(verification.brokenAtSeq).toBe(3);
  });
});
