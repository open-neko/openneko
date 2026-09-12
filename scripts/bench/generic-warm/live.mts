// Demo-only live benchmark. Cache principals/revisions are synthetic; RBAC is not simulated.
import { writeFile } from 'node:fs/promises';
import { pool } from '@neko/db';
import { resolveAgentBackend, ensureHostConfigProvisioned } from '@neko/llm';
import { ensureAgentBroker, shutdownAgentBroker, runChatTurn, createWorkThread, createWorkRun, createWorkMessage, registerAgentBrokerEventSink } from '@neko/llm/work';
import { makeSandboxRunCore, sandboxLauncherOptionsFromConfig, closeSandboxPools } from './sandbox-launcher.mjs';
const results:any[]=[];
const logs:any[]=[];
const orgId=process.env.OPENNEKO_BENCH_ORG ?? 'adventureworks';
const actor={userId:null,role:'admin' as const};
const prompt='Read the connected AdventureWorks demo data and report the total number of sales orders. Use an actual read-only data query. Do not modify data or create schedules. Keep the answer brief.';
try {
 const backend=await resolveAgentBackend(orgId);
 const config=await ensureHostConfigProvisioned(orgId);
 const broker=await ensureAgentBroker();
 const options={...sandboxLauncherOptionsFromConfig(config,broker),agentImage:process.env.OPENNEKO_BENCH_IMAGE ?? 'ghcr.io/open-neko/agent:warm-uncommitted',memory:'768Mi',cpu:'2',warmIdleMs:180000,onLog:(line:string)=>{try{const event=JSON.parse(line);logs.push(event); console.log(JSON.stringify(event));}catch{console.log(line);}}};
 const cold=makeSandboxRunCore({...options,warmPoolSize:0});
 const warm=makeSandboxRunCore({...options,warmPoolSize:1});
 const shared=await createWorkThread(orgId,'Uncommitted warm session test','web');
 for(const mode of ['cold','generic','other-chat','changed-scope']) {
  const thread=mode==='cold'||mode==='other-chat'?await createWorkThread(orgId,'Uncommitted cold baseline','web'):shared;
  const run=await createWorkRun(orgId,thread.id,backend.id,actor);
  await createWorkMessage({orgId,threadId:thread.id,runId:run.id,role:'user',content:prompt});
  const start=performance.now();let firstOutput:number|undefined;let text='';
  const emit=async(event:any)=>{if((event.type==='message'&&event.role==='assistant')||event.type==='output_emit'||event.type==='surface')firstOutput??=performance.now()-start; if(event.type==='message'&&event.role==='assistant')text+=event.content;};
  const unregister=registerAgentBrokerEventSink(run.id,emit);
  console.log(JSON.stringify({test_start:mode,runId:run.id}));
  let result:any;
  try {result=await runChatTurn({orgId,threadId:thread.id,runId:run.id,message:prompt,channel:'web',emit,pluginActions:[],packActions:[]},{runCore:(input:any)=>(mode==='cold'?cold:warm)({...input,sandboxUser:{principalId:'demo-test-fixture',authorizationRevision:mode==='changed-scope'?'test-v2':'test-v1'}})});}
  finally{unregister();}
  const item={mode,runId:run.id,status:result.status,error:result.error,totalMs:performance.now()-start,firstOutputMs:firstOutput,answerValid:/31,?465/.test(text+' '+result.finalText),phases:logs.filter(event=>event.runId===run.id)};
  results.push(item); await writeFile('/app/warm-test/results.json',JSON.stringify(results,null,2));console.log(JSON.stringify({test_result:item}));
  if(result.status!=='completed'||!item.answerValid)throw new Error('live query failed');
 }
 await Promise.all([1,2,3].map(async user => {
  const thread=await createWorkThread(orgId,'Concurrent user warm test '+user,'web');
  const run=await createWorkRun(orgId,thread.id,backend.id,actor);
  await createWorkMessage({orgId,threadId:thread.id,runId:run.id,role:'user',content:prompt});
  const start=performance.now();let firstOutput:number|undefined;let text='';
  const emit=async(event:any)=>{if((event.type==='message'&&event.role==='assistant')||event.type==='output_emit'||event.type==='surface')firstOutput??=performance.now()-start;if(event.type==='message'&&event.role==='assistant')text+=event.content;};
  const unregister=registerAgentBrokerEventSink(run.id,emit);
  console.log(JSON.stringify({test_start:'concurrent-'+user,runId:run.id,wallMs:Date.now()}));
  try {
   const result=await runChatTurn({orgId,threadId:thread.id,runId:run.id,message:prompt,channel:'web',emit,pluginActions:[],packActions:[]},{runCore:(input:any)=>warm({...input,sandboxUser:{principalId:'demo-concurrent-'+user,authorizationRevision:'test-v1'}})});
   const item={mode:'concurrent-'+user,runId:run.id,status:result.status,totalMs:performance.now()-start,firstOutputMs:firstOutput,answerValid:/31,?465/.test(text+' '+result.finalText),phases:logs.filter(event=>event.runId===run.id)};
   results.push(item);console.log(JSON.stringify({test_result:item}));
   if(result.status!=='completed'||!item.answerValid)throw new Error('concurrent live query failed');
  } finally {unregister();}
 }));
 await writeFile('/app/warm-test/results.json',JSON.stringify(results,null,2));
 console.log(JSON.stringify({idle_wait_start:Date.now(),seconds:185}));
 await new Promise(resolve=>setTimeout(resolve,185000));
 console.log(JSON.stringify({idle_wait_end:Date.now()}));

} finally {
 await closeSandboxPools();
 await shutdownAgentBroker();
 await pool().end();
}
