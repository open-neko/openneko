import { bindStartupRun, startupEvent, startupPhase, withStartupTrace } from "@neko/telemetry/startup";
import { ensureHostConfigProvisioned, registerAgentCanceller, type AgentEvent } from "@neko/llm";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { db, eq, pool, skill_usage } from "@neko/db";
import {
  agentRuntimeDepsFromConfig,
  appendWorkRunEvent,
  ensureAgentBroker,
  getWorkRun,
  registerAgentBrokerEventSink,
  runChatTurn,
  scrubAgentEvent,
  listPackActionDescriptors,
  type RunChannel,
} from "@neko/llm/work";
import {
  getCurrentScrubber,
  getPluginRegistryInstance,
} from "../plugins/registry-instance.js";
import { deliverChatReply } from "../channels/delivery.js";
import { includeRecordActionDescriptors } from "../records/adapters.js";
import {
  createWorkerHarnessObserver,
  persistProcessingJobTelemetry,
} from "../telemetry.js";
import { observeSafely } from "@neko/telemetry";
import { createRunSpendGuard } from "@neko/llm/spend";

export async function runWorkRun(jobId: string, orgId: string, payload: Parameters<typeof runWorkRunTraced>[2]): Promise<void> {
  return withStartupTrace({ runId: payload.runId, threadId: payload.threadId }, () => runWorkRunTraced(jobId, orgId, payload));
}

