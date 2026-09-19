import { createServer } from "node:http";
import { createAdminHandler } from "../src/admin-server";
import { createActionRequest } from "@neko/llm/workflows";
// Acceptance driver: real queue and production handler, isolated synthetic stack only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { db, pool, getOrgId, organization, customer_profile, data_source, llm_provider_config, processing_job, pack_action_definition, action_policy, eq } from '@neko/db';
import { boss, enqueue, QUEUE, type WorkRunPayload } from '@neko/db/jobs';
import { createWorkThread, createWorkRun, createWorkMessage, getWorkRun, shutdownAgentBroker } from '@neko/llm/work';
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
if (process.argv.includes('--worker-only')) {
    process.send?.('ready');
    await new Promise(() => {});
}
// An adversarial model answer must not invoke legacy mutation fences.
await fetch('http://127.0.0.1:18118/control', {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({effect_fences:true})});
const effectTables = ['action_request','action_policy','workflow_definition','work_memory'];
async function effectCounts() {
    return Promise.all(effectTables.map(async table => (await pool().query(`SELECT count(*)::int AS n FROM ${table} WHERE org_id=$1`,[orgId])).rows[0].n));
}
const beforeEffects=await effectCounts();
const thread = await createWorkThread(orgId, 'M3 queued lookup');
const run = await createWorkRun(orgId, thread.id, 'harness', { userId: null, role: 'service' });
const [job] = await db().insert(processing_job).values({ org_id: orgId, kind: QUEUE.WORK_RUN, trigger: 'test' }).returning();
await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId, runId: run.id, threadId: thread.id, message: 'Find the seeded reference using lookup.' }, { retryLimit: 0 });
async function waitForJob(jobId:string, runId=run.id) {
for (let n = 0; n < 120; n++) {
    const current = await getWorkRun(orgId, runId);
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
// Exercise the production queue handler and worker-owned proposal preflight API.
const approvalKind='harness_effect_fixture';
await db().insert(pack_action_definition).values({org_id:orgId,kind:approvalKind,readiness:'ready',definition_hash:'fixture',definition:{kind:approvalKind,inputSchema:{type:'object',properties:{value:{type:'integer'}},required:['value'],additionalProperties:false}}});
await db().insert(action_policy).values({org_id:orgId,name:'Harness fixture approval',mode:'approval_required',applies_to_kinds:[approvalKind],applies_to_scopes:['external']});
const admin=createServer(createAdminHandler({actionRequests:{create:async input=>{const request=await createActionRequest(input as Parameters<typeof createActionRequest>[0]);return {id:request.id,status:request.status};}}}));
await new Promise<void>(resolve=>admin.listen(18122,'127.0.0.1',resolve));
const approvalThread=await createWorkThread(orgId,'M4 pending approval');
const approvalRun=await createWorkRun(orgId,approvalThread.id,'harness',{userId:null,role:'service'});
const approvalMessage='Request approval to set the synthetic fixture value to 42.';
await createWorkMessage({orgId,threadId:approvalThread.id,runId:approvalRun.id,role:'user',content:approvalMessage});
try {
 await fetch('http://127.0.0.1:18118/control',{method:'POST',body:JSON.stringify({proposal:true})});
 const [approvalJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-approval'}).returning();
 await enqueue(QUEUE.WORK_RUN,{processingJobId:approvalJob.id,orgId,runId:approvalRun.id,threadId:approvalThread.id,message:approvalMessage},{retryLimit:0});
 await waitForJob(approvalJob.id,approvalRun.id);
 const proposals=(await pool().query('SELECT id,status FROM action_request WHERE org_id=$1 AND work_run_id=$2',[orgId,approvalRun.id])).rows;
 assert.equal(proposals.length,1);assert.equal(proposals[0].status,'pending_approval');
 assert.equal((await pool().query('SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1',[orgId])).rows[0].n,0);
 console.log('M4_QUEUE_APPROVAL_PASS',approvalThread.id,approvalRun.id,proposals[0].id);
} finally {await new Promise<void>(resolve=>admin.close(()=>resolve()));}

// Crash the actual production handler process, including its broker, while
// the remote responder waits. Let pg-boss expire the abandoned job, then restart
// consumption after the original sandbox has had time to finish its response.
await queue.offWork(QUEUE.WORK_RUN);
await shutdownAgentBroker();
await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({pause_responder:true})});
const crashThread=await createWorkThread(orgId,'M4 worker death');
const crashRun=await createWorkRun(orgId,crashThread.id,'harness',{userId:null,role:'service'});
await createWorkMessage({orgId,threadId:crashThread.id,runId:crashRun.id,role:'user',content:'Find the seeded reference using lookup.'});
const [crashJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-worker-death'}).returning();
const queueId=await enqueue(QUEUE.WORK_RUN,{processingJobId:crashJob.id,orgId,runId:crashRun.id,threadId:crashThread.id,message:'Find the seeded reference using lookup.'},{retryLimit:1,retryDelay:0,expireInSeconds:60});
assert.ok(queueId);
const children: ReturnType<typeof spawn>[]=[];
async function startWorker() {
    const child=spawn(process.execPath,['--import','tsx',fileURLToPath(import.meta.url),'--worker-only'],{detached:true,stdio:['ignore','inherit','pipe','ipc']});
    children.push(child);
    let error=''; child.stderr?.on('data',chunk=>{error=(error+chunk).slice(-4096);});
    await Promise.race([
        once(child,'message',{signal:AbortSignal.timeout(15_000)}),
        once(child,'exit').then(()=>{throw Error(`Worker exited before readiness: ${error}`);}),
    ]);
    return child;
}
try {
    const first=await startWorker();
    let reached=false;
    for(let n=0;n<300;n++) {
        if((await (await fetch('http://127.0.0.1:18118/control')).json())['harness-fixture']===3) {reached=true;break;}
        assert.equal(first.exitCode,null,'worker must remain alive until fault injection');
        await new Promise(r=>setTimeout(r,100));
    }
    assert.ok(reached,'worker did not reach paused responder');
    const crashCalls=await (await fetch('http://127.0.0.1:18118/control')).json();
    const beforeCrash=(await pool().query('SELECT accepted_context,result FROM harness_run_journal WHERE org_id=$1 AND run_id=$2',[orgId,crashRun.id])).rows[0];
    assert.equal(beforeCrash.result,null);
    const crashOperations=(await pool().query('SELECT operation_id,request,result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,crashRun.id])).rows;
    assert.equal(crashOperations.length,1);
    assert.ok(crashOperations[0].result);
    const exited=once(first,'exit'); first.kill('SIGKILL'); await exited;
    assert.equal((await queue.getJobById(QUEUE.WORK_RUN,queueId))?.state,'active');
    // Exercise pg-boss's real expiry/retry transition, without editing queue rows
    // or enqueueing a replacement accepted input. The 60s lease also outlasts
    // the fixture's 30s responder pause.
    let expired=false;
    for(let n=0;n<90;n++) {
        await queue.maintain();
        const state=await queue.getJobById(QUEUE.WORK_RUN,queueId);
        if(state?.state==='retry') {expired=true;break;}
        assert.equal(state?.state,'active');
        await new Promise(r=>setTimeout(r,1000));
    }
    assert.ok(expired,'abandoned queue job did not become retryable');
    await startWorker();
    await waitForJob(crashJob.id,crashRun.id);
    const recovered=(await pool().query('SELECT accepted_context,result FROM harness_run_journal WHERE org_id=$1 AND run_id=$2',[orgId,crashRun.id])).rows[0];
    assert.deepEqual(recovered.accepted_context,beforeCrash.accepted_context);
    assert.equal(recovered.result.status,'completed');
    assert.match(recovered.result.finalText,/REF-42/);
    const afterCalls=await (await fetch('http://127.0.0.1:18118/control')).json();
    for(const model of ['harness-fixture','graphjin-fixture']) assert.equal(afterCalls[model],crashCalls[model]);
    assert.deepEqual((await pool().query('SELECT operation_id,request,result,finished_at FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,crashRun.id])).rows,crashOperations);
    for(const role of ['user','assistant']) assert.equal((await pool().query('SELECT count(*)::int AS n FROM work_message WHERE org_id=$1 AND run_id=$2 AND role=$3',[orgId,crashRun.id,role])).rows[0].n,1);
    let redelivered=await queue.getJobById(QUEUE.WORK_RUN,queueId);
    for(let n=0;n<50 && redelivered?.state!=='completed';n++) {
        await new Promise(r=>setTimeout(r,100));
        redelivered=await queue.getJobById(QUEUE.WORK_RUN,queueId);
    }
    assert.equal(redelivered?.state,'completed');
    assert.equal(redelivered?.retryCount,1);
    console.log('M4_QUEUE_WORKER_DEATH_PASS',crashRun.id,queueId);
    const [approvalRetry]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-approval-restart'}).returning();
    await enqueue(QUEUE.WORK_RUN,{processingJobId:approvalRetry.id,orgId,runId:approvalRun.id,threadId:approvalThread.id,message:approvalMessage},{retryLimit:0});
    await waitForJob(approvalRetry.id,approvalRun.id);
    const restored=(await pool().query('SELECT id,status FROM action_request WHERE org_id=$1 AND work_run_id=$2',[orgId,approvalRun.id])).rows;
    assert.equal(restored.length,1);assert.equal(restored[0].status,'pending_approval');
    assert.deepEqual(await (await fetch('http://127.0.0.1:18118/control')).json(),afterCalls);
    console.log('M4_QUEUE_APPROVAL_RESTART_PASS',approvalRun.id);

} finally {
    for(const child of children) {
        try {process.kill(-child.pid!,'SIGKILL');} catch(error) {if((error as NodeJS.ErrnoException).code!=='ESRCH') throw error;}
    }
}
await queue.stop({graceful:true,timeout:5000});
await pool().end();
console.log('M3_QUEUE_PASS',run.id);
process.exit(0);
