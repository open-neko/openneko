import { NextRequest, NextResponse } from "next/server";
import {
  resolveAgentBackend,
  ensureHostConfigProvisioned,
} from "@neko/llm";
import {
  agentRuntimeDepsFromConfig,
  createWorkRun,
  ensureAgentBroker,
  finishWorkRun,
  getWorkRun,
  registerAgentBrokerEventSink,
  runChatTurn,
} from "@neko/llm/work";
import { getCurrentActor } from "@/lib/actor";
import { getPluginActionDescriptors } from "@/lib/auth";
import { createCoalescingEmit } from "@/lib/coalescing-emit";
import { getOrgId } from "@/lib/db";
import {
  registerRun,
  unregisterRun,
} from "@/lib/neko-run-registry";
import {
  createWorkMessage,
  setWorkThreadBackendState,
  suggestWorkThreadTitle,
  touchWorkThread,
} from "@/lib/work-store";
import { parseAppWorkContext, parseRecordWorkContext } from "@neko/llm/work";
import {
  AppChatContextError,
  resolveAppChatContext,
} from "@/lib/app-chat-context";
import { getAuthorizedWorkThread } from "@/lib/work-thread-auth";
import { recordsApiError } from "@/lib/records-api";
import { createWebHarnessObserver } from "@/lib/telemetry";
import { observeSafely, type HarnessRunSummary } from "@neko/telemetry";

