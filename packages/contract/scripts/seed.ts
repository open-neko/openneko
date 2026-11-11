import { appendFileSync } from "node:fs";
import {
  db,
  decideDeviceRequest,
  getOrgId,
  redeemDeviceRequest,
  setLocalAdministrator,
  sql,
  startDeviceRequest,
} from "@neko/db";
import { FIXTURE as F } from "../src/fixtures";

async function deviceToken(orgId: string, userId: string, name: string): Promise<string> {
  const started = await startDeviceRequest(orgId, name);
  await decideDeviceRequest({ orgId, userCode: started.userCode, userId, approve: true });
  const issued = await redeemDeviceRequest(orgId, started.deviceCode);
  if (issued.status !== "issued") throw new Error(`device token for ${userId}: ${issued.status}`);
  return issued.token;
}

async function seedWork(orgId: string, owner: string, ids: {
  workflow: string; thread: string; workRun: string; workflowRun: string; output: string; action: string;
}, mood: string): Promise<void> {
  await db().execute(sql`
    insert into workflow_definition (id, org_id, name, description, goal, owner_user_id, daily_run_budget)
    values (${ids.workflow}, ${orgId}, ${`Contract workflow of ${owner}`}, 'Seeded for the contract tests', 'Report stock', ${owner}, 10)`);
  await db().execute(sql`
    insert into work_thread (id, org_id, title, created_by_user_id) values (${ids.thread}, ${orgId}, 'Contract thread', ${owner})`);
  await db().execute(sql`
    insert into work_run (id, org_id, thread_id, backend, status) values (${ids.workRun}, ${orgId}, ${ids.thread}, 'ax', 'completed')`);
  await db().execute(sql`
    insert into workflow_run (id, org_id, workflow_id, thread_id, work_run_id, trigger_kind, status)
    values (${ids.workflowRun}, ${orgId}, ${ids.workflow}, ${ids.thread}, ${ids.workRun}, 'manual', 'completed')`);
  await db().execute(sql`
    insert into workflow_output (id, org_id, workflow_run_id, work_run_id, kind, title, body, mood)
    values (${ids.output}, ${orgId}, ${ids.workflowRun}, ${ids.workRun}, 'finding', 'Stock is low', 'SKU 1001 has 2 left', ${mood})`);
  await db().execute(sql`
    insert into action_request (id, org_id, workflow_run_id, scope, kind, summary, risk_level)
    values (${ids.action}, ${orgId}, ${ids.workflowRun}, 'internal', 'contract.note', 'Write a note', 'low')`);
}

const orgId = await getOrgId();
const [existing] = (await db().execute<{ n: number }>(sql`select count(*)::int as n from app_user where org_id = ${orgId}`)).rows;
if ((existing?.n ?? 0) > 0) throw new Error("seed needs a new, empty OpenNeko database");

await db().execute(sql`
  insert into app_user (id, org_id, email, name) values
    (${F.adminUserId}, ${orgId}, 'admin@contract.test', 'Contract Admin'),
    (${F.memberUserId}, ${orgId}, 'member@contract.test', 'Contract Member')`);
await setLocalAdministrator(orgId, F.adminUserId, true);
await seedWork(orgId, F.adminUserId, {
  workflow: F.adminWorkflowId, thread: F.adminThreadId, workRun: F.adminWorkRunId,
  workflowRun: F.adminWorkflowRunId, output: F.adminOutputId, action: F.adminActionRequestId,
}, "act");
await seedWork(orgId, F.memberUserId, {
  workflow: F.memberWorkflowId, thread: F.memberThreadId, workRun: F.memberWorkRunId,
  workflowRun: F.memberWorkflowRunId, output: F.memberOutputId, action: F.memberActionRequestId,
}, "watch");

const lines = [
  `OPENNEKO_CONTRACT_ADMIN_TOKEN=${await deviceToken(orgId, F.adminUserId, "Contract admin")}`,
  `OPENNEKO_CONTRACT_MEMBER_TOKEN=${await deviceToken(orgId, F.memberUserId, "Contract member")}`,
];
const out = process.argv[2];
if (out) appendFileSync(out, `${lines.join("\n")}\n`, { mode: 0o600 });
else console.log(lines.join("\n"));
process.exit(0);
