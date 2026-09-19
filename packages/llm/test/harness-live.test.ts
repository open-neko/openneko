import { randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
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
        const runCore = makeSandboxRunCore({ cli: process.env.HARNESS_M3_CLI!, gatewayName: "harness-m2", agentImage: "harness-openneko:m3", modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }], hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url, brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => { } });
        const events: AgentEvent[] = [];
        const input = { backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace, prompt: "Find the seeded reference using lookup. Report the returned reference.", pluginActions: [], emit: async (e: AgentEvent) => { events.push(e); } };
        const result = await runCore(input);
        expect(result.status, JSON.stringify(result)).toBe("completed");
        expect(result.finalText).toContain("REF-42");
        expect(events.some(e => e.type === "tool_start")).toBe(true);
        expect(JSON.stringify(result.backendState)).toContain("REF-42");
        const replay = await runCore(input);
        expect(replay.status, JSON.stringify(replay)).toBe("completed");
        expect(replay.finalText).toBe(result.finalText);
    }
    finally {
        await broker.close();
        await db().delete(organization).where(eq(organization.id, orgId));
        await pool().end();
    }
}, 180000);
