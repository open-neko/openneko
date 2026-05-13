import { NextResponse } from "next/server";
import { and, db, desc, eq, workflow_output, workflow_run } from "@neko/db";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ workflowRunId: string }>;
};

export async function GET(_request: Request, context: RouteContext) {
  const { workflowRunId } = await context.params;
  const orgId = await getOrgId();

  const runs = await db()
    .select()
    .from(workflow_run)
    .where(
      and(
        eq(workflow_run.org_id, orgId),
        eq(workflow_run.id, workflowRunId),
      ),
    )
    .limit(1);

  const run = runs[0];
  if (!run) {
    return NextResponse.json(
      { error: "Workflow run not found" },
      { status: 404 },
    );
  }

  const outputs = await db()
    .select()
    .from(workflow_output)
    .where(eq(workflow_output.workflow_run_id, workflowRunId))
    .orderBy(desc(workflow_output.created_at));

  return NextResponse.json({
    run: {
      id: run.id,
      workflowId: run.workflow_id,
      threadId: run.thread_id,
      workRunId: run.work_run_id,
      triggerKind: run.trigger_kind,
      triggerPayload: run.trigger_payload,
      chainDepth: run.chain_depth,
      status: run.status,
      summary: run.summary,
      error: run.error,
      startedAt: run.started_at?.toISOString() ?? null,
      finishedAt: run.finished_at?.toISOString() ?? null,
      createdAt: run.created_at.toISOString(),
      updatedAt: run.updated_at.toISOString(),
    },
    outputs: outputs.map((o) => ({
      id: o.id,
      kind: o.kind,
      title: o.title,
      body: o.body,
      payload: o.payload,
      artifactPath: o.artifact_path,
      scope: o.scope,
      topic: o.topic,
      mood: o.mood,
      timeWindowStart: o.time_window_start?.toISOString() ?? null,
      timeWindowEnd: o.time_window_end?.toISOString() ?? null,
      freshnessTtlSeconds: o.freshness_ttl_seconds,
      createdAt: o.created_at.toISOString(),
    })),
  });
}
