/**
 * pg-boss start against a real Postgres. The worker starts it only to
 * remove the old queues at boot.
 *
 * Skips when the metadata DB isn't reachable.
 */

import { afterAll, describe, expect, it } from "vitest";
import { boss, stopBoss } from "../../src/jobs";
import { pool } from "../../src";

async function dbReachable(): Promise<boolean> {
  try {
    await pool().query("select 1");
    return true;
  } catch {
    return false;
  }
}

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

if (!reachable) {
  console.warn(
    "[pg-boss-flow] skipping: metadata Postgres unreachable. Run `docker compose up -d` to enable.",
  );
}

describeIfDb("pg-boss start", () => {
  afterAll(async () => {
    await stopBoss();
    await pool().end();
  });

  it("starts the boss singleton on first call", async () => {
    const a = await boss();
    const b = await boss();
    expect(a).toBe(b);
  });
});
