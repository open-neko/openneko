import { execFileSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile, rename, chmod } from "node:fs/promises";
import { join } from "node:path";
import { expect, it } from "vitest";
import { db, pool, organization, data_source, work_thread, work_run, eq } from "@neko/db";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { inProcessControlPlane } from "../src/work/control-plane";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
// Explicit opt-in, isolated DB/gateway/model/GraphJin deployment required.
const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;
live("runs Harness through the real launcher, broker and GraphJin and replays its checkpoint", async () => {
    const root = process.env.HARNESS_STATE!;
    if (!root || process.env.NEKO_PG_PORT !== "18119")
        throw new Error("isolated M3 environment required");
    const orgId = `harness-m3-${randomUUID()}`, threadId = randomUUID(), runId = randomUUID();
    const orgRoot = join(root, "workspace");
    const workspace: AgentWorkspace = { orgRoot, skillsRoot: join(orgRoot, "skills"), memoryRoot: join(orgRoot, "memory"), knowledgeRoot: join(orgRoot, "knowledge"), uploadsRoot: join(orgRoot, "uploads"), runsRoot: join(orgRoot, "runs"), threadUploadsRoot: join(orgRoot, "uploads", threadId), runRoot: join(orgRoot, "runs", runId), artifactRoot: join(orgRoot, "runs", runId, "artifacts"), binRoot: join(orgRoot, "runs", runId, "bin") };
    for (const dir of Object.values(workspace))
        await mkdir(dir, { recursive: true });
    const hermesHome = join(root, "provider-config");
    await mkdir(hermesHome, { recursive: true });
    await writeFile(join(hermesHome, "config.yaml"), 'model:\n  provider: custom\n  default: harness-fixture\n  base_url: http://host.docker.internal:18118/v1\n');
    await db().insert(organization).values({ id: orgId, name: "Harness M3 acceptance" });
    await db().insert(data_source).values({ org_id: orgId, graphql_url: "http://127.0.0.1:18117/api/v1/graphql", kind: "graphjin", auth_mode: "none", is_default: true });
    await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "M3" });
    await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
    const broker = await startAgentBroker({ port: 0, hostAlias: "host.docker.internal", controlPlane: inProcessControlPlane });
    try {
        const url = `http://127.0.0.1:${broker.port}/v1/graphjin/agent`;
        const unauthenticated = await fetch(url, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ instruction: "Find reference" }) });
        expect(unauthenticated.status).toBe(401);
        const token = broker.tokenFor({ orgId, runId, threadId, kind: "work" });
        const forged = await fetch(url, { method: "POST", headers: { "content-type": "application/json", authorization: `Bearer ${token}` }, body: JSON.stringify({ instruction: "Find reference", dataSourceId: randomUUID(), orgId: "forged-org", actor: { role: "admin" } }) });
        const denied = await forged.json();
        expect(denied.denied || denied.error, JSON.stringify(denied)).toBeTruthy();
        broker.release(runId);
        // Lose the first checkpoint transfer after the real model/tool execution.
        // This leaves only the retained sandbox, with no host receipt/checkpoint.
        const cli = join(root, "recovery-cli");
        const fault = join(root, "recovery-download-fault");
        await writeFile(fault, "1");
        await writeFile(cli, `#!/bin/sh
if [ -f '${fault}' ]; then
  for arg in "$@"; do
    if [ "$arg" = download ]; then rm '${fault}'; exit 71; fi
  done
fi
exec '${process.env.HARNESS_M3_CLI!}' "$@"
`);
        await chmod(cli, 0o700);
        const runCore = makeSandboxRunCore({ cli, gatewayName: "harness-m2", agentImage: "harness-openneko:m3", modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }], hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url, brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => { } });
        const events: AgentEvent[] = [];
        const input = { backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace, prompt: "Find the seeded reference using lookup. Report the returned reference.", pluginActions: [], emit: async (e: AgentEvent) => { events.push(e); } };
        await expect(runCore(input)).rejects.toThrow();
        await expect(readFile(join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`))).rejects.toThrow();
        const modelCalls = await (await fetch("http://127.0.0.1:18118/control")).json();
        expect(modelCalls["harness-fixture"]).toBe(3);
        expect(modelCalls["graphjin-fixture"]).toBeGreaterThan(0);
        const result = await runCore(input);
        expect(result.status, JSON.stringify(result)).toBe("completed");
        expect(result.finalText).toContain("REF-42");
        expect(events.some(e => e.type === "tool_start")).toBe(true);
        expect(JSON.stringify(result.backendState)).toContain("REF-42");
        const inspected = JSON.parse(execFileSync(process.env.HARNESS_INSPECT_BIN!, [], {
            env: { HARNESS_STATE_DIR: join(workspace.runRoot, ".harness") },
            input: JSON.stringify({ version: 1, run_id: runId, input_id: runId, prompt: input.prompt }),
            encoding: "utf8",
        }));
        expect(inspected.outcome).toBe("terminal");
        expect(inspected.operations).toHaveLength(1);
        expect(inspected.result.answer).toContain("REF-42");
        const receiptRoot = join(workspace.runsRoot, ".harness-launches", createHash("sha256").update(runId).digest("hex"));
        const receipt = JSON.parse(await readFile(join(receiptRoot, "result.json"), "utf8"));
        expect(receipt.result.status).toBe("completed");
        const replay = await runCore(input);
        expect(replay.status, JSON.stringify(replay)).toBe("completed");
        expect(replay.finalText).toBe(result.finalText);
        await expect(runCore({...input,prompt:"changed accepted input"})).rejects.toThrow("conflicts");
        // Recover again from the downloaded checkpoint after loss of the host receipt.
        await rename(join(receiptRoot,"result.json"),join(receiptRoot,"saved-result.json"));
        const recovery = await Promise.allSettled([runCore(input), runCore(input)]);
        expect(recovery.filter(r => r.status === "fulfilled")).toHaveLength(1);
        expect((recovery.find(r => r.status === "fulfilled") as PromiseFulfilledResult<typeof result>).value).toEqual(result);
        // An unresolved operation must never be reissued, even on repeated delivery.
        const snapshot = join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`);
        const original = await readFile(snapshot, "utf8");
        const unknown = JSON.parse(original);
        delete unknown.result;
        unknown.events = unknown.events.slice(0, unknown.events.findIndex((e: {type: string}) => e.type === "tool.started") + 1);
        unknown.operations = [{id: 1, instruction: unknown.operations[0].instruction, finished: false}];
        await rename(join(receiptRoot,"result.json"),join(receiptRoot,"reconciled-result.json"));
        await writeFile(snapshot, JSON.stringify(unknown));
        await expect(runCore(input)).rejects.toThrow("outcome unknown");
        await expect(runCore(input)).rejects.toThrow("outcome unknown");
        await writeFile(snapshot, original);
        await rename(join(receiptRoot,"saved-result.json"),join(receiptRoot,"result.json"));
        expect(await (await fetch("http://127.0.0.1:18118/control")).json()).toEqual(modelCalls);
    }
    finally {
        await broker.close();
        await db().delete(organization).where(eq(organization.id, orgId));
        await pool().end();
    }
}, 180000);
