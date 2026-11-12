import { afterAll, describe, expect, it } from "vitest";
import { enqueueActionExecute } from "../../src/jobs";
import { pool } from "../../src";

const reachable = await pool().query("select 1").then(() => true, () => false);
const describeIfDb = reachable ? describe : describe.skip;

describeIfDb("enqueueActionExecute", () => {
  const ids: string[] = [];
  afterAll(async () => {
    if (ids.length) await pool().query("delete from river.river_job where id = any($1::bigint[])", [ids]);
    await pool().end();
  });

  it("writes a River row that the Go action_execute worker reads", async () => {
    const id = await enqueueActionExecute({ orgId: "org", actionRequestId: "ar-1", extra: "dropped" } as never);
    ids.push(id);
    const { rows } = await pool().query(
      "select kind, args, max_attempts, queue, state::text, attempt, priority from river.river_job where id = $1",
      [id],
    );
    expect(rows).toEqual([{
      kind: "action_execute",
      args: { orgId: "org", actionRequestId: "ar-1" },
      max_attempts: 3,
      queue: "default",
      state: "available",
      attempt: 0,
      priority: 1,
    }]);
  });
});
