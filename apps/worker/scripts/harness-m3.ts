import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { chmod, mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { createAdminHandler } from "../src/admin-server";
import { runActionExecute } from "../src/jobs/action-execute";
import { registerActionAdapter, createActionRequest } from "@neko/llm/workflows";
// Acceptance driver: real queue and production handler, isolated synthetic stack only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { db, pool, getOrgId, getOrCreateSoloAdmin, organization, customer_profile, data_source, llm_provider_config, processing_job, pack_action_definition, action_policy, workflow_definition, workflow_run, eq } from '@neko/db';
import { boss, enqueue, QUEUE, type HarnessBatchPayload, type WorkRunPayload } from '@neko/db/jobs';
import { createWorkThread, createWorkRun, createWorkMessage, ensureWorkWorkspace, getWorkRun, shutdownAgentBroker } from '@neko/llm/work';
import { runWorkRun } from '../src/jobs/work-run.js';
import { runHarnessBatch } from '../src/jobs/harness-batch.js';
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
if (process.argv.includes('--approval-worker-only')) {
    // Explicit isolated fixture: production action queue/handler, controlled HTTP effect.
    let effects=0;
    const effect=createServer((req,res)=>{
        req.resume();effects++;
        assert.equal(effects,1,'approval must dispatch exactly once');
        res.setHeader('content-type','application/json');res.end(JSON.stringify({value:42}));
        console.log('M4_BROWSER_EFFECT_PASS',effects);
    });
    await new Promise<void>(resolve=>effect.listen(0,'127.0.0.1',resolve));
    registerActionAdapter('harness_effect_fixture',async({idempotencyKey})=>{
        const result=await (await fetch(`http://127.0.0.1:${(effect.address() as {port:number}).port}`,{method:'POST',headers:{'idempotency-key':idempotencyKey ?? ''}})).json();
        if (process.argv.includes('--effect-unknown')) throw Error('Controlled receipt loss after external commit');
        return {result};
    });
    await queue.createQueue(QUEUE.ACTION_EXECUTE);
    await queue.work(QUEUE.ACTION_EXECUTE,async jobs=>{
        for (const job of jobs) await runActionExecute(job.data as Parameters<typeof runActionExecute>[0]);
    });
    console.log('M4_APPROVAL_WORKER_READY');
    await new Promise(()=>{});
}
await queue.createQueue(QUEUE.WORK_RUN);
await queue.createQueue(QUEUE.HARNESS_BATCH);
await queue.work<HarnessBatchPayload>(QUEUE.HARNESS_BATCH, async (jobs) => {
    for (const job of jobs) await runHarnessBatch(job.data);
});
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
// A staged upload must reach the Go harness through the production queue and
// OpenShell sandbox, while another thread's upload remains invisible.
const uploadThread=await createWorkThread(orgId,'M5 staged upload');
const uploadRun=await createWorkRun(orgId,uploadThread.id,'harness',{userId:null,role:'service'});
const uploadWorkspace=await ensureWorkWorkspace(orgId,uploadThread.id,uploadRun.id);
await writeFile(join(uploadWorkspace.threadUploadsRoot,'lead.csv'),'lead_id\nLEAD-42\n');
await mkdir(join(uploadWorkspace.uploadsRoot,'other-thread'),{recursive:true});
await writeFile(join(uploadWorkspace.uploadsRoot,'other-thread','hidden.txt'),'OTHER-SECRET');
const uploadControl=await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({upload:true})});
assert.equal(uploadControl.status,204);
const [uploadJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-upload'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:uploadJob.id,orgId,runId:uploadRun.id,threadId:uploadThread.id,message:'Read the uploaded lead file.'},{retryLimit:0});
await waitForJob(uploadJob.id,uploadRun.id);
const uploadResult=(await pool().query('SELECT result FROM harness_run_journal WHERE org_id=$1 AND run_id=$2',[orgId,uploadRun.id])).rows[0].result;
assert.match(uploadResult.finalText,/LEAD-42/);
const uploadSnapshot=JSON.parse(await readFile(join(uploadWorkspace.runRoot,'.harness',`${createHash('sha256').update(uploadRun.id).digest('hex')}.json`),'utf8'));
assert.deepEqual(uploadSnapshot.operations.map((op:{tool:string})=>op.tool),['upload_search','upload_search','upload_read']);
assert.deepEqual(uploadSnapshot.operations[0].result.paths,[]);
assert.deepEqual(uploadSnapshot.operations[1].result.paths,['lead.csv']);
assert.match(uploadSnapshot.operations[2].result.content,/LEAD-42/);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,uploadRun.id])).rows[0].n,0);
console.log('M5_QUEUE_UPLOAD_PASS',uploadRun.id);
// Only this run's artifact directory is writable by the Go file tools. The
// existing Work artifact projection must expose the completed CSV once.
const soloAdmin=await getOrCreateSoloAdmin(orgId);
assert.ok(soloAdmin,'isolated solo operator required for web download');
const artifactThread=await createWorkThread(orgId,'M5 CSV artifact','web',soloAdmin.id);
const artifactRun=await createWorkRun(orgId,artifactThread.id,'harness',{userId:null,role:'service'});
const artifactWorkspace=await ensureWorkWorkspace(orgId,artifactThread.id,artifactRun.id);
const otherArtifacts=join(artifactWorkspace.runsRoot,'other-run','artifacts');
await mkdir(otherArtifacts,{recursive:true});
await writeFile(join(otherArtifacts,'hidden.txt'),'OTHER-RUN-SECRET');
const artifactControl=await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({artifact:true})});
assert.equal(artifactControl.status,204);
const [artifactJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-artifact'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:artifactJob.id,orgId,runId:artifactRun.id,threadId:artifactThread.id,message:'Create a CSV artifact with the synthetic lead.'},{retryLimit:0});
await waitForJob(artifactJob.id,artifactRun.id);
assert.equal(await readFile(join(artifactWorkspace.artifactRoot,'result.csv'),'utf8'),'lead_id\nLEAD-42\n');
const artifactEvents=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,artifactRun.id])).rows;
assert.deepEqual(artifactEvents.map(row=>row.payload.artifact.path),[`runs/${artifactRun.id}/artifacts/result.csv`]);
const artifactSnapshot=JSON.parse(await readFile(join(artifactWorkspace.runRoot,'.harness',`${createHash('sha256').update(artifactRun.id).digest('hex')}.json`),'utf8'));
assert.deepEqual(artifactSnapshot.operations.map((op:{tool:string})=>op.tool),['file_search','file_write']);
assert.deepEqual(artifactSnapshot.operations[0].result.paths,[]);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,artifactRun.id])).rows[0].n,0);
console.log('M5_QUEUE_ARTIFACT_PASS',artifactRun.id);
// The real pg-boss queue owns a separate, long-lived batch run. This fake
// executable tests host dispatch/projection; OpenShell execution is qualified
// separately by the M5b fixture on the same isolated gateway.
assert.ok(process.env.HARNESS_STATE);
const batchFixture=join(process.env.HARNESS_STATE,'harness-batch-fixture');
await writeFile(batchFixture,`#!/usr/bin/env node
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');
if(process.env.MODEL_API_KEY||!process.env.OPENNEKO_BROKER_TOKEN)process.exit(3);
const count=path.join(process.env.HARNESS_BATCH_WORK_DIR,'invocations');
fs.writeFileSync(count,String(Number(fs.existsSync(count)?fs.readFileSync(count,'utf8'):0)+1));
const artifact=path.join(process.env.HARNESS_BATCH_ARTIFACT_DIR,'union_final.csv');
const bytes=Buffer.from('lead_id\\nLEAD-42\\n');fs.writeFileSync(artifact,bytes);
process.stdout.write(JSON.stringify({artifact,sha256:crypto.createHash('sha256').update(bytes).digest('hex'),rows:1,queries:1}));
`);
await chmod(batchFixture,0o700);
const batchEnv=['OPENNEKO_HARNESS_BATCH_BIN','HARNESS_OPENSHELL_BIN','OPENSHELL_GATEWAY','HARNESS_BATCH_IMAGE',
    'HARNESS_BATCH_SCRIPT','HARNESS_BATCH_SCRIPT_SHA256','HARNESS_BATCH_BUNDLE_DIR','HARNESS_BATCH_BUNDLE_SHA256','HARNESS_BATCH_WORKFLOW_NAME','MODEL_API_KEY'] as const;
