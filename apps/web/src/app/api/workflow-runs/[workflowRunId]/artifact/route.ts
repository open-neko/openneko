import { createReadStream } from "node:fs";
import { Readable } from "node:stream";
import { NextResponse } from "next/server";
import { and, db, eq, workflow_run } from "@neko/db";
import {
  WorkflowApiError,
  getWorkflowApiArtifactForOperator,
} from "@neko/llm/workflows";
import { getOrgId } from "@/lib/db";
import { requireWorkflowRun } from "@/lib/entitlements";
import { readRunArtifact, safeFileName } from "@/lib/work-files";
import { getWorkRunEvents } from "@/lib/work-store";
import { canonicalRunArtifactPath, isEmittedRunArtifact } from "@/lib/work-artifacts";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ workflowRunId: string }>;
};

export async function GET(_request: Request, context: RouteContext) {
  const { workflowRunId } = await context.params;
  const deniedRun = await requireWorkflowRun(workflowRunId);
  if (deniedRun) return deniedRun;
  const orgId = await getOrgId();
  try {
    const [run] = await db()
      .select({
        triggerKind: workflow_run.trigger_kind,
        status: workflow_run.status,
        workRunId: workflow_run.work_run_id,
        artifactPath: workflow_run.result_artifact_path,
      })
      .from(workflow_run)
      .where(and(eq(workflow_run.org_id, orgId), eq(workflow_run.id, workflowRunId)))
      .limit(1);
    if (run && run.triggerKind !== "api") {
      const path = run.workRunId && canonicalRunArtifactPath(run.artifactPath, run.workRunId);
      if (
        run.status !== "completed" ||
        !path ||
        path !== run.artifactPath ||
        !isEmittedRunArtifact(await getWorkRunEvents(orgId, run.workRunId), run.workRunId, path)
      ) {
        throw new WorkflowApiError("artifact_not_ready", "The workflow artifact is not available.", 404);
      }
      const name = path.slice(`runs/${run.workRunId}/artifacts/`.length);
      const file = await readRunArtifact(orgId, run.workRunId, name).catch(() => null);
      if (!file || file.data.length > 64 * 1024 * 1024) {
        throw new WorkflowApiError("artifact_unavailable", "The workflow artifact is unavailable.", 410);
      }
      return new NextResponse(new Uint8Array(file.data), {
        headers: {
          "Content-Type": file.mimeType,
          "Content-Length": String(file.data.length),
          "Content-Disposition": `attachment; filename="${safeFileName(file.filename)}"`,
          "Cache-Control": "no-store, private",
          "X-Content-Type-Options": "nosniff",
        },
      });
    }
    const artifact = await getWorkflowApiArtifactForOperator({
      orgId,
      runId: workflowRunId,
    });
    const body = Readable.toWeb(createReadStream(artifact.absolutePath));
    return new NextResponse(body as ReadableStream, {
      headers: {
        "Content-Type": artifact.contentType,
        "Content-Length": String(artifact.bytes),
        "Content-Disposition": `attachment; filename="${artifact.fileName}"`,
        "Cache-Control": "no-store, private",
        "X-Content-Type-Options": "nosniff",
      },
    });
  } catch (error) {
    if (error instanceof WorkflowApiError) {
      return NextResponse.json(
        { error: { code: error.code, message: error.message } },
        {
          status: error.status,
          headers: { "Cache-Control": "no-store, private" },
        },
      );
    }
    console.error(
      `[workflow-api-artifact] operator download failed: ${error instanceof Error ? error.message : String(error)}`,
    );
    return NextResponse.json(
      { error: { code: "internal_error", message: "Artifact download failed." } },
      { status: 500 },
    );
  }
}
