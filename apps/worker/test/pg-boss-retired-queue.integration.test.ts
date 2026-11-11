import { randomUUID } from "node:crypto";
import pg from "pg";
import PgBoss from "pg-boss";
import { afterAll, describe, expect, it } from "vitest";
import { buildPoolConfig } from "@neko/db";
import { removeRetiredQueue } from "../src/pg-boss-helpers.js";

const config = buildPoolConfig();
const admin = new pg.Pool(config);
let reachable = true;
try { await admin.query("select 1"); } catch { reachable = false; }

describe.skipIf(!reachable)("removeRetiredQueue on pg-boss 10", () => {
  const boss = new PgBoss({
    host: config.host,
    port: config.port,
    user: config.user,
    password: typeof config.password === "string" ? config.password : undefined,
    database: config.database,
    supervise: false,
    schedule: false,
  });

  afterAll(async () => {
    await boss.stop({ graceful: false, wait: true }).catch(() => undefined);
    await admin.end();
  });

  it("removes a queue that holds jobs and a schedule, where deleteQueue alone fails", async () => {
    await boss.start();
    const name = `retired_${randomUUID().replaceAll("-", "")}`;
    await boss.createQueue(name);
    await boss.schedule(name, "*/5 * * * *");
    const done = await boss.send(name, {});
    await boss.send(name, {});
    await boss.complete(name, done!);

    await expect(boss.deleteQueue(name)).rejects.toThrow(/q_fkey/);

    await removeRetiredQueue(boss, name);

    const left = await admin.query(
      `select (select count(*) from pgboss.queue where name = $1)::int as queues,
              (select count(*) from pgboss.job where name = $1)::int as jobs,
              (select count(*) from pgboss.schedule where name = $1)::int as schedules`,
      [name],
    );
    expect(left.rows[0]).toEqual({ queues: 0, jobs: 0, schedules: 0 });
    await expect(removeRetiredQueue(boss, name)).resolves.toBeUndefined();
  });
});