async function runWorkRunTraced(
  jobId: string,
  orgId: string,
  payload: {
    queuedAt?: number;
    runId: string;
    threadId: string;
    message: string;
    channel?: RunChannel;
    channelPlugin?: string;
    recipient?: Record<string, unknown>;
  },
): Promise<void> {
  const { runId, threadId, message, channel, channelPlugin, recipient } =
    payload;

  if (Number.isFinite(payload.queuedAt)) startupEvent("queue.wait", { durationMs: Math.max(0, Date.now() - payload.queuedAt!), execution: "worker_queue", basis: "since_enqueue_including_retries" });
  const run = await startupPhase("run.lookup", () => getWorkRun(orgId, runId));
  if (!run) {
    console.warn(
      `[work-run] run ${runId} not found for thread ${threadId}; skipping stale job`,
    );
    return;
  }
  if (run.status === "cancelled") {
    console.log(`[work-run] cancelled run ${runId} skipped before dispatch`);
    return;
  }
  const runTelemetry = createWorkerHarnessObserver(runId);
  const operationId = `work:${runId}`;
  const startedAt = Date.now();
  await observeSafely(runTelemetry.observer, {
    kind: "run.start",
    measurements: { coverage: "unavailable", ...(Number.isFinite(payload.queuedAt) ? { queueDurationMs: Math.max(0, Date.now() - payload.queuedAt!) } : {}) },
    operationId,
    attributes: {
      "openneko.run.kind": "production",
      "openneko.product.path": "work",
      "openneko.job.kind": "work_run",
      "openneko.delivery.channel": channel ?? "web",
    },
  });

  await bindStartupRun(runId, runTelemetry.observer);

  // Snapshot the scrubber once per run. fs.watch on the secrets file
  // rebuilds the registry's scrubber, so a future run picks up rotated
  // values; mid-run rotation is documented as out-of-scope.
  const scrubber = getCurrentScrubber();

  // A channel-initiated run has no SSE stream, so the agent's a2ui surface(s)
  // would be lost. Collect the (scrubbed) surface messages here so the reply
  // can carry them to channels that render rich content (e.g. Telegram).
  const surfaces: unknown[] = [];
  // The agent reliably emits a `vitals` fence (mandatory) but not always an a2ui
  // surface; collect the vitals so the reply can render them as cards on a
  // channel even when no surface was produced.
  let vitals: { label: string; value: string; sub?: string }[] = [];
  const emit = async (event: AgentEvent): Promise<void> => {
    const scrubbed = scrubAgentEvent(scrubber, event);
    if (scrubbed.type === "surface" && Array.isArray(scrubbed.messages)) {
      surfaces.push(...scrubbed.messages);
    }
    if (scrubbed.type === "vitals" && Array.isArray(scrubbed.items)) {
      vitals = scrubbed.items;
    }
    await appendWorkRunEvent({ orgId, threadId, runId, event: scrubbed });
  };

  let unregisterBrokerEvents = () => {};
  let result;
  try {
    const pluginActions = includeRecordActionDescriptors(
      getPluginRegistryInstance()?.getRegisteredActionDescriptors() ?? [],
    );
    const packActions = await startupPhase("context.pack_actions", () =>
      listPackActionDescriptors(orgId, { forHarness: run.backend === "harness" }));

    // Same gate the workflow job runs: if the boot-time provider sync lost a
    // race with a gateway restart, this is the retry — memoized on success, so
    // the healthy path costs nothing. Without it, channel-triggered runs
    // stayed broken until a settings save or a worker restart.
    const agentRuntime = await startupPhase("config.provision", () => ensureHostConfigProvisioned(orgId));

    const broker = await startupPhase("broker.ready", () => ensureAgentBroker());
    const cancellation = new AbortController();
    const unregisterShutdown = registerAgentCanceller(() => cancellation.abort());
    let polling = false;
    let watching = true;
    const checkCancellation = async () => {
      if (!watching || polling || cancellation.signal.aborted) return;
      polling = true;
      try {
        const { rows } = await pool().query<{ status: string }>(
          "select status from work_run where org_id=$1 and id=$2",
          [orgId, runId],
        );
        if (watching && (!rows[0] || rows[0].status === "cancelled")) {
          cancellation.abort();
        }
      } catch (error) {
        // Without the durable run state, an effect cannot safely publish.
        console.warn(
          `[work-run] cancellation check failed run=${runId}: ${error instanceof Error ? error.message : String(error)}`,
        );
        cancellation.abort(error);
      } finally {
        polling = false;
      }
    };
    const cancellationTimer = setInterval(() => { void checkCancellation(); }, 500);
    cancellationTimer.unref();
    try {
      await checkCancellation();
      const spendGuard = await createRunSpendGuard({ runId, emit, signal: cancellation.signal });
      try {
        unregisterBrokerEvents = registerAgentBrokerEventSink(runId, spendGuard.emit);
        result = await runChatTurn(
          {
            orgId,
            threadId,
            runId,
            message,
            channel,
            emit: spendGuard.emit,
            signal: spendGuard.signal,
            pluginActions,
            packActions,
            observer: runTelemetry.observer,
          },
          agentRuntimeDepsFromConfig(agentRuntime, broker),
        );
      } finally {
        spendGuard.dispose();
      }
    } finally {
      watching = false;
      clearInterval(cancellationTimer);
      unregisterShutdown();
    }
  } catch (cause) {
    if ((await getWorkRun(orgId, runId).catch(() => null))?.status === "cancelled") {
      await observeSafely(runTelemetry.observer, {
        kind: "run.end",
        operationId,
        status: "error",
        errorType: "cancelled",
        attributes: { "openneko.outcome": "cancelled" },
        measurements: { durationMs: Date.now() - startedAt, coverage: "unavailable" },
      });
      await persistProcessingJobTelemetry(jobId, runTelemetry.snapshot());
      console.log(`[work-run] cancelled run ${runId} stopped in worker`);
      return;
    }
    await observeSafely(runTelemetry.observer, {
      kind: "run.end",
      operationId,
      status: "error",
      errorType: cause instanceof Error ? cause.name : "unknown",
      attributes: { "openneko.outcome": "failed" },
      measurements: {
        durationMs: Date.now() - startedAt,
        coverage: "unavailable",
      },
    });
    const summary = runTelemetry.snapshot();
    try {
      await emit({ type: "telemetry", summary });
    } catch {
      // Summary persistence must not replace the original run failure.
    }
    await persistProcessingJobTelemetry(jobId, summary);
    console.log(`[work-run.telemetry] ${JSON.stringify(summary)}`);
    throw cause;
  } finally {
    unregisterBrokerEvents();
  }

  await observeSafely(runTelemetry.observer, {
    kind: "run.end",
    operationId,
    status:
      result.status === "completed" || result.status === "needs_input"
        ? "ok"
        : "error",
    ...(result.error ? { errorType: "work_run_error" } : {}),
    attributes: { "openneko.outcome": result.status },
    measurements: {
      durationMs: Date.now() - startedAt,
      coverage: "unavailable",
    },
  });
  const summary = runTelemetry.snapshot();
  try {
    await emit({ type: "telemetry", summary });
  } catch {
    // Telemetry must not change delivery or the work-run outcome.
  }
  await persistProcessingJobTelemetry(jobId, summary);
  console.log(`[work-run.telemetry] ${JSON.stringify(summary)}`);
  await enqueueSkillLearnForRun(orgId, runId);

  // Channel-initiated runs have no other return path — send the reply back to
  // the sender. Web runs (no channelPlugin) stream over SSE instead.
  if (
    channelPlugin &&
    recipient &&
    (result.status === "completed" || result.status === "needs_input")
  ) {
    await deliverChatReply(orgId, channelPlugin, recipient, runId, result.finalText, surfaces, vitals);
  }
}

async function enqueueSkillLearnForRun(orgId: string, runId: string): Promise<void> {
  try {
    const rows = await db()
      .select({ skillName: skill_usage.skill_name })
      .from(skill_usage)
      .where(eq(skill_usage.run_id, runId));
    for (const row of rows) {
      await enqueue(
        QUEUE.SKILL_LEARN,
        { orgId, skillName: row.skillName },
        { singletonKey: `skill-learn:${orgId}:${row.skillName}` },
      );
    }
  } catch (error) {
    console.warn(
      `[work-run] skill learn enqueue failed for ${runId}: ${
        error instanceof Error ? error.message : error
      }`,
    );
  }
}
