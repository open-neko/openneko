// Tiny helpers around pg-boss 10.x quirks. Extracted for testability —
// the worker's boot loop in apps/worker/src/index.ts uses these.

type CreateQueueFn = (
  name: string,
  options: { name: string; expireInSeconds: number },
) => Promise<unknown>;

// pg-boss 10.x createQueue isn't idempotent: when the partition table
// already exists (queue created in a prior boot, or surviving a
// container recreate), the underlying CREATE TABLE raises 42P07
// "relation already exists" and the worker would crash on boot before
// registering any handlers. Swallow that one specific code; rethrow
// every other error so real failures still surface.
export async function ensureQueueExists(
  createQueue: CreateQueueFn,
  name: string,
  expireInSeconds = 600,
): Promise<void> {
  try {
    await createQueue(name, { name, expireInSeconds });
  } catch (e) {
    const code = (e as { code?: string }).code;
    if (code !== "42P07") throw e;
  }
}

type RetiredQueueBoss = {
  unschedule(name: string): Promise<unknown>;
  getDb(): { executeSql(text: string, values?: unknown[]): Promise<unknown> };
  deleteQueue(name: string): Promise<unknown>;
};

// pg-boss 10.x deleteQueue fails while the queue still holds a job in any
// state: q_fkey is ON DELETE RESTRICT, which is checked at once.
export async function removeRetiredQueue(
  b: RetiredQueueBoss,
  name: string,
): Promise<void> {
  await b.unschedule(name);
  await b.getDb().executeSql("DELETE FROM pgboss.job WHERE name = $1", [name]);
  await b.deleteQueue(name);
}
