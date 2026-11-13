import { existsSync } from "node:fs";
import { rm } from "node:fs/promises";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { app_user, config_change, db, eq, pool } from "@neko/db";
import {
  createTestOrg,
  dbReachable,
  deleteTestOrg,
  uniqueOrgId,
} from "@neko/db/test-helpers";

vi.mock("../../src/embedding", async () => {
  const EMBEDDING_DIM = 384;
  return {
    EMBEDDING_DIM,
    embedText: vi.fn(async (text: string) => {
      const seed = text
        .split("")
        .reduce((acc, c) => (acc * 31 + c.charCodeAt(0)) >>> 0, 7);
      const v = new Array<number>(EMBEDDING_DIM);
      for (let i = 0; i < EMBEDDING_DIM; i++) v[i] = Math.sin(seed + i) * 0.1;
      return v;
    }),
    vectorLiteral: (vec: number[]) => `[${vec.join(",")}]`,
    embeddingModel: () => "test-model",
  };
});

import { saveWorkflow } from "../../src/workflows/store";
import { getOrgAgentRoot } from "../../src/work/workspace";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn("[workflow-config-change] skipping: Postgres unreachable.");
}

describeIfDb("workflow saves write audit rows", () => {
  const orgId = uniqueOrgId("wfaudit");
  let ada: string;
  let root: string;

  beforeAll(async () => {
    await createTestOrg(orgId);
    ada = `${orgId}-ada`;
    await db().insert(app_user).values({
      id: ada,
      email: "ada@example.com",
      org_id: orgId,
    });
    root = getOrgAgentRoot(orgId);
  });

  afterAll(async () => {
    await deleteTestOrg(orgId);
    await rm(root, { recursive: true, force: true }).catch(() => {});
    await pool().end();
    vi.unstubAllEnvs();
  });

  it("a personal and a team save each add one row and write no file", async () => {
    const steps = [{ id: "s1", description: "summarize" }];
    await saveWorkflow({ orgId, name: "Ada Weekly Digest", steps, goal: "digest", ownerUserId: ada });
    await saveWorkflow({ orgId, name: "Team Digest", steps, goal: "digest" });
    expect(existsSync(join(root, "workflows"))).toBe(false);
    expect(existsSync(join(root, ".git"))).toBe(false);
    const rows = await db()
      .select()
      .from(config_change)
      .where(eq(config_change.org_id, orgId));
    const summary = rows
      .map((r) => `${r.scope}|${r.user_id}|${r.actor_user_id ?? "-"}|${r.summary}|${r.commit_sha ?? "-"}`)
      .sort();
    expect(summary).toEqual([
      "team||-|Added workflow: Team Digest|-",
      `user|${ada}|${ada}|Added workflow: Ada Weekly Digest|-`,
    ]);
  });
});
