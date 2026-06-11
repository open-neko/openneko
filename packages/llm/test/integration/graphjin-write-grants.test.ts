// GJ5 — write grants come only from an enabled auto_approve policy for
// kind "graphjin_write", and only an ADMIN actor can hold them.

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";
import { pool } from "@neko/db";
import { createActionPolicy } from "../../src/workflows/action-store";
import { resolveGraphjinWriteGrants } from "../../src/work/graphjin-actor-guard";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[graphjin-write-grants] skipping: Postgres unreachable.");
}

describeIfDb("resolveGraphjinWriteGrants (GJ5)", () => {
  const orgId = uniqueOrgId("gjgrant");

  beforeAll(async () => {
    await createTestOrg(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await pool().end();
  });

  it("no policy → no grants, even for admins", async () => {
    expect(
      await resolveGraphjinWriteGrants(orgId, { userId: "u", role: "admin" }),
    ).toEqual([]);
  });

  it("an auto_approve graphjin_write policy grants its listed subcommands to admins only", async () => {
    await createActionPolicy({
      orgId,
      name: "graphjin_write_admin",
      description: "admins may save queries",
      appliesToKinds: ["graphjin_write"],
      appliesToScopes: ["internal"],
      mode: "auto_approve",
      riskThresholdAutoApprove: null,
      allowedTargets: { patterns: ["write_query", "save_workflow", "serve"] },
      deniedTargets: null,
      limits: {},
      approverRole: null,
      priority: 100,
      enabled: true,
    });
    const grants = await resolveGraphjinWriteGrants(orgId, {
      userId: "u",
      role: "admin",
    });
    // "serve" is not a write subcommand — ignored, never honored.
    expect(grants.sort()).toEqual(["save_workflow", "write_query"]);

    expect(
      await resolveGraphjinWriteGrants(orgId, { userId: "u", role: "member" }),
    ).toEqual([]);
    expect(
      await resolveGraphjinWriteGrants(orgId, { userId: null, role: "service" }),
    ).toEqual([]);
  });
});
