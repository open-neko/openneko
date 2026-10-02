import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { chmod, mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { createAdminHandler } from "../src/admin-server";
import { runActionExecute } from "../src/jobs/action-execute";
import { registerActionAdapter, createActionRequest, enableWorkflowApiAccess, admitWorkflowApiRun, activeBatchExecutor, buildSourceChangeDryRunQuery, parseSourceChangeFilter, parseSourceChangeMatch, sweepWatchers } from "@neko/llm/workflows";
// Acceptance driver: real queue and production handler, isolated synthetic stack only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { db, pool, getOrgId, getOrCreateSoloAdmin, organization, customer_profile, data_source, data_source_secret, llm_provider_config, openapi_spec_asset, processing_job, pack_action_definition, action_policy, workflow_definition, workflow_run, eq } from '@neko/db';
import { boss, enqueue, QUEUE, type HarnessBatchPayload, type LibraryDistillPayload, type LibraryExtractPayload, type WorkRunPayload, type WorkflowRunFirePayload } from '@neko/db/jobs';
import { createWorkThread, createWorkRun, createWorkMessage, ensureWorkWorkspace, getWorkRun, inProcessControlPlane, shutdownAgentBroker } from '@neko/llm/work';
import { dispatchEmbeddingJobs, runEmbeddingIndexJob, runLibraryDistill, type EmbeddingIndexPayload } from '@neko/llm';
import { runWorkRun } from '../src/jobs/work-run.js';
import { runHarnessBatch } from '../src/jobs/harness-batch.js';
import { runWorkflowRunFire } from '../src/jobs/workflow-run-fire.js';
import { runLibraryExtractJob } from '../src/jobs/library-extract.js';
import { runLibraryDistillJob } from '../src/jobs/library-distill.js';
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
    }, 'pack');
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
await queue.createQueue(QUEUE.LIBRARY_EXTRACT);
await queue.createQueue(QUEUE.LIBRARY_DISTILL);
await queue.createQueue(QUEUE.EMBEDDING_INDEX);
await queue.work<HarnessBatchPayload>(QUEUE.HARNESS_BATCH, async (jobs) => {
    for (const job of jobs) await runHarnessBatch(job.data);
});
await queue.work<WorkflowRunFirePayload>(QUEUE.WORKFLOW_RUN_FIRE, async (jobs) => {
    for (const job of jobs) await runWorkflowRunFire(job.data);
});
await queue.work<LibraryExtractPayload>(QUEUE.LIBRARY_EXTRACT, async jobs => {
    for (const job of jobs) await runLibraryExtractJob(job.data);
});
await queue.work<LibraryDistillPayload>(QUEUE.LIBRARY_DISTILL, async jobs => {
    for (const job of jobs) await runLibraryDistillJob(job.data,{run: input => runLibraryDistill({
        ...input,
        llm: async prompt => {
            assert.match(prompt,/UPLOAD-LIBRARY-42/,'distiller must see uploaded file bytes');
            return '```neko_library\n'+JSON.stringify([{op:'upsert',path:'fixture/uploaded-policy.md',type:'Policy',title:'Uploaded lead policy',body:'UPLOAD-LIBRARY-42 is the verified uploaded-document marker.'}])+'\n```';
        },
    })});
});
await queue.work<EmbeddingIndexPayload>(QUEUE.EMBEDDING_INDEX, async jobs => {
    for (const job of jobs) await runEmbeddingIndexJob(job.data);
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
let crashRunIdForDiagnostics='';
async function waitForJob(jobId:string, runId=run.id, expected='completed') {
for (let n = 0; n < 120; n++) {
    const current = await getWorkRun(orgId, runId);
    if (current && ['completed', 'failed', 'cancelled', 'needs_input'].includes(current.status)) {
        if (current.status !== expected && runId === crashRunIdForDiagnostics) {
            const journal=(await pool().query("SELECT result->>'status' AS status,result->>'error' AS error FROM harness_run_journal WHERE org_id=$1 AND run_id=$2",[orgId,runId])).rows[0];
            const events=(await pool().query('SELECT kind FROM work_run_event WHERE org_id=$1 AND run_id=$2 ORDER BY id DESC LIMIT 12',[orgId,runId])).rows;
            console.error('M4_WORKER_DEATH_DIAGNOSTIC',JSON.stringify({journal,events,model:await (await fetch('http://127.0.0.1:18118/control')).json()}));
        }
        assert.equal(current.status, expected, JSON.stringify(current));
        const [finished] = await db().select().from(processing_job).where(eq(processing_job.id, jobId));
        if (finished.status !== 'succeeded' && !(expected === 'failed' && finished.status === 'failed')) {
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
const soloAdmin=await getOrCreateSoloAdmin(orgId);
assert.ok(soloAdmin,'isolated solo operator required for web acceptance');
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({card:true})})).status,204);
const cardThread=await createWorkThread(orgId,'M5 rendered card','web',soloAdmin.id);
const cardRun=await createWorkRun(orgId,cardThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
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
await writeFile(join(processWorkspace.threadUploadsRoot,'hidden.txt'),'UNSELECTED-UPLOAD-SECRET');
process.env.OPENNEKO_PROCESS_CANARY='synthetic-host-only-canary';
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({process:true})})).status,204);
const [processJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-isolated-process'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:processJob.id,orgId,runId:processRun.id,threadId:processThread.id,message:'Run a credential-isolated script on the uploaded lead file.'},{retryLimit:0});
await waitForJob(processJob.id,processRun.id);
delete process.env.OPENNEKO_PROCESS_CANARY;
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
// Exercise bounded multi-megabyte publication through the same queued Work
// path and later download the exact bytes over the public artifact route.
const largeProcessThread=await createWorkThread(orgId,'M5 large isolated artifact','web',soloAdmin.id);
const largeProcessRun=await createWorkRun(orgId,largeProcessThread.id,'harness',{userId:null,role:'service'});
const largeProcessWorkspace=await ensureWorkWorkspace(orgId,largeProcessThread.id,largeProcessRun.id);
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({process_large:true})})).status,204);
const [largeProcessJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-large-isolated-process'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:largeProcessJob.id,orgId,runId:largeProcessRun.id,threadId:largeProcessThread.id,message:'Create the large isolated CSV artifact.'},{retryLimit:0});
await waitForJob(largeProcessJob.id,largeProcessRun.id);
const largeBytes=await readFile(join(largeProcessWorkspace.artifactRoot,'process-1','large.bin'));
assert.equal(largeBytes.length,8<<20);
assert.ok(largeBytes.every(byte=>byte===65));
const largeReceipt=(await pool().query('SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,largeProcessRun.id])).rows[0].result;
assert.equal(largeReceipt.ok,true);
assert.equal(largeReceipt.files[0].sha256,createHash('sha256').update(largeBytes).digest('hex'));
assert.deepEqual((await pool().query("SELECT payload->'artifact'->>'path' AS path FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,largeProcessRun.id])).rows.map(row=>row.path),[`runs/${largeProcessRun.id}/artifacts/process-1/large.bin`]);
if (process.env.HARNESS_M3_WEB === '1' || process.env.HARNESS_M3_API_HTTP === '1') await writeFile(join(process.env.HARNESS_STATE!,'m5-large-process-run'),largeProcessRun.id);
console.log('M5_QUEUE_PROCESS_LARGE_PASS',largeProcessRun.id);
// A subprocess may write bytes and then fail. Those bytes must never become a
// Work artifact, and its ambiguous host operation must not be replayed.
const failedProcessThread=await createWorkThread(orgId,'M5 failed isolated process','web',soloAdmin.id);
const failedProcessRun=await createWorkRun(orgId,failedProcessThread.id,'harness',{userId:null,role:'service'});
const failedProcessWorkspace=await ensureWorkWorkspace(orgId,failedProcessThread.id,failedProcessRun.id);
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({process_fail:true})})).status,204);
const [failedProcessJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-failed-isolated-process'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:failedProcessJob.id,orgId,runId:failedProcessRun.id,threadId:failedProcessThread.id,message:'Run the failing isolated process fixture.'},{retryLimit:0});
await waitForJob(failedProcessJob.id,failedProcessRun.id,'failed');
assert.equal((await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='artifact'",[orgId,failedProcessRun.id])).rows[0].n,0);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2 AND result IS NULL',[orgId,failedProcessRun.id])).rows[0].n,1);
await assert.rejects(readFile(join(failedProcessWorkspace.artifactRoot,'process-1','result.csv')),{code:'ENOENT'});
console.log('M5_QUEUE_PROCESS_FAILURE_PASS',failedProcessRun.id);
// Read-only management MCP catalogs use the same Work actor and broker token;
// none of these list tools grants its adjacent request/save/delete route.
await db().insert(action_policy).values({org_id:orgId,name:'harness-management-rule',mode:'approval_required',applies_to_kinds:['harness_management_fixture'],applies_to_scopes:['external']});
const managementThread=await createWorkThread(orgId,'M5 management catalogs','web',soloAdmin.id);
const managementRun=await createWorkRun(orgId,managementThread.id,'harness',{userId:null,role:'service'});
const managementWorkspace=await ensureWorkWorkspace(orgId,managementThread.id,managementRun.id);
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({management:true})})).status,204);
const [managementJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-management-reads'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:managementJob.id,orgId,runId:managementRun.id,threadId:managementThread.id,message:'Inspect the organization management catalogs without changing anything.'},{retryLimit:0});
await waitForJob(managementJob.id,managementRun.id);
const managementSnapshot=JSON.parse(await readFile(join(managementWorkspace.runRoot,'.harness',`${createHash('sha256').update(managementRun.id).digest('hex')}.json`),'utf8'));
assert.deepEqual(managementSnapshot.operations.map((op:{tool:string})=>op.tool),[
  'mcp_neko_user_manager_list_users','mcp_neko_user_manager_list_groups',
  'mcp_neko_data_source_manager_list_data_sources','mcp_neko_rule_builder_list_rules',
  'mcp_neko_plugin_manager_list_plugins','mcp_neko_channel_manager_list_channels',
]);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,managementRun.id])).rows[0].n,0);
console.log('M5_QUEUE_MANAGEMENT_READ_PASS',managementRun.id);
// Audit is visible only to the bound admin actor. The same queued MCP call
// from a member must receive a denial without the action-request content.
const auditThread=await createWorkThread(orgId,'M5 audit trail','web',soloAdmin.id);
const adminAuditRun=await createWorkRun(orgId,auditThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
await createActionRequest({orgId,scope:'internal',kind:'user_admin',target:'harness-audit-target',status:'pending_approval',summary:'harness-audit-marker',workRunId:adminAuditRun.id});
for (const [role,run] of [
  ['admin',adminAuditRun],
  ['member',await createWorkRun(orgId,auditThread.id,'harness',{userId:null,role:'member'})],
] as const) {
  const workspace=await ensureWorkWorkspace(orgId,auditThread.id,run.id);
  assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({audit:true,audit_denied:role==='member'})})).status,204);
  const [job]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:`test-audit-${role}`}).returning();
  await enqueue(QUEUE.WORK_RUN,{processingJobId:job.id,orgId,runId:run.id,threadId:auditThread.id,message:'Inspect the audit trail for the current actor.'},{retryLimit:0});
  await waitForJob(job.id,run.id);
  const snapshot=JSON.parse(await readFile(join(workspace.runRoot,'.harness',`${createHash('sha256').update(run.id).digest('hex')}.json`),'utf8'));
  assert.deepEqual(snapshot.operations.map((op:{tool:string})=>op.tool),['mcp_neko_audit_audit_trail']);
  assert.equal((await pool().query('SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,run.id])).rows[0].n,0);
  console.log(`M5_QUEUE_AUDIT_${role.toUpperCase()}_PASS`,run.id);
}
if (process.env.HARNESS_M3_API_HTTP === '1') {
  // The real Work upload endpoint writes the file and admits extraction.
  // The fixture worker performs the same extraction/distillation jobs with a
  // deterministic librarian response, then a queued Ax turn searches it.
  const uploadThread=await createWorkThread(orgId,'M5 uploaded library search','web',soloAdmin.id);
  process.env.NEKO_EMBEDDING_URL='http://127.0.0.1:18118';
  const uploadBody=new FormData();
  uploadBody.append('threadId',uploadThread.id);
  uploadBody.append('file',new File(['# Lead policy\n\nUPLOAD-LIBRARY-42 is the verified uploaded-document marker.\n'],'lead-policy.md',{type:'text/markdown'}));
  const uploadResponse=await fetch('http://127.0.0.1:18121/api/work/upload',{method:'POST',body:uploadBody});
  assert.equal(uploadResponse.status,200,await uploadResponse.text());
  let uploadedDocument:{id:string;status:string;user_id:string|null;relative_path:string}|undefined;
  for (let n=0;n<120;n++) {
    uploadedDocument=(await pool().query('SELECT id,status,user_id,relative_path FROM library_document WHERE org_id=$1 AND source_thread_id=$2 ORDER BY created_at DESC LIMIT 1',[orgId,uploadThread.id])).rows[0];
    if (uploadedDocument?.status==='cataloged') break;
    if (uploadedDocument?.status==='failed') throw Error('uploaded library document failed');
    await new Promise(resolve=>setTimeout(resolve,500));
  }
  assert.equal(uploadedDocument?.status,'cataloged','uploaded library document did not finish');
  assert.ok(uploadedDocument);
  assert.equal(uploadedDocument.user_id,soloAdmin.id);
  assert.match(uploadedDocument.relative_path,/^uploads\//);
  const concepts=(await pool().query('SELECT id,body,source_document_id FROM library_concept WHERE org_id=$1 AND source_document_id=$2',[orgId,uploadedDocument.id])).rows;
  assert.equal(concepts.length,1);
  assert.match(concepts[0].body,/UPLOAD-LIBRARY-42/);
  await dispatchEmbeddingJobs();
  let indexed=false;
  for (let n=0;n<120;n++) {
    indexed=(await pool().query('SELECT embedding IS NOT NULL AS indexed FROM library_concept WHERE id=$1',[concepts[0].id])).rows[0]?.indexed===true;
    if (indexed) break;
    await new Promise(resolve=>setTimeout(resolve,500));
  }
  assert.equal(indexed,true,'uploaded library concept was not indexed');
  const uploadRun=await createWorkRun(orgId,uploadThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
  const uploadWorkspace=await ensureWorkWorkspace(orgId,uploadThread.id,uploadRun.id);
  assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({uploaded_library:true})})).status,204);
  const [uploadJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-uploaded-library-search'}).returning();
  await enqueue(QUEUE.WORK_RUN,{processingJobId:uploadJob.id,orgId,runId:uploadRun.id,threadId:uploadThread.id,message:'Find UPLOAD-LIBRARY-42 in the uploaded policy through the library.'},{retryLimit:0});
  await waitForJob(uploadJob.id,uploadRun.id);
  const uploadSnapshot=JSON.parse(await readFile(join(uploadWorkspace.runRoot,'.harness',`${createHash('sha256').update(uploadRun.id).digest('hex')}.json`),'utf8'));
  assert.deepEqual(uploadSnapshot.operations.map((op:{tool:string})=>op.tool),['mcp_library_search']);
  assert.equal((await pool().query('SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,uploadRun.id])).rows[0].n,0);
  console.log('M5_QUEUE_UPLOADED_LIBRARY_PASS',uploadRun.id);
}
// Source metadata is exposed only when the operator enables the feature and
// the bound Work actor is a current admin. No import, config-agent or proposal
// endpoint is part of this Harness read grant.
await db().insert(llm_provider_config).values({org_id:orgId,scope:'graphjin-config',provider:'graphjin-config',enabled:true,config:{sourceConfigEnabled:true},secrets:{}});
await db().insert(data_source_secret).values({org_id:orgId,name:'SYNTHETIC_DB',value_enc:'fixture-encrypted-value',description:'Synthetic source credential name'});
const sourceSpec='openapi: 3.0.0\ninfo:\n  title: Fixture source API\n  version: 1.0.0\npaths: {}\n';
await db().insert(openapi_spec_asset).values({org_id:orgId,source_type:'upload',original_name:'fixture-api.yaml',content:sourceSpec,checksum_sha256:createHash('sha256').update(sourceSpec).digest('hex'),title:'Fixture source API',base_url:'https://fixture.invalid'});
const sourceThread=await createWorkThread(orgId,'M5 source configuration reads','web',soloAdmin.id);
const sourceRun=await createWorkRun(orgId,sourceThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
const sourceMemberRun=await createWorkRun(orgId,sourceThread.id,'harness',{userId:null,role:'member'});
assert.match(JSON.stringify(await inProcessControlPlane.listSourceSecretNames({orgId,runId:sourceMemberRun.id})),/denied/);
const sourceWorkspace=await ensureWorkWorkspace(orgId,sourceThread.id,sourceRun.id);
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({source_config:true})})).status,204);
const [sourceJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-source-config-reads'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:sourceJob.id,orgId,runId:sourceRun.id,threadId:sourceThread.id,message:'Inspect source graph and registered source metadata without changing configuration.'},{retryLimit:0});
await waitForJob(sourceJob.id,sourceRun.id);
const sourceSnapshot=JSON.parse(await readFile(join(sourceWorkspace.runRoot,'.harness',`${createHash('sha256').update(sourceRun.id).digest('hex')}.json`),'utf8'));
assert.deepEqual(sourceSnapshot.operations.map((op:{tool:string})=>op.tool),[
  'mcp_neko_source_config_manager_describe_source_graph',
  'mcp_neko_source_config_manager_list_source_secret_names',
  'mcp_neko_source_config_manager_list_openapi_specs',
]);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,sourceRun.id])).rows[0].n,0);
console.log('M5_QUEUE_SOURCE_CONFIG_READ_PASS',sourceRun.id);
// Workflow writes use a separate journaled grant. Creating requires an absent
// precondition; editing requires the exact version returned by the list tool.
const workflowAuthorThread=await createWorkThread(orgId,'M5 workflow authoring','web',soloAdmin.id);
if (process.env.HARNESS_M3_WEB === '1') await writeFile(join(process.env.HARNESS_STATE!,'m5-workflow-thread'),workflowAuthorThread.id);
const workflowCreateRun=await createWorkRun(orgId,workflowAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({workflow_save:true})})).status,204);
const [workflowCreateJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-workflow-create'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:workflowCreateJob.id,orgId,runId:workflowCreateRun.id,threadId:workflowAuthorThread.id,message:'Create a disabled daily Harness review workflow with one step and a lead_id CSV column.'},{retryLimit:0});
await waitForJob(workflowCreateJob.id,workflowCreateRun.id);
const [createdWorkflow]=(await pool().query('SELECT id,description,cron,cron_enabled,output_contract,xmin::text AS version_token FROM workflow_definition WHERE org_id=$1 AND name=$2',[orgId,'Harness review workflow'])).rows;
assert.equal(createdWorkflow.description,'Review synthetic leads');
assert.equal(createdWorkflow.cron,'0 9 * * *');
assert.equal(createdWorkflow.cron_enabled,false);
assert.equal(createdWorkflow.output_contract.apiBatch.columns[0].name,'lead_id');
const createdCard=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='surface'",[orgId,workflowCreateRun.id])).rows;
assert.deepEqual(createdCard.map(row=>row.payload.messages[0].createSurface.surfaceId),[`workflow-save-${createdWorkflow.id}`]);
const createdReceipt=(await pool().query('SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,workflowCreateRun.id])).rows;
assert.equal(createdReceipt.length,1);
assert.deepEqual({ok:createdReceipt[0].result.ok,action:createdReceipt[0].result.action},{ok:true,action:'created'});
console.log('M5_QUEUE_WORKFLOW_CREATE_PASS',workflowCreateRun.id);
const workflowEditRun=await createWorkRun(orgId,workflowAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({workflow_edit:true})})).status,204);
const [workflowEditJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-workflow-edit'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:workflowEditJob.id,orgId,runId:workflowEditRun.id,threadId:workflowAuthorThread.id,message:'Update the Harness review workflow description and step after reading its current version.'},{retryLimit:0});
await waitForJob(workflowEditJob.id,workflowEditRun.id);
const [editedWorkflow]=(await pool().query('SELECT id,description,steps,xmin::text AS version_token FROM workflow_definition WHERE org_id=$1 AND name=$2',[orgId,'Harness review workflow'])).rows;
assert.equal(editedWorkflow.id,createdWorkflow.id);
assert.equal(editedWorkflow.description,'Reviewed synthetic leads');
assert.equal(editedWorkflow.steps[0].description,'Check the lead owner and status');
assert.notEqual(editedWorkflow.version_token,createdWorkflow.version_token);
const editCard=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='surface'",[orgId,workflowEditRun.id])).rows;
assert.deepEqual(editCard.map(row=>row.payload.messages[0].createSurface.surfaceId),[`workflow-save-${createdWorkflow.id}`]);
await assert.rejects(inProcessControlPlane.saveWorkflowWithTrigger({orgId,createdByRunId:workflowEditRun.id,name:'Harness review workflow',steps:[{id:'stale',description:'Should not replace the newer step'}],expectedVersion:createdWorkflow.version_token}),/changed since it was listed/);
assert.equal((await pool().query('SELECT description FROM workflow_definition WHERE id=$1',[createdWorkflow.id])).rows[0].description,'Reviewed synthetic leads');
console.log('M5_QUEUE_WORKFLOW_EDIT_PASS',workflowEditRun.id);
async function queueTriggerSave(mode:string,message:string) {
  const run=await createWorkRun(orgId,workflowAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
  assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({[mode]:true})})).status,204);
  const [job]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:`test-${mode}`}).returning();
  await enqueue(QUEUE.WORK_RUN,{processingJobId:job.id,orgId,runId:run.id,threadId:workflowAuthorThread.id,message},{retryLimit:0});
  await waitForJob(job.id,run.id);
  const receipts=(await pool().query('SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,run.id])).rows;
  assert.equal(receipts.length,1);
  return {run,receipt:receipts[0].result};
}
const sourceTriggerCreate=await queueTriggerSave('workflow_when','Create a workflow that responds when the seeded reference changes.');
assert.equal(sourceTriggerCreate.receipt.ok,true);
const [sourceTriggerWorkflow]=(await pool().query('SELECT id FROM workflow_definition WHERE org_id=$1 AND name=$2',[orgId,'Harness source-change workflow'])).rows;
const [sourceTrigger]=(await pool().query("SELECT id,filter FROM subscription WHERE org_id=$1 AND workflow_id=$2 AND source_kind='source_change'",[orgId,sourceTriggerWorkflow.id])).rows;
assert.equal(sourceTrigger.id,sourceTriggerCreate.receipt.triggerId);
assert.deepEqual(sourceTrigger.filter.primary_key,['id']);
const sourceProbe=buildSourceChangeDryRunQuery(sourceTrigger.filter,1);
assert.ok(sourceProbe);
const sourceRead=await inProcessControlPlane.queryGraphjinRead({orgId,runId:sourceTriggerCreate.run.id,query:sourceProbe.query,variables:sourceProbe.variables});
const sourceMatch=parseSourceChangeMatch(sourceRead,parseSourceChangeFilter(sourceTrigger.filter)!);
assert.ok(sourceMatch);
assert.deepEqual(sourceMatch.primary_key,{id:42});
console.log('M5_QUEUE_WORKFLOW_WHEN_PASS',sourceTriggerCreate.run.id);
const sourceTriggerEdit=await queueTriggerSave('workflow_when_edit','Narrow the source-change workflow to reference 42 after checking its current revision.');
assert.equal(sourceTriggerEdit.receipt.ok,true);
assert.equal(sourceTriggerEdit.receipt.triggerId,sourceTrigger.id);
const editedTriggers=(await pool().query("SELECT id,filter FROM subscription WHERE org_id=$1 AND workflow_id=$2 AND source_kind='source_change'",[orgId,sourceTriggerWorkflow.id])).rows;
assert.equal(editedTriggers.length,1);
assert.equal(editedTriggers[0].filter.where.id.eq,42);
const listedSource=(await inProcessControlPlane.listWorkflowsWithTriggers({orgId,runId:sourceTriggerEdit.run.id})).workflows.find(item=>item.id===sourceTriggerWorkflow.id);
assert.equal((listedSource?.when?.where as {id:{eq:number}}).id.eq,42);
console.log('M5_QUEUE_WORKFLOW_WHEN_EDIT_PASS',sourceTriggerEdit.run.id);
const watchCreate=await queueTriggerSave('workflow_watch','Watch the seeded reference query and alert when its id exceeds 40.');
assert.equal(watchCreate.receipt.ok,true);
const [savedWatch]=(await pool().query('SELECT id,workflow_id,query,value_path,op,threshold FROM watcher WHERE org_id=$1 AND name=$2',[orgId,'Harness condition watch'])).rows;
assert.equal(savedWatch.id,watchCreate.receipt.watcherId);
assert.equal(savedWatch.value_path,'references.0.id');
const listedWatch=(await inProcessControlPlane.listWorkflowsWithTriggers({orgId,runId:watchCreate.run.id})).workflows.find(item=>item.id===savedWatch.workflow_id);
assert.equal(listedWatch?.watch?.value_path,'references.0.id');
const watchFires:unknown[]=[];
const sweep=await sweepWatchers(orgId,{enqueueFire:async payload=>{watchFires.push(payload);}});
assert.ok(sweep.fired.some(item=>item.watcherId===savedWatch.id && item.value===42));
assert.equal(watchFires.length,1);
console.log('M5_QUEUE_WORKFLOW_WATCH_PASS',watchCreate.run.id);
const looped=await queueTriggerSave('workflow_bad_trigger','Try a workflow that updates the same source table it watches.');
assert.deepEqual({ok:looped.receipt.ok,error:looped.receipt.error},{ok:false,error:'mutation_loop'});
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_definition WHERE org_id=$1 AND name=$2',[orgId,'Harness looped workflow'])).rows[0].n,0);
assert.equal((await pool().query("SELECT count(*)::int AS n FROM subscription WHERE org_id=$1 AND workflow_id IN (SELECT id FROM workflow_definition WHERE name='Harness looped workflow')",[orgId])).rows[0].n,0);
await assert.rejects(inProcessControlPlane.saveWorkflowWithTrigger({orgId,createdByRunId:looped.run.id,name:'Harness missing-source workflow',steps:[{id:'report',description:'Report reference'}],triggers:{when:{table:'missing_reference_table',primary_key:['id']}},expectedVersion:'absent'}),/data-change filter did not pass GraphJin read preflight/);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_definition WHERE org_id=$1 AND name=$2',[orgId,'Harness missing-source workflow'])).rows[0].n,0);
await assert.rejects(inProcessControlPlane.saveWorkflowWithTrigger({orgId,createdByRunId:looped.run.id,name:'Harness missing-value watch',steps:[{id:'report',description:'Report reference'}],triggers:{watch:{query:'query { references { id label } }',value_path:'references.0.missing',op:'gt',threshold:40}},expectedVersion:'absent'}),/watcher value path or threshold is invalid/);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_definition WHERE org_id=$1 AND name=$2',[orgId,'Harness missing-value watch'])).rows[0].n,0);
await pool().query('UPDATE app_user SET disabled_at=now() WHERE org_id=$1 AND id=$2',[orgId,soloAdmin.id]);
try {
  await assert.rejects(inProcessControlPlane.saveWorkflowWithTrigger({orgId,createdByRunId:looped.run.id,name:'Harness disabled-author workflow',steps:[{id:'report',description:'Report reference'}],expectedVersion:'absent'}),/current enabled user/);
} finally {
  await pool().query('UPDATE app_user SET disabled_at=NULL WHERE org_id=$1 AND id=$2',[orgId,soloAdmin.id]);
}
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_definition WHERE org_id=$1 AND name=$2',[orgId,'Harness disabled-author workflow'])).rows[0].n,0);
console.log('M5_QUEUE_WORKFLOW_TRIGGER_ROLLBACK_PASS',looped.run.id);
const workflowConfirmation=`DELETE WORKFLOW ${JSON.stringify('Harness review workflow')} PERMANENTLY`;
const workflowDeleteDeniedRun=await createWorkRun(orgId,workflowAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
await createWorkMessage({orgId,threadId:workflowAuthorThread.id,runId:workflowDeleteDeniedRun.id,role:'user',content:'Delete the Harness review workflow.'});
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({workflow_delete_denied:true})})).status,204);
const [workflowDeleteDeniedJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-workflow-delete-denied'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:workflowDeleteDeniedJob.id,orgId,runId:workflowDeleteDeniedRun.id,threadId:workflowAuthorThread.id,message:'Delete the Harness review workflow.'},{retryLimit:0});
await waitForJob(workflowDeleteDeniedJob.id,workflowDeleteDeniedRun.id);
const deniedDeleteReceipt=(await pool().query('SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,workflowDeleteDeniedRun.id])).rows;
assert.deepEqual(deniedDeleteReceipt.map(row=>({ok:row.result.ok,error:row.result.error,requiredConfirmation:row.result.requiredConfirmation})),[{ok:false,error:'confirmation_required',requiredConfirmation:workflowConfirmation}]);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_definition WHERE id=$1',[createdWorkflow.id])).rows[0].n,1);
assert.equal((await pool().query("SELECT count(*)::int AS n FROM work_run_event WHERE run_id=$1 AND kind='surface'",[workflowDeleteDeniedRun.id])).rows[0].n,0);
console.log('M5_QUEUE_WORKFLOW_DELETE_DENIED_PASS',workflowDeleteDeniedRun.id);
const staleDeleteRun=await createWorkRun(orgId,workflowAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
await createWorkMessage({orgId,threadId:workflowAuthorThread.id,runId:staleDeleteRun.id,role:'user',content:workflowConfirmation});
assert.deepEqual(await inProcessControlPlane.deleteWorkflowForHarness({orgId,runId:staleDeleteRun.id,workflowId:createdWorkflow.id,name:'Harness review workflow',expectedVersion:createdWorkflow.version_token}),{ok:false,error:'workflow_changed'});
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_definition WHERE id=$1',[createdWorkflow.id])).rows[0].n,1);
const unlinkedDeleteRun=await createWorkRun(orgId,workflowAuthorThread.id,'harness',{userId:null,role:'member'});
await createWorkMessage({orgId,threadId:workflowAuthorThread.id,runId:unlinkedDeleteRun.id,role:'user',content:workflowConfirmation});
assert.deepEqual(await inProcessControlPlane.deleteWorkflowForHarness({orgId,runId:unlinkedDeleteRun.id,workflowId:createdWorkflow.id,name:'Harness review workflow',expectedVersion:editedWorkflow.version_token}),{ok:false,error:'actor_denied'});
await pool().query('UPDATE app_user SET disabled_at=now() WHERE org_id=$1 AND id=$2',[orgId,soloAdmin.id]);
try {
  assert.deepEqual(await inProcessControlPlane.deleteWorkflowForHarness({orgId,runId:staleDeleteRun.id,workflowId:createdWorkflow.id,name:'Harness review workflow',expectedVersion:editedWorkflow.version_token}),{ok:false,error:'actor_denied'});
} finally {
  await pool().query('UPDATE app_user SET disabled_at=NULL WHERE org_id=$1 AND id=$2',[orgId,soloAdmin.id]);
}
const [dependentRun]=await db().insert(workflow_run).values({org_id:orgId,workflow_id:createdWorkflow.id,thread_id:workflowAuthorThread.id,work_run_id:staleDeleteRun.id,trigger_kind:'manual'}).returning();
const [dependentSubscription]=(await pool().query("INSERT INTO subscription(org_id,workflow_id,source_kind,filter,enabled) VALUES($1,$2,'source_change','{}'::jsonb,false) RETURNING id",[orgId,createdWorkflow.id])).rows;
const workflowDeleteRun=await createWorkRun(orgId,workflowAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
await createWorkMessage({orgId,threadId:workflowAuthorThread.id,runId:workflowDeleteRun.id,role:'user',content:workflowConfirmation});
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({workflow_delete:true})})).status,204);
const [workflowDeleteJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-workflow-delete'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:workflowDeleteJob.id,orgId,runId:workflowDeleteRun.id,threadId:workflowAuthorThread.id,message:workflowConfirmation},{retryLimit:0});
await waitForJob(workflowDeleteJob.id,workflowDeleteRun.id);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_definition WHERE id=$1',[createdWorkflow.id])).rows[0].n,0);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM workflow_run WHERE id=$1',[dependentRun.id])).rows[0].n,0);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM subscription WHERE id=$1',[dependentSubscription.id])).rows[0].n,0);
const workflowDeleteReceipt=(await pool().query('SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,workflowDeleteRun.id])).rows;
assert.deepEqual(workflowDeleteReceipt.map(row=>({ok:row.result.ok,workflowId:row.result.workflowId})),[{ok:true,workflowId:createdWorkflow.id}]);
const workflowDeleteCards=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='surface'",[orgId,workflowDeleteRun.id])).rows;
assert.deepEqual(workflowDeleteCards.map(row=>row.payload.messages[0].createSurface.surfaceId),[`workflow-delete-${createdWorkflow.id}`]);
if (process.env.HARNESS_M3_WEB === '1') await writeFile(join(process.env.HARNESS_STATE!,'m5-workflow-delete-thread'),workflowAuthorThread.id);
console.log('M5_QUEUE_WORKFLOW_DELETE_PASS',workflowDeleteRun.id);
const ruleAuthorThread=await createWorkThread(orgId,'M5 rule authoring','web',soloAdmin.id);
if (process.env.HARNESS_M3_WEB === '1') await writeFile(join(process.env.HARNESS_STATE!,'m5-rule-thread'),ruleAuthorThread.id);
const ruleCreateRun=await createWorkRun(orgId,ruleAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({rule_save:true})})).status,204);
const [ruleCreateJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-rule-create'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:ruleCreateJob.id,orgId,runId:ruleCreateRun.id,threadId:ruleAuthorThread.id,message:'Create an approval-required rule for the synthetic fixture action.'},{retryLimit:0});
await waitForJob(ruleCreateJob.id,ruleCreateRun.id);
const [createdRule]=(await pool().query('SELECT id,description,mode,xmin::text AS version_token FROM action_policy WHERE org_id=$1 AND name=$2',[orgId,'Harness governed rule'])).rows;
assert.equal(createdRule.description,'Require review for synthetic changes');
assert.equal(createdRule.mode,'approval_required');
const createdRuleCards=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='surface'",[orgId,ruleCreateRun.id])).rows;
assert.deepEqual(createdRuleCards.map(row=>row.payload.messages[0].createSurface.surfaceId),[`policy-save-${createdRule.id}`]);
const createdRuleReceipt=(await pool().query('SELECT result FROM harness_operation WHERE org_id=$1 AND run_id=$2',[orgId,ruleCreateRun.id])).rows;
assert.deepEqual(createdRuleReceipt.map(row=>({ok:row.result.ok,action:row.result.action})),[{ok:true,action:'created'}]);
console.log('M5_QUEUE_RULE_CREATE_PASS',ruleCreateRun.id);
const ruleEditRun=await createWorkRun(orgId,ruleAuthorThread.id,'harness',{userId:soloAdmin.id,role:'admin'});
assert.equal((await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({rule_edit:true})})).status,204);
const [ruleEditJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:'test-rule-edit'}).returning();
await enqueue(QUEUE.WORK_RUN,{processingJobId:ruleEditJob.id,orgId,runId:ruleEditRun.id,threadId:ruleAuthorThread.id,message:'Read the current version, then allow auto-approval for only low-risk fixture rule actions.'},{retryLimit:0});
await waitForJob(ruleEditJob.id,ruleEditRun.id);
const [editedRule]=(await pool().query('SELECT id,description,mode,risk_threshold_auto_approve,applies_to_kinds,applies_to_scopes,limits,xmin::text AS version_token FROM action_policy WHERE org_id=$1 AND name=$2',[orgId,'Harness governed rule'])).rows;
assert.equal(editedRule.id,createdRule.id);
assert.equal(editedRule.description,'Auto-approve synthetic low-risk changes');
assert.equal(editedRule.mode,'auto_approve');
assert.equal(editedRule.risk_threshold_auto_approve,'low');
assert.deepEqual(editedRule.applies_to_kinds,['fixture_rule_action']);
assert.deepEqual(editedRule.applies_to_scopes,['external']);
assert.equal(editedRule.limits.daily_cap,2);
assert.notEqual(editedRule.version_token,createdRule.version_token);
const editedRuleCards=(await pool().query("SELECT payload FROM work_run_event WHERE org_id=$1 AND run_id=$2 AND kind='surface'",[orgId,ruleEditRun.id])).rows;
assert.deepEqual(editedRuleCards.map(row=>row.payload.messages[0].createSurface.surfaceId),[`policy-save-${createdRule.id}`]);
const guardedRule={orgId,name:'Harness governed rule',description:'Should not replace newer policy',appliesToKinds:['fixture_rule_action'],appliesToScopes:['external'] as const,mode:'never' as const,riskThresholdAutoApprove:null,allowedTargets:null,deniedTargets:null,limits:{},priority:100,enabled:true};
await assert.rejects(inProcessControlPlane.upsertActionPolicyByName({...guardedRule,appliesToScopes:['external'],expectedVersion:createdRule.version_token,createdByRunId:ruleEditRun.id}),/changed since it was listed/);
const ruleMemberRun=await createWorkRun(orgId,ruleAuthorThread.id,'harness',{userId:null,role:'member'});
await assert.rejects(inProcessControlPlane.upsertActionPolicyByName({...guardedRule,appliesToScopes:['external'],expectedVersion:editedRule.version_token,createdByRunId:ruleMemberRun.id}),/current admin actor/);
await pool().query('UPDATE app_user SET disabled_at=now() WHERE org_id=$1 AND id=$2',[orgId,soloAdmin.id]);
try {
  await assert.rejects(inProcessControlPlane.upsertActionPolicyByName({...guardedRule,appliesToScopes:['external'],expectedVersion:editedRule.version_token,createdByRunId:ruleEditRun.id}),/current admin actor/);
} finally {
  await pool().query('UPDATE app_user SET disabled_at=NULL WHERE org_id=$1 AND id=$2',[orgId,soloAdmin.id]);
}
const concurrentRule={...guardedRule,name:'Harness concurrent rule',description:'One create-only winner',appliesToScopes:['external'] as ('internal'|'external')[],expectedVersion:'absent',createdByRunId:ruleEditRun.id};
const concurrentSaves=await Promise.allSettled([
  inProcessControlPlane.upsertActionPolicyByName(concurrentRule),
  inProcessControlPlane.upsertActionPolicyByName(concurrentRule),
]);
assert.deepEqual(concurrentSaves.map(result=>result.status).sort(),['fulfilled','rejected']);
const rejectedConcurrent=concurrentSaves.find((result):result is PromiseRejectedResult=>result.status==='rejected');
assert.match(String(rejectedConcurrent?.reason),/changed since it was listed/);
assert.equal((await pool().query('SELECT count(*)::int AS n FROM action_policy WHERE org_id=$1 AND name=$2',[orgId,'Harness concurrent rule'])).rows[0].n,1);
assert.equal((await pool().query('SELECT mode FROM action_policy WHERE id=$1',[createdRule.id])).rows[0].mode,'auto_approve');
console.log('M5_QUEUE_RULE_EDIT_PASS',ruleEditRun.id);
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
    const largeBatch=httpApi && process.env.HARNESS_M6_LARGE_BATCH==='1';
    const apiTargetDay=largeBatch?'2026-09-16':'2026-09-15';
    const expectedApiBytes=Buffer.from(`reference\r\n${'REF-42\r\n'.repeat(largeBatch?1_000_000:1)}`);
    const apiBase='http://localhost:18121';
    const admitted=httpApi
      ? await (async()=>{
          const response=await fetch(`${apiBase}/api/v1/workflows/${realWorkflow.id}/runs`,{
            method:'POST',headers:{'content-type':'application/json',authorization:`Bearer ${token}`,
              'idempotency-key':'m5-graphjin-api-batch'},body:JSON.stringify({targetDay:apiTargetDay})});
          if(response.status!==202) throw new Error(`Workflow HTTP admission returned ${response.status}: ${await response.text()}`);
          return response.json() as Promise<{runId:string;statusUrl:string}>;
        })()
      : await admitWorkflowApiRun({workflowId:realWorkflow.id,token,idempotencyKey:'m5-graphjin-api-batch',
          mode:'single',value:{targetDay:apiTargetDay},clientFingerprint:`m5-${orgId}`});
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
    const savedApiBytes=await readFile(join(apiWorkspace.artifactRoot,'references.csv'));
    assert.equal(savedApiBytes.length,expectedApiBytes.length);
    assert.equal(createHash('sha256').update(savedApiBytes).digest('hex'),
      createHash('sha256').update(expectedApiBytes).digest('hex'));
    const [apiProgress]=(await db().select({progress:workflow_run.progress}).from(workflow_run)
      .where(eq(workflow_run.id,admitted.runId)));
    assert.deepEqual(apiProgress?.progress,{stage:'completed',rows:largeBatch?1_000_000:1,queries:1,
      artifactBytes:expectedApiBytes.length});
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
      assert.equal(download.headers.get('content-length'),String(expectedApiBytes.length));
      assert.equal(download.headers.get('content-disposition'),`attachment; filename="workflow-${admitted.runId}.csv"`);
      const downloaded=Buffer.from(await download.arrayBuffer());
      assert.equal(downloaded.length,expectedApiBytes.length);
      assert.equal(createHash('sha256').update(downloaded).digest('hex'),
        createHash('sha256').update(expectedApiBytes).digest('hex'));
      console.log('M5_HTTP_API_BATCH_GRAPHJIN_PASS',admitted.runId);
      if(largeBatch)console.log('M6_HTTP_API_BATCH_LARGE_PASS',JSON.stringify({runId:admitted.runId,
        bytes:downloaded.length,rows:1_000_000,queries:1}));
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
// This driver splits Work admission and action execution into two processes.
// Production registers executors in the worker before admitting a Harness
// action; mirror that availability here while the separate approval worker
// owns the controlled HTTP effect.
registerActionAdapter(approvalKind, async () => { throw Error('approval effect belongs to the separate worker'); }, 'pack');
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
crashRunIdForDiagnostics=crashRun.id;
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
    // the fixture's paused responder request.
    let expired=false;
    for(let n=0;n<90;n++) {
        await queue.maintain();
        const state=await queue.getJobById(QUEUE.WORK_RUN,queueId);
        if(state?.state==='retry') {expired=true;break;}
        assert.equal(state?.state,'active');
        await new Promise(r=>setTimeout(r,1000));
    }
    assert.ok(expired,'abandoned queue job did not become retryable');
    // Keep model counters while allowing a bounded continuation after the
    // canceled responder stream. The committed lookup must never run again.
    await fetch('http://127.0.0.1:18118/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({continue:true})});
    await startWorker();
    await waitForJob(crashJob.id,crashRun.id);
    const recovered=(await pool().query('SELECT accepted_context,result FROM harness_run_journal WHERE org_id=$1 AND run_id=$2',[orgId,crashRun.id])).rows[0];
    assert.deepEqual(recovered.accepted_context,beforeCrash.accepted_context);
    assert.equal(recovered.result.status,'completed');
    assert.match(recovered.result.finalText,/REF-42/);
    const afterCalls=await (await fetch('http://127.0.0.1:18118/control')).json();
    assert.equal(afterCalls['graphjin-fixture'],crashCalls['graphjin-fixture'],'recovery must not repeat GraphJin work');
    assert.ok(afterCalls['harness-fixture']===crashCalls['harness-fixture'] ||
      afterCalls['harness-fixture']===crashCalls['harness-fixture']+3,'recovery may repeat only one bounded model turn');
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
