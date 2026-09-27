import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import pg from "pg";
import { db, pool, eq, organization, work_thread, work_run, app_state, action_policy, llm_provider_config, processing_job } from "@neko/db";
import { boss, enqueue, QUEUE, type WorkRunPayload } from "@neko/db/jobs";
import { buildRecordsPoolConfig, runRecordsMigrations } from "@neko/db/records-migrate";
import { deleteTestOrg } from "@neko/db/test-helpers";
import {
  loadRecordsGraphjinPolicyModel, projectRecordsGraphjinRoles,
  recordsGraphjinSigningSecret, recordsGraphjinCursorSecret,
  writeRecordsGraphjinConfig, ensureRecordsAuditTrigger,
  RecordsGraphjinClient, RecordsGraphjinRequestError, RecordWriteExecutor, mintRecordsGraphjinToken,
} from "@neko/records";
import { expect, it } from "vitest";
import type { AgentWorkspace } from "../src/agent-backend";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";
import { createWorkRun, createWorkThread, ensureWorkWorkspace, getWorkRun, shutdownAgentBroker } from "../src/work";
import { runWorkRun } from "../../../apps/worker/src/jobs/work-run";
import { approveActionRequest, getActionRequest } from "../src/workflows/action-store";
import { executeApprovedActionRequest, registerActionAdapter } from "../src/workflows/action-executor";
import { createRecordActionAdapter, registerHarnessRecordActionPreflight } from "../../../apps/worker/src/records/adapters";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("reads and mutates populated Records through OpenShell, approval and real GraphJin", async () => {
  if (process.env.NEKO_PG_PORT !== "18119" || process.env.RECORDS_PG_PORT !== "18120" || !process.env.HARNESS_STATE) {
    throw Error("isolated M3 records environment required");
  }
  await runRecordsMigrations({});
  const orgId = `harness-records-data-${randomUUID()}`;
  const threadId = randomUUID(), runId = randomUUID(), objectId = randomUUID();
  const tableName = `equipment__loan_${runId.replaceAll("-", "").slice(0, 8)}`;
  const root = join(process.env.HARNESS_STATE, "records-data", runId);
  const workspace: AgentWorkspace = {
    orgRoot: root, skillsRoot: join(root, "skills"), memoryRoot: join(root, "memory"),
    knowledgeRoot: join(root, "knowledge"), uploadsRoot: join(root, "uploads"),
    runsRoot: join(root, "runs"), threadUploadsRoot: join(root, "uploads", threadId),
    runRoot: join(root, "runs", runId), artifactRoot: join(root, "runs", runId, "artifacts"),
    binRoot: join(root, "runs", runId, "bin"),
  };
  const recordsPool = new pg.Pool(buildRecordsPoolConfig());
  let child: ReturnType<typeof spawn> | undefined;
  let broker: Awaited<ReturnType<typeof startAgentBroker>> | undefined;
  let queue: Awaited<ReturnType<typeof boss>> | undefined;
  let unregisterPreflight=()=>{};
  const priorUrl = process.env.OPENNEKO_RECORDS_GRAPHJIN_URL;
  let logs = "";
  try {
    await mkdir(root, { recursive: true });
    for (const dir of Object.values(workspace)) await mkdir(dir, { recursive: true });
    await mkdir(join(workspace.skillsRoot, "records"));
    await writeFile(join(workspace.skillsRoot, "records", "SKILL.md"), "# Records\nUse only the admitted Records tools and governed action proposals.\n");
    const hermesHome = join(root, "provider-config");
    await mkdir(hermesHome, { recursive: true });
    await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-records-data-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    await db().insert(organization).values({ id: orgId, name: "Populated records fixture" });
    await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Records data" });
    await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "admin" });
    await db().insert(app_state).values({ org_id: orgId, app_id: "equipment", status: "active" });
    await recordsPool.query(`CREATE TABLE public.${tableName} (
      id text PRIMARY KEY, org_id text NOT NULL, name text NOT NULL,
      owner_user_id text, nk_deleted_at timestamptz,
      nk_created_at timestamptz NOT NULL DEFAULT now(), nk_created_by text NOT NULL DEFAULT 'seed',
      nk_updated_at timestamptz NOT NULL DEFAULT now(), nk_updated_by text NOT NULL DEFAULT 'seed',
      nk_action_request_id text NOT NULL DEFAULT 'seed', nk_mutation_id text NOT NULL DEFAULT 'seed')`);
    await recordsPool.query("INSERT INTO engine.registry_version (org_id, revision) VALUES ($1, 1)", [orgId]);
    await recordsPool.query("INSERT INTO engine.record_app (org_id, app_id, label, status) VALUES ($1, 'equipment', 'Equipment', 'active')", [orgId]);
    await recordsPool.query(`INSERT INTO engine.record_object
      (id, org_id, app_id, api_name, label, plural_label, table_name, name_field, visibility)
      VALUES ($1, $2, 'equipment', 'loan', 'Loan', 'Loans', $3, 'name', 'org')`, [objectId, orgId, tableName]);
    await recordsPool.query(`INSERT INTO engine.record_field
      (org_id, object_id, api_name, label, kind, column_name, required)
      VALUES ($1, $2, 'name', 'Name', 'text', 'name', true)`, [orgId, objectId]);
    await recordsPool.query(`INSERT INTO engine.record_permission
      (org_id, app_id, role, object_api_name, can_read, can_create, can_update, can_delete)
      VALUES ($1, 'equipment', 'admin', 'loan', true, true, true, true)`, [orgId]);
    await recordsPool.query("INSERT INTO engine.actor (org_id,user_id,role) VALUES ($1,'records-service','service')",[orgId]);
    await recordsPool.query(`INSERT INTO public.${tableName} (id, org_id, name) VALUES ('loan-42', $1, 'Fixture loan'),('loan-43',$1,'Queued fixture loan')`, [orgId]);
    await ensureRecordsAuditTrigger(recordsPool,{tableSchema:"public",tableName,appId:"equipment",objectApiName:"loan"});
    await recordsPool.query(`INSERT INTO engine.recycle_record
      (org_id, app_id, object_api_name, visibility, record_id, record_name, deleted_at, deletion_action_request_id)
      VALUES ($1, 'equipment', 'loan', 'org', 'loan-deleted-42', 'Deleted fixture loan', now(), 'fixture-delete')`, [orgId]);
    const model = await loadRecordsGraphjinPolicyModel(recordsPool, orgId);
    const configDir = join(root, "graphjin");
    await mkdir(configDir, { recursive: true });
    const configFile = join(configDir, "dev.yml");
    const config = buildRecordsPoolConfig();
    const connection = new URL(`postgres://${config.user}:${config.password}@127.0.0.1:${config.port}/${config.database}?sslmode=disable`);
    await writeRecordsGraphjinConfig({
      configFile,
      config: {
        orgId, roles: projectRecordsGraphjinRoles(model),
        database: { connectionString: connection.toString() },
        jwt: { secret: recordsGraphjinSigningSecret(orgId) },
        secretKey: recordsGraphjinCursorSecret(orgId),
      },
      reloadRecordsGraphjin: async () => {},
    });
    await writeFile(configFile, (await readFile(configFile, "utf8")).replace("0.0.0.0:8090", "127.0.0.1:18124"));
    child = spawn(process.env.RECORDS_GRAPHJIN_BIN ?? "graphjin", ["serve", "--path", configDir], { stdio: ["ignore", "pipe", "pipe"] });
    child.stdout?.on("data", chunk => { logs = (logs + String(chunk)).slice(-2048); });
    child.stderr?.on("data", chunk => { logs = (logs + String(chunk)).slice(-2048); });
    let ready = false;
    for (let n = 0; n < 60; n++) {
      if (child.exitCode !== null || child.signalCode !== null) break;
      try { if ((await fetch("http://127.0.0.1:18124/health")).ok) { ready = true; break; } } catch { /* starting */ }
      await new Promise(resolve => setTimeout(resolve, 250));
    }
    expect(ready, logs).toBe(true);
    process.env.OPENNEKO_RECORDS_GRAPHJIN_URL = "http://127.0.0.1:18124";
    broker = await startAgentBroker({ port: 0, hostAlias: "host.docker.internal", controlPlane: inProcessControlPlane });
    const token = broker.tokenFor({ orgId, threadId, runId, kind: "work", profile: "harness-read-only", recordsRead: true, lookupRead: false });
    const post = async (path: string, body: object) => {
      const response = await fetch(`http://127.0.0.1:${broker!.port}${path}`, {
        method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
        body: JSON.stringify(body),
      });
      expect(response.status, `${path}: ${await response.clone().text()}`).toBe(200);
      return response.json();
    };
    expect(await post("/v1/records/catalog", { appId: "equipment" })).toMatchObject({ apps: [{ appId: "equipment" }] });
    expect((await post("/v1/records/find", { appId: "equipment", objectApiName: "loan", first: 5 })).rows.map((row:{id:string})=>row.id).sort()).toEqual(["loan-42","loan-43"]);
    expect(await post("/v1/records/get", { appId: "equipment", objectApiName: "loan", recordId: "loan-42" })).toMatchObject({ row: { id: "loan-42" } });
    expect(await post("/v1/records/recycle/find", { appId: "equipment", objectApiName: "loan" })).toMatchObject({ rows: [{ recordId: "loan-deleted-42" }] });
    expect(await post("/v1/records/recycle/get", { appId: "equipment", objectApiName: "loan", recordId: "loan-deleted-42" })).toMatchObject({ row: { recordId: "loan-deleted-42" } });
    expect((await post("/v1/records/find", { appId: "equipment", objectApiName: "loan", first: 5, orgId: "forged", runId: "forged" })).rows).toHaveLength(2);
    broker.release(runId);
    const runCore = makeSandboxRunCore({
      cli: process.env.HARNESS_M3_CLI!, gatewayName: "harness-m2", agentImage: "harness-openneko:m3",
      modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }],
      hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url,
      brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => {},
    });
    const result = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace,
      prompt: "Read the equipment loan and recycle bin using Records tools.",
      userMessage: "Which loan exists and which loan was deleted?", dataSurface: "records", pluginActions: [],
      emit: async () => {},
    });
    expect(result.status).toBe("completed");
    expect(result.finalText).toContain("loan-42");
    const snapshot = JSON.parse(await readFile(join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`), "utf8"));
    expect(snapshot.operations.map((operation: { tool: string; finished: boolean }) => [operation.tool, operation.finished])).toEqual([
      ["mcp_neko_records_browse_catalog", true], ["mcp_neko_records_find_records", true],
      ["mcp_neko_records_get_record", true], ["mcp_neko_records_find_recycled_records", true],
      ["mcp_neko_records_get_recycled_record", true],
    ]);
    // Reuse the live registry and GraphJin fixture for one real Records write.
    // The model proposes only; the worker-side adapter performs the mutation
    // after a human approval, and duplicate execution restores its receipt.
    const actionRunId=randomUUID();
    await db().insert(work_run).values({id:actionRunId,org_id:orgId,thread_id:threadId,backend:"harness",actor_role:"admin"});
    await db().insert(action_policy).values({org_id:orgId,name:"Fixture record mutation approval",mode:"approval_required",applies_to_kinds:["record_create","record_update","record_delete","record_restore"],applies_to_scopes:["internal"]});
    const actionWorkspace=Object.fromEntries(Object.entries(workspace).map(([key,value])=>[key,value.replaceAll(runId,actionRunId)])) as AgentWorkspace;
    for(const dir of Object.values(actionWorkspace)) await mkdir(dir,{recursive:true});
    await mkdir(join(actionWorkspace.skillsRoot,"records"),{recursive:true});
    await writeFile(join(actionWorkspace.skillsRoot,"records","SKILL.md"),"# Records\nUse governed action proposals for updates.\n");
    await writeFile(join(hermesHome,"config.yaml"),"model:\n  provider: custom\n  default: harness-records-action-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    unregisterPreflight=registerHarnessRecordActionPreflight();
    const actionEvents: Array<{type:string;action_request_id?:string}>=[];
    const proposed=await runCore({backend:makeAgentBackend({id:"harness"}),orgId,threadId,runId:actionRunId,workspace:actionWorkspace,
      prompt:"Propose the equipment loan update. Do not execute before human approval.",userMessage:"Rename loan-42 to Updated fixture loan",dataSurface:"records",
      pluginActions:[{kind:"record_update",description:"Update one record",scope:"internal",default_mode:"ask"}],
      emit:async event=>{actionEvents.push(event);}});
    expect(proposed.status,JSON.stringify(proposed)).toBe("completed");
    const proposal=actionEvents.find(event=>event.type==="action_request_emit");
    expect(proposal?.action_request_id).toBeTruthy();
    const requestId=proposal!.action_request_id!;
    expect((await getActionRequest(orgId,requestId))?.status).toBe("pending_approval");
    expect((await recordsPool.query(`SELECT name FROM public.${tableName} WHERE id='loan-42'`)).rows[0].name).toBe("Fixture loan");
    await approveActionRequest({orgId,id:requestId,approverUserId:null,approver:{userId:null,role:"admin"}});
    const executor=new RecordWriteExecutor({pool:recordsPool,graphjin:new RecordsGraphjinClient({baseUrl:"http://127.0.0.1:18124"}),
      serviceToken:org=>mintRecordsGraphjinToken({secret:recordsGraphjinSigningSecret(org),orgId:org,userId:"records-service",role:"service"}),
      leaseOwner:`harness-records-${actionRunId}`,recordSourceWrite:async()=>{}});
    const adapter=createRecordActionAdapter("record_update",executor);
    let effectCause:unknown;
    let loseHostReceipt=true;
    registerActionAdapter("record_update",Object.assign(async (input:Parameters<typeof adapter>[0])=>{
      try {
        const outcome=await adapter(input);
        if(loseHostReceipt) {
          loseHostReceipt=false;
          throw Error("Fixture lost the host receipt after Records committed");
        }
        return outcome;
      } catch(error) {effectCause=error;throw error;}
    },{reconcile:adapter.reconcile}));
    const interrupted=await executeApprovedActionRequest(orgId,requestId);
    const effectDetail=effectCause instanceof RecordsGraphjinRequestError
      ? JSON.stringify({status:effectCause.status,errors:effectCause.graphjinErrors}) : String(effectCause);
    expect(interrupted.ok,effectDetail).toBe(false);
    expect(interrupted.error).toContain("outcome unknown");
    expect((await recordsPool.query(`SELECT name FROM public.${tableName} WHERE id='loan-42'`)).rows[0].name).toBe("Updated fixture loan");
    expect((await recordsPool.query("SELECT count(*)::int AS n FROM engine.record_change_log WHERE org_id=$1 AND action_request_id=$2",[orgId,requestId])).rows[0].n).toBe(1);
    const effectResult=await executeApprovedActionRequest(orgId,requestId);
    expect(effectResult.ok,effectDetail).toBe(true);
    expect((effectResult.outcome?.result as {recovered?:boolean})?.recovered).toBe(true);
    expect((await executeApprovedActionRequest(orgId,requestId)).ok).toBe(true);
    expect((await recordsPool.query(`SELECT name FROM public.${tableName} WHERE id='loan-42'`)).rows[0].name).toBe("Updated fixture loan");
    expect((await recordsPool.query("SELECT count(*)::int AS n FROM engine.record_change_log WHERE org_id=$1 AND action_request_id=$2",[orgId,requestId])).rows[0].n).toBe(1);

    // Drive the production Work queue for a second row. Redelivery of the
    // completed run must restore its proposal without another model turn or
    // effect; approval then crosses the same real Records adapter.
    await db().update(organization).set({setup_complete_at:new Date()}).where(eq(organization.id,orgId));
    await db().insert(llm_provider_config).values({org_id:orgId,scope:"primary",provider:"ollama",model:"harness-records-queue-fixture",
      config:{url:"http://host.docker.internal:18118"}});
    // The isolated consumer stack pins an operator-level model home. Its
    // config takes precedence over the org provider row for sandbox launches.
    await writeFile(join(process.env.OPENNEKO_AGENT_HERMES_HOME!,"config.yaml"),
      "model:\n  provider: custom\n  default: harness-records-queue-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const queueThread=await createWorkThread(orgId,"Queued Records mutation","web",null,{recordContext:{
      appId:"equipment",appLabel:"Equipment",objectApiName:"loan",objectLabel:"Loan",surface:"detail",recordId:"loan-43"}});
    const queueRun=await createWorkRun(orgId,queueThread.id,"harness",{userId:null,role:"admin"});
    const queueWorkspace=await ensureWorkWorkspace(orgId,queueThread.id,queueRun.id);
    await mkdir(join(queueWorkspace.skillsRoot,"records"),{recursive:true});
    await writeFile(join(queueWorkspace.skillsRoot,"records","SKILL.md"),"# Records\nPropose governed changes and wait for approval.\n");
    queue=await boss();
    await queue.createQueue(QUEUE.WORK_RUN);
    await queue.work<WorkRunPayload>(QUEUE.WORK_RUN,async jobs=>{
      for(const job of jobs) {
        const {processingJobId,orgId:jobOrgId,...payload}=job.data;
        await db().update(processing_job).set({status:"running"}).where(eq(processing_job.id,processingJobId));
        try {
          await runWorkRun(processingJobId,jobOrgId,{...payload,channel:"web"});
          await db().update(processing_job).set({status:"succeeded"}).where(eq(processing_job.id,processingJobId));
        } catch(error) {
          await db().update(processing_job).set({status:"failed"}).where(eq(processing_job.id,processingJobId));
          throw error;
        }
      }
    });
    const queueMessage="Rename loan-43 to Queued fixture loan updated; request approval first.";
    const enqueueRun=async()=>{
      const [job]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:"records-action-fixture"}).returning();
      await enqueue(QUEUE.WORK_RUN,{processingJobId:job.id,orgId,runId:queueRun.id,threadId:queueThread.id,message:queueMessage},{retryLimit:0});
      for(let attempt=0;attempt<90;attempt++) {
        const current=await getWorkRun(orgId,queueRun.id);
        const [processing]=await db().select().from(processing_job).where(eq(processing_job.id,job.id));
        if(current?.status==="completed" && processing?.status==="succeeded") return;
        if(current?.status==="failed" || processing?.status==="failed") {
          const modelCounts=await (await fetch("http://127.0.0.1:18118/control")).json();
          const checkpointPath=join(queueWorkspace.runRoot,".harness",`${createHash("sha256").update(queueRun.id).digest("hex")}.json`);
          const checkpoint=await readFile(checkpointPath,"utf8").catch(()=>"{}");
          const operations=JSON.parse(checkpoint).operations?.map((operation:{tool:string;finished:boolean})=>({tool:operation.tool,finished:operation.finished}));
          throw Error(`Queued Records run failed: ${current?.error ?? "worker error"}; model counts=${JSON.stringify(modelCounts)}; operations=${JSON.stringify(operations)}`);
        }
        await new Promise(resolve=>setTimeout(resolve,500));
      }
      throw Error("Queued Records run did not finish");
    };
    await enqueueRun();
    const [queuedRequest]= (await pool().query("SELECT id,status FROM action_request WHERE org_id=$1 AND work_run_id=$2",[orgId,queueRun.id])).rows;
    expect(queuedRequest?.status).toBe("pending_approval");
    expect((await recordsPool.query(`SELECT name FROM public.${tableName} WHERE id='loan-43'`)).rows[0].name).toBe("Queued fixture loan");
    const modelCalls=(await (await fetch("http://127.0.0.1:18118/control")).json())["harness-records-queue-fixture"];
    await enqueueRun();
    expect((await (await fetch("http://127.0.0.1:18118/control")).json())["harness-records-queue-fixture"]).toBe(modelCalls);
    expect((await pool().query("SELECT count(*)::int AS n FROM action_request WHERE org_id=$1 AND work_run_id=$2",[orgId,queueRun.id])).rows[0].n).toBe(1);
    await approveActionRequest({orgId,id:queuedRequest.id,approverUserId:null,approver:{userId:null,role:"admin"}});
    expect((await executeApprovedActionRequest(orgId,queuedRequest.id)).ok).toBe(true);
    expect((await recordsPool.query(`SELECT name FROM public.${tableName} WHERE id='loan-43'`)).rows[0].name).toBe("Queued fixture loan updated");
    expect((await recordsPool.query("SELECT count(*)::int AS n FROM engine.record_change_log WHERE org_id=$1 AND action_request_id=$2",[orgId,queuedRequest.id])).rows[0].n).toBe(1);

    // Reuse the same real queue, GraphJin and worker executor for the three
    // remaining CRUD actions. Each action has a distinct model key so the
    // deterministic model validates its own approval receipt on turn two.
    for (const step of [
      {kind:"record_create",model:"harness-records-create-fixture",message:"Create a fixture equipment loan."},
      {kind:"record_delete",model:"harness-records-delete-fixture",message:"Recycle loan-43."},
      {kind:"record_restore",model:"harness-records-restore-fixture",message:"Restore loan-43."},
    ] as const) {
      const stepAdapter=createRecordActionAdapter(step.kind,executor);
      let loseStepReceipt=true;
      registerActionAdapter(step.kind,Object.assign(async (input:Parameters<typeof stepAdapter>[0])=>{
        const outcome=await stepAdapter(input);
        if(loseStepReceipt) {
          loseStepReceipt=false;
          throw Error("Fixture lost the host receipt after Records committed");
        }
        return outcome;
      },{reconcile:stepAdapter.reconcile}));
      await db().update(llm_provider_config).set({model:step.model}).where(eq(llm_provider_config.org_id,orgId));
      await writeFile(join(process.env.OPENNEKO_AGENT_HERMES_HOME!,"config.yaml"),
        `model:\n  provider: custom\n  default: ${step.model}\n  base_url: http://host.docker.internal:18118/v1\n`);
      const stepThread=await createWorkThread(orgId,`Queued ${step.kind}`,"web",null,{recordContext:{
        appId:"equipment",appLabel:"Equipment",objectApiName:"loan",objectLabel:"Loan",surface:"detail",recordId:"loan-43"}});
      const stepRun=await createWorkRun(orgId,stepThread.id,"harness",{userId:null,role:"admin"});
      const [stepJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:`${step.kind}-fixture`}).returning();
      await enqueue(QUEUE.WORK_RUN,{processingJobId:stepJob.id,orgId,runId:stepRun.id,threadId:stepThread.id,message:step.message},{retryLimit:0});
      let done=false;
      for(let attempt=0;attempt<90;attempt++) {
        const current=await getWorkRun(orgId,stepRun.id);
        const [processing]=await db().select().from(processing_job).where(eq(processing_job.id,stepJob.id));
        if(current?.status==="completed" && processing?.status==="succeeded") {done=true;break;}
        if(current?.status==="failed" || processing?.status==="failed") {
          throw Error(`${step.kind} queue failed: ${current?.error ?? "worker error"}`);
        }
        await new Promise(resolve=>setTimeout(resolve,500));
      }
      expect(done,`${step.kind} queue timeout`).toBe(true);
      const requests=(await pool().query("SELECT id,status FROM action_request WHERE org_id=$1 AND work_run_id=$2",[orgId,stepRun.id])).rows;
      expect(requests).toHaveLength(1);
      expect(requests[0].status).toBe("pending_approval");
      const callsBeforeRedelivery=(await (await fetch("http://127.0.0.1:18118/control")).json())[step.model];
      const [replayJob]=await db().insert(processing_job).values({org_id:orgId,kind:QUEUE.WORK_RUN,trigger:`${step.kind}-redelivery`}).returning();
      await enqueue(QUEUE.WORK_RUN,{processingJobId:replayJob.id,orgId,runId:stepRun.id,
        threadId:stepThread.id,message:step.message},{retryLimit:0});
      let replayDone=false;
      for(let attempt=0;attempt<90;attempt++) {
        const [processing]=await db().select().from(processing_job).where(eq(processing_job.id,replayJob.id));
        if(processing?.status==="succeeded") {replayDone=true;break;}
        if(processing?.status==="failed") throw Error(`${step.kind} redelivery failed`);
        await new Promise(resolve=>setTimeout(resolve,500));
      }
      expect(replayDone,`${step.kind} redelivery timeout`).toBe(true);
      expect((await (await fetch("http://127.0.0.1:18118/control")).json())[step.model]).toBe(callsBeforeRedelivery);
      expect((await pool().query("SELECT count(*)::int AS n FROM action_request WHERE org_id=$1 AND work_run_id=$2",[orgId,stepRun.id])).rows[0].n).toBe(1);
      expect((await recordsPool.query("SELECT count(*)::int AS n FROM engine.record_change_log WHERE org_id=$1 AND action_request_id=$2",[orgId,requests[0].id])).rows[0].n).toBe(0);
      await approveActionRequest({orgId,id:requests[0].id,approverUserId:null,approver:{userId:null,role:"admin"}});
      const interrupted=await executeApprovedActionRequest(orgId,requests[0].id);
      expect(interrupted.ok,`${step.kind}: ${JSON.stringify(interrupted)}`).toBe(false);
      expect(interrupted.error).toContain("outcome unknown");
      expect((await recordsPool.query("SELECT count(*)::int AS n FROM engine.record_change_log WHERE org_id=$1 AND action_request_id=$2",[orgId,requests[0].id])).rows[0].n).toBe(1);
      const outcome=await executeApprovedActionRequest(orgId,requests[0].id);
      expect(outcome.ok,`${step.kind}: ${JSON.stringify(outcome)}`).toBe(true);
      expect((outcome.outcome?.result as {recovered?:boolean})?.recovered).toBe(true);
      expect((await executeApprovedActionRequest(orgId,requests[0].id)).ok).toBe(true);
      expect((await recordsPool.query("SELECT count(*)::int AS n FROM engine.record_change_log WHERE org_id=$1 AND action_request_id=$2",[orgId,requests[0].id])).rows[0].n).toBe(1);
      if(step.kind==="record_create") {
        expect((await recordsPool.query(`SELECT count(*)::int AS n FROM public.${tableName} WHERE org_id=$1 AND name='Created fixture loan'`,[orgId])).rows[0].n).toBe(1);
      } else {
        const [row]=(await recordsPool.query(`SELECT nk_deleted_at FROM public.${tableName} WHERE id='loan-43'`)).rows;
        expect(Boolean(row?.nk_deleted_at)).toBe(step.kind==="record_delete");
      }
    }
  } finally {
    unregisterPreflight();
    if(queue) await queue.offWork(QUEUE.WORK_RUN);
    await shutdownAgentBroker();
    if (broker) await broker.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      child.kill("SIGTERM");
      await new Promise(resolve => child!.once("close", resolve));
    }
    if (priorUrl === undefined) delete process.env.OPENNEKO_RECORDS_GRAPHJIN_URL;
    else process.env.OPENNEKO_RECORDS_GRAPHJIN_URL = priorUrl;
    await recordsPool.query("DELETE FROM engine.action_execution WHERE org_id=$1",[orgId]);
    await recordsPool.query("DELETE FROM engine.record_change_log WHERE org_id=$1",[orgId]);
    await recordsPool.query("DELETE FROM engine.record_app WHERE org_id=$1", [orgId]);
    await recordsPool.query("DELETE FROM engine.actor WHERE org_id=$1", [orgId]);
    await recordsPool.query("DELETE FROM engine.registry_version WHERE org_id=$1", [orgId]);
    await recordsPool.query(`DROP TABLE IF EXISTS public.${tableName}`);
    await recordsPool.end();
    await deleteTestOrg(orgId);
  }
}, 120_000);
