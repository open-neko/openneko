// The worker's boot loop in apps/worker/src/index.ts removes the old
// pg-boss queues with this.

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
