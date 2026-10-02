import { mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { afterAll, describe, expect, it, vi } from "vitest";
import { dbReachable, withTestOrg } from "@neko/db/test-helpers";
import { db, eq, pool, workflow_run } from "@neko/db";
import { NextRequest } from "next/server";
import { createWorkRun, createWorkThread, appendWorkRunEvent } from "@neko/llm/work";
import { createWorkflowRun, enableWorkflowApiAccess, finishWorkflowRun, saveWorkflow } from "@neko/llm/workflows";
import { getOrgAgentRoot } from "@neko/llm/work";

const { mockGetOrgId } = vi.hoisted(() => ({ mockGetOrgId: vi.fn() }));
vi.mock("@/lib/db", async () => ({
  ...(await vi.importActual<typeof import("@/lib/db")>("@/lib/db")),
  getOrgId: mockGetOrgId,
}));
// Visibility is a separate gate; this test isolates the route's organization
// scope after an operator has already passed it.
vi.mock("@/lib/entitlements", () => ({ requireWorkflowRun: async () => null }));

import { GET } from "@/app/api/workflow-runs/[workflowRunId]/artifact/route";
import { GET as getPublicArtifact } from "@/app/api/v1/workflows/[workflowId]/runs/[runId]/artifact/route";

const reachable = await dbReachable();
const describeIfDb = reachable ? describe : describe.skip;

describeIfDb("workflow artifact organization scope", () => {
  afterAll(async () => { await pool().end(); });

  it("serves the recorded file to its organization and hides it from another", async () => {
    await withTestOrg(async ownerOrgId => {
      await withTestOrg(async otherOrgId => {
        const { workflow } = await saveWorkflow({
          orgId: ownerOrgId, name: "artifact scope", steps: [{ id: "s1", description: "file" }],
        });
        const thread = await createWorkThread(ownerOrgId, workflow.name, "workflow");
        const work = await createWorkRun(ownerOrgId, thread.id, "hermes");
        const run = await createWorkflowRun({
          orgId: ownerOrgId, workflowId: workflow.id, threadId: thread.id,
          workRunId: work.id, triggerKind: "manual",
        });
        const path = `runs/${work.id}/artifacts/result.csv`;
        const artifactRoot = join(getOrgAgentRoot(ownerOrgId), "runs", work.id, "artifacts");
        await mkdir(artifactRoot, { recursive: true });
        await writeFile(join(artifactRoot, "result.csv"), "id\n42\n");
        try {
          await appendWorkRunEvent({
            orgId: ownerOrgId, threadId: thread.id, runId: work.id,
            event: { type: "artifact", artifact: { path, label: "result.csv" } },
          });
          await finishWorkflowRun({
            workflowRunId: run.id, status: "completed", resultArtifactPath: path,
          });
          const context = { params: Promise.resolve({ workflowRunId: run.id }) };
          mockGetOrgId.mockResolvedValue(ownerOrgId);
          const owner = await GET(new Request("http://localhost/artifact"), context);
          expect(owner.status).toBe(200);
          expect(await owner.text()).toBe("id\n42\n");

          mockGetOrgId.mockResolvedValue(otherOrgId);
          const stranger = await GET(new Request("http://localhost/artifact"), context);
          expect(stranger.status).toBe(404);
          expect(await stranger.json()).toMatchObject({ error: { code: "run_not_found" } });
        } finally {
          await rm(join(getOrgAgentRoot(ownerOrgId), "runs", work.id), { recursive: true, force: true });
        }
      });
    });
  });

  it.each([
    ["xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"],
    ["docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"],
  ])("serves a .%s file with its media type to the owner and rejects another organization's token", async (extension, contentType) => {
    await withTestOrg(async ownerOrgId => {
      await withTestOrg(async otherOrgId => {
        const [{ workflow: ownerWorkflow }, { workflow: otherWorkflow }] = await Promise.all([
          saveWorkflow({ orgId: ownerOrgId, name: "owner file", steps: [{ id: "s1", description: "file" }] }),
          saveWorkflow({ orgId: otherOrgId, name: "other file", steps: [{ id: "s1", description: "file" }] }),
        ]);
        const actor = { userId: null, role: "admin" };
        const [{ token: ownerToken }, { token: otherToken }] = await Promise.all([
          enableWorkflowApiAccess({ orgId: ownerOrgId, workflowId: ownerWorkflow.id, actor }),
          enableWorkflowApiAccess({ orgId: otherOrgId, workflowId: otherWorkflow.id, actor }),
        ]);
        const thread = await createWorkThread(ownerOrgId, ownerWorkflow.name, "workflow");
        const work = await createWorkRun(ownerOrgId, thread.id, "harness");
        const run = await createWorkflowRun({
          orgId: ownerOrgId, workflowId: ownerWorkflow.id, threadId: thread.id,
          workRunId: work.id, triggerKind: "api", executionMode: "single",
        });
        const path = `runs/${work.id}/artifacts/result.${extension}`;
        const artifactRoot = join(getOrgAgentRoot(ownerOrgId), "runs", work.id, "artifacts");
        await mkdir(artifactRoot, { recursive: true });
        const bytes = Buffer.from([0x50, 0x4b, 0x03, 0x04, 0x42, 0x7f]);
        await writeFile(join(artifactRoot, `result.${extension}`), bytes);
        try {
          await finishWorkflowRun({ workflowRunId: run.id, status: "completed", resultArtifactPath: path });
          await db().update(workflow_run).set({ result_expires_at: new Date(Date.now() + 60_000) })
            .where(eq(workflow_run.id, run.id));
          const url = `http://localhost/api/v1/workflows/${ownerWorkflow.id}/runs/${run.id}/artifact`;
          const context = { params: Promise.resolve({ workflowId: ownerWorkflow.id, runId: run.id }) };
          const owner = await getPublicArtifact(new NextRequest(url, {
            headers: { authorization: `Bearer ${ownerToken}` },
          }), context);
          expect(owner.status).toBe(200);
          expect(owner.headers.get("content-type")).toBe(contentType);
          expect(owner.headers.get("content-disposition")).toBe(`attachment; filename="workflow-${run.id}.${extension}"`);
          expect(Buffer.from(await owner.arrayBuffer())).toEqual(bytes);

          mockGetOrgId.mockResolvedValue(ownerOrgId);
          const operator = await GET(new Request("http://localhost/artifact"), {
            params: Promise.resolve({ workflowRunId: run.id }),
          });
          expect(operator.status).toBe(200);
          expect(operator.headers.get("content-type")).toBe(contentType);
          expect(operator.headers.get("content-disposition")).toBe(`attachment; filename="workflow-${run.id}.${extension}"`);
          expect(Buffer.from(await operator.arrayBuffer())).toEqual(bytes);

          const stranger = await getPublicArtifact(new NextRequest(url, {
            headers: { authorization: `Bearer ${otherToken}` },
          }), context);
          expect(stranger.status).toBe(401);
          expect(await stranger.json()).toMatchObject({ error: { code: "invalid_credentials" } });
        } finally {
          await rm(join(getOrgAgentRoot(ownerOrgId), "runs", work.id), { recursive: true, force: true });
        }
      });
    });
  });
});
