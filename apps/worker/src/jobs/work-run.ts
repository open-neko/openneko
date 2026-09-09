import { ensureHostConfigProvisioned, type AgentEvent } from "@neko/llm";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { db, eq, skill_usage } from "@neko/db";
import {
  agentRuntimeDepsFromConfig,
  appendWorkRunEvent,
  ensureAgentBroker,
  getWorkRun,
  registerAgentBrokerEventSink,
  runChatTurn,
  scrubAgentEvent,
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

export async function runWorkRun(
  jobId: string,
  orgId: string,
  payload: {
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

  const run = await getWorkRun(orgId, runId);
  if (!run) {
    console.warn(
      `[work-run] run ${runId} not found for thread ${threadId}; skipping stale job`,
    );
    return;
  }
  const runTelemetry = createWorkerHarnessObserver(runId);
  const operationId = `work:${runId}`;
  const startedAt = Date.now();
  await observeSafely(runTelemetry.observer, {
    kind: "run.start",
    operationId,
    attributes: {
      "openneko.run.kind": "production",
      "openneko.product.path": "work",
      "openneko.job.kind": "work_run",
      "openneko.delivery.channel": channel ?? "web",
    },
  });

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

  const pluginActions = includeRecordActionDescriptors(
    getPluginRegistryInstance()?.getRegisteredActionDescriptors() ?? [],
  );

  // Same gate the workflow job runs: if the boot-time provider sync lost a
  // race with a gateway restart, this is the retry — memoized on success, so
  // the healthy path costs nothing. Without it, channel-triggered runs
  // stayed broken until a settings save or a worker restart.
  const agentRuntime = await ensureHostConfigProvisioned(orgId);

  const broker = await ensureAgentBroker();
  const unregisterBrokerEvents = registerAgentBrokerEventSink(runId, emit);
  let result;
  try {
    result = await runChatTurn(
      {
        orgId,
        threadId,
        runId,
        message,
        channel,
        emit,
        pluginActions,
        observer: runTelemetry.observer,
      },
      agentRuntimeDepsFromConfig(agentRuntime, broker),
    );
  } catch (cause) {
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
