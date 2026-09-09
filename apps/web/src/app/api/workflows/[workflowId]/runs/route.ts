import { NextRequest, NextResponse } from "next/server";
import { db, eq, workflow_run } from "@neko/db";
import { ensureHostConfigProvisioned } from "@neko/llm";
import {
  ensureAgentBroker,
  finishWorkRun,
  getWorkRun,
  registerAgentBrokerEventSink,
  workflowRuntimeDepsFromConfig,
} from "@neko/llm/work";
import {
  finishWorkflowRun,
  prepareWorkflowRun,
  runWorkflowTurn,
} from "@neko/llm/workflows";
import { observeSafely, type HarnessRunSummary } from "@neko/telemetry";
import { getPluginActionDescriptors } from "@/lib/auth";
import { createCoalescingEmit } from "@/lib/coalescing-emit";
import { getOrgId } from "@/lib/db";
import {
  registerRun,
  unregisterRun,
} from "@/lib/neko-run-registry";
import { createWebHarnessObserver } from "@/lib/telemetry";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ workflowId: string }>;
};

export async function POST(request: NextRequest, context: RouteContext) {
  const { workflowId } = await context.params;
  const body = await request.json().catch(() => ({}));
  const userMessage =
    typeof body.userMessage === "string" ? body.userMessage.trim() : undefined;

  const orgId = await getOrgId();
  const agentRuntime = await ensureHostConfigProvisioned(orgId);

  let prepared;
  try {
    prepared = await prepareWorkflowRun({
      orgId,
      workflowId,
      triggerKind: "manual",
      triggerPayload: { userMessage: userMessage ?? null },
    });
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    return NextResponse.json({ error: msg }, { status: 400 });
  }

  const abortController = new AbortController();
  registerRun({
    runId: prepared.workRunId,
    threadId: prepared.threadId,
    orgId,
    abortController,
    subscribers: new Set(),
  });

  const { emit, finalize } = createCoalescingEmit({
    orgId,
    threadId: prepared.threadId,
    runId: prepared.workRunId,
  });

  const runTelemetry = createWebHarnessObserver(prepared.workRunId);
  const telemetryOperationId = `workflow:${prepared.workRunId}`;
  const telemetryStartedAt = Date.now();
  await observeSafely(runTelemetry.observer, {
    kind: "run.start",
    operationId: telemetryOperationId,
    attributes: {
      "openneko.run.kind": "production",
      "openneko.product.path": "workflow",
      "openneko.job.kind": "workflow_manual",
      "openneko.workflow.id": prepared.workflow.id,
      "openneko.workflow_run.id": prepared.workflowRun.id,
      "openneko.trigger.kind": "manual",
    },
    measurements: {
      queueDurationMs: 0,
      attempts: 1,
      coverage: "unavailable",
    },
  });

  void (async () => {
    let unregisterBrokerEvents: () => void = () => undefined;
    try {
      const pluginActions = await getPluginActionDescriptors();
      const broker = await ensureAgentBroker();
      unregisterBrokerEvents = registerAgentBrokerEventSink(
        prepared.workRunId,
        emit,
      );
      const result = await runWorkflowTurn(
        {
          prepared,
          userMessage,
          mode: "live",
          emit,
          signal: abortController.signal,
          pluginActions,
          observer: runTelemetry.observer,
        },
        workflowRuntimeDepsFromConfig(agentRuntime, broker),
      );
      await observeSafely(runTelemetry.observer, {
        kind: "output.contract",
        operationId: `${telemetryOperationId}:terminal-output`,
        parentOperationId: telemetryOperationId,
        status:
          result.status === "completed" || result.status === "needs_input"
            ? "ok"
            : "error",
        ...(result.error ? { errorType: "workflow_run_error" } : {}),
        attributes: { "openneko.output.kind": "workflow_result" },
      });
      await observeSafely(runTelemetry.observer, {
        kind: "run.end",
        operationId: telemetryOperationId,
        status:
          result.status === "completed" || result.status === "needs_input"
            ? "ok"
            : "error",
        ...(result.error ? { errorType: "workflow_run_error" } : {}),
        attributes: { "openneko.outcome": result.status },
        measurements: {
          durationMs: Date.now() - telemetryStartedAt,
          queueDurationMs: 0,
          coverage: "unavailable",
        },
      });
      await persistManualWorkflowTelemetry(
        prepared.workflowRun.id,
        emit,
        runTelemetry.snapshot(),
      );
    } catch (err) {
      await observeSafely(runTelemetry.observer, {
        kind: "run.end",
        operationId: telemetryOperationId,
        status: "error",
        errorType: err instanceof Error ? err.name : "unknown",
        attributes: { "openneko.outcome": "failed" },
        measurements: {
          durationMs: Date.now() - telemetryStartedAt,
          queueDurationMs: 0,
          coverage: "unavailable",
        },
      });
      await persistManualWorkflowTelemetry(
        prepared.workflowRun.id,
        emit,
        runTelemetry.snapshot(),
      );
      console.error(
        `[workflow-run] run ${prepared.workflowRun.id} threw:`,
        err,
      );
      try {
        const current = await getWorkRun(orgId, prepared.workRunId);
        const terminal =
          current?.status === "completed" ||
          current?.status === "failed" ||
          current?.status === "cancelled";
        if (terminal) return;
        const errMsg = err instanceof Error ? err.message : String(err);
        await finishWorkRun(prepared.workRunId, "failed", errMsg);
        await finishWorkflowRun({
          workflowRunId: prepared.workflowRun.id,
          status: "failed",
          error: errMsg,
        });
        await emit({ type: "error", message: errMsg });
        await emit({ type: "done", result: { status: "failed" } });
      } catch (cleanupErr) {
        console.error(
          `[workflow-run] cleanup failed for ${prepared.workflowRun.id}:`,
          cleanupErr,
        );
      }
    } finally {
      console.log(
        `[workflow-run.telemetry] ${JSON.stringify(runTelemetry.snapshot())}`,
      );
      unregisterBrokerEvents();
      try {
        await finalize();
      } catch (err) {
        console.error(
          `[workflow-run] finalize failed for ${prepared.workflowRun.id}:`,
          err,
        );
      }
      unregisterRun(prepared.workRunId);
    }
  })();

  return NextResponse.json({
    workflowRunId: prepared.workflowRun.id,
    workRunId: prepared.workRunId,
    threadId: prepared.threadId,
  });
}

async function persistManualWorkflowTelemetry(
  workflowRunId: string,
  emit: ReturnType<typeof createCoalescingEmit>["emit"],
  summary: HarnessRunSummary,
): Promise<void> {
  try {
    await emit({ type: "telemetry", summary });
  } catch {
    // Run telemetry must remain fail-open.
  }
  try {
    await db()
      .update(workflow_run)
      .set({ telemetry_summary: summary, updated_at: new Date() })
      .where(eq(workflow_run.id, workflowRunId));
  } catch {
    console.warn(
      `[workflow-run.telemetry] failed to persist summary ${workflowRunId}`,
    );
  }
}