const batchPrior=Object.fromEntries(batchEnv.map(name=>[name,process.env[name]]));
Object.assign(process.env,{OPENNEKO_HARNESS_BATCH_BIN:batchFixture,HARNESS_OPENSHELL_BIN:batchFixture,
    OPENSHELL_GATEWAY:'harness-m2',HARNESS_BATCH_IMAGE:'fixture',HARNESS_BATCH_SCRIPT:batchFixture,
    HARNESS_BATCH_SCRIPT_SHA256:'fixture',HARNESS_BATCH_BUNDLE_DIR:process.env.HARNESS_STATE,
    HARNESS_BATCH_BUNDLE_SHA256:'fixture',HARNESS_BATCH_WORKFLOW_NAME:'Fixture batch workflow',MODEL_API_KEY:'host-only-secret'});
try {
    const batchThread=await createWorkThread(orgId,'M5 host-owned batch','web',soloAdmin.id);
    const batchRun=await createWorkRun(orgId,batchThread.id,'harness',{userId:null,role:'service'});
    const [batchWorkflow]=await db().insert(workflow_definition).values({org_id:orgId,name:'Fixture batch workflow',
      output_contract:{harnessBatch:{version:1,executor:'query-to-file'}}}).returning({id:workflow_definition.id});
    const [batchWorkflowRun]=await db().insert(workflow_run).values({org_id:orgId,workflow_id:batchWorkflow.id,
      thread_id:batchThread.id,work_run_id:batchRun.id,trigger_kind:'manual',trigger_payload:{targetDay:'2026-09-15'},status:'running'}).returning({id:workflow_run.id});
    const payload:HarnessBatchPayload={orgId,threadId:batchThread.id,runId:batchRun.id,workflowRunId:batchWorkflowRun.id};
    const batchJob=await enqueue(QUEUE.HARNESS_BATCH,payload,{retryLimit:0});
    assert.ok(batchJob);
    for(let n=0;n<120;n++) {
        const current=await getWorkRun(orgId,batchRun.id);
        if(current?.status==='completed' && (await queue.getJobById(QUEUE.HARNESS_BATCH,batchJob))?.state==='completed') break;
        assert.notEqual(current?.status,'failed');assert.notEqual(current?.status,'cancelled');
        await new Promise(r=>setTimeout(r,250));
    }
    assert.equal((await getWorkRun(orgId,batchRun.id))?.status,'completed');
    assert.equal((await db().select({status:workflow_run.status}).from(workflow_run).where(eq(workflow_run.id,batchWorkflowRun.id)))[0]?.status,'completed');
    const repeat=await enqueue(QUEUE.HARNESS_BATCH,payload,{retryLimit:0});assert.ok(repeat);
    for(let n=0;n<120 && (await queue.getJobById(QUEUE.HARNESS_BATCH,repeat))?.state!=='completed';n++)
        await new Promise(r=>setTimeout(r,250));
    assert.equal((await queue.getJobById(QUEUE.HARNESS_BATCH,repeat))?.state,'completed');
    const batchWorkspace=await ensureWorkWorkspace(orgId,batchThread.id,batchRun.id);
    assert.equal(await readFile(join(batchWorkspace.artifactRoot,'union_final.csv'),'utf8'),'lead_id\nLEAD-42\n');
    assert.equal(await readFile(join(batchWorkspace.runRoot,'batch','invocations'),'utf8'),'1');
    const batchEvents=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,batchRun.id])).rows;
    assert.deepEqual(batchEvents.map(row=>row.payload.artifact.path),[`runs/${batchRun.id}/artifacts/union_final.csv`]);
    console.log('M5_QUEUE_BATCH_PASS',batchRun.id);
} finally {
    for(const name of batchEnv) {if(batchPrior[name]===undefined)delete process.env[name];else process.env[name]=batchPrior[name];}
}
if (process.env.HARNESS_M3_WEB === '1') {
    assert.ok(process.env.HARNESS_STATE);
    await writeFile(join(process.env.HARNESS_STATE, 'm5-artifact-run'), artifactRun.id);
}
// Exercise the production queue handler and worker-owned proposal preflight API.
const approvalKind='harness_effect_fixture';
const beforeExecutions=(await pool().query('SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1',[orgId])).rows[0].n;
await db().insert(pack_action_definition).values({org_id:orgId,kind:approvalKind,readiness:'ready',definition_hash:'fixture',definition:{kind:approvalKind,description:'Controlled fixture action',inputSchema:{type:'object',properties:{value:{type:'integer'}},required:['value'],additionalProperties:false}}}).onConflictDoNothing();
await db().insert(action_policy).values({org_id:orgId,name:'Harness fixture approval',mode:'approval_required',applies_to_kinds:[approvalKind],applies_to_scopes:['external']});
const admin=createServer(createAdminHandler({actionRequests:{create:async input=>{const request=await createActionRequest(input as Parameters<typeof createActionRequest>[0]);return {id:request.id,status:request.status};}}}));
await new Promise<void>(resolve=>admin.listen(18122,'127.0.0.1',resolve));
const approvalOwner=(await pool().query('SELECT solo_admin_user_id FROM organization WHERE id=$1',[orgId])).rows[0]?.solo_admin_user_id ?? null;
const approvalThread=await createWorkThread(orgId,'M4 pending approval','web',approvalOwner);
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
 assert.equal((await pool().query('SELECT count(*)::int AS n FROM action_execution WHERE org_id=$1',[orgId])).rows[0].n,beforeExecutions);
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
    assert.equal((await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='message' AND payload->>'role'='assistant'",[orgId,approvalRun.id])).rows[0].n,1,'recovery must not duplicate the rendered answer');
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