type RouteContext = {
  params: Promise<{ threadId: string }>;
};

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function POST(request: NextRequest, context: RouteContext) {
  const { threadId } = await context.params;
  const body = await request.json().catch(() => ({}));
  const message = typeof body.message === "string" ? body.message.trim() : "";
  if (!message) {
    return NextResponse.json({ error: "message is required" }, { status: 400 });
  }

  const orgId = await getOrgId();
  const actor = await getCurrentActor();
  const thread = await getAuthorizedWorkThread(orgId, threadId, actor);
  if (!thread) {
    return NextResponse.json({ error: "Thread not found" }, { status: 404 });
  }

  const backendState = thread.backend_state && typeof thread.backend_state === "object"
    && !Array.isArray(thread.backend_state)
    ? thread.backend_state as Record<string, unknown>
    : {};
  const appContext = parseAppWorkContext(backendState.appContext);
  const legacyRecordContext = parseRecordWorkContext(backendState.recordContext);
  if (body.recordContext !== undefined) {
    const owningAppId = appContext?.appId ?? legacyRecordContext?.appId;
    if (!owningAppId) {
      return NextResponse.json(
        { error: "Page context is only accepted by app chat" },
        { status: 400 },
      );
    }
    try {
      const resolved = await resolveAppChatContext({
        orgId,
        appId: owningAppId,
        actorRole: actor.role,
        recordContext: body.recordContext,
      });
      const nextState = { ...backendState };
      nextState.appContext = resolved.appContext;
      if (resolved.recordContext) nextState.recordContext = resolved.recordContext;
      else delete nextState.recordContext;
      await setWorkThreadBackendState(threadId, nextState);
    } catch (error) {
      if (error instanceof AppChatContextError) {
        return NextResponse.json({ error: error.message }, { status: 400 });
      }
      return recordsApiError(error);
    }
  }

  // Memory writes are agent-driven through the brokered memory MCP tool.
  const backend = await resolveAgentBackend(orgId);

  // Derive the agent-sandbox env (model egress, gateway provider, key alias)
  // before creating the run. If gateway sync is temporarily unavailable, the
  // memoized provision attempt is cleared and the caller can retry cleanly.
  const agentRuntime = await ensureHostConfigProvisioned(orgId);

  const run = await createWorkRun(orgId, threadId, backend.id, actor);
  const runTelemetry = createWebHarnessObserver(run.id);
  const telemetryOperationId = `work:${run.id}`;
  const telemetryStartedAt = Date.now();
  await observeSafely(runTelemetry.observer, {
    kind: "run.start",
    operationId: telemetryOperationId,
    attributes: {
      "openneko.run.kind": "production",
      "openneko.product.path": "work",
      "openneko.job.kind": "work_run",
      "openneko.delivery.channel": "web",
      "openneko.backend": backend.id,
    },
  });

  if (!thread.title) {
    await touchWorkThread(threadId, { title: suggestWorkThreadTitle(message) });
  }
  await createWorkMessage({
    orgId,
    threadId,
    runId: run.id,
    role: "user",
    content: message,
  });

  const abortController = new AbortController();
  registerRun({
    runId: run.id,
    threadId,
    orgId,
    abortController,
    subscribers: new Set(),
  });

  const { emit, finalize } = createCoalescingEmit({
    orgId,
    threadId,
    runId: run.id,
  });

  const pluginActions = await getPluginActionDescriptors();

  // The agent loop runs in an OpenShell sandbox (SEC9: the only runtime).
  // The web server stays the control plane, launches the box, and relays
  // events over the existing SSE.
  const broker = await ensureAgentBroker();
  const unregisterBrokerEvents = registerAgentBrokerEventSink(run.id, emit);

  void runChatTurn(
    {
      orgId,
      threadId,
      runId: run.id,
      message,
      channel: "web",
      emit,
      signal: abortController.signal,
      pluginActions,
      observer: runTelemetry.observer,
    },
    agentRuntimeDepsFromConfig(agentRuntime, broker),
  )
    .then(async (result) => {
      await observeSafely(runTelemetry.observer, {
        kind: "run.end",
        operationId: telemetryOperationId,
        status:
          result.status === "completed" || result.status === "needs_input"
            ? "ok"
            : "error",
        ...(result.error ? { errorType: "work_run_error" } : {}),
        attributes: { "openneko.outcome": result.status },
        measurements: {
          durationMs: Date.now() - telemetryStartedAt,
          coverage: "unavailable",
        },
      });
      await emitTelemetrySafely(emit, runTelemetry.snapshot());
    })
    .catch(async (err) => {
      await observeSafely(runTelemetry.observer, {
        kind: "run.end",
        operationId: telemetryOperationId,
        status: "error",
        errorType: err instanceof Error ? err.name : "unknown",
        attributes: { "openneko.outcome": "failed" },
        measurements: {
          durationMs: Date.now() - telemetryStartedAt,
          coverage: "unavailable",
        },
      });
      await emitTelemetrySafely(emit, runTelemetry.snapshot());
      console.error(`[work-run/inproc] run ${run.id} threw:`, err);
      try {
        const current = await getWorkRun(orgId, run.id);
        const terminal =
          current?.status === "completed" ||
          current?.status === "failed" ||
          current?.status === "cancelled" ||
          current?.status === "needs_input";
        if (terminal) return;

        const errMsg = err instanceof Error ? err.message : String(err);
        await emit({ type: "error", message: errMsg });
        await emit({ type: "done", result: { status: "failed" } });
        await finishWorkRun(run.id, "failed", errMsg);
      } catch (cleanupErr) {
        console.error(
          `[work-run/inproc] cleanup failed for ${run.id}:`,
          cleanupErr,
        );
      }
    })
    .finally(async () => {
      console.log(
        `[work-run.telemetry] ${JSON.stringify(runTelemetry.snapshot())}`,
      );
      unregisterBrokerEvents();
      try {
        await finalize();
      } catch (err) {
        console.error(
          `[work-run/inproc] finalize failed for ${run.id}:`,
          err,
        );
      }
      unregisterRun(run.id);
    });

  return NextResponse.json({
    runId: run.id,
    threadId,
    backend: backend.id,
    actorRole: actor.role,
  });
}

async function emitTelemetrySafely(
  emit: ReturnType<typeof createCoalescingEmit>["emit"],
  summary: HarnessRunSummary,
): Promise<void> {
  try {
    await emit({ type: "telemetry", summary });
  } catch {
    // Summary persistence must not change the work-run outcome.
  }
}
