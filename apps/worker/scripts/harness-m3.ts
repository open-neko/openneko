import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { chmod, mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { createAdminHandler } from "../src/admin-server";
import { runActionExecute } from "../src/jobs/action-execute";
import { registerActionAdapter, createActionRequest, enableWorkflowApiAccess, admitWorkflowApiRun, activeBatchExecutor } from "@neko/llm/workflows";
// Acceptance driver: real queue and production handler, isolated synthetic stack only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { db, pool, getOrgId, getOrCreateSoloAdmin, organization, customer_profile, data_source, llm_provider_config, processing_job, pack_action_definition, action_policy, workflow_definition, workflow_run, eq } from '@neko/db';
import { boss, enqueue, QUEUE, type HarnessBatchPayload, type WorkRunPayload, type WorkflowRunFirePayload } from '@neko/db/jobs';
import { createWorkThread, createWorkRun, createWorkMessage, ensureWorkWorkspace, getWorkRun, shutdownAgentBroker } from '@neko/llm/work';
import { runWorkRun } from '../src/jobs/work-run.js';
import { runHarnessBatch } from '../src/jobs/harness-batch.js';
import { runWorkflowRunFire } from '../src/jobs/workflow-run-fire.js';
import { runWorkflowApiDispatcherTick } from '../src/workflow-api-dispatcher.js';
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
await queue.createQueue(QUEUE.WORKFLOW_RUN_FIRE);
await queue.work<HarnessBatchPayload>(QUEUE.HARNESS_BATCH, async (jobs) => {
    for (const job of jobs) await runHarnessBatch(job.data);
});
await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async (jobs) => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
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
async function waitForJob(jobId:string, runId=run.id, expected='completed') {
for (let n = 0; n < 120; n++) {
    const current = await getWorkRun(orgId, runId);
    if (current && ['completed', 'failed', 'cancelled', 'needs_input'].includes(current.status)) {
        assert.equal(current.status, expected, JSON.stringify(current));
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
const outerUsageEvents = async () => (await pool().query(
    "SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='usage' AND payload->>'source'='outer' ORDER BY id",
    [orgId,run.id],
)).rows;
const initialUsage = await outerUsageEvents();
assert.equal(initialUsage.length,1,'one outer usage event per completed Harness run');
assert.deepEqual(initialUsage[0].payload.usage,{coverage:'complete',inputTokens:30,outputTokens:30,totalTokens:60,cacheReadTokens:0,cacheWriteTokens:0,reasoningTokens:0});
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
assert.deepEqual(await outerUsageEvents(),initialUsage,'redelivery must not record model usage twice');
console.log('M4_QUEUE_REDELIVERY_PASS',run.id);
// A clarification is a terminal handoff. Queue redelivery must recover the
// durable question instead of turning it into an assistant completion.
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({clarification:true})})).status,204);
const questionThread=await createWorkThread(orgId,'M5 clarification');
const questionRun=await createWorkRun(orgId,questionThread.id,'harness',{userId:null,role:'service'});
const [questionJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-clarification'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:questionJob.id,orgId,runId:questionRun.id,threadId:questionThread.id,message:'Ask me which day.'},{retryLimit:0});
await waitForJob(questionJob.id,questionRun.id,'needs_input');
const questionEvents=async () => (await pool().query("SELECT kind,payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind IN ('surface','needs_input') ORDER BY id",[orgId,questionRun.id])).rows;
const initialQuestions=await questionEvents();
assert.deepEqual(initialQuestions.map(row=>row.kind),['surface','needs_input']);
assert.match(initialQuestions[1].payload.question,/Which day/);
const assistantQuestionEvents=async () => (await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='message' AND payload->>'role'='assistant'",[orgId,questionRun.id])).rows;
assert.deepEqual(await assistantQuestionEvents(),[],'clarification must not include an assistant answer');
const questionCalls=await (await fetch('http://127.0.0.1:18118/control')).json();
assert.equal(questionCalls['harness-fixture'],2,'clarification must stop the Ax loop');
const [questionRetry]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-clarification-redelivery'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:questionRetry.id,orgId,runId:questionRun.id,threadId:questionThread.id,message:'Ask me which day.'},{retryLimit:0});
await waitForJob(questionRetry.id,questionRun.id,'needs_input');
assert.deepEqual(await questionEvents(),initialQuestions,'redelivery must not duplicate clarification events');
assert.equal((await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='done'",[orgId,questionRun.id])).rows[0].n,1);
assert.deepEqual(await assistantQuestionEvents(),[],'redelivery must not append an assistant answer');
assert.deepEqual(await (await fetch('http://127.0.0.1:18118/control')).json(),questionCalls,'redelivery must not call model');
console.log('M5_QUEUE_CLARIFICATION_PASS',questionRun.id);
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({answer_clarification:true})})).status,204);
const answerRun=await createWorkRun(orgId,questionThread.id,'harness',{userId:null,role:'service'});
const [answerJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-clarification-answer'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:answerJob.id,orgId,runId:answerRun.id,threadId:questionThread.id,message:'2026-09-15'},{retryLimit:0});
await waitForJob(answerJob.id,answerRun.id);
const answerReceipt=(await pool().query('SELECT accepted_context,result FROM harness_run_journal WHERE org_id=$1 AND run_id=$2',[orgId,answerRun.id])).rows[0];
assert.match(answerReceipt.accepted_context.prompt,/Which day\?/);
assert.match(answerReceipt.accepted_context.prompt,/2026-09-15/);
assert.match(answerReceipt.result.finalText,/2026-09-15/);
console.log('M5_QUEUE_CLARIFICATION_ANSWER_PASS',answerRun.id);
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({card:true})})).status,204);
const cardThread=await createWorkThread(orgId,'M5 rendered card');
const cardRun=await createWorkRun(orgId,cardThread.id,'harness',{userId:null,role:'service'});
const [cardJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-card'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:cardJob.id,orgId,runId:cardRun.id,threadId:cardThread.id,message:'Show a summary card.'},{retryLimit:0});
await waitForJob(cardJob.id,cardRun.id);
const cardEvents=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='surface'",[orgId,cardRun.id])).rows;
assert.equal(cardEvents.length,1);
assert.equal(cardEvents[0].payload.messages[0].createSurface.surfaceId,'fixture-card');
assert.equal(cardEvents[0].payload.messages[0].createSurface.components[0].text,'Harness card persisted');
if (process.env.HARNESS_M3_WEB === '1') await writeFile(join(process.env.HARNESS_STATE!,'m5-card-thread'),cardThread.id);
console.log('M5_QUEUE_CARD_PASS',cardRun.id);
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
const skillThread=await createWorkThread(orgId,'M5 staged skill');
const skillRun=await createWorkRun(orgId,skillThread.id,'harness',{userId:null,role:'service'});
const skillWorkspace=await ensureWorkWorkspace(orgId,skillThread.id,skillRun.id);
await mkdir(join(skillWorkspace.skillsRoot,'fixture-task'),{recursive:true});
await writeFile(join(skillWorkspace.skillsRoot,'fixture-task','SKILL.md'),'---\nname: fixture-task\ndescription: Create a dated fixture artifact\n---\nSKILL-MARKER: Write skill-result.csv with the selected day. Do not call an LLM from this skill.\n');
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({skill:true})})).status,204);
const [skillJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-skill'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:skillJob.id,orgId,runId:skillRun.id,threadId:skillThread.id,message:'Follow fixture-task and create its dated CSV.'},{retryLimit:0});
await waitForJob(skillJob.id,skillRun.id);
assert.equal(await readFile(join(skillWorkspace.artifactRoot,'skill-result.csv'),'utf8'),'day\n2026-09-15\n');
const skillSnapshot=JSON.parse(await readFile(join(skillWorkspace.runRoot,'.harness',`${createHash('sha256').update(skillRun.id).digest('hex')}.json`),'utf8'));
assert.deepEqual(skillSnapshot.operations.map((op:{tool:string})=>op.tool),['skill_read','file_write']);
console.log('M5_QUEUE_SKILL_PASS',skillRun.id);
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
// The model-visible process tool must stage only this thread's selected upload,
// execute in a second credential-free sandbox, and publish a journaled artifact.
const processThread=await createWorkThread(orgId,'M5 isolated process','web',soloAdmin.id);
const processRun=await createWorkRun(orgId,processThread.id,'harness',{userId:null,role:'service'});
const processWorkspace=await ensureWorkWorkspace(orgId,processThread.id,processRun.id);
await writeFile(join(processWorkspace.threadUploadsRoot,'lead.csv'),'lead_id\nLEAD-42\n');
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({process:true})})).status,204);
const [processJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-isolated-process'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:processJob.id,orgId,runId:processRun.id,threadId:processThread.id,message:'Run a credential-isolated script on the uploaded lead file.'},{retryLimit:0});
await waitForJob(processJob.id,processRun.id);
assert.equal(await readFile(join(processWorkspace.artifactRoot,'process-1','result.csv'),'utf8'),'lead_id\nLEAD-42\n');
const processEvents=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,processRun.id])).rows;
assert.deepEqual(processEvents.map(row=>row.payload.artifact.path),[`runs/${processRun.id}/artifacts/process-1/result.csv`]);
const processJournal=(await pool().query('SELECT operation_id,request,result FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,processRun.id])).rows;
assert.equal(processJournal.length,1);
assert.equal(processJournal[0].request.tool,'process_run');
assert.equal(processJournal[0].result.ok,true);
assert.equal(processJournal[0].result.files[0].sha256,createHash('sha256').update('lead_id\nLEAD-42\n').digest('hex'));
console.log('M5_QUEUE_PROCESS_PASS',processRun.id);
if (process.env.HARNESS_M3_WEB === '1' || process.env.HARNESS_M3_API_HTTP === '1') {
  await writeFile(join(process.env.HARNESS_STATE!,'m5-process-run'),processRun.id);
  await writeFile(join(process.env.HARNESS_STATE!,'m5-process-thread'),processThread.id);
}
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
const artifact=path.join(process.env.HARNESS_BATCH_ARTIFACT_DIR,process.env.HARNESS_BATCH_ARTIFACT_NAME);
const bytes=Buffer.from('lead_id\\nLEAD-42\\n');fs.writeFileSync(artifact,bytes);
process.stdout.write(JSON.stringify({artifact,sha256:crypto.createHash('sha256').update(bytes).digest('hex'),rows:1,queries:1}));
`);
await chmod(batchFixture,0o700);
const batchEnv=['HARNESS_BATCH_EXECUTOR_REGISTRY','MODEL_API_KEY'] as const;
async function pinBatch(workflowId:string,revision:string,file:string,config:{binary:string;openshellBin:string;
    gateway:string;image:string;script:string;scriptSha256:string;bundleDir:string;bundleSha256:string}) {
    const binarySha256=createHash('sha256').update(await readFile(config.binary)).digest('hex');
    const openshellSha256=createHash('sha256').update(await readFile(config.openshellBin)).digest('hex');
    await writeFile(file,JSON.stringify({version:1,executors:[{workflowId,revision,active:true,
      ...config,binarySha256,openshellSha256}]}));
    const selected=activeBatchExecutor(workflowId);
    assert.ok(selected);
    return {revision:selected.revision,fingerprint:selected.fingerprint};
}
const batchPrior=Object.fromEntries(batchEnv.map(name=>[name,process.env[name]]));
Object.assign(process.env,{HARNESS_BATCH_EXECUTOR_REGISTRY:join(process.env.HARNESS_STATE,'batch-fixture-registry.json'),
    MODEL_API_KEY:'host-only-secret'});
try {
    const batchThread=await createWorkThread(orgId,'M5 host-owned batch','web',soloAdmin.id);
    const batchRun=await createWorkRun(orgId,batchThread.id,'harness',{userId:null,role:'service'});
    const batchContract={version:1,executor:'query-to-file',artifactName:'leads.csv',columns:['lead_id']};
    const [batchWorkflow]=await db().insert(workflow_definition).values({org_id:orgId,name:'Fixture batch workflow',
      output_contract:{harnessBatch:batchContract}}).returning({id:workflow_definition.id});
    const batchBinding=await pinBatch(batchWorkflow.id,'fixture-v1',process.env.HARNESS_BATCH_EXECUTOR_REGISTRY!,{
      binary:batchFixture,openshellBin:batchFixture,gateway:'harness-m2',image:'fixture',
      script:batchFixture,scriptSha256:createHash('sha256').update(await readFile(batchFixture)).digest('hex'),
      bundleDir:process.env.HARNESS_STATE!,bundleSha256:'0'.repeat(64)});
    const [batchWorkflowRun]=await db().insert(workflow_run).values({org_id:orgId,workflow_id:batchWorkflow.id,
      thread_id:batchThread.id,work_run_id:batchRun.id,trigger_kind:'manual',trigger_payload:{targetDay:'2026-09-15'},
      executor_contract:{...batchContract,binding:batchBinding},status:'running'}).returning({id:workflow_run.id});
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
    assert.equal(await readFile(join(batchWorkspace.artifactRoot,'leads.csv'),'utf8'),'lead_id\nLEAD-42\n');
    assert.equal(await readFile(join(batchWorkspace.runRoot,'batch','invocations'),'utf8'),'1');
    const batchEvents=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,batchRun.id])).rows;
    assert.deepEqual(batchEvents.map(row=>row.payload.artifact.path),[`runs/${batchRun.id}/artifacts/leads.csv`]);
    console.log('M5_QUEUE_BATCH_PASS',batchRun.id);
} finally {
    for(const name of batchEnv) {if(batchPrior[name]===undefined)delete process.env[name];else process.env[name]=batchPrior[name];}
}
// Drive the production queue and Go runner through a real OpenShell sandbox,
// broker batchRead token, and seeded GraphJin data source. The script gets no
// model credential or broker token; it receives only the response file.
assert.ok(process.env.HARNESS_M3_BATCH_BIN && process.env.HARNESS_M3_BATCH_SCRIPT);
const workflowScript=process.env.HARNESS_M3_BATCH_SCRIPT;
const scriptBytes=await readFile(workflowScript);
const scriptHash=createHash('sha256').update(scriptBytes).digest();
const bundleHash=createHash('sha256').update('run.py\0').update(scriptHash).digest('hex');
const realBatchPrior=Object.fromEntries(batchEnv.map(name=>[name,process.env[name]]));
Object.assign(process.env,{HARNESS_BATCH_EXECUTOR_REGISTRY:process.env.HARNESS_BATCH_EXECUTOR_REGISTRY ??
    join(process.env.HARNESS_STATE,'real-batch-registry.json'),MODEL_API_KEY:'host-only-secret'});
try {
    const realThread=await createWorkThread(orgId,'M5 GraphJin batch','workflow');
    const realRun=await createWorkRun(orgId,realThread.id,'harness',{userId:null,role:'service'});
    const realContract={version:1,executor:'query-to-file',artifactName:'references.csv',columns:['reference']};
    const [realWorkflow]=await db().insert(workflow_definition).values({
      ...(process.env.HARNESS_M3_WORKFLOW_ID ? {id:process.env.HARNESS_M3_WORKFLOW_ID} : {}),
      org_id:orgId,name:'Fixture GraphJin workflow',
      output_contract:{harnessBatch:realContract}}).returning({id:workflow_definition.id});
    const realBinding=await pinBatch(realWorkflow.id,'graphjin-v1',process.env.HARNESS_BATCH_EXECUTOR_REGISTRY!,{
      binary:process.env.HARNESS_M3_BATCH_BIN!,openshellBin:process.env.HARNESS_OPENSHELL_BIN!,
      gateway:'harness-m2',image:'harness-openneko:m3',script:workflowScript,
      scriptSha256:scriptHash.toString('hex'),bundleDir:join(process.env.HARNESS_STATE!,'workflow-bundle'),
      bundleSha256:bundleHash});
    const [realWorkflowRun]=await db().insert(workflow_run).values({org_id:orgId,workflow_id:realWorkflow.id,
      thread_id:realThread.id,work_run_id:realRun.id,trigger_kind:'manual',trigger_payload:{targetDay:'2026-09-15'},
      executor_contract:{...realContract,binding:realBinding},status:'running'}).returning({id:workflow_run.id});
    const realJob=await enqueue(QUEUE.HARNESS_BATCH,{orgId,threadId:realThread.id,runId:realRun.id,
      workflowRunId:realWorkflowRun.id},{retryLimit:1,retryDelay:1,expireInSeconds:1500});
    assert.ok(realJob);
    for(let n=0;n<240;n++) {
        const current=await getWorkRun(orgId,realRun.id);
        const job=await queue.getJobById(QUEUE.HARNESS_BATCH,realJob);
        if(current?.status==='completed' && job?.state==='completed') break;
        assert.notEqual(current?.status,'failed');assert.notEqual(current?.status,'cancelled');
        assert.notEqual(job?.state,'failed');
        await new Promise(r=>setTimeout(r,250));
    }
    assert.equal((await getWorkRun(orgId,realRun.id))?.status,'completed');
    assert.equal((await queue.getJobById(QUEUE.HARNESS_BATCH,realJob))?.state,'completed');
    const realWorkspace=await ensureWorkWorkspace(orgId,realThread.id,realRun.id);
    assert.equal((await readFile(join(realWorkspace.artifactRoot,'references.csv'),'utf8')).replaceAll('\r\n','\n'),'reference\nREF-42\n');
    const realStatus=(await db().select({status:workflow_run.status,progress:workflow_run.progress}).from(workflow_run)
      .where(eq(workflow_run.id,realWorkflowRun.id)))[0];
    assert.equal(realStatus?.status,'completed');
    assert.deepEqual(realStatus?.progress,{stage:'completed',rows:1,queries:1,artifactBytes:19});
    const realEvents=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,realRun.id])).rows;
    assert.deepEqual(realEvents.map(row=>row.payload.artifact.path),[`runs/${realRun.id}/artifacts/references.csv`]);
    console.log('M5_QUEUE_BATCH_GRAPHJIN_PASS',realRun.id);
    const {token}=await enableWorkflowApiAccess({orgId,workflowId:realWorkflow.id,actor:{userId:soloAdmin.id,role:'admin'}});
    const httpApi=process.env.HARNESS_M3_API_HTTP==='1';
    const apiBase='http://localhost:18121';
    const admitted=httpApi
      ? await (async()=>{
          const response=await fetch(`${apiBase}/api/v1/workflows/${realWorkflow.id}/runs`,{
            method:'POST',headers:{'content-type':'application/json',authorization:`Bearer ${token}`,
              'idempotency-key':'m5-graphjin-api-batch'},body:JSON.stringify({targetDay:'2026-09-15'})});
          if(response.status!==202) throw new Error(`Workflow HTTP admission returned ${response.status}: ${await response.text()}`);
          return response.json() as Promise<{runId:string;statusUrl:string}>;
        })()
      : await admitWorkflowApiRun({workflowId:realWorkflow.id,token,idempotencyKey:'m5-graphjin-api-batch',
          mode:'single',value:{targetDay:'2026-09-15'},clientFingerprint:`m5-${orgId}`});
    await db().update(workflow_definition).set({name:'Renamed after API admission',
      output_contract:{harnessBatch:{...realContract,artifactName:'later.csv',columns:['later']}}})
      .where(eq(workflow_definition.id,realWorkflow.id));
    const dispatch=await runWorkflowApiDispatcherTick();
    assert.equal(dispatch.dispatched,1);
    let apiRow:{status:string;work_run_id:string;result_artifact_path:string|null}|undefined;
    for(let n=0;n<240;n++) {
      apiRow=(await pool().query('SELECT status,work_run_id,result_artifact_path FROM workflow_run WHERE id=$1',[admitted.runId])).rows[0];
      if(apiRow?.status==='completed')break;
      assert.notEqual(apiRow?.status,'failed');assert.notEqual(apiRow?.status,'cancelled');
      await new Promise(r=>setTimeout(r,250));
    }
    assert.equal(apiRow?.status,'completed');
    assert.equal(apiRow.result_artifact_path,`runs/${apiRow.work_run_id}/artifacts/references.csv`);
    const apiWorkspace=await ensureWorkWorkspace(orgId,(await pool().query('SELECT thread_id FROM workflow_run WHERE id=$1',[admitted.runId])).rows[0].thread_id,apiRow.work_run_id);
    assert.equal((await readFile(join(apiWorkspace.artifactRoot,'references.csv'),'utf8')).replaceAll('\r\n','\n'),'reference\nREF-42\n');
    const apiEvents=(await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,apiRow.work_run_id])).rows[0].n;
    assert.equal(apiEvents,1);
    console.log('M5_API_BATCH_GRAPHJIN_PASS',admitted.runId);
    if(httpApi){
      const headers={authorization:`Bearer ${token}`};
      const status=await fetch(new URL(admitted.statusUrl,apiBase),{headers});
      assert.equal(status.status,200);
      const outcome=await status.json() as {status:string;artifact:{url:string}|null};
      assert.equal(outcome.status,'completed');
      assert.ok(outcome.artifact?.url);
      const download=await fetch(new URL(outcome.artifact.url,apiBase),{headers});
      assert.equal(download.status,200);
      assert.match(download.headers.get('content-type')??'',/text\/csv/);
      assert.equal(download.headers.get('content-disposition'),`attachment; filename="workflow-${admitted.runId}.csv"`);
      assert.equal((await download.text()).replaceAll('\r\n','\n'),'reference\nREF-42\n');
      console.log('M5_HTTP_API_BATCH_GRAPHJIN_PASS',admitted.runId);
    }
    if (process.env.HARNESS_M3_WEB === '1')
        await writeFile(join(process.env.HARNESS_STATE!,'m5-batch-workflow-run'),realWorkflowRun.id);
} finally {
    for(const name of batchEnv) {if(realBatchPrior[name]===undefined)delete process.env[name];else process.env[name]=realBatchPrior[name];}
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
        once(child,'message',{signal:AbortSignal.timeout(45_000)}),
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
