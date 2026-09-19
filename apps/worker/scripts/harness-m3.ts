// Acceptance driver: real queue and production handler, isolated synthetic stack only.
import assert from 'node:assert/strict';
import { db, pool, getOrgId, organization, customer_profile, data_source, llm_provider_config, processing_job, eq } from '@neko/db';
import { boss, enqueue, QUEUE, type WorkRunPayload } from '@neko/db/jobs';
import { createWorkThread, createWorkRun, getWorkRun } from '@neko/llm/work';
import { runWorkRun } from '../src/jobs/work-run.js';
import { extractActionRequestFences, extractWorkflowSaveFence, extractRuleSaveFence } from '../../../packages/llm/src/workflows/fence-parsers';
import { extractMemoryFences } from '../../../packages/llm/src/agent-backends/memory-fence';
if (process.env.HARNESS_M3_LIVE !== '1' || process.env.NEKO_PG_PORT !== '18119')
    throw Error('isolated M3 environment required');
const orgId = await getOrgId();
await db().update(organization).set({ setup_complete_at: new Date() }).where(eq(organization.id, orgId));
await db().insert(llm_provider_config).values({ org_id: orgId, scope: 'primary', provider: 'ollama', model: 'harness-fixture', config: { url: 'http://host.docker.internal:18118' } }).onConflictDoNothing();
await db().insert(data_source).values({ org_id: orgId, graphql_url: 'http://127.0.0.1:18117/api/v1/graphql', kind: 'graphjin', auth_mode: 'none', is_default: true }).onConflictDoNothing();
await db().insert(customer_profile).values({ org_id: orgId, version: 1, is_current: true, company_note: 'Synthetic reference business', business_profile: 'Seeded reference data for Harness acceptance' }).onConflictDoNothing();
if (process.argv.includes('--seed-only'))
    process.exit(0);
// An adversarial model answer must not invoke legacy mutation fences.
await fetch('http://127.0.0.1:18118/control', {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({effect_fences:true})});
const effectTables = ['action_request','action_policy','workflow_definition','work_memory'];
async function effectCounts() {
    return Promise.all(effectTables.map(async table => (await pool().query(`SELECT count(*)::int AS n FROM ${table} WHERE org_id=$1`,[orgId])).rows[0].n));
}
const beforeEffects=await effectCounts();
const queue = await boss();
await queue.createQueue(QUEUE.WORK_RUN);
await queue.work<WorkRunPayload>(QUEUE.WORK_RUN, async (jobs) => {
    for (const job of jobs) {
        const { processingJobId, orgId, ...payload } = job.data;
        await db().update(processing_job).set({ status: 'running' }).where(eq(processing_job.id, processingJobId));
        try {
            await runWorkRun(processingJobId, orgId, { ...payload, channel: 'web' });
            await db().update(processing_job).set({ status: 'succeeded' }).where(eq(processing_job.id, processingJobId));
        }
        catch (e) {
            await db().update(processing_job).set({ status: 'failed' }).where(eq(processing_job.id, processingJobId));
            throw e;
        }
    }
});
const thread = await createWorkThread(orgId, 'M3 queued lookup');
const run = await createWorkRun(orgId, thread.id, 'harness', { userId: null, role: 'service' });
const [job] = await db().insert(processing_job).values({ org_id: orgId, kind: QUEUE.WORK_RUN, trigger: 'test' }).returning();
await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId, runId: run.id, threadId: thread.id, message: 'Find the seeded reference using lookup.' }, { retryLimit: 0 });
async function waitForJob(jobId:string) {
for (let n = 0; n < 120; n++) {
    const current = await getWorkRun(orgId, run.id);
    if (current && ['completed', 'failed', 'cancelled'].includes(current.status)) {
        assert.equal(current.status, 'completed', JSON.stringify(current));
        const [finished] = await db().select().from(processing_job).where(eq(processing_job.id, jobId));
        if (finished.status !== 'succeeded') {
            await new Promise(r => setTimeout(r, 100));
            continue;
        }
        return;
    }
    await new Promise(r => setTimeout(r, 1000));
}
throw Error('queued run timed out');

}
await waitForJob(job.id);
const modelCalls = await (await fetch('http://127.0.0.1:18118/control')).json();
const receipt = (await pool().query('SELECT accepted_context, result FROM harness_run_journal WHERE org_id=$1 AND run_id=$2',[orgId,run.id])).rows[0];
const context=receipt.accepted_context;
// These are valid legacy commands, not malformed text that would be ignored anyway.
assert.equal(extractActionRequestFences(receipt.result.finalText).payloads.length,1);
assert.ok(extractWorkflowSaveFence(receipt.result.finalText).payload);
assert.ok(extractRuleSaveFence(receipt.result.finalText).payload);
assert.equal(extractMemoryFences(receipt.result.finalText).ops.length,1);
assert.ok(context?.prompt);
const operations=(await pool().query('SELECT operation_id,request,result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,run.id])).rows;
assert.equal(operations.length,1);
assert.equal(operations[0].operation_id,1);
assert.ok(operations[0].result?.response);
assert.ok(operations[0].finished_at);
assert.deepEqual(await effectCounts(),beforeEffects,'read-only Harness must not execute effect fences');
// Redelivery rebuilds context including the first answer and a changed profile.
await db().update(customer_profile).set({company_note:'Changed business context after acceptance'}).where(eq(customer_profile.org_id,orgId));
const [retry] = await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-redelivery'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:retry.id,orgId,runId:run.id,threadId:thread.id,message:'Find the seeded reference using lookup.'},{retryLimit:0});
await waitForJob(retry.id);
assert.deepEqual(await (await fetch('http://127.0.0.1:18118/control')).json(),modelCalls,'redelivery must not execute the model or lookup');
assert.deepEqual((await pool().query('SELECT accepted_context FROM harness_run_journal WHERE org_id=$1 AND run_id=$2',[orgId,run.id])).rows[0].accepted_context,context);
assert.deepEqual(await effectCounts(),beforeEffects,'replayed answers must not execute effect fences');
assert.deepEqual((await pool().query('SELECT operation_id,request,result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,run.id])).rows,operations);
assert.equal((await pool().query("SELECT count(*)::int AS n FROM work_message WHERE org_id=$1 AND run_id=$2 AND role='assistant'",[orgId,run.id])).rows[0].n,1);
console.log('M4_QUEUE_REDELIVERY_PASS',run.id);
await queue.stop({graceful:true,timeout:5000});
await pool().end();
console.log('M3_QUEUE_PASS',run.id);
process.exit(0);
