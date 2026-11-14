import pg from "pg";
import { FIXTURE as F } from "../../../../packages/contract/src/fixtures";
import { cardThreads } from "./cards";

async function main() {
  const client = new pg.Client({ connectionString: process.argv[2] });
  await client.connect();
  for (const card of cardThreads) {
    await client.query(
      `insert into work_thread (id, org_id, title, created_by_user_id)
       select $1, org_id, $3, created_by_user_id from work_thread where id = $2`,
      [card.threadId, F.adminThreadId, `Card ${card.key}`],
    );
    await client.query(
      "insert into work_run (id, org_id, thread_id, backend, status) select $1, org_id, $2, 'ax', 'completed' from work_thread where id = $2",
      [card.runId, card.threadId],
    );
    await client.query(
      `insert into work_message (org_id, thread_id, run_id, role, content) select org_id, id, $2, 'user', $3 from work_thread where id = $1`,
      [card.threadId, card.runId, `Show the ${card.key} answer`],
    );
    for (const event of [{ type: "surface", messages: card.messages }, { type: "done" }]) {
      await client.query(
        "insert into work_run_event (org_id, thread_id, run_id, kind, payload) select org_id, id, $2, $3, $4::jsonb from work_thread where id = $1",
        [card.threadId, card.runId, event.type, JSON.stringify(event)],
      );
    }
  }
  await client.end();
}

void main();
