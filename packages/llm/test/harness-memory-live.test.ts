import { createHash, randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { db, library_concept, organization, sql, work_thread, work_run } from "@neko/db";
import { deleteTestOrg } from "@neko/db/test-helpers";
import { expect, it } from "vitest";
import type { AgentControlPlane } from "../src/work/control-plane";
import type { AgentEvent, AgentWorkspace } from "../src/agent-backend";
import { makeAgentBackend } from "../src/agent-runtime";
import { startAgentBroker } from "../src/work/broker";
import { searchLibraryForRun } from "../src/work/library";
import { makeSandboxRunCore } from "../src/work/sandbox-launcher";

const live = process.env.HARNESS_M3_LIVE === "1" ? it : it.skip;

live("runs journaled Harness memory and library reads through OpenShell, MCP and the broker", async () => {
  if (process.env.NEKO_PG_PORT !== "18119" || !process.env.HARNESS_STATE) {
    throw new Error("isolated M3 environment required");
  }
  const orgId = `harness-memory-${randomUUID()}`;
  const threadId = randomUUID();
  const runId = randomUUID();
  const libraryRunId = randomUUID();
  const priorEmbeddingURL = process.env.NEKO_EMBEDDING_URL;
  const orgRoot = join(process.env.HARNESS_STATE, "memory", runId);
  const workspace: AgentWorkspace = {
    orgRoot,
    skillsRoot: join(orgRoot, "skills"),
    memoryRoot: join(orgRoot, "memory"),
    knowledgeRoot: join(orgRoot, "knowledge"),
    uploadsRoot: join(orgRoot, "uploads"),
    runsRoot: join(orgRoot, "runs"),
    threadUploadsRoot: join(orgRoot, "uploads", threadId),
    runRoot: join(orgRoot, "runs", runId),
    artifactRoot: join(orgRoot, "runs", runId, "artifacts"),
    binRoot: join(orgRoot, "runs", runId, "bin"),
  };
  for (const dir of Object.values(workspace)) await mkdir(dir, { recursive: true });
  const hermesHome = join(orgRoot, "provider-config");
  await mkdir(hermesHome, { recursive: true });
  await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-memory-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
  await db().insert(organization).values({ id: orgId, name: "Harness memory acceptance" });
  await db().insert(work_thread).values({ id: threadId, org_id: orgId, title: "Memory" });
  await db().insert(work_run).values({ id: runId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
  const vector = sql`${JSON.stringify([1, ...Array(383).fill(0)])}::vector`;
  await db().insert(library_concept).values({
    org_id: orgId, user_id: null, path: "contracts/example", type: "contract",
    title: "Fixture contract", body: "TERMS-42", status: "stable", embedding: vector,
  });
  let searches = 0;
  let librarySearches = 0;
  const controlPlane = {
    async searchWorkMemoryByContext(args: { orgId: string; runId: string; query: string }) {
      expect(args).toMatchObject({ orgId, runId, query: "find policy" });
      searches++;
      return [{ id: "memory-1", text: "Fixture policy" }];
    },
    async searchLibraryForRun(args: { orgId: string; runId: string; query: string }) {
      expect(args).toMatchObject({ orgId, runId: libraryRunId, query: "find contract" });
      librarySearches++;
      return searchLibraryForRun(args);
    },
  } as unknown as AgentControlPlane;
  const broker = await startAgentBroker({ port: 0, hostAlias: "host.docker.internal", controlPlane });
  try {
    const runCore = makeSandboxRunCore({
      cli: process.env.HARNESS_M3_CLI!, gatewayName: "harness-m2", agentImage: "harness-openneko:m3",
      modelProvider: "harness-m3", modelHosts: [{ host: "host.docker.internal", port: 18118 }],
      hermesHomeHostPath: hermesHome, warmPoolSize: 0, brokerUrl: broker.url,
      brokerTokenFor: broker.tokenFor, brokerRelease: broker.release, onLog: () => {},
    });
    const events: AgentEvent[] = [];
    const result = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId, workspace,
      prompt: "Search saved memory for the policy and report it.", userMessage: "Find the policy.",
      dataSurface: "customer", pluginActions: [], emit: async (event) => { events.push(event); },
    });
    expect(result.status).toBe("completed");
    expect(result.finalText).toContain("memory-1");
    expect(searches).toBe(1);
    expect(events).toContainEqual(expect.objectContaining({ type: "tool_start", name: "mcp_neko_memory_search" }));
    const snapshotPath = join(workspace.runRoot, ".harness", `${createHash("sha256").update(runId).digest("hex")}.json`);
    const snapshot = JSON.parse(await readFile(snapshotPath, "utf8"));
    expect(snapshot.operations).toHaveLength(1);
    expect(snapshot.operations[0]).toMatchObject({ tool: "mcp_memory_search", finished: true });
    expect(snapshot.operations[0].binding).toMatch(/^[a-f0-9]{64}$/);
    expect(snapshot.events).toContainEqual(expect.objectContaining({ type: "tool.finished", name: "mcp_memory_search", effect: "read" }));

    const libraryWorkspace: AgentWorkspace = {
      ...workspace,
      runRoot: join(workspace.runsRoot, libraryRunId),
      artifactRoot: join(workspace.runsRoot, libraryRunId, "artifacts"),
      binRoot: join(workspace.runsRoot, libraryRunId, "bin"),
    };
    for (const dir of [libraryWorkspace.runRoot, libraryWorkspace.artifactRoot, libraryWorkspace.binRoot]) await mkdir(dir, { recursive: true });
    await db().insert(work_run).values({ id: libraryRunId, org_id: orgId, thread_id: threadId, backend: "harness", actor_role: "service" });
    process.env.NEKO_EMBEDDING_URL = "http://127.0.0.1:18118";
    await writeFile(join(hermesHome, "config.yaml"), "model:\n  provider: custom\n  default: harness-library-fixture\n  base_url: http://host.docker.internal:18118/v1\n");
    const libraryEvents: AgentEvent[] = [];
    const libraryResult = await runCore({
      backend: makeAgentBackend({ id: "harness" }), orgId, threadId, runId: libraryRunId, workspace: libraryWorkspace,
      prompt: "Search the document library for the contract and report it.", userMessage: "Find the contract.",
      dataSurface: "customer", pluginActions: [], emit: async (event) => { libraryEvents.push(event); },
    });
    expect(libraryResult.status).toBe("completed");
    expect(libraryResult.finalText).toContain("TERMS-42");
    expect(librarySearches).toBe(1);
    expect(libraryEvents).toContainEqual(expect.objectContaining({ type: "tool_start", name: "mcp_library_search" }));
    const librarySnapshot = JSON.parse(await readFile(join(libraryWorkspace.runRoot, ".harness", `${createHash("sha256").update(libraryRunId).digest("hex")}.json`), "utf8"));
    expect(librarySnapshot.operations).toHaveLength(1);
    expect(librarySnapshot.operations[0]).toMatchObject({ tool: "mcp_library_search", finished: true });
    expect(librarySnapshot.events).toContainEqual(expect.objectContaining({ type: "tool.finished", name: "mcp_library_search", effect: "read" }));
  } finally {
    if (priorEmbeddingURL === undefined) delete process.env.NEKO_EMBEDDING_URL;
    else process.env.NEKO_EMBEDDING_URL = priorEmbeddingURL;
    try { await broker.close(); }
    finally { await deleteTestOrg(orgId); }
  }
}, 120_000);
