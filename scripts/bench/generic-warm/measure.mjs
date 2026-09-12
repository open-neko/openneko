import {spawn} from 'node:child_process';
import {createInterface} from 'node:readline';
import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,existsSync} from 'node:fs';
import assert from 'node:assert/strict';
const variant=process.argv[2]; const sample=Number(process.argv[3]);
const home=mkdtempSync('/tmp/generic-warm-');const hermesHome=home+'/hermes';mkdirSync(hermesHome);
const env={PATH:process.env.PATH,HOME:home,HERMES_HOME:hermesHome,PYTHONUNBUFFERED:'1',HERMES_DISABLE_LAZY_INSTALLS:'1',HERMES_ACP_SKIP_CONFIGURED_MCP:'1',PRELOAD_REPORT:home+'/preload.json'};
assert(!existsSync(hermesHome+'/config.yaml'));
const now=()=>performance.now();const start=now();
const child=spawn(variant==='stock'?'hermes':'/usr/local/uv/tools/hermes-agent/bin/python',variant==='stock'?['--yolo','acp']:['/bench/preload.py'],{cwd:home,env,stdio:['pipe','pipe','pipe']});
let seq=0;const pending=new Map();let stderr='';child.stderr.on('data',b=>{stderr+=b;});
const rl=createInterface({input:child.stdout});
rl.on('line',line=>{try{const f=JSON.parse(line);const p=pending.get(f.id);if(p){pending.delete(f.id);f.error?p.reject(Error(JSON.stringify(f.error))):p.resolve(f.result);}}catch{}});
function request(method,params){return new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});child.stdin.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');});}
function memory(){const current=Number(readFileSync('/sys/fs/cgroup/memory.current','utf8'));const stat=Object.fromEntries(readFileSync('/sys/fs/cgroup/memory.stat','utf8').trim().split('\n').map(l=>l.split(' ')));return {current_bytes:current,working_set_bytes:current-Number(stat.inactive_file),peak_bytes:Number(readFileSync('/sys/fs/cgroup/memory.peak','utf8'))};}
const timeout=setTimeout(()=>{console.error('benchmark timeout');child.kill('SIGKILL');process.exitCode=1;},60000);
try{
 const initialized=await request('initialize',{protocolVersion:1,clientCapabilities:{fs:{readTextFile:false,writeTextFile:false}}});assert(initialized.protocolVersion);
 const warmAt=now();const idle=[];
 for(let i=0;i<5;i++){await new Promise(r=>setTimeout(r,1000));idle.push(memory());}
 assert(!existsSync(hermesHome+'/config.yaml'),'model config must be absent throughout prewarm');
 const workspace=home+'/request-'+sample;mkdirSync(workspace);
 const model='offline-checkout-'+sample;
 const config={model:{default:model,provider:'offline-bench',context_length:256000},providers:{'offline-bench':{base_url:'http://127.0.0.1:9/v1',api_key:'not-a-real-key',default_model:model}},agent:{max_turns:1},delegation:{orchestrator_enabled:false}};
 writeFileSync(hermesHome+'/config.yaml',JSON.stringify(config));
 const withMcp=sample%2===1;
 const checkout=now();
 const session=await request('session/new',{cwd:workspace,mcpServers:withMcp?[{name:'late-bound',command:'node',args:['/bench/fixture.mjs','late'],env:[]}]:[]});
 const ready=now();
 assert(session.sessionId);assert(JSON.stringify(session.models).includes(model),'late model selection not observed');
 const preload=existsSync(home+'/preload.json')?JSON.parse(readFileSync(home+'/preload.json','utf8')):null;
 if(preload){assert(preload.has_terminal);assert(!preload.scoped_tool_names.some(n=>n.startsWith('mcp')));}
 console.log(JSON.stringify({variant,sample,withMcp,initialize_ms:warmAt-start,idle,checkout_session_ms:ready-checkout,post_checkout_memory:memory(),model_verified:true,no_config_before_checkout:true,preload}));
}catch(e){console.error(String(e));console.error(stderr.slice(-2500));process.exitCode=1;}
finally{clearTimeout(timeout);child.kill('SIGTERM');rl.close();setTimeout(()=>child.kill('SIGKILL'),1000).unref();}
