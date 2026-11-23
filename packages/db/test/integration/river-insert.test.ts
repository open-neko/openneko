import { afterAll, describe, expect, it } from "vitest";
import { enqueueActionExecute, enqueueWorkflowRunFire } from "../../src/jobs";
import { pool } from "../../src";

const reachable = await pool().query("select 1").then(() => true, () => false);
const describeIfDb = reachable ? describe : describe.skip;

afterAll(async () => {
  await pool().end();
});

describeIfDb("enqueueActionExecute", () => {
  const ids: string[] = [];
  afterAll(async () => {
    if (ids.length) await pool().query("delete from river.river_job where id = any($1::bigint[])", [ids]);
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

describeIfDb("enqueueWorkflowRunFire", () => {
  const ids: string[] = [];
  afterAll(async () => {
    if (ids.length) await pool().query("delete from river.river_job where id = any($1::bigint[])", [ids]);
  });

  it("writes a River row that the Go workflow_run_fire worker reads, once per window", async () => {
    const key = `watcher:${Date.now()}-${Math.random()}`;
    const payload = {
      orgId: "org",
      workflowId: "wf-1",
      triggerKind: "watcher" as const,
      triggerPayload: { watcherId: "w1" },
    };
    const id = await enqueueWorkflowRunFire(payload, { dedupeKey: key, dedupeSeconds: 3600 });
    expect(id).not.toBeNull();
    ids.push(id!);
    expect(await enqueueWorkflowRunFire(payload, { dedupeKey: key, dedupeSeconds: 3600 })).toBeNull();
    const plain = await enqueueWorkflowRunFire({ ...payload, triggerKind: "cron" });
    expect(plain).not.toBeNull();
    ids.push(plain!);
    const { rows } = await pool().query(
      "select kind, args, max_attempts, queue, state::text from river.river_job where id = $1",
      [id],
    );
    expect(rows).toEqual([{
      kind: "workflow_run_fire",
      args: { ...payload, queuedAt: expect.any(Number), dedupe: expect.stringMatching(new RegExp(`^${key.replace(/[.]/g, "\\.")}@\\d+$`)) },
      max_attempts: 3,
      queue: "workflow_run",
      state: "available",
    }]);
    await expect(enqueueWorkflowRunFire({ ...payload, triggerKind: "api" })).rejects.toThrow(/openneko web/);
  });
});
