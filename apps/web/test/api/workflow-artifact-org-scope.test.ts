import { mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { afterAll, describe, expect, it, vi } from "vitest";
import { dbReachable, withTestOrg } from "@neko/db/test-helpers";
import { pool } from "@neko/db";
import { createWorkRun, createWorkThread, appendWorkRunEvent } from "@neko/llm/work";
import { createWorkflowRun, finishWorkflowRun, saveWorkflow } from "@neko/llm/workflows";
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
});
